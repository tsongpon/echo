package dto

import (
	"time"

	"github.com/tsongpon/echo/internal/model"
)

// CreateInvitationRequest is the request body for POST /v1/invitation.
type CreateInvitationRequest struct {
	OrganizationName string     `json:"organization_name"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

// InvitationResponse is the representation of an invitation returned to
// clients. The token is the signed JWT the bearer presents at registration;
// the remaining fields mirror model.Invitation so the client can show who
// created it, when, and when it lapses.
type InvitationResponse struct {
	Token            string    `json:"token"`
	ID               string    `json:"id"`
	CreatedBy        string    `json:"created_by"`
	OrganizationName string    `json:"organization_name"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// ToInvitationResponse maps a domain invitation and its signed token to an
// API-safe response.
func ToInvitationResponse(token string, inv *model.Invitation) InvitationResponse {
	if inv == nil {
		return InvitationResponse{Token: token}
	}
	return InvitationResponse{
		Token:            token,
		ID:               inv.ID,
		CreatedBy:        inv.CreatedBy,
		OrganizationName: inv.OrganizationName,
		CreatedAt:        inv.CreatedAt,
		ExpiresAt:        inv.ExpiresAt,
	}
}