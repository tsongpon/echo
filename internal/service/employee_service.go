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
	ListByOrganization(ctx context.Context, organizationName string, limit int, cursorID string) ([]*model.Employee, string, error)
}

// EmployeeService is the application layer that orchestrates employee
// operations against an EmployeeRepository.
type EmployeeService struct {
	repo             EmployeeRepository
	mailer           Mailer
	verifySigner     *auth.EmailVerificationTokenSigner
	invitationSigner *auth.InvitationTokenSigner
	logger           *slog.Logger
}

// NewEmployeeService creates an EmployeeService backed by the given
// repository, mailer, email-verification token signer, and invitation token
// signer. If logger is nil, slog.Default() is used.
func NewEmployeeService(repo EmployeeRepository, mailer Mailer, verifySigner *auth.EmailVerificationTokenSigner, invitationSigner *auth.InvitationTokenSigner, logger *slog.Logger) *EmployeeService {
	if logger == nil {
		logger = slog.Default()
	}
	return &EmployeeService{repo: repo, mailer: mailer, verifySigner: verifySigner, invitationSigner: invitationSigner, logger: logger}
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
func (s *EmployeeService) Register(ctx context.Context, inviteToken string, employee *model.Employee) (*model.Employee, error) {
	if employee == nil {
		return nil, apperror.ErrInvalidEmployee("employee must not be nil")
	}
	if strings.TrimSpace(employee.Name) == "" {
		return nil, apperror.ErrInvalidEmployee("name is required")
	}
	if strings.TrimSpace(employee.Email) == "" {
		return nil, apperror.ErrInvalidEmployee("email is required")
	}
	if employee.Password == "" {
		return nil, apperror.ErrInvalidEmployee("password is required")
	}
	if len(employee.Password) > maxPasswordLen {
		return nil, apperror.ErrInvalidEmployee("password must be at most 64 characters")
	}

	// Authorization by invitation token. Two paths:
	//
	//   - With a token: the caller has been invited. The token is verified and,
	//     on success, its organization name overrides whatever the client
	//     supplied so a user can't self-join an arbitrary org, and the role is
	//     "user". An invalid/expired/wrong-purpose token is rejected as
	//     ErrInvalidInvitationToken so the handler can map it to a 400.
	//
	//   - Without a token: the caller is bootstrapping a new organization, so
	//     the client-supplied organization_name is required and honored, and
	//     the role is "org_admin" (the first admin).
	if strings.TrimSpace(inviteToken) != "" {
		claims, err := s.invitationSigner.Verify(inviteToken)
		if err != nil {
			return nil, apperror.ErrInvalidInvitationToken
		}
		employee.OrganizationName = claims.OrganizationName
		employee.Role = model.RoleUser
	} else {
		if strings.TrimSpace(employee.OrganizationName) == "" {
			return nil, apperror.ErrInvalidEmployee("organization_name is required")
		}
		employee.Role = model.RoleOrgAdmin
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

// DefaultEmployeeListLimit is the page size used when the caller does not
// specify a limit. It is sized for a reviewee picker, which typically renders
// one screen of options before the user scrolls or searches.
const DefaultEmployeeListLimit = 20

// MaxEmployeeListLimit caps the page size a caller can request. It protects
// the database from a single request pulling the entire organization into
// memory; a client that needs more pages can follow next_cursor.
const MaxEmployeeListLimit = 100

// ListByOrganization returns one page of employees in the named organization,
// ordered by name ascending, plus the ID of the last employee on the page for
// use as the next page's cursor. The organization name is taken from the
// authenticated caller's JWT by the handler, so an employee can only see their
// own organization's members.
//
// limit is the page size; if <= 0 DefaultEmployeeListLimit is used, and it is
// capped at MaxEmployeeListLimit. cursorID is the ID of the last employee from
// the previous page (the next_cursor value the client received); an empty
// cursorID starts a new listing from the beginning. The returned nextCursorID
// is empty when there are no more pages.
func (s *EmployeeService) ListByOrganization(ctx context.Context, organizationName string, limit int, cursorID string) ([]*model.Employee, string, error) {
	if strings.TrimSpace(organizationName) == "" {
		s.logger.Warn("employee list rejected: missing organization_name")
		return nil, "", apperror.ErrInvalidEmployee("organization_name is required")
	}
	if limit <= 0 {
		limit = DefaultEmployeeListLimit
	}
	if limit > MaxEmployeeListLimit {
		limit = MaxEmployeeListLimit
	}

	employees, nextCursorID, err := s.repo.ListByOrganization(ctx, organizationName, limit, cursorID)
	if err != nil {
		s.logger.Error("employee list failed", "error", err, "organization_name", organizationName, "limit", limit, "cursor_id", cursorID)
		return nil, "", err
	}
	return employees, nextCursorID, nil
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
