package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/dto"
	"github.com/tsongpon/echo/internal/model"
)

type InvitationService interface {
	CreateInvitationToken(creatorID, organizationName string, expiresAt *time.Time) (string, error)
	ExtractInvitationToken(token string) (*model.Invitation, error)
}

type InvitationHandler struct {
	invitationService InvitationService
}

// NewInvitationHandler creates an InvitationHandler backed by the given
// invitation service.
func NewInvitationHandler(invitationService InvitationService) *InvitationHandler {
	return &InvitationHandler{invitationService: invitationService}
}

// CreateInvitation handles POST /v1/invitation: issues a signed invitation
// token that lets the bearer register as a member of the inviter's
// organization. The inviter is the authenticated employee (taken from the JWT
// subject), so the route must be mounted behind the Auth middleware.
//
// The organization the invitee will join is taken from the verified JWT
// claims, never from the request body: an org_admin must not be able to mint
// join-tokens for an organization they do not administer (the C4 finding).
func (h *InvitationHandler) CreateInvitation(c *echo.Context) error {
	claims := ClaimsFromContext(c)
	if claims == nil {
		// Auth middleware should have already rejected the request; this guard
		// protects against accidental wiring without the middleware.
		return echo.NewHTTPError(http.StatusUnauthorized, "missing or invalid token")
	}

	if claims.Role != model.RoleOrgAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "only org admins can create invitations")
	}
	if strings.TrimSpace(claims.OrganizationName) == "" {
		// A signed token should always carry an organization; an empty one
		// means the token predates the claim or was misissued.
		return echo.NewHTTPError(http.StatusForbidden, "inviter token carries no organization")
	}

	var req dto.CreateInvitationRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	token, err := h.invitationService.CreateInvitationToken(claims.Subject, claims.OrganizationName, req.ExpiresAt)
	if err != nil {
		var invalid apperror.ErrInvalidEmployee
		if errors.As(err, &invalid) {
			return echo.NewHTTPError(http.StatusBadRequest, invalid.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create invitation")
	}

	// Round-trip the token through ExtractInvitationToken to build the
	// response from the verified claims rather than trusting the caller's
	// inputs. This also guarantees the response reflects exactly what the
	// bearer will get when they later present the token at registration.
	inv, err := h.invitationService.ExtractInvitationToken(token)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create invitation")
	}

	return c.JSON(http.StatusCreated, dto.ToInvitationResponse(token, inv))
}
