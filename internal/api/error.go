package api

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/wandyirawan/task-manager-api/internal/domain"
)

// ErrorResponse is the single JSON error shape returned to clients.
// Every error path (sentinel, wrapped, fiber.NeError, panic, 404/405 default)
// flows through NewErrorHandler so the contract stays uniform.
type ErrorResponse struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// code registry — the only error codes exposed to clients (SPEC §8).
const (
	codeValidation   = "VALIDATION_ERROR"
	codeUnauthorized = "UNAUTHORIZED"
	codeNotFound     = "TASK_NOT_FOUND"
	codeForbidden    = "FORBIDDEN"
	codeInternal     = "INTERNAL_ERROR"
	codeEmailTaken   = "EMAIL_TAKEN"
)

// errInfo maps a sentinel (or arbitrary) error to an HTTP status + client code.
// Resolution order: *fiber.Error first (handlers/middleware may raise one),
// then sentinel match via errors.Is, otherwise a generic 500.
type errInfo struct {
	status  int
	message string
}

// NewErrorHandler builds the fiber.Config.ErrorHandler for the app.
// It renders errInfo as ErrorResponse JSON and, in prod, masks 5xx detail.
func NewErrorHandler(env string) fiber.ErrorHandler {
	prod := env == "prod"
	return func(c fiber.Ctx, err error) error {
		info := classify(err)

		code := codeForStatus(info.status)
		message := info.message
		if prod && info.status >= 500 {
			// Never leak internal detail in production.
			message = "internal server error"
		}

		return c.Status(info.status).JSON(ErrorResponse{
			Status:    info.status,
			Code:      code,
			Message:   message,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// classify determines the HTTP status + user-facing message for err.
func classify(err error) errInfo {
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return errInfo{status: fe.Code, message: fe.Message}
	}
	switch {
	case errors.Is(err, domain.ErrValidation):
		return errInfo{status: fiber.StatusBadRequest, message: domain.ErrValidation.Error()}
	case errors.Is(err, domain.ErrUnauthorized):
		return errInfo{status: fiber.StatusUnauthorized, message: domain.ErrUnauthorized.Error()}
	case errors.Is(err, domain.ErrNotFound):
		return errInfo{status: fiber.StatusNotFound, message: domain.ErrNotFound.Error()}
	case errors.Is(err, domain.ErrEmailTaken):
		return errInfo{status: fiber.StatusConflict, message: domain.ErrEmailTaken.Error()}
	case errors.Is(err, domain.ErrInternal):
		return errInfo{status: fiber.StatusInternalServerError, message: domain.ErrInternal.Error()}
	default:
		return errInfo{status: fiber.StatusInternalServerError, message: "internal server error"}
	}
}

// StatusOf reports the HTTP status an error should map to, mirroring the
// error handler's classification. Exported so the request-logging middleware
// can pick the right log level for handler errors without duplicating the map.
func StatusOf(err error) int {
	return classify(err).status
}

// codeForStatus maps an HTTP status to the canonical client code for the
// statuses that have a dedicated code. Fallback is a generic error code.
func codeForStatus(status int) string {
	switch status {
	case fiber.StatusBadRequest:
		return codeValidation
	case fiber.StatusUnauthorized:
		return codeUnauthorized
	case fiber.StatusForbidden:
		return codeForbidden
	case fiber.StatusNotFound:
		return codeNotFound
	case fiber.StatusConflict:
		return codeEmailTaken
	default:
		return codeInternal
	}
}