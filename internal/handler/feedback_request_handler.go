package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/dto"
	"github.com/tsongpon/echo/internal/model"
)

// FeedbackRequestService is the consumer-defined contract for the
// feedback-request application service. It is intentionally minimal: only
// the operations the handler actually needs. The concrete
// *service.FeedbackRequestService satisfies it implicitly.
type FeedbackRequestService interface {
	Create(ctx context.Context, requesterID string, request *model.FeedbackRequest) (*model.FeedbackRequest, error)
	Decline(ctx context.Context, callerID, requestID string) (*model.FeedbackRequest, error)
	List(ctx context.Context, callerID, direction string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error)
}

// FeedbackRequestHandler exposes HTTP endpoints for feedback-request
// operations.
type FeedbackRequestHandler struct {
	requests FeedbackRequestService
	logger   *slog.Logger
}

// NewFeedbackRequestHandler creates a FeedbackRequestHandler backed by the
// given service. If logger is nil, slog.Default() is used.
func NewFeedbackRequestHandler(requests FeedbackRequestService, logger *slog.Logger) *FeedbackRequestHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackRequestHandler{requests: requests, logger: logger}
}

// Create handles POST /v1/feedback-requests: asks a colleague for feedback
// in a feedback period. The requester is the authenticated employee (taken
// from the JWT subject), so the route must be mounted behind the Auth
// middleware. At most one open request may exist per (requester, requestee,
// period) triple; a duplicate yields 409. A notification email is sent to
// the requestee on a best-effort basis.
func (h *FeedbackRequestHandler) Create(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("feedback request create rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	var req dto.CreateFeedbackRequestRequest
	if err := c.Bind(&req); err != nil {
		h.logger.Warn("feedback request create rejected: invalid request body",
			"requester_id", claims.Subject, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	created, err := h.requests.Create(c.Request().Context(), claims.Subject, req.ToFeedbackRequest())
	if err != nil {
		return mapRequestError(err, h.logger, "feedback request create", claims.Subject)
	}

	return c.JSON(http.StatusCreated, dto.ToFeedbackRequestResponse(created))
}

// List handles GET /v1/me/feedback-requests: returns one page of the
// caller's requests, ordered by created_at descending (newest first). The
// optional direction query parameter selects the side: "received" (the
// default) lists requests where the caller is the requestee; "sent" lists
// requests where the caller is the requester.
//
// Pagination is controlled by two optional query parameters, identical to
// GET /v1/me/feedbacks:
//   - limit:  page size, default 20, max 100. Non-numeric or <= 0 falls back to
//     the default; values above the max are capped.
//   - cursor: the ID of the last request from the previous page (the
//     next_cursor value the client received). Omit on the first page.
func (h *FeedbackRequestHandler) List(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("feedback request list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	cursorID := c.QueryParam("cursor")
	direction := c.QueryParam("direction")

	requests, nextCursorID, err := h.requests.List(c.Request().Context(), claims.Subject, direction, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackRequestNotFound) && cursorID != "" {
			// On the list path a not-found means the cursor was unknown: 400,
			// consistent with the other list endpoints.
			h.logger.Warn("feedback request list rejected: unknown cursor",
				"caller_id", claims.Subject, "cursor", cursorID)
			return echo.NewHTTPError(http.StatusBadRequest, "unknown cursor")
		}
		return mapRequestError(err, h.logger, "feedback request list", claims.Subject)
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackRequestListResponse(requests, nextCursorID))
}

// Decline handles POST /v1/feedback-requests/:id/decline: transitions the
// caller's received request to declined, releasing its
// (requester, requestee, period) slot so the requester may ask again. Only
// the requestee may decline; anyone else — like a request that does not
// exist — gets 404 rather than 403, so a request's existence never leaks to
// other employees.
func (h *FeedbackRequestHandler) Decline(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("feedback request decline rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	declined, err := h.requests.Decline(c.Request().Context(), claims.Subject, c.Param("id"))
	if err != nil {
		return mapRequestError(err, h.logger, "feedback request decline", claims.Subject)
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackRequestResponse(declined))
}

// mapRequestError translates service errors from the feedback-request
// endpoints onto HTTP responses. The prefix identifies the operation in log
// lines; callerID is the authenticated employee.
func mapRequestError(err error, logger *slog.Logger, prefix, callerID string) error {
	switch {
	case errors.Is(err, apperror.ErrFeedbackRequestAlreadyExists):
		logger.Warn(prefix + " rejected: request already exists")
		return echo.NewHTTPError(http.StatusConflict, apperror.ErrFeedbackRequestAlreadyExists.Error())
	case errors.Is(err, apperror.ErrFeedbackRequestNotFound):
		logger.Warn(prefix+" rejected: request not found", "caller_id", callerID)
		return echo.NewHTTPError(http.StatusNotFound, apperror.ErrFeedbackRequestNotFound.Error())
	case errors.Is(err, apperror.ErrFeedbackPeriodNotFound):
		// Should not surface (the service maps it to a validation error), but
		// kept for safety.
		logger.Warn(prefix+" rejected: period not found", "caller_id", callerID)
		return echo.NewHTTPError(http.StatusBadRequest, apperror.ErrFeedbackPeriodNotFound.Error())
	case errors.Is(err, apperror.ErrEmployeeNotFound):
		// Should not surface (the service maps it to a validation error), but
		// kept for safety.
		logger.Warn(prefix+" rejected: employee not found", "caller_id", callerID)
		return echo.NewHTTPError(http.StatusBadRequest, apperror.ErrEmployeeNotFound.Error())
	}
	var invalid apperror.ErrInvalidFeedbackRequest
	if errors.As(err, &invalid) {
		logger.Warn(prefix+" rejected: validation failed", "caller_id", callerID, "reason", invalid.Error())
		return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
	}
	logger.Error(prefix+" failed", "error", err, "caller_id", callerID)
	return echo.NewHTTPError(http.StatusInternalServerError, "failed")
}
