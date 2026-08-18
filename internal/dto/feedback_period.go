package dto

import (
	"time"

	"github.com/tsongpon/echo/internal/model"
)

// CreateFeedbackPeriodRequest is the request body for POST /v1/feedback-periods.
// OrganizationName is taken from the authenticated employee's JWT rather than
// trusted from the body, so a client cannot create a period for an org they do
// not belong to; the field here is ignored on input.
type CreateFeedbackPeriodRequest struct {
	Name      string     `json:"name"`
	StartDate time.Time  `json:"start_date"`
	EndDate   *time.Time `json:"end_date,omitempty"`
}

// FeedbackPeriodResponse is the representation of a feedback period returned to
// clients. It mirrors model.FeedbackPeriod so the client can show the window,
// the organization it belongs to, and the lifecycle timestamps.
type FeedbackPeriodResponse struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	OrganizationName string    `json:"organization_name"`
	StartDate        time.Time `json:"start_date"`
	EndDate          time.Time `json:"end_date"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ToFeedbackPeriod maps a create request to a domain FeedbackPeriod, without the
// lifecycle fields (ID/CreatedAt/UpdatedAt) which the repository assigns. The
// organization name is set by the service from the authenticated caller's JWT
// and is therefore left empty here.
func (r CreateFeedbackPeriodRequest) ToFeedbackPeriod() *model.FeedbackPeriod {
	end := time.Time{}
	if r.EndDate != nil {
		end = *r.EndDate
	}
	return &model.FeedbackPeriod{
		Name:      r.Name,
		StartDate: r.StartDate,
		EndDate:   end,
	}
}

// ToFeedbackPeriodResponse maps a domain FeedbackPeriod to an API-safe response.
func ToFeedbackPeriodResponse(p *model.FeedbackPeriod) FeedbackPeriodResponse {
	if p == nil {
		return FeedbackPeriodResponse{}
	}
	return FeedbackPeriodResponse{
		ID:               p.ID,
		Name:             p.Name,
		OrganizationName: p.OrganizationName,
		StartDate:        p.StartDate,
		EndDate:          p.EndDate,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

// FeedbackPeriodListResponse is the paginated-style wrapper returned by
// GET /v1/feedback-periods. The periods slice is never nil: an organization
// with no periods yet yields { "periods": [] }.
type FeedbackPeriodListResponse struct {
	Periods []FeedbackPeriodResponse `json:"periods"`
}

// ToFeedbackPeriodListResponse maps a slice of domain FeedbackPeriod to the
// list response shape, ensuring a non-nil slice so the JSON encodes as [].
func ToFeedbackPeriodListResponse(periods []*model.FeedbackPeriod) FeedbackPeriodListResponse {
	out := make([]FeedbackPeriodResponse, 0, len(periods))
	for _, p := range periods {
		out = append(out, ToFeedbackPeriodResponse(p))
	}
	return FeedbackPeriodListResponse{Periods: out}
}