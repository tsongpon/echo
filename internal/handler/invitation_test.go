package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/model"
)

// fakeInvitationService is an in-test stand-in for the handler.InvitationService
// interface. createFn lets each test decide how tokens are issued; the default
// issues nothing and fails.
type fakeInvitationService struct {
	createFn func(creatorID, organizationName string, expiresAt *time.Time) (string, error)
}

func (f *fakeInvitationService) CreateInvitationToken(creatorID, organizationName string, expiresAt *time.Time) (string, error) {
	if f.createFn != nil {
		return f.createFn(creatorID, organizationName, expiresAt)
	}
	return "", errors.New("not stubbed")
}

func (f *fakeInvitationService) ExtractInvitationToken(token string) (*model.Invitation, error) {
	// Round-trip through a real signer so the response is built from verified
	// claims, exactly as the handler does against the real service.
	signer, err := auth.NewInvitationTokenSigner("test-secret", time.Hour)
	if err != nil {
		return nil, err
	}
	claims, err := signer.Verify(token)
	if err != nil {
		return nil, apperror.ErrInvalidVerificationToken
	}
	var createdAt, expiresAt time.Time
	if claims.IssuedAt != nil {
		createdAt = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}
	return &model.Invitation{
		ID:               claims.ID,
		CreatedBy:        claims.Subject,
		OrganizationName: claims.OrganizationName,
		CreatedAt:        createdAt,
		ExpiresAt:        expiresAt,
	}, nil
}

// newInvitationTestContext builds an echo context for a POST /v1/invitation
// request with the given body, authenticated with a token signed for the given
// employee (mimicking the Auth middleware by storing verified claims).
func newInvitationTestContext(t *testing.T, body string, inviter *model.Employee) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()

	signer, err := auth.NewTokenSigner("test-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}
	token, err := signer.Sign(inviter)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	claims, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/invitation", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e := echo.New()
	c := e.NewContext(req, rec)
	c.Set(contextKeyUser, claims)
	return c, rec
}

func TestCreateInvitation_BoundToCallerOrganization(t *testing.T) {
	admin := &model.Employee{
		ID:               "inviter-1",
		OrganizationName: "OrgA",
		Role:             model.RoleOrgAdmin,
		Email:            "admin@orga.example.com",
	}

	// The invitation service is the real signer wiring; the fake's createFn
	// records what the handler asked it to sign.
	var gotOrg string
	svc := &fakeInvitationService{createFn: func(_, organizationName string, _ *time.Time) (string, error) {
		gotOrg = organizationName
		signer, err := auth.NewInvitationTokenSigner("test-secret", time.Hour)
		if err != nil {
			return "", err
		}
		return signer.Sign("inviter-1", organizationName, nil)
	}}
	h := NewInvitationHandler(svc)

	t.Run("organization comes from the JWT, not the body", func(t *testing.T) {
		// The C4 PoC: an OrgA admin requests an invitation for OrgB. The
		// body field is gone; any stray field must be ignored, and the token
		// must be bound to the caller's own organization.
		c, rec := newInvitationTestContext(t, `{"organization_name":"OrgB"}`, admin)
		if err := h.CreateInvitation(c); err != nil {
			t.Fatalf("CreateInvitation: unexpected error: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		if gotOrg != "OrgA" {
			t.Fatalf("handler asked service to sign organization %q, want OrgA (from JWT)", gotOrg)
		}
		if !strings.Contains(rec.Body.String(), `"organization_name":"OrgA"`) {
			t.Fatalf("issued token is not bound to OrgA: %s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "OrgB") {
			t.Fatalf("response leaks foreign organization OrgB: %s", rec.Body.String())
		}
	})

	t.Run("empty body still invites to the caller organization", func(t *testing.T) {
		c, rec := newInvitationTestContext(t, `{}`, admin)
		if err := h.CreateInvitation(c); err != nil {
			t.Fatalf("CreateInvitation: unexpected error: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		if gotOrg != "OrgA" {
			t.Fatalf("handler asked service to sign organization %q, want OrgA", gotOrg)
		}
	})

	t.Run("non-admin is rejected", func(t *testing.T) {
		user := &model.Employee{ID: "user-1", OrganizationName: "OrgA", Role: model.RoleUser}
		c, _ := newInvitationTestContext(t, `{}`, user)
		err := h.CreateInvitation(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusForbidden {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusForbidden)
		}
	})

	t.Run("missing claims is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/invitation", strings.NewReader(`{}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.CreateInvitation(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})

	t.Run("token without organization is rejected", func(t *testing.T) {
		// A signed access token must carry an organization; if one ever
		// arrives without it, the handler must fail closed rather than issue
		// an invitation for an empty organization.
		noOrg := &model.Employee{ID: "inviter-2", Role: model.RoleOrgAdmin, Email: "a@b.example.com"}
		c, _ := newInvitationTestContext(t, `{}`, noOrg)
		err := h.CreateInvitation(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusForbidden {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusForbidden)
		}
	})
}
