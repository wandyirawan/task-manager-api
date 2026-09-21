package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/handler"
	"github.com/wandyirawan/task-manager-api/internal/api/middleware"
	"github.com/wandyirawan/task-manager-api/internal/config"
	"github.com/wandyirawan/task-manager-api/internal/infra"
	"github.com/wandyirawan/task-manager-api/internal/repository"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

const tokenTTL = 24 * time.Hour // SPEC §7 — nyambung ke window idempotency 24 jam

func main() {
	cfg, err := config.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: failed to load config: %v\n", err)
		os.Exit(1)
	}

	logger := infra.NewLogger(cfg.Env)
	logger.Info("config loaded", "port", cfg.Port, "env", cfg.Env)

	// --- infrastructure wiring (composition root) ---
	db, err := infra.NewDB(cfg)
	if err != nil {
		logger.Error("db connection failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Fail-fast: schema must be ready before serving traffic. Embedded
	// migrations make the binary self-contained (distroless-friendly).
	if err := infra.RunMigrations(db); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}
	logger.Info("migrations up to date")

	jwtSvc := infra.NewJWTService(cfg.JWTSecret, tokenTTL)

	// --- handlers & services ---
	taskSvc := service.NewTaskService(repository.NewTaskRepository(db), logger)
	taskHandler := handler.NewTaskHandler(taskSvc)

	// Public auth: register/login (SPEC §7) — no auth middleware required.
	authSvc := service.NewAuthService(repository.NewUserRepository(db), jwtSvc, logger)
	authHandler := handler.NewAuthHandler(authSvc)

	// --- fiber app + middleware chain (SPEC §9: request_id → logging → recovery; auth per group) ---
	app := fiber.New(fiber.Config{
		ErrorHandler: api.NewErrorHandler(cfg.Env),
	})

	app.Use(middleware.RequestID())
	app.Use(middleware.RequestLogger(logger))
	app.Use(middleware.Recover(logger))
	app.Get("/healthz", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	// Public auth routes on the app directly, before the protected /tasks group.
	handler.RegisterAuthRoutes(app, authHandler)

	// Protected task routes (P3 middleware injects user_id → P4 handler reads it).
	protected := app.Group("/tasks", middleware.Protected(jwtSvc))
	handler.RegisterRoutes(protected, taskHandler)

	// --- graceful shutdown (SIGTERM/SIGINT) ---
	go func() {
		if err := app.Listen(fmt.Sprintf(":%d", cfg.Port)); err != nil {
			logger.Error("server stopped", "error", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down gracefully")

	if err := app.Shutdown(); err != nil {
		logger.Error("shutdown error", "error", err)
	}
}
