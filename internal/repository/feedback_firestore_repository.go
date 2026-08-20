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
	CreatedAt          time.Time                `firestore:"created_at"`
	UpdatedAt          time.Time                `firestore:"updated_at"`
}

// Create stores the given feedback entry as a new document keyed by its ID and
// returns the stored record. The ID is assigned by the caller (the service
// layer); Create does not generate one. CreatedAt and UpdatedAt are set here to
// the current time, overriding whatever the caller supplied.
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
// (reviewee_id ASC, created_at DESC). The repository does not manage a
// firestore.indexes.json; the index must be created out-of-band (e.g. via the
// GCP console or `gcloud firestore indexes composite create`). The first
// request against an unindexed collection will fail with a
// FailedPrecondition error whose message contains a one-click link to create
// the index.
func (r *FeedbackFirestoreRepository) ListByReviewee(ctx context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if strings.TrimSpace(revieweeID) == "" {
		return []*model.Feedback{}, "", nil
	}
	if limit <= 0 {
		limit = 1
	}

	query := r.client.Collection(FeedbackCollection).
		Where("reviewee_id", "==", revieweeID).
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
		CreatedAt:          doc.CreatedAt,
		UpdatedAt:          doc.UpdatedAt,
	}, nil
}
