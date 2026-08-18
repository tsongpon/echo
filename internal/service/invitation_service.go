package service

import (
	"log/slog"
	"strings"
	"time"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/handler"
	"github.com/tsongpon/echo/internal/model"
)

// Compile-time assertion that InvitationService satisfies the
// handler.InvitationService interface.
var _ handler.InvitationService = (*InvitationService)(nil)

// InvitationService is the application layer that issues and redeems
// organization-invitation tokens. A token is a self-contained signed JWT
// (see auth.InvitationTokenSigner): nothing is stored server-side, so
// creation and extraction are stateless and do not require a repository.
type InvitationService struct {
	signer *auth.InvitationTokenSigner
	logger *slog.Logger
}

// NewInvitationService creates an InvitationService backed by the given
// invitation-token signer. If logger is nil, slog.Default() is used.
func NewInvitationService(signer *auth.InvitationTokenSigner, logger *slog.Logger) *InvitationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &InvitationService{signer: signer, logger: logger}
}

// CreateInvitationToken issues a signed invitation token that lets the bearer
// register as a member of the named organization. creatorID is the employee ID
// of the inviter (carried as the token subject for auditing) and
// organizationName is the organization the invitee will join. If expiresAt is
// nil the token expires after the signer's default TTL; otherwise the
// caller-supplied time is used, which lets an admin shorten or extend an
// individual invitation's lifetime.
//
// The caller is responsible for authorization (i.e. that creatorID is actually
// permitted to invite to organizationName); this method only validates the
// inputs and issues the token.
func (s *InvitationService) CreateInvitationToken(creatorID, organizationName string, expiresAt *time.Time) (string, error) {
	if strings.TrimSpace(creatorID) == "" {
		return "", apperror.ErrInvalidEmployee("creator id is required")
	}
	if strings.TrimSpace(organizationName) == "" {
		return "", apperror.ErrInvalidEmployee("organization_name is required")
	}
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return "", apperror.ErrInvalidEmployee("expires_at must be in the future")
	}

	token, err := s.signer.Sign(creatorID, organizationName, expiresAt)
	if err != nil {
		s.logger.Error("failed to sign invitation token", "error", err, "creator_id", creatorID, "organization_name", organizationName)
		return "", err
	}
	return token, nil
}

// ExtractInvitationToken verifies a signed invitation token and returns the
// invitation it represents. Any token failure (malformed, expired, wrong-key,
// or wrong-purpose) is reported as apperror.ErrInvalidVerificationToken so
// callers can uniformly map it to a 400, mirroring the email-verification
// flow's error contract.
func (s *InvitationService) ExtractInvitationToken(token string) (*model.Invitation, error) {
	if strings.TrimSpace(token) == "" {
		return nil, apperror.ErrInvalidVerificationToken
	}

	claims, err := s.signer.Verify(token)
	if err != nil {
		return nil, apperror.ErrInvalidVerificationToken
	}

	var (
		id        string
		createdAt time.Time
		expiresAt time.Time
	)
	if claims.ID != "" {
		id = claims.ID
	}
	if claims.IssuedAt != nil {
		createdAt = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}

	return &model.Invitation{
		ID:               id,
		CreatedBy:        claims.Subject,
		CreatedAt:        createdAt,
		OrganizationName: claims.OrganizationName,
		ExpiresAt:        expiresAt,
	}, nil
}