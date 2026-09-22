package repository

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/service"
)

var _ service.UserStore = (*userRepository)(nil)

type userRepository struct {
	db *sqlx.DB
}

// NewUserRepository wires the sqlx-backed user repository onto the shared DB.
func NewUserRepository(db *sqlx.DB) service.UserStore {
	return &userRepository{db: db}
}

func (r *userRepository) Create(ctx context.Context, u *domain.User) error {
	_, err := r.db.NamedExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, created_at, updated_at)
		 VALUES (:id, :email, :password_hash, :created_at, :updated_at)`,
		u)
	if err != nil {
		// email is UNIQUE — translate the driver's constraint violation into a
		// domain sentinel so the service/handler can map it to a 409.
		if isUniqueViolation(err) {
			return domain.ErrEmailTaken
		}
		return err
	}
	return nil
}

// GetByEmail returns the user matching email, or (nil, nil) when absent.
// Absence is signalled by nil rather than an error so callers can cleanly
// branch (register check vs login denial) without error-type gymnastics.
func (r *userRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	var u domain.User
	err := r.db.GetContext(ctx, &u,
		`SELECT id, email, password_hash, created_at, updated_at
		 FROM users WHERE email = $1`, email)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *userRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	var u domain.User
	err := r.db.GetContext(ctx, &u,
		`SELECT id, email, password_hash, created_at, updated_at
		 FROM users WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}
