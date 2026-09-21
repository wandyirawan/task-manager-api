package domain

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// User is a registered account record. PasswordHash is never serialized to
// JSON (json:"-") so the bcrypt digest can never leak in an API response.
type User struct {
	ID           string    `json:"id" db:"id"`
	Email        string    `json:"email" db:"email"`
	PasswordHash string    `json:"-" db:"password_hash"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt    time.Time `json:"updatedAt" db:"updated_at"`
}

const (
	minPasswordLength = 8
)

// RegisterInput carries the payload for creating an account.
type RegisterInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Validate checks the registration payload. Email must be a parseable mail
// address (net/mail, stdlib) and password must be at least 8 characters.
// Returns a wrapped domain.ErrValidation for the first offending rule.
func (in RegisterInput) Validate() error {
	email := strings.TrimSpace(in.Email)
	if email == "" {
		return fmt.Errorf("%w: email is required", ErrValidation)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return fmt.Errorf("%w: email is invalid", ErrValidation)
	}
	if len(in.Password) < minPasswordLength {
		return fmt.Errorf("%w: password must be at least %d characters", ErrValidation, minPasswordLength)
	}
	return nil
}