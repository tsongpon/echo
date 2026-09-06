package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tsongpon/echo/internal/model"
)

func TestToFeedbackListResponse_RedactsAnonymousReviewer(t *testing.T) {
	feedbacks := []*model.Feedback{
		{
			ID:         "fb-named",
			RevieweeID: "emp-1",
			ReviewerID: "emp-2",
			Visibility: model.FeedbackVisibilityNamed,
		},
		{
			ID:         "fb-anon",
			RevieweeID: "emp-1",
			ReviewerID: "emp-3",
			Visibility: model.FeedbackVisibilityAnonymous,
		},
	}

	resp := ToFeedbackListResponse(feedbacks, "fb-anon")

	if len(resp.Feedbacks) != 2 {
		t.Fatalf("got %d feedbacks, want 2", len(resp.Feedbacks))
	}
	// Named entry keeps reviewer_id; anonymous entry must have it blanked.
	if resp.Feedbacks[0].ReviewerID != "emp-2" {
		t.Fatalf("named entry reviewer_id = %q, want emp-2", resp.Feedbacks[0].ReviewerID)
	}
	if resp.Feedbacks[1].ReviewerID != "" {
		t.Fatalf("anonymous entry reviewer_id = %q, want empty (redacted)", resp.Feedbacks[1].ReviewerID)
	}
	if resp.NextCursor == nil || *resp.NextCursor != "fb-anon" {
		t.Fatalf("next_cursor = %v, want fb-anon", resp.NextCursor)
	}

	// JSON serialization must surface the redaction and a string next_cursor.
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"reviewer_id":"emp-2"`) {
		t.Fatalf("expected named reviewer_id in JSON, got %s", s)
	}
	if strings.Contains(s, `"reviewer_id":"emp-3"`) {
		t.Fatalf("anonymous reviewer_id emp-3 must not appear in JSON: %s", s)
	}
	if !strings.Contains(s, `"next_cursor":"fb-anon"`) {
		t.Fatalf("expected next_cursor fb-anon in JSON, got %s", s)
	}
}

func TestToFeedbackListResponse_EmptyAndNoNextCursor(t *testing.T) {
	resp := ToFeedbackListResponse(nil, "")
	if resp.Feedbacks == nil {
		t.Fatal("expected non-nil feedbacks slice for nil input")
	}
	if len(resp.Feedbacks) != 0 {
		t.Fatalf("got %d feedbacks, want 0", len(resp.Feedbacks))
	}
	if resp.NextCursor != nil {
		t.Fatalf("expected nil next_cursor for empty page, got %v", *resp.NextCursor)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"feedbacks":[],"next_cursor":null}`
	if string(out) != want {
		t.Fatalf("got %s, want %s", string(out), want)
	}
}

func TestToFeedbackResponse_UnchangedForCreatePath(t *testing.T) {
	// ToFeedbackResponse is used by POST /v1/feedbacks to return the entry to
	// its reviewer, who is allowed to know they wrote it. The redaction lives
	// only in ToFeedbackListResponse, so this mapper must NOT blank
	// reviewer_id even for anonymous entries.
	f := &model.Feedback{
		ID:         "fb-1",
		ReviewerID: "emp-2",
		Visibility: model.FeedbackVisibilityAnonymous,
	}
	resp := ToFeedbackResponse(f)
	if resp.ReviewerID != "emp-2" {
		t.Fatalf("ToFeedbackResponse redacted reviewer_id to %q; it must preserve it (reviewer is allowed to know they wrote it)", resp.ReviewerID)
	}
}

func TestCreateFeedbackDraftRequest_ToFeedback(t *testing.T) {
	t.Run("minimal request maps to an empty draft", func(t *testing.T) {
		req := CreateFeedbackDraftRequest{PeriodID: "period-1", RevieweeID: "emp-2"}
		f := req.ToFeedback()
		if f.PeriodID != "period-1" || f.RevieweeID != "emp-2" {
			t.Fatalf("got period/reviewee %q/%q", f.PeriodID, f.RevieweeID)
		}
		if f.Status != model.FeedbackStatusDraft {
			t.Fatalf("got status %q, want draft", f.Status)
		}
		if f.CommunicationScore != 0 || f.TrustScore != 0 {
			t.Fatalf("expected zero scores for omitted fields, got %d/%d", f.CommunicationScore, f.TrustScore)
		}
	})

	t.Run("supplied scores are mapped", func(t *testing.T) {
		four, five := 4, 5
		req := CreateFeedbackDraftRequest{PeriodID: "period-1", RevieweeID: "emp-2", CommunicationScore: &four, TrustScore: &five}
		f := req.ToFeedback()
		if f.CommunicationScore != 4 || f.TrustScore != 5 {
			t.Fatalf("got scores %d/%d, want 4/5", f.CommunicationScore, f.TrustScore)
		}
	})

	t.Run("json decoding distinguishes omitted from zero scores", func(t *testing.T) {
		// A supplied 0 must decode to a non-nil pointer so the update path
		// can overwrite; an omitted score must stay nil so it is left alone.
		var req UpdateFeedbackDraftRequest
		if err := json.Unmarshal([]byte(`{"communication_score":0}`), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.CommunicationScore == nil || *req.CommunicationScore != 0 {
			t.Fatalf("expected explicit 0 to decode to a non-nil pointer, got %v", req.CommunicationScore)
		}
		if req.LeadershipScore != nil {
			t.Fatalf("expected omitted leadership_score to stay nil, got %v", *req.LeadershipScore)
		}
	})
}

func TestToFeedbackResponse_StatusNormalized(t *testing.T) {
	t.Run("blank status (legacy document) reports submitted", func(t *testing.T) {
		f := &model.Feedback{ID: "fb-1", ReviewerID: "emp-2"}
		resp := ToFeedbackResponse(f)
		if resp.Status != model.FeedbackStatusSubmitted {
			t.Fatalf("got status %q, want submitted (normalized)", resp.Status)
		}
	})

	t.Run("draft status preserved", func(t *testing.T) {
		f := &model.Feedback{ID: "draft-1", Status: model.FeedbackStatusDraft}
		resp := ToFeedbackResponse(f)
		if resp.Status != model.FeedbackStatusDraft {
			t.Fatalf("got status %q, want draft", resp.Status)
		}
	})
}

func TestToFeedbackDraftListResponse(t *testing.T) {
	t.Run("author sees reviewer identity even on anonymous drafts", func(t *testing.T) {
		drafts := []*model.Feedback{
			{ID: "draft-1", ReviewerID: "emp-1", Visibility: model.FeedbackVisibilityAnonymous, Status: model.FeedbackStatusDraft},
		}
		resp := ToFeedbackDraftListResponse(drafts, "")
		if len(resp.Drafts) != 1 {
			t.Fatalf("got %d drafts, want 1", len(resp.Drafts))
		}
		if resp.Drafts[0].ReviewerID != "emp-1" {
			t.Fatalf("expected reviewer_id preserved for the author, got %q", resp.Drafts[0].ReviewerID)
		}
	})

	t.Run("nil input yields empty array and null cursor", func(t *testing.T) {
		resp := ToFeedbackDraftListResponse(nil, "")
		if resp.Drafts == nil || len(resp.Drafts) != 0 {
			t.Fatalf("expected non-nil empty drafts, got %v", resp.Drafts)
		}
		out, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"drafts":[],"next_cursor":null}`
		if string(out) != want {
			t.Fatalf("got %s, want %s", string(out), want)
		}
	})
}

func TestToFeedbackGivenListResponse(t *testing.T) {
	t.Run("author sees reviewer identity even on anonymous entries", func(t *testing.T) {
		feedbacks := []*model.Feedback{
			{ID: "fb-1", ReviewerID: "emp-1", Visibility: model.FeedbackVisibilityAnonymous, Status: model.FeedbackStatusSubmitted},
		}
		resp := ToFeedbackGivenListResponse(feedbacks, "")
		if len(resp.Feedbacks) != 1 {
			t.Fatalf("got %d feedbacks, want 1", len(resp.Feedbacks))
		}
		if resp.Feedbacks[0].ReviewerID != "emp-1" {
			t.Fatalf("expected reviewer_id preserved for the author, got %q", resp.Feedbacks[0].ReviewerID)
		}
	})

	t.Run("nil input yields empty array and null cursor", func(t *testing.T) {
		resp := ToFeedbackGivenListResponse(nil, "")
		if resp.Feedbacks == nil || len(resp.Feedbacks) != 0 {
			t.Fatalf("expected non-nil empty feedbacks, got %v", resp.Feedbacks)
		}
		out, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"feedbacks":[],"next_cursor":null}`
		if string(out) != want {
			t.Fatalf("got %s, want %s", string(out), want)
		}
	})

	t.Run("next cursor is serialized when set", func(t *testing.T) {
		resp := ToFeedbackGivenListResponse(nil, "fb-9")
		out, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"feedbacks":[],"next_cursor":"fb-9"}`
		if string(out) != want {
			t.Fatalf("got %s, want %s", string(out), want)
		}
	})
}
