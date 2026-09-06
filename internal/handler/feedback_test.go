package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/model"
)

// fakeFeedbackService is an in-test stand-in for the handler.FeedbackService
// interface. It records the inputs the handler passed through and returns
// canned outputs, so the handler can be unit-tested without the real service
// or Firestore.
type fakeFeedbackService struct {
	gotReviewerID  string
	createFn       func(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error)
	listByReviewee func(ctx context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error)
	listForManager func(ctx context.Context, callerID, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error)

	createDraftFn func(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error)
	getDraftFn    func(ctx context.Context, reviewerID, draftID string) (*model.Feedback, error)
	updateDraftFn func(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error)
	submitDraftFn func(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error)
	deleteDraftFn func(ctx context.Context, reviewerID, draftID string) error
	listDraftsFn  func(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
	listGivenFn   func(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error)
}

func (f *fakeFeedbackService) CreateDraft(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error) {
	if f.createDraftFn != nil {
		return f.createDraftFn(ctx, reviewerID, feedback)
	}
	f.gotReviewerID = reviewerID
	if feedback == nil {
		feedback = &model.Feedback{}
	}
	return &model.Feedback{
		ID:         "draft-1",
		PeriodID:   feedback.PeriodID,
		RevieweeID: feedback.RevieweeID,
		ReviewerID: reviewerID,
		Visibility: feedback.Visibility,
		Status:     model.FeedbackStatusDraft,
	}, nil
}

func (f *fakeFeedbackService) GetDraft(ctx context.Context, reviewerID, draftID string) (*model.Feedback, error) {
	if f.getDraftFn != nil {
		return f.getDraftFn(ctx, reviewerID, draftID)
	}
	return &model.Feedback{ID: draftID, ReviewerID: reviewerID, Status: model.FeedbackStatusDraft}, nil
}

func (f *fakeFeedbackService) UpdateDraft(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error) {
	if f.updateDraftFn != nil {
		return f.updateDraftFn(ctx, reviewerID, draftID, apply)
	}
	return &model.Feedback{ID: draftID, ReviewerID: reviewerID, Status: model.FeedbackStatusDraft}, nil
}

func (f *fakeFeedbackService) SubmitDraft(ctx context.Context, reviewerID, draftID string, apply func(*model.Feedback) error) (*model.Feedback, error) {
	if f.submitDraftFn != nil {
		return f.submitDraftFn(ctx, reviewerID, draftID, apply)
	}
	return &model.Feedback{ID: draftID, ReviewerID: reviewerID, Status: model.FeedbackStatusSubmitted}, nil
}

func (f *fakeFeedbackService) DeleteDraft(ctx context.Context, reviewerID, draftID string) error {
	if f.deleteDraftFn != nil {
		return f.deleteDraftFn(ctx, reviewerID, draftID)
	}
	return nil
}

func (f *fakeFeedbackService) ListMyDrafts(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if f.listDraftsFn != nil {
		return f.listDraftsFn(ctx, reviewerID, limit, cursorID)
	}
	return []*model.Feedback{}, "", nil
}

func (f *fakeFeedbackService) ListMyGivenFeedbacks(ctx context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if f.listGivenFn != nil {
		return f.listGivenFn(ctx, reviewerID, limit, cursorID)
	}
	return []*model.Feedback{}, "", nil
}

func (f *fakeFeedbackService) Create(_ context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error) {
	if f.createFn != nil {
		return f.createFn(context.Background(), reviewerID, feedback)
	}
	// Default happy path: echo back a populated feedback so the handler can
	// serialize it. Record the reviewer ID the handler passed (taken from the
	// JWT) so tests can assert the handler does not trust the body.
	f.gotReviewerID = reviewerID
	if feedback == nil {
		feedback = &model.Feedback{}
	}
	return &model.Feedback{
		ID:                 "feedback-1",
		PeriodID:           feedback.PeriodID,
		RevieweeID:         feedback.RevieweeID,
		ReviewerID:         reviewerID,
		CommunicationScore: feedback.CommunicationScore,
		LeadershipScore:    feedback.LeadershipScore,
		TechnicalScore:     feedback.TechnicalScore,
		CollaborationScore: feedback.CollaborationScore,
		DeliveryScore:      feedback.DeliveryScore,
		TrustScore:         feedback.TrustScore,
		StrengthsComment:   feedback.StrengthsComment,
		WeaknessesComment:  feedback.WeaknessesComment,
		Visibility:         feedback.Visibility,
	}, nil
}

func (f *fakeFeedbackService) ListByReviewee(_ context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if f.listByReviewee != nil {
		return f.listByReviewee(context.Background(), revieweeID, limit, cursorID)
	}
	return []*model.Feedback{}, "", nil
}

func (f *fakeFeedbackService) ListByRevieweeForManager(_ context.Context, callerID, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
	if f.listForManager != nil {
		return f.listForManager(context.Background(), callerID, revieweeID, limit, cursorID)
	}
	return []*model.Feedback{}, "", nil
}

func TestCreateFeedback_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	reviewer := &model.Employee{
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "Acme",
		Role:             model.RoleUser,
		Title:            "Engineer",
		Email:            "alice@example.com",
	}

	// setClaims signs+verifies a token for the given employee and stores the
	// resulting claims in the echo context, mimicking what the Auth middleware
	// does on a real request.
	setClaims := func(c *echo.Context, e *model.Employee) {
		token, err := signer.Sign(e)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := signer.Verify(token)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		c.Set(contextKeyUser, claims)
	}

	const validBody = `{"period_id":"period-1","reviewee_id":"emp-2","communication_score":4,"leadership_score":5,"technical_score":3,"collaboration_score":4,"delivery_score":5,"trust_score":2,"strengths_comment":"great","weaknesses_comment":"docs","visibility":"anonymous"}`

	cases := []struct {
		name        string
		claims      *model.Employee
		body        string
		createFn    func(ctx context.Context, reviewerID string, feedback *model.Feedback) (*model.Feedback, error)
		wantCode    int
		wantMessage string
		wantBodyIn  string
	}{
		{
			name:       "success",
			claims:     reviewer,
			body:       validBody,
			wantCode:   http.StatusCreated,
			wantBodyIn: `"reviewer_id":"emp-1"`,
		},
		{
			name:   "validation error",
			claims: reviewer,
			body:   `{"period_id":"period-1","reviewee_id":"emp-1","communication_score":4,"leadership_score":5,"technical_score":3,"collaboration_score":4,"delivery_score":5,"trust_score":2}`,
			createFn: func(_ context.Context, _ string, _ *model.Feedback) (*model.Feedback, error) {
				return nil, apperror.ErrInvalidFeedback("reviewer cannot review themselves")
			},
			wantCode:    http.StatusBadRequest,
			wantMessage: "reviewer cannot review themselves",
		},
		{
			name:   "internal error",
			claims: reviewer,
			body:   validBody,
			createFn: func(_ context.Context, _ string, _ *model.Feedback) (*model.Feedback, error) {
				return nil, errors.New("db down")
			},
			wantCode:    http.StatusInternalServerError,
			wantMessage: "failed to create feedback",
		},
		{
			name:        "malformed body",
			claims:      reviewer,
			body:        `{not-json`,
			wantCode:    http.StatusBadRequest,
			wantMessage: "invalid request body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeFeedbackService{createFn: tc.createFn}
			h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

			req := httptest.NewRequest(http.MethodPost, "/v1/feedbacks", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			e := echo.New()
			c := e.NewContext(req, rec)
			setClaims(c, tc.claims)

			err := h.CreateFeedback(c)
			if tc.wantCode == http.StatusCreated {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if rec.Code != tc.wantCode {
					t.Fatalf("got status %d, want %d", rec.Code, tc.wantCode)
				}
				if !strings.Contains(rec.Body.String(), tc.wantBodyIn) {
					t.Fatalf("expected %q in body, got %s", tc.wantBodyIn, rec.Body.String())
				}
				// The handler must take the reviewer ID from the JWT subject,
				// not the body.
				if svc.gotReviewerID != "emp-1" {
					t.Fatalf("handler passed reviewer_id %q to service, want emp-1 (from JWT subject)", svc.gotReviewerID)
				}
				return
			}

			he, ok := err.(*echo.HTTPError)
			if !ok {
				t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
			}
			if he.Code != tc.wantCode {
				t.Fatalf("got status %d, want %d", he.Code, tc.wantCode)
			}
			if he.Message != tc.wantMessage {
				t.Fatalf("expected message %q, got %v", tc.wantMessage, he.Message)
			}
		})
	}

	t.Run("no token is unauthorized", func(t *testing.T) {
		// Bypass middleware to assert the handler guards against missing
		// claims (defence in depth for miswired routes).
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodPost, "/v1/feedbacks", strings.NewReader(validBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.CreateFeedback(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}

func TestListMyFeedbacks_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	caller := &model.Employee{
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "Acme",
		Role:             model.RoleUser,
		Title:            "Engineer",
		Email:            "alice@example.com",
	}

	// setClaims signs+verifies a token for the given employee and stores the
	// resulting claims in the echo context, mimicking what the Auth middleware
	// does on a real request.
	setClaims := func(c *echo.Context, e *model.Employee) {
		token, err := signer.Sign(e)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := signer.Verify(token)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		c.Set(contextKeyUser, claims)
	}

	t.Run("success returns feedback received by the caller", func(t *testing.T) {
		feedbacks := []*model.Feedback{
			{ID: "fb-1", RevieweeID: "emp-1", ReviewerID: "emp-2", Visibility: model.FeedbackVisibilityNamed, PeriodID: "period-1"},
			{ID: "fb-2", RevieweeID: "emp-1", ReviewerID: "emp-3", Visibility: model.FeedbackVisibilityAnonymous, PeriodID: "period-1"},
		}
		var gotRevieweeID string
		var gotLimit int
		var gotCursor string
		svc := &fakeFeedbackService{
			listByReviewee: func(_ context.Context, revieweeID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
				gotRevieweeID = revieweeID
				gotLimit = limit
				gotCursor = cursorID
				return feedbacks, "fb-2", nil
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/feedbacks?limit=2&cursor=fb-0", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		if err := h.ListMyFeedbacks(c); err != nil {
			t.Fatalf("ListMyFeedbacks: unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}
		// The handler must take the reviewee id from the JWT subject, not the
		// body or query.
		if gotRevieweeID != "emp-1" {
			t.Fatalf("handler passed reviewee_id %q to service, want emp-1 (from JWT subject)", gotRevieweeID)
		}
		if gotLimit != 2 {
			t.Fatalf("handler passed limit %d, want 2", gotLimit)
		}
		if gotCursor != "fb-0" {
			t.Fatalf("handler passed cursor %q, want fb-0", gotCursor)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"feedbacks":[`) {
			t.Fatalf("response missing feedbacks array: %s", body)
		}
		if !strings.Contains(body, `"next_cursor":"fb-2"`) {
			t.Fatalf("expected next_cursor fb-2, got %s", body)
		}
		// Named entry keeps reviewer_id; anonymous entry must have it blanked.
		if !strings.Contains(body, `"reviewer_id":"emp-2"`) {
			t.Fatalf("expected named entry reviewer_id emp-2 to be present, got %s", body)
		}
		if strings.Contains(body, `"reviewer_id":"emp-3"`) {
			t.Fatalf("anonymous entry reviewer_id emp-3 must be redacted, got %s", body)
		}
	})

	t.Run("empty result returns empty array, not null", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listByReviewee: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return []*model.Feedback{}, "", nil
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/feedbacks", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		if err := h.ListMyFeedbacks(c); err != nil {
			t.Fatalf("ListMyFeedbacks: unexpected error: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"feedbacks":[]`) {
			t.Fatalf("expected empty feedbacks array, got %s", body)
		}
		if !strings.Contains(body, `"next_cursor":null`) {
			t.Fatalf("expected next_cursor:null, got %s", body)
		}
	})

	t.Run("unknown cursor maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listByReviewee: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", apperror.ErrFeedbackNotFound
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/feedbacks?cursor=does-not-exist", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		err := h.ListMyFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusBadRequest)
		}
		if he.Message != "unknown cursor" {
			t.Fatalf("expected message %q, got %v", "unknown cursor", he.Message)
		}
	})

	t.Run("internal error maps to 500", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listByReviewee: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", errors.New("db down")
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/feedbacks", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		err := h.ListMyFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusInternalServerError)
		}
		if he.Message != "failed to list feedbacks" {
			t.Fatalf("expected message %q, got %v", "failed to list feedbacks", he.Message)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		// Bypass middleware to assert the handler guards against missing
		// claims (defence in depth for miswired routes).
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/feedbacks", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.ListMyFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}

func TestListMyGivenFeedbacks_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	caller := &model.Employee{
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "Acme",
		Role:             model.RoleUser,
		Title:            "Engineer",
		Email:            "alice@example.com",
	}

	setClaims := func(c *echo.Context, e *model.Employee) {
		token, err := signer.Sign(e)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := signer.Verify(token)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		c.Set(contextKeyUser, claims)
	}

	t.Run("success returns feedback given by the caller with reviewer_id preserved", func(t *testing.T) {
		feedbacks := []*model.Feedback{
			{ID: "fb-1", RevieweeID: "emp-2", ReviewerID: "emp-1", Visibility: model.FeedbackVisibilityNamed, PeriodID: "period-1", Status: model.FeedbackStatusSubmitted},
			{ID: "fb-2", RevieweeID: "emp-3", ReviewerID: "emp-1", Visibility: model.FeedbackVisibilityAnonymous, PeriodID: "period-1", Status: model.FeedbackStatusSubmitted},
		}
		var gotReviewerID string
		var gotLimit int
		var gotCursor string
		svc := &fakeFeedbackService{
			listGivenFn: func(_ context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
				gotReviewerID = reviewerID
				gotLimit = limit
				gotCursor = cursorID
				return feedbacks, "fb-2", nil
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/given-feedbacks?limit=2&cursor=fb-0", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		if err := h.ListMyGivenFeedbacks(c); err != nil {
			t.Fatalf("ListMyGivenFeedbacks: unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}
		// The handler must take the reviewer id from the JWT subject, not the
		// body or query.
		if gotReviewerID != "emp-1" {
			t.Fatalf("handler passed reviewer_id %q to service, want emp-1 (from JWT subject)", gotReviewerID)
		}
		if gotLimit != 2 {
			t.Fatalf("handler passed limit %d, want 2", gotLimit)
		}
		if gotCursor != "fb-0" {
			t.Fatalf("handler passed cursor %q, want fb-0", gotCursor)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"feedbacks":[`) {
			t.Fatalf("response missing feedbacks array: %s", body)
		}
		if !strings.Contains(body, `"next_cursor":"fb-2"`) {
			t.Fatalf("expected next_cursor fb-2, got %s", body)
		}
		// The caller is the reviewer: reviewer_id is always preserved, even
		// for anonymous entries (an author knows their own identity).
		if !strings.Contains(body, `"reviewer_id":"emp-1"`) {
			t.Fatalf("expected reviewer_id emp-1 to be present, got %s", body)
		}
	})

	t.Run("empty result returns empty array, not null", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listGivenFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return []*model.Feedback{}, "", nil
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/given-feedbacks", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		if err := h.ListMyGivenFeedbacks(c); err != nil {
			t.Fatalf("ListMyGivenFeedbacks: unexpected error: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"feedbacks":[]`) {
			t.Fatalf("expected empty feedbacks array, got %s", body)
		}
		if !strings.Contains(body, `"next_cursor":null`) {
			t.Fatalf("expected next_cursor:null, got %s", body)
		}
	})

	t.Run("unknown cursor maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listGivenFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", apperror.ErrFeedbackNotFound
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/given-feedbacks?cursor=does-not-exist", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		err := h.ListMyGivenFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusBadRequest)
		}
		if he.Message != "unknown cursor" {
			t.Fatalf("expected message %q, got %v", "unknown cursor", he.Message)
		}
	})

	t.Run("internal error maps to 500", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listGivenFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", errors.New("db down")
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/given-feedbacks", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)
		setClaims(c, caller)

		err := h.ListMyGivenFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusInternalServerError)
		}
		if he.Message != "failed to list feedbacks" {
			t.Fatalf("expected message %q, got %v", "failed to list feedbacks", he.Message)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/me/given-feedbacks", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.ListMyGivenFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}

func TestListEmployeeFeedbacks_Handler(t *testing.T) {
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}

	caller := &model.Employee{
		ID:               "emp-1",
		Name:             "Alice",
		OrganizationName: "Acme",
		Role:             model.RoleUser,
		Title:            "Manager",
		Email:            "alice@example.com",
	}

	// newContext builds an echo context for the GET route with the :id path
	// parameter set and the caller's claims stored, mimicking what the router
	// and Auth middleware do for a real request.
	newContext := func(query string) (*echo.Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodGet, "/v1/employees/emp-2/feedbacks"+query, nil)
		rec := httptest.NewRecorder()
		e := echo.New()
		c := e.NewContext(req, rec)
		c.SetPath("/v1/employees/:id/feedbacks")
		c.SetPathValues(echo.PathValues{{Name: "id", Value: "emp-2"}})
		token, err := signer.Sign(caller)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		claims, err := signer.Verify(token)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		c.Set(contextKeyUser, claims)
		return c, rec
	}

	t.Run("manager sees redacted anonymous feedback", func(t *testing.T) {
		feedbacks := []*model.Feedback{
			{ID: "fb-1", RevieweeID: "emp-2", ReviewerID: "reviewer-9", StrengthsComment: "s", WeaknessesComment: "w", Visibility: model.FeedbackVisibilityNamed},
			{ID: "fb-2", RevieweeID: "emp-2", ReviewerID: "reviewer-7", StrengthsComment: "s", WeaknessesComment: "w", Visibility: model.FeedbackVisibilityAnonymous},
		}
		svc := &fakeFeedbackService{
			listForManager: func(_ context.Context, callerID, revieweeID string, _ int, _ string) ([]*model.Feedback, string, error) {
				if callerID != "emp-1" {
					t.Fatalf("handler passed caller %q, want emp-1 (from JWT)", callerID)
				}
				if revieweeID != "emp-2" {
					t.Fatalf("handler passed reviewee %q, want emp-2 (from path)", revieweeID)
				}
				return feedbacks, "", nil
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, rec := newContext("")
		if err := h.ListEmployeeFeedbacks(c); err != nil {
			t.Fatalf("ListEmployeeFeedbacks: unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		// The named entry keeps its reviewer_id.
		if !strings.Contains(body, `"reviewer_id":"reviewer-9"`) {
			t.Fatalf("expected named entry to keep reviewer_id, got %s", body)
		}
		// The anonymous entry must have its reviewer_id blanked.
		if strings.Contains(body, "reviewer-7") {
			t.Fatalf("anonymous reviewer id must be redacted for the manager, got %s", body)
		}
	})

	t.Run("limit and cursor reach the service", func(t *testing.T) {
		var gotLimit int
		var gotCursor string
		svc := &fakeFeedbackService{
			listForManager: func(_ context.Context, _ string, _ string, limit int, cursorID string) ([]*model.Feedback, string, error) {
				gotLimit = limit
				gotCursor = cursorID
				return []*model.Feedback{}, "", nil
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, _ := newContext("?limit=7&cursor=fb-1")
		if err := h.ListEmployeeFeedbacks(c); err != nil {
			t.Fatalf("ListEmployeeFeedbacks: unexpected error: %v", err)
		}
		if gotLimit != 7 {
			t.Fatalf("handler passed limit %d, want 7", gotLimit)
		}
		if gotCursor != "fb-1" {
			t.Fatalf("handler passed cursor %q, want fb-1", gotCursor)
		}
	})

	t.Run("non-manager caller maps to 403", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listForManager: func(_ context.Context, _ string, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", apperror.ErrForbidden
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, _ := newContext("")
		err := h.ListEmployeeFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusForbidden {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusForbidden)
		}
	})

	t.Run("unknown employee maps to 404", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listForManager: func(_ context.Context, _ string, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", apperror.ErrEmployeeNotFound
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, _ := newContext("")
		err := h.ListEmployeeFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusNotFound {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusNotFound)
		}
	})

	t.Run("unknown cursor maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listForManager: func(_ context.Context, _ string, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", apperror.ErrFeedbackNotFound
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, _ := newContext("?cursor=ghost")
		err := h.ListEmployeeFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusBadRequest)
		}
		if he.Message != "unknown cursor" {
			t.Fatalf("expected message %q, got %v", "unknown cursor", he.Message)
		}
	})

	t.Run("internal error maps to 500", func(t *testing.T) {
		svc := &fakeFeedbackService{
			listForManager: func(_ context.Context, _ string, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
				return nil, "", errors.New("db down")
			},
		}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, _ := newContext("")
		err := h.ListEmployeeFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusInternalServerError)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		req := httptest.NewRequest(http.MethodGet, "/v1/employees/emp-2/feedbacks", nil)
		rec := httptest.NewRecorder()

		e := echo.New()
		c := e.NewContext(req, rec)

		err := h.ListEmployeeFeedbacks(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}

// newDraftContext builds an echo context for a draft route with the :id path
// parameter set (when non-empty) and the caller's claims stored, mimicking
// what the router and Auth middleware do for a real request.
func newDraftContext(method, target, path string, id string, body string, claims *auth.Claims) (*echo.Context, *httptest.ResponseRecorder) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	if path != "" {
		c.SetPath(path)
		if id != "" {
			c.SetPathValues(echo.PathValues{{Name: "id", Value: id}})
		}
	}
	if claims != nil {
		c.Set(contextKeyUser, claims)
	}
	return c, rec
}

// draftClaims signs+verifies a token for the given employee ID and returns
// the resulting claims, mimicking what the Auth middleware stores.
func draftClaims(t *testing.T, e *model.Employee) *auth.Claims {
	t.Helper()
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}
	token, err := signer.Sign(e)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	claims, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return claims
}

func TestCreateFeedbackDraft_Handler(t *testing.T) {
	caller := &model.Employee{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Role: model.RoleUser, Email: "alice@example.com"}

	const validBody = `{"period_id":"period-1","reviewee_id":"emp-2","communication_score":4,"strengths_comment":"wip"}`

	t.Run("success returns 201 with draft status", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, rec := newDraftContext(http.MethodPost, "/v1/feedback-drafts", "", "", validBody, draftClaims(t, caller))
		if err := h.CreateFeedbackDraft(c); err != nil {
			t.Fatalf("CreateFeedbackDraft: unexpected error: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusCreated)
		}
		if !strings.Contains(rec.Body.String(), `"status":"draft"`) {
			t.Fatalf("expected draft status in body, got %s", rec.Body.String())
		}
		if svc.gotReviewerID != "emp-1" {
			t.Fatalf("handler passed reviewer %q, want emp-1 (from JWT)", svc.gotReviewerID)
		}
	})

	t.Run("duplicate draft maps to 409", func(t *testing.T) {
		svc := &fakeFeedbackService{createDraftFn: func(_ context.Context, _ string, _ *model.Feedback) (*model.Feedback, error) {
			return nil, apperror.ErrFeedbackDraftAlreadyExists
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPost, "/v1/feedback-drafts", "", "", validBody, draftClaims(t, caller))
		err := h.CreateFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusConflict {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusConflict)
		}
	})

	t.Run("validation error maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{createDraftFn: func(_ context.Context, _ string, _ *model.Feedback) (*model.Feedback, error) {
			return nil, apperror.ErrInvalidFeedback("period_id is required")
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPost, "/v1/feedback-drafts", "", "", `{}`, draftClaims(t, caller))
		err := h.CreateFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest || he.Message != "period_id is required" {
			t.Fatalf("got %d %v, want 400 period_id is required", he.Code, he.Message)
		}
	})

	t.Run("malformed body maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPost, "/v1/feedback-drafts", "", "", `{not-json`, draftClaims(t, caller))
		err := h.CreateFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest || he.Message != "invalid request body" {
			t.Fatalf("got %d %v, want 400 invalid request body", he.Code, he.Message)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPost, "/v1/feedback-drafts", "", "", validBody, nil)
		err := h.CreateFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}

func TestGetFeedbackDraft_Handler(t *testing.T) {
	caller := &model.Employee{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Role: model.RoleUser, Email: "alice@example.com"}

	t.Run("success returns the draft", func(t *testing.T) {
		var gotID string
		svc := &fakeFeedbackService{getDraftFn: func(_ context.Context, reviewerID, draftID string) (*model.Feedback, error) {
			if reviewerID != "emp-1" {
				t.Fatalf("handler passed reviewer %q, want emp-1", reviewerID)
			}
			gotID = draftID
			return &model.Feedback{ID: draftID, ReviewerID: reviewerID, Status: model.FeedbackStatusDraft}, nil
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, rec := newDraftContext(http.MethodGet, "/v1/feedback-drafts/draft-9", "/v1/feedback-drafts/:id", "draft-9", "", draftClaims(t, caller))
		if err := h.GetFeedbackDraft(c); err != nil {
			t.Fatalf("GetFeedbackDraft: unexpected error: %v", err)
		}
		if gotID != "draft-9" {
			t.Fatalf("handler passed id %q, want draft-9", gotID)
		}
		if !strings.Contains(rec.Body.String(), `"id":"draft-9"`) {
			t.Fatalf("expected draft id in body, got %s", rec.Body.String())
		}
	})

	t.Run("unknown or foreign draft maps to 404", func(t *testing.T) {
		svc := &fakeFeedbackService{getDraftFn: func(_ context.Context, _, _ string) (*model.Feedback, error) {
			return nil, apperror.ErrFeedbackNotFound
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodGet, "/v1/feedback-drafts/ghost", "/v1/feedback-drafts/:id", "ghost", "", draftClaims(t, caller))
		err := h.GetFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusNotFound {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusNotFound)
		}
	})
}

func TestUpdateFeedbackDraft_Handler(t *testing.T) {
	caller := &model.Employee{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Role: model.RoleUser, Email: "alice@example.com"}

	t.Run("partial body applies only supplied fields", func(t *testing.T) {
		var applied *model.Feedback
		svc := &fakeFeedbackService{updateDraftFn: func(_ context.Context, _, _ string, apply func(*model.Feedback) error) (*model.Feedback, error) {
			draft := &model.Feedback{ID: "draft-1", StrengthsComment: "original", Status: model.FeedbackStatusDraft}
			if err := apply(draft); err != nil {
				return nil, err
			}
			applied = draft
			return draft, nil
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPatch, "/v1/feedback-drafts/draft-1", "/v1/feedback-drafts/:id", "draft-1", `{"communication_score":5}`, draftClaims(t, caller))
		if err := h.UpdateFeedbackDraft(c); err != nil {
			t.Fatalf("UpdateFeedbackDraft: unexpected error: %v", err)
		}
		if applied == nil {
			t.Fatal("expected the update to be applied")
		}
		if applied.CommunicationScore != 5 {
			t.Fatalf("got communication_score %d, want 5", applied.CommunicationScore)
		}
		if applied.StrengthsComment != "original" {
			t.Fatalf("omitted strengths_comment changed: got %q", applied.StrengthsComment)
		}
	})

	t.Run("invalid visibility in body maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{updateDraftFn: func(_ context.Context, _, _ string, apply func(*model.Feedback) error) (*model.Feedback, error) {
			draft := &model.Feedback{ID: "draft-1", Status: model.FeedbackStatusDraft}
			if err := apply(draft); err != nil {
				return nil, err
			}
			return draft, nil
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPatch, "/v1/feedback-drafts/draft-1", "/v1/feedback-drafts/:id", "draft-1", `{"visibility":"secret"}`, draftClaims(t, caller))
		err := h.UpdateFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest || he.Message != "visibility must be one of anonymous, named" {
			t.Fatalf("got %d %v, want 400 visibility message", he.Code, he.Message)
		}
	})

	t.Run("concurrent update maps to 409", func(t *testing.T) {
		svc := &fakeFeedbackService{updateDraftFn: func(_ context.Context, _, _ string, _ func(*model.Feedback) error) (*model.Feedback, error) {
			return nil, apperror.ErrFeedbackConcurrentUpdate
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPatch, "/v1/feedback-drafts/draft-1", "/v1/feedback-drafts/:id", "draft-1", `{}`, draftClaims(t, caller))
		err := h.UpdateFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusConflict {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusConflict)
		}
	})
}

func TestSubmitFeedbackDraft_Handler(t *testing.T) {
	caller := &model.Employee{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Role: model.RoleUser, Email: "alice@example.com"}

	t.Run("success returns submitted status", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, rec := newDraftContext(http.MethodPost, "/v1/feedback-drafts/draft-1/submit", "/v1/feedback-drafts/:id/submit", "draft-1", "", draftClaims(t, caller))
		if err := h.SubmitFeedbackDraft(c); err != nil {
			t.Fatalf("SubmitFeedbackDraft: unexpected error: %v", err)
		}
		if !strings.Contains(rec.Body.String(), `"status":"submitted"`) {
			t.Fatalf("expected submitted status in body, got %s", rec.Body.String())
		}
	})

	t.Run("closed period maps to 422", func(t *testing.T) {
		svc := &fakeFeedbackService{submitDraftFn: func(_ context.Context, _, _ string, _ func(*model.Feedback) error) (*model.Feedback, error) {
			return nil, apperror.ErrFeedbackPeriodClosed
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPost, "/v1/feedback-drafts/draft-1/submit", "/v1/feedback-drafts/:id/submit", "draft-1", "", draftClaims(t, caller))
		err := h.SubmitFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnprocessableEntity {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnprocessableEntity)
		}
	})

	t.Run("incomplete draft maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{submitDraftFn: func(_ context.Context, _, _ string, _ func(*model.Feedback) error) (*model.Feedback, error) {
			return nil, apperror.ErrInvalidFeedback("strengths_comment is required")
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodPost, "/v1/feedback-drafts/draft-1/submit", "/v1/feedback-drafts/:id/submit", "draft-1", "", draftClaims(t, caller))
		err := h.SubmitFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest || he.Message != "strengths_comment is required" {
			t.Fatalf("got %d %v, want 400 strengths message", he.Code, he.Message)
		}
	})
}

func TestDeleteFeedbackDraft_Handler(t *testing.T) {
	caller := &model.Employee{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Role: model.RoleUser, Email: "alice@example.com"}

	t.Run("success returns 204", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, rec := newDraftContext(http.MethodDelete, "/v1/feedback-drafts/draft-1", "/v1/feedback-drafts/:id", "draft-1", "", draftClaims(t, caller))
		if err := h.DeleteFeedbackDraft(c); err != nil {
			t.Fatalf("DeleteFeedbackDraft: unexpected error: %v", err)
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusNoContent)
		}
	})

	t.Run("unknown draft maps to 404", func(t *testing.T) {
		svc := &fakeFeedbackService{deleteDraftFn: func(_ context.Context, _, _ string) error {
			return apperror.ErrFeedbackNotFound
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodDelete, "/v1/feedback-drafts/ghost", "/v1/feedback-drafts/:id", "ghost", "", draftClaims(t, caller))
		err := h.DeleteFeedbackDraft(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusNotFound {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusNotFound)
		}
	})
}

func TestListMyFeedbackDrafts_Handler(t *testing.T) {
	caller := &model.Employee{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Role: model.RoleUser, Email: "alice@example.com"}

	t.Run("success returns drafts array with cursor", func(t *testing.T) {
		svc := &fakeFeedbackService{listDraftsFn: func(_ context.Context, reviewerID string, limit int, cursorID string) ([]*model.Feedback, string, error) {
			if reviewerID != "emp-1" {
				t.Fatalf("handler passed reviewer %q, want emp-1", reviewerID)
			}
			return []*model.Feedback{{ID: "draft-1", ReviewerID: reviewerID, Status: model.FeedbackStatusDraft, Visibility: model.FeedbackVisibilityAnonymous}}, "draft-1", nil
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, rec := newDraftContext(http.MethodGet, "/v1/feedback-drafts?limit=2&cursor=draft-0", "", "", "", draftClaims(t, caller))
		if err := h.ListMyFeedbackDrafts(c); err != nil {
			t.Fatalf("ListMyFeedbackDrafts: unexpected error: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"drafts":[`) {
			t.Fatalf("expected drafts array, got %s", body)
		}
		if !strings.Contains(body, `"next_cursor":"draft-1"`) {
			t.Fatalf("expected next_cursor draft-1, got %s", body)
		}
		// The author sees their own reviewer_id even on anonymous drafts.
		if !strings.Contains(body, `"reviewer_id":"emp-1"`) {
			t.Fatalf("expected reviewer_id to be preserved for the author, got %s", body)
		}
	})

	t.Run("empty result is an empty array", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, rec := newDraftContext(http.MethodGet, "/v1/feedback-drafts", "", "", "", draftClaims(t, caller))
		if err := h.ListMyFeedbackDrafts(c); err != nil {
			t.Fatalf("ListMyFeedbackDrafts: unexpected error: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"drafts":[]`) {
			t.Fatalf("expected empty drafts array, got %s", body)
		}
		if !strings.Contains(body, `"next_cursor":null`) {
			t.Fatalf("expected null next_cursor, got %s", body)
		}
	})

	t.Run("unknown cursor maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackService{listDraftsFn: func(_ context.Context, _ string, _ int, _ string) ([]*model.Feedback, string, error) {
			return nil, "", apperror.ErrFeedbackNotFound
		}}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodGet, "/v1/feedback-drafts?cursor=ghost", "", "", "", draftClaims(t, caller))
		err := h.ListMyFeedbackDrafts(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest || he.Message != "unknown cursor" {
			t.Fatalf("got %d %v, want 400 unknown cursor", he.Code, he.Message)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		svc := &fakeFeedbackService{}
		h := NewFeedbackHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newDraftContext(http.MethodGet, "/v1/feedback-drafts", "", "", "", nil)
		err := h.ListMyFeedbackDrafts(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}
