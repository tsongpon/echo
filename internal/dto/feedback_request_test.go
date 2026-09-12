package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tsongpon/echo/internal/model"
)

func TestToFeedbackRequestListResponse(t *testing.T) {
	t.Run("nil input yields empty array and null cursor", func(t *testing.T) {
		resp := ToFeedbackRequestListResponse(nil, "")
		if resp.Requests == nil || len(resp.Requests) != 0 {
			t.Fatalf("expected non-nil empty requests, got %v", resp.Requests)
		}
		out, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"requests":[],"next_cursor":null}`
		if string(out) != want {
			t.Fatalf("got %s, want %s", string(out), want)
		}
	})

	t.Run("requests and cursor are serialized", func(t *testing.T) {
		resp := ToFeedbackRequestListResponse([]*model.FeedbackRequest{
			{ID: "req-1", RequesterID: "emp-1", RequesteeID: "emp-2", PeriodID: "p-1", Status: model.FeedbackRequestStatusOpen},
		}, "req-1")
		out, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(out)
		if !strings.Contains(s, `"status":"open"`) || !strings.Contains(s, `"next_cursor":"req-1"`) {
			t.Fatalf("unexpected JSON: %s", s)
		}
	})
}
