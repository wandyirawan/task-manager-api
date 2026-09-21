package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

// UserStore is the persistence contract the auth service consumes
// (consumer-side interface, implemented by internal/repository). GetByEmail
// returns (nil, nil) when no account matches — absence is not an error.
type UserStore interface {
	Create(ctx context.Context, u *domain.User) error
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	GetByID(ctx context.Context, id string) (*domain.User, error)
}

// AuthService implements the register/login use-cases. It owns bcrypt hashing
// and JWT minting; the store is injected so the layer stays unit-testable.
type AuthService struct {
	users  UserStore
	jwt    *infra.JWTService
	logger *slog.Logger
	now    func() time.Time
}

// NewAuthService builds an AuthService with the given user store, JWT signer
// and logger. Clock is injectable for deterministic tests.
func NewAuthService(users UserStore, jwt *infra.JWTService, logger *slog.Logger) *AuthService {
	return &AuthService{
		users:  users,
		jwt:    jwt,
		logger: logger,
		now:    time.Now,
	}
}

// normalizeEmail trims and lowercases an address so registration and login
// match case-insensitively and duplicates cannot slip by on case alone.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Register validates the input, persists a bcrypt-hashed account and returns
// the fresh user plus a signed JWT for immediate use. A non-unique email
// surfaces as domain.ErrEmailTaken (checked up front and enforced by the store).
func (s *AuthService) Register(ctx context.Context, in domain.RegisterInput) (*domain.User, string, error) {
	if err := in.Validate(); err != nil {
		return nil, "", err
	}

	email := normalizeEmail(in.Email)

	existing, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		s.logger.Error("register lookup failed", "error", err)
		return nil, "", err
	}
	if existing != nil {
		return nil, "", domain.ErrEmailTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		s.logger.Error("bcrypt hash failed", "error", err)
		return nil, "", domain.ErrInternal
	}

	now := s.now().UTC()
	u := &domain.User{
		ID:           uuid.NewString(),
		Email:        email,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.users.Create(ctx, u); err != nil {
		s.logger.Error("user create failed", "error", err)
		// The store translates a UNIQUE collision to ErrEmailTaken as well,
		// guarding against a concurrent registration racing the pre-check.
		return nil, "", err
	}

	token, err := s.jwt.GenerateToken(u.ID)
	if err != nil {
		s.logger.Error("token generation failed", "error", err)
		return nil, "", domain.ErrInternal
	}

	return u, token, nil
}

// Login verifies credentials and mints a JWT. Both an unknown email and a
// wrong password resolve to the SAME generic domain.ErrUnauthorized so a
// caller cannot enumerate which accounts exist.
func (s *AuthService) Login(ctx context.Context, email, password string) (*domain.User, string, error) {
	email = normalizeEmail(email)

	u, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		s.logger.Error("login lookup failed", "error", err)
		return nil, "", err
	}
	if u == nil {
		return nil, "", domain.ErrUnauthorized
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		// Same generic error as the not-found branch — do not leak existence.
		return nil, "", domain.ErrUnauthorized
	}

	token, err := s.jwt.GenerateToken(u.ID)
	if err != nil {
		s.logger.Error("token generation failed", "error", err)
		return nil, "", domain.ErrInternal
	}

	return u, token, nil
}