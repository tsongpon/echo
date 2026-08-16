package mailer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeRoundTripper captures the outbound request and returns a canned response
// without touching the network. It lets the ResendMailer be tested without
// binding a loopback listener (which the sandbox disallows).
type fakeRoundTripper struct {
	status  int
	body    string
	gotReq  *http.Request
	gotBody []byte
}

func (rt *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.gotReq = req
	b, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	rt.gotBody = b
	return &http.Response{
		StatusCode: rt.status,
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     make(http.Header),
	}, nil
}

func newTestResendMailer(rt *fakeRoundTripper) *ResendMailer {
	m := NewResendMailer("test-key", "noreply@example.com", "https://app.example.com", nil)
	m.httpClient = &http.Client{Transport: rt}
	return m
}

func TestResendMailer_SendVerificationEmail(t *testing.T) {
	rt := &fakeRoundTripper{status: http.StatusOK, body: `{"id":"abc"}`}
	m := newTestResendMailer(rt)

	if err := m.SendVerificationEmail(context.Background(), "alice@example.com", "tok-123"); err != nil {
		t.Fatalf("SendVerificationEmail: %v", err)
	}

	if rt.gotReq == nil {
		t.Fatal("expected an HTTP request to be made")
	}
	if method := rt.gotReq.Method; method != http.MethodPost {
		t.Fatalf("method = %q, want POST", method)
	}
	if path := rt.gotReq.URL.Path; path != "/emails" {
		t.Fatalf("request path = %q, want /emails", path)
	}
	if got := rt.gotReq.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", got)
	}
	if ct := rt.gotReq.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var payload resendEmailRequest
	if err := json.Unmarshal(rt.gotBody, &payload); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if payload.From != "noreply@example.com" {
		t.Fatalf("from = %q, want noreply@example.com", payload.From)
	}
	if len(payload.To) != 1 || payload.To[0] != "alice@example.com" {
		t.Fatalf("to = %v, want [alice@example.com]", payload.To)
	}
	if payload.Subject != "Verify your email address" {
		t.Fatalf("subject = %q", payload.Subject)
	}

	wantLink := "https://app.example.com/v1/verify-email?token=tok-123"
	if !strings.Contains(payload.HTML, wantLink) {
		t.Fatalf("html body missing verification link %q: %s", wantLink, payload.HTML)
	}
	if !strings.Contains(payload.Text, wantLink) {
		t.Fatalf("text body missing verification link %q: %s", wantLink, payload.Text)
	}
}

func TestResendMailer_APIError(t *testing.T) {
	rt := &fakeRoundTripper{status: http.StatusUnauthorized, body: `{"message":"invalid api key"}`}
	m := newTestResendMailer(rt)

	err := m.SendVerificationEmail(context.Background(), "alice@example.com", "tok")
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("expected error to mention status 401, got %v", err)
	}
}

func TestResendMailer_DefaultsBaseURL(t *testing.T) {
	rt := &fakeRoundTripper{status: http.StatusOK, body: `{}`}
	m := NewResendMailer("k", "noreply@example.com", "", nil) // baseURL empty -> local default
	m.httpClient = &http.Client{Transport: rt}

	if err := m.SendVerificationEmail(context.Background(), "a@example.com", "t"); err != nil {
		t.Fatalf("SendVerificationEmail: %v", err)
	}

	var payload resendEmailRequest
	if err := json.Unmarshal(rt.gotBody, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(payload.Text, "http://localhost:1323/v1/verify-email?token=t") {
		t.Fatalf("expected local default link, got: %s", payload.Text)
	}
}

func TestComposeVerificationEmail_ContainsLink(t *testing.T) {
	const link = "https://app.example.com/v1/verify-email?token=abc"
	subject, html, text := composeVerificationEmail("a@example.com", link)
	if subject != "Verify your email address" {
		t.Fatalf("subject = %q", subject)
	}
	if !strings.Contains(html, link) {
		t.Fatalf("html missing link")
	}
	if !strings.Contains(text, link) {
		t.Fatalf("text missing link")
	}
	if !strings.Contains(text, "24 hours") {
		t.Fatalf("text should mention 24-hour expiry")
	}
	if !strings.Contains(text, "360°Review") {
		t.Fatalf("text should reference the app name 360°Review")
	}
	if !strings.Contains(html, "360°Review") {
		t.Fatalf("html should reference the app name 360°Review")
	}
}
