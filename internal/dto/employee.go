package dto

import (
	"time"

	"github.com/tsongpon/echo/internal/model"
)

// RegisterEmployeeRequest is the request body for POST /v1/register.
type RegisterEmployeeRequest struct {
	Name             string  `json:"name"`
	OrganizationName string  `json:"organization_name"`
	ManagerID        *string `json:"manager_id,omitempty"`
	Title            string  `json:"title"`
	Email            string  `json:"email"`
	Password         string  `json:"password"`
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
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	OrganizationName string    `json:"organization_name"`
	ManagerID        *string   `json:"manager_id,omitempty"`
	Title            string    `json:"title"`
	Email            string    `json:"email"`
	IsMailVerified   bool      `json:"is_mail_verified"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ToEmployee maps a register request to a domain Employee, without the
// lifecycle fields (ID/CreatedAt/UpdatedAt) which the repository assigns.
func (r RegisterEmployeeRequest) ToEmployee() *model.Employee {
	return &model.Employee{
		Name:             r.Name,
		OrganizationName: r.OrganizationName,
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
		ManagerID:        e.ManagerID,
		Title:            e.Title,
		Email:            e.Email,
		IsMailVerified:   e.IsMailVerified,
		CreatedAt:        e.CreatedAt,
		UpdatedAt:        e.UpdatedAt,
	}
}
