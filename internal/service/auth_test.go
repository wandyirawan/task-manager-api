package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

// fakeUserStore is an in-memory UserStore backing the real AuthService so the
// service layer (validate → hash → persist → token) is exercised without a DB.
type fakeUserStore struct {
	users []*domain.User
}

func (f *fakeUserStore) seed(u *domain.User) { f.users = append(f.users, u) }

func (f *fakeUserStore) Create(_ context.Context, u *domain.User) error {
	for _, ex := range f.users {
		if ex.Email == u.Email {
			return domain.ErrEmailTaken
		}
	}
	f.users = append(f.users, u)
	return nil
}

func (f *fakeUserStore) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}

func (f *fakeUserStore) GetByID(_ context.Context, id string) (*domain.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func newAuthSvc(store UserStore) *AuthService {
	return NewAuthService(store, infra.NewJWTService("service-test-secret", time.Hour), discardLogger())
}

func TestAuthRegisterHappyPath(t *testing.T) {
	svc := newAuthSvc(&fakeUserStore{})

	u, token, err := svc.Register(context.Background(), domain.RegisterInput{
		Email:    "register@example.com",
		Password: "supersecret",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.ID == "" {
		t.Error("missing user id")
	}
	if u.Email != "register@example.com" {
		t.Errorf("email = %q, want register@example.com", u.Email)
	}
	if u.PasswordHash == "" || u.PasswordHash == "supersecret" {
		t.Errorf("hash must be non-empty and != plaintext, got %q", u.PasswordHash)
	}
	// Hash must actually be a valid bcrypt digest of the given password.
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("supersecret")); err != nil {
		t.Errorf("hash does not verify against password: %v", err)
	}
	if token == "" {
		t.Error("expected a non-empty token")
	}
}

func TestAuthRegisterDuplicateEmail(t *testing.T) {
	store := &fakeUserStore{}
	store.seed(&domain.User{ID: "existing", Email: "taken@example.com"})
	svc := newAuthSvc(store)

	_, _, err := svc.Register(context.Background(), domain.RegisterInput{
		Email:    "taken@example.com",
		Password: "anotherpass123",
	})
	if !errors.Is(err, domain.ErrEmailTaken) {
		t.Errorf("Register err = %v, want ErrEmailTaken", err)
	}
}

func TestAuthRegisterValidation(t *testing.T) {
	svc := newAuthSvc(&fakeUserStore{})

	cases := []domain.RegisterInput{
		{Email: "not-an-email", Password: "password123"},
		{Email: "ok@example.com", Password: "short"},
		{Email: "", Password: ""},
	}
	for _, in := range cases {
		if _, _, err := svc.Register(context.Background(), in); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("Register(%+v) err = %v, want ErrValidation", in, err)
		}
	}
}

func TestAuthLoginHappyPath(t *testing.T) {
	store := &fakeUserStore{}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-pass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	store.seed(&domain.User{
		ID:           "user-login",
		Email:        "login@example.com",
		PasswordHash: string(hash),
	})
	svc := newAuthSvc(store)

	u, token, err := svc.Login(context.Background(), "login@example.com", "correct-pass")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if u == nil || u.ID != "user-login" {
		t.Errorf("user = %+v, want user-login", u)
	}
	if token == "" {
		t.Error("expected a non-empty token")
	}
}

// Wrong password and unknown email must both be ErrUnauthorized with an
// identical message so callers cannot tell accounts apart.
func TestAuthLoginUnauthorizedCases(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("right-pass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	store := &fakeUserStore{}
	store.seed(&domain.User{ID: "u1", Email: "known@example.com", PasswordHash: string(hash)})
	svc := newAuthSvc(store)

	_, _, wrongPass := svc.Login(context.Background(), "known@example.com", "wrong-pass")
	_, _, noUser := svc.Login(context.Background(), "ghost@example.com", "right-pass")

	if !errors.Is(wrongPass, domain.ErrUnauthorized) {
		t.Errorf("wrong password err = %v, want ErrUnauthorized", wrongPass)
	}
	if !errors.Is(noUser, domain.ErrUnauthorized) {
		t.Errorf("unknown email err = %v, want ErrUnauthorized", noUser)
	}
	if wrongPass.Error() != noUser.Error() {
		t.Errorf("leaked distinction: wrong-pass=%q unknown-email=%q", wrongPass.Error(), noUser.Error())
	}
}