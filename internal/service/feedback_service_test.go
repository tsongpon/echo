package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
// By default it resolves any ID to a non-nil period (the happy path); tests
// can override getFn to simulate a missing period or a repository failure.
type fakePeriodLookup struct {
	gotID   string
	getFn  func(ctx context.Context, id string) (*model.FeedbackPeriod, error)
}

func (f *fakePeriodLookup) GetByID(ctx context.Context, id string) (*model.FeedbackPeriod, error) {
	f.gotID = id
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return &model.FeedbackPeriod{ID: id, Name: "Test Period"}, nil
}

// newFeedbackTestService builds a FeedbackService backed by a fake feedback
// repo, a fake period lookup (happy path: any period ID resolves), and a
// discarding logger. Returns the period lookup so tests can override its
// behavior or assert on which ID was looked up.
func newFeedbackTestService() (*FeedbackService, *fakeFeedbackRepo, *fakePeriodLookup) {
	repo := &fakeFeedbackRepo{}
	periods := &fakePeriodLookup{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewFeedbackService(repo, periods, logger), repo, periods
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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
				ID:        "fb-" + itoa(i),
				RevieweeID: "reviewee-1",
				PeriodID:  "period-1",
				CreatedAt: time.Unix(int64(i), 0).UTC(),
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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		svc := NewFeedbackService(repo, &fakePeriodLookup{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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