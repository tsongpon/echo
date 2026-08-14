package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/dto"
	"github.com/tsongpon/echo/internal/model"
	"github.com/tsongpon/echo/internal/repository"
	"github.com/tsongpon/echo/internal/service"
)

// EmployeeService is the consumer-defined contract for the employee
// application service. It is intentionally minimal: only the operations the
// handler actually needs. The concrete *service.EmployeeService satisfies it
// implicitly.
type EmployeeService interface {
	Register(ctx context.Context, employee *model.Employee) (*model.Employee, error)
	Login(ctx context.Context, email, password string) (*model.Employee, error)
	GetByID(ctx context.Context, id string) (*model.Employee, error)
	SendVerification(ctx context.Context, employee *model.Employee) error
	VerifyEmail(ctx context.Context, token string) error
}

// EmployeeHandler exposes HTTP endpoints for employee operations.
type EmployeeHandler struct {
	employees EmployeeService
	tokens    *auth.TokenSigner
}

// NewEmployeeHandler creates an EmployeeHandler backed by the given service
// and token signer.
func NewEmployeeHandler(employees EmployeeService, tokens *auth.TokenSigner) *EmployeeHandler {
	return &EmployeeHandler{employees: employees, tokens: tokens}
}

// Register handles POST /v1/register: creates a new employee from the
// request body and returns the created employee.
func (h *EmployeeHandler) Register(c *echo.Context) error {
	var req dto.RegisterEmployeeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	created, err := h.employees.Register(c.Request().Context(), req.ToEmployee())
	if err != nil {
		var invalid service.ErrInvalidEmployee
		switch {
		case errors.As(err, &invalid):
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		case errors.Is(err, repository.ErrNilEmployee):
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		default:
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to register employee")
		}
	}

	// Best-effort: registration has already succeeded. A failure to hand the
	// token to the mailer must not turn a 201 into a 5xx; the user can still
	// log in and a resend endpoint can re-issue later.
	if err := h.employees.SendVerification(c.Request().Context(), created); err != nil {
		c.Logger().Error("failed to send verification email", "error", err)
	}

	return c.JSON(http.StatusCreated, dto.ToEmployeeResponse(created))
}

// Login handles POST /v1/login: validates the supplied email/password and,
// on success, returns a signed JWT for the authenticated employee.
func (h *EmployeeHandler) Login(c *echo.Context) error {
	var req dto.LoginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	employee, err := h.employees.Login(c.Request().Context(), req.Email, req.Password)
	if err != nil {
		// Treat unknown email and wrong password identically to avoid
		// leaking which one was wrong.
		if errors.Is(err, service.ErrInvalidCredentials) {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to login")
	}

	token, err := h.tokens.Sign(employee)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to issue token")
	}

	return c.JSON(http.StatusOK, dto.LoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(h.tokens.TTL().Seconds()),
		Employee:    dto.ToEmployeeResponse(employee),
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
		if errors.Is(err, service.ErrEmployeeNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "employee not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch employee")
	}

	return c.JSON(http.StatusOK, dto.ToEmployeeResponse(employee))
}

// VerifyEmail handles POST /v1/verify-email: validates the supplied
// email-verification token and, on success, marks the employee's email as
// verified. The endpoint is public; identity is established solely from the
// token, which is bound to a specific employee ID.
func (h *EmployeeHandler) VerifyEmail(c *echo.Context) error {
	var req dto.VerifyEmailRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	if err := h.employees.VerifyEmail(c.Request().Context(), req.Token); err != nil {
		if errors.Is(err, service.ErrInvalidVerificationToken) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid or expired verification token")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to verify email")
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "email verified"})
}
