package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
)

// minScore and maxScore bound the six numeric score fields on a feedback entry.
// They mirror the Likert-style range used by the dto package's request
// validation, and are duplicated here rather than imported to keep the service
// layer independent of the transport layer.
const (
	minScore = 1
	maxScore = 5
)

// FeedbackRepository is the consumer-defined contract for the feedback
// repository. It is intentionally minimal: only the operations the service
// actually needs. The concrete repository implementation satisfies it
// implicitly.
type FeedbackRepository interface {
	Create(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error)
	ListByReviewee(ctx context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error)
}

// FeedbackPeriodLookup is the consumer-defined contract for resolving a feedback
// period by ID. It is a subset of FeedbackPeriodRepository so the feedback
// service can validate that a feedback entry's period_id refers to an existing
// period without depending on the full period repository or its create path.
type FeedbackPeriodLookup interface {
	GetByID(ctx context.Context, id string) (*model.FeedbackPeriod, error)
}

// FeedbackService is the application layer that orchestrates feedback
// operations against a FeedbackRepository. It validates that a feedback entry's
// period_id refers to an existing feedback period via the injected
// FeedbackPeriodLookup before persisting.
type FeedbackService struct {
	repo        FeedbackRepository
	periods     FeedbackPeriodLookup
	logger      *slog.Logger
}

// NewFeedbackService creates a FeedbackService backed by the given feedback
// repository and feedback-period lookup. If logger is nil, slog.Default() is
// used. periods may be nil to disable period-existence validation (useful in
// tests that don't care about the period); in production it should always be
// provided.
func NewFeedbackService(repo FeedbackRepository, periods FeedbackPeriodLookup, logger *slog.Logger) *FeedbackService {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackService{repo: repo, periods: periods, logger: logger}
}

// Create creates a new feedback entry after validating the input. The reviewer
// is identified by reviewerID, which is taken from the authenticated JWT by the
// handler and assigned here so a client cannot file feedback on someone else's
// behalf. A UUIDv7 ID is assigned and CreatedAt/UpdatedAt are set by the
// repository on persist. The reviewer cannot review themselves.
//
// Validation:
//   - period_id is required (non-empty after trim).
//   - reviewee_id is required (non-empty after trim).
//   - reviewer_id is required (non-empty after trim).
//   - reviewee_id must differ from reviewer_id (no self-review).
//   - each of the six score fields must be in [minScore, maxScore].
//   - strengths_comment is required (non-empty after trim).
//   - weaknesses_comment is required (non-empty after trim).
//   - visibility defaults to anonymous when empty, and must otherwise be one of
//     the defined model.FeedbackVisibility constants.
func (s *FeedbackService) Create(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error) {
	if feedback == nil {
		s.logger.Warn("feedback create rejected: nil feedback", "reviewer_id", reviewerID)
		return nil, apperror.ErrInvalidFeedback("feedback must not be nil")
	}
	if strings.TrimSpace(reviewerID) == "" {
		s.logger.Warn("feedback create rejected: missing reviewer_id", "period_id", feedback.PeriodID, "reviewee_id", feedback.RevieweeID)
		return nil, apperror.ErrInvalidFeedback("reviewer_id is required")
	}
	if strings.TrimSpace(feedback.PeriodID) == "" {
		s.logger.Warn("feedback create rejected: missing period_id", "reviewer_id", reviewerID, "reviewee_id", feedback.RevieweeID)
		return nil, apperror.ErrInvalidFeedback("period_id is required")
	}
	if s.periods != nil {
		if _, err := s.periods.GetByID(ctx, feedback.PeriodID); err != nil {
			if errors.Is(err, apperror.ErrFeedbackPeriodNotFound) {
				s.logger.Warn("feedback create rejected: period not found",
					"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "reviewee_id", feedback.RevieweeID)
				return nil, apperror.ErrInvalidFeedback("period_id does not refer to an existing feedback period")
			}
			s.logger.Error("feedback create aborted: period lookup failed",
				"error", err, "reviewer_id", reviewerID, "period_id", feedback.PeriodID)
			return nil, fmt.Errorf("validate feedback period: %w", err)
		}
	}
	if strings.TrimSpace(feedback.RevieweeID) == "" {
		s.logger.Warn("feedback create rejected: missing reviewee_id", "reviewer_id", reviewerID, "period_id", feedback.PeriodID)
		return nil, apperror.ErrInvalidFeedback("reviewee_id is required")
	}
	if feedback.RevieweeID == reviewerID {
		s.logger.Warn("feedback create rejected: self-review", "reviewer_id", reviewerID, "period_id", feedback.PeriodID)
		return nil, apperror.ErrInvalidFeedback("reviewer cannot review themselves")
	}
	if err := validateScore("communication_score", feedback.CommunicationScore); err != nil {
		s.logger.Warn("feedback create rejected: invalid communication_score",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "score", feedback.CommunicationScore)
		return nil, err
	}
	if err := validateScore("leadership_score", feedback.LeadershipScore); err != nil {
		s.logger.Warn("feedback create rejected: invalid leadership_score",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "score", feedback.LeadershipScore)
		return nil, err
	}
	if err := validateScore("technical_score", feedback.TechnicalScore); err != nil {
		s.logger.Warn("feedback create rejected: invalid technical_score",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "score", feedback.TechnicalScore)
		return nil, err
	}
	if err := validateScore("collaboration_score", feedback.CollaborationScore); err != nil {
		s.logger.Warn("feedback create rejected: invalid collaboration_score",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "score", feedback.CollaborationScore)
		return nil, err
	}
	if err := validateScore("delivery_score", feedback.DeliveryScore); err != nil {
		s.logger.Warn("feedback create rejected: invalid delivery_score",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "score", feedback.DeliveryScore)
		return nil, err
	}
	if err := validateScore("trust_score", feedback.TrustScore); err != nil {
		s.logger.Warn("feedback create rejected: invalid trust_score",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "score", feedback.TrustScore)
		return nil, err
	}
	if strings.TrimSpace(feedback.StrengthsComment) == "" {
		s.logger.Warn("feedback create rejected: missing strengths_comment",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID)
		return nil, apperror.ErrInvalidFeedback("strengths_comment is required")
	}
	if strings.TrimSpace(feedback.WeaknessesComment) == "" {
		s.logger.Warn("feedback create rejected: missing weaknesses_comment",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID)
		return nil, apperror.ErrInvalidFeedback("weaknesses_comment is required")
	}
	if feedback.Visibility != "" && !validVisibility(feedback.Visibility) {
		s.logger.Warn("feedback create rejected: invalid visibility",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "visibility", string(feedback.Visibility))
		return nil, apperror.ErrInvalidFeedback("visibility must be one of anonymous, named")
	}
	feedback.Visibility = normalizeVisibility(feedback.Visibility)

	feedback.ReviewerID = reviewerID
	id, err := uuid.NewV7()
	if err != nil {
		s.logger.Error("feedback create aborted: failed to generate UUID", "error", err, "reviewer_id", reviewerID)
		return nil, err
	}
	feedback.ID = id.String()

	created, err := s.repo.Create(ctx, feedback)
	if err != nil {
		s.logger.Error("feedback create aborted: repository create failed",
			"error", err, "feedback_id", feedback.ID, "reviewer_id", reviewerID, "reviewee_id", feedback.RevieweeID, "period_id", feedback.PeriodID)
		return nil, err
	}
	return created, nil
}

// validateScore returns an ErrInvalidFeedback when the score is outside the
// allowed Likert range.
func validateScore(field string, score int) error {
	if score < minScore || score > maxScore {
		return apperror.ErrInvalidFeedback(field + " must be between 1 and 5")
	}
	return nil
}

// normalizeVisibility defaults an empty visibility to anonymous and otherwise
// leaves a valid value untouched. It does not reject unknown values: that is
// the caller's responsibility via validVisibility below.
func normalizeVisibility(v model.FeedbackVisibility) model.FeedbackVisibility {
	if v == "" {
		return model.FeedbackVisibilityAnonymous
	}
	return v
}

// validVisibility reports whether v is one of the defined
// model.FeedbackVisibility constants.
func validVisibility(v model.FeedbackVisibility) bool {
	switch v {
	case model.FeedbackVisibilityAnonymous, model.FeedbackVisibilityNamed:
		return true
	default:
		return false
	}
}

// DefaultFeedbackListLimit is the page size used when the caller does not
// specify a limit for ListByReviewee. It mirrors the employee list default so
// the API has a consistent page size across list endpoints.
const DefaultFeedbackListLimit = 20

// MaxFeedbackListLimit caps the page size a caller can request for
// ListByReviewee. It protects the database from a single request pulling an
// entire reviewee's feedback history into memory; a client that needs more
// pages can follow next_cursor.
const MaxFeedbackListLimit = 100

// ListByReviewee returns one page of feedback entries received by the named
// reviewee (i.e. entries whose reviewee_id matches), ordered by created_at
// descending (newest first), plus the ID of the last entry on the page for use
// as the next page's cursor. The reviewee ID is taken from the authenticated
// caller's JWT by the handler, so an employee can only list their own received
// feedback.
//
// limit is the page size; if <= 0 DefaultFeedbackListLimit is used, and it is
// capped at MaxFeedbackListLimit. cursorID is the ID of the last feedback
// entry from the previous page (the next_cursor value the client received);
// an empty cursorID starts a new listing from the beginning. An unknown cursor
// (one that does not refer to an existing feedback entry) propagates as
// apperror.ErrFeedbackNotFound so the handler can map it to a 400. The returned
// nextCursorID is empty when there are no more pages.
func (s *FeedbackService) ListByReviewee(ctx context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(revieweeID) == "" {
		s.logger.Warn("feedback list rejected: missing reviewee_id")
		return nil, "", apperror.ErrInvalidFeedback("reviewee_id is required")
	}
	if limit <= 0 {
		limit = DefaultFeedbackListLimit
	}
	if limit > MaxFeedbackListLimit {
		limit = MaxFeedbackListLimit
	}

	feedbacks, nextCursorID, err := s.repo.ListByReviewee(ctx, revieweeID, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			// An unknown cursor is a caller error, not a service failure.
			// Pass it through so the handler can map it to a 400.
			return nil, "", err
		}
		s.logger.Error("feedback list failed", "error", err, "reviewee_id", revieweeID, "limit", limit, "cursor_id", cursorID)
		return nil, "", err
	}
	return feedbacks, nextCursorID, nil
}