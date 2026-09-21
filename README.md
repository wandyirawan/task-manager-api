# Task Manager API

Task Management API multi-user yang dibangun dengan [Go](https://go.dev/) dan [Fiber v3](https://gofiber.io/). Menyediakan REST endpoint untuk manajemen task (CRUD), autentikasi JWT, filter/search/pagination, idempotent create, dan assign task dengan transactional audit trail. Data disimpan di SQLite.

## Tech Stack

| Layer          | Teknologi                                                                 |
|----------------|---------------------------------------------------------------------------|
| Web framework  | [Fiber v3](https://gofiber.io/)                                           |
| Database       | [SQLite](https://www.sqlite.org/) via `modernc.org/sqlite` (pure Go, tanpa CGO) |
| Query builder  | [jmoiron/sqlx](https://github.com/jmoiron/sqlx)                           |
| Migrasi        | [golang-migrate](https://github.com/golang-migrate/migrate)               |
| Logging        | `log/slog` (structured JSON → stdout)                                    |
| Auth           | [golang-jwt/jwt v5](https://github.com/golang-jwt/jwt) + `x/crypto/bcrypt` (middleware hand-rolled) |
| Docs / API     | [swaggo](https://github.com/swaggo/swag) `*`                              |
| Testing        | `testing` stdlib + [testify](https://github.com/stretchr/testify)         |

`*` Swagger UI & daftar endpoint lengkap masih menyusul di milestone berikutnya (lihat [TODO / Roadmap](#todo--roadmap)).

## Requirements

- Go **1.27+**
- `make` (opsional — alternatif pakai `go run`)
- Tidak butuh database eksternal; SQLite embedded.

## Setup & Run

1. Clone & masuk ke direktori project.
2. Salin template env dan isi secret-nya:

   ```bash
   cp .env.example .env
   # wajib isi JWT_SECRET dengan secret acak
   ```

3. Jalankan server:

   ```bash
   make run            # atau
   go run ./cmd/api
   ```

Server `make run` dijalankan dengan nilai demo untuk kemudahan development.

### Environment Variables

| Variable          | Wajib | Default        | Keterangan                                        |
|-------------------|-------|----------------|---------------------------------------------------|
| `PORT`            | –     | `8080`         | Port listen HTTP                                  |
| `ENV`             | –     | `dev`          | `dev` atau `prod`. `prod` menyembunyikan detail internal di pesan error 5xx |
| `JWT_SECRET`      | ✓     | –              | Secret penanda token JWT (HS256). Kosong → fail-fast |
| `DB_PATH`         | –     | `./data/tasks.db` | Lokasi file SQLite                              |
| `DB_MAX_OPEN_CONNS` | –   | `4`            | Maksimal koneksi DB yang dibuka secara bersamaan (pool bounded) |
| `DB_MAX_IDLE_CONNS` | –   | `4`            | Maksimal koneksi idle di pool                    |

`JWT_SECRET` tidak punya default dan divalidasi fail-fast di startup — aplikasi menolak jalan jika kosong. Field lain punya default di dalam [config](internal/config/config.go).

## Arsitektur

### Layered structure

```
task-manager-api/
├── cmd/api/main.go            # composition root: config → deps → server
├── internal/
│   ├── config/                # struct Config + parse env + fail-fast validasi
│   ├── api/
│   │   ├── middleware/        # request_id, logging, error handler, auth JWT, recovery
│   │   └── handler/           # THIN: parse request → service → format response
│   ├── domain/                # task/user model + error codes (stdlib saja)
│   ├── service/               # business logic + definisi interface repository
│   ├── repository/            # implementasi interface dengan sqlx + sqlite
│   └── infra/                 # DB connection + pragmas, logger setup, JWT helper
├── migrations/                # SQL golang-migrate (NNNN_name.up.sql / .down.sql)
├── docs/                      # output swag init
└── Makefile
```

**Dependency rules:**
- `handler` tipis — hanya parse request dan format response, tidak ada SQL/business logic.
- `service` mendefinisikan interface yang dibutuhkan (`TaskRepository`, `IdempotencyStore`, `Notifier`); `repository` yang mengimplementasi → mudah di-mock di unit test tanpa DB.
- `domain` berada di lapisan paling bawah, hanya import stdlib.
- `main.go` adalah satu-satunya yang melakukan wiring (constructor injection manual).
- Config dibaca dari env via struct + validasi presence.

### Patterns

Layered architecture dengan **repository pattern** dan **constructor injection** — tanpa ceremony hexagonal/DDD yang tidak proporsional untuk skala 4 tabel.

| Pattern             | Di mana            | Kenapa ada                                       |
|---------------------|--------------------|--------------------------------------------------|
| Layered architecture | struktur folder    | batas antar-layer testable, bukan spaghetti      |
| Repository          | interface di service, impl di repository | mockable, unit test tanpa DB     |
| Constructor injection | `main.go`        | dependency injection manual tanpa framework      |
| Middleware chain    | request_id → logging → auth → recovery | cross-cutting, tidak dibebankan ke handler |
| Sentinel + wrapped errors | `domain` + handler | satu sumber code error, `errors.Is/As`       |
| Adapter             | `Notifier` (mock)  | notifikasi dibuat eksplisit sebagai interface    |

### Desain idempotency (`POST /tasks`)

Idempotent create diejawantahkan sebagai **satu transaction** — key dan task tidak pernah di-commit secara terpisah:

```
POST /tasks (header Idempotency-Key: <uuid>)
├─ 0. Validasi header + format UUID → invalid? 400 INVALID_IDEMPOTENCY_KEY
├─ 1. FAST PATH (read tanpa lock): SELECT key
│     └─ ketemu, belum expired, ada snapshot → REPLAY response identik (201)
├─ 2. SLOW PATH: BEGIN IMMEDIATE (write lock)
│     a. cek ulang key dalam lock
│        ├─ fresh + snapshot → ROLLBACK → replay (kasus race)
│        └─ absent/expired:
│           b. DELETE key expired (lazy cleanup, dalam tx yang sama)
│           c. INSERT tasks → task_id
│           d. INSERT idempotency_keys(snapshot, expires_at = +24h)
│     COMMIT → 201
```

Mengapa `BEGIN IMMEDIATE` + `UNIQUE` constraint dipakai bersamaan:

- Jika transaction pertama crash di tengah jalan → rollback penuh → key tidak persist → retry dianggap fresh (tidak ada key zombie).
- Key yang sama di koneksi berbeda diblokir `BEGIN IMMEDIATE` (menunggu write lock); sekalipun dua transaction lolos bersamaan, insert kedua **pasti** kena `UNIQUE` violation → handler menangkap, re-read, lalu replay.
- Key di-scope per user (composite PK `key + user_id`), jadi key yang sama antar user tidak saling bertabrakan.

### Transaction assign (`POST /tasks/:id/assign`)

Assignment dan audit trail-nya berada dalam **satu transaction**:

```
BEGIN
  UPDATE tasks SET assignee_id=?, updated_at=? WHERE id=? AND owner_id=?  (owner check)
  INSERT INTO task_logs (task_id, actor_id, action='assigned', payload)
  [notifikasi → interface Notifier, implementasi dev = log mock, DILUAR tx]
COMMIT
```

- Salah satu step gagal → rollback penuh (defer rollback + commit eksplisit), tidak ada state parsial.
- Owner check dilakukan di level query (`WHERE owner_id = ?`), bukan filter di aplikasi.
- Lazy cleanup key yang expired dilakukan dalam transaction yang sama.

## Database Design

Empat tabel, schema tersimpan di [migrations/](migrations/):

| Tabel              | Purpose                                              |
|--------------------|------------------------------------------------------|
| `users`            | akun pengguna; `email` UNIQUE, `password_hash` bcrypt |
| `tasks`            | task milik user; FK `owner_id`; `assignee_id` nullable; status `todo`/`in_progress`/`done`; index untuk filter owner+status dan search title |
| `task_logs`        | audit trail assignment (ikut transaction assign)     |
| `idempotency_keys` | snapshot + expiry untuk idempotent create (`key + user_id` composite PK) |

Pragmatik runtime:

- SQLite dijalankan dengan **WAL** journal mode untuk concurrency baca-tulis.
- **Pragmas dikonfigurasi via DSN** (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`) — bukan via `Exec("PRAGMA ...")` sekali, karena `foreign_keys`/`busy_timeout` bersifat per-connection dan `Exec` sekali hanya kena satu koneksi dari pool.
- **Connection pool dibatasi** (default 4/4) via config, bukan unlimited.

## Testing

```bash
make test        # atau: go test ./...
```

Suite saat ini mencakup sanity checks level database:

- koneksi DB ping & pool tersetup benar,
- pragma terpasang via DSN (`foreign_keys=ON`, WAL),
- migrasi up/down jalan dan menghasilkan 4 tabel.

Suite race-condition idempotency (sequential + concurrent, `go test -race` dengan SQLite temp file) masih menyusul — lihat [TODO / Roadmap](#todo--roadmap).

## TODO / Roadmap

<!-- TODO(P8): daftar endpoint lengkap (swagger) -->
<!-- TODO(P8): Docker & compose instructions -->

## License

Belum ditentukan.