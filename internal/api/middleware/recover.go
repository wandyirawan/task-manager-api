package middleware

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/gofiber/fiber/v3"
)

// Recover turns a handler panic into a consistent 500 via the app's error
// handler, and logs the stack trace (SPEC §8: recovery middleware).
func Recover(logger *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered",
					"request_id", c.Locals("request_id"),
					"method", c.Method(),
					"path", c.Path(),
					"panic", r,
					"stack", string(debug.Stack()),
				)
				// Route through the configured error handler to keep the
				// JSON error shape identical to every other failure.
				err = fmt.Errorf("panic recovered: %v", r)
			}
		}()
		return c.Next()
	}
}