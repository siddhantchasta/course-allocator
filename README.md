# Concurrent Course Allocator: High-Concurrency Registration Engine

**Concurrent Course Allocator** is a high-performance distributed backend microservice in Go designed to handle extreme concurrency (10,000+ req/sec) during university registration periods. The system demonstrates distributed race condition prevention, eventual consistency write-back, dynamic credit pricing, and anti-bot rate limiting.

---

## 🌟 Key Architectural Features

1. **Atomic Concurrency Control (Redis + Lua):** Bypasses vulnerable database transactions by executing the check-and-decrement critical path in memory via atomic Redis Lua scripts, guaranteeing **zero oversold seats** under extreme load.
2. **Eventual Consistency Pipeline (RabbitMQ):** Decouples in-memory seat reservations from durable database persistence. An asynchronous background Go worker drains reservation events from RabbitMQ and persists them to PostgreSQL without blocking the low-latency hot path.
3. **Anti-Bot Rate Limiter (Token Bucket):** Distributed Token Bucket algorithm in Redis Lua + Gin middleware that intercepts high-volume bot attacks (HTTP 429) at line-rate (~9,500 req/sec) with zero database load.
4. **Dynamic Scarcity Pricing Engine:** Unit-tested pricing engine that reads real-time cache inventory and scales course credit requirements when available seats drop below 50% capacity.

---

## 🏗️ Architecture & Tech Stack

* **Language:** Go (Golang 1.25)
* **Web Framework:** Gin
* **Primary Database:** PostgreSQL 15 (Durable System of Record)
* **In-Memory Cache & Locking:** Redis 7 + Lua Scripting (Atomic Counters & Rate Limiting)
* **Message Broker:** RabbitMQ 3 (Asynchronous Write-Behind Queue)
* **Load Testing:** K6 (Distributed Concurrency Simulation)
* **Infrastructure:** Docker & Docker Compose

---

## 📂 Project Structure

Following the **Standard Go Project Layout** for maintainability, domain isolation, and scalability:

```text
course-allocator/
├── cmd/
│   └── server/
│       └── main.go           # Application entry point (Wires DB, Redis, RabbitMQ, Gin Routes)
├── internal/
│   ├── handlers/             # HTTP Controllers (Registration & Reset endpoints)
│   ├── middleware/           # Security Layer: Distributed Token Bucket Rate Limiter
│   ├── pricing/              # Domain Layer: Scarcity Dynamic Pricing Engine & Unit Tests
│   │   ├── pricing.go
│   │   └── pricing_test.go
│   └── repository/           # Data Layer: Postgres Queries, Redis Lua, RabbitMQ Consumer
│       └── store.go
├── docker-compose.yml        # Multi-container infrastructure (Postgres, Redis, RabbitMQ)
├── attack.js                 # K6 concurrency attack script
└── go.mod                    # Go dependencies
```

---

## 📊 Benchmark & Concurrency Comparison

All benchmarks were captured using **K6** with **10 concurrent Virtual Users (VUs)** executing tight request loops over a **5-second burst**:

| Benchmark Metric | Phase 1: Vulnerable (`/register/vulnerable`) | Phase 2: Atomic (`/register/atomic`) | Phase 3: Anti-Bot (`/register/rate-limited`) |
| :--- | :---: | :---: | :---: |
| **Throughput** | **5,849 req/sec** | **7,473 req/sec** | **10,093 req/sec** |
| **Total Requests Processed** | 29,257 requests | 37,372 requests | 50,656 requests |
| **Successful Allocations (200 OK)** | **108** | **100** | **10** |
| **Course Capacity Limit** | 100 seats | 100 seats | 100 seats |
| **Oversold Seats (Anomaly)** | **+8 seats (+8% Overbooked)** | **0 seats (Exact Match)** | **0 (Protected)** |
| **Failed / Course Full (410)** | 29,149 | 37,272 | 0 |
| **Throttled by Rate Limiter (429)** | 0 (No Protection) | 0 (Benchmark Mode) | **50,646 (99.98% Dropped)** |
| **Average Latency** | 1.63 ms | 1.28 ms | **935 µs (< 1 ms)** |
| **p95 Latency** | 3.41 ms | 1.87 ms | 1.60 ms |
| **Data Integrity Outcome** | ❌ **Corrupted (`seats = -8`)** | ✅ **100% Consistent (`seats = 0`)** | 🛡️ **Bot Blocked (90 seats left)** |

---

## 🔄 Architectural Workflow

```text
[Incoming Student / Bot Traffic]
               │
               ▼
   [Gin HTTP Router (Port 9090)]
               │
   ┌───────────┴───────────────────────────────────────────┐
   │                                                       │
   ▼                                                       ▼
[/register/atomic]                             [/register/rate-limited]
   │                                                       │
   │                                           [Token Bucket Middleware]
   │                                              • Capacity: 5
   │                                              • Refill: 1/sec
   │                                              • Result: HTTP 429 Dropped
   │                                                       │ (Tokens granted)
   └───────────────────────────┬───────────────────────────┘
                               │
                               ▼
                [Redis Lua Check-and-Decrement]
                     • Atomic in RAM
                     • 0 Overselling Guarantee
                               │
                               ├──────────────────────┐
                               ▼                      ▼
                     [HTTP 200 OK Response]    [Publish AMQP Event]
                     (Sub-millisecond)                │
                                                      ▼
                                            [RabbitMQ Queue]
                                            (seat_decrements)
                                                      │
                                                      ▼
                                            [Async Go Worker]
                                                      │
                                                      ▼
                                            [PostgreSQL Update]
                                            (Eventual Consistency)
```

---

## Phase 1: The Vulnerable Implementation & Exploit

The initial implementation demonstrates the classic **Read → Check → Modify** race condition using direct PostgreSQL transactions:

```go
// 1. READ current seats
SELECT seats FROM courses WHERE id = 1;

// 2. CHECK availability in Go application layer
if seats <= 0 { return false }

// 3. MODIFY seats
UPDATE courses SET seats = seats - 1 WHERE id = 1;
```

### The Exploit Result: Data Corruption (Overselling)

Under heavy K6 concurrency, multiple goroutines read the same seat count at the exact same microsecond before any transaction commits the decrement to disk.

* **Total Requests Processed:** 29,257 requests (5,849 req/sec)
* **Successful Allocations:** **108** (for a 100-seat course)
* **Oversold Anomaly:** **+8 seats (8% overbooking)**
* **Database State:** `SELECT seats FROM courses;` returns `-8`.

![K6 vulnerable endpoint results](assets/Vulnerable.png)

---

## Phase 2: The Solution (Redis Lua + RabbitMQ Eventual Consistency)

To eliminate the race condition and remove the database I/O bottleneck from the hot path:

### 1. In-Memory Atomic Reservation
The critical section is moved to a single-threaded **Redis Lua script**:

```lua
local seats = tonumber(redis.call("GET", KEYS[1]))

if seats > 0 then
    redis.call("DECR", KEYS[1])
    return 1 -- Seat allocated successfully
else
    return 0 -- Course Full
end
```

### 2. Asynchronous Write-Behind via RabbitMQ
* When Redis confirms an allocation, the service publishes a lightweight event to the RabbitMQ `seat_decrements` queue.
* A decoupled Go background consumer (`repo.ConsumeSeatDecrements`) drains the queue and persists updates to PostgreSQL with `UPDATE courses SET seats = seats - 1 WHERE id = 1 AND seats > 0`.
* **Result:** The user gets a sub-millisecond confirmation, while PostgreSQL stays eventually consistent without slowing down registration throughput.

### The Atomic Result: Perfect Consistency

* **Total Requests Processed:** 37,372 requests (7,473 req/sec)
* **Successful Allocations:** **Exactly 100** (100% capacity)
* **Oversold Seats:** **0 (Zero Anomaly)**
* **Subsequent Requests:** Gracefully rejected with `410 Course registration is closed (Full)!`

![K6 atomic endpoint results](assets/Lua.png)

---

## Phase 3: Anti-Bot Rate Limiting (Distributed Token Bucket)

To prevent automated bot scripts from hoarding seat inventory from a single IP, a distributed **Token Bucket algorithm** was implemented using Gin middleware and Redis Lua.

* **Configuration:** Capacity = 5 tokens (burst allowance), Refill Rate = 1 token/sec.
* **Fail-Open Strategy:** If Redis is temporarily unreachable, the middleware fails open to keep registration alive rather than hard-dropping legitimate users.

### The Rate Limiting Result: Sub-Millisecond Defense

When an aggressive bot script hammers the endpoint:
* **Total Requests Processed:** 50,656 requests (10,093 req/sec)
* **Allowed Requests:** **10** (5 initial burst + 5 refilled tokens)
* **Blocked Requests:** **50,646 requests (99.98%)** dropped at the door with `HTTP 429 Too Many Requests`.
* **Average Response Time:** **935 µs** with **zero database load**.

![Rate-limited server responses](assets/Middleware-429.png)

---

## Phase 4: Dynamic Scarcity Pricing Engine

An inventory-aware pricing algorithm in `internal/pricing` recalculates course credit costs in real-time as seats become scarce:

* **Base Cost:** 10 credits (when $>50\%$ seats remain).
* **Surge Tiering:** When available seats drop $\le 50\%$, the price dynamically increases by $+1$ credit for every 10 seats sold, capped at a maximum of 20 credits.
* **Unit Test Coverage:** Fully verified across 8 boundary test cases in `internal/pricing/pricing_test.go`.

---

## 🚀 Getting Started

### 1. Start Multi-Container Infrastructure

Ensure Docker is running, then boot PostgreSQL, Redis, and RabbitMQ:

```bash
docker-compose up -d
```

### 2. Boot the Go Microservice

```bash
go run cmd/server/main.go
```

The server initializes on port `:9090` and starts the background RabbitMQ consumer.

### 3. Run Load Tests & Verify

Reset course inventory to 100:
```bash
curl -X POST http://localhost:9090/reset
```

Run K6 concurrency attack scenarios:

```bash
# Scenario A: Atomic Concurrency Benchmark (Phase 2)
k6 run attack.js

# Scenario B: Anti-Bot Rate Limiting Demo (Phase 3)
k6 run attack.js -e TARGET_URL=http://127.0.0.1:9090/register/rate-limited

# Scenario C: Vulnerable Race Condition Demo (Phase 1)
k6 run attack.js -e TARGET_URL=http://127.0.0.1:9090/register/vulnerable
```

---

## 💡 Key Design Decisions & Interview Highlights

* **Redis Lua vs. Database Row Locking (`SELECT ... FOR UPDATE`):**
  * *Database Locking (Rejected):* Locks database rows on disk, creating extreme thread contention and slashing throughput.
  * *Redis Lua (Chosen):* Executes atomically in single-threaded RAM, delivering 6,000–11,000+ req/sec.
* **RabbitMQ Write-Behind vs. Synchronous Dual-Write:**
  * *Dual-Write (Rejected):* Writing synchronously to Redis and PostgreSQL within the HTTP handler drags response times down to disk write speeds and risks partial write failures.
  * *RabbitMQ Eventual Consistency (Chosen):* Buffers persistence writes, protecting PostgreSQL from connection pool exhaustion during traffic surges.
* **Connection Pooling:** Go connection pool configured with `db.SetMaxOpenConns(25)` and `db.SetMaxIdleConns(25)` to prevent `pq: sorry, too many clients` crashes.


