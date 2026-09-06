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

// FeedbackStatus is the lifecycle state of a feedback entry. Entries are
// either drafts visible only to their author, or submitted entries that count
// toward the reviewee's feedback.
type FeedbackStatus string

const (
	// FeedbackStatusDraft marks a feedback entry that is still being written.
	// Drafts are visible only to their reviewer; they never appear in the
	// reviewee's or a manager's feedback listings and are not counted as
	// filed feedback.
	FeedbackStatusDraft FeedbackStatus = "draft"
	// FeedbackStatusSubmitted marks a feedback entry that has been filed. It
	// is visible to the reviewee and their manager (subject to the visibility
	// policy). Entries created via POST /v1/feedbacks are submitted
	// immediately; draft entries transition to submitted via the draft submit
	// endpoint. Documents written before the status field existed decode
	// with a "" status, which is normalized to submitted at read time.
	FeedbackStatusSubmitted FeedbackStatus = "submitted"
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
	Status             FeedbackStatus
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NormalizedStatus returns the effective status of the entry, mapping the
// zero value (a document written before the status field existed) to
// FeedbackStatusSubmitted. Callers validating or branching on status should
// use this rather than the raw field.
func (f *Feedback) NormalizedStatus() FeedbackStatus {
	if f.Status == "" {
		return FeedbackStatusSubmitted
	}
	return f.Status
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
