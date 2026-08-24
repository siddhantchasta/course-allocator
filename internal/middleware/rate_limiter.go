package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type RateLimiter struct {
	rdb *redis.Client
}

func NewRateLimiter(rdb *redis.Client) *RateLimiter {
	return &RateLimiter{rdb: rdb}
}

// TokenBucketMiddleware intercepts requests and runs the Lua rate limiting script
func (rl *RateLimiter) TokenBucketMiddleware() gin.HandlerFunc {
	// The Lua script for atomic Token Bucket calculation
	luaScript := `
		local key = KEYS[1]
		local capacity = tonumber(ARGV[1])
		local refill_rate_per_sec = tonumber(ARGV[2])
		local current_time = tonumber(ARGV[3])

		-- Fetch current state (tokens and last refresh time)
		local last_tokens = tonumber(redis.call("HGET", key, "tokens")) or capacity
		local last_refreshed = tonumber(redis.call("HGET", key, "timestamp")) or current_time

		-- Calculate tokens to add based on elapsed time
		local time_passed = math.max(0, current_time - last_refreshed)
		local new_tokens = math.min(capacity, last_tokens + (time_passed * refill_rate_per_sec))

		-- Check if the request can be processed
		if new_tokens >= 1 then
			-- Decrement token, update state, and set a TTL (Time To Live) to clean up old keys
			redis.call("HSET", key, "tokens", new_tokens - 1)
			redis.call("HSET", key, "timestamp", current_time)
			redis.call("EXPIRE", key, 10) -- Key expires in 10 seconds if inactive
			return 1 -- Allowed
		else
			return 0 -- Denied (Rate limited)
		end
	`

	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		redisKey := "rate_limit:" + clientIP

		// Configuration: Allow a burst of 5 requests, refilling at 1 request per second
		// (Demonstrates Token Bucket rate-limiting under high-concurrency bot spikes)
		capacity := 5
		refillRate := 1
		currentTime := time.Now().Unix()

		// Execute the script atomically in Redis
		result, err := rl.rdb.Eval(
			context.Background(),
			luaScript,
			[]string{redisKey},
			capacity,
			refillRate,
			currentTime,
		).Int()

		if err != nil {
			// Fail-open strategy: If Redis is down temporarily, allow the request to pass
			// rather than crashing the entire registration system.
			c.Next()
			return
		}

		if result == 1 {
			// Token granted, proceed to the registration handler
			c.Next()
		} else {
			// Bucket is empty, block the request
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many registration attempts. Please slow down.",
			})
		}
	}
}
