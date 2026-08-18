package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

// fakeFeedbackPeriodService is an in-test stand-in for the
// handler.FeedbackPeriodService interface. It records the inputs the handler
// passed through and returns canned outputs, so the handler can be unit-tested
// without the real service or Firestore.
type fakeFeedbackPeriodService struct {
	gotOrg   string
	gotName  string
	createFn func(ctx context.Context, organizationName string, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error)
	listFn   func(ctx context.Context, organizationName string) ([]*model.FeedbackPeriod, error)
}

func (f *fakeFeedbackPeriodService) Create(_ context.Context, organizationName string, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error) {
	if f.createFn != nil {
		return f.createFn(context.Background(), organizationName, period)
	}
	// Default happy path: echo back a populated period so the handler can
	// serialize it. Record the org the handler passed (taken from the JWT) so
	// tests can assert the handler does not trust the body.
	f.gotOrg = organizationName
	if period != nil {
		f.gotName = period.Name
	}
	return &model.FeedbackPeriod{
		ID:               "period-1",
		Name:             period.Name,
		OrganizationName: organizationName,
		StartDate:        period.StartDate,
		EndDate:          period.EndDate,
	}, nil
}

func (f *fakeFeedbackPeriodService) ListByOrganization(_ context.Context, organizationName string) ([]*model.FeedbackPeriod, error) {
	if f.listFn != nil {
		return f.listFn(context.Background(), organizationName)
	}
	return []*model.FeedbackPeriod{}, nil
}

func TestCreateFeedbackPeriod_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	admin := &model.Employee{
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "Acme",
		Role:             model.RoleOrgAdmin,
		Title:            "Engineer",
		Email:            "alice@example.com",
	}
	user := &model.Employee{
		ID:               "emp-2",
		Name:             "Bob",
		OrganizationName: "Acme",
		Role:             model.RoleUser,
		Email:            "bob@example.com",
	}

	// setClaims signs+verifies a token for the given employee and stores the
	// resulting claims in the echo context, mimicking what the Auth middleware
	// does on a real request.
	setClaims := func(c *echo.Context, e *model.Employee) {
		token, err := signer.Sign(e)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := signer.Verify(token)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		c.Set(contextKeyUser, claims)
	}

	const validBody = `{"name":"H1 2026","start_date":"2026-01-01T00:00:00Z","end_date":"2026-06-30T00:00:00Z"}`

	cases := []struct {
		name        string
		claims      *model.Employee
		body        string
		createFn    func(ctx context.Context, organizationName string, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error)
		wantCode    int
		wantMessage string
		wantBodyIn  string
	}{
		{
			name:       "success",
			claims:     admin,
			body:       validBody,
			wantCode:   http.StatusCreated,
			wantBodyIn: `"organization_name":"Acme"`,
		},
		{
			name:   "validation error",
			claims: admin,
			body:   `{"name":"","start_date":"2026-01-01T00:00:00Z","end_date":"2026-06-30T00:00:00Z"}`,
			createFn: func(_ context.Context, _ string, _ *model.FeedbackPeriod) (*model.FeedbackPeriod, error) {
				return nil, apperror.ErrInvalidFeedbackPeriod("name is required")
			},
			wantCode:    http.StatusBadRequest,
			wantMessage: "name is required",
		},
		{
			name:   "internal error",
			claims: admin,
			body:   validBody,
			createFn: func(_ context.Context, _ string, _ *model.FeedbackPeriod) (*model.FeedbackPeriod, error) {
				return nil, errors.New("db down")
			},
			wantCode:    http.StatusInternalServerError,
			wantMessage: "failed to create feedback period",
		},
		{
			name:        "malformed body",
			claims:      admin,
			body:        `{not-json`,
			wantCode:    http.StatusBadRequest,
			wantMessage: "invalid request body",
		},
		{
			name:        "non-admin forbidden",
			claims:      user,
			body:        validBody,
			wantCode:    http.StatusForbidden,
			wantMessage: "only org admins can create feedback periods",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeFeedbackPeriodService{createFn: tc.createFn}
			h := NewFeedbackPeriodHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

			req := httptest.NewRequest(http.MethodPost, "/v1/feedback-periods", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			e := echo.New()
			c := e.NewContext(req, rec)
			setClaims(c, tc.claims)

			err := h.CreateFeedbackPeriod(c)
			if tc.wantCode == http.StatusCreated {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if rec.Code != tc.wantCode {
					t.Fatalf("got status %d, want %d", rec.Code, tc.wantCode)
				}
				if !strings.Contains(rec.Body.String(), tc.wantBodyIn) {
					t.Fatalf("expected %q in body, got %s", tc.wantBodyIn, rec.Body.String())
				}
				// The handler must take the org from the JWT, not the body.
				if svc.gotOrg != "Acme" {
					t.Fatalf("handler passed org %q to service, want Acme (from JWT)", svc.gotOrg)
				}
				return
			}

			he, ok := err.(*echo.HTTPError)
			if !ok {
				t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
			}
			if he.Code != tc.wantCode {
				t.Fatalf("got status %d, want %d", he.Code, tc.wantCode)
			}
			if he.Message != tc.wantMessage {
				t.Fatalf("expected message %q, got %v", tc.wantMessage, he.Message)
			}
		})
	}

	t.Run("no token is unauthorized", func(t *testing.T) {
		// Bypass middleware to assert the handler guards against missing
		// claims (defence in depth for miswired routes).
		svc := &fakeFeedbackPeriodService{}
		h := NewFeedbackPeriodHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodPost, "/v1/feedback-periods", strings.NewReader(validBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.CreateFeedbackPeriod(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}

func TestListFeedbackPeriods_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	user := &model.Employee{
		ID:               "emp-2",
		Name:             "Bob",
		OrganizationName: "Acme",
		Role:             model.RoleUser,
		Email:            "bob@example.com",
	}

	// setClaims signs+verifies a token for the given employee and stores the
	// resulting claims in the echo context, mimicking what the Auth middleware
	// does on a real request.
	setClaims := func(c *echo.Context, e *model.Employee) {
		token, err := signer.Sign(e)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := signer.Verify(token)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		c.Set(contextKeyUser, claims)
	}

	t.Run("success returns periods for the caller's organization", func(t *testing.T) {
		periods := []*model.FeedbackPeriod{
			{ID: "p-1", Name: "H2", OrganizationName: "Acme", StartDate: time.Unix(2, 0).UTC(), EndDate: time.Unix(3, 0).UTC()},
			{ID: "p-2", Name: "H1", OrganizationName: "Acme", StartDate: time.Unix(1, 0).UTC(), EndDate: time.Unix(2, 0).UTC()},
		}
		svc := &fakeFeedbackPeriodService{
			listFn: func(_ context.Context, organizationName string) ([]*model.FeedbackPeriod, error) {
				if organizationName != "Acme" {
					t.Fatalf("handler passed org %q to service, want Acme (from JWT)", organizationName)
				}
				return periods, nil
			},
		}
		h := NewFeedbackPeriodHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/feedback-periods", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, user)

		if err := h.ListFeedbackPeriods(c); err != nil {
			t.Fatalf("ListFeedbackPeriods: unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"periods":[`) {
			t.Fatalf("response missing periods array: %s", body)
		}
		if !strings.Contains(body, `"id":"p-1"`) || !strings.Contains(body, `"id":"p-2"`) {
			t.Fatalf("response missing period ids: %s", body)
		}
		if !strings.Contains(body, `"organization_name":"Acme"`) {
			t.Fatalf("response missing organization_name: %s", body)
		}
	})

	t.Run("empty result returns empty array, not null", func(t *testing.T) {
		svc := &fakeFeedbackPeriodService{
			listFn: func(_ context.Context, _ string) ([]*model.FeedbackPeriod, error) {
				return []*model.FeedbackPeriod{}, nil
			},
		}
		h := NewFeedbackPeriodHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/feedback-periods", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, user)

		if err := h.ListFeedbackPeriods(c); err != nil {
			t.Fatalf("ListFeedbackPeriods: unexpected error: %v", err)
		}
		if !strings.Contains(rec.Body.String(), `"periods":[]`) {
			t.Fatalf("expected empty periods array, got %s", rec.Body.String())
		}
	})

	t.Run("internal error maps to 500", func(t *testing.T) {
		svc := &fakeFeedbackPeriodService{
			listFn: func(_ context.Context, _ string) ([]*model.FeedbackPeriod, error) {
				return nil, errors.New("db down")
			},
		}
		h := NewFeedbackPeriodHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/feedback-periods", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, user)

		err := h.ListFeedbackPeriods(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusInternalServerError)
		}
		if he.Message != "failed to list feedback periods" {
			t.Fatalf("expected message %q, got %v", "failed to list feedback periods", he.Message)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		svc := &fakeFeedbackPeriodService{}
		h := NewFeedbackPeriodHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/feedback-periods", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.ListFeedbackPeriods(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})

	t.Run("non-admin user is allowed", func(t *testing.T) {
		// Unlike create, listing periods is available to any authenticated
		// employee (they need to see periods to file feedback). The handler
		// must not 403 a role:user caller.
		svc := &fakeFeedbackPeriodService{
			listFn: func(_ context.Context, _ string) ([]*model.FeedbackPeriod, error) {
				return []*model.FeedbackPeriod{}, nil
			},
		}
		h := NewFeedbackPeriodHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/feedback-periods", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, user) // user has role.RoleUser

		if err := h.ListFeedbackPeriods(c); err != nil {
			t.Fatalf("ListFeedbackPeriods: unexpected error for non-admin: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d (non-admin allowed)", rec.Code, http.StatusOK)
		}
	})
}