package domain

import "errors"

// Sentinel errors exposed across layers.
var (
	ErrNotFound      = errors.New("resource not found")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrValidation    = errors.New("validation failed")
	ErrInternal      = errors.New("internal server error")
	ErrTokenExpired  = errors.New("token expired")
	ErrEmailTaken    = errors.New("email already registered")
)
