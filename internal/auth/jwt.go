package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tsongpon/echo/internal/model"
)

// ErrInvalidSecret is returned when a TokenSigner is created with an empty
// secret.
var ErrInvalidSecret = errors.New("jwt secret must not be empty")

// ErrInvalidToken is returned by Verify when a token is malformed, expired,
// or signed with the wrong key. A single sentinel for all failure modes avoids
// leaking why a token was rejected.
var ErrInvalidToken = errors.New("invalid token")

// ErrInvalidVerificationToken is returned by EmailVerificationTokenSigner.Verify
// for any malformed, expired, or wrong-key verification token. As with
// ErrInvalidToken, a single sentinel avoids leaking the failure reason.
var ErrInvalidVerificationToken = errors.New("invalid verification token")

// TokenSigner issues signed JWTs (HS256) for authenticated employees. The
// secret is held in memory and never serialized.
type TokenSigner struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenSigner creates a TokenSigner. ttl is the access-token lifetime; if
// zero, DefaultTTL is used.
func NewTokenSigner(secret string, ttl time.Duration) (*TokenSigner, error) {
	if secret == "" {
		return nil, ErrInvalidSecret
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &TokenSigner{secret: []byte(secret), ttl: ttl}, nil
}

// DefaultTTL is the token lifetime used when none is specified.
const DefaultTTL = 24 * time.Hour

// DefaultVerificationTTL is the email-verification token lifetime used when
// none is specified.
const DefaultVerificationTTL = 24 * time.Hour

// purposeEmailVerification marks a JWT as an email-verification token. The
// claim is checked on verify so that a token of any other type (including an
// access token) is never accepted as a verification token.
const purposeEmailVerification = "email_verification"

// Claims is the JWT payload for an employee access token.
type Claims struct {
	Email          string `json:"email"`
	Name           string `json:"name"`
	OrganizationID string `json:"organization_id"`
	Title          string `json:"title"`
	jwt.RegisteredClaims
}

// TTL returns the configured token lifetime.
func (s *TokenSigner) TTL() time.Duration { return s.ttl }

// Sign issues a signed JWT for the given employee. The token's subject is the
// employee ID and it expires after the signer's TTL.
func (s *TokenSigner) Sign(employee *model.Employee) (string, error) {
	if employee == nil {
		return "", errors.New("employee must not be nil")
	}

	now := time.Now().UTC()
	claims := Claims{
		Email:          employee.Email,
		Name:           employee.Name,
		OrganizationID: employee.OrganizationID,
		Title:          employee.Title,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   employee.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Verify validates a signed JWT and returns its claims. Any failure
// (malformed, expired, or wrong-signature token) returns ErrInvalidToken so
// callers can uniformly map it to a 401.
func (s *TokenSigner) Verify(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return s.secret, nil
	})
	if err != nil || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// EmailVerificationTokenSigner issues short-lived signed JWTs used to verify
// that an employee controls the email they registered with. It is deliberately
// separate from TokenSigner: it signs with a derived key (the base secret
// suffixed with "::emailverify") so a verification token can never be valid as
// an access token and vice-versa, even when both signers share the same base
// secret.
type EmailVerificationTokenSigner struct {
	secret []byte
	ttl    time.Duration
}

// NewEmailVerificationTokenSigner creates an EmailVerificationTokenSigner. ttl
// is the verification-token lifetime; if zero, DefaultVerificationTTL is used.
func NewEmailVerificationTokenSigner(secret string, ttl time.Duration) (*EmailVerificationTokenSigner, error) {
	if secret == "" {
		return nil, ErrInvalidSecret
	}
	if ttl <= 0 {
		ttl = DefaultVerificationTTL
	}
	return &EmailVerificationTokenSigner{secret: []byte(secret + "::emailverify"), ttl: ttl}, nil
}

// VerificationClaims is the JWT payload for an email-verification token. The
// subject is the employee ID.
type VerificationClaims struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
	jwt.RegisteredClaims
}

// Sign issues a signed email-verification JWT for the given employee. The
// token's subject is the employee ID and it expires after the signer's TTL.
func (s *EmailVerificationTokenSigner) Sign(employee *model.Employee) (string, error) {
	if employee == nil {
		return "", errors.New("employee must not be nil")
	}

	now := time.Now().UTC()
	claims := VerificationClaims{
		Email:   employee.Email,
		Purpose: purposeEmailVerification,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   employee.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("sign verification token: %w", err)
	}
	return signed, nil
}

// Verify validates a signed email-verification JWT and returns its claims. Any
// failure (malformed, expired, wrong-key, or wrong-purpose token) returns
// ErrInvalidVerificationToken so callers can uniformly map it to a 400.
func (s *EmailVerificationTokenSigner) Verify(token string) (*VerificationClaims, error) {
	claims := &VerificationClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidVerificationToken
		}
		return s.secret, nil
	})
	if err != nil || !parsed.Valid {
		return nil, ErrInvalidVerificationToken
	}
	if claims.Purpose != purposeEmailVerification {
		return nil, ErrInvalidVerificationToken
	}
	return claims, nil
}
