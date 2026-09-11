package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
)

// fakeFeedbackRepo is an in-test stand-in for service.FeedbackRepository that
// records the feedback passed to Create without going through the real
// repository package (which would create an import cycle).
type fakeFeedbackRepo struct {
	created        *model.Feedback
	createFn       func(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error)
	listByReviewee func(ctx context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error)
	byReviewee     map[string][]*model.Feedback

	// Draft-support state: an in-memory store keyed by feedback ID plus
	// override hooks mirroring the real repository's draft methods.
	drafts        map[string]*model.Feedback
	claims        map[string]bool
	getFn         func(ctx context.Context, id string) (*model.Feedback, error)
	createDraftFn func(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error)
	updateDraftFn func(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error)
	submitDraftFn func(ctx context.Context, id string) (*model.Feedback, error)
	deleteDraftFn func(ctx context.Context, id string) error
	listDraftsFn  func(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
	listGivenFn   func(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
}

func (f *fakeFeedbackRepo) Get(_ context.Context, id string) (*model.Feedback, error) {
	if f.getFn != nil {
		return f.getFn(context.Background(), id)
	}
	if fb, ok := f.drafts[id]; ok {
		// Return a copy so a test that mutates the stored entry between the
		// service's read and write simulates a genuinely concurrent update,
		// the way a Firestore round trip would.
		copied := *fb
		return &copied, nil
	}
	return nil, apperror.ErrFeedbackNotFound
}

func (f *fakeFeedbackRepo) CreateDraft(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error) {
	if f.createDraftFn != nil {
		return f.createDraftFn(ctx, feedback)
	}
	if f.drafts == nil {
		f.drafts = map[string]*model.Feedback{}
	}
	if f.claims == nil {
		f.claims = map[string]bool{}
	}
	claim := feedback.ReviewerID + "_" + feedback.RevieweeID + "_" + feedback.PeriodID
	if f.claims[claim] {
		return nil, apperror.ErrFeedbackDraftAlreadyExists
	}
	f.claims[claim] = true
	stored := *feedback
	now := time.Now()
	stored.CreatedAt = now
	stored.UpdatedAt = now
	f.drafts[stored.ID] = &stored
	return &stored, nil
}

func (f *fakeFeedbackRepo) UpdateDraft(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error) {
	if f.updateDraftFn != nil {
		return f.updateDraftFn(ctx, feedback)
	}
	current, ok := f.drafts[feedback.ID]
	if !ok || current.NormalizedStatus() != model.FeedbackStatusDraft {
		return nil, apperror.ErrFeedbackNotFound
	}
	if !current.UpdatedAt.Equal(feedback.UpdatedAt) {
		return nil, apperror.ErrFeedbackConcurrentUpdate
	}
	updated := *feedback
	updated.Status = model.FeedbackStatusDraft
	updated.UpdatedAt = time.Now()
	f.drafts[feedback.ID] = &updated
	return &updated, nil
}

func (f *fakeFeedbackRepo) SubmitDraft(ctx context.Context, id string) (*model.Feedback, error) {
	if f.submitDraftFn != nil {
		return f.submitDraftFn(ctx, id)
	}
	current, ok := f.drafts[id]
	if !ok || current.NormalizedStatus() != model.FeedbackStatusDraft {
		return nil, apperror.ErrFeedbackNotFound
	}
	submitted := *current
	submitted.Status = model.FeedbackStatusSubmitted
	f.drafts[id] = &submitted
	claim := submitted.ReviewerID + "_" + submitted.RevieweeID + "_" + submitted.PeriodID
	delete(f.claims, claim)
	return &submitted, nil
}

func (f *fakeFeedbackRepo) DeleteDraft(ctx context.Context, id string) error {
	if f.deleteDraftFn != nil {
		return f.deleteDraftFn(ctx, id)
	}
	current, ok := f.drafts[id]
	if !ok || current.NormalizedStatus() != model.FeedbackStatusDraft {
		return apperror.ErrFeedbackNotFound
	}
	delete(f.drafts, id)
	claim := current.ReviewerID + "_" + current.RevieweeID + "_" + current.PeriodID
	delete(f.claims, claim)
	return nil
}

func (f *fakeFeedbackRepo) ListDraftsByReviewer(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if f.listDraftsFn != nil {
		return f.listDraftsFn(ctx, reviewerID, limit, cursorID)
	}
	var all []*model.Feedback
	for _, fb := range f.drafts {
		if fb.ReviewerID == reviewerID && fb.NormalizedStatus() == model.FeedbackStatusDraft {
			all = append(all, fb)
		}
	}
	// Newest first by CreatedAt, then by ID for stability.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	start := 0
	if strings.TrimSpace(cursorID) != "" {
		found := -1
		for i, fb := range all {
			if fb.ID == cursorID {
				found = i
				break
			}
		}
		if found == -1 {
			return nil, "", apperror.ErrFeedbackNotFound
		}
		start = found + 1
	}
	if limit <= 0 {
		limit = DefaultDraftListLimit
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	next := ""
	if end < len(all) && len(page) > 0 {
		next = page[len(page)-1].ID
	}
	return page, next, nil
}

// ListSubmittedByReviewer mirrors service.FeedbackRepository.
// ListSubmittedByReviewer. When listGivenFn is nil it serves from an
// in-memory slice (set via byReviewee below, filtered to submitted entries
// the reviewer wrote) so tests can exercise the happy path and pagination.
// Unknown cursor IDs return apperror.ErrFeedbackNotFound, matching the real
// Firestore repository.
func (f *fakeFeedbackRepo) ListSubmittedByReviewer(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if f.listGivenFn != nil {
		return f.listGivenFn(ctx, reviewerID, limit, cursorID)
	}
	var all []*model.Feedback
	for _, entries := range f.byReviewee {
		for _, fb := range entries {
			if fb.ReviewerID == reviewerID && fb.NormalizedStatus() == model.FeedbackStatusSubmitted {
				all = append(all, fb)
			}
		}
	}
	// Newest first by CreatedAt, then by ID for stability.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	start := 0
	if strings.TrimSpace(cursorID) != "" {
		found := -1
		for i, fb := range all {
			if fb.ID == cursorID {
				found = i
				break
			}
		}
		if found == -1 {
			return nil, "", apperror.ErrFeedbackNotFound
		}
		start = found + 1
	}
	if limit <= 0 {
		limit = DefaultFeedbackListLimit
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	next := ""
	if end < len(all) && len(page) > 0 {
		next = page[len(page)-1].ID
	}
	return page, next, nil
}

func (f *fakeFeedbackRepo) Create(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error) {
	if f.createFn != nil {
		return f.createFn(ctx, feedback)
	}
	f.created = feedback
	return feedback, nil
}

// ListByReviewee mirrors service.FeedbackRepository.ListByReviewee. When
// listByReviewee is nil it serves from an in-memory slice (set via byReviewee
// below) so tests can exercise the happy path and pagination without writing a
// custom function each time. Unknown cursor IDs return
// apperror.ErrFeedbackNotFound, matching the real Firestore repository.
func (f *fakeFeedbackRepo) ListByReviewee(_ context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if f.listByReviewee != nil {
		return f.listByReviewee(context.Background(), revieweeID, limit, cursorID)
	}
	all := f.byReviewee[revieweeID]
	// Find the cursor position; an unknown cursor mirrors the repo's
	// apperror.ErrFeedbackNotFound so the service test can exercise that path.
	start := 0
	if strings.TrimSpace(cursorID) != "" {
		found := -1
		for i, fb := range all {
			if fb.ID == cursorID {
				found = i
				break
			}
		}
		if found == -1 {
			return nil, "", apperror.ErrFeedbackNotFound
		}
		start = found + 1
	}
	if limit <= 0 {
		limit = DefaultFeedbackListLimit
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	if page == nil {
		page = []*model.Feedback{}
	}
	nextCursor := ""
	if end < len(all) {
		nextCursor = page[len(page)-1].ID
	}
	return page, nextCursor, nil
}

// fakePeriodLookup is an in-test stand-in for service.FeedbackPeriodLookup.
// By default it resolves any ID to a period whose date window is always open
// (a wide range around now); tests can override getFn to simulate a missing
// period, a repository failure, or a closed window.
type fakePeriodLookup struct {
	gotID string
	getFn func(ctx context.Context, id string) (*model.FeedbackPeriod, error)
}

func (f *fakePeriodLookup) GetByID(ctx context.Context, id string) (*model.FeedbackPeriod, error) {
	f.gotID = id
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return &model.FeedbackPeriod{ID: id, Name: "Test Period", StartDate: time.Now().Add(-24 * time.Hour), EndDate: time.Now().Add(24 * time.Hour)}, nil
}

// fakeEmployeeLookup is an in-test stand-in for service.EmployeeLookup. It
// serves GetByID from an in-memory map; a missing ID yields
// apperror.ErrEmployeeNotFound, matching the real employee repository.
type fakeEmployeeLookup struct {
	byID   map[string]*model.Employee
	gotIDs []string
}

func (f *fakeEmployeeLookup) GetByID(_ context.Context, id string) (*model.Employee, error) {
	f.gotIDs = append(f.gotIDs, id)
	if e, ok := f.byID[id]; ok {
		return e, nil
	}
	return nil, apperror.ErrEmployeeNotFound
}

// newFeedbackTestService builds a FeedbackService backed by a fake feedback
// repo, a fake period lookup (happy path: any period ID resolves), and a
// discarding logger. Returns the period lookup so tests can override its
// behavior or assert on which ID was looked up.
func newFeedbackTestService() (*FeedbackService, *fakeFeedbackRepo, *fakePeriodLookup) {
	repo := &fakeFeedbackRepo{}
	periods := &fakePeriodLookup{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewFeedbackService(repo, periods, nil, nil, logger), repo, periods
}

// validFeedbackInput returns a feedback with all required fields and valid
// scores, for use as a base in tests.
func validFeedbackInput() *model.Feedback {
	return &model.Feedback{
		PeriodID:           "period-1",
		RevieweeID:         "reviewee-1",
		CommunicationScore: 4,
		LeadershipScore:    5,
		TechnicalScore:     3,
		CollaborationScore: 4,
		DeliveryScore:      5,
		TrustScore:         2,
		StrengthsComment:   "great teammate",
		WeaknessesComment:  "could document more",
		Visibility:         model.FeedbackVisibilityAnonymous,
	}
}

func TestFeedback_Create(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		svc, repo, periods := newFeedbackTestService()
		feedback := validFeedbackInput()

		created, err := svc.Create(context.Background(), "reviewer-1", feedback)
		if err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}

		// The reviewer is taken from the caller (the JWT subject), not the
		// body, so a client cannot file feedback on someone else's behalf.
		if created.ReviewerID != "reviewer-1" {
			t.Fatalf("got reviewer_id %q, want reviewer-1 (from caller)", created.ReviewerID)
		}
		if created.RevieweeID != "reviewee-1" {
			t.Fatalf("got reviewee_id %q, want reviewee-1", created.RevieweeID)
		}
		if created.ID == "" {
			t.Fatal("expected a non-empty ID assigned by the service")
		}
		if repo.created == nil {
			t.Fatal("expected the repo to have received the feedback")
		}
		// The service must have looked up the period by ID before persisting.
		if periods.gotID != "period-1" {
			t.Fatalf("service looked up period %q, want period-1", periods.gotID)
		}
		if repo.created.ID != created.ID {
			t.Fatalf("repo received id %q, want %q", repo.created.ID, created.ID)
		}
	})

	t.Run("overrides client reviewer_id", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		// Client sets a different reviewer; the caller's must win.
		feedback := validFeedbackInput()
		feedback.ReviewerID = "should-be-ignored"
		created, err := svc.Create(context.Background(), "reviewer-1", feedback)
		if err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
		if created.ReviewerID != "reviewer-1" {
			t.Fatalf("got reviewer_id %q, want reviewer-1 (caller wins)", created.ReviewerID)
		}
	})

	t.Run("empty visibility defaults to anonymous", func(t *testing.T) {
		svc, repo, _ := newFeedbackTestService()
		feedback := validFeedbackInput()
		feedback.Visibility = ""

		_, err := svc.Create(context.Background(), "reviewer-1", feedback)
		if err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
		if repo.created.Visibility != model.FeedbackVisibilityAnonymous {
			t.Fatalf("got visibility %q, want anonymous", repo.created.Visibility)
		}
	})

	t.Run("visibility anonymous honored", func(t *testing.T) {
		svc, repo, _ := newFeedbackTestService()
		feedback := validFeedbackInput()
		feedback.Visibility = model.FeedbackVisibilityAnonymous

		_, err := svc.Create(context.Background(), "reviewer-1", feedback)
		if err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
		if repo.created.Visibility != model.FeedbackVisibilityAnonymous {
			t.Fatalf("got visibility %q, want anonymous", repo.created.Visibility)
		}
	})

	t.Run("visibility named honored", func(t *testing.T) {
		svc, repo, _ := newFeedbackTestService()
		feedback := validFeedbackInput()
		feedback.Visibility = model.FeedbackVisibilityNamed

		_, err := svc.Create(context.Background(), "reviewer-1", feedback)
		if err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
		if repo.created.Visibility != model.FeedbackVisibilityNamed {
			t.Fatalf("got visibility %q, want named", repo.created.Visibility)
		}
	})

	t.Run("self-review rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		feedback := validFeedbackInput()
		feedback.RevieweeID = "reviewer-1"

		_, err := svc.Create(context.Background(), "reviewer-1", feedback)
		if err == nil {
			t.Fatal("expected error for self-review, got nil")
		}
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback, got %T: %v", err, err)
		}
		if err.Error() != "reviewer cannot review themselves" {
			t.Fatalf("expected self-review message, got %q", err.Error())
		}
	})

	t.Run("repository error propagates", func(t *testing.T) {
		repo := &fakeFeedbackRepo{
			createFn: func(_ context.Context, _ *model.Feedback) (*model.Feedback, error) {
				return nil, errors.New("db down")
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput())
		if err == nil {
			t.Fatal("expected repository error to propagate, got nil")
		}
	})

	t.Run("period not found is rejected", func(t *testing.T) {
		svc, _, periods := newFeedbackTestService()
		periods.getFn = func(_ context.Context, _ string) (*model.FeedbackPeriod, error) {
			return nil, apperror.ErrFeedbackPeriodNotFound
		}
		_, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput())
		if err == nil {
			t.Fatal("expected error for unknown period, got nil")
		}
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback, got %T: %v", err, err)
		}
		if err.Error() != "period_id does not refer to an existing feedback period" {
			t.Fatalf("expected period-not-found message, got %q", err.Error())
		}
	})

	t.Run("period lookup error propagates", func(t *testing.T) {
		svc, _, periods := newFeedbackTestService()
		periods.getFn = func(_ context.Context, _ string) (*model.FeedbackPeriod, error) {
			return nil, errors.New("firestore unavailable")
		}
		_, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput())
		if err == nil {
			t.Fatal("expected period lookup error to propagate, got nil")
		}
		// Non-not-found errors should surface as a wrapped error, not an
		// ErrInvalidFeedback, so the handler maps them to 500 rather than 400.
		if apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected a non-validation error, got ErrInvalidFeedback: %v", err)
		}
	})
}

func TestFeedback_Create_ValidationErrors(t *testing.T) {
	cases := []struct {
		name       string
		reviewerID string
		feedback   *model.Feedback
		wantMsg    string
	}{
		{
			name:       "nil feedback",
			reviewerID: "reviewer-1",
			feedback:   nil,
			wantMsg:    "feedback must not be nil",
		},
		{
			name:       "missing reviewer_id",
			reviewerID: "",
			feedback:   validFeedbackInput(),
			wantMsg:    "reviewer_id is required",
		},
		{
			name:       "blank reviewer_id",
			reviewerID: "   ",
			feedback:   validFeedbackInput(),
			wantMsg:    "reviewer_id is required",
		},
		{
			name:       "missing period_id",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{RevieweeID: "reviewee-1", CommunicationScore: 1, LeadershipScore: 1, TechnicalScore: 1, CollaborationScore: 1, DeliveryScore: 1, TrustScore: 1},
			wantMsg:    "period_id is required",
		},
		{
			name:       "missing reviewee_id",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", CommunicationScore: 1, LeadershipScore: 1, TechnicalScore: 1, CollaborationScore: 1, DeliveryScore: 1, TrustScore: 1},
			wantMsg:    "reviewee_id is required",
		},
		{
			name:       "communication_score below range",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", RevieweeID: "reviewee-1", CommunicationScore: 0, LeadershipScore: 1, TechnicalScore: 1, CollaborationScore: 1, DeliveryScore: 1, TrustScore: 1},
			wantMsg:    "communication_score must be between 1 and 5",
		},
		{
			name:       "communication_score above range",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", RevieweeID: "reviewee-1", CommunicationScore: 6, LeadershipScore: 1, TechnicalScore: 1, CollaborationScore: 1, DeliveryScore: 1, TrustScore: 1},
			wantMsg:    "communication_score must be between 1 and 5",
		},
		{
			name:       "leadership_score out of range",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", RevieweeID: "reviewee-1", CommunicationScore: 1, LeadershipScore: 7, TechnicalScore: 1, CollaborationScore: 1, DeliveryScore: 1, TrustScore: 1},
			wantMsg:    "leadership_score must be between 1 and 5",
		},
		{
			name:       "technical_score out of range",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", RevieweeID: "reviewee-1", CommunicationScore: 1, LeadershipScore: 1, TechnicalScore: 0, CollaborationScore: 1, DeliveryScore: 1, TrustScore: 1},
			wantMsg:    "technical_score must be between 1 and 5",
		},
		{
			name:       "collaboration_score out of range",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", RevieweeID: "reviewee-1", CommunicationScore: 1, LeadershipScore: 1, TechnicalScore: 1, CollaborationScore: 9, DeliveryScore: 1, TrustScore: 1},
			wantMsg:    "collaboration_score must be between 1 and 5",
		},
		{
			name:       "delivery_score out of range",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", RevieweeID: "reviewee-1", CommunicationScore: 1, LeadershipScore: 1, TechnicalScore: 1, CollaborationScore: 1, DeliveryScore: -1, TrustScore: 1},
			wantMsg:    "delivery_score must be between 1 and 5",
		},
		{
			name:       "trust_score out of range",
			reviewerID: "reviewer-1",
			feedback:   &model.Feedback{PeriodID: "period-1", RevieweeID: "reviewee-1", CommunicationScore: 1, LeadershipScore: 1, TechnicalScore: 1, CollaborationScore: 1, DeliveryScore: 1, TrustScore: 99},
			wantMsg:    "trust_score must be between 1 and 5",
		},
		{
			name:       "missing strengths_comment",
			reviewerID: "reviewer-1",
			feedback: func() *model.Feedback {
				f := validFeedbackInput()
				f.StrengthsComment = "  "
				return f
			}(),
			wantMsg: "strengths_comment is required",
		},
		{
			name:       "missing weaknesses_comment",
			reviewerID: "reviewer-1",
			feedback: func() *model.Feedback {
				f := validFeedbackInput()
				f.WeaknessesComment = ""
				return f
			}(),
			wantMsg: "weaknesses_comment is required",
		},
		{
			name:       "invalid visibility value",
			reviewerID: "reviewer-1",
			feedback: func() *model.Feedback {
				f := validFeedbackInput()
				f.Visibility = "secret"
				return f
			}(),
			wantMsg: "visibility must be one of anonymous, named",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newFeedbackTestService()
			_, err := svc.Create(context.Background(), tc.reviewerID, tc.feedback)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !apperror.IsInvalidFeedback(err) {
				t.Fatalf("expected ErrInvalidFeedback, got %T: %v", err, err)
			}
			if err.Error() != tc.wantMsg {
				t.Fatalf("expected message %q, got %q", tc.wantMsg, err.Error())
			}
		})
	}
}

func TestFeedback_ListByReviewee(t *testing.T) {
	// buildFeedbacks returns n feedback entries with stable IDs fb-1..fb-n and
	// strictly increasing created_at, so the service's created_at-desc ordering
	// is the reverse of insertion. The cursor semantics below rely on this.
	buildFeedbacks := func(n int) []*model.Feedback {
		out := make([]*model.Feedback, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, &model.Feedback{
				ID:         "fb-" + itoa(i),
				RevieweeID: "reviewee-1",
				PeriodID:   "period-1",
				CreatedAt:  time.Unix(int64(i), 0).UTC(),
			})
		}
		return out
	}

	t.Run("success returns feedback for the given reviewee", func(t *testing.T) {
		repo := &fakeFeedbackRepo{
			byReviewee: map[string][]*model.Feedback{
				"reviewee-1": buildFeedbacks(2),
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		got, nextCursor, err := svc.ListByReviewee(context.Background(), "reviewee-1", 0, "")
		if err != nil {
			t.Fatalf("ListByReviewee: unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d feedbacks, want 2", len(got))
		}
		if nextCursor != "" {
			t.Fatalf("expected empty nextCursor, got %q", nextCursor)
		}
	})

	t.Run("passes reviewee id, limit, and cursor to the repository", func(t *testing.T) {
		var gotRevieweeID string
		var gotLimit int
		var gotCursor string
		repo := &fakeFeedbackRepo{
			listByReviewee: func(_ context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
				gotRevieweeID = revieweeID
				gotLimit = limit
				gotCursor = cursorID
				return []*model.Feedback{}, "", nil
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		if _, _, err := svc.ListByReviewee(context.Background(), "reviewee-1", 5, "fb-3"); err != nil {
			t.Fatalf("ListByReviewee: unexpected error: %v", err)
		}
		if gotRevieweeID != "reviewee-1" {
			t.Fatalf("repo received reviewee_id %q, want reviewee-1", gotRevieweeID)
		}
		if gotLimit != 5 {
			t.Fatalf("repo received limit %d, want 5", gotLimit)
		}
		if gotCursor != "fb-3" {
			t.Fatalf("repo received cursor %q, want fb-3", gotCursor)
		}
	})

	t.Run("default limit is applied when limit <= 0", func(t *testing.T) {
		var gotLimit int
		repo := &fakeFeedbackRepo{
			listByReviewee: func(_ context.Context, _ string, limit int, _ string) ([]*model.Feedback, string, error) {
				gotLimit = limit
				return []*model.Feedback{}, "", nil
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if _, _, err := svc.ListByReviewee(context.Background(), "reviewee-1", 0, ""); err != nil {
			t.Fatalf("ListByReviewee: unexpected error: %v", err)
		}
		if gotLimit != DefaultFeedbackListLimit {
			t.Fatalf("got limit %d, want default %d", gotLimit, DefaultFeedbackListLimit)
		}
	})

	t.Run("limit is capped at MaxFeedbackListLimit", func(t *testing.T) {
		var gotLimit int
		repo := &fakeFeedbackRepo{
			listByReviewee: func(_ context.Context, _ string, limit int, _ string) ([]*model.Feedback, string, error) {
				gotLimit = limit
				return []*model.Feedback{}, "", nil
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if _, _, err := svc.ListByReviewee(context.Background(), "reviewee-1", 9999, ""); err != nil {
			t.Fatalf("ListByReviewee: unexpected error: %v", err)
		}
		if gotLimit != MaxFeedbackListLimit {
			t.Fatalf("got limit %d, want max %d", gotLimit, MaxFeedbackListLimit)
		}
	})

	t.Run("pagination returns next cursor when more pages exist", func(t *testing.T) {
		// 5 entries, page size 2: page 1 returns fb-2..fb-1 (newest first),
		// nextCursor = last ID on the page; page 2 starts after that cursor.
		repo := &fakeFeedbackRepo{
			byReviewee: map[string][]*model.Feedback{
				"reviewee-1": buildFeedbacks(5),
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		page1, cursor1, err := svc.ListByReviewee(context.Background(), "reviewee-1", 2, "")
		if err != nil {
			t.Fatalf("page 1: unexpected error: %v", err)
		}
		if len(page1) != 2 {
			t.Fatalf("page 1: got %d feedbacks, want 2", len(page1))
		}
		if cursor1 == "" {
			t.Fatal("page 1: expected non-empty nextCursor, got empty")
		}

		page2, cursor2, err := svc.ListByReviewee(context.Background(), "reviewee-1", 2, cursor1)
		if err != nil {
			t.Fatalf("page 2: unexpected error: %v", err)
		}
		if len(page2) != 2 {
			t.Fatalf("page 2: got %d feedbacks, want 2", len(page2))
		}
		// page 2's first item must not duplicate page 1's last item.
		if page2[0].ID == page1[1].ID {
			t.Fatalf("page 2 repeats id %q from page 1", page1[1].ID)
		}
		// Final page (3 entries left, page size 2) should return 1 item and
		// an empty cursor signalling the end.
		page3, cursor3, err := svc.ListByReviewee(context.Background(), "reviewee-1", 2, cursor2)
		if err != nil {
			t.Fatalf("page 3: unexpected error: %v", err)
		}
		if len(page3) != 1 {
			t.Fatalf("page 3: got %d feedbacks, want 1", len(page3))
		}
		if cursor3 != "" {
			t.Fatalf("page 3: expected empty nextCursor, got %q", cursor3)
		}
	})

	t.Run("empty reviewee id is rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		_, _, err := svc.ListByReviewee(context.Background(), "  ", 0, "")
		if err == nil {
			t.Fatal("expected error for empty reviewee id, got nil")
		}
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback, got %T: %v", err, err)
		}
		if err.Error() != "reviewee_id is required" {
			t.Fatalf("expected message %q, got %q", "reviewee_id is required", err.Error())
		}
	})

	t.Run("unknown cursor propagates ErrFeedbackNotFound", func(t *testing.T) {
		repo := &fakeFeedbackRepo{
			byReviewee: map[string][]*model.Feedback{
				"reviewee-1": buildFeedbacks(1),
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, _, err := svc.ListByReviewee(context.Background(), "reviewee-1", 10, "does-not-exist")
		if err == nil {
			t.Fatal("expected error for unknown cursor, got nil")
		}
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound, got %T: %v", err, err)
		}
	})

	t.Run("repository error propagates", func(t *testing.T) {
		repo := &fakeFeedbackRepo{
			listByReviewee: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", errors.New("db down")
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, _, err := svc.ListByReviewee(context.Background(), "reviewee-1", 10, "")
		if err == nil {
			t.Fatal("expected repository error to propagate, got nil")
		}
		// Non-not-found errors should surface as a wrapped error, not an
		// ErrInvalidFeedback, so the handler maps them to 500 rather than 400.
		if apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected a non-validation error, got ErrInvalidFeedback: %v", err)
		}
	})
}

// itoa is a tiny strconv-free itoa used only inside test helpers to build
// feedback IDs. Using strconv.Itoa here would pull in an extra import for one
// helper; this keeps the test file self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// newManagerTestService builds a FeedbackService wired with a fake employee
// lookup seeded with a manager/reviewee pair, plus a feedback repo seeded
// with two entries for the reviewee (one anonymous, one named). It returns
// the service and the seeded feedback for assertions.
func newManagerTestService(t *testing.T) (*FeedbackService, *fakeFeedbackRepo) {
	t.Helper()
	managerID := "manager-1"
	revieweeID := "reviewee-1"
	employees := &fakeEmployeeLookup{byID: map[string]*model.Employee{
		revieweeID: {ID: revieweeID, Name: "Carol", ManagerID: &managerID},
		"orphan-1": {ID: "orphan-1", Name: "NoManager"},
	}}

	repo := &fakeFeedbackRepo{byReviewee: map[string][]*model.Feedback{
		revieweeID: {
			{ID: "fb-1", RevieweeID: revieweeID, ReviewerID: "reviewer-9", StrengthsComment: "s", WeaknessesComment: "w", Visibility: model.FeedbackVisibilityNamed},
			{ID: "fb-2", RevieweeID: revieweeID, ReviewerID: "reviewer-7", StrengthsComment: "s", WeaknessesComment: "w", Visibility: model.FeedbackVisibilityAnonymous},
		},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewFeedbackService(repo, &fakePeriodLookup{}, employees, nil, logger), repo
}

func TestFeedback_ListByRevieweeForManager(t *testing.T) {
	t.Run("manager sees reviewee feedback", func(t *testing.T) {
		svc, _ := newManagerTestService(t)

		got, nextCursor, err := svc.ListByRevieweeForManager(context.Background(), "manager-1", "reviewee-1", 0, "")
		if err != nil {
			t.Fatalf("ListByRevieweeForManager: %v", err)
		}
		if len(got) != 2 || nextCursor != "" {
			t.Fatalf("expected 2 feedbacks and no cursor, got %d and %q", len(got), nextCursor)
		}
	})

	t.Run("non-manager caller is forbidden", func(t *testing.T) {
		svc, _ := newManagerTestService(t)

		_, _, err := svc.ListByRevieweeForManager(context.Background(), "someone-else", "reviewee-1", 0, "")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("expected ErrForbidden for non-manager caller, got %v", err)
		}
	})

	t.Run("reviewee without manager is forbidden to anyone", func(t *testing.T) {
		svc, _ := newManagerTestService(t)

		_, _, err := svc.ListByRevieweeForManager(context.Background(), "manager-1", "orphan-1", 0, "")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("expected ErrForbidden for manager-less reviewee, got %v", err)
		}
	})

	t.Run("unknown reviewee is not found", func(t *testing.T) {
		svc, _ := newManagerTestService(t)

		_, _, err := svc.ListByRevieweeForManager(context.Background(), "manager-1", "ghost-1", 0, "")
		if !errors.Is(err, apperror.ErrEmployeeNotFound) {
			t.Fatalf("expected ErrEmployeeNotFound for unknown reviewee, got %v", err)
		}
	})

	t.Run("fails closed without employee lookup", func(t *testing.T) {
		repo := &fakeFeedbackRepo{}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		_, _, err := svc.ListByRevieweeForManager(context.Background(), "manager-1", "reviewee-1", 0, "")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("expected ErrForbidden when employee lookup is nil, got %v", err)
		}
	})

	t.Run("cursor semantics delegate to ListByReviewee", func(t *testing.T) {
		svc, _ := newManagerTestService(t)

		_, _, err := svc.ListByRevieweeForManager(context.Background(), "manager-1", "reviewee-1", 0, "ghost-cursor")
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound for unknown cursor, got %v", err)
		}
	})

	t.Run("missing caller id is invalid", func(t *testing.T) {
		svc, _ := newManagerTestService(t)

		_, _, err := svc.ListByRevieweeForManager(context.Background(), "", "reviewee-1", 0, "")
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback for missing caller, got %v", err)
		}
	})

	t.Run("missing reviewee id is invalid", func(t *testing.T) {
		svc, _ := newManagerTestService(t)

		_, _, err := svc.ListByRevieweeForManager(context.Background(), "manager-1", "", 0, "")
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback for missing reviewee, got %v", err)
		}
	})
}

// draftInput returns a minimal draft input: only the required reviewee and
// period set, everything else empty — the "start a draft" shape.
func draftInput() *model.Feedback {
	return &model.Feedback{
		PeriodID:   "period-1",
		RevieweeID: "reviewee-1",
	}
}

// completeDraft returns a draft input with all submit-required fields filled.
func completeDraft() *model.Feedback {
	return &model.Feedback{
		PeriodID:           "period-1",
		RevieweeID:         "reviewee-1",
		CommunicationScore: 4,
		LeadershipScore:    5,
		TechnicalScore:     3,
		CollaborationScore: 4,
		DeliveryScore:      5,
		TrustScore:         2,
		StrengthsComment:   "great teammate",
		WeaknessesComment:  "could document more",
		Visibility:         model.FeedbackVisibilityAnonymous,
		Status:             model.FeedbackStatusDraft,
	}
}

func TestFeedback_CreateDraft(t *testing.T) {
	t.Run("success with minimal input", func(t *testing.T) {
		svc, repo, periods := newFeedbackTestService()

		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: unexpected error: %v", err)
		}
		if created.ReviewerID != "reviewer-1" {
			t.Fatalf("got reviewer_id %q, want reviewer-1 (from caller)", created.ReviewerID)
		}
		if created.Status != model.FeedbackStatusDraft {
			t.Fatalf("got status %q, want draft", created.Status)
		}
		if created.ID == "" {
			t.Fatal("expected a non-empty ID assigned by the service")
		}
		if repo.created != nil && repo.drafts[created.ID] == nil {
			t.Fatal("expected the draft to be stored via CreateDraft")
		}
		if periods.gotID != "period-1" {
			t.Fatalf("service looked up period %q, want period-1", periods.gotID)
		}
	})

	t.Run("partial scores are accepted but bounds-checked", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		d := draftInput()
		d.CommunicationScore = 3
		d.TrustScore = 5

		if _, err := svc.CreateDraft(context.Background(), "reviewer-1", d); err != nil {
			t.Fatalf("CreateDraft: unexpected error: %v", err)
		}

		svc2, _, _ := newFeedbackTestService()
		bad := draftInput()
		bad.CommunicationScore = 6
		_, err := svc2.CreateDraft(context.Background(), "reviewer-1", bad)
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback for out-of-range score, got %v", err)
		}
		if err.Error() != "communication_score must be between 1 and 5" {
			t.Fatalf("expected score range message, got %q", err.Error())
		}
	})

	t.Run("self-review rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		d := draftInput()
		d.RevieweeID = "reviewer-1"

		_, err := svc.CreateDraft(context.Background(), "reviewer-1", d)
		if err == nil || err.Error() != "reviewer cannot review themselves" {
			t.Fatalf("expected self-review rejection, got %v", err)
		}
	})

	t.Run("missing period and reviewee rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		if _, err := svc.CreateDraft(context.Background(), "reviewer-1", &model.Feedback{RevieweeID: "r"}); err == nil || err.Error() != "period_id is required" {
			t.Fatalf("expected period_id rejection, got %v", err)
		}
		svc2, _, _ := newFeedbackTestService()
		if _, err := svc2.CreateDraft(context.Background(), "reviewer-1", &model.Feedback{PeriodID: "p"}); err == nil || err.Error() != "reviewee_id is required" {
			t.Fatalf("expected reviewee_id rejection, got %v", err)
		}
	})

	t.Run("duplicate draft for the same pair rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		if _, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput()); err != nil {
			t.Fatalf("first CreateDraft: %v", err)
		}
		_, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if !errors.Is(err, apperror.ErrFeedbackDraftAlreadyExists) {
			t.Fatalf("expected ErrFeedbackDraftAlreadyExists, got %v", err)
		}
	})

	t.Run("closed period window does not block drafting", func(t *testing.T) {
		// A draft may be started before the period opens; only submission is
		// window-gated.
		periods := &fakePeriodLookup{}
		periods.getFn = func(_ context.Context, id string) (*model.FeedbackPeriod, error) {
			return &model.FeedbackPeriod{ID: id, StartDate: time.Now().Add(24 * time.Hour), EndDate: time.Now().Add(48 * time.Hour)}, nil
		}
		repo := &fakeFeedbackRepo{}
		svc := NewFeedbackService(repo, periods, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		if _, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput()); err != nil {
			t.Fatalf("CreateDraft during closed window: unexpected error: %v", err)
		}
	})

	t.Run("visibility normalized to anonymous", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		d := draftInput()
		d.Visibility = ""

		created, err := svc.CreateDraft(context.Background(), "reviewer-1", d)
		if err != nil {
			t.Fatalf("CreateDraft: unexpected error: %v", err)
		}
		if created.Visibility != model.FeedbackVisibilityAnonymous {
			t.Fatalf("got visibility %q, want anonymous", created.Visibility)
		}
	})
}

func TestFeedback_GetDraft(t *testing.T) {
	t.Run("author can fetch own draft", func(t *testing.T) {
		svc, repo, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		got, err := svc.GetDraft(context.Background(), "reviewer-1", created.ID)
		if err != nil {
			t.Fatalf("GetDraft: %v", err)
		}
		if got.ID != created.ID {
			t.Fatalf("got draft %q, want %q", got.ID, created.ID)
		}
		_ = repo
	})

	t.Run("non-author gets not-found, not forbidden", func(t *testing.T) {
		// A draft's existence must not leak: another caller sees the same
		// 404 as for a missing draft.
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		_, err = svc.GetDraft(context.Background(), "reviewer-2", created.ID)
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound for non-author, got %v", err)
		}
	})

	t.Run("submitted entry is not a draft", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}
		if _, err := svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil); err != nil {
			t.Fatalf("SubmitDraft: %v", err)
		}

		_, err = svc.GetDraft(context.Background(), "reviewer-1", created.ID)
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound for submitted entry, got %v", err)
		}
	})
}

func TestFeedback_UpdateDraft(t *testing.T) {
	t.Run("partial update leaves omitted fields untouched", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", func() *model.Feedback {
			d := draftInput()
			d.StrengthsComment = "original"
			d.CommunicationScore = 2
			return d
		}())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		four := 4
		updated, err := svc.UpdateDraft(context.Background(), "reviewer-1", created.ID, func(f *model.Feedback) error {
			f.CommunicationScore = four
			return nil
		})
		if err != nil {
			t.Fatalf("UpdateDraft: %v", err)
		}
		if updated.CommunicationScore != 4 {
			t.Fatalf("got communication_score %d, want 4", updated.CommunicationScore)
		}
		if updated.StrengthsComment != "original" {
			t.Fatalf("omitted strengths_comment changed: got %q", updated.StrengthsComment)
		}
	})

	t.Run("out-of-range score rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		_, err = svc.UpdateDraft(context.Background(), "reviewer-1", created.ID, func(f *model.Feedback) error {
			f.TrustScore = 9
			return nil
		})
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback, got %v", err)
		}
	})

	t.Run("non-author gets not-found", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		_, err = svc.UpdateDraft(context.Background(), "reviewer-2", created.ID, func(f *model.Feedback) error { return nil })
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound, got %v", err)
		}
	})

	t.Run("concurrent write between read and update is rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		// Simulate another device's edit landing between this request's
		// read (GetDraft inside UpdateDraft) and its write: the hook bumps the
		// stored UpdatedAt just before the repository compares them, exactly
		// where a real Firestore transaction would detect the race.
		repo := svc.repo.(*fakeFeedbackRepo)
		injected := repo.updateDraftFn
		repo.updateDraftFn = func(ctx context.Context, feedback *model.Feedback) (*model.Feedback, error) {
			stored := repo.drafts[feedback.ID]
			stored.UpdatedAt = stored.UpdatedAt.Add(time.Second)
			if injected != nil {
				return injected(ctx, feedback)
			}
			// Re-run the default comparison with the bumped timestamp.
			current := repo.drafts[feedback.ID]
			if current.NormalizedStatus() != model.FeedbackStatusDraft {
				return nil, apperror.ErrFeedbackNotFound
			}
			if !current.UpdatedAt.Equal(feedback.UpdatedAt) {
				return nil, apperror.ErrFeedbackConcurrentUpdate
			}
			updated := *feedback
			repo.drafts[feedback.ID] = &updated
			return &updated, nil
		}

		_, err = svc.UpdateDraft(context.Background(), "reviewer-1", created.ID, func(f *model.Feedback) error { return nil })
		if !errors.Is(err, apperror.ErrFeedbackConcurrentUpdate) {
			t.Fatalf("expected ErrFeedbackConcurrentUpdate, got %v", err)
		}
	})
}

func TestFeedback_SubmitDraft(t *testing.T) {
	t.Run("complete draft submits successfully", func(t *testing.T) {
		svc, repo, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		submitted, err := svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil)
		if err != nil {
			t.Fatalf("SubmitDraft: %v", err)
		}
		if submitted.Status != model.FeedbackStatusSubmitted {
			t.Fatalf("got status %q, want submitted", submitted.Status)
		}
		// The claim is released so a new draft for the same pair is allowed.
		claim := "reviewer-1_reviewee-1_period-1"
		if repo.claims[claim] {
			t.Fatal("expected the draft claim to be released on submit")
		}
	})

	t.Run("final edits applied via apply func", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		submitted, err := svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, func(f *model.Feedback) error {
			f.CommunicationScore = 4
			f.LeadershipScore = 5
			f.TechnicalScore = 3
			f.CollaborationScore = 4
			f.DeliveryScore = 5
			f.TrustScore = 2
			f.StrengthsComment = "great"
			f.WeaknessesComment = "docs"
			return nil
		})
		if err != nil {
			t.Fatalf("SubmitDraft: %v", err)
		}
		if submitted.StrengthsComment != "great" {
			t.Fatalf("final edit not applied: got strengths_comment %q", submitted.StrengthsComment)
		}
	})

	t.Run("incomplete draft rejected at submit", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		_, err = svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil)
		if !apperror.IsInvalidFeedback(err) {
			t.Fatalf("expected ErrInvalidFeedback for incomplete draft, got %v", err)
		}
		if err.Error() != "communication_score must be between 1 and 5" {
			t.Fatalf("expected missing-score message, got %q", err.Error())
		}
	})

	t.Run("closed period window rejected", func(t *testing.T) {
		periods := &fakePeriodLookup{}
		periods.getFn = func(_ context.Context, id string) (*model.FeedbackPeriod, error) {
			return &model.FeedbackPeriod{ID: id, StartDate: time.Now().Add(24 * time.Hour), EndDate: time.Now().Add(48 * time.Hour)}, nil
		}
		repo := &fakeFeedbackRepo{}
		svc := NewFeedbackService(repo, periods, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}
		_, err = svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil)
		if !errors.Is(err, apperror.ErrFeedbackPeriodClosed) {
			t.Fatalf("expected ErrFeedbackPeriodClosed, got %v", err)
		}
	})

	t.Run("expired period window rejected", func(t *testing.T) {
		periods := &fakePeriodLookup{}
		periods.getFn = func(_ context.Context, id string) (*model.FeedbackPeriod, error) {
			return &model.FeedbackPeriod{ID: id, StartDate: time.Now().Add(-48 * time.Hour), EndDate: time.Now().Add(-24 * time.Hour)}, nil
		}
		repo := &fakeFeedbackRepo{}
		svc := NewFeedbackService(repo, periods, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}
		_, err = svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil)
		if !errors.Is(err, apperror.ErrFeedbackPeriodClosed) {
			t.Fatalf("expected ErrFeedbackPeriodClosed, got %v", err)
		}
	})

	t.Run("double submit rejected", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}
		if _, err := svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil); err != nil {
			t.Fatalf("first SubmitDraft: %v", err)
		}

		_, err = svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil)
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound on double submit, got %v", err)
		}
	})

	t.Run("non-author gets not-found", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		_, err = svc.SubmitDraft(context.Background(), "reviewer-2", created.ID, nil)
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound for non-author, got %v", err)
		}
	})
}

func TestFeedback_DeleteDraft(t *testing.T) {
	t.Run("delete releases the claim", func(t *testing.T) {
		svc, repo, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		if err := svc.DeleteDraft(context.Background(), "reviewer-1", created.ID); err != nil {
			t.Fatalf("DeleteDraft: %v", err)
		}
		claim := "reviewer-1_reviewee-1_period-1"
		if repo.claims[claim] {
			t.Fatal("expected the draft claim to be released on delete")
		}
		// The same pair may draft again immediately.
		if _, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput()); err != nil {
			t.Fatalf("re-draft after delete: %v", err)
		}
	})

	t.Run("non-author gets not-found", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", draftInput())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}

		if err := svc.DeleteDraft(context.Background(), "reviewer-2", created.ID); !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound for non-author, got %v", err)
		}
	})

	t.Run("submitted entry cannot be deleted", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}
		if _, err := svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil); err != nil {
			t.Fatalf("SubmitDraft: %v", err)
		}

		if err := svc.DeleteDraft(context.Background(), "reviewer-1", created.ID); !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound for submitted entry, got %v", err)
		}
	})
}

func TestFeedback_ListMyDrafts(t *testing.T) {
	buildDrafts := func(n int) []*model.Feedback {
		out := make([]*model.Feedback, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, &model.Feedback{
				ID:         "draft-" + itoa(i),
				ReviewerID: "reviewer-1",
				PeriodID:   "period-1",
				Status:     model.FeedbackStatusDraft,
				CreatedAt:  time.Unix(int64(i), 0).UTC(),
			})
		}
		return out
	}

	newService := func(seed []*model.Feedback) *FeedbackService {
		repo := &fakeFeedbackRepo{drafts: map[string]*model.Feedback{}, claims: map[string]bool{}}
		for _, d := range seed {
			stored := *d
			repo.drafts[d.ID] = &stored
		}
		return NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	t.Run("returns only the caller's drafts, newest first", func(t *testing.T) {
		svc := newService(append(buildDrafts(3), &model.Feedback{ID: "other", ReviewerID: "reviewer-2", Status: model.FeedbackStatusDraft, CreatedAt: time.Unix(1, 0).UTC()}))

		got, next, err := svc.ListMyDrafts(context.Background(), "reviewer-1", 0, "")
		if err != nil {
			t.Fatalf("ListMyDrafts: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d drafts, want 3", len(got))
		}
		if got[0].ID != "draft-3" {
			t.Fatalf("expected newest first, got %q first", got[0].ID)
		}
		if next != "" {
			t.Fatalf("expected empty cursor, got %q", next)
		}
	})

	t.Run("submitted entries are not listed as drafts", func(t *testing.T) {
		svc := newService([]*model.Feedback{{ID: "sub", ReviewerID: "reviewer-1", Status: model.FeedbackStatusSubmitted, CreatedAt: time.Unix(1, 0).UTC()}})

		got, _, err := svc.ListMyDrafts(context.Background(), "reviewer-1", 0, "")
		if err != nil {
			t.Fatalf("ListMyDrafts: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d drafts, want 0", len(got))
		}
	})

	t.Run("limit and cursor flow to the repository", func(t *testing.T) {
		repo := &fakeFeedbackRepo{
			listDraftsFn: func(_ context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
				if reviewerID != "reviewer-1" || limit != 5 || cursorID != "draft-2" {
					t.Fatalf("unexpected repo args: %q %d %q", reviewerID, limit, cursorID)
				}
				return []*model.Feedback{}, "", nil
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		if _, _, err := svc.ListMyDrafts(context.Background(), "reviewer-1", 5, "draft-2"); err != nil {
			t.Fatalf("ListMyDrafts: %v", err)
		}
	})

	t.Run("default and max limits", func(t *testing.T) {
		var gotLimit int
		repo := &fakeFeedbackRepo{
			listDraftsFn: func(_ context.Context, _ string, limit int, _ string) ([]*model.Feedback, string, error) {
				gotLimit = limit
				return []*model.Feedback{}, "", nil
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		if _, _, err := svc.ListMyDrafts(context.Background(), "reviewer-1", 0, ""); err != nil {
			t.Fatalf("ListMyDrafts: %v", err)
		}
		if gotLimit != DefaultDraftListLimit {
			t.Fatalf("got default limit %d, want %d", gotLimit, DefaultDraftListLimit)
		}

		if _, _, err := svc.ListMyDrafts(context.Background(), "reviewer-1", 9999, ""); err != nil {
			t.Fatalf("ListMyDrafts: %v", err)
		}
		if gotLimit != MaxDraftListLimit {
			t.Fatalf("got capped limit %d, want %d", gotLimit, MaxDraftListLimit)
		}
	})

	t.Run("unknown cursor propagates ErrFeedbackNotFound", func(t *testing.T) {
		svc := newService(buildDrafts(1))

		_, _, err := svc.ListMyDrafts(context.Background(), "reviewer-1", 10, "does-not-exist")
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound, got %v", err)
		}
	})
}

func TestFeedback_ListMyGivenFeedbacks(t *testing.T) {
	buildGiven := func(n int) []*model.Feedback {
		out := make([]*model.Feedback, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, &model.Feedback{
				ID:         "given-" + itoa(i),
				ReviewerID: "reviewer-1",
				RevieweeID: "reviewee-" + itoa(i),
				PeriodID:   "period-1",
				Status:     model.FeedbackStatusSubmitted,
				CreatedAt:  time.Unix(int64(i), 0).UTC(),
			})
		}
		return out
	}

	newService := func(seed []*model.Feedback) *FeedbackService {
		repo := &fakeFeedbackRepo{byReviewee: map[string][]*model.Feedback{}}
		for _, f := range seed {
			repo.byReviewee[f.RevieweeID] = append(repo.byReviewee[f.RevieweeID], f)
		}
		return NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	t.Run("returns only the caller's submitted entries, newest first", func(t *testing.T) {
		seed := append(buildGiven(3), &model.Feedback{ID: "other", ReviewerID: "reviewer-2", RevieweeID: "reviewee-9", Status: model.FeedbackStatusSubmitted, CreatedAt: time.Unix(1, 0).UTC()})
		svc := newService(seed)

		got, next, err := svc.ListMyGivenFeedbacks(context.Background(), "reviewer-1", 0, "")
		if err != nil {
			t.Fatalf("ListMyGivenFeedbacks: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d entries, want 3", len(got))
		}
		if got[0].ID != "given-3" {
			t.Fatalf("expected newest first, got %q first", got[0].ID)
		}
		if next != "" {
			t.Fatalf("expected empty cursor, got %q", next)
		}
	})

	t.Run("drafts are not listed as given feedback", func(t *testing.T) {
		svc := newService([]*model.Feedback{{ID: "draft", ReviewerID: "reviewer-1", RevieweeID: "reviewee-1", Status: model.FeedbackStatusDraft, CreatedAt: time.Unix(1, 0).UTC()}})

		got, _, err := svc.ListMyGivenFeedbacks(context.Background(), "reviewer-1", 0, "")
		if err != nil {
			t.Fatalf("ListMyGivenFeedbacks: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d entries, want 0", len(got))
		}
	})

	t.Run("limit and cursor flow to the repository", func(t *testing.T) {
		repo := &fakeFeedbackRepo{
			listGivenFn: func(_ context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
				if reviewerID != "reviewer-1" || limit != 5 || cursorID != "given-2" {
					t.Fatalf("unexpected repo args: %q %d %q", reviewerID, limit, cursorID)
				}
				return []*model.Feedback{}, "", nil
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		if _, _, err := svc.ListMyGivenFeedbacks(context.Background(), "reviewer-1", 5, "given-2"); err != nil {
			t.Fatalf("ListMyGivenFeedbacks: %v", err)
		}
	})

	t.Run("default and max limits", func(t *testing.T) {
		var gotLimit int
		repo := &fakeFeedbackRepo{
			listGivenFn: func(_ context.Context, _ string, limit int, _ string) ([]*model.Feedback, string, error) {
				gotLimit = limit
				return []*model.Feedback{}, "", nil
			},
		}
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		if _, _, err := svc.ListMyGivenFeedbacks(context.Background(), "reviewer-1", 0, ""); err != nil {
			t.Fatalf("ListMyGivenFeedbacks: %v", err)
		}
		if gotLimit != DefaultFeedbackListLimit {
			t.Fatalf("got default limit %d, want %d", gotLimit, DefaultFeedbackListLimit)
		}

		if _, _, err := svc.ListMyGivenFeedbacks(context.Background(), "reviewer-1", 9999, ""); err != nil {
			t.Fatalf("ListMyGivenFeedbacks: %v", err)
		}
		if gotLimit != MaxFeedbackListLimit {
			t.Fatalf("got capped limit %d, want %d", gotLimit, MaxFeedbackListLimit)
		}
	})

	t.Run("unknown cursor propagates ErrFeedbackNotFound", func(t *testing.T) {
		svc := newService(buildGiven(1))

		_, _, err := svc.ListMyGivenFeedbacks(context.Background(), "reviewer-1", 10, "does-not-exist")
		if !errors.Is(err, apperror.ErrFeedbackNotFound) {
			t.Fatalf("expected ErrFeedbackNotFound, got %v", err)
		}
	})

	t.Run("missing reviewer id is a validation error", func(t *testing.T) {
		svc := newService(nil)

		_, _, err := svc.ListMyGivenFeedbacks(context.Background(), "  ", 10, "")
		var invalid apperror.ErrInvalidFeedback
		if !errors.As(err, &invalid) {
			t.Fatalf("expected ErrInvalidFeedback, got %v", err)
		}
	})
}

func TestFeedback_Create_PeriodWindow(t *testing.T) {
	t.Run("create during open window succeeds", func(t *testing.T) {
		svc, _, _ := newFeedbackTestService()
		if _, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput()); err != nil {
			t.Fatalf("Create: unexpected error: %v", err)
		}
	})

	t.Run("create before window opens is rejected", func(t *testing.T) {
		periods := &fakePeriodLookup{}
		periods.getFn = func(_ context.Context, id string) (*model.FeedbackPeriod, error) {
			return &model.FeedbackPeriod{ID: id, StartDate: time.Now().Add(24 * time.Hour), EndDate: time.Now().Add(48 * time.Hour)}, nil
		}
		repo := &fakeFeedbackRepo{}
		svc := NewFeedbackService(repo, periods, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		_, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput())
		if !errors.Is(err, apperror.ErrFeedbackPeriodClosed) {
			t.Fatalf("expected ErrFeedbackPeriodClosed, got %v", err)
		}
	})

	t.Run("create after window ends is rejected", func(t *testing.T) {
		periods := &fakePeriodLookup{}
		periods.getFn = func(_ context.Context, id string) (*model.FeedbackPeriod, error) {
			return &model.FeedbackPeriod{ID: id, StartDate: time.Now().Add(-48 * time.Hour), EndDate: time.Now().Add(-24 * time.Hour)}, nil
		}
		repo := &fakeFeedbackRepo{}
		svc := NewFeedbackService(repo, periods, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

		_, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput())
		if !errors.Is(err, apperror.ErrFeedbackPeriodClosed) {
			t.Fatalf("expected ErrFeedbackPeriodClosed, got %v", err)
		}
	})
}
