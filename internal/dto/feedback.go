package dto

import (
	"time"

	"github.com/tsongpon/echo/internal/model"
)

// minScore and maxScore bound the six numeric score fields on a feedback entry.
// They mirror the Likert-style range used by the service-layer validation.
const (
	minScore = 1
	maxScore = 5
)

// CreateFeedbackRequest is the request body for POST /v1/feedbacks. The reviewer
// is taken from the authenticated employee's JWT rather than trusted from the
// body, so a client cannot file feedback on someone else's behalf; reviewer_id
// in the body is ignored on input.
type CreateFeedbackRequest struct {
	PeriodID           string                   `json:"period_id"`
	RevieweeID         string                   `json:"reviewee_id"`
	CommunicationScore int                      `json:"communication_score"`
	LeadershipScore    int                      `json:"leadership_score"`
	TechnicalScore     int                      `json:"technical_score"`
	CollaborationScore int                      `json:"collaboration_score"`
	DeliveryScore      int                      `json:"delivery_score"`
	TrustScore         int                      `json:"trust_score"`
	StrengthsComment   string                   `json:"strengths_comment"`
	WeaknessesComment  string                   `json:"weaknesses_comment"`
	Visibility         model.FeedbackVisibility `json:"visibility"`
}

// FeedbackResponse is the representation of a feedback entry returned to
// clients. It mirrors model.Feedback so the client can show the scores,
// comments, visibility, and lifecycle timestamps.
type FeedbackResponse struct {
	ID                 string                   `json:"id"`
	PeriodID           string                   `json:"period_id"`
	RevieweeID         string                   `json:"reviewee_id"`
	ReviewerID         string                   `json:"reviewer_id"`
	CommunicationScore int                      `json:"communication_score"`
	LeadershipScore    int                      `json:"leadership_score"`
	TechnicalScore     int                      `json:"technical_score"`
	CollaborationScore int                      `json:"collaboration_score"`
	DeliveryScore      int                      `json:"delivery_score"`
	TrustScore         int                      `json:"trust_score"`
	StrengthsComment   string                   `json:"strengths_comment"`
	WeaknessesComment  string                   `json:"weaknesses_comment"`
	Visibility         model.FeedbackVisibility `json:"visibility"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

// ToFeedback maps a create request to a domain Feedback, without the
// lifecycle fields (ID/CreatedAt/UpdatedAt) which the repository assigns. The
// reviewer ID is set by the service from the authenticated caller's JWT and is
// therefore left empty here.
func (r CreateFeedbackRequest) ToFeedback() *model.Feedback {
	return &model.Feedback{
		PeriodID:           r.PeriodID,
		RevieweeID:         r.RevieweeID,
		CommunicationScore: r.CommunicationScore,
		LeadershipScore:    r.LeadershipScore,
		TechnicalScore:     r.TechnicalScore,
		CollaborationScore: r.CollaborationScore,
		DeliveryScore:      r.DeliveryScore,
		TrustScore:         r.TrustScore,
		StrengthsComment:   r.StrengthsComment,
		WeaknessesComment:  r.WeaknessesComment,
		Visibility:         r.Visibility,
	}
}

// ToFeedbackResponse maps a domain Feedback to an API-safe response.
func ToFeedbackResponse(f *model.Feedback) FeedbackResponse {
	if f == nil {
		return FeedbackResponse{}
	}
	return FeedbackResponse{
		ID:                 f.ID,
		PeriodID:           f.PeriodID,
		RevieweeID:         f.RevieweeID,
		ReviewerID:         f.ReviewerID,
		CommunicationScore: f.CommunicationScore,
		LeadershipScore:    f.LeadershipScore,
		TechnicalScore:     f.TechnicalScore,
		CollaborationScore: f.CollaborationScore,
		DeliveryScore:      f.DeliveryScore,
		TrustScore:         f.TrustScore,
		StrengthsComment:   f.StrengthsComment,
		WeaknessesComment:  f.WeaknessesComment,
		Visibility:         f.Visibility,
		CreatedAt:          f.CreatedAt,
		UpdatedAt:          f.UpdatedAt,
	}
}

// FeedbackListResponse is the paginated wrapper returned by
// GET /v1/me/feedbacks. The feedbacks slice is never nil: an employee who has
// received no feedback yet yields { "feedbacks": [] }.
type FeedbackListResponse struct {
	Feedbacks  []FeedbackResponse `json:"feedbacks"`
	NextCursor *string            `json:"next_cursor"`
}

// ToFeedbackListResponse maps a slice of domain Feedback to the list response
// shape, ensuring a non-nil slice so the JSON encodes as []. nextCursorID is
// the ID of the last feedback on the page; pass "" when there is no next page
// so NextCursor serializes as null.
//
// Visibility policy: when an entry's visibility is anonymous, the reviewer's
// identity is hidden from the reviewee (who is the caller of this endpoint).
// The ReviewerID field is blanked for such entries before serialization. Named
// entries keep ReviewerID intact. This redaction is applied only here, not in
// ToFeedbackResponse, so the create endpoint still returns the reviewer's own
// ID to the reviewer themselves (the reviewer is allowed to know they wrote
// it).
func ToFeedbackListResponse(feedbacks []*model.Feedback, nextCursorID string) FeedbackListResponse {
	out := make([]FeedbackResponse, 0, len(feedbacks))
	for _, f := range feedbacks {
		resp := ToFeedbackResponse(f)
		if f != nil && f.Visibility == model.FeedbackVisibilityAnonymous {
			resp.ReviewerID = ""
		}
		out = append(out, resp)
	}
	var cursor *string
	if nextCursorID != "" {
		c := nextCursorID
		cursor = &c
	}
	return FeedbackListResponse{Feedbacks: out, NextCursor: cursor}
}