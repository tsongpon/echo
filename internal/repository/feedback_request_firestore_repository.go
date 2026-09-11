package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
	"github.com/tsongpon/echo/internal/service"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"log/slog"
)

// Compile-time assertion that FeedbackRequestFirestoreRepository satisfies the
// service.FeedbackRequestRepository interface.
var _ service.FeedbackRequestRepository = (*FeedbackRequestFirestoreRepository)(nil)

// FeedbackRequestCollection is the Firestore collection that holds feedback
// requests.
const FeedbackRequestCollection = "feedback_requests"

// FeedbackRequestClaimCollection is a dedicated collection that enforces at
// most one OPEN request per (requester, requestee, period) triple. Each
// document is keyed by "<requesterID>_<requesteeID>_<periodID>" and points at
// the request that claimed it. Firestore only enforces uniqueness on document
// IDs, not on fields, so the claim document acts as a unique-constraint ledger
// in the same spirit as the draft claim collection. The claim is released
// (deleted) when the request is completed or declined, after which the
// requester may ask again for the same pair.
const FeedbackRequestClaimCollection = "feedback_request_claims"

// feedbackRequestClaimDoc returns the document reference for the request
// claim keyed by the (requester, requestee, period) triple. The IDs are UUIDs,
// so joining them with "_" is collision-free.
func feedbackRequestClaimDoc(client *firestore.Client, requesterID, requesteeID, periodID string) *firestore.DocumentRef {
	return client.Collection(FeedbackRequestClaimCollection).Doc(requesterID + "_" + requesteeID + "_" + periodID)
}

// FeedbackRequestFirestoreRepository is a service.FeedbackRequestRepository
// backed by GCP Firestore. Each request is one document in the
// FeedbackRequestCollection keyed by the request ID, so lookups by ID are a
// direct document read rather than a query.
type FeedbackRequestFirestoreRepository struct {
	client *firestore.Client
	logger *slog.Logger
}

// NewFeedbackRequestFirestoreRepository creates a repository over the given
// Firestore client. The client is owned by the caller: closing it is the
// caller's responsibility. The logger defaults to slog.Default() when nil.
func NewFeedbackRequestFirestoreRepository(client *firestore.Client, logger *slog.Logger) *FeedbackRequestFirestoreRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackRequestFirestoreRepository{client: client, logger: logger}
}

// logError records a failed Firestore call. See
// EmployeeFirestoreRepository.logError for the rationale behind logging the
// gRPC status code as its own field.
func (r *FeedbackRequestFirestoreRepository) logError(msg string, err error, args ...any) {
	r.logger.Error(msg, append([]any{"error", err, "code", status.Code(err).String()}, args...)...)
}

// feedbackRequestDocument is the Firestore representation of
// model.FeedbackRequest. It is kept separate from the domain model so the
// stored field names are an explicit, stable contract rather than a by-product
// of Go field naming. The request ID is the document ID and is deliberately
// not duplicated as a field.
type feedbackRequestDocument struct {
	RequesterID string                      `firestore:"requester_id"`
	RequesteeID string                      `firestore:"requestee_id"`
	PeriodID    string                      `firestore:"period_id"`
	Status      model.FeedbackRequestStatus `firestore:"status"`
	CreatedAt   time.Time                   `firestore:"created_at"`
	UpdatedAt   time.Time                   `firestore:"updated_at"`
}

// newFeedbackRequestDocument projects a domain request onto its stored form.
func newFeedbackRequestDocument(request *model.FeedbackRequest) *feedbackRequestDocument {
	return &feedbackRequestDocument{
		RequesterID: request.RequesterID,
		RequesteeID: request.RequesteeID,
		PeriodID:    request.PeriodID,
		Status:      request.Status,
		CreatedAt:   request.CreatedAt,
		UpdatedAt:   request.UpdatedAt,
	}
}

// toFeedbackRequest decodes a document snapshot into a domain feedback
// request, taking the ID from the document key.
func (r *FeedbackRequestFirestoreRepository) toFeedbackRequest(snapshot *firestore.DocumentSnapshot) (*model.FeedbackRequest, error) {
	var doc feedbackRequestDocument
	if err := snapshot.DataTo(&doc); err != nil {
		// A stored document that no longer fits the struct: a schema change or
		// a record written by something other than this repository. Logged
		// with the document ID because the bad record has to be found by hand.
		r.logError("firestore: decode feedback request document failed", err, "request_id", snapshot.Ref.ID)
		return nil, fmt.Errorf("firestore: decode feedback request %s: %w", snapshot.Ref.ID, err)
	}

	return &model.FeedbackRequest{
		ID:          snapshot.Ref.ID,
		RequesterID: doc.RequesterID,
		RequesteeID: doc.RequesteeID,
		PeriodID:    doc.PeriodID,
		Status:      doc.Status,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}, nil
}

// storeTime is shared with the other repositories: it normalizes a timestamp
// to what Firestore will hand back on the next read (UTC, microsecond
// precision), so equality checks against read-back values do not spuriously
// fail. (See employee_firestore_repository.go for the rationale.)

// Create stores the given feedback request as a new document keyed by its ID
// and claims the (requester, requestee, period) open-request slot, both inside
// one transaction so the uniqueness rule is enforced atomically: reading the
// claim inside the transaction adds it to the read set, so concurrent request
// creations for the same pair are serialized by Firestore's per-document
// conflict detection and the loser observes
// apperror.ErrFeedbackRequestAlreadyExists on retry. See the draft claim
// Create for the pattern's rationale.
//
// CreatedAt and UpdatedAt are set here, overriding the caller's values. The
// stored status is always open: this method must not be used to write a
// completed or declined request.
func (r *FeedbackRequestFirestoreRepository) Create(ctx context.Context, request *model.FeedbackRequest) (*model.FeedbackRequest, error) {
	if request == nil {
		return nil, ErrNilFeedbackRequest
	}
	if strings.TrimSpace(request.ID) == "" {
		return nil, ErrNilFeedbackRequestID
	}

	stored := *request
	stored.Status = model.FeedbackRequestStatusOpen
	now := storeTime(time.Now())
	stored.CreatedAt = now
	stored.UpdatedAt = now

	claimRef := feedbackRequestClaimDoc(r.client, stored.RequesterID, stored.RequesteeID, stored.PeriodID)
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(claimRef); err == nil {
			return apperror.ErrFeedbackRequestAlreadyExists
		} else if status.Code(err) != codes.NotFound {
			return fmt.Errorf("firestore: read request claim: %w", err)
		}
		if err := tx.Create(claimRef, map[string]any{
			"requester_id": stored.RequesterID,
			"requestee_id": stored.RequesteeID,
			"period_id":    stored.PeriodID,
			"request_id":   stored.ID,
			"created_at":   stored.CreatedAt,
		}); err != nil {
			return fmt.Errorf("firestore: claim request: %w", err)
		}
		if err := tx.Create(r.client.Collection(FeedbackRequestCollection).Doc(stored.ID), newFeedbackRequestDocument(&stored)); err != nil {
			return fmt.Errorf("firestore: create request: %w", err)
		}
		return nil
	})
	if err != nil {
		// Logged out here rather than inside the transaction body: Firestore
		// re-runs that body on contention, so logging in it would emit a line
		// per attempt for what is ultimately one failure.
		if errors.Is(err, apperror.ErrFeedbackRequestAlreadyExists) {
			// A duplicate request is a caller error, not a Firestore failure.
			return nil, err
		}
		r.logError("firestore: create request failed", err, "request_id", stored.ID, "requester_id", stored.RequesterID, "requestee_id", stored.RequesteeID, "period_id", stored.PeriodID)
		return nil, err
	}

	return &stored, nil
}

// Get returns the request with the given ID, or
// apperror.ErrFeedbackRequestNotFound when no such document exists. It does
// not enforce ownership: callers (the service) decide who may see a request.
func (r *FeedbackRequestFirestoreRepository) Get(ctx context.Context, id string) (*model.FeedbackRequest, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrNilFeedbackRequestID
	}

	snapshot, err := r.client.Collection(FeedbackRequestCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, apperror.ErrFeedbackRequestNotFound
	}
	if err != nil {
		r.logError("firestore: get request by ID failed", err, "request_id", id)
		return nil, fmt.Errorf("firestore: get request %s: %w", id, err)
	}
	return r.toFeedbackRequest(snapshot)
}

// SetStatus transitions a request to the given status and — when the new
// status is completed or declined — releases the (requester, requestee,
// period) open-request claim inside the same transaction, so the requester
// may immediately ask again for the same pair.
//
// The write is guarded by a re-read: the stored status must currently be open
// (completing or declining a missing or already-closed request yields
// apperror.ErrFeedbackRequestNotFound), so concurrent transitions cannot
// double-fire. UpdatedAt is refreshed to mark the transition time.
func (r *FeedbackRequestFirestoreRepository) SetStatus(ctx context.Context, id string, to model.FeedbackRequestStatus) (*model.FeedbackRequest, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrNilFeedbackRequestID
	}
	if to != model.FeedbackRequestStatusCompleted && to != model.FeedbackRequestStatusDeclined {
		return nil, fmt.Errorf("firestore: invalid target status %q", to)
	}

	doc := r.client.Collection(FeedbackRequestCollection).Doc(id)
	var updated *model.FeedbackRequest

	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(doc)
		if status.Code(err) == codes.NotFound {
			return apperror.ErrFeedbackRequestNotFound
		}
		if err != nil {
			return fmt.Errorf("firestore: read request for status change: %w", err)
		}

		current, err := r.toFeedbackRequest(snapshot)
		if err != nil {
			return err
		}
		if current.Status != model.FeedbackRequestStatusOpen {
			// The request was completed or declined between the caller's read
			// and this write. Treat it as gone: the caller fetched something
			// that is no longer open.
			return apperror.ErrFeedbackRequestNotFound
		}

		current.Status = to
		current.UpdatedAt = storeTime(time.Now())
		if err := tx.Set(doc, newFeedbackRequestDocument(current)); err != nil {
			return fmt.Errorf("firestore: set request status: %w", err)
		}

		// Release the uniqueness claim inside the same transaction: a closed
		// request no longer occupies the open-request slot.
		claimRef := feedbackRequestClaimDoc(r.client, current.RequesterID, current.RequesteeID, current.PeriodID)
		if err := tx.Delete(claimRef); err != nil {
			return fmt.Errorf("firestore: release request claim: %w", err)
		}

		updated = current
		return nil
	})
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
			// Transitioning a missing or already-closed request is a caller
			// error, not a Firestore failure.
			return nil, err
		}
		r.logError("firestore: set request status failed", err, "request_id", id, "status", string(to))
		return nil, err
	}

	return updated, nil
}

// CloseMatchingOpen finds the open request matching the (requester, requestee,
// period) triple — the requester asked the reviewee (who just submitted
// feedback) for feedback in that period — and marks it completed, releasing
// its claim. It returns apperror.ErrFeedbackRequestNotFound when no open
// request matches, which callers treat as "nothing to complete" rather than
// an error.
func (r *FeedbackRequestFirestoreRepository) CloseMatchingOpen(ctx context.Context, requesterID, requesteeID, periodID string) (*model.FeedbackRequest, error) {
	if strings.TrimSpace(requesterID) == "" || strings.TrimSpace(requesteeID) == "" || strings.TrimSpace(periodID) == "" {
		return nil, apperror.ErrFeedbackRequestNotFound
	}

	claimRef := feedbackRequestClaimDoc(r.client, requesterID, requesteeID, periodID)
	snapshot, err := claimRef.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, apperror.ErrFeedbackRequestNotFound
	}
	if err != nil {
		r.logError("firestore: read request claim failed", err, "requester_id", requesterID, "requestee_id", requesteeID, "period_id", periodID)
		return nil, fmt.Errorf("firestore: read request claim: %w", err)
	}

	requestID, _ := snapshot.Data()["request_id"].(string)
	if strings.TrimSpace(requestID) == "" {
		// A claim document that predates the request_id field; treat it as
		// no match rather than guessing.
		return nil, apperror.ErrFeedbackRequestNotFound
	}

	return r.SetStatus(ctx, requestID, model.FeedbackRequestStatusCompleted)
}

// listDirection selects the side of the request relationship a listing is
// scoped to, mirroring the received/sent split of the feedback lists.
type listDirection string

const (
	listByRequestee listDirection = "requestee"
	listByRequester listDirection = "requester"
)

// list returns one page of requests where the given employee is the requestee
// (received) or the requester (sent), ordered by created_at descending (newest
// first), plus the ID of the last request on the page for use as the next
// page's cursor.
//
// Pagination semantics mirror the feedback repository: cursor-based on
// document IDs, limit+1 page-probe, unknown cursor yields
// apperror.ErrFeedbackRequestNotFound, empty result is a non-nil empty slice.
//
// NOTE: both list queries require Firestore composite indexes on
// (requestee_id ASC, status ASC, created_at DESC) and
// (requester_id ASC, status ASC, created_at DESC), which must be created
// out-of-band like the ListByReviewee index. When a request is still open its
// status filter matches "open"; completed and declined requests are listed
// too (the listing is not status-filtered beyond the query shape).
func (r *FeedbackRequestFirestoreRepository) list(ctx context.Context, direction listDirection, employeeID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	if strings.TrimSpace(employeeID) == "" {
		return []*model.FeedbackRequest{}, "", nil
	}
	if limit <= 0 {
		limit = 1
	}

	field := "requestee_id"
	if direction == listByRequester {
		field = "requester_id"
	}

	query := r.client.Collection(FeedbackRequestCollection).
		Where(field, "==", employeeID).
		OrderBy("created_at", firestore.Desc).
		Limit(limit + 1)

	if strings.TrimSpace(cursorID) != "" {
		cursorSnap, err := r.client.Collection(FeedbackRequestCollection).Doc(cursorID).Get(ctx)
		if status.Code(err) == codes.NotFound {
			return nil, "", apperror.ErrFeedbackRequestNotFound
		}
		if err != nil {
			r.logError("firestore: fetch request cursor failed", err, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: fetch request cursor: %w", err)
		}
		query = query.StartAfter(cursorSnap)
	}

	iter := query.Documents(ctx)
	defer iter.Stop()

	requests := []*model.FeedbackRequest{}
	for {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			r.logError("firestore: query requests failed", err, "employee_id", employeeID, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: query requests: %w", err)
		}
		req, err := r.toFeedbackRequest(snapshot)
		if err != nil {
			return nil, "", err
		}
		requests = append(requests, req)
	}

	nextCursorID := ""
	if len(requests) > limit {
		requests = requests[:limit]
		nextCursorID = requests[limit-1].ID
	}
	return requests, nextCursorID, nil
}

// ListByRequestee returns one page of requests the named employee has
// received, ordered by created_at descending, plus the next-page cursor.
func (r *FeedbackRequestFirestoreRepository) ListByRequestee(ctx context.Context, requesteeID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	return r.list(ctx, listByRequestee, requesteeID, limit, cursorID)
}

// ListByRequester returns one page of requests the named employee has sent,
// ordered by created_at descending, plus the next-page cursor.
func (r *FeedbackRequestFirestoreRepository) ListByRequester(ctx context.Context, requesterID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	return r.list(ctx, listByRequester, requesterID, limit, cursorID)
}
