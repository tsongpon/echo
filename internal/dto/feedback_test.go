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
