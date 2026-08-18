package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
)

// fakeFeedbackPeriodRepo is an in-test stand-in for
// service.FeedbackPeriodRepository that records the period passed to Create and
// serves lookups from an in-memory map without going through the real
// repository package (which would create an import cycle).
type fakeFeedbackPeriodRepo struct {
	created  *model.FeedbackPeriod
	byID     map[string]*model.FeedbackPeriod
	createFn func(ctx context.Context, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error)
}

func (f *fakeFeedbackPeriodRepo) Create(ctx context.Context, period *model.FeedbackPeriod) (*model.FeedbackPeriod, error) {
	if f.createFn != nil {
		return f.createFn(ctx, period)
	}
	if f.byID == nil {
		f.byID = make(map[string]*model.FeedbackPeriod)
	}
	f.created = period
	f.byID[period.ID] = period
	return period, nil
}

// newFeedbackPeriodTestService builds a FeedbackPeriodService backed by a fake
// repo and a discarding logger.
func newFeedbackPeriodTestService() (*FeedbackPeriodService, *fakeFeedbackPeriodRepo) {
	repo := &fakeFeedbackPeriodRepo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewFeedbackPeriodService(repo, logger), repo
}

func TestFeedbackPeriod_Create(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	t.Run("success", func(t *testing.T) {
		svc, repo := newFeedbackPeriodTestService()
		period := &model.FeedbackPeriod{
			Name:      "H1 2026",
			StartDate: start,
			EndDate:   end,
		}

		created, err := svc.Create(context.Background(), "Acme", period)
		if err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}

		// The org is taken from the caller (the JWT), not the body, so a
		// client cannot create a period for an org they do not belong to.
		if created.OrganizationName != "Acme" {
			t.Fatalf("got organization_name %q, want Acme", created.OrganizationName)
		}
		if created.Name != "H1 2026" {
			t.Fatalf("got name %q, want H1 2026", created.Name)
		}
		if created.ID == "" {
			t.Fatal("expected a non-empty ID assigned by the service")
		}
		if repo.created == nil {
			t.Fatal("expected the repo to have received the period")
		}
		if repo.created.ID != created.ID {
			t.Fatalf("repo received id %q, want %q", repo.created.ID, created.ID)
		}
	})

	t.Run("overrides client organization_name", func(t *testing.T) {
		svc, _ := newFeedbackPeriodTestService()
		// Client sets a different org; the caller's must win.
		period := &model.FeedbackPeriod{
			Name:             "H1 2026",
			OrganizationName: "should-be-ignored",
			StartDate:        start,
			EndDate:          end,
		}
		created, err := svc.Create(context.Background(), "Acme", period)
		if err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
		if created.OrganizationName != "Acme" {
			t.Fatalf("got organization_name %q, want Acme (caller wins)", created.OrganizationName)
		}
	})

	t.Run("repository error propagates", func(t *testing.T) {
		repo := &fakeFeedbackPeriodRepo{
			createFn: func(_ context.Context, _ *model.FeedbackPeriod) (*model.FeedbackPeriod, error) {
				return nil, errors.New("db down")
			},
		}
		svc := NewFeedbackPeriodService(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, err := svc.Create(context.Background(), "Acme", &model.FeedbackPeriod{
			Name:      "H1 2026",
			StartDate: start,
			EndDate:   end,
		})
		if err == nil {
			t.Fatal("expected repository error to propagate, got nil")
		}
	})
}

func TestFeedbackPeriod_Create_ValidationErrors(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name             string
		organizationName string
		period           *model.FeedbackPeriod
		wantMsg          string
	}{
		{
			name:             "nil period",
			organizationName: "Acme",
			period:           nil,
			wantMsg:          "feedback period must not be nil",
		},
		{
			name:             "missing name",
			organizationName: "Acme",
			period:           &model.FeedbackPeriod{StartDate: start, EndDate: end},
			wantMsg:          "name is required",
		},
		{
			name:             "blank name",
			organizationName: "Acme",
			period:           &model.FeedbackPeriod{Name: "   ", StartDate: start, EndDate: end},
			wantMsg:          "name is required",
		},
		{
			name:             "missing organization_name",
			organizationName: "",
			period:           &model.FeedbackPeriod{Name: "H1 2026", StartDate: start, EndDate: end},
			wantMsg:          "organization_name is required",
		},
		{
			name:             "missing start_date",
			organizationName: "Acme",
			period:           &model.FeedbackPeriod{Name: "H1 2026", EndDate: end},
			wantMsg:          "start_date is required",
		},
		{
			name:             "missing end_date",
			organizationName: "Acme",
			period:           &model.FeedbackPeriod{Name: "H1 2026", StartDate: start},
			wantMsg:          "end_date is required",
		},
		{
			name:             "end_date not after start_date",
			organizationName: "Acme",
			period:           &model.FeedbackPeriod{Name: "H1 2026", StartDate: end, EndDate: start},
			wantMsg:          "end_date must be after start_date",
		},
		{
			name:             "end_date equals start_date",
			organizationName: "Acme",
			period:           &model.FeedbackPeriod{Name: "H1 2026", StartDate: start, EndDate: start},
			wantMsg:          "end_date must be after start_date",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newFeedbackPeriodTestService()
			_, err := svc.Create(context.Background(), tc.organizationName, tc.period)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !apperror.IsInvalidFeedbackPeriod(err) {
				t.Fatalf("expected ErrInvalidFeedbackPeriod, got %T: %v", err, err)
			}
			if err.Error() != tc.wantMsg {
				t.Fatalf("expected message %q, got %q", tc.wantMsg, err.Error())
			}
		})
	}
}