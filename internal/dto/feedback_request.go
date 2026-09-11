package dto

import (
	"time"

	"github.com/tsongpon/echo/internal/model"
)

// CreateFeedbackRequestRequest is the request body for
// POST /v1/feedback-requests. The requester is taken from the JWT and
// requester_id in the body is ignored.
type CreateFeedbackRequestRequest struct {
	RequesteeID string `json:"requestee_id"`
	PeriodID    string `json:"period_id"`
}

// ToFeedbackRequest projects the request body onto the domain model. Only the
// requestee and period are set; identity and lifecycle fields are the
// service's to fill.
func (r CreateFeedbackRequestRequest) ToFeedbackRequest() *model.FeedbackRequest {
	return &model.FeedbackRequest{
		RequesteeID: r.RequesteeID,
		PeriodID:    r.PeriodID,
	}
}

// FeedbackRequestResponse is the representation of a feedback request
// returned to clients.
type FeedbackRequestResponse struct {
	ID          string                      `json:"id"`
	RequesterID string                      `json:"requester_id"`
	RequesteeID string                      `json:"requestee_id"`
	PeriodID    string                      `json:"period_id"`
	Status      model.FeedbackRequestStatus `json:"status"`
	CreatedAt   time.Time                   `json:"created_at"`
	UpdatedAt   time.Time                   `json:"updated_at"`
}

// ToFeedbackRequestResponse maps a domain feedback request to its response
// shape.
func ToFeedbackRequestResponse(r *model.FeedbackRequest) FeedbackRequestResponse {
	if r == nil {
		return FeedbackRequestResponse{}
	}
	return FeedbackRequestResponse{
		ID:          r.ID,
		RequesterID: r.RequesterID,
		RequesteeID: r.RequesteeID,
		PeriodID:    r.PeriodID,
		Status:      r.Status,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// FeedbackRequestListResponse is the paginated wrapper returned by
// GET /v1/me/feedback-requests. The requests slice is never nil: an employee
// with no requests yields { "requests": [] }.
type FeedbackRequestListResponse struct {
	Requests   []FeedbackRequestResponse `json:"requests"`
	NextCursor *string                   `json:"next_cursor"`
}

// ToFeedbackRequestListResponse maps a slice of domain feedback requests to
// the list response shape, ensuring a non-nil slice so the JSON encodes as
// []. nextCursorID is the ID of the last request on the page; pass "" when
// there is no next page so NextCursor serializes as null.
func ToFeedbackRequestListResponse(requests []*model.FeedbackRequest, nextCursorID string) FeedbackRequestListResponse {
	out := make([]FeedbackRequestResponse, 0, len(requests))
	for _, r := range requests {
		out = append(out, ToFeedbackRequestResponse(r))
	}
	var cursor *string
	if nextCursorID != "" {
		c := nextCursorID
		cursor = &c
	}
	return FeedbackRequestListResponse{Requests: out, NextCursor: cursor}
}
