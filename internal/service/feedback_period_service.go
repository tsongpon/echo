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
	GetByID(ctx context.Context, id string) (*model.FeedbackPeriod, error)
	ListByOrganization(ctx context.Context, organizationName string) ([]*model.FeedbackPeriod, error)
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
		s.logger.Warn("feedback period create rejected: nil period", "organization_name", organizationName)
		return nil, apperror.ErrInvalidFeedbackPeriod("feedback period must not be nil")
	}
	if strings.TrimSpace(period.Name) == "" {
		s.logger.Warn("feedback period create rejected: missing name", "organization_name", organizationName)
		return nil, apperror.ErrInvalidFeedbackPeriod("name is required")
	}
	if strings.TrimSpace(organizationName) == "" {
		s.logger.Warn("feedback period create rejected: missing organization_name", "name", period.Name)
		return nil, apperror.ErrInvalidFeedbackPeriod("organization_name is required")
	}
	if period.StartDate.IsZero() {
		s.logger.Warn("feedback period create rejected: missing start_date", "name", period.Name, "organization_name", organizationName)
		return nil, apperror.ErrInvalidFeedbackPeriod("start_date is required")
	}
	if period.EndDate.IsZero() {
		s.logger.Warn("feedback period create rejected: missing end_date", "name", period.Name, "organization_name", organizationName)
		return nil, apperror.ErrInvalidFeedbackPeriod("end_date is required")
	}
	if !period.EndDate.After(period.StartDate) {
		s.logger.Warn("feedback period create rejected: end_date not after start_date",
			"name", period.Name, "organization_name", organizationName,
			"start_date", period.StartDate, "end_date", period.EndDate)
		return nil, apperror.ErrInvalidFeedbackPeriod("end_date must be after start_date")
	}

	period.OrganizationName = organizationName
	id, err := uuid.NewV7()
	if err != nil {
		s.logger.Error("feedback period create aborted: failed to generate UUID", "error", err, "organization_name", organizationName)
		return nil, err
	}
	period.ID = id.String()

	created, err := s.repo.Create(ctx, period)
	if err != nil {
		s.logger.Error("feedback period create aborted: repository create failed",
			"error", err, "period_id", period.ID, "name", period.Name, "organization_name", organizationName)
		return nil, err
	}
	return created, nil
}

// ListByOrganization returns the feedback periods that belong to the named
// organization, ordered by start date descending (most recent first). The
// organization name is taken from the authenticated caller's JWT by the
// handler, so an employee can only see their own organization's periods.
// Returns an empty slice when no periods exist yet.
func (s *FeedbackPeriodService) ListByOrganization(ctx context.Context, organizationName string) ([]*model.FeedbackPeriod, error) {
	if strings.TrimSpace(organizationName) == "" {
		s.logger.Warn("feedback period list rejected: missing organization_name")
		return nil, apperror.ErrInvalidFeedbackPeriod("organization_name is required")
	}

	periods, err := s.repo.ListByOrganization(ctx, organizationName)
	if err != nil {
		s.logger.Error("feedback period list failed",
			"error", err, "organization_name", organizationName)
		return nil, err
	}
	return periods, nil
}