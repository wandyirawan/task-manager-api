# Task Manager API

A multi-user task management REST API built with [Go](https://go.dev/) and [Fiber v3](https://gofiber.io/). Provides JWT authentication (register/login), `/tasks` CRUD with status filtering, title search, and pagination, **idempotent** task creation (`Idempotency-Key`), assignment with an audit trail in a single transaction, and a **Swagger UI** for interactive docs. Backed by **PostgreSQL** — runs fully via Docker Compose.

## Tech Stack

| Layer           | Technology                                                                     |
|-----------------|--------------------------------------------------------------------------------|
| Web framework   | [Fiber v3](https://gofiber.io/)                                               |
| Database        | [PostgreSQL 16](https://www.postgresql.org/) via `jackc/pgx/v5` (stdlib driver) |
| Query / SQL     | [jmoiron/sqlx](https://github.com/jmoiron/sqlx)                               |
| Migrations      | [golang-migrate/migrate](https://github.com/golang-migrate/migrate) (embedded, runs automatically at boot) |
| Logging         | `log/slog` (structured JSON → stdout)                                         |
| Auth            | [golang-jwt/jwt v5](https://github.com/golang-jwt/jwt) + `golang.org/x/crypto/bcrypt` (hand-rolled middleware) |
| Swagger         | [swaggo](https://github.com/swaggo/swag) + `gofiber/contrib/v3/swaggo`        |
| Env parsing     | `caarlos0/env/v11`                                                            |
| Testing         | `testing` stdlib + end-to-end race suite (real HTTP + real PostgreSQL)        |

## Requirements

- Go **1.27+**
- **Docker + Docker Compose** (for PostgreSQL and/or the full stack)
- `make` (optional — can be replaced with `go run` / `go test` directly)
- `migrate` CLI (optional — only for manual migrations against an external DB; the binary auto-migrates at boot)

Or just run once:

```bash
make setup   # checks go + docker, installs the migrate CLI if missing
```

### Development Workflow

`make` (no arguments) prints this guide. Daily working order:

```bash
# Once (setup):
make setup                            # checks tooling, installs migrate CLI
cp .env.example .env                  # then set JWT_SECRET

# Daily dev loop:
make dev                              # starts Postgres only (db service, health-gated)
make check                            # vet + test + build — quick gate before push
make test-race                        # full suite + race detector — the real gate
make run                              # local API on :8080, Swagger at /swagger/index.html

# Full / optional gates:
make test-all                         # test + race + anti-flaky -count=2 (like CI)
make dev-restart                      # resets the Postgres volume to fresh (DESTRUCTIVE)
make watch                            # hot reload (requires air)
```

Tip: `make dev` only starts the database — the API runs on the host via `make run`, keeping the edit-restart loop fast without image rebuilds. `make test` is safe to run without a database (DB-dependent tests automatically skip instead of fail).

## Quick Start (Docker — recommended)

1. Copy the env template and set `JWT_SECRET` to a random secret (required — the app fails fast if empty):

   ```bash
   cp .env.example .env
   # edit .env → JWT_SECRET=... (random string, never empty)
   ```

2. Build and run the full stack (API + PostgreSQL):

   ```bash
   make run-prod            # = docker compose up -d --build
   # or without make:
   docker compose up -d --build
   ```

3. Check health and open the Swagger UI:

   ```bash
   curl localhost:8080/healthz          # → ok
   open http://localhost:8080/swagger/index.html
   ```

Stop the stack with `make run-prod-down` (or `docker compose down`). The `pgdata` volume is preserved — data survives restarts.

## Quick Start (Local, no Docker for the API)

Requires PostgreSQL running at `localhost:5432` (easiest: `make dev` — starts only the `db` service).

1. `cp .env.example .env`, set `JWT_SECRET`, and point `DB_URL` at your local Postgres:

   ```
   DB_URL=postgres://tm_user:***@localhost:5432/tmapi?sslmode=disable
   ```

2. Run it (schema migrations apply automatically at boot, fail-fast on error):

   ```bash
   make run                 # demo: PORT=8080 ENV=dev JWT_SECRET=demo-secret-for-testing go run ./cmd/api
   # or without make:
   go run ./cmd/api
   ```

### Environment Variables

| Variable            | Required | Default                 | Description                                                                 |
|---------------------|----------|-------------------------|-----------------------------------------------------------------------------|
| `PORT`              | –        | `8080`                  | HTTP listen port.                                                           |
| `ENV`               | –        | `dev`                   | `dev` or `prod`. In `prod`, internal details are hidden from 5xx error messages. |
| `JWT_SECRET`        | ✓        | –                       | JWT signing secret (HS256). Empty → fail-fast (the app refuses to start).   |
| `DB_URL`            | –        | `postgres://tm_user:***@localhost:5432/tmapi?sslmode=disable` | PostgreSQL connection string. In compose, the host is `db` (the service name). |
| `DB_MAX_OPEN_CONNS` | –        | `10`                    | Max concurrently open DB connections (pool is bounded, not unlimited).      |
| `DB_MAX_IDLE_CONNS` | –        | `4`                     | Max idle connections in the pool.                                           |

`JWT_SECRET` has no default and is validated fail-fast at startup. Other fields have defaults defined in [`internal/config/config.go`](internal/config/config.go).

## Running with Docker

Multi-stage image: builds with `CGO_ENABLED=0`, runtime is `distroless/static` (static binary, no shell). Migrations still run automatically inside the container at boot.

```bash
cp .env.example .env          # required: compose reads env_file .env
make run-prod                 # = docker compose up -d --build
```

- The `api` service exposes `${PORT:-8080}:8080` and waits for `db` to be healthy (`pg_isready`) before starting (`depends_on: condition: service_healthy`).
- The `db` service is PostgreSQL 16-alpine with a `pgdata` volume for persistence; port `5432` is exposed to the host for the test suite and `make migrate-*`.
- Container logs rotate via the `json-file` driver (`max-size 10m`, `max-file 3`).
- Stop with `make run-prod-down`.

## API Endpoints

All responses are JSON. List responses carry an envelope `{ data, meta: { page, limit, total } }`. Errors are consistent: `{ status, code, message, timestamp }`.

| Method | Path                     | Auth | Notes                                                                 |
|--------|--------------------------|------|-----------------------------------------------------------------------|
| POST   | `/register`              | –    | body: `email`, `password` → returns a JWT.                            |
| POST   | `/login`                 | –    | body: `email`, `password` → access token (HS256, 24h exp).             |
| POST   | `/tasks`                 | ✓    | header `Idempotency-Key: <uuid>` → idempotent create (snapshot replay). |
| GET    | `/tasks`                 | ✓    | query `?status=&search=&limit=&page=`.                                 |
| GET    | `/tasks/:id`             | ✓    | owner check in the query → 404 if owned by another user.               |
| PUT    | `/tasks/:id`             | ✓    | body: `title` / `description` / `status`.                              |
| DELETE | `/tasks/:id`             | ✓    | –                                                                      |
| POST   | `/tasks/:id/assign`      | ✓    | body: `assigneeId` — updates assignee + `task_logs` in one tx; owner only (403 otherwise). |
| GET    | `/healthz`               | –    | healthcheck (Docker).                                                  |
| GET    | `/swagger/index.html`    | –    | Swagger UI (spec at `/swagger/doc.json`).                              |

All routes above — except public auth, health, and swagger — are protected by a hand-rolled JWT middleware that places `user_id` in `c.Locals()`.

## Architecture

### Layered structure

```
task-manager-api/
├── cmd/api/main.go            # composition root: config → deps → server → graceful shutdown
├── internal/
│   ├── config/                # Config struct + env parsing + fail-fast validation
│   ├── api/
│   │   ├── middleware/        # request_id, logging, error handler, auth JWT, recovery
│   │   ├── handler/           # THIN: parse request → service → format response (+ swagger annotations)
│   │   └── docs/              # swag init output (docs.go, swagger.json) — committed
│   ├── domain/                # Task, User, Status, error codes — stdlib only
│   ├── service/               # business logic + repository interface DEFINITIONS
│   ├── repository/            # implements the interfaces with sqlx + pgx
│   └── infra/                 # DB pool (pgx), logger, JWT helper, embedded migrations
├── migrations/                # SQL golang-migrate (000001_init.up.sql / .down.sql), embedded
├── Dockerfile                 # multi-stage, CGO_ENABLED=0 → distroless
├── docker-compose.yml         # api + postgres:16-alpine (pg_isready healthcheck)
├── .env.example               # committed; the real .env is gitignored
├── Makefile                   # setup, run, build, test, vet, docker-*, deploy, migrate-*
└── README.md
```

**Dependency rules:**

- `handler` is thin — only parses requests and formats responses; no SQL or business logic.
- `service` defines the interfaces it needs (`TaskRepository`, `IdempotencyStore`, `Notifier`); `repository` implements them → easy to mock in unit tests without a DB.
- `domain` is the lowest layer, imports only the stdlib.
- `main.go` is the only place that wires things (manual constructor injection, no DI framework).
- Config is read from env via a struct + presence validation; empty `JWT_SECRET` → fail-fast exit.

### Patterns

Layered architecture with the **repository pattern** and **constructor injection** — without the hexagonal/DDD ceremony that would be disproportionate for a 4-table app.

| Pattern              | Where                                | Why it exists                                             |
|----------------------|--------------------------------------|-----------------------------------------------------------|
| Layered architecture  | folder structure                     | layer boundaries stay testable, not spaghetti             |
| Repository           | interface in service, impl in repository | mockable, unit tests without a DB                     |
| Constructor injection | `main.go`                            | manual dependency injection without a framework           |
| Middleware chain     | request_id → logging → auth → recovery | cross-cutting concerns don't leak into handlers         |
| Sentinel + wrapped errors | `domain` + handler              | one source of error codes, `errors.Is/As`                 |
| Adapter              | `Notifier` (log-based in main.go)    | notifications explicit as an interface, mockable          |

### Idempotency design (`POST /tasks`)

The key and the task are inserted in **one transaction** — never committed separately:

```
POST /tasks (header Idempotency-Key: <uuid>)
├─ 0. Validate header + UUID format → invalid? 400 INVALID_IDEMPOTENCY_KEY
├─ 1. FAST PATH (lock-free read): SELECT key
│     └─ found, not expired, has snapshot → REPLAY identical response (201)
├─ 2. SLOW PATH: BEGIN (write path)
│     a. lazy DELETE of expired keys (in the same tx)
│     b. INSERT tasks → task_id
│     c. INSERT idempotency_keys(snapshot, expires_at = +24h)
│     COMMIT → 201
```

- A UNIQUE constraint on the composite PK `(key, user_id)` is the anti-race backstop: two racing requests, the loser hits the UNIQUE violation → clean rollback → replays the winner's snapshot.
- Keys are scoped per user, so the same key across users never collides.
- The response snapshot is stored (`response_status` + `response_body`) — replay returns a body byte-for-byte identical to the first request, even if the task was mutated afterwards.
- 24h expiry, checked lazily on lookup (no cron).

### Assignment transaction (`POST /tasks/:id/assign`)

Assignment + audit trail live in **one transaction**:

```
BEGIN
  UPDATE tasks SET assignee_id=$1, updated_at=$2
    WHERE id=$3 AND owner_id=$4        (owner check at query level)
  INSERT INTO task_logs (task_id, actor_id, action='assigned', payload)
COMMIT
[notification via the Notifier interface — OUTSIDE the tx, failure does not roll back the assign]
```

Rows affected = 0 → rollback → `403 FORBIDDEN` (not your task, or not the owner). The notification is sent after commit — a failed notify is only a warn log; DB state stays consistent.

## Database Design

The schema lives in [`migrations/000001_init.up.sql`](migrations/000001_init.up.sql) and is embedded into the binary (fail-fast when a migration fails).

| Table              | Purpose                                                                          |
|--------------------|----------------------------------------------------------------------------------|
| `users`            | accounts; `email` UNIQUE, `password_hash` bcrypt.                                |
| `tasks`            | user-owned tasks; FK `owner_id`; nullable `assignee_id` (FK to users); status `todo`/`in_progress`/`done`; indexes for owner+status filtering, title search, and assignee lookup. |
| `task_logs`        | assignment audit trail (committed within the assign transaction).                |
| `idempotency_keys` | snapshot + expiry for idempotent creates (`key + user_id` composite PK).         |

Runtime pragmatics:

- **Connection pool is bounded** (default 10/4) via config, not unlimited.
- Migrations are **embedded** (`embed.FS`) and run automatically at startup — the binary is self-contained, distroless-friendly, and needs no `migrate` CLI at deploy time.

## Testing

```bash
make check           # vet + test + build — quick gate before push
make test            # = go test ./... (DB-backed tests skip without Postgres)
make test-race       # = go test -race ./... — must stay green
make test-all        # test + race + anti-flaky -count=2 — the CI order
go vet ./...         # static check only
```

Tests that need PostgreSQL (repository, infra, and the race suite) automatically **skip** (not fail) when `TEST_DB_URL` is unreachable; set the variable to run them fully:

```bash
export TEST_DB_URL=postgres://tm_user:***@localhost:5432/postgres?sslmode=disable
go test ./...
```

### Race suite (flagship)

[`internal/api/handler/race_test.go`](internal/api/handler/race_test.go) is an end-to-end suite: **real HTTP** (a real fiber listener) + **real PostgreSQL** (a throwaway database per run, auto-dropped). A barrier pattern releases 50 goroutines simultaneously — no sleeps, no jitter, deterministic:

| Test                              | What it proves                                                                    |
|-----------------------------------|-----------------------------------------------------------------------------------|
| `TestRace_IdempotencySequential`  | same key → 201 + byte-identical body; exactly 1 task + 1 key in the DB.            |
| `TestRace_IdempotencyConcurrent50`| 50 goroutines, 1 key: all 201, all bodies identical, exactly 1 task + 1 key in DB. |
| `TestRace_IdempotencyNegativeControl` | 50 goroutines, 50 distinct keys → 50 tasks (proof there is no over-blocking). |
| `TestRace_ReplayAfterMutation`    | PUT after create, then replay the old key → the original snapshot, not the updated task. |
| `TestRace_AuthConcurrent`         | 50 distinct users → each gets exactly 1 task; no cross-user leaks.                 |

```bash
go test -race -count=2 ./internal/api/handler -run TestRace_
```

## License

Not yet determined.
