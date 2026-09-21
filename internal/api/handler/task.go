package handler

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

// localsUserID is the Locals key the auth middleware (P3) populates with the
// authenticated user ID. Must match middleware/auth.go.
const localsUserID = "user_id"

// TaskHandler is the thin HTTP adapter for task endpoints: parse → service →
// respond. It holds NO business logic and NO SQL — caller ID comes from Locals,
// which the protected middleware injects.
type TaskHandler struct {
	svc *service.TaskService
}

// NewTaskHandler builds a TaskHandler backed by the given service.
func NewTaskHandler(svc *service.TaskService) *TaskHandler {
	return &TaskHandler{svc: svc}
}

// RegisterRoutes mounts the task routes onto a (later protected) group.
// POST "" → POST /tasks; GET "" → GET /tasks; etc., so the parent can group
// them under /tasks and apply the auth middleware.
func RegisterRoutes(app fiber.Router, h *TaskHandler) {
	app.Post("", h.Create)
	app.Get("", h.List)
	app.Get("/:id", h.Get)
	app.Put("/:id", h.Update)
	app.Delete("/:id", h.Delete)
}

// userID extracts the authenticated caller from Locals, mirroring the contract
// established by the P3 auth middleware. Absent → unauthorized.
func userID(c fiber.Ctx) (string, error) {
	id, ok := c.Locals(localsUserID).(string)
	if !ok || id == "" {
		return "", domain.ErrUnauthorized
	}
	return id, nil
}

// Create godoc
// @Summary      Create task
// @Description  Create a new task owned by the authenticated user. Idempotent:
// the Idempotency-Key header (a UUID v4) guarantees the same request replayed
// returns the identical 201 response. Missing or non-UUID key → 400
// INVALID_IDEMPOTENCY_KEY.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header string true "Idempotency key (UUID v4)"
// @Param        task body domain.CreateTaskInput true "Task payload"
// @Success      201 {object} map[string]interface{}
// @Failure      400 {object} api.ErrorResponse
// @Router       /tasks [post]
func (h *TaskHandler) Create(c fiber.Ctx) error {
	ownerID, err := userID(c)
	if err != nil {
		return err
	}

	// Idempotency-Key is mandatory and MUST be a UUID v4 (strict — no body hash).
	key := c.Get("Idempotency-Key")
	if key == "" {
		return domain.ErrInvalidIdempotencyKey
	}
	if _, err := uuid.Parse(key); err != nil {
		return domain.ErrInvalidIdempotencyKey
	}

	var in domain.CreateTaskInput
	if err := c.Bind().JSON(&in); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrValidation, err)
	}

	task, _, err := h.svc.CreateIdempotent(c.Context(), ownerID, key, in)
	if err != nil {
		return err
	}

	// Marshal the task once, then wrap it in the {"data": ...} envelope with the
	// SAME code on both the fresh and replay paths so the bytes are identical.
	raw, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("idempotent create marshal: %w", err)
	}
	envelope, err := json.Marshal(struct {
		Data json.RawMessage `json:"data"`
	}{Data: raw})
	if err != nil {
		return fmt.Errorf("idempotent create envelope: %w", err)
	}

	return c.Status(fiber.StatusCreated).Send(envelope)
}

// Get godoc
// @Summary      Get task
// @Description  Get one task owned by the authenticated user.
// @Tags         tasks
// @Produce      json
// @Param        id path string true "Task ID"
// @Success      200 {object} map[string]interface{}
// @Failure      404 {object} api.ErrorResponse
// @Router       /tasks/{id} [get]
func (h *TaskHandler) Get(c fiber.Ctx) error {
	ownerID, err := userID(c)
	if err != nil {
		return err
	}

	task, err := h.svc.Get(c.Context(), ownerID, c.Params("id"))
	if err != nil {
		return err
	}

	return c.JSON(fiber.Map{"data": task})
}

// List godoc
// @Summary      List tasks
// @Description  List the authenticated user's tasks with optional status
// filter, title search, and pagination.
// @Tags         tasks
// @Produce      json
// @Param        status query string false "Filter by status"
// @Param        search query string false "Title search term"
// @Param        page  query int    false "Page number (>=1)"
// @Param        limit query int    false "Page size (1-100)"
// @Success      200 {object} map[string]interface{}
// @Router       /tasks [get]
func (h *TaskHandler) List(c fiber.Ctx) error {
	ownerID, err := userID(c)
	if err != nil {
		return err
	}

	f := domain.TaskFilter{
		Status: domain.TaskStatus(c.Query("status")),
		Search: c.Query("search"),
	}
	if p := c.Query("page"); p != "" {
		f.Page, _ = strconv.Atoi(p)
	}
	if l := c.Query("limit"); l != "" {
		f.Limit, _ = strconv.Atoi(l)
	}

	tasks, total, err := h.svc.List(c.Context(), ownerID, f)
	if err != nil {
		return err
	}
	f = f.Normalize()

	return c.JSON(fiber.Map{
		"data": tasks,
		"meta": fiber.Map{
			"page":  f.Page,
			"limit": f.Limit,
			"total": total,
		},
	})
}

// Update godoc
// @Summary      Update task
// @Description  Partially update title, description, and/or status of a task.
// @Tags         tasks
// @Accept       json
// @Produce      json
// @Param        id   path string                   true "Task ID"
// @Param        task body domain.UpdateTaskInput    true "Task patch"
// @Success      200  {object} map[string]interface{}
// @Failure      400  {object} api.ErrorResponse
// @Failure      404  {object} api.ErrorResponse
// @Router       /tasks/{id} [put]
func (h *TaskHandler) Update(c fiber.Ctx) error {
	ownerID, err := userID(c)
	if err != nil {
		return err
	}

	var in domain.UpdateTaskInput
	if err := c.Bind().JSON(&in); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrValidation, err)
	}

	task, err := h.svc.Update(c.Context(), ownerID, c.Params("id"), in)
	if err != nil {
		return err
	}

	return c.JSON(fiber.Map{"data": task})
}

// Delete godoc
// @Summary      Delete task
// @Description  Delete a task owned by the authenticated user.
// @Tags         tasks
// @Produce      json
// @Param        id path string true "Task ID"
// @Success      204 "No Content"
// @Failure      404 {object} api.ErrorResponse
// @Router       /tasks/{id} [delete]
func (h *TaskHandler) Delete(c fiber.Ctx) error {
	ownerID, err := userID(c)
	if err != nil {
		return err
	}

	if err := h.svc.Delete(c.Context(), ownerID, c.Params("id")); err != nil {
		return err
	}

	return c.SendStatus(fiber.StatusNoContent)
}
