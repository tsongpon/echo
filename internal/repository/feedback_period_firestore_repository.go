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

// Compile-time assertion that FeedbackPeriodFirestoreRepository satisfies the
// service.FeedbackPeriodRepository interface.
var _ service.FeedbackPeriodRepository = (*FeedbackPeriodFirestoreRepository)(nil)

// FeedbackPeriodCollection is the Firestore collection that holds feedback-
// period records.
const FeedbackPeriodCollection = "feedback_periods"

// FeedbackPeriodFirestoreRepository is a service.FeedbackPeriodRepository backed
// by GCP Firestore. Each period is one document in the FeedbackPeriodCollection
// keyed by the period ID, so lookups by ID are a direct document read rather
// than a query.
type FeedbackPeriodFirestoreRepository struct {
	client *firestore.Client
	logger *slog.Logger
}

// NewFeedbackPeriodFirestoreRepository creates a repository over the given
// Firestore client. The client is owned by the caller: closing it is the
// caller's responsibility. The logger defaults to slog.Default() when nil.
func NewFeedbackPeriodFirestoreRepository(client *firestore.Client, logger *slog.Logger) *FeedbackPeriodFirestoreRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackPeriodFirestoreRepository{client: client, logger: logger}
}

// logError records a failed Firestore call. See
// EmployeeFirestoreRepository.logError for the rationale behind logging the
// gRPC status code as its own field.
func (r *FeedbackPeriodFirestoreRepository) logError(msg string, err error, args ...any) {
	r.logger.Error(msg, append([]any{"error", err, "code", status.Code(err).String()}, args...)...)
}

// feedbackPeriodDocument is the Firestore representation of
// model.FeedbackPeriod. It is kept separate from the domain model so the
// stored field names are an explicit, stable contract rather than a by-product
// of Go field naming. The period ID is the document ID and is deliberately not
// duplicated as a field.
type feedbackPeriodDocument struct {
	Name             string    `firestore:"name"`
	OrganizationName string    `firestore:"organization_name"`
	StartDate        time.Time `firestore:"start_date"`
	EndDate          time.Time `firestore:"end_date"`
	CreatedAt        time.Time `firestore:"created_at"`
	UpdatedAt        time.Time `firestore:"updated_at"`
}

// Create stores the given feedback period as a new document keyed by its ID
// and returns the stored record. The ID is assigned by the caller (the service
// layer); Create does not generate one. CreatedAt and UpdatedAt are set here to
// the current time, overriding whatever the caller supplied.
//
// Create fails if a document with the same ID already exists, so a retried
// create cannot silently overwrite an existing period.
func (r *FeedbackPeriodFirestoreRepository) Create(ctx context.Context, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error) {
	if period == nil {
		return nil, ErrNilFeedbackPeriod
	}
	if strings.TrimSpace(period.ID) == "" {
		return nil, ErrNilFeedbackPeriodID
	}

	stored := *period
	now := storeTime(time.Now())
	stored.CreatedAt = now
	stored.UpdatedAt = now

	if _, err := r.client.Collection(FeedbackPeriodCollection).Doc(stored.ID).Create(ctx, newFeedbackPeriodDocument(&stored)); err != nil {
		r.logError("firestore: create feedback period failed", err, "period_id", stored.ID)
		return nil, fmt.Errorf("firestore: create feedback period: %w", err)
	}

	return &stored, nil
}

// newFeedbackPeriodDocument projects a domain feedback period onto its stored
// form.
func newFeedbackPeriodDocument(period *model.FeedbackPeriod) *feedbackPeriodDocument {
	return &feedbackPeriodDocument{
		Name:             period.Name,
		OrganizationName: period.OrganizationName,
		StartDate:        period.StartDate,
		EndDate:          period.EndDate,
		CreatedAt:        period.CreatedAt,
		UpdatedAt:        period.UpdatedAt,
	}
}

// toFeedbackPeriod decodes a document snapshot into a domain feedback period,
// taking the ID from the document key.
func (r *FeedbackPeriodFirestoreRepository) toFeedbackPeriod(snapshot *firestore.DocumentSnapshot) (*model.FeedbackPeriod, error) {
	var doc feedbackPeriodDocument
	if err := snapshot.DataTo(&doc); err != nil {
		r.logError("firestore: decode feedback period document failed", err, "period_id", snapshot.Ref.ID)
		return nil, fmt.Errorf("firestore: decode feedback period %s: %w", snapshot.Ref.ID, err)
	}

	return &model.FeedbackPeriod{
		ID:               snapshot.Ref.ID,
		Name:             doc.Name,
		OrganizationName: doc.OrganizationName,
		StartDate:        doc.StartDate,
		EndDate:          doc.EndDate,
		CreatedAt:        doc.CreatedAt,
		UpdatedAt:        doc.UpdatedAt,
	}, nil
}