package repository

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/status"

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
	PeriodID           string                    `firestore:"period_id"`
	RevieweeID         string                    `firestore:"reviewee_id"`
	ReviewerID         string                    `firestore:"reviewer_id"`
	CommunicationScore int                       `firestore:"communication_score"`
	LeadershipScore    int                       `firestore:"leadership_score"`
	TechnicalScore     int                       `firestore:"technical_score"`
	CollaborationScore int                       `firestore:"collaboration_score"`
	DeliveryScore      int                       `firestore:"delivery_score"`
	TrustScore         int                       `firestore:"trust_score"`
	StrengthsComment   string                    `firestore:"strengths_comment"`
	WeaknessesComment  string                    `firestore:"weaknesses_comment"`
	Visibility         model.FeedbackVisibility  `firestore:"visibility"`
	CreatedAt          time.Time                 `firestore:"created_at"`
	UpdatedAt          time.Time                 `firestore:"updated_at"`
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