# Task Manager API

REST API manajemen task multi-user yang dibangun dengan [Go](https://go.dev/) dan [Fiber v3](https://gofiber.io/). Menyediakan autentikasi JWT (register/login), CRUD `/tasks` dengan filter status, pencarian title, dan pagination, pembuatan task yang **idempoten** (`Idempotency-Key`), serta audit trail assignment. Seluruh data disimpan di **SQLite** (embedded, pure-Go, tanpa CGO) — tidak butuh database eksternal.

## Tech Stack

| Layer           | Teknologi                                                                     |
|-----------------|-------------------------------------------------------------------------------|
| Web framework   | [Fiber v3](https://gofiber.io/)                                               |
| Database        | [SQLite](https://www.sqlite.org/) via `modernc.org/sqlite` (pure Go, CGO off) |
| Query / SQL     | [jmoiron/sqlx](https://github.com/jmoiron/sqlx)                               |
| Migrations      | [golang-migrate/migrate](https://github.com/golang-migrate/migrate) (embedded, jalan otomatis saat boot) |
| Logging         | `log/slog` (structured JSON → stdout)                                         |
| Auth            | [golang-jwt/jwt v5](https://github.com/golang-jwt/jwt) + `golang.org/x/crypto/bcrypt` (middleware hand-rolled) |
| Env parsing     | `caarlos0/env/v11`                                                            |
| Testing         | `testing` stdlib + [testify](https://github.com/stretchr/testify)             |

> **Catatan Swagger:** UI Swagger (`swaggo`) belum di-wire ke `go.mod`, jadi dokumentasi interaktif di `/swagger/index.html` masih menyusul. Daftar endpoint lengkap ada di bawah ([API Endpoints](#api-endpoints)).

## Requirements

- Go **1.27+**
- `make` (opsional — bisa diganti `go run` / `go test` langsung)
- Docker + Docker Compose (opsional, untuk menjalankan via container)
- Tidak butuh database eksternal; SQLite embedded.

## Quick Start (Local)

1. Clone repo dan masuk ke direktori.
2. Salin template env, lalu isi `JWT_SECRET` dengan secret acak (wajib — aplikasi fail-fast kalau kosong):

   ```bash
   cp .env.example .env
   # edit .env → JWT_SECRET=... (string acak, jangan kosong)
   ```

3. Jalankan server (migrasi schema otomatis jalan saat boot, fail-fast kalau gagal):

   ```bash
   make run                 # demo: PORT=8080 ENV=dev JWT_SECRET=demo-secret-for-testing go run ./cmd/api
   # atau tanpa make:
   go run ./cmd/api
   ```

Server mendengarkan di `PORT` (default `8080`). Cek health: `curl localhost:8080/healthz` → `ok`.

### Environment Variables

| Variable            | Wajib | Default           | Keterangan                                                                 |
|---------------------|-------|-------------------|----------------------------------------------------------------------------|
| `PORT`              | –     | `8080`            | Port HTTP yang di-listen.                                                  |
| `ENV`               | –     | `dev`             | `dev` atau `prod`. Di `prod`, detail internal disembunyikan pada pesan error 5xx. |
| `JWT_SECRET`        | ✓     | –                 | Secret penanda JWT (HS256). Kosong → fail-fast (aplikasi menolak jalan).   |
| `DB_PATH`           | –     | `./data/tasks.db` | Lokasi file database SQLite. (Bukan `DB_URL` — project ini memakai SQLite, bukan server Postgres.) |
| `DB_MAX_OPEN_CONNS` | –     | `4`               | Maksimum koneksi DB dibuka bersamaan (pool dibatasi, bukan unlimited).     |
| `DB_MAX_IDLE_CONNS` | –     | `4`               | Maksimum koneksi idle di pool.                                             |

`JWT_SECRET` tidak punya default dan divalidasi fail-fast saat startup. Field lain punya default di [`internal/config/config.go`](internal/config/config.go).

## Running with Docker

Image multi-stage: build `CGO_ENABLED=0` lalu runtime `distroless/static` (binary statis, ~2MB, no shell). Migrasi tetap jalan otomatis di dalam container saat boot.

```bash
cp .env.example .env          # wajib: compose membaca env_file .env
docker compose up --build     # build + jalankan service api
# atau: docker compose up -d --build  (detached)
```

- Container mengekspos `${PORT:-8080}:8080` dan menimpa `DB_PATH=/data/tasks.db`, dengan volume `./data:/data` agar database persisten di host.
- Healthcheck pakai `/healthz` (baris `healthcheck:` di compose saat ini ter-comment; aktifkan bila perlu).
- Log container dirotasi via driver `json-file` (`max-size 10m`, `max-file 3`).
- Hentikan: `docker compose down`.

## API Endpoints

Semua response JSON. List response membawa envelope `{ data, meta: { page, limit, total } }`. Error konsisten: `{ status, code, message, timestamp }`.

| Method | Path                  | Auth | Catatan                                                                 |
|--------|-----------------------|------|-------------------------------------------------------------------------|
| POST   | `/register`           | –    | body: `email`, `password` → kembalikan JWT.                            |
| POST   | `/login`              | –    | body: `email`, `password` → access token (HS256, exp 24h).             |
| POST   | `/tasks`              | ✓    | header `Idempotency-Key: <uuid>` → create idempoten.                   |
| GET    | `/tasks`              | ✓    | query `?status=&search=&limit=&page=`.                                 |
| GET    | `/tasks/:id`          | ✓    | owner check di query (`WHERE owner_id = ?`) → 404 kalau milik user lain. |
| PUT    | `/tasks/:id`          | ✓    | body: `title` / `description` / `status`.                             |
| DELETE | `/tasks/:id`          | ✓    | –                                                                       |
| GET    | `/healthz`            | –    | healthcheck (Docker).                                                   |

> Endpoint `POST /tasks/:id/assign` (assignment + `task_logs` dalam satu tx) sudah direncanakan di desain/schema (`assignee_id`, tabel `task_logs`) namun **belum di-register** di router saat ini — lihat [`internal/api/handler/task.go`](internal/api/handler/task.go).

Semua response (sukses & error) berformat JSON. Semua route di atas — kecuali auth publik, health, dan swagger — dilindungi middleware JWT hand-rolled yang menaruh `user_id` di `c.Locals()`.

## Architecture

### Layered structure

```
task-manager-api/
├── cmd/api/main.go            # composition root: config → deps → server → graceful shutdown
├── internal/
│   ├── config/                # struct Config + parse env + fail-fast validasi
│   ├── api/
│   │   ├── middleware/        # request_id, logging, error handler, auth JWT, recovery
│   │   └── handler/           # THIN: parse request → service → format response
│   ├── domain/                # Task, User, Status, error codes — stdlib saja
│   ├── service/               # business logic + DEFINISI interface repository
│   ├── repository/            # implementasi interface dengan sqlx + sqlite
│   └── infra/                 # DB connection + pragmas, logger, JWT helper, migrations
├── migrations/                # SQL golang-migrate (000001_init.up.sql / .down.sql), di-embed
├── docs/                      # output swag (jika di-generate)
├── Dockerfile                 # multi-stage, CGO_ENABLED=0 → distroless
├── docker-compose.yml
├── .env.example              # di-commit; .env asli di-gitignore
├── Makefile                   # run, build, test, vet, migrate-up/down
└── README.md
```

**Dependency rules:**

- `handler` tipis — hanya parse request dan format response, tidak ada SQL/business logic.
- `service` mendefinisikan interface yang dibutuhkan (`TaskRepository`, `IdempotencyStore`, `Notifier`); `repository` yang mengimplementasi → mudah di-mock di unit test tanpa DB.
- `domain` lapisan paling bawah, hanya import stdlib.
- `main.go` satu-satunya yang melakukan wiring (constructor injection manual, tanpa framework DI).
- Config dibaca dari env via struct + validasi presence; `ENV=dev|prod` wajib, `JWT_SECRET` kosong → exit fail-fast.

### Patterns

Layered architecture dengan **repository pattern** dan **constructor injection** — tanpa ceremony hexagonal/DDD yang tidak proporsional untuk skala 4 tabel.

| Pattern              | Di mana                              | Kenapa ada                                                |
|----------------------|--------------------------------------|-----------------------------------------------------------|
| Layered architecture  | struktur folder                       | batas antar-layer testable, bukan spaghetti               |
| Repository           | interface di service, impl di repository | mockable, unit test tanpa DB                        |
| Constructor injection | `main.go`                           | dependency injection manual tanpa framework               |
| Middleware chain     | request_id → logging → auth → recovery | cross-cutting, tidak dibebankan ke handler             |
| Sentinel + wrapped errors | `domain` + handler               | satu sumber code error, `errors.Is/As`                    |
| Adapter              | `Notifier` (mock log)                | notifikasi dibuat eksplisit sebagai interface             |

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

- Transaction di-retry dengan exponential back-off bila kena `SQLITE_BUSY` (write contention di WAL).
- UNIQUE constraint pada PK composite `(key, user_id)` = backstop anti-race: dua goroutine yang nge-race, yang kalah kena UNIQUE violation → rollback bersih → replay snapshot pemenang.
- Key di-scope per user, jadi key sama antar user tidak bertabrakan.
- `go test -race` membuktikan tidak ada memory race di kode Go; atomicity DB dijamin UNIQUE + retry BUSY (dua mekanisme berbeda).

### Transaction assign (`POST /tasks/:id/assign`, direncanakan)

Berdasarkan desain, assignment + audit trail berada dalam **satu transaction**:

```
BEGIN
  UPDATE tasks SET assignee_id=?, updated_at=? WHERE id=? AND owner_id=?  (owner check)
  INSERT INTO task_logs (task_id, actor_id, action='assigned', payload)
  [notifikasi → interface Notifier, implementasi dev = log mock, DILUAR tx]
COMMIT
```

Salah satu step gagal → rollback penuh (defer rollback + commit eksplisit), tidak ada state parsial. Owner check di level query (`WHERE owner_id = ?`), bukan filter aplikasi.

## Database Design

Skema ada di [`migrations/000001_init.up.sql`](migrations/000001_init.up.sql) dan di-embed ke binary (fail-fast saat migrasi gagal).

| Tabel              | Purpose                                                                         |
|--------------------|---------------------------------------------------------------------------------|
| `users`            | akun; `email` UNIQUE, `password_hash` bcrypt.                                  |
| `tasks`            | task milik user; FK `owner_id`; `assignee_id` nullable; status `todo`/`in_progress`/`done`; index untuk filter owner+status dan search title. |
| `task_logs`        | audit trail assignment (ikut transaction assign).                              |
| `idempotency_keys` | snapshot + expiry untuk idempotent create (`key + user_id` composite PK).       |

Pragmatik runtime:

- SQLite berjalan dengan **WAL** journal mode untuk concurrency baca-tulis.
- **Pragmas dikonfigurasi via DSN** (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`) — bukan via `Exec("PRAGMA ...")` sekali, karena `foreign_keys`/`busy_timeout` bersifat per-connection dan `Exec` sekali hanya kena satu koneksi dari pool.
- **Connection pool dibatasi** (default 4/4) via config, bukan unlimited.

## Testing

```bash
make test            # = go test ./...
go test ./...        # semua package

go test -race ./...  # race detector (wajib hijau untuk suite idempotency concurrency)
go vet ./...         # static check
make vet             # = go vet ./...
```

Suite saat ini mencakup sanity check level database (ping & pool, pragma via DSN, migrasi up/down menghasilkan 4 tabel) serta race suite idempotency (sequential + concurrent dengan SQLite temp file via `t.TempDir()`, real `httptest` server, barrier pattern — tepat 1 task + 1 key untuk key sama).

> **Blocker build saat ini:** tree ini gagal compile karena import `"errors"` yang tidak terpakai di [`internal/repository/task.go`](internal/repository/task.go) (Go melaporkan *"errors imported and not used"*). Ini error kode, bukan dokumentasi — `go build ./...` / `go test ./...` baru akan hijau setelah import tersebut dihapus (perubahan kode di luar ruang lingkup update README ini).

## License

Belum ditentukan.
