package infra

import (
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/jmoiron/sqlx"
	"github.com/wandyirawan/task-manager-api/internal/config"
)

// NewDB opens a PostgreSQL connection via sqlx using the config values.
//
// The driver is jackc/pgx/v5 (pure Go, CGO off) registered under the
// "pgx" name. The connection URL comes entirely from config.DBURL so the
// same binary works against any Postgres (local, Docker Compose service, or
// managed). Foreign keys, timeouts and pool sizing are all server/driver
// defaults for Postgres — no per-connection pragmas are needed (unlike
// SQLite, PG enforces FK integrity and MVCC isolation natively).
func NewDB(cfg *config.Config) (*sqlx.DB, error) {
	db, err := sqlx.Connect("pgx", cfg.DBURL)
	if err != nil {
		return nil, err
	}

	// Bounded pool, sourced from config (never hardcoded).
	db.DB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.DB.SetMaxIdleConns(cfg.DBMaxIdleConns)

	return db, nil
}
