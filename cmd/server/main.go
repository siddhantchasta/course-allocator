package main

import (
	"database/sql"
	"fmt"
	"log"

	"course-allocator/internal/handlers"
	"course-allocator/internal/middleware"
	"course-allocator/internal/repository"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

const (
	host     = "localhost"
	port     = 5435
	user     = "siddhantchasta"
	password = "postgres"
	dbname   = "courseallocator"
)

func main() {
	// 1. Connect to Postgres (Durable ledger — eventually consistent with
	// Redis via the async consumer below, not the live gate on registration)
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	db, err := sql.Open("postgres", psqlInfo)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Connection Pooling (Application-layer throttling)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(0)

	// 2. Connect to Redis (High-speed Cache & Atomic Locks)
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	// 3. Connect to RabbitMQ (Async write-back to Postgres).
	// If it's unreachable, the server still starts: Redis stays
	// authoritative for live seat availability, Postgres just falls
	// behind until RabbitMQ comes back.
	var mqChannel *amqp.Channel
	mqConn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	if err != nil {
		log.Printf("warning: RabbitMQ unavailable, Postgres write-back disabled: %v", err)
	} else {
		defer mqConn.Close()
		ch, chErr := mqConn.Channel()
		if chErr != nil {
			log.Printf("warning: failed to open RabbitMQ channel: %v", chErr)
		} else {
			defer ch.Close()
			_, qErr := ch.QueueDeclare(
				"seat_decrements",
				true,  // durable
				false, // auto-delete
				false, // exclusive
				false, // no-wait
				nil,
			)
			if qErr != nil {
				log.Printf("warning: failed to declare seat_decrements queue: %v", qErr)
			} else {
				mqChannel = ch
			}
		}
	}

	// 4. Initialize Layers (Dependency Injection)
	repo := repository.NewRepo(db, rdb, mqChannel)
	courseHandler := handlers.NewCourseHandler(repo)
	rateLimiter := middleware.NewRateLimiter(rdb)

	if err := repo.SetupDatabase(); err != nil {
		log.Fatal("Failed to setup database:", err)
	}

	// Async consumer: persists Redis seat decrements to Postgres so it
	// doesn't stay permanently stale once /register/atomic starts serving.
	go repo.ConsumeSeatDecrements()

	fmt.Println("🚀 Concurrent Course Allocator System Initialized (Postgres + Redis + RabbitMQ)")

	// 5. Setup Router
	r := gin.Default()

	// Reset endpoint (Unprotected)
	r.POST("/reset", courseHandler.ResetCourses)

	// Phase 1: The Vulnerable Endpoint (NO Rate Limiting)
	// Demonstrates Postgres Read-Check-Modify race condition (+9 oversold anomaly)
	r.POST("/register/vulnerable", courseHandler.RegisterVulnerable)

	// Phase 2: The Atomic Endpoint (Redis Lua + RabbitMQ Eventual Consistency)
	// Demonstrates high-throughput atomic seat allocation (100 seats allocated, 0 oversold)
	r.POST("/register/atomic", courseHandler.RegisterCourse)

	// Phase 3: The Rate-Limited Endpoint (Protected by Redis Token Bucket Lua Script)
	// Intercepts bot/abusive traffic spikes with HTTP 429 Too Many Requests
	protectedRoute := r.Group("/")
	protectedRoute.Use(rateLimiter.TokenBucketMiddleware())
	protectedRoute.POST("/register/rate-limited", courseHandler.RegisterCourse)

	// 6. Run Server
	if err := r.Run(":9090"); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
