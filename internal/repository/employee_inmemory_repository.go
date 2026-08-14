package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/tsongpon/echo/internal/model"
	"github.com/tsongpon/echo/internal/service"
)

// Compile-time assertion that EmployeeInMemoryRepository satisfies the
// service.EmployeeRepository interface.
var _ service.EmployeeRepository = (*EmployeeInMemoryRepository)(nil)

// EmployeeInMemoryRepository is a thread-safe in-memory implementation of
// service.EmployeeRepository. It stores employee records keyed by their ID.
type EmployeeInMemoryRepository struct {
	mu        sync.RWMutex
	employees map[string]*model.Employee
}

// NewEmployeeInMemoryRepository creates a new empty EmployeeInMemoryRepository.
func NewEmployeeInMemoryRepository() *EmployeeInMemoryRepository {
	return &EmployeeInMemoryRepository{
		employees: make(map[string]*model.Employee),
	}
}

// Create stores a copy of the given employee, assigning it a new ID
// and timestamps, and returns the stored employee.
func (r *EmployeeInMemoryRepository) Create(ctx context.Context, employee *model.Employee) (*model.Employee, error) {
	if employee == nil {
		return nil, ErrNilEmployee
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	stored := *employee
	stored.ID = id
	stored.CreatedAt = now
	stored.UpdatedAt = now

	r.mu.Lock()
	r.employees[id] = &stored
	r.mu.Unlock()

	// Return a copy so callers can't mutate the stored record.
	result := stored
	return &result, nil
}

// GetByEmail returns the employee with the given email. Email comparison is
// case-insensitive. Returns service.ErrEmployeeNotFound when no match exists.
func (r *EmployeeInMemoryRepository) GetByEmail(_ context.Context, email string) (*model.Employee, error) {
	if email == "" {
		return nil, service.ErrEmployeeNotFound
	}

	target := strings.ToLower(strings.TrimSpace(email))

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, stored := range r.employees {
		if strings.ToLower(stored.Email) == target {
			result := *stored
			return &result, nil
		}
	}
	return nil, service.ErrEmployeeNotFound
}

// GetByID returns the employee with the given ID. Returns
// service.ErrEmployeeNotFound when no match exists.
func (r *EmployeeInMemoryRepository) GetByID(_ context.Context, id string) (*model.Employee, error) {
	if id == "" {
		return nil, service.ErrEmployeeNotFound
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	stored, ok := r.employees[id]
	if !ok {
		return nil, service.ErrEmployeeNotFound
	}

	result := *stored
	return &result, nil
}

// Update overwrites the mutable fields of the stored employee with the given
// ID and returns the updated record. ID and CreatedAt are preserved; UpdatedAt
// is refreshed. Returns service.ErrEmployeeNotFound when the ID is unknown.
func (r *EmployeeInMemoryRepository) Update(_ context.Context, employee *model.Employee) (*model.Employee, error) {
	if employee == nil {
		return nil, ErrNilEmployee
	}
	if employee.ID == "" {
		return nil, service.ErrEmployeeNotFound
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	stored, ok := r.employees[employee.ID]
	if !ok {
		return nil, service.ErrEmployeeNotFound
	}

	stored.Name = employee.Name
	stored.OrganizationID = employee.OrganizationID
	stored.ManagerID = employee.ManagerID
	stored.Title = employee.Title
	stored.Email = employee.Email
	stored.Password = employee.Password
	stored.IsMailVerified = employee.IsMailVerified
	stored.UpdatedAt = time.Now().UTC()

	result := *stored
	return &result, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
