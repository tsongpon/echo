package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"golang.org/x/crypto/bcrypt"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/model"
)

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
		return nil, apperror.ErrInvalidEmployee("employee must not be nil")
	}
	if strings.TrimSpace(employee.Name) == "" {
		return nil, apperror.ErrInvalidEmployee("name is required")
	}
	if strings.TrimSpace(employee.Email) == "" {
		return nil, apperror.ErrInvalidEmployee("email is required")
	}
	if strings.TrimSpace(employee.OrganizationID) == "" {
		return nil, apperror.ErrInvalidEmployee("organization_id is required")
	}
	if employee.Password == "" {
		return nil, apperror.ErrInvalidEmployee("password is required")
	}
	if len(employee.Password) > maxPasswordLen {
		return nil, apperror.ErrInvalidEmployee("password must be at most 64 characters")
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(employee.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	employee.IsMailVerified = false
	employee.Password = string(hashed)
	id, err := uuid.NewV7() // (UUID, error) — time-ordered
	if err != nil {
		s.logger.Error("failed to generate UUID", "error", err)
		return nil, err
	}
	employee.ID = id.String()
	employee.Email = strings.ToLower(employee.Email)

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
// returns the matching employee; on any credential failure (unknown email or
// wrong password) it returns apperror.ErrInvalidCredentials. A single error
// for both cases avoids leaking which of the two was wrong.
//
// If the credentials are valid but the employee's email is not yet verified,
// Login returns apperror.ErrEmailNotVerified. The verification status is only
// checked after the password is confirmed, so an attacker without the password
// cannot learn whether an account exists or is unverified.
func (s *EmployeeService) Login(ctx context.Context, email, password string) (*model.Employee, error) {
	if strings.TrimSpace(email) == "" || password == "" {
		return nil, apperror.ErrInvalidCredentials
	}

	employee, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		return nil, apperror.ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(employee.Password), []byte(password)); err != nil {
		return nil, apperror.ErrInvalidCredentials
	}

	if !employee.IsMailVerified {
		return nil, apperror.ErrEmailNotVerified
	}

	return employee, nil
}

// GetByID returns the employee with the given ID. It returns
// apperror.ErrEmployeeNotFound when no employee matches.
func (s *EmployeeService) GetByID(ctx context.Context, id string) (*model.Employee, error) {
	return s.repo.GetByID(ctx, id)
}

// SendVerification issues an email-verification token for the given employee
// and hands it to the mailer for delivery. The token is self-contained (a
// signed JWT); nothing is stored server-side.
func (s *EmployeeService) SendVerification(ctx context.Context, employee *model.Employee) error {
	if employee == nil {
		return apperror.ErrInvalidEmployee("employee must not be nil")
	}
	token, err := s.verifySigner.Sign(employee)
	if err != nil {
		return err
	}
	return s.mailer.SendVerificationEmail(ctx, employee.Email, token)
}

// VerifyEmail validates the supplied verification token and, on success, marks
// the corresponding employee's email as verified. It is idempotent: verifying
// an already-verified employee is a no-op that still succeeds. Any token
// failure (malformed, expired, wrong-key, or unknown employee) is reported as
// apperror.ErrInvalidVerificationToken.
func (s *EmployeeService) VerifyEmail(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return apperror.ErrInvalidVerificationToken
	}

	claims, err := s.verifySigner.Verify(token)
	if err != nil {
		return apperror.ErrInvalidVerificationToken
	}

	employee, err := s.repo.GetByID(ctx, claims.Subject)
	if err != nil {
		return apperror.ErrInvalidVerificationToken
	}

	employee.IsMailVerified = true
	if _, err := s.repo.Update(ctx, employee); err != nil {
		return err
	}
	return nil
}
