package infra

import (
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/jmoiron/sqlx"
	"github.com/wandyirawan/task-manager-api/internal/config"
)

// NewDB opens a sqlite connection via sqlx using the config values.
//
// Pragmas (WAL, busy_timeout, foreign_keys) are supplied through the DSN so
// the modernc.org/sqlite driver applies them to EVERY new pooled connection.
// Never issue `PRAGMA ...` via db.Exec once at startup: foreign_keys and
// busy_timeout are per-connection, so a one-off Exec only touches one pooled
// connection while the rest silently run with FK=OFF.
func NewDB(cfg *config.Config) (*sqlx.DB, error) {
	dsn := "file:" + cfg.DBPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sqlx.Connect("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	// Bounded pool, sourced from config (never hardcoded).
	db.DB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.DB.SetMaxIdleConns(cfg.DBMaxIdleConns)

	return db, nil
}