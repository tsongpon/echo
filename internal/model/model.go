package model

import "time"

type Organization struct {
	ID   string
	Name string
}

type Employee struct {
	ID             string
	Name           string
	OrganizationID string
	ManagerID      *string
	Title          string
	Email          string
	Password       string
	IsMailVerified bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type FeedbackVisibility string

const (
	FeedbackVisibilityPublic      FeedbackVisibility = "public"
	FeedbackVisibilityPrivate     FeedbackVisibility = "private"
	FeedbackVisibilityManagerOnly FeedbackVisibility = "manager_only"
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
	StrengthsComment   string
	WeaknessesComment  string
	Visibility         FeedbackVisibility
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type FeedbackPeriod struct {
	ID             string
	Name           string
	OrganizationID string
	StartDate      time.Time
	EndDate        time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
