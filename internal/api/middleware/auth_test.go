package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/middleware"
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

const authTestSecret = "auth-test-secret-hs256"

// authApp wires the app with the error handler pipeline + Protected middleware
// so error responses flow through the same ErrorResponse JSON contract.
func authApp(handler fiber.Handler) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: api.NewErrorHandler("dev"),
	})
	app.Use(middleware.Protected(authTestSecret))
	app.Get("/protected", handler)
	return app
}

func newValidToken(t *testing.T, userID string) string {
	t.Helper()
	svc := infra.NewJWTService(authTestSecret, time.Hour)
	token, err := svc.GenerateToken(userID)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func TestProtected_MissingHeader(t *testing.T) {
	app := authApp(func(c fiber.Ctx) error { return c.SendString("ok") })

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/protected", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	assertErrorShape(t, resp, "UNAUTHORIZED")
}

func TestProtected_ValidTokenSetsUserID(t *testing.T) {
	app := authApp(func(c fiber.Ctx) error {
		userID := c.Locals("user_id").(string)
		if userID != "user-abc" {
			return fiber.NewError(fiber.StatusInternalServerError, "wrong user_id")
		}
		// Ensure the same ID propagates through the request context (SPEC §9).
		if got := api.UserIDFromContext(c.Context()); got != "user-abc" {
			return fiber.NewError(fiber.StatusInternalServerError, "context user_id mismatch")
		}
		return c.SendString(userID)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+newValidToken(t, "user-abc"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestProtected_InvalidToken(t *testing.T) {
	app := authApp(func(c fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer not.a.valid.token")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	assertErrorShape(t, resp, "UNAUTHORIZED")
}

func TestProtected_MalformedHeader(t *testing.T) {
	app := authApp(func(c fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	assertErrorShape(t, resp, "UNAUTHORIZED")
}

// assertErrorShape verifies the body matches the ErrorResponse JSON contract
// {status, code, message, timestamp} with the expected code.
func assertErrorShape(t *testing.T, resp *http.Response, wantCode string) {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if got := body["code"]; got != wantCode {
		t.Errorf("code = %v, want %s", got, wantCode)
	}
	for _, field := range []string{"status", "message", "timestamp"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing %q field", field)
		}
	}
}