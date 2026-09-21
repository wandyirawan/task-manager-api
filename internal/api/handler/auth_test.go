package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/handler"
	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/infra"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

// handlerFakeUsers is an in-memory UserStore backing the real AuthService so
// the handler test drives the full parse→service→respond path end to end.
type handlerFakeUsers struct {
	users map[string]*domain.User // keyed by ID, email map built for lookup
	byEmail map[string]*domain.User
}

func newHandlerFakeUsers() *handlerFakeUsers {
	return &handlerFakeUsers{users: map[string]*domain.User{}, byEmail: map[string]*domain.User{}}
}

func (s *handlerFakeUsers) Create(_ context.Context, u *domain.User) error {
	if _, exists := s.byEmail[u.Email]; exists {
		return domain.ErrEmailTaken
	}
	cpy := *u
	s.users[u.ID] = &cpy
	s.byEmail[u.Email] = &cpy
	return nil
}

func (s *handlerFakeUsers) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	if u, ok := s.byEmail[email]; ok {
		cpy := *u
		return &cpy, nil
	}
	return nil, nil
}

func (s *handlerFakeUsers) GetByID(_ context.Context, id string) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		cpy := *u
		return &cpy, nil
	}
	return nil, nil
}

// newAuthApp wires a fiber app with the P2 error handler + public auth routes,
// exactly like production main.go wiring for the /register and /login paths.
func newAuthApp(t *testing.T) (*fiber.App, *handlerFakeUsers) {
	t.Helper()
	lg := slog.New(slog.NewTextHandler(nopWriter{}, nil))
	store := newHandlerFakeUsers()
	svc := service.NewAuthService(store, infra.NewJWTService("handler-test-secret", time.Hour), lg)

	app := fiber.New(fiber.Config{ErrorHandler: api.NewErrorHandler("dev")})
	handler.RegisterAuthRoutes(app, handler.NewAuthHandler(svc))
	return app, store
}

func doAuthReq(t *testing.T, app *fiber.App, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, data
}

type authEnv struct {
	Data struct {
		User  domain.User `json:"user"`
		Token string      `json:"token"`
	} `json:"data"`
}

const validPassword = "hunter2secure"

func TestRegisterReturns201TokenAndHidesHash(t *testing.T) {
	app, _ := newAuthApp(t)

	status, data := doAuthReq(t, app, "/register",
		`{"email":"fresh@example.com","password":"`+validPassword+`"}`)
	if status != fiber.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", status, data)
	}

	var env authEnv
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode: %v (%s)", err, data)
	}
	if env.Data.User.ID == "" {
		t.Error("user.id is empty")
	}
	if env.Data.User.Email != "fresh@example.com" {
		t.Errorf("user.email = %q, want fresh@example.com", env.Data.User.Email)
	}
	if env.Data.Token == "" {
		t.Error("token is empty")
	}
	// The bcrypt digest must NEVER appear in the response body.
	raw := string(data)
	if strings.Contains(raw, "$2a$") || strings.Contains(raw, "$2b$") ||
		strings.Contains(strings.ToLower(raw), "passwordhash") || strings.Contains(raw, "PasswordHash") {
		t.Errorf("password hash leaked in response: %s", raw)
	}
}

func TestRegisterDuplicateEmailConflict(t *testing.T) {
	app, _ := newAuthApp(t)

	if status, _ := doAuthReq(t, app, "/register",
		`{"email":"dup@example.com","password":"`+validPassword+`"}`); status != fiber.StatusCreated {
		t.Fatalf("first register status = %d, want 201", status)
	}

	status, data := doAuthReq(t, app, "/register",
		`{"email":"dup@example.com","password":"`+validPassword+`"}`)
	if status != fiber.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", status, data)
	}
	var errResp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(data, &errResp); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if errResp.Code != "EMAIL_TAKEN" {
		t.Errorf("code = %q, want EMAIL_TAKEN", errResp.Code)
	}
}

func TestLoginReturns200AndToken(t *testing.T) {
	app, _ := newAuthApp(t)

	doAuthReq(t, app, "/register", `{"email":"login@example.com","password":"`+validPassword+`"}`)

	status, data := doAuthReq(t, app, "/login",
		`{"email":"login@example.com","password":"`+validPassword+`"}`)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, data)
	}
	var env authEnv
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Token == "" {
		t.Error("token is empty")
	}
	if env.Data.User.Email != "login@example.com" {
		t.Errorf("email = %q", env.Data.User.Email)
	}
}

func TestLoginWrongPasswordUnauthorized(t *testing.T) {
	app, _ := newAuthApp(t)
	doAuthReq(t, app, "/register", `{"email":"wrong@example.com","password":"`+validPassword+`"}`)

	status, data := doAuthReq(t, app, "/login",
		`{"email":"wrong@example.com","password":"not-the-secret"}`)
	if status != fiber.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", status, data)
	}
	var errResp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(data, &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Code != "UNAUTHORIZED" {
		t.Errorf("code = %q, want UNAUTHORIZED", errResp.Code)
	}
}

func TestAuthValidationBadRequest(t *testing.T) {
	app, _ := newAuthApp(t)

	cases := []string{
		`{"email":"nope","password":"` + validPassword + `"}`, // bad email
		`{"email":"ok@example.com","password":"short"}`,         // short password
		`{"email":"","password":""}`,                            // empty
		`{invalid json`,                                         // malformed body
	}
	for _, body := range cases {
		status, data := doAuthReq(t, app, "/register", body)
		if status != fiber.StatusBadRequest {
			t.Errorf("register(%s) status = %d, want 400 (body %s)", body, status, data)
		}
	}
}