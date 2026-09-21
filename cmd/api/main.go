package main

import (
	"fmt"
	"os"

	"github.com/wandyirawan/task-manager-api/internal/config"
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

func main() {
	cfg, err := config.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: failed to load config: %v\n", err)
		os.Exit(1)
	}

	logger := infra.NewLogger(cfg.Env)
	logger.Info("config loaded", "port", cfg.Port, "env", cfg.Env)
	logger.Info("P4: router belum di-wire — placeholder main")
}
