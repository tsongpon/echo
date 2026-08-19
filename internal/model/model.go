package model

import "time"

type Employee struct {
	ID               string
	Name             string
	Role             Role
	OrganizationName string
	ManagerID        *string
	Title            string
	Email            string
	Password         string
	IsMailVerified   bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Role is the authorization role of an employee within their organization.
type Role string

const (
	// RoleOrgAdmin is the role for an employee who can manage their
	// organization (e.g. invite new members). The first employee to register
	// without an invitation token is made an org admin.
	RoleOrgAdmin Role = "org_admin"
	// RoleUser is the default role for an employee who joins via an invitation.
	RoleUser Role = "user"
)

type FeedbackVisibility string

const (
	// FeedbackVisibilityAnonymous hides the reviewer's identity from the
	// reviewee. This is the default when no visibility is supplied.
	FeedbackVisibilityAnonymous FeedbackVisibility = "anonymous"
	// FeedbackVisibilityNamed attributes the feedback to its reviewer.
	FeedbackVisibilityNamed FeedbackVisibility = "named"
)

type Feedback struct {
	ID                 string
	PeriodID           string
	RevieweeID         string
	ReviewerID         string
	CommunicationScore int
	LeadershipScore    int
	TechnicalScore     int
	CollaborationScore int
	DeliveryScore      int
	TrustScore         int
	StrengthsComment   string
	WeaknessesComment  string
	Visibility         FeedbackVisibility
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type FeedbackPeriod struct {
	ID               string
	Name             string
	OrganizationName string
	StartDate        time.Time
	EndDate          time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Invitation struct {
	ID               string
	CreatedBy        string
	CreatedAt        time.Time
	OrganizationName string
	ExpiresAt        time.Time
}
