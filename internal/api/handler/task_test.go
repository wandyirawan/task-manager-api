package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/handler"
	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

// handlerFakeRepo is a tiny in-memory TaskRepository used to back the real
// service so the handler test exercises the whole parse→service→respond path.
type handlerFakeRepo struct {
	tasks map[string]*domain.Task
}

func newHandlerFakeRepo() *handlerFakeRepo {
	return &handlerFakeRepo{tasks: map[string]*domain.Task{}}
}

func (r *handlerFakeRepo) Create(ctx context.Context, task *domain.Task) error {
	cpy := *task
	r.tasks[task.ID] = &cpy
	return nil
}

func (r *handlerFakeRepo) GetByID(ctx context.Context, ownerID, taskID string) (*domain.Task, error) {
	t, ok := r.tasks[taskID]
	if !ok || t.OwnerID != ownerID {
		return nil, domain.ErrNotFound
	}
	cpy := *t
	return &cpy, nil
}

func (r *handlerFakeRepo) List(ctx context.Context, f domain.TaskFilter) ([]domain.Task, int, error) {
	var out []domain.Task
	for _, t := range r.tasks {
		if t.OwnerID != f.OwnerID {
			continue
		}
		if f.Status.Valid() && t.Status != f.Status {
			continue
		}
		out = append(out, *t)
	}
	return out, len(out), nil
}

func (r *handlerFakeRepo) Update(ctx context.Context, ownerID, taskID string, in domain.UpdateTaskInput) (*domain.Task, error) {
	t, ok := r.tasks[taskID]
	if !ok || t.OwnerID != ownerID {
		return nil, domain.ErrNotFound
	}
	if in.Title != nil {
		t.Title = *in.Title
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.Status != nil {
		t.Status = *in.Status
	}
	cpy := *t
	return &cpy, nil
}

func (r *handlerFakeRepo) Delete(ctx context.Context, ownerID, taskID string) error {
	t, ok := r.tasks[taskID]
	if !ok || t.OwnerID != ownerID {
		return domain.ErrNotFound
	}
	delete(r.tasks, taskID)
	return nil
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// newHandlerApp builds a fiber app wired exactly like production: ErrorHandler
// (P2) + a FAKE auth middleware that injects user_id into Locals (stand-in for
// the P3 Protected middleware, NOT yet integrated). If userID is "", no Locals
// is set so the handler's 401 path is exercised.
func newHandlerApp(t *testing.T, userID string) *fiber.App {
	t.Helper()
	lg := slog.New(slog.NewTextHandler(nopWriter{}, nil))
	svc := service.NewTaskService(newHandlerFakeRepo(), lg)

	app := fiber.New(fiber.Config{ErrorHandler: api.NewErrorHandler("dev")})

	app.Use(func(c fiber.Ctx) error {
		if userID != "" {
			c.Locals("user_id", userID)
		}
		return c.Next()
	})

	group := app.Group("/tasks")
	handler.RegisterRoutes(group, handler.NewTaskHandler(svc))
	return app
}

func doReq(t *testing.T, app *fiber.App, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
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

func decode(t *testing.T, data []byte, out any) {
	t.Helper()
	if len(data) == 0 {
		return
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
}

func createTask(t *testing.T, app *fiber.App) *domain.Task {
	t.Helper()
	_, data := doReq(t, app, "POST", "/tasks",
		`{"title":"Write docs","description":"s2"}`)
	var env struct {
		Data domain.Task `json:"data"`
	}
	decode(t, data, &env)
	if env.Data.ID == "" {
		t.Fatalf("create did not set id: %s", data)
	}
	return &env.Data
}

func TestCreateTask(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	status, _ := doReq(t, app, "POST", "/tasks",
		`{"title":"Write docs","description":"s1"}`)
	if status != fiber.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	createTask(t, app)
}

func TestCreateValidationBadRequest(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	status, data := doReq(t, app, "POST", "/tasks", `{"title":""}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, data)
	}
	var errResp struct {
		Code string `json:"code"`
	}
	decode(t, data, &errResp)
	if errResp.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", errResp.Code)
	}
}

func TestGetTask(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	tsk := createTask(t, app)

	status, data := doReq(t, app, "GET", "/tasks/"+tsk.ID, "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var env struct {
		Data domain.Task `json:"data"`
	}
	decode(t, data, &env)
	if env.Data.ID != tsk.ID {
		t.Errorf("id = %q, want %q", env.Data.ID, tsk.ID)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	status, data := doReq(t, app, "GET", "/tasks/nope", "")
	if status != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", status, data)
	}
	var errResp struct {
		Code string `json:"code"`
	}
	decode(t, data, &errResp)
	if errResp.Code != "TASK_NOT_FOUND" {
		t.Errorf("code = %q, want TASK_NOT_FOUND", errResp.Code)
	}
}

func TestListTasks(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	createTask(t, app)
	createTask(t, app)

	status, data := doReq(t, app, "GET", "/tasks", "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var env struct {
		Data []domain.Task `json:"data"`
		Meta struct {
			Page  int `json:"page"`
			Limit int `json:"limit"`
			Total int `json:"total"`
		} `json:"meta"`
	}
	decode(t, data, &env)
	if env.Meta.Total != 2 || len(env.Data) != 2 {
		t.Errorf("total=%d len=%d, want 2/2", env.Meta.Total, len(env.Data))
	}
	if env.Meta.Page != 1 || env.Meta.Limit != 10 {
		t.Errorf("meta = %+v, want page=1 limit=10", env.Meta)
	}
}

func TestUpdateTask(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	tsk := createTask(t, app)

	status, data := doReq(t, app, "PUT", "/tasks/"+tsk.ID, `{"status":"done"}`)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, data)
	}
	var env struct {
		Data domain.Task `json:"data"`
	}
	decode(t, data, &env)
	if env.Data.Status != domain.TaskStatusDone {
		t.Errorf("status = %q, want done", env.Data.Status)
	}
	// Title unchanged.
	if env.Data.Title != tsk.Title {
		t.Errorf("title = %q, want %q (unchanged)", env.Data.Title, tsk.Title)
	}
}

func TestUpdateTaskNotFound(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	status, _ := doReq(t, app, "PUT", "/tasks/nope", `{"title":"x"}`)
	if status != fiber.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestUpdateTaskValidationBadStatus(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	tsk := createTask(t, app)

	status, data := doReq(t, app, "PUT", "/tasks/"+tsk.ID, `{"status":"nope"}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, data)
	}
}

func TestDeleteTask(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	tsk := createTask(t, app)

	status, data := doReq(t, app, "DELETE", "/tasks/"+tsk.ID, "")
	if status != fiber.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", status, data)
	}

	// Confirm gone.
	status, _ = doReq(t, app, "GET", "/tasks/"+tsk.ID, "")
	if status != fiber.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", status)
	}
}

func TestNoUserIDUnauthorized(t *testing.T) {
	app := newHandlerApp(t, "") // no Locals set → handler must reject 401

	status, data := doReq(t, app, "GET", "/tasks", "")
	if status != fiber.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", status, data)
	}
	var errResp struct {
		Code string `json:"code"`
	}
	decode(t, data, &errResp)
	if errResp.Code != "UNAUTHORIZED" {
		t.Errorf("code = %q, want UNAUTHORIZED", errResp.Code)
	}
}
