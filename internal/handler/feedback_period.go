package handler

import (
	"context"
	"errors"
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
}

// FeedbackPeriodHandler exposes HTTP endpoints for feedback-period operations.
type FeedbackPeriodHandler struct {
	periods FeedbackPeriodService
}

// NewFeedbackPeriodHandler creates a FeedbackPeriodHandler backed by the given
// service.
func NewFeedbackPeriodHandler(periods FeedbackPeriodService) *FeedbackPeriodHandler {
	return &FeedbackPeriodHandler{periods: periods}
}

// CreateFeedbackPeriod handles POST /v1/feedback-period: creates a new feedback
// period scoped to the authenticated employee's organization. The creator is
// the authenticated employee (taken from the JWT), so the route must be mounted
// behind the Auth middleware. Only org admins can open feedback periods.
func (h *FeedbackPeriodHandler) CreateFeedbackPeriod(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	if claims.Role != model.RoleOrgAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "only org admins can create feedback periods")
	}

	var req dto.CreateFeedbackPeriodRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	created, err := h.periods.Create(c.Request().Context(), claims.OrganizationName, req.ToFeedbackPeriod())
	if err != nil {
		var invalid apperror.ErrInvalidFeedbackPeriod
		if errors.As(err, &invalid) {
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create feedback period")
	}

	return c.JSON(http.StatusCreated, dto.ToFeedbackPeriodResponse(created))
}