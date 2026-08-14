package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tsongpon/echo/internal/model"
)

func TestTokenSigner_SignAndParse(t *testing.T) {
	signer, err := NewTokenSigner("test-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	emp := &model.Employee{
		ID:             "emp-1",
		Name:           "Alice",
		OrganizationID: "org-1",
		Title:          "Engineer",
		Email:          "alice@example.com",
	}

	signed, err := signer.Sign(emp)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if signed == "" {
		t.Fatal("Sign returned empty token")
	}

	// Parse it back with the same secret and verify the claims.
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(signed, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte("test-secret"), nil
	})
	if err != nil {
		t.Fatalf("ParseWithClaims: %v", err)
	}
	if !token.Valid {
		t.Fatal("token not valid")
	}
	if claims.Subject != "emp-1" {
		t.Fatalf("got subject %q, want emp-1", claims.Subject)
	}
	if claims.Email != "alice@example.com" {
		t.Fatalf("got email %q", claims.Email)
	}
	if claims.OrganizationID != "org-1" {
		t.Fatalf("got organization_id %q", claims.OrganizationID)
	}
	if claims.ExpiresAt == nil {
		t.Fatal("expires_at is nil")
	}
}

func TestTokenSigner_RejectsEmptySecret(t *testing.T) {
	if _, err := NewTokenSigner("", time.Hour); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("expected ErrInvalidSecret, got %v", err)
	}
}

func TestTokenSigner_DefaultTTL(t *testing.T) {
	s, err := NewTokenSigner("s", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}
	if s.TTL() != DefaultTTL {
		t.Fatalf("got TTL %v, want default %v", s.TTL(), DefaultTTL)
	}
}

func TestTokenSigner_RejectsNilEmployee(t *testing.T) {
	s, _ := NewTokenSigner("s", time.Hour)
	if _, err := s.Sign(nil); err == nil {
		t.Fatal("expected error signing nil employee")
	}
}

func TestTokenSigner_Verify(t *testing.T) {
	signer, err := NewTokenSigner("test-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	emp := &model.Employee{
		ID:             "emp-1",
		Name:           "Alice",
		OrganizationID: "org-1",
		Title:          "Engineer",
		Email:          "alice@example.com",
	}

	signed, err := signer.Sign(emp)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	t.Run("valid token", func(t *testing.T) {
		claims, err := signer.Verify(signed)
		if err != nil {
			t.Fatalf("Verify: unexpected error: %v", err)
		}
		if claims.Subject != "emp-1" {
			t.Fatalf("got subject %q, want emp-1", claims.Subject)
		}
		if claims.Email != "alice@example.com" {
			t.Fatalf("got email %q", claims.Email)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		// Build a token signed with the same secret but already expired. The
		// test lives in package auth, so it can reach the unexported secret.
		now := time.Now().UTC()
		claims := Claims{
			Email:          emp.Email,
			Name:           emp.Name,
			OrganizationID: emp.OrganizationID,
			Title:          emp.Title,
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   emp.ID,
				IssuedAt:  jwt.NewNumericDate(now.Add(-2 * time.Hour)),
				NotBefore: jwt.NewNumericDate(now.Add(-2 * time.Hour)),
				ExpiresAt: jwt.NewNumericDate(now.Add(-time.Hour)),
			},
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(signer.secret)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		if _, err := signer.Verify(signed); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expected ErrInvalidToken, got %v", err)
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		other, _ := NewTokenSigner("other-secret", time.Hour)
		tok, err := other.Sign(emp)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if _, err := signer.Verify(tok); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expected ErrInvalidToken, got %v", err)
		}
	})

	t.Run("malformed token", func(t *testing.T) {
		if _, err := signer.Verify("not-a-jwt"); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expected ErrInvalidToken, got %v", err)
		}
	})

	t.Run("empty token", func(t *testing.T) {
		if _, err := signer.Verify(""); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expected ErrInvalidToken, got %v", err)
		}
	})
}

func TestEmailVerificationTokenSigner(t *testing.T) {
	signer, err := NewEmailVerificationTokenSigner("test-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewEmailVerificationTokenSigner: %v", err)
	}

	emp := &model.Employee{ID: "emp-1", Email: "alice@example.com"}

	t.Run("empty secret rejected", func(t *testing.T) {
		if _, err := NewEmailVerificationTokenSigner("", time.Hour); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("expected ErrInvalidSecret, got %v", err)
		}
	})

	t.Run("nil employee rejected", func(t *testing.T) {
		if _, err := signer.Sign(nil); err == nil {
			t.Fatal("expected error signing nil employee")
		}
	})

	t.Run("roundtrip", func(t *testing.T) {
		token, err := signer.Sign(emp)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := signer.Verify(token)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if claims.Subject != "emp-1" {
			t.Fatalf("got subject %q, want emp-1", claims.Subject)
		}
		if claims.Purpose != purposeEmailVerification {
			t.Fatalf("got purpose %q, want %q", claims.Purpose, purposeEmailVerification)
		}
		if claims.Email != "alice@example.com" {
			t.Fatalf("got email %q", claims.Email)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		now := time.Now().UTC()
		claims := VerificationClaims{
			Email:   emp.Email,
			Purpose: purposeEmailVerification,
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   emp.ID,
				IssuedAt:  jwt.NewNumericDate(now.Add(-2 * time.Hour)),
				NotBefore: jwt.NewNumericDate(now.Add(-2 * time.Hour)),
				ExpiresAt: jwt.NewNumericDate(now.Add(-time.Hour)),
			},
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(signer.secret)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		if _, err := signer.Verify(signed); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("wrong purpose", func(t *testing.T) {
		now := time.Now().UTC()
		claims := VerificationClaims{
			Email:   emp.Email,
			Purpose: "something-else",
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   emp.ID,
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			},
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(signer.secret)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		if _, err := signer.Verify(signed); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("access token is not a verification token", func(t *testing.T) {
		accessSigner, err := NewTokenSigner("test-secret", time.Hour)
		if err != nil {
			t.Fatalf("NewTokenSigner: %v", err)
		}
		accessToken, err := accessSigner.Sign(emp)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if _, err := signer.Verify(accessToken); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("verification token is not an access token", func(t *testing.T) {
		accessSigner, err := NewTokenSigner("test-secret", time.Hour)
		if err != nil {
			t.Fatalf("NewTokenSigner: %v", err)
		}
		verifyToken, err := signer.Sign(emp)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if _, err := accessSigner.Verify(verifyToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expected ErrInvalidToken, got %v", err)
		}
	})
}
