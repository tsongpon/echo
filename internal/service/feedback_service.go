package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	Get(ctx context.Context, id string) (*model.Feedback, error)
	CreateDraft(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error)
	UpdateDraft(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error)
	SubmitDraft(ctx context.Context, id string) (*model.Feedback, error)
	DeleteDraft(ctx context.Context, id string) error
	ListDraftsByReviewer(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
	ListSubmittedByReviewer(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
}

// FeedbackPeriodLookup is the consumer-defined contract for resolving a feedback
// period by ID. It is a subset of FeedbackPeriodRepository so the feedback
// service can validate that a feedback entry's period_id refers to an existing
// period without depending on the full period repository or its create path.
type FeedbackPeriodLookup interface {
	GetByID(ctx context.Context, id string) (*model.FeedbackPeriod, error)
}

// EmployeeLookup is the consumer-defined contract for resolving an employee
// by ID. It is a subset of EmployeeRepository so the feedback service can
// authorize the manager-view endpoint (the caller must be the reviewee's
// manager) without depending on the full employee repository.
type EmployeeLookup interface {
	GetByID(ctx context.Context, id string) (*model.Employee, error)
}

// FeedbackRequestCloser is the consumer-defined contract for completing a
// feedback request when its matching feedback is submitted. The feedback
// service calls it after a successful create/submit; the concrete
// FeedbackRequestService implements it. Keeping the contract to one method
// means the feedback service knows nothing about request storage.
type FeedbackRequestCloser interface {
	CloseMatchingOpen(ctx context.Context, requesterID, requesteeID, periodID string) error
}

// FeedbackService is the application layer that orchestrates feedback
// operations against a FeedbackRepository. It validates that a feedback entry's
// period_id refers to an existing feedback period via the injected
// FeedbackPeriodLookup before persisting, and authorizes the manager-view
// listing via the injected EmployeeLookup.
type FeedbackService struct {
	feedbackRepo          FeedbackRepository
	periodLookup          FeedbackPeriodLookup
	employeeLookup        EmployeeLookup
	feedbackRequestCloser FeedbackRequestCloser
	logger                *slog.Logger
}

// periodFor loads and returns the feedback period referenced by id, mapping a
// missing period to a validation error and propagating lookup failures
// unchanged. Shared by the create and submit paths, which both must enforce
// that the period exists and is open.
func (s *FeedbackService) periodFor(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.FeedbackPeriod, error) {
	if s.periodLookup == nil {
		return nil, nil
	}
	period, err := s.periodLookup.GetByID(ctx, feedback.PeriodID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackPeriodNotFound) {
			s.logger.Warn("feedback period lookup failed: period not found",
				"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "reviewee_id", feedback.RevieweeID)
			return nil, apperror.ErrInvalidFeedback("period_id does not refer to an existing feedback period")
		}
		s.logger.Error("feedback period lookup failed",
			"error", err, "reviewer_id", reviewerID, "period_id", feedback.PeriodID)
		return nil, fmt.Errorf("validate feedback period: %w", err)
	}
	return period, nil
}

// validatePeriodOpen reports whether the given period is open for submission
// at the current time: now must fall within [StartDate, EndDate]. A period
// whose window has not started or has already ended yields
// apperror.ErrFeedbackPeriodClosed. When the period could not be loaded, the
// error is returned unchanged for the caller to handle.
func validatePeriodOpen(period *model.FeedbackPeriod) error {
	if period == nil {
		return nil
	}
	now := time.Now()
	if now.Before(period.StartDate) || now.After(period.EndDate) {
		return apperror.ErrFeedbackPeriodClosed
	}
	return nil
}

// NewFeedbackService creates a FeedbackService backed by the given feedback
// repository, feedback-period lookup, employee lookup, and feedback-request
// closer. If logger is nil, slog.Default() is used. periods may be nil to
// disable period-existence validation (useful in tests that don't care about
// the period); in production it should always be provided. employees may
// likewise be nil in tests, but ListByRevieweeForManager fails closed when it
// is nil (the manager-view authorization cannot be performed without it).
// requests may be nil to disable request completion (tests); in production
// it should be the FeedbackRequestService so submitted feedback completes
// the matching open request, best-effort.
func NewFeedbackService(repo FeedbackRepository, periods FeedbackPeriodLookup, employees EmployeeLookup, requests FeedbackRequestCloser, logger *slog.Logger) *FeedbackService {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackService{feedbackRepo: repo, periodLookup: periods, employeeLookup: employees, feedbackRequestCloser: requests, logger: logger}
}

// closeMatchingRequests completes the open feedback request — if any — that
// the reviewee (the person the feedback is about... reversed here: the
// reviewer just wrote feedback about revieweeID, so any request FROM
// revieweeID TO the reviewer in this period is fulfilled) matching the
// just-submitted feedback. Best-effort: a failure is logged and never
// surfaces to the submitter, because the feedback itself is already durable.
func (s *FeedbackService) closeMatchingRequests(ctx context.Context, reviewerID, revieweeID, periodID string) {
	if s.feedbackRequestCloser == nil {
		return
	}
	// The reviewer wrote feedback about the reviewee, so the fulfilled request
	// is: requester = revieweeID, requestee = reviewerID, in this period.
	if err := s.feedbackRequestCloser.CloseMatchingOpen(ctx, revieweeID, reviewerID, periodID); err != nil {
		s.logger.Error("failed to complete matching feedback request",
			"error", err, "reviewer_id", reviewerID, "reviewee_id", revieweeID, "period_id", periodID)
	}
}

// Create creates a new submitted feedback entry after validating the input.
// The reviewer is identified by reviewerID, which is taken from the
// authenticated JWT by the handler and assigned here so a client cannot file
// feedback on someone else's behalf. A UUIDv7 ID is assigned and
// CreatedAt/UpdatedAt are set by the repository on persist. The reviewer
// cannot review themselves.
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
//
// The referenced period must exist and its date window must be open: now must
// fall within [start_date, end_date]. An unopened or closed period yields
// apperror.ErrFeedbackPeriodClosed.
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
	period, err := s.periodFor(ctx, reviewerID, feedback)
	if err != nil {
		return nil, err
	}
	if err := validatePeriodOpen(period); err != nil {
		s.logger.Warn("feedback create rejected: period not open",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "reviewee_id", feedback.RevieweeID)
		return nil, err
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
	feedback.Status = model.FeedbackStatusSubmitted
	feedback.ReviewerID = reviewerID
	id, err := uuid.NewV7()
	if err != nil {
		s.logger.Error("feedback create aborted: failed to generate UUID", "error", err, "reviewer_id", reviewerID)
		return nil, err
	}
	feedback.ID = id.String()

	created, err := s.feedbackRepo.Create(ctx, feedback)
	if err != nil {
		s.logger.Error("feedback create aborted: repository create failed",
			"error", err, "feedback_id", feedback.ID, "reviewer_id", reviewerID, "reviewee_id", feedback.RevieweeID, "period_id", feedback.PeriodID)
		return nil, err
	}

	// The submitted feedback may fulfill an open request from the reviewee to
	// the reviewer in this period; complete it best-effort.
	s.closeMatchingRequests(ctx, reviewerID, created.RevieweeID, created.PeriodID)

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

	feedbacks, nextCursorID, err := s.feedbackRepo.ListByReviewee(ctx, revieweeID, limit, cursorID)
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

// ListByRevieweeForManager returns one page of feedback entries received by
// the named reviewee, but only when the caller is that reviewee's manager.
// It backs the manager view of a reportee's feedback (e.g. GET
// /v1/employees/:id/feedbacks). Pagination, ordering, and cursor semantics
// are identical to ListByReviewee.
//
// Authorization is a fresh-load check: the reviewee's current manager_id
// must equal the caller's ID. An employee who is not the reviewee's manager
// — including the reviewee themselves — gets apperror.ErrForbidden (403)
// rather than a 404, because the reviewee's feedback existence is not a
// secret from their manager. An unknown reviewee ID yields 404 via
// apperror.ErrEmployeeNotFound. When the service was constructed without an
// EmployeeLookup the call fails closed with ErrForbidden: it cannot be
// authorized, so it must not succeed.
//
// Visibility policy: the manager never sees who wrote an entry. The handler
// (via the DTO layer) blanks reviewer_id on every entry — including named
// ones — while preserving comments in full. This is enforced in
// dto.ToFeedbackManagerListResponse, not here.
func (s *FeedbackService) ListByRevieweeForManager(ctx context.Context, callerID, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(callerID) == "" {
		s.logger.Warn("manager feedback list rejected: missing caller_id", "reviewee_id", revieweeID)
		return nil, "", apperror.ErrInvalidFeedback("caller_id is required")
	}
	if strings.TrimSpace(revieweeID) == "" {
		s.logger.Warn("manager feedback list rejected: missing reviewee_id", "caller_id", callerID)
		return nil, "", apperror.ErrInvalidFeedback("reviewee_id is required")
	}
	if s.employeeLookup == nil {
		// Fail closed: without an employee lookup the caller's right to see
		// this reviewee's feedback cannot be established.
		s.logger.Error("manager feedback list rejected: no employee lookup configured (miswired service?)",
			"caller_id", callerID, "reviewee_id", revieweeID)
		return nil, "", apperror.ErrForbidden
	}

	reviewee, err := s.employeeLookup.GetByID(ctx, revieweeID)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			s.logger.Warn("manager feedback list rejected: reviewee not found",
				"caller_id", callerID, "reviewee_id", revieweeID)
			return nil, "", err
		}
		s.logger.Error("manager feedback list aborted: reviewee lookup failed",
			"error", err, "reviewee_id", revieweeID)
		return nil, "", err
	}
	if reviewee.ManagerID == nil || *reviewee.ManagerID != callerID {
		s.logger.Warn("manager feedback list rejected: caller is not the reviewee's manager",
			"caller_id", callerID, "reviewee_id", revieweeID)
		return nil, "", apperror.ErrForbidden
	}

	return s.ListByReviewee(ctx, revieweeID, limit, cursorID)
}

// DefaultDraftListLimit and MaxDraftListLimit bound the page size of the
// caller's draft listing, mirroring the feedback list constants so all list
// endpoints share one page-size contract.
const (
	DefaultDraftListLimit = 20
	MaxDraftListLimit     = 100
)

// CreateDraft creates a new draft feedback entry owned by the reviewer. A
// draft is the reviewer's private work-in-progress: it requires only the
// reviewee and period, and any scores or comments already written are
// bounds-checked but not required. At most one draft may exist per
// (reviewer, reviewee, period) triple; a duplicate yields
// apperror.ErrFeedbackDraftAlreadyExists. The reviewer cannot draft a
// self-review, and the period must exist — but, unlike submission, the
// period's date window is not enforced here: a draft may be started before
// the period opens and will fail at submit time if the window is still shut.
func (s *FeedbackService) CreateDraft(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error) {
	if feedback == nil {
		s.logger.Warn("draft create rejected: nil feedback", "reviewer_id", reviewerID)
		return nil, apperror.ErrInvalidFeedback("feedback must not be nil")
	}
	if strings.TrimSpace(reviewerID) == "" {
		s.logger.Warn("draft create rejected: missing reviewer_id", "period_id", feedback.PeriodID, "reviewee_id", feedback.RevieweeID)
		return nil, apperror.ErrInvalidFeedback("reviewer_id is required")
	}
	if strings.TrimSpace(feedback.PeriodID) == "" {
		s.logger.Warn("draft create rejected: missing period_id", "reviewer_id", reviewerID, "reviewee_id", feedback.RevieweeID)
		return nil, apperror.ErrInvalidFeedback("period_id is required")
	}
	if _, err := s.periodFor(ctx, reviewerID, feedback); err != nil {
		return nil, err
	}
	if strings.TrimSpace(feedback.RevieweeID) == "" {
		s.logger.Warn("draft create rejected: missing reviewee_id", "reviewer_id", reviewerID, "period_id", feedback.PeriodID)
		return nil, apperror.ErrInvalidFeedback("reviewee_id is required")
	}
	if feedback.RevieweeID == reviewerID {
		s.logger.Warn("draft create rejected: self-review", "reviewer_id", reviewerID, "period_id", feedback.PeriodID)
		return nil, apperror.ErrInvalidFeedback("reviewer cannot review themselves")
	}
	// Supplied scores are bounds-checked so a draft cannot park an invalid
	// value, but are not required: an unwritten score stays at the zero value
	// until submit-time validation demands it.
	for field, score := range map[string]int{
		"communication_score": feedback.CommunicationScore,
		"leadership_score":    feedback.LeadershipScore,
		"technical_score":     feedback.TechnicalScore,
		"collaboration_score": feedback.CollaborationScore,
		"delivery_score":      feedback.DeliveryScore,
		"trust_score":         feedback.TrustScore,
	} {
		if score != 0 {
			if err := validateScore(field, score); err != nil {
				s.logger.Warn("draft create rejected: invalid "+field,
					"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "score", score)
				return nil, err
			}
		}
	}
	if feedback.Visibility != "" && !validVisibility(feedback.Visibility) {
		s.logger.Warn("draft create rejected: invalid visibility",
			"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "visibility", string(feedback.Visibility))
		return nil, apperror.ErrInvalidFeedback("visibility must be one of anonymous, named")
	}
	feedback.Visibility = normalizeVisibility(feedback.Visibility)

	feedback.Status = model.FeedbackStatusDraft
	feedback.ReviewerID = reviewerID
	id, err := uuid.NewV7()
	if err != nil {
		s.logger.Error("draft create aborted: failed to generate UUID", "error", err, "reviewer_id", reviewerID)
		return nil, err
	}
	feedback.ID = id.String()

	created, err := s.feedbackRepo.CreateDraft(ctx, feedback)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackDraftAlreadyExists) {
			s.logger.Warn("draft create rejected: draft already exists",
				"reviewer_id", reviewerID, "period_id", feedback.PeriodID, "reviewee_id", feedback.RevieweeID)
			return nil, err
		}
		s.logger.Error("draft create aborted: repository create failed",
			"error", err, "feedback_id", feedback.ID, "reviewer_id", reviewerID, "reviewee_id", feedback.RevieweeID, "period_id", feedback.PeriodID)
		return nil, err
	}
	return created, nil
}

// GetDraft returns the caller's own draft with the given ID. Any other
// caller — including the reviewee or a manager — gets
// apperror.ErrFeedbackNotFound rather than a 403: a draft's existence is
// private to its author, and revealing it exists (but is forbidden) would
// leak that feedback is being written about them.
func (s *FeedbackService) GetDraft(ctx context.Context, reviewerID, draftID string) (*model.Feedback, error) {
	draft, err := s.feedbackRepo.Get(ctx, draftID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			s.logger.Warn("draft get rejected: not found", "reviewer_id", reviewerID, "draft_id", draftID)
			return nil, err
		}
		s.logger.Error("draft get aborted: repository get failed", "error", err, "draft_id", draftID)
		return nil, err
	}
	if draft.ReviewerID != reviewerID || draft.NormalizedStatus() != model.FeedbackStatusDraft {
		// Not the author's draft, or not a draft at all: same answer as
		// missing, so nothing about other employees' entries leaks.
		s.logger.Warn("draft get rejected: not the caller's draft", "reviewer_id", reviewerID, "draft_id", draftID)
		return nil, apperror.ErrFeedbackNotFound
	}
	return draft, nil
}

// UpdateDraft applies a partial update to the caller's draft. Only fields the
// request explicitly supplied are overwritten (the handler resolves them onto
// a freshly loaded draft); period_id, reviewee_id, and reviewer_id are fixed
// once the draft exists. Supplied scores are bounds-checked but may be
// omitted; comments may be cleared. The draft's period existence is not
// re-checked (it was checked at draft creation), and the period's date
// window is not enforced on save — only on submit.
//
// Concurrency: the repository rejects the write with
// ErrFeedbackConcurrentUpdate when the draft changed since the caller read
// it; the service surfaces it unchanged for the handler to map to 409.
func (s *FeedbackService) UpdateDraft(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error) {
	draft, err := s.GetDraft(ctx, reviewerID, draftID)
	if err != nil {
		return nil, err
	}
	if apply != nil {
		if err := apply(draft); err != nil {
			return nil, err
		}
	}
	// Re-validate the merged result: a supplied score must be within range
	// (zero is allowed at this stage — it means "not filled in yet") and the
	// visibility, if set, must be a known value.
	for field, score := range map[string]int{
		"communication_score": draft.CommunicationScore,
		"leadership_score":    draft.LeadershipScore,
		"technical_score":     draft.TechnicalScore,
		"collaboration_score": draft.CollaborationScore,
		"delivery_score":      draft.DeliveryScore,
		"trust_score":         draft.TrustScore,
	} {
		if score != 0 {
			if err := validateScore(field, score); err != nil {
				s.logger.Warn("draft update rejected: invalid "+field,
					"reviewer_id", reviewerID, "draft_id", draftID, "score", score)
				return nil, err
			}
		}
	}
	if draft.Visibility != "" && !validVisibility(draft.Visibility) {
		s.logger.Warn("draft update rejected: invalid visibility",
			"reviewer_id", reviewerID, "draft_id", draftID, "visibility", string(draft.Visibility))
		return nil, apperror.ErrInvalidFeedback("visibility must be one of anonymous, named")
	}
	draft.Visibility = normalizeVisibility(draft.Visibility)

	updated, err := s.feedbackRepo.UpdateDraft(ctx, draft)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			s.logger.Warn("draft update rejected: no longer a draft", "reviewer_id", reviewerID, "draft_id", draftID)
			return nil, err
		}
		// ErrFeedbackConcurrentUpdate and Firestore failures propagate for
		// the handler to map (409 / 500 respectively).
		s.logger.Error("draft update aborted: repository update failed",
			"error", err, "reviewer_id", reviewerID, "draft_id", draftID)
		return nil, err
	}
	return updated, nil
}

// SubmitDraft submits the caller's draft: it applies any final edits, runs
// the full submit-time validation (every score present and in range, both
// comments non-empty), enforces the period's date window (now must fall
// within [start_date, end_date]), transitions the entry to submitted, and
// releases the (reviewer, reviewee, period) claim so a new draft may be
// started immediately.
//
// The entry returns to the reviewer with the visibility policy intact: the
// reviewer is allowed to know they wrote it even when it is anonymous.
func (s *FeedbackService) SubmitDraft(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error) {
	draft, err := s.GetDraft(ctx, reviewerID, draftID)
	if err != nil {
		return nil, err
	}
	if apply != nil {
		if err := apply(draft); err != nil {
			return nil, err
		}
	}

	period, err := s.periodFor(ctx, reviewerID, draft)
	if err != nil {
		return nil, err
	}
	if err := validatePeriodOpen(period); err != nil {
		s.logger.Warn("draft submit rejected: period not open",
			"reviewer_id", reviewerID, "draft_id", draftID, "period_id", draft.PeriodID)
		return nil, err
	}

	// Full submit-time validation: everything required of a directly-created
	// feedback entry is required of a submitted draft.
	if err := validateScore("communication_score", draft.CommunicationScore); err != nil {
		s.logger.Warn("draft submit rejected: invalid communication_score", "reviewer_id", reviewerID, "draft_id", draftID, "score", draft.CommunicationScore)
		return nil, err
	}
	if err := validateScore("leadership_score", draft.LeadershipScore); err != nil {
		s.logger.Warn("draft submit rejected: invalid leadership_score", "reviewer_id", reviewerID, "draft_id", draftID, "score", draft.LeadershipScore)
		return nil, err
	}
	if err := validateScore("technical_score", draft.TechnicalScore); err != nil {
		s.logger.Warn("draft submit rejected: invalid technical_score", "reviewer_id", reviewerID, "draft_id", draftID, "score", draft.TechnicalScore)
		return nil, err
	}
	if err := validateScore("collaboration_score", draft.CollaborationScore); err != nil {
		s.logger.Warn("draft submit rejected: invalid collaboration_score", "reviewer_id", reviewerID, "draft_id", draftID, "score", draft.CollaborationScore)
		return nil, err
	}
	if err := validateScore("delivery_score", draft.DeliveryScore); err != nil {
		s.logger.Warn("draft submit rejected: invalid delivery_score", "reviewer_id", reviewerID, "draft_id", draftID, "score", draft.DeliveryScore)
		return nil, err
	}
	if err := validateScore("trust_score", draft.TrustScore); err != nil {
		s.logger.Warn("draft submit rejected: invalid trust_score", "reviewer_id", reviewerID, "draft_id", draftID, "score", draft.TrustScore)
		return nil, err
	}
	if strings.TrimSpace(draft.StrengthsComment) == "" {
		s.logger.Warn("draft submit rejected: missing strengths_comment", "reviewer_id", reviewerID, "draft_id", draftID)
		return nil, apperror.ErrInvalidFeedback("strengths_comment is required")
	}
	if strings.TrimSpace(draft.WeaknessesComment) == "" {
		s.logger.Warn("draft submit rejected: missing weaknesses_comment", "reviewer_id", reviewerID, "draft_id", draftID)
		return nil, apperror.ErrInvalidFeedback("weaknesses_comment is required")
	}
	if draft.Visibility != "" && !validVisibility(draft.Visibility) {
		s.logger.Warn("draft submit rejected: invalid visibility",
			"reviewer_id", reviewerID, "draft_id", draftID, "visibility", string(draft.Visibility))
		return nil, apperror.ErrInvalidFeedback("visibility must be one of anonymous, named")
	}
	draft.Visibility = normalizeVisibility(draft.Visibility)

	// Persist the final edits first, then transition. The update keeps the
	// optimistic-concurrency guard; the submit re-reads and is idempotent-
	// failing (ErrFeedbackNotFound once the entry is no longer a draft).
	if _, err := s.feedbackRepo.UpdateDraft(ctx, draft); err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			s.logger.Warn("draft submit rejected: no longer a draft", "reviewer_id", reviewerID, "draft_id", draftID)
			return nil, err
		}
		s.logger.Error("draft submit aborted: repository update failed",
			"error", err, "reviewer_id", reviewerID, "draft_id", draftID)
		return nil, err
	}

	submitted, err := s.feedbackRepo.SubmitDraft(ctx, draftID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			s.logger.Warn("draft submit rejected: no longer a draft", "reviewer_id", reviewerID, "draft_id", draftID)
			return nil, err
		}
		s.logger.Error("draft submit aborted: repository submit failed",
			"error", err, "reviewer_id", reviewerID, "draft_id", draftID)
		return nil, err
	}

	// The submitted feedback may fulfill an open request from the reviewee to
	// the reviewer in this period; complete it best-effort.
	s.closeMatchingRequests(ctx, reviewerID, submitted.RevieweeID, submitted.PeriodID)

	return submitted, nil
}

// DeleteDraft removes the caller's draft and releases its uniqueness claim.
// Deleting a submitted entry is not possible via this method: the entry is
// looked up and confirmed to still be a draft first, yielding
// apperror.ErrFeedbackNotFound otherwise. Submitted feedback is immutable,
// matching the create endpoint's write-once semantics.
func (s *FeedbackService) DeleteDraft(ctx context.Context, reviewerID, draftID string) error {
	if _, err := s.GetDraft(ctx, reviewerID, draftID); err != nil {
		return err
	}
	if err := s.feedbackRepo.DeleteDraft(ctx, draftID); err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			s.logger.Warn("draft delete rejected: no longer a draft", "reviewer_id", reviewerID, "draft_id", draftID)
			return err
		}
		s.logger.Error("draft delete aborted: repository delete failed",
			"error", err, "reviewer_id", reviewerID, "draft_id", draftID)
		return err
	}
	return nil
}

// ListMyDrafts returns one page of the caller's draft feedback entries,
// ordered by created_at descending (newest first), plus the ID of the last
// entry on the page for the next page's cursor. limit and cursorID semantics
// mirror ListByReviewee (default 20, cap 100, unknown cursor yields
// ErrFeedbackNotFound for the handler to map to a 400).
func (s *FeedbackService) ListMyDrafts(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(reviewerID) == "" {
		s.logger.Warn("draft list rejected: missing reviewer_id")
		return nil, "", apperror.ErrInvalidFeedback("reviewer_id is required")
	}
	if limit <= 0 {
		limit = DefaultDraftListLimit
	}
	if limit > MaxDraftListLimit {
		limit = MaxDraftListLimit
	}

	drafts, nextCursorID, err := s.feedbackRepo.ListDraftsByReviewer(ctx, reviewerID, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			// An unknown cursor is a caller error, not a service failure.
			return nil, "", err
		}
		s.logger.Error("draft list failed", "error", err, "reviewer_id", reviewerID, "limit", limit, "cursor_id", cursorID)
		return nil, "", err
	}
	return drafts, nextCursorID, nil
}

// ListMyGivenFeedbacks returns one page of feedback entries the caller has
// submitted (i.e. entries they wrote as the reviewer), ordered by created_at
// descending (newest first), plus the ID of the last entry on the page for
// the next page's cursor. Only submitted entries are listed; drafts stay in
// ListMyDrafts. limit and cursorID semantics mirror ListByReviewee (default
// 20, cap 100, unknown cursor yields ErrFeedbackNotFound for the handler to
// map to a 400).
func (s *FeedbackService) ListMyGivenFeedbacks(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(reviewerID) == "" {
		s.logger.Warn("given feedback list rejected: missing reviewer_id")
		return nil, "", apperror.ErrInvalidFeedback("reviewer_id is required")
	}
	if limit <= 0 {
		limit = DefaultFeedbackListLimit
	}
	if limit > MaxFeedbackListLimit {
		limit = MaxFeedbackListLimit
	}

	feedbacks, nextCursorID, err := s.feedbackRepo.ListSubmittedByReviewer(ctx, reviewerID, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			// An unknown cursor is a caller error, not a service failure.
			return nil, "", err
		}
		s.logger.Error("given feedback list failed", "error", err, "reviewer_id", reviewerID, "limit", limit, "cursor_id", cursorID)
		return nil, "", err
	}
	return feedbacks, nextCursorID, nil
}
