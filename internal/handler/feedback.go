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
	ListByRevieweeForManager(ctx context.Context, callerID, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error)
	CreateDraft(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error)
	GetDraft(ctx context.Context, reviewerID, draftID string) (*model.Feedback, error)
	UpdateDraft(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error)
	SubmitDraft(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error)
	DeleteDraft(ctx context.Context, reviewerID, draftID string) error
	ListMyDrafts(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
	ListMyGivenFeedbacks(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
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
		switch {
		case errors.As(err, &invalid):
			h.logger.Warn("feedback create rejected: validation failed",
				"reviewer_id", claims.Subject, "period_id", req.PeriodID, "reviewee_id", req.RevieweeID, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		case errors.Is(err, apperror.ErrFeedbackPeriodClosed):
			h.logger.Warn("feedback create rejected: period not open",
				"reviewer_id", claims.Subject, "period_id", req.PeriodID, "reviewee_id", req.RevieweeID)
			return echo.NewHTTPError(http.StatusUnprocessableEntity, apperror.ErrFeedbackPeriodClosed.Error())
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

// ListMyGivenFeedbacks handles GET /v1/me/given-feedbacks: returns one page
// of feedback entries the authenticated employee has submitted (i.e. entries
// they wrote as the reviewer), ordered by created_at descending (newest
// first). The reviewer is the authenticated employee (taken from the JWT
// subject), so an employee can only list the feedback they gave themselves.
// Only submitted entries are listed; drafts stay in
// GET /v1/feedback-drafts.
//
// Pagination is controlled by two optional query parameters, identical to
// GET /v1/me/feedbacks:
//   - limit:  page size, default 20, max 100. Non-numeric or <= 0 falls back to
//     the default; values above the max are capped.
//   - cursor: the ID of the last feedback entry from the previous page (the
//     next_cursor value the client received). Omit on the first page.
//
// Visibility policy: none applies here — the caller is the reviewer of every
// entry, so reviewer_id is always included, even for anonymous entries (an
// author always knows their own identity).
func (h *FeedbackHandler) ListMyGivenFeedbacks(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("given feedback list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	cursorID := c.QueryParam("cursor")

	feedbacks, nextCursorID, err := h.feedbacks.ListMyGivenFeedbacks(c.Request().Context(), claims.Subject, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			// An unknown cursor: the cursor ID did not match a stored feedback.
			h.logger.Warn("given feedback list rejected: unknown cursor",
				"caller_id", claims.Subject, "cursor", cursorID)
			return echo.NewHTTPError(http.StatusBadRequest, "unknown cursor")
		}
		var invalid apperror.ErrInvalidFeedback
		if errors.As(err, &invalid) {
			h.logger.Warn("given feedback list rejected: validation failed",
				"caller_id", claims.Subject, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("given feedback list failed",
			"error", err, "caller_id", claims.Subject, "cursor", cursorID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list feedbacks")
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackGivenListResponse(feedbacks, nextCursorID))
}

// ListEmployeeFeedbacks handles GET /v1/employees/:id/feedbacks: returns one
// page of feedback entries received by the named employee, but only when the
// authenticated caller is that employee's manager. The route must be mounted
// behind the Auth middleware. A caller who is not the reviewee's manager
// gets 403; an unknown employee ID gets 404.
//
// Pagination is controlled by two optional query parameters, identical to
// GET /v1/me/feedbacks:
//   - limit:  page size, default 20, max 100. Non-numeric or <= 0 falls back to
//     the default; values above the max are capped.
//   - cursor: the ID of the last feedback entry from the previous page (the
//     next_cursor value the client received). Omit on the first page.
//
// Visibility policy: as with the reviewee's own view, entries with
// visibility == "anonymous" have their reviewer_id blanked in the response —
// the manager sees the same redacted view the reviewee sees. Named entries
// include reviewer_id as usual.
func (h *FeedbackHandler) ListEmployeeFeedbacks(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		h.logger.Warn("manager feedback list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	cursorID := c.QueryParam("cursor")

	feedbacks, nextCursorID, err := h.feedbacks.ListByRevieweeForManager(c.Request().Context(), claims.Subject, c.Param("id"), limit, cursorID)
	if err != nil {
		switch {
		case errors.Is(err, apperror.ErrForbidden):
			h.logger.Warn("manager feedback list rejected: forbidden",
				"caller_id", claims.Subject, "reviewee_id", c.Param("id"))
			return echo.NewHTTPError(http.StatusForbidden, "only the employee's manager can view their feedback")
		case errors.Is(err, apperror.ErrEmployeeNotFound):
			h.logger.Warn("manager feedback list rejected: reviewee not found",
				"caller_id", claims.Subject, "reviewee_id", c.Param("id"))
			return echo.NewHTTPError(http.StatusNotFound, "employee not found")
		case errors.Is(err, apperror.ErrFeedbackNotFound):
			// An unknown cursor: the cursor ID did not match a stored feedback.
			h.logger.Warn("manager feedback list rejected: unknown cursor",
				"caller_id", claims.Subject, "reviewee_id", c.Param("id"), "cursor", cursorID)
			return echo.NewHTTPError(http.StatusBadRequest, "unknown cursor")
		}
		var invalid apperror.ErrInvalidFeedback
		if errors.As(err, &invalid) {
			h.logger.Warn("manager feedback list rejected: validation failed",
				"caller_id", claims.Subject, "reviewee_id", c.Param("id"), "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("manager feedback list failed",
			"error", err, "caller_id", claims.Subject, "reviewee_id", c.Param("id"), "cursor", cursorID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list feedbacks")
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackListResponse(feedbacks, nextCursorID))
}

// draftApply resolves the fields an UpdateFeedbackDraftRequest explicitly
// supplied into a mutation on the freshly loaded draft. Omitted fields are
// left untouched; this is where the pointer-based partial-update request
// becomes a concrete assignment.
func draftApply(req *dto.UpdateFeedbackDraftRequest) func(*model.Feedback) error {
	return func(f *model.Feedback) error {
		if req.CommunicationScore != nil {
			f.CommunicationScore = *req.CommunicationScore
		}
		if req.LeadershipScore != nil {
			f.LeadershipScore = *req.LeadershipScore
		}
		if req.TechnicalScore != nil {
			f.TechnicalScore = *req.TechnicalScore
		}
		if req.CollaborationScore != nil {
			f.CollaborationScore = *req.CollaborationScore
		}
		if req.DeliveryScore != nil {
			f.DeliveryScore = *req.DeliveryScore
		}
		if req.TrustScore != nil {
			f.TrustScore = *req.TrustScore
		}
		if req.StrengthsComment != nil {
			f.StrengthsComment = *req.StrengthsComment
		}
		if req.WeaknessesComment != nil {
			f.WeaknessesComment = *req.WeaknessesComment
		}
		if req.Visibility != nil {
			if *req.Visibility != "" && !(*req.Visibility == model.FeedbackVisibilityAnonymous || *req.Visibility == model.FeedbackVisibilityNamed) {
				return apperror.ErrInvalidFeedback("visibility must be one of anonymous, named")
			}
			f.Visibility = *req.Visibility
		}
		return nil
	}
}

// mapDraftError translates the service's draft errors into HTTP responses.
// It centralizes the mapping shared by every draft endpoint so the switch
// stays consistent across them. The prefix argument labels log lines.
func (h *FeedbackHandler) mapDraftError(prefix, callerID, draftID string, err error) error {
	var invalid apperror.ErrInvalidFeedback
	switch {
	case errors.As(err, &invalid):
		h.logger.Warn(prefix+" rejected: validation failed",
			"caller_id", callerID, "draft_id", draftID, "reason", invalid.Error())
		return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
	case errors.Is(err, apperror.ErrFeedbackNotFound):
		// Unknown draft ID, a draft owned by someone else, or an entry that
		// is no longer a draft: all deliberately indistinguishable so a
		// draft's existence never leaks to anyone but its author.
		h.logger.Warn(prefix+" rejected: draft not found",
			"caller_id", callerID, "draft_id", draftID)
		return echo.NewHTTPError(http.StatusNotFound, "feedback draft not found")
	case errors.Is(err, apperror.ErrFeedbackDraftAlreadyExists):
		h.logger.Warn(prefix+" rejected: draft already exists",
			"caller_id", callerID, "draft_id", draftID)
		return echo.NewHTTPError(http.StatusConflict, apperror.ErrFeedbackDraftAlreadyExists.Error())
	case errors.Is(err, apperror.ErrFeedbackPeriodClosed):
		h.logger.Warn(prefix+" rejected: period not open",
			"caller_id", callerID, "draft_id", draftID)
		return echo.NewHTTPError(http.StatusUnprocessableEntity, apperror.ErrFeedbackPeriodClosed.Error())
	case errors.Is(err, apperror.ErrFeedbackConcurrentUpdate):
		h.logger.Warn(prefix+" rejected: draft changed concurrently",
			"caller_id", callerID, "draft_id", draftID)
		return echo.NewHTTPError(http.StatusConflict, "draft was modified concurrently; fetch it again and retry")
	}
	h.logger.Error(prefix+" failed",
		"error", err, "caller_id", callerID, "draft_id", draftID)
	return echo.NewHTTPError(http.StatusInternalServerError, "failed")
}

// CreateFeedbackDraft handles POST /v1/feedback-drafts: creates a new draft
// feedback entry owned by the authenticated employee (taken from the JWT
// subject). Only period_id and reviewee_id are required; scores, comments,
// and visibility may be supplied now or filled in later via the update
// endpoint. At most one draft may exist per (reviewer, reviewee, period).
func (h *FeedbackHandler) CreateFeedbackDraft(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("draft create rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	var req dto.CreateFeedbackDraftRequest
	if err := c.Bind(&req); err != nil {
		h.logger.Warn("draft create rejected: invalid request body",
			"reviewer_id", claims.Subject, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	created, err := h.feedbacks.CreateDraft(c.Request().Context(), claims.Subject, req.ToFeedback())
	if err != nil {
		return h.mapDraftError("draft create", claims.Subject, "", err)
	}

	return c.JSON(http.StatusCreated, dto.ToFeedbackResponse(created))
}

// GetFeedbackDraft handles GET /v1/feedback-drafts/:id: returns the caller's
// own draft. A draft owned by anyone else — like a draft that does not
// exist — yields 404 rather than 403, so a draft's existence never leaks.
func (h *FeedbackHandler) GetFeedbackDraft(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("draft get rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	draft, err := h.feedbacks.GetDraft(c.Request().Context(), claims.Subject, c.Param("id"))
	if err != nil {
		return h.mapDraftError("draft get", claims.Subject, c.Param("id"), err)
	}
	return c.JSON(http.StatusOK, dto.ToFeedbackResponse(draft))
}

// UpdateFeedbackDraft handles PATCH /v1/feedback-drafts/:id: applies a
// partial update to the caller's draft. Only fields present in the body are
// overwritten; omitted fields keep their stored values. period_id,
// reviewee_id, and reviewer_id are fixed once the draft is created. The
// full submit-time validation (scores required, comments required, period
// window open) runs at submit, not here: a draft is allowed to be
// incomplete.
func (h *FeedbackHandler) UpdateFeedbackDraft(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("draft update rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	var req dto.UpdateFeedbackDraftRequest
	if err := c.Bind(&req); err != nil {
		h.logger.Warn("draft update rejected: invalid request body",
			"reviewer_id", claims.Subject, "draft_id", c.Param("id"), "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	apply := draftApply(&req)
	updated, err := h.feedbacks.UpdateDraft(c.Request().Context(), claims.Subject, c.Param("id"), apply)
	if err != nil {
		return h.mapDraftError("draft update", claims.Subject, c.Param("id"), err)
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackResponse(updated))
}

// SubmitFeedbackDraft handles POST /v1/feedback-drafts/:id/submit: submits
// the caller's draft, transitioning it to the submitted state and releasing
// its uniqueness claim. The body is optional; when present, it carries the
// same shape as the update request and its fields are applied before
// validation, letting a client submit and final-edit in one call. The full
// feedback validation runs here: all six scores must be present and in
// range, both comments must be non-empty, and the period's date window must
// be open.
func (h *FeedbackHandler) SubmitFeedbackDraft(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("draft submit rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	// The body is optional: an empty body submits the draft as stored.
	var req dto.UpdateFeedbackDraftRequest
	if err := c.Bind(&req); err != nil && c.Request().Body != nil && c.Request().ContentLength != 0 {
		h.logger.Warn("draft submit rejected: invalid request body",
			"reviewer_id", claims.Subject, "draft_id", c.Param("id"), "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	apply := draftApply(&req)
	submitted, err := h.feedbacks.SubmitDraft(c.Request().Context(), claims.Subject, c.Param("id"), apply)
	if err != nil {
		return h.mapDraftError("draft submit", claims.Subject, c.Param("id"), err)
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackResponse(submitted))
}

// DeleteFeedbackDraft handles DELETE /v1/feedback-drafts/:id: removes the
// caller's draft and releases its uniqueness claim so a new draft may be
// started for the same (reviewee, period) immediately. Deleting an entry
// that is missing or no longer a draft yields 404; submitted feedback is
// immutable.
func (h *FeedbackHandler) DeleteFeedbackDraft(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("draft delete rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	if err := h.feedbacks.DeleteDraft(c.Request().Context(), claims.Subject, c.Param("id")); err != nil {
		return h.mapDraftError("draft delete", claims.Subject, c.Param("id"), err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListMyFeedbackDrafts handles GET /v1/feedback-drafts: returns one page of
// the authenticated employee's own draft entries, ordered by created_at
// descending (newest first). Pagination is controlled by the same two
// optional query parameters as the feedback listings:
//   - limit:  page size, default 20, max 100. Non-numeric or <= 0 falls back to
//     the default; values above the max are capped.
//   - cursor: the ID of the last draft from the previous page. Omit on the
//     first page.
func (h *FeedbackHandler) ListMyFeedbackDrafts(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		h.logger.Warn("draft list rejected: missing claims (route miswired?)")
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	cursorID := c.QueryParam("cursor")

	drafts, nextCursorID, err := h.feedbacks.ListMyDrafts(c.Request().Context(), claims.Subject, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			h.logger.Warn("draft list rejected: unknown cursor",
				"caller_id", claims.Subject, "cursor", cursorID)
			return echo.NewHTTPError(http.StatusBadRequest, "unknown cursor")
		}
		var invalid apperror.ErrInvalidFeedback
		if errors.As(err, &invalid) {
			h.logger.Warn("draft list rejected: validation failed",
				"caller_id", claims.Subject, "reason", invalid.Error())
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		h.logger.Error("draft list failed",
			"error", err, "caller_id", claims.Subject, "cursor", cursorID)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list feedback drafts")
	}

	return c.JSON(http.StatusOK, dto.ToFeedbackDraftListResponse(drafts, nextCursorID))
}
