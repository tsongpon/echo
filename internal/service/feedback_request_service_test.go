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

// fakeFeedbackRequestRepo is an in-test stand-in for
// service.FeedbackRequestRepository. It serves from in-memory maps and
// enforces the one-open-request-per-triple rule via a claims set, mirroring
// the real Firestore claim-collection semantics.
type fakeFeedbackRequestRepo struct {
	requests map[string]*model.FeedbackRequest
	claims   map[string]bool

	createFn     func(ctx context.Context, request *model.FeedbackRequest) (*model.FeedbackRequest, error)
	getFn        func(ctx context.Context, id string) (*model.FeedbackRequest, error)
	setStatusFn  func(ctx context.Context, id string, to model.FeedbackRequestStatus) (*model.FeedbackRequest, error)
	closeMatchFn func(ctx context.Context, requesterID, requesteeID, periodID string) (*model.FeedbackRequest, error)
}

func claimKey(requesterID, requesteeID, periodID string) string {
	return requesterID + "_" + requesteeID + "_" + periodID
}

func (f *fakeFeedbackRequestRepo) Create(ctx context.Context, request *model.FeedbackRequest) (*model.FeedbackRequest, error) {
	if f.createFn != nil {
		return f.createFn(ctx, request)
	}
	if f.requests == nil {
		f.requests = map[string]*model.FeedbackRequest{}
	}
	if f.claims == nil {
		f.claims = map[string]bool{}
	}
	key := claimKey(request.RequesterID, request.RequesteeID, request.PeriodID)
	if f.claims[key] {
		return nil, apperror.ErrFeedbackRequestAlreadyExists
	}
	stored := *request
	stored.Status = model.FeedbackRequestStatusOpen
	stored.CreatedAt = time.Now()
	stored.UpdatedAt = stored.CreatedAt
	f.requests[stored.ID] = &stored
	f.claims[key] = true
	return &stored, nil
}

func (f *fakeFeedbackRequestRepo) Get(ctx context.Context, id string) (*model.FeedbackRequest, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	if r, ok := f.requests[id]; ok {
		copied := *r
		return &copied, nil
	}
	return nil, apperror.ErrFeedbackRequestNotFound
}

func (f *fakeFeedbackRequestRepo) SetStatus(ctx context.Context, id string, to model.FeedbackRequestStatus) (*model.FeedbackRequest, error) {
	if f.setStatusFn != nil {
		return f.setStatusFn(ctx, id, to)
	}
	r, ok := f.requests[id]
	if !ok || r.Status != model.FeedbackRequestStatusOpen {
		return nil, apperror.ErrFeedbackRequestNotFound
	}
	r.Status = to
	r.UpdatedAt = time.Now()
	delete(f.claims, claimKey(r.RequesterID, r.RequesteeID, r.PeriodID))
	copied := *r
	return &copied, nil
}

func (f *fakeFeedbackRequestRepo) CloseMatchingOpen(ctx context.Context, requesterID, requesteeID, periodID string) (*model.FeedbackRequest, error) {
	if f.closeMatchFn != nil {
		return f.closeMatchFn(ctx, requesterID, requesteeID, periodID)
	}
	if !f.claims[claimKey(requesterID, requesteeID, periodID)] {
		return nil, apperror.ErrFeedbackRequestNotFound
	}
	for _, r := range f.requests {
		if r.RequesterID == requesterID && r.RequesteeID == requesteeID && r.PeriodID == periodID && r.Status == model.FeedbackRequestStatusOpen {
			r.Status = model.FeedbackRequestStatusCompleted
			r.UpdatedAt = time.Now()
			delete(f.claims, claimKey(requesterID, requesteeID, periodID))
			copied := *r
			return &copied, nil
		}
	}
	return nil, apperror.ErrFeedbackRequestNotFound
}

func (f *fakeFeedbackRequestRepo) ListByRequestee(ctx context.Context, requesteeID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	return f.list("requestee", requesteeID, limit, cursorID)
}

func (f *fakeFeedbackRequestRepo) ListByRequester(ctx context.Context, requesterID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	return f.list("requester", requesterID, limit, cursorID)
}

func (f *fakeFeedbackRequestRepo) list(side, employeeID string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	var all []*model.FeedbackRequest
	for _, r := range f.requests {
		if side == "requestee" && r.RequesteeID == employeeID {
			all = append(all, r)
		}
		if side == "requester" && r.RequesterID == employeeID {
			all = append(all, r)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	start := 0
	if strings.TrimSpace(cursorID) != "" {
		found := -1
		for i, r := range all {
			if r.ID == cursorID {
				found = i
				break
			}
		}
		if found == -1 {
			return nil, "", apperror.ErrFeedbackRequestNotFound
		}
		start = found + 1
	}
	if limit <= 0 {
		limit = DefaultFeedbackRequestListLimit
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

// newRequestTestService builds a FeedbackRequestService over the caller's
// repo fake with a period lookup that always resolves and an employee lookup
// serving the given employees.
func newRequestTestService(repo FeedbackRequestRepository, employees map[string]*model.Employee, mailer Mailer) *FeedbackRequestService {
	periods := &fakePeriodLookup{}
	emp := &fakeEmployeeLookup{byID: employees}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewFeedbackRequestService(repo, periods, emp, mailer, logger)
}

func TestFeedbackRequest_Create(t *testing.T) {
	employees := map[string]*model.Employee{
		"emp-1": {ID: "emp-1", Name: "Alice", Email: "alice@acme.test", OrganizationName: "Acme"},
		"emp-2": {ID: "emp-2", Name: "Bob", Email: "bob@acme.test", OrganizationName: "Acme"},
		"emp-3": {ID: "emp-3", Name: "Cara", Email: "cara@other.test", OrganizationName: "OtherOrg"},
	}
	newInput := func() *model.FeedbackRequest {
		return &model.FeedbackRequest{RequesteeID: "emp-2", PeriodID: "period-1"}
	}

	t.Run("creates an open request and notifies the requestee", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		mailer := &noopMailer{}
		svc := newRequestTestService(repo, employees, mailer)

		created, err := svc.Create(context.Background(), "emp-1", newInput())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created.Status != model.FeedbackRequestStatusOpen {
			t.Fatalf("got status %q, want open", created.Status)
		}
		if created.RequesterID != "emp-1" {
			t.Fatalf("requester_id must come from the caller, got %q", created.RequesterID)
		}
		if mailer.lastReqTo != "bob@acme.test" {
			t.Fatalf("expected notification to emp-2, got %q", mailer.lastReqTo)
		}
	})

	t.Run("mailer failure does not fail the create", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, &failingMailer{})
		if _, err := svc.Create(context.Background(), "emp-1", newInput()); err != nil {
			t.Fatalf("Create must succeed despite mailer failure: %v", err)
		}
	})

	t.Run("self-request is rejected", func(t *testing.T) {
		svc := newRequestTestService(&fakeFeedbackRequestRepo{}, employees, nil)
		_, err := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-1", PeriodID: "period-1"})
		var invalid apperror.ErrInvalidFeedbackRequest
		if !errors.As(err, &invalid) {
			t.Fatalf("expected ErrInvalidFeedbackRequest, got %v", err)
		}
	})

	t.Run("cross-organization requestee is rejected", func(t *testing.T) {
		svc := newRequestTestService(&fakeFeedbackRequestRepo{}, employees, nil)
		_, err := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-3", PeriodID: "period-1"})
		var invalid apperror.ErrInvalidFeedbackRequest
		if !errors.As(err, &invalid) {
			t.Fatalf("expected ErrInvalidFeedbackRequest for cross-org requestee, got %v", err)
		}
	})

	t.Run("unknown requestee is rejected", func(t *testing.T) {
		svc := newRequestTestService(&fakeFeedbackRequestRepo{}, employees, nil)
		_, err := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-x", PeriodID: "period-1"})
		var invalid apperror.ErrInvalidFeedbackRequest
		if !errors.As(err, &invalid) {
			t.Fatalf("expected ErrInvalidFeedbackRequest for unknown requestee, got %v", err)
		}
	})

	t.Run("duplicate open request yields ErrFeedbackRequestAlreadyExists", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		if _, err := svc.Create(context.Background(), "emp-1", newInput()); err != nil {
			t.Fatalf("first Create: %v", err)
		}
		if _, err := svc.Create(context.Background(), "emp-1", newInput()); !errors.Is(err, apperror.ErrFeedbackRequestAlreadyExists) {
			t.Fatalf("expected ErrFeedbackRequestAlreadyExists, got %v", err)
		}
	})

	t.Run("declined request frees the slot for a new request", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		created, err := svc.Create(context.Background(), "emp-1", newInput())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := svc.Decline(context.Background(), "emp-2", created.ID); err != nil {
			t.Fatalf("Decline: %v", err)
		}
		if _, err := svc.Create(context.Background(), "emp-1", newInput()); err != nil {
			t.Fatalf("re-Create after decline: %v", err)
		}
	})
}

func TestFeedbackRequest_Decline(t *testing.T) {
	employees := map[string]*model.Employee{
		"emp-1": {ID: "emp-1", Name: "Alice", Email: "alice@acme.test", OrganizationName: "Acme"},
		"emp-2": {ID: "emp-2", Name: "Bob", Email: "bob@acme.test", OrganizationName: "Acme"},
	}

	t.Run("requestee declines an open request", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		created, err := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-2", PeriodID: "period-1"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		declined, err := svc.Decline(context.Background(), "emp-2", created.ID)
		if err != nil {
			t.Fatalf("Decline: %v", err)
		}
		if declined.Status != model.FeedbackRequestStatusDeclined {
			t.Fatalf("got status %q, want declined", declined.Status)
		}
	})

	t.Run("requester cannot decline their own request", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		created, _ := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-2", PeriodID: "period-1"})
		_, err := svc.Decline(context.Background(), "emp-1", created.ID)
		if !errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
			t.Fatalf("expected ErrFeedbackRequestNotFound (existence must not leak), got %v", err)
		}
	})

	t.Run("declining twice yields not found", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		created, _ := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-2", PeriodID: "period-1"})
		if _, err := svc.Decline(context.Background(), "emp-2", created.ID); err != nil {
			t.Fatalf("first Decline: %v", err)
		}
		if _, err := svc.Decline(context.Background(), "emp-2", created.ID); !errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
			t.Fatalf("expected ErrFeedbackRequestNotFound on second decline, got %v", err)
		}
	})
}

func TestFeedbackRequest_List(t *testing.T) {
	employees := map[string]*model.Employee{
		"emp-1": {ID: "emp-1", Name: "Alice", Email: "alice@acme.test", OrganizationName: "Acme"},
		"emp-2": {ID: "emp-2", Name: "Bob", Email: "bob@acme.test", OrganizationName: "Acme"},
	}
	seed := func(repo *fakeFeedbackRequestRepo) {
		repo.Create(context.Background(), &model.FeedbackRequest{ID: "req-1", RequesterID: "emp-1", RequesteeID: "emp-2", PeriodID: "period-1"})
		repo.Create(context.Background(), &model.FeedbackRequest{ID: "req-2", RequesterID: "emp-2", RequesteeID: "emp-1", PeriodID: "period-1"})
	}

	t.Run("received lists requests where the caller is the requestee", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		seed(repo)
		svc := newRequestTestService(repo, employees, nil)
		got, _, err := svc.List(context.Background(), "emp-2", "received", 0, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].ID != "req-1" {
			t.Fatalf("expected only req-1 received by emp-2, got %v", got)
		}
	})

	t.Run("sent lists requests where the caller is the requester", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		seed(repo)
		svc := newRequestTestService(repo, employees, nil)
		got, _, err := svc.List(context.Background(), "emp-2", "sent", 0, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].ID != "req-2" {
			t.Fatalf("expected only req-2 sent by emp-2, got %v", got)
		}
	})

	t.Run("unknown cursor propagates ErrFeedbackRequestNotFound", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		seed(repo)
		svc := newRequestTestService(repo, employees, nil)
		_, _, err := svc.List(context.Background(), "emp-2", "received", 10, "does-not-exist")
		if !errors.Is(err, apperror.ErrFeedbackRequestNotFound) {
			t.Fatalf("expected ErrFeedbackRequestNotFound, got %v", err)
		}
	})
}

func TestFeedbackRequest_CloseMatchingOpen(t *testing.T) {
	employees := map[string]*model.Employee{
		"emp-1": {ID: "emp-1", Name: "Alice", Email: "alice@acme.test", OrganizationName: "Acme"},
		"emp-2": {ID: "emp-2", Name: "Bob", Email: "bob@acme.test", OrganizationName: "Acme"},
	}

	t.Run("completes the matching open request", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		created, _ := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-2", PeriodID: "period-1"})
		if err := svc.CloseMatchingOpen(context.Background(), "emp-1", "emp-2", "period-1"); err != nil {
			t.Fatalf("CloseMatchingOpen: %v", err)
		}
		got, _ := repo.Get(context.Background(), created.ID)
		if got.Status != model.FeedbackRequestStatusCompleted {
			t.Fatalf("got status %q, want completed", got.Status)
		}
	})

	t.Run("no matching request is not an error", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		if err := svc.CloseMatchingOpen(context.Background(), "emp-1", "emp-2", "period-1"); err != nil {
			t.Fatalf("expected nil for no match, got %v", err)
		}
	})

	t.Run("completed request frees the slot", func(t *testing.T) {
		repo := &fakeFeedbackRequestRepo{}
		svc := newRequestTestService(repo, employees, nil)
		if _, err := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-2", PeriodID: "period-1"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := svc.CloseMatchingOpen(context.Background(), "emp-1", "emp-2", "period-1"); err != nil {
			t.Fatalf("CloseMatchingOpen: %v", err)
		}
		// The slot is free: a new request for the same triple succeeds.
		if _, err := svc.Create(context.Background(), "emp-1", &model.FeedbackRequest{RequesteeID: "emp-2", PeriodID: "period-1"}); err != nil {
			t.Fatalf("re-Create after completion: %v", err)
		}
	})
}

// TestFeedbackService_CloseMatchingRequests verifies the feedback service's
// completion hook: submitting feedback (direct or via draft) calls the
// injected FeedbackRequestCloser with the reviewee as the fulfilled request's
// requester.
func TestFeedbackService_CloseMatchingRequests(t *testing.T) {
	newHookService := func(closer FeedbackRequestCloser) *FeedbackService {
		repo := &fakeFeedbackRepo{drafts: map[string]*model.Feedback{}, claims: map[string]bool{}}
		return NewFeedbackService(repo, &fakePeriodLookup{}, nil, closer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	t.Run("direct create closes the matching request", func(t *testing.T) {
		var gotRequester, gotRequestee, gotPeriod string
		closer := &fakeRequestCloser{fn: func(ctx context.Context, requesterID, requesteeID, periodID string) error {
			gotRequester, gotRequestee, gotPeriod = requesterID, requesteeID, periodID
			return nil
		}}
		svc := newHookService(closer)
		if _, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput()); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if gotRequester != "reviewee-1" || gotRequestee != "reviewer-1" {
			t.Fatalf("closeMatchingRequests got (requester=%q, requestee=%q), want (reviewee-1 fulfilled, reviewer-1 wrote)", gotRequester, gotRequestee)
		}
		if gotPeriod == "" {
			t.Fatal("expected period to be passed to the closer")
		}
	})

	t.Run("draft submit closes the matching request", func(t *testing.T) {
		var called bool
		closer := &fakeRequestCloser{fn: func(context.Context, string, string, string) error {
			called = true
			return nil
		}}
		svc := newHookService(closer)
		created, err := svc.CreateDraft(context.Background(), "reviewer-1", completeDraft())
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}
		if _, err := svc.SubmitDraft(context.Background(), "reviewer-1", created.ID, nil); err != nil {
			t.Fatalf("SubmitDraft: %v", err)
		}
		if !called {
			t.Fatal("expected the completion hook to fire on draft submit")
		}
	})

	t.Run("closer failure never fails the submit", func(t *testing.T) {
		closer := &fakeRequestCloser{fn: func(context.Context, string, string, string) error {
			return errors.New("boom")
		}}
		svc := newHookService(closer)
		if _, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput()); err != nil {
			t.Fatalf("Create must succeed despite closer failure: %v", err)
		}
	})

	t.Run("nil closer is tolerated", func(t *testing.T) {
		svc := newHookService(nil)
		if _, err := svc.Create(context.Background(), "reviewer-1", validFeedbackInput()); err != nil {
			t.Fatalf("Create with nil closer: %v", err)
		}
	})
}

// fakeRequestCloser is a configurable FeedbackRequestCloser for tests.
type fakeRequestCloser struct {
	fn func(ctx context.Context, requesterID, requesteeID, periodID string) error
}

func (f *fakeRequestCloser) CloseMatchingOpen(ctx context.Context, requesterID, requesteeID, periodID string) error {
	if f.fn == nil {
		return nil
	}
	return f.fn(ctx, requesterID, requesteeID, periodID)
}

// failingMailer is a service.Mailer whose every send fails; used to assert
// best-effort behavior.
type failingMailer struct{}

func (failingMailer) SendVerificationEmail(context.Context, string, string) error {
	return errors.New("mailer down")
}
func (failingMailer) SendFeedbackRequestEmail(context.Context, string, string, string) error {
	return errors.New("mailer down")
}
