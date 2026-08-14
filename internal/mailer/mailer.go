package mailer

import (
	"context"
	"log"
)

// LogMailer is a Mailer implementation that does not send any real email. It
// logs the verification link to the standard logger, which is sufficient for
// local development and tests. A production SMTP implementation can satisfy
// the service.Mailer interface independently.
type LogMailer struct {
	// BaseURL is the origin prepended to the verification link. If empty, a
	// local default is used.
	BaseURL string
}

// NewLogMailer creates a LogMailer. baseURL overrides the link origin; pass an
// empty string to use the local default.
func NewLogMailer(baseURL string) *LogMailer {
	return &LogMailer{BaseURL: baseURL}
}

// SendVerificationEmail logs a verification link containing the token. It never
// fails, so registration is not blocked by email delivery in local dev.
func (m *LogMailer) SendVerificationEmail(_ context.Context, to, token string) error {
	base := m.BaseURL
	if base == "" {
		base = "http://localhost:1323"
	}
	log.Printf("verification email -> %s: %s/v1/verify-email?token=%s", to, base, token)
	return nil
}
