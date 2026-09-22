# Task Manager API

REST API manajemen task multi-user yang dibangun dengan [Go](https://go.dev/) dan [Fiber v3](https://gofiber.io/). Menyediakan autentikasi JWT (register/login), CRUD `/tasks` dengan filter status, pencarian title, dan pagination, pembuatan task yang **idempoten** (`Idempotency-Key`), assignment dengan audit trail dalam satu transaction, serta **Swagger UI** untuk dokumentasi interaktif. Database-nya **PostgreSQL** — jalan penuh via Docker Compose.

## Tech Stack

| Layer           | Teknologi                                                                     |
|-----------------|-------------------------------------------------------------------------------|
| Web framework   | [Fiber v3](https://gofiber.io/)                                               |
| Database        | [PostgreSQL 16](https://www.postgresql.org/) via `jackc/pgx/v5` (stdlib driver) |
| Query / SQL     | [jmoiron/sqlx](https://github.com/jmoiron/sqlx)                               |
| Migrations      | [golang-migrate/migrate](https://github.com/golang-migrate/migrate) (embedded, jalan otomatis saat boot) |
| Logging         | `log/slog` (structured JSON → stdout)                                         |
| Auth            | [golang-jwt/jwt v5](https://github.com/golang-jwt/jwt) + `golang.org/x/crypto/bcrypt` (middleware hand-rolled) |
| Swagger         | [swaggo](https://github.com/swaggo/swag) + `gofiber/contrib/v3/swaggo`        |
| Env parsing     | `caarlos0/env/v11`                                                            |
| Testing         | `testing` stdlib + race suite end-to-end (real HTTP + real PostgreSQL)        |

## Requirements

- Go **1.27+**
- **Docker + Docker Compose** (untuk PostgreSQL dan/atau full stack)
- `make` (opsional — bisa diganti `go run` / `go test` langsung)
- `migrate` CLI (opsional — hanya untuk migrasi manual ke DB eksternal; binary auto-migrate saat boot)

Atau cukup jalankan sekali:

```bash
make setup   # cek go + docker, install migrate CLI kalau belum ada
```

### Development Workflow

`make` (tanpa argumen) menampilkan panduan ini. Urutan kerja harian:

```bash
# Sekali aja (setup):
make setup                            # cek tooling, install migrate CLI
cp .env.example .env                  # lalu isi JWT_SECRET

# Loop development harian:
make dev                              # nyalain Postgres doang (db service, health-gated)
make check                            # vet + test + build — gate cepet sebelum push
make test-race                        # full suite + race detector — gate sebenarnya
make run                              # API lokal :8080, Swagger di /swagger/index.html

# Gate lengkap / opsional:
make test-all                         # test + race + anti-flaky -count=2 (kayak CI)
make dev-restart                      # reset volume Postgres ke fresh (DESTRUCTIVE)
make watch                            # hot reload (butuh air)
```

Tips: `make dev` cuma menyalakan database — API-nya jalan di host via `make run`, jadi loop edit-restart tetap cepat tanpa rebuild image. `make test` aman jalan tanpa database (test yang butuh DB otomatis skip, bukan fail).

## Quick Start (Docker — recommended)

1. Salin template env, lalu isi `JWT_SECRET` dengan secret acak (wajib — aplikasi fail-fast kalau kosong):

   ```bash
   cp .env.example .env
   # edit .env → JWT_SECRET=... (string acak, jangan kosong)
   ```

2. Build dan jalankan full stack (API + PostgreSQL):

   ```bash
   make run-prod            # = docker compose up -d --build
   # atau tanpa make:
   docker compose up -d --build
   ```

3. Cek health dan buka Swagger UI:

   ```bash
   curl localhost:8080/healthz          # → ok
   open http://localhost:8080/swagger/index.html
   ```

Hentikan stack: `make run-prod-down` (atau `docker compose down`). Volume `pgdata` dipertahankan — data tidak hilang antar restart.

## Quick Start (Local, tanpa Docker untuk API)

Butuh PostgreSQL yang jalan di `localhost:5432` (paling gampang: `make dev` — hanya menyalakan service `db`).

1. `cp .env.example .env`, isi `JWT_SECRET`, dan pastikan `DB_URL` mengarah ke Postgres lokal:

   ```
   DB_URL=postgres://tm_user:tm_pass@localhost:5432/tmapi?sslmode=disable
   ```

2. Jalankan (migrasi schema otomatis saat boot, fail-fast kalau gagal):

   ```bash
   make run                 # demo: PORT=8080 ENV=dev JWT_SECRET=demo-secret-for-testing go run ./cmd/api
   # atau tanpa make:
   go run ./cmd/api
   ```

### Environment Variables

| Variable            | Wajib | Default                 | Keterangan                                                                 |
|---------------------|-------|-------------------------|----------------------------------------------------------------------------|
| `PORT`              | –     | `8080`                  | Port HTTP yang di-listen.                                                  |
| `ENV`               | –     | `dev`                   | `dev` atau `prod`. Di `prod`, detail internal disembunyikan pada pesan error 5xx. |
| `JWT_SECRET`        | ✓     | –                       | Secret penanda JWT (HS256). Kosong → fail-fast (aplikasi menolak jalan).   |
| `DB_URL`            | –     | `postgres://tm_user:tm_pass@localhost:5432/tmapi?sslmode=disable` | Connection string PostgreSQL. Di compose, host-nya `db` (nama service). |
| `DB_MAX_OPEN_CONNS` | –     | `10`                    | Maksimum koneksi DB dibuka bersamaan (pool dibatasi, bukan unlimited).     |
| `DB_MAX_IDLE_CONNS` | –     | `4`                     | Maksimum koneksi idle di pool.                                             |

`JWT_SECRET` tidak punya default dan divalidasi fail-fast saat startup. Field lain punya default di [`internal/config/config.go`](internal/config/config.go).

## Running with Docker

Image multi-stage: build `CGO_ENABLED=0` lalu runtime `distroless/static` (binary statis, no shell). Migrasi tetap jalan otomatis di dalam container saat boot.

```bash
cp .env.example .env          # wajib: compose membaca env_file .env
make run-prod                 # = docker compose up -d --build
```

- Service `api` mengekspos `${PORT:-8080}:8080` dan menunggu `db` healthy (`pg_isready`) sebelum start (`depends_on: condition: service_healthy`).
- Service `db` = PostgreSQL 16-alpine, volume `pgdata` untuk persistensi, port `5432` diekspos ke host untuk test suite dan `make migrate-*`.
- Log container dirotasi via driver `json-file` (`max-size 10m`, `max-file 3`).
- Hentikan: `make run-prod-down`.

## API Endpoints

Semua response JSON. List response membawa envelope `{ data, meta: { page, limit, total } }`. Error konsisten: `{ status, code, message, timestamp }`.

| Method | Path                     | Auth | Catatan                                                                 |
|--------|--------------------------|------|-------------------------------------------------------------------------|
| POST   | `/register`              | –    | body: `email`, `password` → kembalikan JWT.                            |
| POST   | `/login`                 | –    | body: `email`, `password` → access token (HS256, exp 24h).             |
| POST   | `/tasks`                 | ✓    | header `Idempotency-Key: <uuid>` → create idempoten (snapshot replay).  |
| GET    | `/tasks`                 | ✓    | query `?status=&search=&limit=&page=`.                                 |
| GET    | `/tasks/:id`             | ✓    | owner check di query → 404 kalau milik user lain.                      |
| PUT    | `/tasks/:id`             | ✓    | body: `title` / `description` / `status`.                              |
| DELETE | `/tasks/:id`             | ✓    | –                                                                       |
| POST   | `/tasks/:id/assign`      | ✓    | body: `assigneeId` — update assignee + `task_logs` dalam satu tx; hanya owner (403 kalau bukan). |
| GET    | `/healthz`               | –    | healthcheck (Docker).                                                   |
| GET    | `/swagger/index.html`    | –    | Swagger UI (spec di `/swagger/doc.json`).                              |

Semua route di atas — kecuali auth publik, health, dan swagger — dilindungi middleware JWT hand-rolled yang menaruh `user_id` di `c.Locals()`.

## Architecture

### Layered structure

```
task-manager-api/
├── cmd/api/main.go            # composition root: config → deps → server → graceful shutdown
├── internal/
│   ├── config/                # struct Config + parse env + fail-fast validasi
│   ├── api/
│   │   ├── middleware/        # request_id, logging, error handler, auth JWT, recovery
│   │   ├── handler/           # THIN: parse request → service → format response (+ swagger annotations)
│   │   └── docs/              # output swag init (docs.go, swagger.json) — di-commit
│   ├── domain/                # Task, User, Status, error codes — stdlib saja
│   ├── service/               # business logic + DEFINISI interface repository
│   ├── repository/            # implementasi interface dengan sqlx + pgx
│   └── infra/                 # DB pool (pgx), logger, JWT helper, embedded migrations
├── migrations/                # SQL golang-migrate (000001_init.up.sql / .down.sql), di-embed
├── Dockerfile                 # multi-stage, CGO_ENABLED=0 → distroless
├── docker-compose.yml         # api + postgres:16-alpine (healthcheck pg_isready)
├── .env.example              # di-commit; .env asli di-gitignore
├── Makefile                   # setup, run, build, test, vet, docker-*, deploy, migrate-*
└── README.md
```

**Dependency rules:**

- `handler` tipis — hanya parse request dan format response, tidak ada SQL/business logic.
- `service` mendefinisikan interface yang dibutuhkan (`TaskRepository`, `IdempotencyStore`, `Notifier`); `repository` yang mengimplementasikan → mudah di-mock di unit test tanpa DB.
- `domain` lapisan paling bawah, hanya import stdlib.
- `main.go` satu-satunya yang melakukan wiring (constructor injection manual, tanpa framework DI).
- Config dibaca dari env via struct + validasi presence; `JWT_SECRET` kosong → exit fail-fast.

### Patterns

Layered architecture dengan **repository pattern** dan **constructor injection** — tanpa ceremony hexagonal/DDD yang tidak proporsional untuk skala 4 tabel.

| Pattern              | Di mana                              | Kenapa ada                                                |
|----------------------|--------------------------------------|-----------------------------------------------------------|
| Layered architecture  | struktur folder                       | batas antar-layer testable, bukan spaghetti               |
| Repository           | interface di service, impl di repository | mockable, unit test tanpa DB                        |
| Constructor injection | `main.go`                           | dependency injection manual tanpa framework               |
| Middleware chain     | request_id → logging → auth → recovery | cross-cutting, tidak dibebankan ke handler             |
| Sentinel + wrapped errors | `domain` + handler               | satu sumber code error, `errors.Is/As`                    |
| Adapter              | `Notifier` (log-based di main.go)    | notifikasi eksplisit sebagai interface, mockable          |

### Desain idempotency (`POST /tasks`)

Key dan task di-insert dalam **satu transaction** — tidak pernah di-commit terpisah:

```
POST /tasks (header Idempotency-Key: <uuid>)
├─ 0. Validasi header + format UUID → invalid? 400 INVALID_IDEMPOTENCY_KEY
├─ 1. FAST PATH (read tanpa lock): SELECT key
│     └─ ketemu, belum expired, ada snapshot → REPLAY response identik (201)
├─ 2. SLOW PATH: BEGIN (write path)
│     a. lazy DELETE key expired (dalam tx yang sama)
│     b. INSERT tasks → task_id
│     c. INSERT idempotency_keys(snapshot, expires_at = +24h)
│     COMMIT → 201
```

- UNIQUE constraint pada PK composite `(key, user_id)` = backstop anti-race: dua request yang nge-race, yang kalah kena UNIQUE violation → rollback bersih → replay snapshot pemenang.
- Key di-scope per user, jadi key sama antar user tidak bertabrakan.
- Snapshot response disimpan (`response_status` + `response_body`) — replay mengembalikan body byte-per-byte identik dengan request pertama, meski task sudah di-mutasi setelahnya.
- Expiry 24 jam, dicek lazy saat lookup (tanpa cron).

### Transaction assign (`POST /tasks/:id/assign`)

Assignment + audit trail berada dalam **satu transaction**:

```
BEGIN
  UPDATE tasks SET assignee_id=$1, updated_at=$2
    WHERE id=$3 AND owner_id=$4        (owner check di level query)
  INSERT INTO task_logs (task_id, actor_id, action='assigned', payload)
COMMIT
[notifikasi via interface Notifier — DI LUAR tx, kegagalan tidak meng-rollback assign]
```

Rows affected = 0 → rollback → `403 FORBIDDEN` (bukan task-nya, atau bukan owner-nya). Notifikasi dikirim setelah commit — gagal notify hanya jadi warn log, state DB tetap konsisten.

## Database Design

Skema ada di [`migrations/000001_init.up.sql`](migrations/000001_init.up.sql) dan di-embed ke binary (fail-fast saat migrasi gagal).

| Tabel              | Purpose                                                                         |
|--------------------|---------------------------------------------------------------------------------|
| `users`            | akun; `email` UNIQUE, `password_hash` bcrypt.                                  |
| `tasks`            | task milik user; FK `owner_id`; `assignee_id` nullable (FK ke users); status `todo`/`in_progress`/`done`; index untuk filter owner+status, search title, dan lookup assignee. |
| `task_logs`        | audit trail assignment (ikut transaction assign).                              |
| `idempotency_keys` | snapshot + expiry untuk idempotent create (`key + user_id` composite PK).       |

Pragmatik runtime:

- **Connection pool dibatasi** (default 10/4) via config, bukan unlimited.
- Migrasi **embedded** (`embed.FS`) dan jalan otomatis di startup — binary self-contained, distroless-friendly, tidak butuh `migrate` CLI saat deploy.

## Testing

```bash
make check           # vet + test + build — gate cepet sebelum push
make test            # = go test ./... (DB-backed test skip kalau tanpa Postgres)
make test-race       # = go test -race ./... — wajib hijau
make test-all        # test + race + anti-flaky -count=2 — urutan CI
go vet ./...         # static check saja
```

Test yang butuh PostgreSQL (repository, infra, dan race suite) otomatis **skip** (bukan fail) kalau `TEST_DB_URL` tidak reachable; set variabel ini untuk menjalankan penuh:

```bash
export TEST_DB_URL=postgres://tm_user:tm_pass@localhost:5432/postgres?sslmode=disable
go test ./...
```

### Race suite (flagship)

[`internal/api/handler/race_test.go`](internal/api/handler/race_test.go) adalah suite end-to-end: **real HTTP** (fiber listener sungguhan) + **real PostgreSQL** (throwaway database per run, auto-drop). Barrier pattern melepas 50 goroutine bersamaan — tanpa sleep, tanpa jitter, deterministik:

| Test                          | Yang dibuktikan                                                                 |
|-------------------------------|---------------------------------------------------------------------------------|
| `TestRace_IdempotencySequential` | key sama → 201 + body byte-identical; tepat 1 task + 1 key di DB.             |
| `TestRace_IdempotencyConcurrent50` | 50 goroutine, 1 key: semua 201, semua body identik, DB tepat 1 task + 1 key. |
| `TestRace_IdempotencyNegativeControl` | 50 goroutine, 50 key berbeda → 50 task (bukti tidak over-blocking).       |
| `TestRace_ReplayAfterMutation` | PUT setelah create, lalu replay key lama → snapshot asli, bukan task ter-update. |
| `TestRace_AuthConcurrent`     | 50 user berbeda → masing-masing tepat 1 task; tidak ada cross-user leak.        |

```bash
go test -race -count=2 ./internal/api/handler -run TestRace_
```

## License

Belum ditentukan.
