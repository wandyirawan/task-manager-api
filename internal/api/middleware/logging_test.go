package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/middleware"
	"github.com/wandyirawan/task-manager-api/internal/domain"
)

// captureLogger builds an slog logger writing JSON to an in-memory buffer and
// returns { logger, lines() }. RequestLogger is exercised against it.
func captureLogger() (*slog.Logger, func() []string) {
	buf := bytes.Buffer{}
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)
	lines := func() []string {
		var out []string
		for _, ln := range strings.Split(buf.String(), "\n") {
			if strings.TrimSpace(ln) != "" {
				out = append(out, ln)
			}
		}
		return out
	}
	return logger, lines
}

func TestRequestLogger_LevelsAndFields(t *testing.T) {
	logger, lines := captureLogger()

	app := fiber.New(fiber.Config{ErrorHandler: api.NewErrorHandler("dev")})
	app.Use(middleware.RequestID())
	app.Use(middleware.RequestLogger(logger))

	app.Get("/ok", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/warn", func(c fiber.Ctx) error { return domain.ErrValidation })
	app.Get("/err", func(c fiber.Ctx) error { return domain.ErrInternal })

	cases := []struct {
		path   string
		status int
	}{
		{"/ok", 200},
		{"/warn", 400},
		{"/err", 500},
	}

	for _, tc := range cases {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, tc.path, nil))
		if err != nil {
			t.Fatalf("%s request: %v", tc.path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s status = %v, want %v", tc.path, resp.StatusCode, tc.status)
		}
	}

	all := lines()
	if len(all) != 3 {
		t.Errorf("expected exactly 3 log entries, got %v", len(all))
	}
	var foundInfo, foundWarn, foundErr bool
	for _, ln := range all {
		if !strings.Contains(ln, "\"request_id\":") {
			t.Error("entry missing request_id")
		}
		if !strings.Contains(ln, "\"latency_ms\":") {
			t.Error("entry missing latency_ms")
		}
		switch {
		case strings.Contains(ln, "\"level\":\"INFO\""):
			foundInfo = true
		case strings.Contains(ln, "\"level\":\"WARN\""):
			foundWarn = true
		case strings.Contains(ln, "\"level\":\"ERROR\""):
			foundErr = true
		}
	}
	if !foundInfo || !foundWarn || !foundErr {
		t.Errorf("expected all three levels; info=%v warn=%v err=%v", foundInfo, foundWarn, foundErr)
	}
}

func TestRequestLogger_AttachesErrorFor5xx(t *testing.T) {
	logger, lines := captureLogger()

	app := fiber.New(fiber.Config{ErrorHandler: api.NewErrorHandler("dev")})
	app.Use(middleware.RequestID())
	app.Use(middleware.RequestLogger(logger))
	app.Get("/err", func(c fiber.Ctx) error { return domain.ErrInternal })

	resp, _ := app.Test(httptest.NewRequest(http.MethodGet, "/err", nil))
	defer resp.Body.Close()

	haveError := false
	for _, ln := range lines() {
		if strings.Contains(ln, "\"error\":") {
			haveError = true
		}
	}
	if !haveError {
		t.Error("5xx entry should include an error field")
	}
}

func TestRequestLogger_UsesRequestID(t *testing.T) {
	logger, lines := captureLogger()

	app := fiber.New(fiber.Config{ErrorHandler: api.NewErrorHandler("dev")})
	app.Use(middleware.RequestID())
	app.Use(middleware.RequestLogger(logger))
	app.Get("/ok", func(c fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("X-Request-Id", "trace-me")
	_, _ = app.Test(req)

	var sawTraceID bool
	for _, ln := range lines() {
		if strings.Contains(ln, "trace-me") {
			sawTraceID = true
		}
	}
	if !sawTraceID {
		t.Error("request logger should include the request_id value")
	}
}