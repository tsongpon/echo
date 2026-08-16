package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/model"
)

// fakeEmployeeService is an in-test stand-in for the handler.EmployeeService
// interface. It lets the Me handler be unit-tested without the real bcrypt-
// backed service or the in-memory repository.
type fakeEmployeeService struct {
	byID        map[string]*model.Employee
	getErr      error
	verifyErr   error
	loginEmp    *model.Employee
	loginErr    error
	registerEmp *model.Employee
	registerErr error
}

func (f *fakeEmployeeService) Register(_ context.Context, _ *model.Employee) (*model.Employee, error) {
	return f.registerEmp, f.registerErr
}

func (f *fakeEmployeeService) Login(_ context.Context, _, _ string) (*model.Employee, error) {
	return f.loginEmp, f.loginErr
}

func (f *fakeEmployeeService) GetByID(_ context.Context, id string) (*model.Employee, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if e, ok := f.byID[id]; ok {
		return e, nil
	}
	return nil, apperror.ErrEmployeeNotFound
}

func (f *fakeEmployeeService) VerifyEmail(_ context.Context, _ string) error {
	return f.verifyErr
}

func TestMe_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
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
	svc := &fakeEmployeeService{byID: map[string]*model.Employee{"emp-1": emp}}
	h := NewEmployeeHandler(svc, signer)

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

	t.Run("authenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, emp)

		if err := h.Me(c); err != nil {
			t.Fatalf("Me: unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"id":"emp-1"`) {
			t.Fatalf("response missing id: %s", body)
		}
		if !strings.Contains(body, `"email":"alice@example.com"`) {
			t.Fatalf("response missing email: %s", body)
		}
		if strings.Contains(body, "password") {
			t.Fatalf("response must not contain password: %s", body)
		}
	})

	t.Run("employee not found", func(t *testing.T) {
		other := &model.Employee{ID: "ghost", Email: "ghost@example.com"}

		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, other)

		err := h.Me(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusNotFound {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusNotFound)
		}
	})

	t.Run("no token", func(t *testing.T) {
		// Bypass middleware to assert the handler guards against missing
		// claims (defence in depth for miswired routes).
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.Me(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})

	t.Run("repository error", func(t *testing.T) {
		svc := &fakeEmployeeService{getErr: errors.New("db down")}
		h := NewEmployeeHandler(svc, signer)

		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, emp)

		err := h.Me(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusInternalServerError)
		}
	})
}

func TestAuth_Middleware(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	emp := &model.Employee{ID: "emp-1", Email: "alice@example.com"}
	validToken, err := signer.Sign(emp)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	cases := []struct {
		name     string
		authz    string
		wantCode int
	}{
		{name: "valid bearer", authz: "Bearer " + validToken, wantCode: http.StatusOK},
		{name: "missing header", authz: "", wantCode: http.StatusUnauthorized},
		{name: "wrong scheme", authz: "Basic abc", wantCode: http.StatusUnauthorized},
		{name: "garbage token", authz: "Bearer not-a-jwt", wantCode: http.StatusUnauthorized},
		{name: "lowercase scheme accepted", authz: "bearer " + validToken, wantCode: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			rec := httptest.NewRecorder()

			e := echo.New()
			c := e.NewContext(req, rec)

			mw := Auth(signer)
			handler := mw(func(c *echo.Context) error {
				claims := ClaimsFromContext(c)
				if claims == nil {
					t.Fatal("expected claims in context on success path")
				}
				if claims.Subject != "emp-1" {
					t.Fatalf("got subject %q, want emp-1", claims.Subject)
				}
				return c.String(http.StatusOK, "ok")
			})

			err := handler(c)
			if tc.wantCode == http.StatusOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
				}
			} else {
				he, ok := err.(*echo.HTTPError)
				if !ok {
					t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
				}
				if he.Code != tc.wantCode {
					t.Fatalf("got status %d, want %d", he.Code, tc.wantCode)
				}
			}
		})
	}
}

func TestVerifyEmail_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	cases := []struct {
		name      string
		token     string
		verifyErr error
		wantCode  int
	}{
		{
			name:      "valid token",
			token:     "abc",
			verifyErr: nil,
			wantCode:  http.StatusOK,
		},
		{
			name:      "invalid token",
			token:     "abc",
			verifyErr: apperror.ErrInvalidVerificationToken,
			wantCode:  http.StatusBadRequest,
		},
		{
			name:      "repository error",
			token:     "abc",
			verifyErr: errors.New("db down"),
			wantCode:  http.StatusInternalServerError,
		},
		{
			name:      "missing token",
			token:     "",
			verifyErr: apperror.ErrInvalidVerificationToken,
			wantCode:  http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeEmployeeService{verifyErr: tc.verifyErr}
			h := NewEmployeeHandler(svc, signer)

			req := httptest.NewRequest(http.MethodGet, "/v1/verify-email?token="+tc.token, nil)
			rec := httptest.NewRecorder()

			e := echo.New()
			c := e.NewContext(req, rec)

			err := h.VerifyEmail(c)
			if tc.wantCode == http.StatusOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if rec.Code != tc.wantCode {
					t.Fatalf("got status %d, want %d", rec.Code, tc.wantCode)
				}
				if !strings.Contains(rec.Body.String(), "email verified") {
					t.Fatalf("expected success message, got %s", rec.Body.String())
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
		})
	}
}

func TestLogin_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	verifiedEmp := &model.Employee{
		ID:             "emp-1",
		Name:           "Alice",
		OrganizationID: "org-1",
		Title:          "Engineer",
		Email:          "alice@example.com",
		IsMailVerified: true,
	}

	cases := []struct {
		name       string
		body       string
		loginEmp   *model.Employee
		loginErr   error
		wantCode   int
		wantBodyIn string
	}{
		{
			name:       "success",
			body:       `{"email":"alice@example.com","password":"supersecret"}`,
			loginEmp:   verifiedEmp,
			wantCode:   http.StatusOK,
			wantBodyIn: "access_token",
		},
		{
			name:       "invalid credentials",
			body:       `{"email":"alice@example.com","password":"wrong"}`,
			loginErr:   apperror.ErrInvalidCredentials,
			wantCode:   http.StatusUnauthorized,
			wantBodyIn: "invalid email or password",
		},
		{
			name:       "email not verified",
			body:       `{"email":"alice@example.com","password":"supersecret"}`,
			loginErr:   apperror.ErrEmailNotVerified,
			wantCode:   http.StatusForbidden,
			wantBodyIn: "email not verified",
		},
		{
			name:       "internal error",
			body:       `{"email":"alice@example.com","password":"supersecret"}`,
			loginErr:   errors.New("db down"),
			wantCode:   http.StatusInternalServerError,
			wantBodyIn: "failed to login",
		},
		{
			name:       "malformed body",
			body:       `{not-json`,
			wantCode:   http.StatusBadRequest,
			wantBodyIn: "invalid request body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeEmployeeService{loginEmp: tc.loginEmp, loginErr: tc.loginErr}
			h := NewEmployeeHandler(svc, signer)

			req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			e := echo.New()
			c := e.NewContext(req, rec)

			err := h.Login(c)
			if tc.wantCode == http.StatusOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if rec.Code != tc.wantCode {
					t.Fatalf("got status %d, want %d", rec.Code, tc.wantCode)
				}
				if !strings.Contains(rec.Body.String(), tc.wantBodyIn) {
					t.Fatalf("expected %q in body, got %s", tc.wantBodyIn, rec.Body.String())
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
			if he.Message != tc.wantBodyIn {
				t.Fatalf("expected message %q, got %v", tc.wantBodyIn, he.Message)
			}
		})
	}
}

func TestRegister_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	createdEmp := &model.Employee{
		ID:             "emp-1",
		Name:           "Alice",
		OrganizationID: "org-1",
		Title:          "Engineer",
		Email:          "alice@example.com",
	}

	cases := []struct {
		name        string
		body        string
		registerEmp *model.Employee
		registerErr error
		wantCode    int
		wantBodyIn  string
	}{
		{
			name:        "success",
			body:        `{"name":"Alice","organization_id":"org-1","title":"Engineer","email":"alice@example.com","password":"supersecret"}`,
			registerEmp: createdEmp,
			wantCode:    http.StatusCreated,
			wantBodyIn:  "alice@example.com",
		},
		{
			name:        "duplicate email",
			body:        `{"name":"Alice","organization_id":"org-1","email":"alice@example.com","password":"supersecret"}`,
			registerErr: apperror.ErrEmailTaken,
			wantCode:    http.StatusConflict,
			wantBodyIn:  "email already taken",
		},
		{
			name:        "validation error",
			body:        `{"name":"","organization_id":"org-1","email":"a@example.com","password":"supersecret"}`,
			registerErr: apperror.ErrInvalidEmployee("name is required"),
			wantCode:    http.StatusBadRequest,
			wantBodyIn:  "name is required",
		},
		{
			name:        "internal error",
			body:        `{"name":"Alice","organization_id":"org-1","email":"a@example.com","password":"supersecret"}`,
			registerErr: errors.New("db down"),
			wantCode:    http.StatusInternalServerError,
			wantBodyIn:  "failed to register employee",
		},
		{
			name:       "malformed body",
			body:       `{not-json`,
			wantCode:   http.StatusBadRequest,
			wantBodyIn: "invalid request body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeEmployeeService{registerEmp: tc.registerEmp, registerErr: tc.registerErr}
			h := NewEmployeeHandler(svc, signer)

			req := httptest.NewRequest(http.MethodPost, "/v1/register", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			e := echo.New()
			c := e.NewContext(req, rec)

			err := h.Register(c)
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
				if strings.Contains(rec.Body.String(), "password") {
					t.Fatalf("response must not contain password: %s", rec.Body.String())
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
			if he.Message != tc.wantBodyIn {
				t.Fatalf("expected message %q, got %v", tc.wantBodyIn, he.Message)
			}
		})
	}
}
