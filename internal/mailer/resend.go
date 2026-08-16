package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// DefaultResendAPIBase is the Resend API endpoint used when none is configured.
const DefaultResendAPIBase = "https://api.resend.com"

// DefaultResendFrom is the Resend sandbox sender address used when no from
// address is configured. It is suitable for testing on a verified Resend
// account; production deployments should pass a sender on a verified domain.
const DefaultResendFrom = "onboarding@resend.dev"

// resendEmailRequest is the JSON body posted to the Resend /emails endpoint.
type resendEmailRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

// ResendMailer is a Mailer implementation that delivers email through the
// Resend HTTP API (https://resend.com/docs/api-reference/emails). It satisfies
// service.Mailer implicitly; no SDK dependency is required.
type ResendMailer struct {
	apiKey     string
	from       string
	baseURL    string // origin prepended to the verification link
	apiBase    string // Resend API base, defaults to DefaultResendAPIBase
	httpClient *http.Client
	logger     *slog.Logger
}

// NewResendMailer creates a ResendMailer. baseURL is the origin used to build
// the verification link (e.g. "https://app.example.com"); if empty, a local
// default is used. client may be nil to use http.DefaultClient. The logger
// defaults to slog.Default() when nil.
func NewResendMailer(apiKey, from, baseURL string, client *http.Client) *ResendMailer {
	if from == "" {
		from = DefaultResendFrom
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &ResendMailer{
		apiKey:     apiKey,
		from:       from,
		baseURL:    baseURL,
		apiBase:    DefaultResendAPIBase,
		httpClient: client,
		logger:     slog.Default(),
	}
}

// SendVerificationEmail composes a verification email for the given recipient
// and sends it through the Resend API. The token is embedded as the `token`
// query parameter of the verification link, which points at GET /v1/verify-email.
func (m *ResendMailer) SendVerificationEmail(ctx context.Context, to, token string) error {
	link := m.verificationLink(token)
	subject, html, text := composeVerificationEmail(to, link)

	body, err := json.Marshal(resendEmailRequest{
		From:    m.from,
		To:      []string{to},
		Subject: subject,
		HTML:    html,
		Text:    text,
	})
	if err != nil {
		return fmt.Errorf("marshal resend request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.apiBase+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send resend request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("resend API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	m.logger.Info("verification email sent", "to", to, "from", m.from)
	return nil
}

// verificationLink builds the click target embedded in the email body.
func (m *ResendMailer) verificationLink(token string) string {
	base := m.baseURL
	if base == "" {
		base = "http://localhost:1323"
	}
	return base + "/v1/verify-email?token=" + token
}

// composeVerificationEmail builds the subject and the HTML/text bodies for the
// email-address verification message. The link is the only variable content,
// so the bodies are constant aside from it. The token expires after 24 hours
// (see auth.DefaultVerificationTTL), which is reflected in the copy.
func composeVerificationEmail(_, link string) (subject, html, text string) {
	subject = "Verify your email address"

	text = "Welcome to 360°Review!\n\n" +
		"Please verify your email address by opening the link below. The link expires in 24 hours.\n\n" +
		link + "\n\n" +
		"If you did not create an account, you can safely ignore this email.\n"

	html = `<!doctype html>
<html lang="en">
  <body style="font-family: Arial, Helvetica, sans-serif; color: #1f2937; max-width: 560px; margin: 0 auto; padding: 24px;">
    <h1 style="font-size: 20px; margin: 0 0 16px;">Verify your email address</h1>
    <p style="margin: 0 0 16px;">Welcome to 360°Review! Confirm that this is your email address by clicking the button below. The link expires in 24 hours.</p>
    <p style="margin: 0 0 24px;">
      <a href="` + link + `" style="display: inline-block; background-color: #2563eb; color: #ffffff; text-decoration: none; padding: 12px 20px; border-radius: 6px; font-weight: 600;">Verify email</a>
    </p>
    <p style="margin: 0 0 16px; font-size: 14px; color: #6b7280;">If the button above does not work, copy and paste this link into your browser:</p>
    <p style="margin: 0 0 24px; font-size: 14px; color: #6b7280; word-break: break-all;">` + link + `</p>
    <p style="margin: 0; font-size: 14px; color: #6b7280;">If you did not create an account, you can safely ignore this email.</p>
  </body>
</html>`
	return subject, html, text
}
