package domain

import (
	"fmt"
	"strings"
	"time"
)

// TaskStatus is the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusTodo       TaskStatus = "todo"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusDone       TaskStatus = "done"
)

// Valid reports whether s is one of the allowed task statuses.
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskStatusTodo, TaskStatusInProgress, TaskStatusDone:
		return true
	default:
		return false
	}
}

// Task is a user-owned task record.
type Task struct {
	ID          string     `json:"id" db:"id"`
	OwnerID     string     `json:"ownerId" db:"owner_id"`
	AssigneeID  *string    `json:"assigneeId" db:"assignee_id"`
	Title       string     `json:"title" db:"title"`
	Description string     `json:"description" db:"description"`
	Status      TaskStatus `json:"status" db:"status"`
	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt   time.Time  `json:"updatedAt" db:"updated_at"`
}

const (
	maxTitleLength       = 255
	maxDescriptionLength = 2000
)

// CreateTaskInput carries the payload for creating a task.
type CreateTaskInput struct {
	OwnerID     string `json:"ownerId"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Validate checks the create payload. It returns a wrapped domain.ErrValidation
// with a clear message for the first offending rule.
func (in CreateTaskInput) Validate() error {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return fmt.Errorf("%w: title is required", ErrValidation)
	}
	if len(title) > maxTitleLength {
		return fmt.Errorf("%w: title must be at most %d characters", ErrValidation, maxTitleLength)
	}
	if len(in.Description) > maxDescriptionLength {
		return fmt.Errorf("%w: description must be at most %d characters", ErrValidation, maxDescriptionLength)
	}
	return nil
}

// UpdateTaskInput carries the optional, partial fields for updating a task.
// nil pointers mean "leave unchanged".
type UpdateTaskInput struct {
	Title       *string     `json:"title"`
	Description *string     `json:"description"`
	Status      *TaskStatus `json:"status"`
}

// Validate checks the update payload. At least one field must be provided and
// every provided field must be valid. Returns a wrapped domain.ErrValidation.
func (in UpdateTaskInput) Validate() error {
	if in.Title == nil && in.Description == nil && in.Status == nil {
		return fmt.Errorf("%w: at least one of title, description or status must be provided", ErrValidation)
	}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return fmt.Errorf("%w: title must not be empty", ErrValidation)
		}
		if len(title) > maxTitleLength {
			return fmt.Errorf("%w: title must be at most %d characters", ErrValidation, maxTitleLength)
		}
	}
	if in.Description != nil && len(*in.Description) > maxDescriptionLength {
		return fmt.Errorf("%w: description must be at most %d characters", ErrValidation, maxDescriptionLength)
	}
	if in.Status != nil && !in.Status.Valid() {
		return fmt.Errorf("%w: invalid status %q", ErrValidation, *in.Status)
	}
	return nil
}

// TaskFilter drives the list query: scoped to an owner, optional status and
// free-text title search, plus pagination.
type TaskFilter struct {
	OwnerID string
	Status  TaskStatus
	Search  string
	Page    int
	Limit   int
}

// Normalize clamps pagination to safe defaults: page >= 1, limit in [1,100]
// with a default of 10. It returns the filter with clamped values.
func (f TaskFilter) Normalize() TaskFilter {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = 10
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	return f
}
