package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/wandyirawan/task-manager-api/internal/api"
)

const fiberHeaderRequestID = "X-Request-ID"

// RequestID assigns a request_id to every request. If the client sends an
// X-Request-ID header its value is honored; otherwise a UUID v4 is generated.
// The ID is mirrored on c.Locals for the request logger and propagated into
// the request context so service/repo layers can log correlated entries.
//
// Place RequestID OUTSIDE RequestLogger so the logger always sees an ID.
func RequestID() fiber.Handler {
	return func(c fiber.Ctx) error {
		id := strings.TrimSpace(c.Get(fiberHeaderRequestID))
		if id == "" {
			id = uuid.New().String()
		}
		c.Locals("request_id", id)
		c.SetContext(api.WithRequestID(c.Context(), id))
		return c.Next()
	}
}