package dto

import (
	"time"

	"github.com/tsongpon/echo/internal/model"
)

// RegisterEmployeeRequest is the request body for POST /v1/register.
type RegisterEmployeeRequest struct {
	Name             string     `json:"name"`
	OrganizationName string     `json:"organization_name"`
	Role             model.Role `json:"role"`
	ManagerID        *string    `json:"manager_id,omitempty"`
	Title            string     `json:"title"`
	Email            string     `json:"email"`
	Password         string     `json:"password"`
	InviteToken      string     `json:"invite_token"`
}

// LoginRequest is the request body for POST /v1/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is returned by POST /v1/login on successful authentication.
type LoginResponse struct {
	AccessToken string           `json:"access_token"`
	TokenType   string           `json:"token_type"`
	ExpiresIn   int              `json:"expires_in"`
	Employee    EmployeeResponse `json:"employee"`
}

// EmployeeResponse is the representation of an employee returned to clients.
// It intentionally omits the password.
type EmployeeResponse struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	OrganizationName string     `json:"organization_name"`
	Role             model.Role `json:"role"`
	ManagerID        *string    `json:"manager_id,omitempty"`
	Title            string     `json:"title"`
	Email            string     `json:"email"`
	IsMailVerified   bool       `json:"is_mail_verified"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ToEmployee maps a register request to a domain Employee, without the
// lifecycle fields (ID/CreatedAt/UpdatedAt) which the repository assigns.
func (r RegisterEmployeeRequest) ToEmployee() *model.Employee {
	return &model.Employee{
		Name:             r.Name,
		OrganizationName: r.OrganizationName,
		Role:             r.Role,
		ManagerID:        r.ManagerID,
		Title:            r.Title,
		Email:            r.Email,
		Password:         r.Password,
	}
}

// ToEmployeeResponse maps a domain Employee to an API-safe response.
func ToEmployeeResponse(e *model.Employee) EmployeeResponse {
	if e == nil {
		return EmployeeResponse{}
	}
	return EmployeeResponse{
		ID:               e.ID,
		Name:             e.Name,
		OrganizationName: e.OrganizationName,
		Role:             e.Role,
		ManagerID:        e.ManagerID,
		Title:            e.Title,
		Email:            e.Email,
		IsMailVerified:   e.IsMailVerified,
		CreatedAt:        e.CreatedAt,
		UpdatedAt:        e.UpdatedAt,
	}
}

// EmployeeListResponse is the wrapper returned by GET /v1/employees. The
// employees slice is never nil: an organization with no members yet yields
// { "employees": [] }. NextCursor is the ID of the last employee on this page
// and is meant to be passed back as the cursor query parameter to fetch the
// next page; it is null when there are no more pages.
type EmployeeListResponse struct {
	Employees   []EmployeeResponse `json:"employees"`
	NextCursor  *string            `json:"next_cursor"`
}

// ToEmployeeListResponse maps a slice of domain Employee to the list response
// shape, ensuring a non-nil slice so the JSON encodes as []. nextCursorID is
// the ID of the last employee on the page; pass "" when there is no next page
// so NextCursor serializes as null.
func ToEmployeeListResponse(employees []*model.Employee, nextCursorID string) EmployeeListResponse {
	out := make([]EmployeeResponse, 0, len(employees))
	for _, e := range employees {
		out = append(out, ToEmployeeResponse(e))
	}
	var cursor *string
	if nextCursorID != "" {
		c := nextCursorID
		cursor = &c
	}
	return EmployeeListResponse{Employees: out, NextCursor: cursor}
}
