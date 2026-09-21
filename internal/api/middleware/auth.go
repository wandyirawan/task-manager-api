package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

const (
	fiberHeaderAuthorization = "Authorization"
	fiberBearerScheme        = "Bearer "
	localsUserID             = "user_id"
)

// Protected returns a middleware that requires a valid `Authorization:
// Bearer <token>` header. On success the user_id is stored on c.Locals and in
// the request context. On any failure the middleware returns a sentinel so the
// request flows through the app's error handler pipeline (SPEC §7) — the JSON
// error shape stays consistent with every other error; no ad-hoc response.
//
// Place after RequestID and RequestLogger, before handler registration.
func Protected(secret string) fiber.Handler {
	svc := infra.NewJWTService(secret, 0)

	return func(c fiber.Ctx) error {
		header := c.Get(fiberHeaderAuthorization)
		token, ok := strings.CutPrefix(header, fiberBearerScheme)
		if !ok || token == "" {
			// Missing or malformed Authorization header → generic unauthorized.
			return fiber.NewError(fiber.StatusUnauthorized, "missing or malformed bearer token")
		}

		userID, err := svc.VerifyToken(token)
		if err != nil {
			// Send the error through the error-handler pipeline. domain.ErrUnauthorized
			// maps to 401 UNAUTHORIZED (errors.Is unwraps the wrapped sentinel).
			return err
		}

		c.Locals(localsUserID, userID)
		c.SetContext(api.WithUserID(c.Context(), userID))
		return c.Next()
	}
}