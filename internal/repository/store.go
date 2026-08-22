package repository

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/redis/go-redis/v9"
)

type Repo struct {
	db  *sql.DB
	rdb *redis.Client
	ctx context.Context
}

// NewRepo creates a new repository instance.
func NewRepo(db *sql.DB, rdb *redis.Client) *Repo {
	return &Repo{
		db:  db,
		rdb: rdb,
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
	// Reset PostgreSQL state of truth.
	if _, err := r.db.Exec("TRUNCATE courses"); err != nil {
		return err
	}

	if _, err := r.db.Exec(
		"INSERT INTO courses (id, seats) VALUES (1, 100)",
	); err != nil {
		return err
	}

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
// using a Redis Lua script to prevent distributed race conditions.
func (r *Repo) RegisterCourseAtomic() (bool, error) {
	luaScript := `
		local seats = tonumber(redis.call("GET", KEYS[1]))

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

	return result == 1, nil
}
