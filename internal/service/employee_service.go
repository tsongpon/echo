package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/model"
)

// ErrEmployeeNotFound is returned by repository lookups when no employee
// matches the query.
var ErrEmployeeNotFound = errors.New("employee not found")

// ErrInvalidCredentials is returned by authentication operations when the
// supplied email/password do not match a stored employee.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrInvalidVerificationToken is returned by VerifyEmail when the supplied
// token is malformed, expired, or does not correspond to a known employee. A
// single sentinel for all failure modes avoids leaking why a token was
// rejected.
var ErrInvalidVerificationToken = errors.New("invalid or expired verification token")

// Mailer is the consumer-defined contract for sending transactional email. It
// is intentionally minimal: only the operations the service actually needs.
// Concrete implementations (e.g. a logging mailer for local dev, an SMTP
// mailer for production) satisfy it implicitly.
type Mailer interface {
	SendVerificationEmail(ctx context.Context, to, token string) error
}

// EmployeeRepository is the consumer-defined contract for the employee
// repository. It is intentionally minimal: only the operations the service
// actually needs. The concrete repository implementation satisfies it
// implicitly.
type EmployeeRepository interface {
	Create(ctx context.Context, employee *model.Employee) (*model.Employee, error)
	GetByEmail(ctx context.Context, email string) (*model.Employee, error)
	GetByID(ctx context.Context, id string) (*model.Employee, error)
	Update(ctx context.Context, employee *model.Employee) (*model.Employee, error)
}

// EmployeeService is the application layer that orchestrates employee
// operations against an EmployeeRepository.
type EmployeeService struct {
	repo         EmployeeRepository
	mailer       Mailer
	verifySigner *auth.EmailVerificationTokenSigner
	logger       *slog.Logger
}

// NewEmployeeService creates an EmployeeService backed by the given
// repository, mailer, and email-verification token signer. If logger is nil,
// slog.Default() is used.
func NewEmployeeService(repo EmployeeRepository, mailer Mailer, verifySigner *auth.EmailVerificationTokenSigner, logger *slog.Logger) *EmployeeService {
	if logger == nil {
		logger = slog.Default()
	}
	return &EmployeeService{repo: repo, mailer: mailer, verifySigner: verifySigner, logger: logger}
}

// maxPasswordLen caps the incoming plaintext password length before
// hashing. bcrypt silently truncates inputs longer than 72 bytes, which can
// cause distinct passwords to hash identically. 64 bytes is safely below the
// bcrypt limit while allowing long passphrases.
const maxPasswordLen = 64

// Register creates a new employee after validating the input. The supplied
// plaintext password is hashed with bcrypt before being persisted; the
// plaintext is never stored. After a successful create it best-effort sends an
// email-verification token via the mailer: a delivery failure is logged but
// does not fail the registration, since the account is already usable and a
// resend endpoint can re-issue later.
func (s *EmployeeService) Register(ctx context.Context, employee *model.Employee) (*model.Employee, error) {
	if employee == nil {
		return nil, ErrInvalidEmployee("employee must not be nil")
	}
	if strings.TrimSpace(employee.Name) == "" {
		return nil, ErrInvalidEmployee("name is required")
	}
	if strings.TrimSpace(employee.Email) == "" {
		return nil, ErrInvalidEmployee("email is required")
	}
	if strings.TrimSpace(employee.OrganizationID) == "" {
		return nil, ErrInvalidEmployee("organization_id is required")
	}
	if employee.Password == "" {
		return nil, ErrInvalidEmployee("password is required")
	}
	if len(employee.Password) > maxPasswordLen {
		return nil, ErrInvalidEmployee("password must be at most 64 characters")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(employee.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	employee.IsMailVerified = false
	employee.Password = string(hashed)

	created, err := s.repo.Create(ctx, employee)
	if err != nil {
		return nil, err
	}

	if err := s.SendVerification(ctx, created); err != nil {
		s.logger.Error("failed to send verification email", "error", err, "employee_id", created.ID)
	}
	return created, nil
}

// Login authenticates an employee by email and password. On success it
// returns the matching employee; on any failure (unknown email or wrong
// password) it returns ErrInvalidCredentials. A single error for both cases
// avoids leaking which of the two was wrong.
func (s *EmployeeService) Login(ctx context.Context, email, password string) (*model.Employee, error) {
	if strings.TrimSpace(email) == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	employee, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		return nil, ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(employee.Password), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return employee, nil
}

// GetByID returns the employee with the given ID. It returns
// ErrEmployeeNotFound when no employee matches.
func (s *EmployeeService) GetByID(ctx context.Context, id string) (*model.Employee, error) {
	return s.repo.GetByID(ctx, id)
}

// SendVerification issues an email-verification token for the given employee
// and hands it to the mailer for delivery. The token is self-contained (a
// signed JWT); nothing is stored server-side.
func (s *EmployeeService) SendVerification(ctx context.Context, employee *model.Employee) error {
	if employee == nil {
		return ErrInvalidEmployee("employee must not be nil")
	}
	token, err := s.verifySigner.Sign(employee)
	if err != nil {
		return err
	}
	return s.mailer.SendVerificationEmail(ctx, employee.Email, token)
}

// VerifyEmail validates the supplied verification token and, on success, marks
// the corresponding employee's email as verified. It is idempotent: verifying
// an already-verified employee is a no-op that still succeeds.
func (s *EmployeeService) VerifyEmail(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return ErrInvalidVerificationToken
	}

	claims, err := s.verifySigner.Verify(token)
	if err != nil {
		return ErrInvalidVerificationToken
	}

	employee, err := s.repo.GetByID(ctx, claims.Subject)
	if err != nil {
		return ErrInvalidVerificationToken
	}

	employee.IsMailVerified = true
	if _, err := s.repo.Update(ctx, employee); err != nil {
		return err
	}
	return nil
}

// ErrInvalidEmployee indicates a validation failure of an employee input.
type ErrInvalidEmployee string

func (e ErrInvalidEmployee) Error() string { return string(e) }

// IsInvalidEmployee reports whether err is an ErrInvalidEmployee.
func IsInvalidEmployee(err error) bool {
	var target ErrInvalidEmployee
	return errors.As(err, &target)
}
