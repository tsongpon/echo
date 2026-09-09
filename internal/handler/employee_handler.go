package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/dto"
	"github.com/tsongpon/echo/internal/model"
)

// EmployeeService is the consumer-defined contract for the employee
// application service. It is intentionally minimal: only the operations the
// handler actually needs. The concrete *service.EmployeeService satisfies it
// implicitly.
type EmployeeService interface {
	Register(ctx context.Context, inviteToken string, employee *model.Employee) (*model.Employee, error)
	Login(ctx context.Context, email, password string) (*model.Employee, error)
	GetByID(ctx context.Context, id string) (*model.Employee, error)
	VerifyEmail(ctx context.Context, token string) error
	ListByOrganization(ctx context.Context, organizationName string, limit int, cursorID string) ([]*model.Employee, string, error)
	AssignManager(ctx context.Context, callerID, targetID string, managerID *string) (*model.Employee, error)
	ListReportees(ctx context.Context, managerID string, limit int, cursorID string) ([]*model.Employee, string, error)
}

// EmployeeHandler exposes HTTP endpoints for employee operations.
type EmployeeHandler struct {
	employees EmployeeService
	tokens    *auth.TokenSigner
	logger    *slog.Logger
}

// NewEmployeeHandler creates an EmployeeHandler backed by the given service
// and token signer. If logger is nil, slog.Default() is used.
func NewEmployeeHandler(employees EmployeeService, tokens *auth.TokenSigner, logger *slog.Logger) *EmployeeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &EmployeeHandler{employees: employees, tokens: tokens, logger: logger}
}

// Register handles POST /v1/register: creates a new employee from the
// request body and returns the created employee.
func (h *EmployeeHandler) Register(c *echo.Context) error {
	var req dto.RegisterEmployeeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	created, err := h.employees.Register(c.Request().Context(), req.InviteToken, req.ToEmployee())
	if err != nil {
		var invalid apperror.ErrInvalidEmployee
		if errors.As(err, &invalid) {
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		if errors.Is(err, apperror.ErrEmailTaken) {
			return echo.NewHTTPError(http.StatusConflict, "email already taken")
		}
		if errors.Is(err, apperror.ErrOrganizationTaken) {
			return echo.NewHTTPError(http.StatusConflict, "organization already exists")
		}
		if errors.Is(err, apperror.ErrInvalidInvitationToken) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to register employee")
	}

	// The service sends the verification email as part of Register (best-effort);
	// the handler does not need to orchestrate delivery.
	return c.JSON(http.StatusCreated, dto.ToEmployeeResponse(created))
}

// Login handles POST /v1/login: validates the supplied email/password and,
// on success, returns a signed JWT for the authenticated employee.
func (h *EmployeeHandler) Login(c *echo.Context) error {
	var req dto.LoginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	result, err := h.employees.Login(c.Request().Context(), req.Email, req.Password)
	if err != nil {
		// Treat unknown email and wrong password identically to avoid
		// leaking which one was wrong. An unverified account is reported only
		// after the password is confirmed, with a distinct, actionable error.
		switch {
		case errors.Is(err, apperror.ErrInvalidCredentials):
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
		case errors.Is(err, apperror.ErrEmailNotVerified):
			return echo.NewHTTPError(http.StatusForbidden, "email not verified")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to login")
	}

	token, err := h.tokens.Sign(result)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to issue token")
	}

	return c.JSON(http.StatusOK, dto.LoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(h.tokens.TTL().Seconds()),
		Employee:    dto.ToEmployeeResponse(result),
	})
}

// Me handles GET /v1/me: returns the profile of the authenticated employee.
// The caller's identity is taken from the verified JWT (see Auth middleware);
// the employee is looked up by the token's subject, which is the employee ID.
func (h *EmployeeHandler) Me(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	employee, err := h.employees.GetByID(c.Request().Context(), claims.Subject)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "employee not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch employee")
	}

	return c.JSON(http.StatusOK, dto.ToEmployeeResponse(employee))
}

// VerifyEmail handles GET /v1/verify-email: validates the email-verification
// token supplied in the `token` query parameter and, on success, marks the
// employee's email as verified. The endpoint is public and is designed to be
// the target of the verification link sent in the registration email, so it
// uses GET with a query parameter rather than a request body. Identity is
// established solely from the token, which is bound to a specific employee ID.
func (h *EmployeeHandler) VerifyEmail(c *echo.Context) error {
	token := c.QueryParam("token")

	if err := h.employees.VerifyEmail(c.Request().Context(), token); err != nil {
		if errors.Is(err, apperror.ErrInvalidVerificationToken) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid or expired verification token")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to verify email")
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "email verified"})
}

// ListEmployees handles GET /v1/employees: returns one page of employees in
// the authenticated caller's organization, ordered by name ascending. The
// organization is taken from the JWT, so an employee can only see their own
// organization's members. Any authenticated employee may list colleagues (they
// need to see them to file feedback). The response omits the password field.
//
// Pagination is controlled by two optional query parameters:
//   - limit:  page size, default 20, max 100. Non-numeric or <= 0 falls back to
//     the default; values above the max are capped.
//   - cursor: the ID of the last employee from the previous page (the
//     next_cursor value the client received). Omit on the first page.
//
// The response includes next_cursor: the ID to pass as cursor on the next
// request, or null when there are no more pages. An unknown cursor (one that
// does not refer to an existing employee) returns 400.
func (h *EmployeeHandler) ListEmployees(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("employee list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	cursorID := c.QueryParam("cursor")

	employees, nextCursorID, err := h.employees.ListByOrganization(c.Request().Context(), claims.OrganizationName, limit, cursorID)
	if err != nil {
		var invalid apperror.ErrInvalidEmployee
		if errors.As(err, &invalid) {
			h.logger.Warn("employee list rejected: validation failed",
				"caller_id", claims.Subject, "organization_name", claims.OrganizationName, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			// An unknown cursor: the cursor ID did not match a stored employee.
			h.logger.Warn("employee list rejected: unknown cursor",
				"caller_id", claims.Subject, "organization_name", claims.OrganizationName, "cursor", cursorID)
			return echo.NewHTTPError(http.StatusBadRequest, "unknown cursor")
		}
		h.logger.Error("employee list failed",
			"error", err, "caller_id", claims.Subject, "organization_name", claims.OrganizationName, "cursor", cursorID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list employees")
	}

	return c.JSON(http.StatusOK, dto.ToEmployeeListResponse(employees, nextCursorID))
}

// AssignManager handles PATCH /v1/employees/:id/manager: assigns or clears
// the manager of the named employee. Only org admins may do this; the check
// is a fresh-load check inside the service (not a JWT-claims check), so a
// stale token cannot grant privileges the caller no longer has. The caller
// is the authenticated employee (taken from the JWT subject), so the route
// must be mounted behind the Auth middleware.
//
// The request body is {"manager_id": "<employee id>"} to assign, or
// {"manager_id": null} to clear the assignment.
func (h *EmployeeHandler) AssignManager(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("manager assignment rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	var req dto.AssignManagerRequest
	if err := c.Bind(&req); err != nil {
		h.logger.Warn("manager assignment rejected: invalid request body",
			"caller_id", claims.Subject, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	updated, err := h.employees.AssignManager(c.Request().Context(), claims.Subject, c.Param("id"), req.ManagerID)
	if err != nil {
		switch {
		case errors.Is(err, apperror.ErrForbidden):
			h.logger.Warn("manager assignment rejected: forbidden",
				"caller_id", claims.Subject, "target_id", c.Param("id"))
			return echo.NewHTTPError(http.StatusForbidden, "only org admins can assign managers within their organization")
		case errors.Is(err, apperror.ErrEmployeeNotFound):
			h.logger.Warn("manager assignment rejected: target not found",
				"caller_id", claims.Subject, "target_id", c.Param("id"))
			return echo.NewHTTPError(http.StatusNotFound, "employee not found")
		}
		var invalid apperror.ErrInvalidEmployee
		if errors.As(err, &invalid) {
			h.logger.Warn("manager assignment rejected: validation failed",
				"caller_id", claims.Subject, "target_id", c.Param("id"), "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("manager assignment failed",
			"error", err, "caller_id", claims.Subject, "target_id", c.Param("id"))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to assign manager")
	}

	return c.JSON(http.StatusOK, dto.ToEmployeeResponse(updated))
}

// ListMyReports handles GET /v1/me/reports: returns one page of the
// authenticated employee's direct reportees (employees whose manager_id is
// the caller), ordered by name ascending. The manager is the authenticated
// employee (taken from the JWT subject), so the route must be mounted behind
// the Auth middleware. Any authenticated employee may list their own
// reportees; an employee with none gets an empty list.
//
// Pagination is controlled by two optional query parameters:
//   - limit:  page size, default 20, max 100. Non-numeric or <= 0 falls back to
//     the default; values above the max are capped.
//   - cursor: the ID of the last employee from the previous page (the
//     next_cursor value the client received). Omit on the first page.
func (h *EmployeeHandler) ListMyReports(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("reportee list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	cursorID := c.QueryParam("cursor")

	employees, nextCursorID, err := h.employees.ListReportees(c.Request().Context(), claims.Subject, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			// An unknown cursor: the cursor ID did not match a stored employee.
			h.logger.Warn("reportee list rejected: unknown cursor",
				"caller_id", claims.Subject, "cursor", cursorID)
			return echo.NewHTTPError(http.StatusBadRequest, "unknown cursor")
		}
		var invalid apperror.ErrInvalidEmployee
		if errors.As(err, &invalid) {
			h.logger.Warn("reportee list rejected: validation failed",
				"caller_id", claims.Subject, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("reportee list failed",
			"error", err, "caller_id", claims.Subject, "cursor", cursorID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list reportees")
	}

	return c.JSON(http.StatusOK, dto.ToEmployeeListResponse(employees, nextCursorID))
}
