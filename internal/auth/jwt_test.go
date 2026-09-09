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
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "org-1",
		Title:            "Engineer",
		Email:            "alice@example.com",
	}

	signed, err := signer.Sign(emp)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if signed == "" {
		t.Fatal("Sign returned empty token")
	}

	// Parse it back with the same signer (and therefore the same derived
	// key) and verify the claims. Parsing by hand rather than calling
	// Verify only to assert the claims contents.
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(signed, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return deriveKey("test-secret", purposeAccess)
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
	if claims.OrganizationName != "org-1" {
		t.Fatalf("got organization_name %q", claims.OrganizationName)
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
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "org-1",
		Title:            "Engineer",
		Email:            "alice@example.com",
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
			Email:            emp.Email,
			Name:             emp.Name,
			OrganizationName: emp.OrganizationName,
			Title:            emp.Title,
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

// TestSigners_RejectForgedKeys covers the C2 finding: every signer must sign
// with an HKDF-derived key, so a forged token signed with the raw base secret
// or with the legacy concatenation-style keys ("secret::emailverify",
// "secret::invitation") must be rejected by every verify path. Before the fix,
// anyone who knew the base secret could compute the verification and
// invitation keys by guessing the suffix.
func TestSigners_RejectForgedKeys(t *testing.T) {
	const baseSecret = "test-secret"

	emp := &model.Employee{ID: "emp-1", Email: "alice@example.com"}

	accessSigner, err := NewTokenSigner(baseSecret, time.Hour)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}
	verifySigner, err := NewEmailVerificationTokenSigner(baseSecret, time.Hour)
	if err != nil {
		t.Fatalf("NewEmailVerificationTokenSigner: %v", err)
	}
	invSigner, err := NewInvitationTokenSigner(baseSecret, time.Hour)
	if err != nil {
		t.Fatalf("NewInvitationTokenSigner: %v", err)
	}

	// Forged keys: the raw base secret and the legacy concatenation forms.
	forgedKeys := map[string][]byte{
		"raw base secret":        []byte(baseSecret),
		"legacy emailverify key": []byte(baseSecret + "::emailverify"),
		"legacy invitation key":  []byte(baseSecret + "::invitation"),
	}

	for name, key := range forgedKeys {
		t.Run(name, func(t *testing.T) {
			// Forge an access token.
			accessClaims := Claims{
				Email:            emp.Email,
				OrganizationName: "org-1",
				Role:             model.RoleOrgAdmin,
				RegisteredClaims: jwt.RegisteredClaims{
					Subject:   emp.ID,
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				},
			}
			forgedAccess, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(key)
			if err != nil {
				t.Fatalf("sign forged access token: %v", err)
			}
			if _, err := accessSigner.Verify(forgedAccess); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("access signer accepted forged key: %v", err)
			}

			// Forge an email-verification token.
			verifyClaims := VerificationClaims{
				Email:   emp.Email,
				Purpose: purposeEmailVerification,
				RegisteredClaims: jwt.RegisteredClaims{
					Subject:   emp.ID,
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				},
			}
			forgedVerify, err := jwt.NewWithClaims(jwt.SigningMethodHS256, verifyClaims).SignedString(key)
			if err != nil {
				t.Fatalf("sign forged verification token: %v", err)
			}
			if _, err := verifySigner.Verify(forgedVerify); !errors.Is(err, ErrInvalidVerificationToken) {
				t.Fatalf("verification signer accepted forged key: %v", err)
			}

			// Forge an invitation token.
			invClaims := InvitationClaims{
				OrganizationName: "org-1",
				Purpose:          purposeInvitation,
				RegisteredClaims: jwt.RegisteredClaims{
					Subject:   emp.ID,
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				},
			}
			forgedInv, err := jwt.NewWithClaims(jwt.SigningMethodHS256, invClaims).SignedString(key)
			if err != nil {
				t.Fatalf("sign forged invitation token: %v", err)
			}
			if _, err := invSigner.Verify(forgedInv); !errors.Is(err, ErrInvalidInvitationToken) {
				t.Fatalf("invitation signer accepted forged key: %v", err)
			}
		})
	}
}

// TestSigners_DerivedKeysAreDistinct asserts that the three signers derived
// from one base secret hold three different keys (HKDF domain separation). It
// guards against a regression that silently reverts to a shared key.
func TestSigners_DerivedKeysAreDistinct(t *testing.T) {
	const baseSecret = "test-secret"

	accessSigner, err := NewTokenSigner(baseSecret, time.Hour)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}
	verifySigner, err := NewEmailVerificationTokenSigner(baseSecret, time.Hour)
	if err != nil {
		t.Fatalf("NewEmailVerificationTokenSigner: %v", err)
	}
	invSigner, err := NewInvitationTokenSigner(baseSecret, time.Hour)
	if err != nil {
		t.Fatalf("NewInvitationTokenSigner: %v", err)
	}

	keys := [][]byte{accessSigner.secret, verifySigner.secret, invSigner.secret}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if string(keys[i]) == string(keys[j]) {
				t.Fatalf("signers %d and %d share the same key", i, j)
			}
		}
	}
}
