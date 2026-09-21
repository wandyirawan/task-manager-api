package infra

import (
	"log/slog"
	"os"
)

// NewLogger creates an slog.Logger that writes JSON to stdout.
// Level is INFO by default; DEBUG when env is "dev".
func NewLogger(env string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	if env == "dev" {
		opts.Level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	return slog.New(handler)
}
