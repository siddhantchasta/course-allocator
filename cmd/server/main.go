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
	// 1. Connect to Postgres (State of Truth)
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

	// 2. Connect to Redis (High-speed Cache & Distributed Locks)
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	// 3. Initialize Layers (Dependency Injection)
	repo := repository.NewRepo(db, rdb)
	courseHandler := handlers.NewCourseHandler(repo)
	rateLimiter := middleware.NewRateLimiter(rdb)

	if err := repo.SetupDatabase(); err != nil {
		log.Fatal("Failed to setup database:", err)
	}

	fmt.Println("🚀 Concurrent Course Allocator System Initialized (Postgres + Redis)")

	// 4. Setup Router
	r := gin.Default()

	// Reset endpoint (Unprotected)
	r.POST("/reset", courseHandler.ResetCourses)

	// Phase 1: The Vulnerable Endpoint (NO Rate Limiting)
	// This must remain unprotected so K6 can bombard the database with concurrent requests
	r.POST("/register/vulnerable", courseHandler.RegisterVulnerable)

	// Phase 2: The Atomic Endpoint (Protected by Redis Token Bucket Lua Script)
	atomicRoute := r.Group("/")
	atomicRoute.Use(rateLimiter.TokenBucketMiddleware())
	atomicRoute.POST("/register/atomic", courseHandler.RegisterCourse)

	// 5. Run Server
	if err := r.Run(":9090"); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
