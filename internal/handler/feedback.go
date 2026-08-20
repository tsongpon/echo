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

// FeedbackService is the consumer-defined contract for the feedback
// application service. It is intentionally minimal: only the operations the
// handler actually needs. The concrete *service.FeedbackService satisfies it
// implicitly.
type FeedbackService interface {
	Create(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error)
	ListByReviewee(ctx context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error)
}

// FeedbackHandler exposes HTTP endpoints for feedback operations.
type FeedbackHandler struct {
	feedbacks FeedbackService
	logger    *slog.Logger
}

// NewFeedbackHandler creates a FeedbackHandler backed by the given service. If
// logger is nil, slog.Default() is used.
func NewFeedbackHandler(feedbacks FeedbackService, logger *slog.Logger) *FeedbackHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackHandler{feedbacks: feedbacks, logger: logger}
}

// CreateFeedback handles POST /v1/feedbacks: creates a new feedback entry from
// the request body. The reviewer is the authenticated employee (taken from the
// JWT subject), so the route must be mounted behind the Auth middleware. Any
// authenticated employee may file feedback.
func (h *FeedbackHandler) CreateFeedback(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("feedback create rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	var req dto.CreateFeedbackRequest
	if err := c.Bind(&req); err != nil {
		h.logger.Warn("feedback create rejected: invalid request body",
			"reviewer_id", claims.Subject, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	created, err := h.feedbacks.Create(c.Request().Context(), claims.Subject, req.ToFeedback())
	if err != nil {
		var invalid apperror.ErrInvalidFeedback
		if errors.As(err, &invalid) {
			h.logger.Warn("feedback create rejected: validation failed",
				"reviewer_id", claims.Subject, "period_id", req.PeriodID, "reviewee_id", req.RevieweeID, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("feedback create failed",
			"error", err, "reviewer_id", claims.Subject, "period_id", req.PeriodID, "reviewee_id", req.RevieweeID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create feedback")
	}

	return c.JSON(http.StatusCreated, dto.ToFeedbackResponse(created))
}

// ListMyFeedbacks handles GET /v1/me/feedbacks: returns one page of feedback
// entries received by the authenticated employee (i.e. entries others have
// written about them), ordered by created_at descending (newest first). The
// reviewee is the authenticated employee (taken from the JWT subject), so the
// route must be mounted behind the Auth middleware. Any authenticated employee
// may list their own received feedback.
//
// Pagination is controlled by two optional query parameters:
//   - limit:  page size, default 20, max 100. Non-numeric or <= 0 falls back to
//     the default; values above the max are capped.
//   - cursor: the ID of the last feedback entry from the previous page (the
//     next_cursor value the client received). Omit on the first page.
//
// Visibility policy: for entries with visibility == "anonymous", the reviewer's
// identity is hidden from the caller (the reviewee) — reviewer_id is blanked
// in the response. Named entries include reviewer_id as usual.
func (h *FeedbackHandler) ListMyFeedbacks(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("feedback list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	cursorID := c.QueryParam("cursor")

	feedbacks, nextCursorID, err := h.feedbacks.ListByReviewee(c.Request().Context(), claims.Subject, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			// An unknown cursor: the cursor ID did not match a stored feedback.
			h.logger.Warn("feedback list rejected: unknown cursor",
				"caller_id", claims.Subject, "cursor", cursorID)
			return echo.NewHTTPError(http.StatusBadRequest, "unknown cursor")
		}
		var invalid apperror.ErrInvalidFeedback
		if errors.As(err, &invalid) {
			h.logger.Warn("feedback list rejected: validation failed",
				"caller_id", claims.Subject, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("feedback list failed",
			"error", err, "caller_id", claims.Subject, "cursor", cursorID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list feedbacks")
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackListResponse(feedbacks, nextCursorID))
}