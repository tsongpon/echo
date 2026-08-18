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
	listFn   func(ctx context.Context, organizationName string) ([]*model.FeedbackPeriod, error)
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

func (f *fakeFeedbackPeriodRepo) GetByID(_ context.Context, id string) (*model.FeedbackPeriod, error) {
	if f.byID == nil {
		return nil, apperror.ErrFeedbackPeriodNotFound
	}
	if p, ok := f.byID[id]; ok {
		return p, nil
	}
	return nil, apperror.ErrFeedbackPeriodNotFound
}

func (f *fakeFeedbackPeriodRepo) ListByOrganization(ctx context.Context, organizationName string) ([]*model.FeedbackPeriod, error) {
	if f.listFn != nil {
		return f.listFn(ctx, organizationName)
	}
	if f.byID == nil {
		return []*model.FeedbackPeriod{}, nil
	}
	var out []*model.FeedbackPeriod
	for _, p := range f.byID {
		if p.OrganizationName == organizationName {
			out = append(out, p)
		}
	}
	return out, nil
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

func TestFeedbackPeriod_ListByOrganization(t *testing.T) {
	start1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end1 := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	start2 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end2 := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

	t.Run("returns periods for the named organization", func(t *testing.T) {
		svc, repo := newFeedbackPeriodTestService()
		// Seed two periods for "Acme" and one for "Other".
		acme1 := &model.FeedbackPeriod{ID: "p-1", Name: "H1", OrganizationName: "Acme", StartDate: start1, EndDate: end1}
		acme2 := &model.FeedbackPeriod{ID: "p-2", Name: "H2", OrganizationName: "Acme", StartDate: start2, EndDate: end2}
		other := &model.FeedbackPeriod{ID: "p-3", Name: "Other", OrganizationName: "Other", StartDate: start1, EndDate: end1}
		repo.byID = map[string]*model.FeedbackPeriod{
			acme1.ID: acme1, acme2.ID: acme2, other.ID: other,
		}

		got, err := svc.ListByOrganization(context.Background(), "Acme")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d periods, want 2", len(got))
		}
		for _, p := range got {
			if p.OrganizationName != "Acme" {
				t.Fatalf("got period with org %q, want Acme", p.OrganizationName)
			}
		}
	})

	t.Run("empty organization returns an empty slice", func(t *testing.T) {
		svc, _ := newFeedbackPeriodTestService()
		got, err := svc.ListByOrganization(context.Background(), "NoSuchOrg")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil slice, got nil")
		}
		if len(got) != 0 {
			t.Fatalf("got %d periods, want 0", len(got))
		}
	})

	t.Run("missing organization_name is rejected", func(t *testing.T) {
		svc, _ := newFeedbackPeriodTestService()
		_, err := svc.ListByOrganization(context.Background(), "")
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if !apperror.IsInvalidFeedbackPeriod(err) {
			t.Fatalf("expected ErrInvalidFeedbackPeriod, got %T: %v", err, err)
		}
		if err.Error() != "organization_name is required" {
			t.Fatalf("expected organization_name is required, got %q", err.Error())
		}
	})

	t.Run("repository error propagates", func(t *testing.T) {
		repo := &fakeFeedbackPeriodRepo{
			listFn: func(_ context.Context, _ string) ([]*model.FeedbackPeriod, error) {
				return nil, errors.New("firestore unavailable")
			},
		}
		svc := NewFeedbackPeriodService(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, err := svc.ListByOrganization(context.Background(), "Acme")
		if err == nil {
			t.Fatal("expected repository error to propagate, got nil")
		}
	})
}