package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/repository"
)

// NOTE: newTestDB and findMigrationsDir are shared from task_test.go (same
// package) — they boot a migrated temp SQLite file, exactly as the task repo
// tests do. No redefinition here.

func TestUserCreateAndGetByEmail(t *testing.T) {
	db := newTestDB(t)
	repo := repository.NewUserRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	u := &domain.User{
		ID:           "user-bcrypt-1",
		Email:        "alice@example.com",
		PasswordHash: "$2a$10$0123456789012345678901eXAMPLEHASH",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got == nil {
		t.Fatal("GetByEmail = nil, want user")
	}
	if got.ID != u.ID || got.Email != u.Email || got.PasswordHash != u.PasswordHash {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestUserGetByID(t *testing.T) {
	db := newTestDB(t)
	repo := repository.NewUserRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	u := &domain.User{
		ID:           "user-byid",
		Email:        "bob@example.com",
		PasswordHash: "hash-bob",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, "user-byid")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.Email != "bob@example.com" {
		t.Errorf("GetByID = %+v, want bob", got)
	}
}

func TestUserGetByEmailNotFound(t *testing.T) {
	db := newTestDB(t)
	repo := repository.NewUserRepository(db)

	got, err := repo.GetByEmail(context.Background(), "nobody@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got != nil {
		t.Errorf("GetByEmail = %+v, want nil", got)
	}
}

func TestUserCreateDuplicateEmailReturnsEmailTaken(t *testing.T) {
	db := newTestDB(t)
	repo := repository.NewUserRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	first := &domain.User{ID: "user-a", Email: "dup@example.com", PasswordHash: "h1", CreatedAt: now, UpdatedAt: now}
	second := &domain.User{ID: "user-b", Email: "dup@example.com", PasswordHash: "h2", CreatedAt: now, UpdatedAt: now}

	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	err := repo.Create(ctx, second)
	if !errors.Is(err, domain.ErrEmailTaken) {
		t.Errorf("duplicate Create err = %v, want ErrEmailTaken", err)
	}
}