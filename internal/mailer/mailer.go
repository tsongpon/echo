package mailer

import (
	"context"
	"log/slog"
)

// LogMailer is a Mailer implementation that does not send any real email. It
// logs the verification link via slog, which is sufficient for local development
// and tests. A production SMTP implementation can satisfy the service.Mailer
// interface independently.
type LogMailer struct {
	// BaseURL is the origin prepended to the verification link. If empty, a
	// local default is used.
	BaseURL string
	logger  *slog.Logger
}

// NewLogMailer creates a LogMailer. baseURL overrides the link origin; pass an
// empty string to use the local default. If logger is nil, slog.Default() is
// used.
func NewLogMailer(baseURL string, logger *slog.Logger) *LogMailer {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogMailer{BaseURL: baseURL, logger: logger}
}

// SendVerificationEmail logs a verification link containing the token. It never
// fails, so registration is not blocked by email delivery in local dev.
func (m *LogMailer) SendVerificationEmail(_ context.Context, to, token string) error {
	base := m.BaseURL
	if base == "" {
		base = "http://localhost:1323"
	}
	m.logger.Info("verification email", "to", to, "url", base+"/v1/verify-email?token="+token)
	return nil
}
