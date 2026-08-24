package repository

import (
	"context"
	"database/sql"
	"log"
	"strconv"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

const seatDecrementQueue = "seat_decrements"

type Repo struct {
	db   *sql.DB
	rdb  *redis.Client
	mq   *amqp.Channel
	mqMu sync.Mutex
	ctx  context.Context
}

func NewRepo(db *sql.DB, rdb *redis.Client, mq *amqp.Channel) *Repo {
	return &Repo{
		db:  db,
		rdb: rdb,
		mq:  mq,
		ctx: context.Background(),
	}
}

// SetupDatabase ensures the Postgres table exists for the academic domain.
func (r *Repo) SetupDatabase() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS courses (
			id SERIAL PRIMARY KEY,
			seats INT
		)
	`)
	return err
}

// ResetCourses resets both PostgreSQL and Redis to 100 seats.
func (r *Repo) ResetCourses() error {
	// Ensure table exists in case DB was freshly launched
	if err := r.SetupDatabase(); err != nil {
		return err
	}

	// Reset PostgreSQL state of truth.
	if _, err := r.db.Exec("TRUNCATE courses"); err != nil {
		return err
	}

	if _, err := r.db.Exec(
		"INSERT INTO courses (id, seats) VALUES (1, 100)",
	); err != nil {
		return err
	}

	// Purge pending decrement messages from RabbitMQ queue if connected
	if r.mq != nil {
		r.mqMu.Lock()
		_, _ = r.mq.QueuePurge(seatDecrementQueue, false)
		r.mqMu.Unlock()
	}

	// Reset Rate Limiter keys if any exist
	_ = r.rdb.Del(r.ctx, "rate_limit:127.0.0.1").Err()

	// Reset Redis high-speed cache.
	return r.rdb.Set(
		r.ctx,
		"course_seats",
		100,
		0,
	).Err()
}

// GetAvailableSeats retrieves the current seat count from Redis for dynamic pricing.
func (r *Repo) GetAvailableSeats() (int, error) {
	val, err := r.rdb.Get(r.ctx, "course_seats").Result()
	if err != nil {
		if err == redis.Nil {
			return 0, nil // Key doesn't exist
		}
		return 0, err
	}
	return strconv.Atoi(val)
}

// RegisterCourseVulnerable intentionally demonstrates the
// Read → Check → Modify race condition in PostgreSQL.
// Multiple concurrent requests can read the same seat count
// before another request updates it.
func (r *Repo) RegisterCourseVulnerable() (bool, error) {
	var seats int

	// 1. READ current seats.
	err := r.db.QueryRow(
		"SELECT seats FROM courses WHERE id = 1",
	).Scan(&seats)

	if err != nil {
		return false, err
	}

	// 2. CHECK whether seats are available.
	if seats <= 0 {
		return false, nil
	}

	// 3. MODIFY the seats.
	_, err = r.db.Exec(
		"UPDATE courses SET seats = seats - 1 WHERE id = 1",
	)

	if err != nil {
		return false, err
	}

	return true, nil
}

// RegisterCourseAtomic atomically checks and decrements seat availability
// using a Redis Lua script to prevent concurrent race conditions. Redis
// remains the source of truth for live availability (it's what gates the
// registration itself); on success it also fires an async event so Postgres
// gets written back without adding a synchronous DB round-trip to the hot
// path.
func (r *Repo) RegisterCourseAtomic() (bool, error) {
	luaScript := `
		local seats = tonumber(redis.call("GET", KEYS[1]))

		if not seats then
			return -1
		end

		if seats > 0 then
			redis.call("DECR", KEYS[1])
			return 1
		else
			return 0
		end
	`

	result, err := r.rdb.Eval(
		r.ctx,
		luaScript,
		[]string{"course_seats"},
	).Int()

	if err != nil {
		return false, err
	}

	if result == -1 {
		// Key missing or uninitialized in Redis, treat safely as course registration closed
		return false, nil
	}

	success := result == 1
	if success {
		r.publishSeatDecrement()
	}

	return success, nil
}

// publishSeatDecrement notifies the async consumer to persist the decrement
// to Postgres. Best-effort and non-blocking: a publish failure here does not
// fail the registration (Redis already committed it) — it only means
// Postgres lags until the next successful publish or a reconciliation pass.
func (r *Repo) publishSeatDecrement() {
	if r.mq == nil {
		return
	}
	r.mqMu.Lock()
	defer r.mqMu.Unlock()

	err := r.mq.PublishWithContext(
		r.ctx,
		"",                 // default exchange
		seatDecrementQueue, // routing key = queue name
		false,              // mandatory
		false,              // immediate
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        []byte("1"),
		},
	)
	if err != nil {
		log.Printf("warning: failed to publish seat decrement event: %v", err)
	}
}

// ConsumeSeatDecrements runs a blocking consumer loop that applies each
// decrement event to Postgres, keeping it eventually consistent with Redis.
// Call this in its own goroutine from main.
func (r *Repo) ConsumeSeatDecrements() {
	if r.mq == nil {
		log.Println("seat decrement consumer not started: RabbitMQ unavailable")
		return
	}

	msgs, err := r.mq.Consume(
		seatDecrementQueue,
		"",    // consumer tag
		true,  // auto-ack — losing an occasional event only delays consistency, acceptable here
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,
	)
	if err != nil {
		log.Printf("warning: failed to start seat decrement consumer: %v", err)
		return
	}

	for range msgs {
		// The seats > 0 guard stops Postgres from going negative if an
		// event is ever delivered more than once.
		if _, err := r.db.Exec(
			"UPDATE courses SET seats = seats - 1 WHERE id = 1 AND seats > 0",
		); err != nil {
			log.Printf("warning: failed to persist seat decrement to postgres: %v", err)
		}
	}
}
