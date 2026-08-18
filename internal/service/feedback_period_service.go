package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
)

// FeedbackPeriodRepository is the consumer-defined contract for the feedback-
// period repository. It is intentionally minimal: only the operations the
// service actually needs. The concrete repository implementation satisfies it
// implicitly.
type FeedbackPeriodRepository interface {
	Create(ctx context.Context, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error)
}

// FeedbackPeriodService is the application layer that orchestrates feedback-
// period operations against a FeedbackPeriodRepository.
type FeedbackPeriodService struct {
	repo   FeedbackPeriodRepository
	logger *slog.Logger
}

// NewFeedbackPeriodService creates a FeedbackPeriodService backed by the given
// repository. If logger is nil, slog.Default() is used.
func NewFeedbackPeriodService(repo FeedbackPeriodRepository, logger *slog.Logger) *FeedbackPeriodService {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackPeriodService{repo: repo, logger: logger}
}

// Create creates a new feedback period after validating the input. The caller
// (an org admin) is identified by organizationName, which is taken from the
// authenticated JWT by the handler and assigned here so a client cannot create
// a period for an org they do not belong to. A UUIDv7 ID is assigned and
// CreatedAt/UpdatedAt are set by the repository on persist.
//
// Validation:
//   - name is required (non-empty after trim).
//   - organizationName is required (non-empty after trim).
//   - startDate must not be the zero time.
//   - endDate must not be the zero time and must be after startDate.
func (s *FeedbackPeriodService) Create(ctx context.Context, organizationName string, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error) {
	if period == nil {
		return nil, apperror.ErrInvalidFeedbackPeriod("feedback period must not be nil")
	}
	if strings.TrimSpace(period.Name) == "" {
		return nil, apperror.ErrInvalidFeedbackPeriod("name is required")
	}
	if strings.TrimSpace(organizationName) == "" {
		return nil, apperror.ErrInvalidFeedbackPeriod("organization_name is required")
	}
	if period.StartDate.IsZero() {
		return nil, apperror.ErrInvalidFeedbackPeriod("start_date is required")
	}
	if period.EndDate.IsZero() {
		return nil, apperror.ErrInvalidFeedbackPeriod("end_date is required")
	}
	if !period.EndDate.After(period.StartDate) {
		return nil, apperror.ErrInvalidFeedbackPeriod("end_date must be after start_date")
	}

	period.OrganizationName = organizationName
	id, err := uuid.NewV7()
	if err != nil {
		s.logger.Error("failed to generate UUID", "error", err)
		return nil, err
	}
	period.ID = id.String()

	created, err := s.repo.Create(ctx, period)
	if err != nil {
		return nil, err
	}
	return created, nil
}