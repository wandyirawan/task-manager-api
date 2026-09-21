package infra_test

import (
	"errors"
	"testing"
	"time"

	"github.com/wandyirawan/task-manager-api/internal/domain"
	"github.com/wandyirawan/task-manager-api/internal/infra"
)

const jwtTestSecret = "test-secret-for-jwt-hs256"

func TestJWTService_GenerateVerifyRoundtrip(t *testing.T) {
	svc := infra.NewJWTService(jwtTestSecret, time.Hour)

	token, err := svc.GenerateToken("user-123")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	userID, err := svc.VerifyToken(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if userID != "user-123" {
		t.Errorf("userID = %q, want user-123", userID)
	}
}

func TestJWTService_VerifyExpiredToken(t *testing.T) {
	// Negative TTL puts exp in the past the moment the token is issued.
	svc := infra.NewJWTService(jwtTestSecret, -time.Hour)

	token, err := svc.GenerateToken("user-123")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	_, err = svc.VerifyToken(token)
	if err == nil || !errors.Is(err, domain.ErrTokenExpired) {
		t.Errorf("verify = %v, want ErrTokenExpired", err)
	}
}

func TestJWTService_VerifyFakeSignature(t *testing.T) {
	issuer := infra.NewJWTService(jwtTestSecret, time.Hour)
	verifier := infra.NewJWTService("a-different-secret", time.Hour)

	token, err := issuer.GenerateToken("user-123")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	_, err = verifier.VerifyToken(token)
	if err == nil || !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("verify = %v, want ErrUnauthorized", err)
	}
	if errors.Is(err, domain.ErrTokenExpired) {
		t.Errorf("verify = %v, must not be ErrTokenExpired", err)
	}
}

func TestJWTService_VerifyMalformedToken(t *testing.T) {
	svc := infra.NewJWTService(jwtTestSecret, time.Hour)

	cases := []string{
		"not-a-token",
		"header.payload",
		"",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			_, err := svc.VerifyToken(tc)
			if err == nil || !errors.Is(err, domain.ErrUnauthorized) {
				t.Errorf("verify(%q) = %v, want ErrUnauthorized", tc, err)
			}
		})
	}
}