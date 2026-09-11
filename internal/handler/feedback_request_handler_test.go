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

// fakeFeedbackRequestService is an in-test stand-in for the handler's
// FeedbackRequestService interface, mirroring the feedback handler's fake.
type fakeFeedbackRequestService struct {
	createFn  func(ctx context.Context, requesterID string, request *model.FeedbackRequest) (*model.FeedbackRequest, error)
	declineFn func(ctx context.Context, callerID, requestID string) (*model.FeedbackRequest, error)
	listFn    func(ctx context.Context, callerID, direction string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error)
}

func (f *fakeFeedbackRequestService) Create(ctx context.Context, requesterID string, request *model.FeedbackRequest) (*model.FeedbackRequest, error) {
	if f.createFn != nil {
		return f.createFn(ctx, requesterID, request)
	}
	return &model.FeedbackRequest{ID: "req-1", RequesterID: requesterID, RequesteeID: request.RequesteeID, PeriodID: request.PeriodID, Status: model.FeedbackRequestStatusOpen}, nil
}

func (f *fakeFeedbackRequestService) Decline(ctx context.Context, callerID, requestID string) (*model.FeedbackRequest, error) {
	if f.declineFn != nil {
		return f.declineFn(ctx, callerID, requestID)
	}
	return &model.FeedbackRequest{ID: requestID, RequesteeID: callerID, Status: model.FeedbackRequestStatusDeclined}, nil
}

func (f *fakeFeedbackRequestService) List(ctx context.Context, callerID, direction string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
	if f.listFn != nil {
		return f.listFn(ctx, callerID, direction, limit, cursorID)
	}
	return []*model.FeedbackRequest{}, "", nil
}

// newRequestContext builds an echo context for a feedback-request route with
// the caller's claims stored, mimicking what the router and Auth middleware
// do for a real request.
func newRequestContext(t *testing.T, method, path string, body string) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	signer, err := auth.NewTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewTokenSigner: %v", err)
	}
	caller := &model.Employee{ID: "emp-1", Name: "Alice", OrganizationName: "Acme", Role: model.RoleUser, Title: "Engineer", Email: "alice@example.com"}

	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
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

func TestFeedbackRequestHandler_Create(t *testing.T) {
	t.Run("success returns 201 with the created request", func(t *testing.T) {
		svc := &fakeFeedbackRequestService{
			createFn: func(_ context.Context, requesterID string, request *model.FeedbackRequest) (*model.FeedbackRequest, error) {
				if requesterID != "emp-1" {
					t.Fatalf("requester must come from the JWT, got %q", requesterID)
				}
				if request.RequesteeID != "emp-2" || request.PeriodID != "period-1" {
					t.Fatalf("unexpected request: %+v", request)
				}
				return &model.FeedbackRequest{ID: "req-1", RequesterID: requesterID, RequesteeID: request.RequesteeID, PeriodID: request.PeriodID, Status: model.FeedbackRequestStatusOpen}, nil
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, rec := newRequestContext(t, http.MethodPost, "/v1/feedback-requests", `{"requestee_id":"emp-2","period_id":"period-1"}`)
		if err := h.Create(c); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusCreated)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"status":"open"`) || !strings.Contains(body, `"requester_id":"emp-1"`) {
			t.Fatalf("unexpected body: %s", body)
		}
	})

	t.Run("duplicate maps to 409", func(t *testing.T) {
		svc := &fakeFeedbackRequestService{
			createFn: func(context.Context, string, *model.FeedbackRequest) (*model.FeedbackRequest, error) {
				return nil, apperror.ErrFeedbackRequestAlreadyExists
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, _ := newRequestContext(t, http.MethodPost, "/v1/feedback-requests", `{"requestee_id":"emp-2","period_id":"period-1"}`)
		err := h.Create(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusConflict {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusConflict)
		}
	})

	t.Run("validation error maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackRequestService{
			createFn: func(context.Context, string, *model.FeedbackRequest) (*model.FeedbackRequest, error) {
				return nil, apperror.ErrInvalidFeedbackRequest("requestee must belong to your organization")
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, _ := newRequestContext(t, http.MethodPost, "/v1/feedback-requests", `{"requestee_id":"emp-9","period_id":"period-1"}`)
		err := h.Create(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusBadRequest)
		}
		if he.Message != "requestee must belong to your organization" {
			t.Fatalf("unexpected message: %v", he.Message)
		}
	})

	t.Run("invalid body maps to 400", func(t *testing.T) {
		h := NewFeedbackRequestHandler(&fakeFeedbackRequestService{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newRequestContext(t, http.MethodPost, "/v1/feedback-requests", `{not json`)
		err := h.Create(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusBadRequest)
		}
	})

	t.Run("no token is unauthorized", func(t *testing.T) {
		h := NewFeedbackRequestHandler(&fakeFeedbackRequestService{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		req := httptest.NewRequest(http.MethodPost, "/v1/feedback-requests", nil)
		rec := httptest.NewRecorder()
		e := echo.New()
		c := e.NewContext(req, rec)
		err := h.Create(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusUnauthorized)
		}
	})
}

func TestFeedbackRequestHandler_List(t *testing.T) {
	t.Run("direction and pagination flow to the service", func(t *testing.T) {
		var gotDir string
		var gotLimit int
		var gotCursor string
		svc := &fakeFeedbackRequestService{
			listFn: func(_ context.Context, _, direction string, limit int, cursorID string) ([]*model.FeedbackRequest, string, error) {
				gotDir, gotLimit, gotCursor = direction, limit, cursorID
				return []*model.FeedbackRequest{{ID: "req-1", RequesterID: "emp-2", RequesteeID: "emp-1", Status: model.FeedbackRequestStatusOpen}}, "req-1", nil
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, rec := newRequestContext(t, http.MethodGet, "/v1/me/feedback-requests?direction=sent&limit=5&cursor=req-0", "")
		if err := h.List(c); err != nil {
			t.Fatalf("List: %v", err)
		}
		if gotDir != "sent" || gotLimit != 5 || gotCursor != "req-0" {
			t.Fatalf("unexpected service args: dir=%q limit=%d cursor=%q", gotDir, gotLimit, gotCursor)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"requests":[`) || !strings.Contains(body, `"next_cursor":"req-1"`) {
			t.Fatalf("unexpected body: %s", body)
		}
	})

	t.Run("empty result returns empty array, not null", func(t *testing.T) {
		h := NewFeedbackRequestHandler(&fakeFeedbackRequestService{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, rec := newRequestContext(t, http.MethodGet, "/v1/me/feedback-requests", "")
		if err := h.List(c); err != nil {
			t.Fatalf("List: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"requests":[]`) || !strings.Contains(body, `"next_cursor":null`) {
			t.Fatalf("expected empty requests and null cursor, got %s", body)
		}
	})

	t.Run("unknown cursor maps to 400", func(t *testing.T) {
		svc := &fakeFeedbackRequestService{
			listFn: func(context.Context, string, string, int, string) ([]*model.FeedbackRequest, string, error) {
				return nil, "", apperror.ErrFeedbackRequestNotFound
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newRequestContext(t, http.MethodGet, "/v1/me/feedback-requests?cursor=x", "")
		err := h.List(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		// A not-found from the list path means the cursor was unknown: 400,
		// consistent with the other list endpoints.
		if he.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusBadRequest)
		}
		if he.Message != "unknown cursor" && he.Message != apperror.ErrFeedbackRequestNotFound.Error() {
			t.Fatalf("unexpected message: %v", he.Message)
		}
	})
}

func TestFeedbackRequestHandler_Decline(t *testing.T) {
	t.Run("success returns the declined request", func(t *testing.T) {
		svc := &fakeFeedbackRequestService{
			declineFn: func(_ context.Context, callerID, requestID string) (*model.FeedbackRequest, error) {
				if callerID != "emp-1" {
					t.Fatalf("caller must come from the JWT, got %q", callerID)
				}
				if requestID != "req-9" {
					t.Fatalf("request id must come from the path, got %q", requestID)
				}
				return &model.FeedbackRequest{ID: "req-9", RequesteeID: callerID, Status: model.FeedbackRequestStatusDeclined}, nil
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

		c, rec := newRequestContext(t, http.MethodPost, "/v1/feedback-requests/req-9/decline", "")
		c.SetPath("/v1/feedback-requests/:id/decline")
		c.SetPathValues(echo.PathValues{{Name: "id", Value: "req-9"}})
		if err := h.Decline(c); err != nil {
			t.Fatalf("Decline: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}
		if !strings.Contains(rec.Body.String(), `"status":"declined"`) {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
	})

	t.Run("not the requestee maps to 404", func(t *testing.T) {
		svc := &fakeFeedbackRequestService{
			declineFn: func(context.Context, string, string) (*model.FeedbackRequest, error) {
				return nil, apperror.ErrFeedbackRequestNotFound
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newRequestContext(t, http.MethodPost, "/v1/feedback-requests/req-9/decline", "")
		c.SetPath("/v1/feedback-requests/:id/decline")
		c.SetPathValues(echo.PathValues{{Name: "id", Value: "req-9"}})
		err := h.Decline(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusNotFound {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusNotFound)
		}
	})

	t.Run("service failure maps to 500", func(t *testing.T) {
		svc := &fakeFeedbackRequestService{
			declineFn: func(context.Context, string, string) (*model.FeedbackRequest, error) {
				return nil, errors.New("db down")
			},
		}
		h := NewFeedbackRequestHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
		c, _ := newRequestContext(t, http.MethodPost, "/v1/feedback-requests/req-9/decline", "")
		c.SetPath("/v1/feedback-requests/:id/decline")
		c.SetPathValues(echo.PathValues{{Name: "id", Value: "req-9"}})
		err := h.Decline(c)
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if he.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", he.Code, http.StatusInternalServerError)
		}
	})
}
