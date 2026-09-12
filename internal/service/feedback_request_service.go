package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
)

// FeedbackRequestRepository is the consumer-defined contract for the
// feedback-request repository. It is intentionally minimal: only the
// operations the service actually needs. The concrete repository
// implementation satisfies it implicitly.
type FeedbackRequestRepository interface {
	Create(ctx context.Context, request *model.FeedbackRequest) (*model.FeedbackRequest, error)
	Get(ctx context.Context, id string) (*model.FeedbackRequest, error)
	SetStatus(ctx context.Context, id string, to model.FeedbackRequestStatus) (*model.FeedbackRequest, error)
	CloseMatchingOpen(ctx context.Context, requesterID, requesteeID, periodID string) (*model.FeedbackRequest, error)
	ListByRequestee(ctx context.Context, requesteeID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error)
	ListByRequester(ctx context.Context, requesterID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error)
}

// DefaultFeedbackRequestListLimit and MaxFeedbackRequestListLimit bound the
// page size of the request listings, mirroring the feedback list constants
// so all list endpoints share one page-size contract.
const (
	DefaultFeedbackRequestListLimit = 20
	MaxFeedbackRequestListLimit     = 100
)

// FeedbackRequestService is the application layer that orchestrates
// feedback-request operations against a FeedbackRequestRepository. It
// validates that a request's period_id refers to an existing feedback period
// via the injected FeedbackPeriodLookup and that the requestee is a
// colleague of the caller via the injected EmployeeLookup before persisting.
type FeedbackRequestService struct {
	feedbackRequestRepo FeedbackRequestRepository
	periodLookup        FeedbackPeriodLookup
	employeeLookup      EmployeeLookup
	mailer              Mailer
	logger              *slog.Logger
}

// NewFeedbackRequestService creates a FeedbackRequestService backed by the
// given repository, period lookup, employee lookup, and mailer. If logger is
// nil, slog.Default() is used. mailer may be nil to disable the notification
// email (useful in tests); periodLookup and employeeLookup may likewise be nil
// in tests, but Create fails closed then, since the request's period and
// requestee cannot be validated.
func NewFeedbackRequestService(feedbackRequestRepo FeedbackRequestRepository, periodLookup FeedbackPeriodLookup, employeeLookup EmployeeLookup, mailer Mailer, logger *slog.Logger) *FeedbackRequestService {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackRequestService{feedbackRequestRepo: feedbackRequestRepo, periodLookup: periodLookup, employeeLookup: employeeLookup, mailer: mailer, logger: logger}
}

// Create records a request from the authenticated employee (the requester)
// asking the requestee for feedback within a feedback period. Validations:
//   - requester must differ from the requestee (no self-requests)
//   - period_id must refer to an existing feedback period (the period's date
//     window is intentionally NOT enforced: a request may be made before the
//     period opens, mirroring draft semantics)
//   - requestee must exist and belong to the same organization
//
// At most one OPEN request may exist per (requester, requestee, period)
// triple; a duplicate yields apperror.ErrFeedbackRequestAlreadyExists. After
// creating the request, a notification email is sent to the requestee on a
// best-effort basis: a mailer failure is logged but does not fail the create.
func (s *FeedbackRequestService) Create(ctx context.Context, requesterID string, request *model.FeedbackRequest) (*model.FeedbackRequest, error) {
	if request == nil {
		s.logger.Warn("feedback request create rejected: nil request", "requester_id", requesterID)
		return nil, apperror.ErrInvalidFeedbackRequest("request must not be nil")
	}
	if strings.TrimSpace(requesterID) == "" {
		s.logger.Warn("feedback request create rejected: missing requester_id")
		return nil, apperror.ErrInvalidFeedbackRequest("requester_id is required")
	}

	requester, err := s.employeeLookup.GetByID(ctx, requesterID)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			s.logger.Warn("feedback request create rejected: requester not found", "requester_id", requesterID)
			return nil, apperror.ErrInvalidFeedbackRequest("requester_id does not refer to an existing employee")
		}
		s.logger.Error("feedback request create aborted: requester lookup failed",
			"error", err, "requester_id", requesterID)
		return nil, err
	}

	if strings.TrimSpace(request.PeriodID) == "" {
		s.logger.Warn("feedback request create rejected: missing period_id", "requester_id", requesterID)
		return nil, apperror.ErrInvalidFeedbackRequest("period_id is required")
	}
	period, err := s.periodLookup.GetByID(ctx, request.PeriodID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackPeriodNotFound) {
			s.logger.Warn("feedback request create rejected: period not found",
				"requester_id", requesterID, "period_id", request.PeriodID)
			return nil, apperror.ErrInvalidFeedbackRequest("period_id does not refer to an existing feedback period")
		}
		s.logger.Error("feedback request create aborted: period lookup failed",
			"error", err, "requester_id", requesterID, "period_id", request.PeriodID)
		return nil, err
	}

	if strings.TrimSpace(request.RequesteeID) == "" {
		s.logger.Warn("feedback request create rejected: missing requestee_id", "requester_id", requesterID)
		return nil, apperror.ErrInvalidFeedbackRequest("requestee_id is required")
	}
	if request.RequesteeID == requesterID {
		s.logger.Warn("feedback request create rejected: self-request", "requester_id", requesterID, "period_id", request.PeriodID)
		return nil, apperror.ErrInvalidFeedbackRequest("requester cannot ask themselves for feedback")
	}
	requestee, err := s.employeeLookup.GetByID(ctx, request.RequesteeID)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			s.logger.Warn("feedback request create rejected: requestee not found",
				"requester_id", requesterID, "requestee_id", request.RequesteeID)
			return nil, apperror.ErrInvalidFeedbackRequest("requestee_id does not refer to an existing employee")
		}
		s.logger.Error("feedback request create aborted: requestee lookup failed",
			"error", err, "requester_id", requesterID, "requestee_id", request.RequesteeID)
		return nil, err
	}
	if requestee.OrganizationName != requester.OrganizationName {
		s.logger.Warn("feedback request create rejected: requestee in another organization",
			"requester_id", requesterID, "requestee_id", request.RequesteeID)
		return nil, apperror.ErrInvalidFeedbackRequest("requestee must belong to your organization")
	}

	request.RequesterID = requesterID
	request.Status = model.FeedbackRequestStatusOpen
	id, err := uuid.NewV7()
	if err != nil {
		s.logger.Error("feedback request create aborted: failed to generate UUID", "error", err, "requester_id", requesterID)
		return nil, err
	}
	request.ID = id.String()

	created, err := s.feedbackRequestRepo.Create(ctx, request)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackRequestAlreadyExists) {
			s.logger.Warn("feedback request create rejected: request already exists",
				"requester_id", requesterID, "period_id", request.PeriodID, "requestee_id", request.RequesteeID)
			return nil, err
		}
		s.logger.Error("feedback request create aborted: repository create failed",
			"error", err, "request_id", request.ID, "requester_id", requesterID, "requestee_id", request.RequesteeID, "period_id", request.PeriodID)
		return nil, err
	}

	// Best-effort notification: a mailer failure is logged but never blocks
	// the create (the request itself is already durable).
	if s.mailer != nil {
		if err := s.mailer.SendFeedbackRequestEmail(ctx, requestee.Email, requester.Name, period.Name); err != nil {
			s.logger.Error("failed to send feedback request email",
				"error", err, "request_id", created.ID, "requestee_id", requestee.ID)
		}
	}

	return created, nil
}

// Decline transitions an open request to declined. Only the requestee may
// decline; anyone else — including the requester — gets
// apperror.ErrFeedbackRequestNotFound, so a request's existence never leaks
// to other employees. The (requester, requestee, period) slot is released, so
// the requester may ask again.
func (s *FeedbackRequestService) Decline(ctx context.Context, callerID, requestID string) (*model.FeedbackRequest, error) {
	if strings.TrimSpace(callerID) == "" {
		s.logger.Warn("feedback request decline rejected: missing caller_id")
		return nil, apperror.ErrInvalidFeedbackRequest("caller_id is required")
	}
	if strings.TrimSpace(requestID) == "" {
		s.logger.Warn("feedback request decline rejected: missing request_id", "caller_id", callerID)
		return nil, apperror.ErrInvalidFeedbackRequest("request_id is required")
	}

	request, err := s.feedbackRequestRepo.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if request.RequesteeID != callerID {
		// Only the requestee may decline. A request belonging to anyone else
		// is indistinguishable from one that does not exist, so its existence
		// never leaks.
		s.logger.Warn("feedback request decline rejected: caller is not the requestee",
			"caller_id", callerID, "request_id", requestID)
		return nil, apperror.ErrFeedbackRequestNotFound
	}

	declined, err := s.feedbackRequestRepo.SetStatus(ctx, requestID, model.FeedbackRequestStatusDeclined)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
			// The request was completed or declined concurrently.
			s.logger.Warn("feedback request decline rejected: no longer open",
				"caller_id", callerID, "request_id", requestID)
			return nil, err
		}
		s.logger.Error("feedback request decline aborted: repository update failed",
			"error", err, "request_id", requestID, "caller_id", callerID)
		return nil, err
	}
	return declined, nil
}

// List returns one page of the caller's requests, ordered by created_at
// descending (newest first), plus the ID of the last request on the page for
// the next page's cursor. direction selects the side: "received" lists
// requests where the caller is the requestee; "sent" lists requests where the
// caller is the requester. Any other value defaults to "received". limit and
// cursor semantics mirror the feedback lists (default 20, cap 100, unknown
// cursor yields ErrFeedbackRequestNotFound for the handler to map to a 400).
func (s *FeedbackRequestService) List(ctx context.Context, callerID, direction string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	if strings.TrimSpace(callerID) == "" {
		s.logger.Warn("feedback request list rejected: missing caller_id")
		return nil, "", apperror.ErrInvalidFeedbackRequest("caller_id is required")
	}
	if limit <= 0 {
		limit = DefaultFeedbackRequestListLimit
	}
	if limit > MaxFeedbackRequestListLimit {
		limit = MaxFeedbackRequestListLimit
	}

	if direction == "sent" {
		requests, nextCursorID, err := s.feedbackRequestRepo.ListByRequester(ctx, callerID, limit, cursorID)
		if err != nil {
			if errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
				// An unknown cursor is a caller error, not a service failure.
				return nil, "", err
			}
			s.logger.Error("sent request list failed", "error", err, "caller_id", callerID, "limit", limit, "cursor_id", cursorID)
			return nil, "", err
		}
		return requests, nextCursorID, nil
	}

	requests, nextCursorID, err := s.feedbackRequestRepo.ListByRequestee(ctx, callerID, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
			// An unknown cursor is a caller error, not a service failure.
			return nil, "", err
		}
		s.logger.Error("received request list failed", "error", err, "caller_id", callerID, "limit", limit, "cursor_id", cursorID)
		return nil, "", err
	}
	return requests, nextCursorID, nil
}

// CloseMatchingOpen marks the open request matching the
// (requester, requestee, period) triple — if any — as completed. It is the
// FeedbackRequestCloser consumed by the feedback service: when the requestee
// submits feedback for the requester in that period, this flips the
// matching request to completed and releases its slot. A missing match is
// returned as apperror.ErrFeedbackRequestNotFound, which the feedback service
// treats as "nothing to complete" (logged, never an error the submitter sees).
func (s *FeedbackRequestService) CloseMatchingOpen(ctx context.Context, requesterID, requesteeID, periodID string) error {
	_, err := s.feedbackRequestRepo.CloseMatchingOpen(ctx, requesterID, requesteeID, periodID)
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
			// No open request matched: the common case, not a failure.
			return nil
		}
		s.logger.Error("feedback request completion failed",
			"error", err, "requester_id", requesterID, "requestee_id", requesteeID, "period_id", periodID)
		return err
	}
	s.logger.Info("feedback request completed",
		"requester_id", requesterID, "requestee_id", requesteeID, "period_id", periodID)
	return nil
}
