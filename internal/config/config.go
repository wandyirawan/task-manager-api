package config

import (
	"errors"

	"github.com/caarlos0/env/v11"
)

var errJWTSecretMissing = errors.New("JWT_SECRET environment variable is required")

// Config holds application configuration parsed from environment variables.
type Config struct {
	Port           int    `env:"PORT"`
	Env            string `env:"ENV"`
	JWTSecret      string `env:"JWT_SECRET"`
	DBURL          string `env:"DB_URL"`
	DBMaxOpenConns int    `env:"DB_MAX_OPEN_CONNS"`
	DBMaxIdleConns int    `env:"DB_MAX_IDLE_CONNS"`
}

// New returns a Config with defaults filled, then calls Load.
func New() (*Config, error) {
	c := &Config{
		Port:           8080,
		Env:            "dev",
		DBURL:          "postgres://tm_user:tm_pass@localhost:5432/tmapi?sslmode=disable",
		DBMaxOpenConns: 10,
		DBMaxIdleConns: 10,
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

// Load parses env vars and validates. Returns error on missing required fields.
func (c *Config) load() error {
	if err := env.Parse(c); err != nil {
		return err
	}

	// Fail-fast: JWT_SECRET is required (no default).
	if c.JWTSecret == "" {
		return errJWTSecretMissing
	}

	// Env must be dev or prod.
	if c.Env != "dev" && c.Env != "prod" {
		return errors.New("ENV must be 'dev' or 'prod'")
	}

	return nil
}
