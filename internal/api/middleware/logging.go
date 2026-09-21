package middleware

import (
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/wandyirawan/task-manager-api/internal/api"
)

// RequestLogger emits exactly ONE slog entry per request (SPEC §9).
//   - INFO  for 2xx/3xx
//   - WARN  for 4xx
//   - ERROR for 5xx
//
// Fields: request_id, method, path, status, latency_ms, and (for 4xx/5xx)
// the underlying error text. Run AFTER RequestID so request_id is populated.
func RequestLogger(logger *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		c.Locals("request_start", start)
		c.SetContext(api.WithRequestID(c.Context(), requestID(c)))

		err := c.Next()

		// On the handler-error path the framework renders the error response
		// AFTER c.Next() returns here, so c.Response().StatusCode() is still
		// 200. Classify the returned error directly to get the status.
		status := c.Response().StatusCode()
		if err != nil {
			status = api.StatusOf(err)
		}
		latency := float64(time.Since(start).Microseconds()) / 1000.0 // ms

		attrs := []any{
			"request_id", requestID(c),
			"method", c.Method(),
			"path", c.Path(),
			"status", status,
			"latency_ms", latency,
		}

		// Attach the underlying error text for WARN/ERROR entries.
		if err != nil {
			attrs = append(attrs, "error", err.Error())
		}

		switch {
		case status >= 500:
			logger.Error("request", attrs...)
		case status >= 400:
			logger.Warn("request", attrs...)
		default:
			logger.Info("request", attrs...)
		}
		return err
	}
}

// requestID reads the request_id from locals with a safe fallback.
func requestID(c fiber.Ctx) string {
	if v, ok := c.Locals("request_id").(string); ok {
		return v
	}
	return c.Get(fiberHeaderRequestID)
}