package service

import (
	"context"
	"errors"
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
	ListByManager(ctx context.Context, managerID string, limit int, cursorID string) ([]*model.Employee, string, error)
	HasOrganization(ctx context.Context, organizationName string) (bool, error)
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
//
// Organization membership is authorization-gated (see the invitation-token
// branch below): without a token the caller must be creating a brand-new
// organization, and registering into an existing one returns
// apperror.ErrOrganizationTaken.
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
	//     the role is "org_admin" (the first admin). Bootstrapping is only
	//     allowed for an organization that does not exist yet: registering
	//     without a token into an organization that already has members
	//     returns ErrOrganizationTaken, so a stranger cannot grant themselves
	//     org_admin in someone else's organization. Joining an existing
	//     organization requires an invitation token issued by one of its
	//     admins. The check is a pre-create existence query, not a uniqueness
	//     ledger, so two concurrent bootstraps of the same brand-new
	//     organization could in principle both succeed; the window is
	//     milliseconds and each still needs a globally unique email.
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
		exists, err := s.repo.HasOrganization(ctx, employee.OrganizationName)
		if err != nil {
			s.logger.Error("register aborted: organization existence check failed",
				"error", err, "organization_name", employee.OrganizationName)
			return nil, err
		}
		if exists {
			s.logger.Warn("register rejected: organization already exists",
				"email", employee.Email, "organization_name", employee.OrganizationName)
			return nil, apperror.ErrOrganizationTaken
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

// maxManagerChainHops caps the manager-chain walk performed by AssignManager
// to detect (or defend against) management cycles. Each hop is one document
// read, so the cap bounds the worst-case cost of an assignment on an org
// chart that is already corrupt (e.g. a pre-existing cycle written before
// this check existed). Any realistic org chart is far shallower than this.
const maxManagerChainHops = 50

// AssignManager sets (or, when managerID is nil, clears) the manager of the
// target employee. Only org admins may assign, and the target and the new
// manager must belong to the caller's organization.
//
// Authorization is a fresh-load check, not a JWT-claims check: the caller is
// loaded by ID here so a stale token (issued before a role or organization
// change) cannot grant privileges the caller no longer has.
//
// Validation and error mapping:
//   - caller must exist (ErrEmployeeNotFound, 404) and be an org admin
//     (ErrForbidden, 403).
//   - target must exist (ErrEmployeeNotFound, 404) and belong to the caller's
//     organization (ErrForbidden, 403).
//   - when assigning (managerID non-nil), the new manager must exist
//     (ErrInvalidEmployee, 400), belong to the same organization
//     (ErrInvalidEmployee, 400), and not be the target itself (ErrInvalidEmployee,
//     400 — a self-assignment is a one-node cycle).
//   - the assignment must not create a management cycle: the manager chain
//     starting at the new manager is walked (one read per hop, capped at
//     maxManagerChainHops) and must not reach the target (ErrInvalidEmployee,
//     400). Exceeding the hop cap is reported the same way: the chain is
//     either cyclic already or implausibly deep, and either way the
//     assignment is refused rather than risk closing a loop.
//   - clearing the manager (managerID nil) skips manager validation and the
//     cycle walk: removing a parent pointer cannot create a cycle.
//
// The cycle walk runs before the write but outside the update transaction, so
// two concurrent assignments could in principle interleave and jointly create
// a cycle. The window is milliseconds, requires an admin racing itself, and
// the repository's optimistic-concurrency check narrows it further; the
// residual risk is accepted.
func (s *EmployeeService) AssignManager(ctx context.Context, callerID, targetID string, managerID *string) (*model.Employee, error) {
	if strings.TrimSpace(callerID) == "" {
		s.logger.Warn("manager assignment rejected: missing caller_id", "target_id", targetID)
		return nil, apperror.ErrInvalidEmployee("caller_id is required")
	}
	if strings.TrimSpace(targetID) == "" {
		s.logger.Warn("manager assignment rejected: missing target_id", "caller_id", callerID)
		return nil, apperror.ErrInvalidEmployee("target_id is required")
	}

	caller, err := s.repo.GetByID(ctx, callerID)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			s.logger.Warn("manager assignment rejected: caller not found", "caller_id", callerID, "target_id", targetID)
			return nil, apperror.ErrForbidden
		}
		s.logger.Error("manager assignment aborted: caller lookup failed", "error", err, "caller_id", callerID)
		return nil, err
	}
	if caller.Role != model.RoleOrgAdmin {
		s.logger.Warn("manager assignment rejected: caller is not an org admin",
			"caller_id", callerID, "target_id", targetID, "role", string(caller.Role))
		return nil, apperror.ErrForbidden
	}

	target, err := s.repo.GetByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			s.logger.Warn("manager assignment rejected: target not found", "caller_id", callerID, "target_id", targetID)
			return nil, err
		}
		s.logger.Error("manager assignment aborted: target lookup failed", "error", err, "target_id", targetID)
		return nil, err
	}
	if target.OrganizationName != caller.OrganizationName {
		s.logger.Warn("manager assignment rejected: target in another organization",
			"caller_id", callerID, "target_id", targetID,
			"caller_org", caller.OrganizationName, "target_org", target.OrganizationName)
		return nil, apperror.ErrForbidden
	}

	if managerID != nil {
		if strings.TrimSpace(*managerID) == "" {
			s.logger.Warn("manager assignment rejected: empty manager_id", "caller_id", callerID, "target_id", targetID)
			return nil, apperror.ErrInvalidEmployee("manager_id must not be empty")
		}
		if *managerID == targetID {
			s.logger.Warn("manager assignment rejected: self-assignment", "caller_id", callerID, "target_id", targetID)
			return nil, apperror.ErrInvalidEmployee("manager cannot be the employee themselves")
		}
		manager, err := s.repo.GetByID(ctx, *managerID)
		if err != nil {
			if errors.Is(err, apperror.ErrEmployeeNotFound) {
				s.logger.Warn("manager assignment rejected: manager not found", "caller_id", callerID, "target_id", targetID, "manager_id", *managerID)
				return nil, apperror.ErrInvalidEmployee("manager_id does not refer to an existing employee")
			}
			s.logger.Error("manager assignment aborted: manager lookup failed", "error", err, "manager_id", *managerID)
			return nil, err
		}
		if manager.OrganizationName != caller.OrganizationName {
			s.logger.Warn("manager assignment rejected: manager in another organization",
				"caller_id", callerID, "target_id", targetID, "manager_id", *managerID,
				"caller_org", caller.OrganizationName, "manager_org", manager.OrganizationName)
			return nil, apperror.ErrInvalidEmployee("manager_id must refer to an employee in the same organization")
		}
		if err := s.detectManagerCycle(ctx, targetID, manager); err != nil {
			return nil, err
		}
	}

	target.ManagerID = managerID
	updated, err := s.repo.Update(ctx, target)
	if err != nil {
		s.logger.Error("manager assignment aborted: repository update failed",
			"error", err, "caller_id", callerID, "target_id", targetID)
		return nil, err
	}
	return updated, nil
}

// detectManagerCycle walks the manager chain upward starting at manager and
// reports a validation error if assigning manager as the parent of targetID
// would create a cycle — i.e. if the chain reaches targetID before reaching
// the top of the org chart. The walk is capped at maxManagerChainHops to bound
// both its cost and its termination on chains that are already cyclic or
// implausibly deep.
func (s *EmployeeService) detectManagerCycle(ctx context.Context, targetID string, manager *model.Employee) error {
	current := manager
	for hops := 0; hops < maxManagerChainHops && current != nil; hops++ {
		if current.ID == targetID {
			s.logger.Warn("manager assignment rejected: would create a management cycle",
				"target_id", targetID, "manager_id", manager.ID, "hops", hops)
			return apperror.ErrInvalidEmployee("assignment would create a management cycle")
		}
		if current.ManagerID == nil {
			// Reached the top of the chain without seeing the target.
			return nil
		}
		next, err := s.repo.GetByID(ctx, *current.ManagerID)
		if err != nil {
			if errors.Is(err, apperror.ErrEmployeeNotFound) {
				// A dangling manager pointer. It cannot close a cycle back to
				// the target, so the assignment is allowed; the chain is
				// simply broken higher up.
				s.logger.Warn("manager chain walk hit a dangling manager_id",
					"target_id", targetID, "manager_id", manager.ID, "dangling_id", *current.ManagerID)
				return nil
			}
			s.logger.Error("manager chain walk aborted: lookup failed",
				"error", err, "manager_id", *current.ManagerID)
			return err
		}
		current = next
	}
	s.logger.Warn("manager assignment rejected: manager chain exceeded the hop cap",
		"target_id", targetID, "manager_id", manager.ID, "hop_cap", maxManagerChainHops)
	return apperror.ErrInvalidEmployee("manager chain is too deep or already cyclic")
}

// ListReportees returns one page of employees whose manager is the named
// employee, ordered by name ascending, plus the ID of the last employee on
// the page for use as the next page's cursor. The manager ID is taken from the
// authenticated caller's JWT by the handler, so an employee can only list
// their own reportees.
//
// limit is the page size; if <= 0 DefaultEmployeeListLimit is used, and it is
// capped at MaxEmployeeListLimit. cursorID is the ID of the last employee from
// the previous page; an empty cursorID starts a new listing from the
// beginning. The returned nextCursorID is empty when there are no more pages.
func (s *EmployeeService) ListReportees(ctx context.Context, managerID string, limit int, cursorID string) ([]*model.Employee, string, error) {
	if strings.TrimSpace(managerID) == "" {
		s.logger.Warn("reportee list rejected: missing manager_id")
		return nil, "", apperror.ErrInvalidEmployee("manager_id is required")
	}
	if limit <= 0 {
		limit = DefaultEmployeeListLimit
	}
	if limit > MaxEmployeeListLimit {
		limit = MaxEmployeeListLimit
	}

	employees, nextCursorID, err := s.repo.ListByManager(ctx, managerID, limit, cursorID)
	if err != nil {
		if errors.Is(err, apperror.ErrEmployeeNotFound) {
			// An unknown cursor is a caller error, not a service failure.
			// Pass it through so the handler can map it to a 400.
			return nil, "", err
		}
		s.logger.Error("reportee list failed", "error", err, "manager_id", managerID, "limit", limit, "cursor_id", cursorID)
		return nil, "", err
	}
	return employees, nextCursorID, nil
}
