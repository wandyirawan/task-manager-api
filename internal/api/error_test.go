package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/domain"
)

func newApp(env string) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: api.NewErrorHandler(env),
	})
	return app
}

func TestErrorHandler_SentinelMappings(t *testing.T) {
	cases := []struct {
		name     string
		handler  func(fiber.Ctx) error
		wantCode string
	}{
		{
			name:     "validation",
			handler:  func(c fiber.Ctx) error { return domain.ErrValidation },
			wantCode: "VALIDATION_ERROR",
		},
		{
			name:     "unauthorized",
			handler:  func(c fiber.Ctx) error { return domain.ErrUnauthorized },
			wantCode: "UNAUTHORIZED",
		},
		{
			name:     "not_found",
			handler:  func(c fiber.Ctx) error { return domain.ErrNotFound },
			wantCode: "TASK_NOT_FOUND",
		},
		{
			name:     "internal",
			handler:  func(c fiber.Ctx) error { return domain.ErrInternal },
			wantCode: "INTERNAL_ERROR",
		},
		{
			name:     "wrapped_internal",
			handler:  func(c fiber.Ctx) error { return errors.Join(domain.ErrInternal) },
			wantCode: "INTERNAL_ERROR",
		},
		{
			name:     "unknown_error_defaults_500",
			handler:  func(c fiber.Ctx) error { return errors.New("boom") },
			wantCode: "INTERNAL_ERROR",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newApp("dev")
			app.Get("/", tc.handler)

			resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := body["code"]; got != tc.wantCode {
				t.Errorf("code = %v, want %s", got, tc.wantCode)
			}
			if _, ok := body["timestamp"]; !ok {
				t.Error("missing timestamp field")
			}
			if _, ok := body["message"]; !ok {
				t.Error("missing message field")
			}
			if _, ok := body["status"]; !ok {
				t.Error("missing status field")
			}
		})
	}
}

func TestErrorHandler_ProdMasks5xx(t *testing.T) {
	app := newApp("prod")
	app.Get("/", func(c fiber.Ctx) error { return domain.ErrInternal })

	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	msg := body["message"].(string)
	if msg != "internal server error" {
		t.Errorf("prod 5xx message = %q, want generic mask", msg)
	}
	if !strings.Contains(msg, "server error") {
		t.Error("prod 5xx should not leak internal detail")
	}
}

func TestErrorHandler_DevShowsDetail(t *testing.T) {
	app := newApp("dev")
	app.Get("/", func(c fiber.Ctx) error { return domain.ErrValidation })

	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg := body["message"].(string); msg != domain.ErrValidation.Error() {
		t.Errorf("dev message = %q, want %q", msg, domain.ErrValidation.Error())
	}
}