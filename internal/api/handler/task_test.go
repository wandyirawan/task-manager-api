package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/wandyirawan/task-manager-api/internal/api"
	"github.com/wandyirawan/task-manager-api/internal/api/handler"
	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

// noopNotifier does nothing — used by handler tests when they don't care about notifications.
type noopNotifier struct{}

func (noopNotifier) Notify(_ context.Context, _, _, _ string) error { return nil }

// handlerFakeRepo is a tiny in-memory TaskRepository used to back the real
// service so the handler test exercises the whole parse→service→respond path.
// It also implements service.IdempotencyStore so the production
// NewTaskService wiring (which recovers the IdempotencyStore from the same repo
// object) is exercised here too.
type handlerFakeRepo struct {
	tasks    map[string]*domain.Task
	keys     map[string]*domain.IdempotencyRecord
	assigned map[string]string // taskID → assigneeID
}

func newHandlerFakeRepo() *handlerFakeRepo {
	return &handlerFakeRepo{
		tasks:    map[string]*domain.Task{},
		keys:     map[string]*domain.IdempotencyRecord{},
		assigned: map[string]string{},
	}
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

// LookupByKey implements service.IdempotencyStore: read-only fetch, expired →
// treated as not-found.
func (r *handlerFakeRepo) LookupByKey(ctx context.Context, key, userID string) (*domain.IdempotencyRecord, error) {
	rec, ok := r.keys[userID+"|"+key]
	if !ok {
		return nil, nil
	}
	if rec.ExpiresAt.Before(time.Now()) {
		return nil, nil
	}
	return rec, nil
}

// CreateTaskWithKey implements service.IdempotencyStore: atomic-feeling insert
// of task + snapshot, with a UNIQUE (key,userID) race backstop.
func (r *handlerFakeRepo) CreateTaskWithKey(ctx context.Context, task *domain.Task, key string) (*domain.Task, bool, error) {
	k := task.OwnerID + "|" + key
	if existing, ok := r.keys[k]; ok {
		var t domain.Task
		if err := json.Unmarshal([]byte(existing.ResponseBody), &t); err != nil {
			return nil, false, err
		}
		return &t, false, nil
	}
	body, err := json.Marshal(task)
	if err != nil {
		return nil, false, err
	}
	cpy := *task
	r.tasks[task.ID] = &cpy
	r.keys[k] = &domain.IdempotencyRecord{
		Key:            key,
		UserID:         task.OwnerID,
		TaskID:         task.ID,
		ResponseStatus: 201,
		ResponseBody:   string(body),
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}
	return &cpy, true, nil
}

// Assign implements service.TaskRepository.Assign for handler tests.
func (r *handlerFakeRepo) Assign(ctx context.Context, ownerID, taskID, assigneeID string) error {
	t, ok := r.tasks[taskID]
	if !ok || t.OwnerID != ownerID {
		return domain.ErrForbidden
	}
	t.AssigneeID = &assigneeID
	r.assigned[taskID] = assigneeID
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
	svc := service.NewTaskService(newHandlerFakeRepo(), lg, noopNotifier{})

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

// doReq issues a request. headers is a variadic list of [name, value] pairs
// (e.g. an Idempotency-Key header).
func doReq(t *testing.T, app *fiber.App, method, path, body string, headers ...[2]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
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

// createTask posts a task with a fresh idempotency key and returns the created
// task. Every POST /tasks now requires an Idempotency-Key header (P5).
func createTask(t *testing.T, app *fiber.App) *domain.Task {
	t.Helper()
	_, data := doReq(t, app, "POST", "/tasks",
		`{"title":"Write docs","description":"s2"}`,
		[2]string{"Idempotency-Key", uuid.NewString()})
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
		`{"title":"Write docs","description":"s1"}`,
		[2]string{"Idempotency-Key", uuid.NewString()})
	if status != fiber.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	createTask(t, app)
}

func TestCreateValidationBadRequest(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	// Valid idempotency key first so we actually reach the validation check.
	status, data := doReq(t, app, "POST", "/tasks", `{"title":""}`,
		[2]string{"Idempotency-Key", uuid.NewString()})
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

// --- P5 idempotency handler tests ---

func TestCreateMissingIdempotencyKey(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	status, data := doReq(t, app, "POST", "/tasks", `{"title":"x"}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, data)
	}
	var errResp struct {
		Code string `json:"code"`
	}
	decode(t, data, &errResp)
	if errResp.Code != "INVALID_IDEMPOTENCY_KEY" {
		t.Errorf("code = %q, want INVALID_IDEMPOTENCY_KEY", errResp.Code)
	}
}

func TestCreateInvalidIdempotencyKey(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	// Non-UUID key → 400 INVALID_IDEMPOTENCY_KEY (strict, no body hash).
	status, data := doReq(t, app, "POST", "/tasks", `{"title":"x"}`,
		[2]string{"Idempotency-Key", "abc"})
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, data)
	}
	var errResp struct {
		Code string `json:"code"`
	}
	decode(t, data, &errResp)
	if errResp.Code != "INVALID_IDEMPOTENCY_KEY" {
		t.Errorf("code = %q, want INVALID_IDEMPOTENCY_KEY", errResp.Code)
	}
}

func TestCreateValidIdempotencyKey(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	key := uuid.NewString()
	status, data := doReq(t, app, "POST", "/tasks", `{"title":"docs","description":"d"}`,
		[2]string{"Idempotency-Key", key})
	if status != fiber.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", status, data)
	}
	var env struct {
		Data domain.Task `json:"data"`
	}
	decode(t, data, &env)
	if env.Data.ID == "" || env.Data.Title != "docs" {
		t.Errorf("unexpected task: %+v (body %s)", env.Data, data)
	}
}

func TestCreateReplayIdenticalBody(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	key := uuid.NewString()
	_, first := doReq(t, app, "POST", "/tasks", `{"title":"replay me","description":"d"}`,
		[2]string{"Idempotency-Key", key})
	_, second := doReq(t, app, "POST", "/tasks", `{"title":"replay me","description":"d"}`,
		[2]string{"Idempotency-Key", key})

	// Replay must be byte-for-byte identical to the original response.
	if string(first) != string(second) {
		t.Errorf("replay body not identical:\n first=%s\nsecond=%s", first, second)
	}
}

func TestCreateReplayAfterMutation(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	key := uuid.NewString()
	_, first := doReq(t, app, "POST", "/tasks", `{"title":"snapshot","description":"d"}`,
		[2]string{"Idempotency-Key", key})

	var env struct {
		Data domain.Task `json:"data"`
	}
	decode(t, first, &env)
	taskID := env.Data.ID

	// Mutate the task via PUT.
	if status, _ := doReq(t, app, "PUT", "/tasks/"+taskID, `{"status":"done"}`); status != fiber.StatusOK {
		t.Fatalf("PUT status = %d, want 200", status)
	}

	// Replay the SAME idempotency key → body must equal the ORIGINAL snapshot,
	// proving it replays the stored snapshot, not a re-query.
	_, replay := doReq(t, app, "POST", "/tasks", `{"title":"snapshot","description":"d"}`,
		[2]string{"Idempotency-Key", key})
	if string(first) != string(replay) {
		t.Errorf("replay after mutation differs from original snapshot:\n orig=%s\nreplay=%s",
			first, replay)
	}
}

// --- P6 assign handler tests ---

func TestAssignTask(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	tsk := createTask(t, app)

	target := uuid.NewString()
	status, data := doReq(t, app, "POST", "/tasks/"+tsk.ID+"/assign",
		`{"assigneeId":"`+target+`"}`)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, data)
	}
	var env struct {
		Data domain.Task `json:"data"`
	}
	decode(t, data, &env)
	if env.Data.AssigneeID == nil || *env.Data.AssigneeID != target {
		t.Errorf("assigneeId = %+v, want %s", env.Data.AssigneeID, target)
	}
}

func TestAssignTaskNotFound(t *testing.T) {
	app := newHandlerApp(t, "user-42")

	status, data := doReq(t, app, "POST", "/tasks/nonexistent/assign",
		`{"assigneeId":"`+uuid.NewString()+`"}`)
	if status != fiber.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", status, data)
	}
}

func TestAssignTaskInvalidUUID(t *testing.T) {
	app := newHandlerApp(t, "user-42")
	tsk := createTask(t, app)

	status, data := doReq(t, app, "POST", "/tasks/"+tsk.ID+"/assign", `{"assigneeId":"not-a-uuid"}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, data)
	}
}

// --- removed debug tests


