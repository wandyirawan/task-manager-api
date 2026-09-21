package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/middleware"
)

func TestRequestID_GeneratesUUID(t *testing.T) {
	app := fiber.New()
	app.Use(middleware.RequestID())
	app.Get("/", func(c fiber.Ctx) error {
		id, ok := c.Locals("request_id").(string)
		if !ok || id == "" {
			return fiber.NewError(fiber.StatusInternalServerError, "no request_id")
		}
		// UUID v4 shape: 8-4-4-4-12 hex groups.
		if len(id) != 36 {
			return fiber.NewError(fiber.StatusInternalServerError, "not uuid: "+id)
		}
		return c.SendString("ok")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %v", resp.StatusCode)
	}
}

func TestRequestID_EchoesClientHeader(t *testing.T) {
	app := fiber.New()
	app.Use(middleware.RequestID())
	app.Get("/", func(c fiber.Ctx) error {
		id, _ := c.Locals("request_id").(string)
		return c.SendString(id)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "client-supplied-id")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "client-supplied-id" {
		t.Errorf("echoed = %q, want client-supplied-id", body)
	}
}

func TestRequestID_PropagatesIntoContext(t *testing.T) {
	app := fiber.New()
	app.Use(middleware.RequestID())
	app.Get("/", func(c fiber.Ctx) error {
		id := c.Locals("request_id").(string)
		got := api.RequestIDFromContext(c.Context())
		if got != id {
			return fiber.NewError(fiber.StatusInternalServerError, "context id mismatch")
		}
		return c.SendString("ok")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %v", resp.StatusCode)
	}
}