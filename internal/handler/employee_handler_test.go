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
	listFn      func(ctx context.Context, organizationName string, limit int, cursorID string) ([]*model.Employee, string, error)
}

func (f *fakeEmployeeService) Register(_ context.Context, _ string, _ *model.Employee) (*model.Employee, error) {
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

func (f *fakeEmployeeService) ListByOrganization(_ context.Context, organizationName string, limit int, cursorID string) ([]*model.Employee, string, error) {
	if f.listFn != nil {
		return f.listFn(context.Background(), organizationName, limit, cursorID)
	}
	return []*model.Employee{}, "", nil
}

func TestMe_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
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
	svc := &fakeEmployeeService{byID: map[string]*model.Employee{"emp-1": emp}}
	h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
			h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "org-1",
		Title:            "Engineer",
		Email:            "alice@example.com",
		IsMailVerified:   true,
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
			h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "org-1",
		Title:            "Engineer",
		Email:            "alice@example.com",
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
			body:        `{"name":"Alice","organization_name":"org-1","title":"Engineer","email":"alice@example.com","password":"supersecret"}`,
			registerEmp: createdEmp,
			wantCode:    http.StatusCreated,
			wantBodyIn:  "alice@example.com",
		},
		{
			name:        "duplicate email",
			body:        `{"name":"Alice","organization_name":"org-1","email":"alice@example.com","password":"supersecret"}`,
			registerErr: apperror.ErrEmailTaken,
			wantCode:    http.StatusConflict,
			wantBodyIn:  "email already taken",
		},
		{
			name:        "validation error",
			body:        `{"name":"","organization_name":"org-1","email":"a@example.com","password":"supersecret"}`,
			registerErr: apperror.ErrInvalidEmployee("name is required"),
			wantCode:    http.StatusBadRequest,
			wantBodyIn:  "name is required",
		},
		{
			name:        "internal error",
			body:        `{"name":"Alice","organization_name":"org-1","email":"a@example.com","password":"supersecret"}`,
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
			h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

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

func TestListEmployees_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	caller := &model.Employee{
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "Acme",
		Role:             model.RoleUser,
		Title:            "Engineer",
		Email:            "alice@example.com",
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

	t.Run("success returns employees for the caller's organization", func(t *testing.T) {
		employees := []*model.Employee{
			{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Title: "Engineer", Email: "alice@acme.com"},
			{ID: "emp-2", Name: "Bob", OrganizationName: "Acme", Title: "Manager", Email: "bob@acme.com"},
		}
		svc := &fakeEmployeeService{
			listFn: func(_ context.Context, organizationName string, _ int, _ string) ([]*model.Employee, string, error) {
				if organizationName != "Acme" {
					t.Fatalf("handler passed org %q to service, want Acme (from JWT)", organizationName)
				}
				return employees, "", nil
			},
		}
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		if err := h.ListEmployees(c); err != nil {
			t.Fatalf("ListEmployees: unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"employees":[`) {
			t.Fatalf("response missing employees array: %s", body)
		}
		if !strings.Contains(body, `"id":"emp-1"`) || !strings.Contains(body, `"id":"emp-2"`) {
			t.Fatalf("response missing employee ids: %s", body)
		}
		// No next page on a complete result; next_cursor must be null.
		if !strings.Contains(body, `"next_cursor":null`) {
			t.Fatalf("expected next_cursor:null, got %s", body)
		}
		// The response must not leak the password field.
		if strings.Contains(body, "password") {
			t.Fatalf("response must not contain password: %s", body)
		}
	})

	t.Run("empty result returns empty array, not null", func(t *testing.T) {
		svc := &fakeEmployeeService{
			listFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Employee, string, error) {
				return []*model.Employee{}, "", nil
			},
		}
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		if err := h.ListEmployees(c); err != nil {
			t.Fatalf("ListEmployees: unexpected error: %v", err)
		}
		if !strings.Contains(rec.Body.String(), `"employees":[]`) {
			t.Fatalf("expected empty employees array, got %s", rec.Body.String())
		}
	})

	t.Run("paginated response includes next_cursor", func(t *testing.T) {
		page := []*model.Employee{
			{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Email: "a@acme.com"},
			{ID: "emp-2", Name: "Bob", OrganizationName: "Acme", Email: "b@acme.com"},
		}
		var gotLimit int
		var gotCursor string
		svc := &fakeEmployeeService{
			listFn: func(_ context.Context, _ string, limit int, cursorID string) ([]*model.Employee, string, error) {
				gotLimit = limit
				gotCursor = cursorID
				return page, "emp-2", nil
			},
		}
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees?limit=2&cursor=emp-0", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		if err := h.ListEmployees(c); err != nil {
			t.Fatalf("ListEmployees: unexpected error: %v", err)
		}
		if gotLimit != 2 {
			t.Fatalf("handler passed limit %d, want 2", gotLimit)
		}
		if gotCursor != "emp-0" {
			t.Fatalf("handler passed cursor %q, want emp-0", gotCursor)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"next_cursor":"emp-2"`) {
			t.Fatalf("expected next_cursor emp-2, got %s", body)
		}
	})

	t.Run("unknown cursor maps to 400", func(t *testing.T) {
		svc := &fakeEmployeeService{
			listFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Employee, string, error) {
				return nil, "", apperror.ErrEmployeeNotFound
			},
		}
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees?cursor=does-not-exist", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		err := h.ListEmployees(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusBadRequest)
		}
		if he.Message != "unknown cursor" {
			t.Fatalf("expected message %q, got %v", "unknown cursor", he.Message)
		}
	})

	t.Run("internal error maps to 500", func(t *testing.T) {
		svc := &fakeEmployeeService{
			listFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Employee, string, error) {
				return nil, "", errors.New("db down")
			},
		}
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		err := h.ListEmployees(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusInternalServerError)
		}
		if he.Message != "failed to list employees" {
			t.Fatalf("expected message %q, got %v", "failed to list employees", he.Message)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		svc := &fakeEmployeeService{}
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.ListEmployees(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})

	t.Run("non-admin user is allowed", func(t *testing.T) {
		// Listing colleagues is available to any authenticated employee (they
		// need to see colleagues to file feedback). The handler must not 403 a
		// role:user caller.
		svc := &fakeEmployeeService{
			listFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Employee, string, error) {
				return []*model.Employee{}, "", nil
			},
		}
		h := NewEmployeeHandler(svc, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller) // caller has role.RoleUser

		if err := h.ListEmployees(c); err != nil {
			t.Fatalf("ListEmployees: unexpected error for non-admin: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d (non-admin allowed)", rec.Code, http.StatusOK)
		}
	})
}
