package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/dto"
	"github.com/tsongpon/echo/internal/model"
)

// FeedbackPeriodService is the consumer-defined contract for the feedback-
// period application service. It is intentionally minimal: only the operations
// the handler actually needs. The concrete *service.FeedbackPeriodService
// satisfies it implicitly.
type FeedbackPeriodService interface {
	Create(ctx context.Context, organizationName string, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error)
	ListByOrganization(ctx context.Context, organizationName string) ([]*model.FeedbackPeriod, error)
}

// FeedbackPeriodHandler exposes HTTP endpoints for feedback-period operations.
type FeedbackPeriodHandler struct {
	periods FeedbackPeriodService
	logger  *slog.Logger
}

// NewFeedbackPeriodHandler creates a FeedbackPeriodHandler backed by the given
// service. If logger is nil, slog.Default() is used.
func NewFeedbackPeriodHandler(periods FeedbackPeriodService, logger *slog.Logger) *FeedbackPeriodHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackPeriodHandler{periods: periods, logger: logger}
}

// CreateFeedbackPeriod handles POST /v1/feedback-periods: creates a new feedback
// period scoped to the authenticated employee's organization. The creator is
// the authenticated employee (taken from the JWT), so the route must be mounted
// behind the Auth middleware. Only org admins can open feedback periods.
func (h *FeedbackPeriodHandler) CreateFeedbackPeriod(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("feedback period create rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	if claims.Role != model.RoleOrgAdmin {
		h.logger.Warn("feedback period create rejected: caller is not org admin",
			"caller_id", claims.Subject, "role", string(claims.Role), "organization_name", claims.OrganizationName)
		return echo.NewHTTPError(http.StatusForbidden, "only org admins can create feedback periods")
	}

	var req dto.CreateFeedbackPeriodRequest
	if err := c.Bind(&req); err != nil {
		h.logger.Warn("feedback period create rejected: invalid request body",
			"caller_id", claims.Subject, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	created, err := h.periods.Create(c.Request().Context(), claims.OrganizationName, req.ToFeedbackPeriod())
	if err != nil {
		var invalid apperror.ErrInvalidFeedbackPeriod
		if errors.As(err, &invalid) {
			h.logger.Warn("feedback period create rejected: validation failed",
				"caller_id", claims.Subject, "organization_name", claims.OrganizationName, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("feedback period create failed",
			"error", err, "caller_id", claims.Subject, "organization_name", claims.OrganizationName, "name", req.Name)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create feedback period")
	}

	return c.JSON(http.StatusCreated, dto.ToFeedbackPeriodResponse(created))
}

// ListFeedbackPeriods handles GET /v1/feedback-periods: returns the feedback
// periods for the authenticated employee's organization, ordered by start date
// descending. The organization is taken from the JWT, so an employee can only
// see their own organization's periods. Any authenticated employee may list
// periods.
func (h *FeedbackPeriodHandler) ListFeedbackPeriods(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("feedback period list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	periods, err := h.periods.ListByOrganization(c.Request().Context(), claims.OrganizationName)
	if err != nil {
		var invalid apperror.ErrInvalidFeedbackPeriod
		if errors.As(err, &invalid) {
			h.logger.Warn("feedback period list rejected: validation failed",
				"caller_id", claims.Subject, "organization_name", claims.OrganizationName, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("feedback period list failed",
			"error", err, "caller_id", claims.Subject, "organization_name", claims.OrganizationName)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list feedback periods")
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackPeriodListResponse(periods))
}