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

// CreateFeedbackDraftRequest is the request body for POST /v1/feedback-drafts.
// As with the create endpoint, the reviewer is taken from the authenticated
// employee's JWT; reviewer_id in the body is ignored. Unlike a submitted
// entry, a draft requires only the reviewee and period: scores and comments
// may be filled in later via the update endpoint.
type CreateFeedbackDraftRequest struct {
	PeriodID           string                   `json:"period_id"`
	RevieweeID         string                   `json:"reviewee_id"`
	CommunicationScore *int                     `json:"communication_score"`
	LeadershipScore    *int                     `json:"leadership_score"`
	TechnicalScore     *int                     `json:"technical_score"`
	CollaborationScore *int                     `json:"collaboration_score"`
	DeliveryScore      *int                     `json:"delivery_score"`
	TrustScore         *int                     `json:"trust_score"`
	StrengthsComment   string                   `json:"strengths_comment"`
	WeaknessesComment  string                   `json:"weaknesses_comment"`
	Visibility         model.FeedbackVisibility `json:"visibility"`
}

// UpdateFeedbackDraftRequest is the request body for
// PATCH /v1/feedback-drafts/:id. All fields are pointers so the request can
// distinguish an omitted field (left unchanged) from an explicitly supplied
// one (overwritten); period_id, reviewee_id, and reviewer_id are fixed once
// the draft is created and are therefore not accepted here.
type UpdateFeedbackDraftRequest struct {
	CommunicationScore *int                      `json:"communication_score"`
	LeadershipScore    *int                      `json:"leadership_score"`
	TechnicalScore     *int                      `json:"technical_score"`
	CollaborationScore *int                      `json:"collaboration_score"`
	DeliveryScore      *int                      `json:"delivery_score"`
	TrustScore         *int                      `json:"trust_score"`
	StrengthsComment   *string                   `json:"strengths_comment"`
	WeaknessesComment  *string                   `json:"weaknesses_comment"`
	Visibility         *model.FeedbackVisibility `json:"visibility"`
}

// ToFeedback maps a draft create request to a domain Feedback in the draft
// state. Omitted scores decode to their zero values, which the service's
// draft validation accepts (draft scores are only bounds-checked, never
// required). The reviewer ID is set by the service from the authenticated
// caller.
func (r CreateFeedbackDraftRequest) ToFeedback() *model.Feedback {
	feedback := &model.Feedback{
		PeriodID:          r.PeriodID,
		RevieweeID:        r.RevieweeID,
		StrengthsComment:  r.StrengthsComment,
		WeaknessesComment: r.WeaknessesComment,
		Visibility:        r.Visibility,
		Status:            model.FeedbackStatusDraft,
	}
	if r.CommunicationScore != nil {
		feedback.CommunicationScore = *r.CommunicationScore
	}
	if r.LeadershipScore != nil {
		feedback.LeadershipScore = *r.LeadershipScore
	}
	if r.TechnicalScore != nil {
		feedback.TechnicalScore = *r.TechnicalScore
	}
	if r.CollaborationScore != nil {
		feedback.CollaborationScore = *r.CollaborationScore
	}
	if r.DeliveryScore != nil {
		feedback.DeliveryScore = *r.DeliveryScore
	}
	if r.TrustScore != nil {
		feedback.TrustScore = *r.TrustScore
	}
	return feedback
}

// FeedbackResponse is the representation of a feedback entry returned to
// clients. It mirrors model.Feedback so the client can show the scores,
// comments, visibility, lifecycle status, and lifecycle timestamps.
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
	Status             model.FeedbackStatus     `json:"status"`
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
		Status:             model.FeedbackStatusSubmitted,
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
		Status:             f.NormalizedStatus(),
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

// ToFeedbackManagerListResponse maps a slice of a reportee's feedback entries
// to the manager-view list response shape. Visibility policy: the manager
// never sees who wrote an entry — ReviewerID is blanked unconditionally,
// including for entries with visibility "named" (the reviewee still sees
// named reviewers in their own view; only the manager view is blinded).
// Comments are intentionally preserved in full. nextCursorID semantics match
// ToFeedbackListResponse.
func ToFeedbackManagerListResponse(feedbacks []*model.Feedback, nextCursorID string) FeedbackListResponse {
	out := make([]FeedbackResponse, 0, len(feedbacks))
	for _, f := range feedbacks {
		resp := ToFeedbackResponse(f)
		resp.ReviewerID = ""
		out = append(out, resp)
	}
	var cursor *string
	if nextCursorID != "" {
		c := nextCursorID
		cursor = &c
	}
	return FeedbackListResponse{Feedbacks: out, NextCursor: cursor}
}

// FeedbackDraftListResponse is the paginated wrapper returned by
// GET /v1/feedback-drafts: the authenticated caller's own draft entries,
// ordered by created_at descending. As with FeedbackListResponse the drafts
// slice is never nil.
type FeedbackDraftListResponse struct {
	Drafts     []FeedbackResponse `json:"drafts"`
	NextCursor *string            `json:"next_cursor"`
}

// ToFeedbackDraftListResponse maps a slice of the caller's drafts to the
// draft list response shape. The caller is the reviewer of every entry, so
// no reviewer redaction applies: an author always knows their own identity
// regardless of the entry's visibility. nextCursorID semantics match
// ToFeedbackListResponse.
func ToFeedbackDraftListResponse(drafts []*model.Feedback, nextCursorID string) FeedbackDraftListResponse {
	out := make([]FeedbackResponse, 0, len(drafts))
	for _, f := range drafts {
		out = append(out, ToFeedbackResponse(f))
	}
	var cursor *string
	if nextCursorID != "" {
		c := nextCursorID
		cursor = &c
	}
	return FeedbackDraftListResponse{Drafts: out, NextCursor: cursor}
}

// FeedbackGivenListResponse is the paginated wrapper returned by
// GET /v1/me/given-feedbacks: feedback entries the authenticated caller
// has submitted, ordered by created_at descending. As with
// FeedbackListResponse the feedbacks slice is never nil.
type FeedbackGivenListResponse struct {
	Feedbacks  []FeedbackResponse `json:"feedbacks"`
	NextCursor *string            `json:"next_cursor"`
}

// ToFeedbackGivenListResponse maps a slice of the caller's submitted
// feedback entries to the given-feedback list response shape. The caller is
// the reviewer of every entry, so no reviewer redaction applies: an author
// always knows their own identity regardless of the entry's visibility.
// nextCursorID semantics match ToFeedbackListResponse.
func ToFeedbackGivenListResponse(feedbacks []*model.Feedback, nextCursorID string) FeedbackGivenListResponse {
	out := make([]FeedbackResponse, 0, len(feedbacks))
	for _, f := range feedbacks {
		out = append(out, ToFeedbackResponse(f))
	}
	var cursor *string
	if nextCursorID != "" {
		c := nextCursorID
		cursor = &c
	}
	return FeedbackGivenListResponse{Feedbacks: out, NextCursor: cursor}
}
