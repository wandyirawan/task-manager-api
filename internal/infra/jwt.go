package infra

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/wandyirawan/task-manager-api/internal/domain"
)

// JWTService signs and verifies HS256 access tokens (SPEC §7).
// Claims: sub = user_id, iat, exp. Hand-rolled — no jwtware dependency.
type JWTService struct {
	secret []byte
	ttl    time.Duration
}

// NewJWTService creates a JWTService with a shared secret and token TTL.
func NewJWTService(secret string, ttl time.Duration) *JWTService {
	return &JWTService{
		secret: []byte(secret),
		ttl:    ttl,
	}
}

// GenerateToken mints a signed HS256 token carrying sub=userID plus iat/exp.
func (s *JWTService) GenerateToken(userID string) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

// VerifyToken parses, verifies signature+expiry and returns the userID (sub).
// Error outcomes are distinguishable:
//   - expired         → domain.ErrTokenExpired (via errors.Is)
//   - malformed/bad  → wrapped domain.ErrUnauthorized (generic)
//
// Never logs the secret or token.
func (s *JWTService) VerifyToken(tokenString string) (string, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return "", domain.ErrTokenExpired
		}
		return "", fmt.Errorf("invalid token: %w", domain.ErrUnauthorized)
	}

	userID, err := token.Claims.GetSubject()
	if err != nil || userID == "" {
		return "", fmt.Errorf("invalid token subject: %w", domain.ErrUnauthorized)
	}
	return userID, nil
}