package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
	"github.com/tsongpon/echo/internal/service"
)

// Compile-time assertion that FeedbackFirestoreRepository satisfies the
// service.FeedbackRepository interface.
var _ service.FeedbackRepository = (*FeedbackFirestoreRepository)(nil)

// FeedbackCollection is the Firestore collection that holds feedback records.
const FeedbackCollection = "feedbacks"

// FeedbackDraftClaimCollection is a dedicated collection that enforces at most
// one draft per (reviewer, reviewee, period) triple. Each document is keyed
// by "<reviewerID>_<revieweeID>_<periodID>" and points at the draft feedback
// that claimed it. Firestore only enforces uniqueness on document IDs, not on
// fields, so the claim document acts as a unique-constraint ledger in the
// same spirit as the employee email-claim collection: a Create on an existing
// claim ID fails atomically inside the draft-create transaction. The claim
// is released (deleted) when the draft is submitted or deleted, after which
// the reviewer may start a new draft for the same pair again.
const FeedbackDraftClaimCollection = "feedback_draft_claims"

// feedbackDraftClaimDoc returns the document reference for the draft claim
// keyed by the (reviewer, reviewee, period) triple. The IDs are UUIDs, so
// joining them with "_" is collision-free.
func feedbackDraftClaimDoc(client *firestore.Client, reviewerID, revieweeID, periodID string) *firestore.DocumentRef {
	return client.Collection(FeedbackDraftClaimCollection).Doc(reviewerID + "_" + revieweeID + "_" + periodID)
}

// FeedbackFirestoreRepository is a service.FeedbackRepository backed by GCP
// Firestore. Each feedback entry is one document in the FeedbackCollection
// keyed by the feedback ID, so lookups by ID are a direct document read rather
// than a query.
type FeedbackFirestoreRepository struct {
	client *firestore.Client
	logger *slog.Logger
}

// NewFeedbackFirestoreRepository creates a repository over the given Firestore
// client. The client is owned by the caller: closing it is the caller's
// responsibility. The logger defaults to slog.Default() when nil.
func NewFeedbackFirestoreRepository(client *firestore.Client, logger *slog.Logger) *FeedbackFirestoreRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackFirestoreRepository{client: client, logger: logger}
}

// logError records a failed Firestore call. See
// EmployeeFirestoreRepository.logError for the rationale behind logging the
// gRPC status code as its own field.
func (r *FeedbackFirestoreRepository) logError(msg string, err error, args ...any) {
	r.logger.Error(msg, append([]any{"error", err, "code", status.Code(err).String()}, args...)...)
}

// feedbackDocument is the Firestore representation of model.Feedback. It is
// kept separate from the domain model so the stored field names are an explicit,
// stable contract rather than a by-product of Go field naming. The feedback ID
// is the document ID and is deliberately not duplicated as a field.
type feedbackDocument struct {
	PeriodID           string                   `firestore:"period_id"`
	RevieweeID         string                   `firestore:"reviewee_id"`
	ReviewerID         string                   `firestore:"reviewer_id"`
	CommunicationScore int                      `firestore:"communication_score"`
	LeadershipScore    int                      `firestore:"leadership_score"`
	TechnicalScore     int                      `firestore:"technical_score"`
	CollaborationScore int                      `firestore:"collaboration_score"`
	DeliveryScore      int                      `firestore:"delivery_score"`
	TrustScore         int                      `firestore:"trust_score"`
	StrengthsComment   string                   `firestore:"strengths_comment"`
	WeaknessesComment  string                   `firestore:"weaknesses_comment"`
	Visibility         model.FeedbackVisibility `firestore:"visibility"`
	Status             model.FeedbackStatus     `firestore:"status"`
	CreatedAt          time.Time                `firestore:"created_at"`
	UpdatedAt          time.Time                `firestore:"updated_at"`
}

// Create stores the given feedback entry as a new document keyed by its ID and
// returns the stored record. The ID is assigned by the caller (the service
// layer); Create does not generate one. CreatedAt and UpdatedAt are set here to
// the current time, overriding whatever the caller supplied. A blank status is
// normalized to submitted so a legacy service path cannot persist a document
// without the field.
//
// Create fails if a document with the same ID already exists, so a retried
// create cannot silently overwrite an existing feedback entry.
func (r *FeedbackFirestoreRepository) Create(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error) {
	if feedback == nil {
		return nil, ErrNilFeedback
	}
	if strings.TrimSpace(feedback.ID) == "" {
		return nil, ErrNilFeedbackID
	}

	stored := *feedback
	if stored.Status == "" {
		stored.Status = model.FeedbackStatusSubmitted
	}
	now := storeTime(time.Now())
	stored.CreatedAt = now
	stored.UpdatedAt = now

	if _, err := r.client.Collection(FeedbackCollection).Doc(stored.ID).Create(ctx, newFeedbackDocument(&stored)); err != nil {
		r.logError("firestore: create feedback failed", err, "feedback_id", stored.ID)
		return nil, fmt.Errorf("firestore: create feedback: %w", err)
	}

	return &stored, nil
}

// newFeedbackDocument projects a domain feedback entry onto its stored form.
func newFeedbackDocument(feedback *model.Feedback) *feedbackDocument {
	return &feedbackDocument{
		PeriodID:           feedback.PeriodID,
		RevieweeID:         feedback.RevieweeID,
		ReviewerID:         feedback.ReviewerID,
		CommunicationScore: feedback.CommunicationScore,
		LeadershipScore:    feedback.LeadershipScore,
		TechnicalScore:     feedback.TechnicalScore,
		CollaborationScore: feedback.CollaborationScore,
		DeliveryScore:      feedback.DeliveryScore,
		TrustScore:         feedback.TrustScore,
		StrengthsComment:   feedback.StrengthsComment,
		WeaknessesComment:  feedback.WeaknessesComment,
		Visibility:         feedback.Visibility,
		Status:             feedback.Status,
		CreatedAt:          feedback.CreatedAt,
		UpdatedAt:          feedback.UpdatedAt,
	}
}

// ListByReviewee returns one page of feedback entries received by the named
// reviewee (i.e. entries whose reviewee_id matches), ordered by created_at
// descending (newest first), plus the ID of the last entry on the page for use
// as the next page's cursor.
//
// Pagination is cursor-based: when cursorID is non-empty, the cursor feedback's
// document snapshot is fetched first and used as a StartAfter point. Using the
// snapshot (rather than the bare created_at value) preserves correct ordering
// when two entries share a created_at, because Firestore's StartAfter on a
// snapshot breaks ties by document ID. The cursor must refer to an existing
// feedback entry; an unknown cursor returns apperror.ErrFeedbackNotFound so
// the handler can map it to a 400.
//
// To detect whether another page exists, the query fetches limit+1 rows and
// the caller only sees the first limit; the extra row (if any) is discarded
// but its presence indicates more results. When the query returns fewer than
// limit+1 rows the returned nextCursorID is empty, signalling the end of the
// listing. Returns an empty (non-nil) slice when no feedback matches.
//
// NOTE: this query requires a Firestore composite index on
// (reviewee_id ASC, status ASC, created_at DESC). The repository does not
// manage a firestore.indexes.json; the index must be created out-of-band (e.g.
// via the GCP console or `gcloud firestore indexes composite create`). The first
// request against an unindexed collection will fail with a
// FailedPrecondition error whose message contains a one-click link to create
// the index.
//
// Only submitted entries are returned: drafts are the reviewer's private
// work-in-progress and must never appear in the reviewee's or a manager's
// view. The status filter matches the literal "submitted" string, so it
// excludes both drafts and legacy documents written before the status field
// existed — hence the required one-off backfill that sets status on existing
// documents (see cmd/server/backfill_feedback_status.go).
func (r *FeedbackFirestoreRepository) ListByReviewee(ctx context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(revieweeID) == "" {
		return []*model.Feedback{}, "", nil
	}
	if limit <= 0 {
		limit = 1
	}

	query := r.client.Collection(FeedbackCollection).
		Where("reviewee_id", "==", revieweeID).
		Where("status", "==", model.FeedbackStatusSubmitted).
		OrderBy("created_at", firestore.Desc).
		Limit(limit + 1)

	if strings.TrimSpace(cursorID) != "" {
		cursorSnap, err := r.client.Collection(FeedbackCollection).Doc(cursorID).Get(ctx)
		if status.Code(err) == codes.NotFound {
			return nil, "", apperror.ErrFeedbackNotFound
		}
		if err != nil {
			r.logError("firestore: fetch feedback cursor failed", err, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: fetch feedback cursor: %w", err)
		}
		query = query.StartAfter(cursorSnap)
	}

	iter := query.Documents(ctx)
	defer iter.Stop()

	feedbacks := []*model.Feedback{}
	for {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			r.logError("firestore: query feedback by reviewee failed", err, "reviewee_id", revieweeID, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: query feedback by reviewee: %w", err)
		}
		fb, err := r.toFeedback(snapshot)
		if err != nil {
			return nil, "", err
		}
		feedbacks = append(feedbacks, fb)
	}

	// If we got limit+1 rows, there is at least one more page. Trim the extra
	// row from what we return, and use the last visible entry's ID as the next
	// cursor. If we got limit or fewer, there is no next page.
	nextCursorID := ""
	if len(feedbacks) > limit {
		feedbacks = feedbacks[:limit]
		nextCursorID = feedbacks[limit-1].ID
	}
	return feedbacks, nextCursorID, nil
}

// toFeedback decodes a document snapshot into a domain feedback entry, taking
// the ID from the document key.
func (r *FeedbackFirestoreRepository) toFeedback(snapshot *firestore.DocumentSnapshot) (*model.Feedback, error) {
	var doc feedbackDocument
	if err := snapshot.DataTo(&doc); err != nil {
		// A stored document that no longer fits the struct: a schema change or
		// a record written by something other than this repository. Logged
		// with the document ID because the bad record has to be found by hand.
		r.logError("firestore: decode feedback document failed", err, "feedback_id", snapshot.Ref.ID)
		return nil, fmt.Errorf("firestore: decode feedback %s: %w", snapshot.Ref.ID, err)
	}

	return &model.Feedback{
		ID:                 snapshot.Ref.ID,
		PeriodID:           doc.PeriodID,
		RevieweeID:         doc.RevieweeID,
		ReviewerID:         doc.ReviewerID,
		CommunicationScore: doc.CommunicationScore,
		LeadershipScore:    doc.LeadershipScore,
		TechnicalScore:     doc.TechnicalScore,
		CollaborationScore: doc.CollaborationScore,
		DeliveryScore:      doc.DeliveryScore,
		TrustScore:         doc.TrustScore,
		StrengthsComment:   doc.StrengthsComment,
		WeaknessesComment:  doc.WeaknessesComment,
		Visibility:         doc.Visibility,
		Status:             doc.Status,
		CreatedAt:          doc.CreatedAt,
		UpdatedAt:          doc.UpdatedAt,
	}, nil
}

// Get returns the feedback entry with the given ID. Returns
// apperror.ErrFeedbackNotFound when no entry matches.
func (r *FeedbackFirestoreRepository) Get(ctx context.Context, id string) (*model.Feedback, error) {
	if strings.TrimSpace(id) == "" {
		return nil, apperror.ErrFeedbackNotFound
	}

	snapshot, err := r.client.Collection(FeedbackCollection).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, apperror.ErrFeedbackNotFound
	}
	if err != nil {
		r.logError("firestore: get feedback by ID failed", err, "feedback_id", id)
		return nil, fmt.Errorf("firestore: get feedback by ID: %w", err)
	}

	return r.toFeedback(snapshot)
}

// CreateDraft stores a draft feedback entry and claims the
// (reviewer, reviewee, period) triple, both inside one transaction so the
// uniqueness rule is enforced atomically: reading the claim inside the
// transaction adds it to the read set, so concurrent draft creations for the
// same pair are serialized by Firestore's per-document conflict detection and
// the loser observes apperror.ErrFeedbackDraftAlreadyExists on retry. See the
// employee email-claim Create for the pattern's rationale.
//
// CreatedAt and UpdatedAt are set here, overriding the caller's values. The
// stored status is always draft: this method must not be used to write a
// submitted entry.
func (r *FeedbackFirestoreRepository) CreateDraft(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error) {
	if feedback == nil {
		return nil, ErrNilFeedback
	}
	if strings.TrimSpace(feedback.ID) == "" {
		return nil, ErrNilFeedbackID
	}

	stored := *feedback
	stored.Status = model.FeedbackStatusDraft
	now := storeTime(time.Now())
	stored.CreatedAt = now
	stored.UpdatedAt = now

	claimRef := feedbackDraftClaimDoc(r.client, stored.ReviewerID, stored.RevieweeID, stored.PeriodID)
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(claimRef); err == nil {
			return apperror.ErrFeedbackDraftAlreadyExists
		} else if status.Code(err) != codes.NotFound {
			return fmt.Errorf("firestore: read draft claim: %w", err)
		}
		if err := tx.Create(claimRef, map[string]any{
			"reviewer_id": stored.ReviewerID,
			"reviewee_id": stored.RevieweeID,
			"period_id":   stored.PeriodID,
			"feedback_id": stored.ID,
			"created_at":  stored.CreatedAt,
		}); err != nil {
			return fmt.Errorf("firestore: claim draft: %w", err)
		}
		if err := tx.Create(r.client.Collection(FeedbackCollection).Doc(stored.ID), newFeedbackDocument(&stored)); err != nil {
			return fmt.Errorf("firestore: create draft: %w", err)
		}
		return nil
	})
	if err != nil {
		// Logged out here rather than inside the transaction body: Firestore
		// re-runs that body on contention, so logging in it would emit a line
		// per attempt for what is ultimately one failure.
		if errors.Is(err, apperror.ErrFeedbackDraftAlreadyExists) {
			// A duplicate draft is a caller error, not a Firestore failure.
			return nil, err
		}
		r.logError("firestore: create draft failed", err, "feedback_id", stored.ID, "reviewer_id", stored.ReviewerID, "reviewee_id", stored.RevieweeID, "period_id", stored.PeriodID)
		return nil, err
	}

	return &stored, nil
}

// UpdateDraft overwrites the mutable fields of a stored draft and returns the
// updated record. ID, ReviewerID, RevieweeID, PeriodID, and CreatedAt are
// preserved from the stored document; Status remains draft; UpdatedAt is
// refreshed.
//
// The write is guarded two ways inside a transaction: the stored entry must
// still be a draft (submitting or deleting concurrently makes the update fail
// with apperror.ErrFeedbackNotFound rather than resurrecting the draft), and
// the stored UpdatedAt must match the one on the supplied record
// (apperror.ErrFeedbackConcurrentUpdate otherwise), mirroring the employee
// repository's optimistic-concurrency pattern. Callers must pass a record
// obtained from Get (or CreateDraft).
func (r *FeedbackFirestoreRepository) UpdateDraft(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error) {
	if feedback == nil {
		return nil, ErrNilFeedback
	}
	if strings.TrimSpace(feedback.ID) == "" {
		return nil, ErrNilFeedbackID
	}

	doc := r.client.Collection(FeedbackCollection).Doc(feedback.ID)
	updated := *feedback

	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(doc)
		if status.Code(err) == codes.NotFound {
			return apperror.ErrFeedbackNotFound
		}
		if err != nil {
			return fmt.Errorf("firestore: read draft for update: %w", err)
		}

		current, err := r.toFeedback(snapshot)
		if err != nil {
			return err
		}
		if current.NormalizedStatus() != model.FeedbackStatusDraft {
			// The draft was submitted or deleted between the caller's read
			// and this write. Treat it as gone: the caller fetched something
			// that is no longer a draft.
			return apperror.ErrFeedbackNotFound
		}
		if !current.UpdatedAt.Equal(feedback.UpdatedAt) {
			return apperror.ErrFeedbackConcurrentUpdate
		}

		// Identity and lifecycle fields are not the caller's to change.
		updated.ID = current.ID
		updated.ReviewerID = current.ReviewerID
		updated.RevieweeID = current.RevieweeID
		updated.PeriodID = current.PeriodID
		updated.Status = model.FeedbackStatusDraft
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = storeTime(time.Now())

		return tx.Set(doc, newFeedbackDocument(&updated))
	})
	if err != nil {
		switch {
		case errors.Is(err, apperror.ErrFeedbackNotFound):
			// An update against a missing or no-longer-draft entry is a
			// caller error, not a Firestore failure.
		case errors.Is(err, apperror.ErrFeedbackConcurrentUpdate):
			// Expected when the reviewer edits from two devices at once; the
			// caller can retry with a fresh read.
			r.logger.Warn("firestore: draft update rejected, record changed since it was read",
				"feedback_id", feedback.ID)
		default:
			r.logError("firestore: update draft failed", err, "feedback_id", feedback.ID)
		}
		return nil, err
	}

	return &updated, nil
}

// SubmitDraft transitions a draft to submitted and releases its
// (reviewer, reviewee, period) claim, both inside one transaction so the
// reviewer can immediately start a new draft for the same pair without a
// window in which the claim blocks them. The transition is guarded by a
// re-read: if the entry is missing or no longer a draft,
// apperror.ErrFeedbackNotFound is returned rather than double-submitting.
// UpdatedAt is refreshed to mark the submission time.
func (r *FeedbackFirestoreRepository) SubmitDraft(ctx context.Context, id string) (*model.Feedback, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrNilFeedbackID
	}

	doc := r.client.Collection(FeedbackCollection).Doc(id)
	var submitted *model.Feedback

	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(doc)
		if status.Code(err) == codes.NotFound {
			return apperror.ErrFeedbackNotFound
		}
		if err != nil {
			return fmt.Errorf("firestore: read draft for submit: %w", err)
		}

		current, err := r.toFeedback(snapshot)
		if err != nil {
			return err
		}
		if current.NormalizedStatus() != model.FeedbackStatusDraft {
			return apperror.ErrFeedbackNotFound
		}

		current.Status = model.FeedbackStatusSubmitted
		current.UpdatedAt = storeTime(time.Now())
		if err := tx.Set(doc, newFeedbackDocument(current)); err != nil {
			return fmt.Errorf("firestore: submit draft: %w", err)
		}

		// Release the uniqueness claim inside the same transaction: the
		// submitted entry no longer occupies the draft slot.
		claimRef := feedbackDraftClaimDoc(r.client, current.ReviewerID, current.RevieweeID, current.PeriodID)
		if err := tx.Delete(claimRef); err != nil {
			return fmt.Errorf("firestore: release draft claim: %w", err)
		}

		submitted = current
		return nil
	})
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			// Submitting a missing or already-submitted draft is a caller
			// error, not a Firestore failure.
			return nil, err
		}
		r.logError("firestore: submit draft failed", err, "feedback_id", id)
		return nil, err
	}

	return submitted, nil
}

// DeleteDraft removes a draft and releases its (reviewer, reviewee, period)
// claim inside one transaction, so a deleted draft immediately frees its slot.
// Deleting an entry that is missing or no longer a draft yields
// apperror.ErrFeedbackNotFound rather than touching a submitted entry.
func (r *FeedbackFirestoreRepository) DeleteDraft(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrNilFeedbackID
	}

	doc := r.client.Collection(FeedbackCollection).Doc(id)
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(doc)
		if status.Code(err) == codes.NotFound {
			return apperror.ErrFeedbackNotFound
		}
		if err != nil {
			return fmt.Errorf("firestore: read draft for delete: %w", err)
		}

		current, err := r.toFeedback(snapshot)
		if err != nil {
			return err
		}
		if current.NormalizedStatus() != model.FeedbackStatusDraft {
			return apperror.ErrFeedbackNotFound
		}

		if err := tx.Delete(doc); err != nil {
			return fmt.Errorf("firestore: delete draft: %w", err)
		}

		claimRef := feedbackDraftClaimDoc(r.client, current.ReviewerID, current.RevieweeID, current.PeriodID)
		if err := tx.Delete(claimRef); err != nil {
			return fmt.Errorf("firestore: release draft claim: %w", err)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, apperror.ErrFeedbackNotFound) {
			// Deleting a missing or already-submitted entry is a caller
			// error, not a Firestore failure.
			return err
		}
		r.logError("firestore: delete draft failed", err, "feedback_id", id)
		return err
	}
	return nil
}

// ListDraftsByReviewer returns one page of draft feedback entries written by
// the named reviewer, ordered by created_at descending (newest first), plus
// the ID of the last entry on the page for use as the next page's cursor.
// Only drafts are returned; submitted entries the reviewer wrote are not
// part of this listing.
//
// Pagination semantics mirror ListByReviewee: cursor-based on document IDs,
// limit+1 page-probe, unknown cursor yields apperror.ErrFeedbackNotFound,
// empty result is a non-nil empty slice.
//
// NOTE: this query requires a Firestore composite index on
// (reviewer_id ASC, status ASC, created_at DESC), which must be created
// out-of-band like the ListByReviewee index.
func (r *FeedbackFirestoreRepository) ListDraftsByReviewer(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(reviewerID) == "" {
		return []*model.Feedback{}, "", nil
	}
	if limit <= 0 {
		limit = 1
	}

	query := r.client.Collection(FeedbackCollection).
		Where("reviewer_id", "==", reviewerID).
		Where("status", "==", model.FeedbackStatusDraft).
		OrderBy("created_at", firestore.Desc).
		Limit(limit + 1)

	if strings.TrimSpace(cursorID) != "" {
		cursorSnap, err := r.client.Collection(FeedbackCollection).Doc(cursorID).Get(ctx)
		if status.Code(err) == codes.NotFound {
			return nil, "", apperror.ErrFeedbackNotFound
		}
		if err != nil {
			r.logError("firestore: fetch draft cursor failed", err, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: fetch draft cursor: %w", err)
		}
		query = query.StartAfter(cursorSnap)
	}

	iter := query.Documents(ctx)
	defer iter.Stop()

	drafts := []*model.Feedback{}
	for {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			r.logError("firestore: query drafts by reviewer failed", err, "reviewer_id", reviewerID, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: query drafts by reviewer: %w", err)
		}
		fb, err := r.toFeedback(snapshot)
		if err != nil {
			return nil, "", err
		}
		drafts = append(drafts, fb)
	}

	nextCursorID := ""
	if len(drafts) > limit {
		drafts = drafts[:limit]
		nextCursorID = drafts[limit-1].ID
	}
	return drafts, nextCursorID, nil
}

// ListSubmittedByReviewer returns one page of submitted feedback entries
// written by the named reviewer, ordered by created_at descending (newest
// first), plus the ID of the last entry on the page for use as the next
// page's cursor. Drafts are not part of this listing — they stay private
// to the author and are listed via ListDraftsByReviewer.
//
// Pagination semantics mirror ListByReviewee: cursor-based on document IDs,
// limit+1 page-probe, unknown cursor yields apperror.ErrFeedbackNotFound,
// empty result is a non-nil empty slice.
//
// NOTE: this query requires the same Firestore composite index as
// ListDraftsByReviewer: (reviewer_id ASC, status ASC, created_at DESC).
func (r *FeedbackFirestoreRepository) ListSubmittedByReviewer(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(reviewerID) == "" {
		return []*model.Feedback{}, "", nil
	}
	if limit <= 0 {
		limit = 1
	}

	query := r.client.Collection(FeedbackCollection).
		Where("reviewer_id", "==", reviewerID).
		Where("status", "==", model.FeedbackStatusSubmitted).
		OrderBy("created_at", firestore.Desc).
		Limit(limit + 1)

	if strings.TrimSpace(cursorID) != "" {
		cursorSnap, err := r.client.Collection(FeedbackCollection).Doc(cursorID).Get(ctx)
		if status.Code(err) == codes.NotFound {
			return nil, "", apperror.ErrFeedbackNotFound
		}
		if err != nil {
			r.logError("firestore: fetch given-feedback cursor failed", err, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: fetch given-feedback cursor: %w", err)
		}
		query = query.StartAfter(cursorSnap)
	}

	iter := query.Documents(ctx)
	defer iter.Stop()

	feedbacks := []*model.Feedback{}
	for {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			r.logError("firestore: query submitted feedback by reviewer failed", err, "reviewer_id", reviewerID, "cursor_id", cursorID)
			return nil, "", fmt.Errorf("firestore: query submitted feedback by reviewer: %w", err)
		}
		fb, err := r.toFeedback(snapshot)
		if err != nil {
			return nil, "", err
		}
		feedbacks = append(feedbacks, fb)
	}

	nextCursorID := ""
	if len(feedbacks) > limit {
		feedbacks = feedbacks[:limit]
		nextCursorID = feedbacks[limit-1].ID
	}
	return feedbacks, nextCursorID, nil
}
