package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/auth"
	"github.com/tsongpon/echo/internal/model"
)

// fakeRepo is an in-test stand-in for service.EmployeeRepository that records
// the employee passed to Create and serves lookups from an in-memory map
// without going through the real repository package (which would create an
// import cycle).
type fakeRepo struct {
	created *model.Employee
	byEmail map[string]*model.Employee
	byID    map[string]*model.Employee
}

func (f *fakeRepo) Create(_ context.Context, employee *model.Employee) (*model.Employee, error) {
	if f.byEmail == nil {
		f.byEmail = make(map[string]*model.Employee)
	}
	if f.byID == nil {
		f.byID = make(map[string]*model.Employee)
	}
	if _, exists := f.byEmail[strings.ToLower(employee.Email)]; exists {
		return nil, apperror.ErrEmailTaken
	}
	f.created = employee
	employee.ID = "emp-1"
	f.byEmail[strings.ToLower(employee.Email)] = employee
	f.byID[employee.ID] = employee
	return employee, nil
}

func (f *fakeRepo) GetByEmail(_ context.Context, email string) (*model.Employee, error) {
	if f.byEmail == nil {
		return nil, apperror.ErrEmployeeNotFound
	}
	if e, ok := f.byEmail[strings.ToLower(email)]; ok {
		return e, nil
	}
	return nil, apperror.ErrEmployeeNotFound
}

func (f *fakeRepo) GetByID(_ context.Context, id string) (*model.Employee, error) {
	if f.byID == nil {
		return nil, apperror.ErrEmployeeNotFound
	}
	if e, ok := f.byID[id]; ok {
		return e, nil
	}
	return nil, apperror.ErrEmployeeNotFound
}

func (f *fakeRepo) Update(_ context.Context, employee *model.Employee) (*model.Employee, error) {
	if f.byID == nil {
		f.byID = make(map[string]*model.Employee)
	}
	f.byID[employee.ID] = employee
	return employee, nil
}

func (f *fakeRepo) ListByOrganization(_ context.Context, organizationName string, limit int, cursorID string) ([]*model.Employee, string, error) {
	if f.byID == nil {
		return []*model.Employee{}, "", nil
	}
	// Collect and sort by name ascending, then by ID for stable tie-breaking,
	// mirroring Firestore's ordering on (name, document ID).
	var all []*model.Employee
	for _, e := range f.byID {
		if e.OrganizationName == organizationName {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})
	// Find the cursor position; an unknown cursor mirrors the repo's
	// apperror.ErrEmployeeNotFound so the service test can exercise that path.
	start := 0
	if strings.TrimSpace(cursorID) != "" {
		found := -1
		for i, e := range all {
			if e.ID == cursorID {
				found = i
				break
			}
		}
		if found == -1 {
			return nil, "", apperror.ErrEmployeeNotFound
		}
		start = found + 1
	}
	if limit <= 0 {
		limit = 20
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	if page == nil {
		page = []*model.Employee{}
	}
	nextCursor := ""
	if end < len(all) {
		nextCursor = page[len(page)-1].ID
	}
	return page, nextCursor, nil
}

// ListByManager mirrors service.EmployeeRepository.ListByManager. It serves
// from the in-memory byID map, filtering by manager_id and sorting by name
// ascending then by ID, mirroring Firestore's ordering on (name, document
// ID). An unknown cursor returns apperror.ErrEmployeeNotFound, matching the
// real Firestore repository.
func (f *fakeRepo) ListByManager(_ context.Context, managerID string, limit int, cursorID string) ([]*model.Employee, string, error) {
	var all []*model.Employee
	for _, e := range f.byID {
		if e.ManagerID != nil && *e.ManagerID == managerID {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})
	start := 0
	if strings.TrimSpace(cursorID) != "" {
		found := -1
		for i, e := range all {
			if e.ID == cursorID {
				found = i
				break
			}
		}
		if found == -1 {
			return nil, "", apperror.ErrEmployeeNotFound
		}
		start = found + 1
	}
	if limit <= 0 {
		limit = 20
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	if page == nil {
		page = []*model.Employee{}
	}
	nextCursor := ""
	if end < len(all) {
		nextCursor = page[len(page)-1].ID
	}
	return page, nextCursor, nil
}

// HasOrganization mirrors service.EmployeeRepository.HasOrganization: it
// reports whether any stored employee belongs to the named organization.
func (f *fakeRepo) HasOrganization(_ context.Context, organizationName string) (bool, error) {
	for _, e := range f.byID {
		if e.OrganizationName == organizationName {
			return true, nil
		}
	}
	return false, nil
}

// noopMailer is a service.Mailer stand-in that records the last token it was
// asked to "send" without doing any real delivery.
type noopMailer struct {
	lastTo    string
	lastToken string
}

func (m *noopMailer) SendVerificationEmail(_ context.Context, to, token string) error {
	m.lastTo = to
	m.lastToken = token
	return nil
}

func newTestService() (*EmployeeService, *noopMailer) {
	signer, err := auth.NewEmailVerificationTokenSigner("test-secret", 0)
	if err != nil {
		panic(err)
	}
	invitationSigner, err := auth.NewInvitationTokenSigner("test-secret", 0)
	if err != nil {
		panic(err)
	}
	m := &noopMailer{}
	// Discard log output so service tests stay quiet.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewEmployeeService(&fakeRepo{}, m, signer, invitationSigner, logger), m
}

// newTestServiceWithRepo builds an EmployeeService over a caller-supplied
// repository, for tests that need a fake with special behavior.
func newTestServiceWithRepo(repo EmployeeRepository) *EmployeeService {
	signer, err := auth.NewEmailVerificationTokenSigner("test-secret", 0)
	if err != nil {
		panic(err)
	}
	invitationSigner, err := auth.NewInvitationTokenSigner("test-secret", 0)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewEmployeeService(repo, &noopMailer{}, signer, invitationSigner, logger)
}

// hasOrgErrRepo wraps fakeRepo and fails HasOrganization with a persistent
// error, to assert that an existence-check failure aborts registration rather
// than being treated as "organization does not exist".
type hasOrgErrRepo struct {
	fakeRepo
}

func (r *hasOrgErrRepo) HasOrganization(_ context.Context, _ string) (bool, error) {
	return false, errors.New("existence check failed")
}

func TestRegister_HashesPassword(t *testing.T) {
	svc, _ := newTestService()

	plaintext := "supersecret"
	emp := &model.Employee{
		Name:           "Alice",
		OrganizationName: "org-1",
		Email:          "alice@example.com",
		Password:       plaintext,
	}

	created, err := svc.Register(context.Background(), "", emp)
	if err != nil {
		t.Fatalf("Register returned unexpected error: %v", err)
	}

	// The stored password must not be the plaintext.
	if created.Password == plaintext {
		t.Fatalf("stored password equals the plaintext; it must be hashed")
	}

	// The stored password must be a bcrypt hash that verifies against the
	// original plaintext.
	if err := bcrypt.CompareHashAndPassword([]byte(created.Password), []byte(plaintext)); err != nil {
		t.Fatalf("stored password is not a valid bcrypt hash of the plaintext: %v", err)
	}

	// The stored password must NOT verify against a wrong plaintext.
	if bcrypt.CompareHashAndPassword([]byte(created.Password), []byte("wrong")) == nil {
		t.Fatalf("stored password verified against the wrong plaintext")
	}

	// The original input struct's password should also be replaced, so
	// callers can't accidentally keep the plaintext around via the passed-in
	// pointer.
	if emp.Password == plaintext {
		t.Fatalf("input struct still holds the plaintext password")
	}
}

func TestRegister_SendsVerification(t *testing.T) {
	svc, mailer := newTestService()

	created, err := svc.Register(context.Background(), "", &model.Employee{
		Name:           "Alice",
		OrganizationName: "org-1",
		Email:          "alice@example.com",
		Password:       "supersecret",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Register must hand a verification token to the mailer bound to the
	// created employee's email.
	if mailer.lastTo != "alice@example.com" {
		t.Fatalf("got mailer.to %q, want alice@example.com", mailer.lastTo)
	}
	if mailer.lastToken == "" {
		t.Fatal("expected a non-empty verification token to be handed to the mailer")
	}
	if created.IsMailVerified {
		t.Fatal("newly registered employee should still be unverified; IsMailVerified must be false")
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	svc, _ := newTestService()

	first := &model.Employee{
		Name:           "Alice",
		OrganizationName: "org-1",
		Email:          "alice@example.com",
		Password:       "supersecret",
	}
	if _, err := svc.Register(context.Background(), "", first); err != nil {
		t.Fatalf("first Register: unexpected error: %v", err)
	}

	// A second registration with the same email must be rejected. Email
	// uniqueness is global, so an different organization does not help.
	second := &model.Employee{
		Name:           "Alicia",
		OrganizationName: "org-2",
		Email:          "alice@example.com",
		Password:       "anothersecret",
	}
	_, err := svc.Register(context.Background(), "", second)
	if !errors.Is(err, apperror.ErrEmailTaken) {
		t.Fatalf("expected apperror.ErrEmailTaken, got %v", err)
	}

	// The duplicate must not have overwritten or mutated the first employee.
	got, err := svc.GetByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("GetByID after duplicate: %v", err)
	}
	if got.Name != "Alice" {
		t.Fatalf("first employee name changed to %q", got.Name)
	}

	// Case-insensitive: an uppercased variant of the same email is still a
	// duplicate.
	third := &model.Employee{
		Name:           "Aly",
		OrganizationName: "org-3",
		Email:          "ALICE@example.com",
		Password:       "yetanother",
	}
	if _, err := svc.Register(context.Background(), "", third); !errors.Is(err, apperror.ErrEmailTaken) {
		t.Fatalf("expected apperror.ErrEmailTaken for case variant, got %v", err)
	}

	// A genuinely new email still succeeds. The organization must also be
	// new: bootstrapping without a token into an existing organization is
	// rejected by the C3 fix (see TestRegister_ExistingOrganization), and
	// this test is about email uniqueness, not org membership.
	fresh := &model.Employee{
		Name:           "Bob",
		OrganizationName: "org-fresh",
		Email:          "bob@example.com",
		Password:       "supersecret",
	}
	if _, err := svc.Register(context.Background(), "", fresh); err != nil {
		t.Fatalf("fresh email Register: unexpected error: %v", err)
	}
}

func TestRegister_ExistingOrganization(t *testing.T) {
	// Bootstrap the first admin of "PentestOrg", as the C3 PoC did before
	// the fix.
	bootstrap := func(t *testing.T, svc *EmployeeService, name, email, org string) error {
		t.Helper()
		_, err := svc.Register(context.Background(), "", &model.Employee{
			Name:             name,
			OrganizationName: org,
			Email:            email,
			Password:         "supersecret",
		})
		return err
	}

	t.Run("bootstrap into existing organization is rejected", func(t *testing.T) {
		svc, mailer := newTestService()
		if err := bootstrap(t, svc, "Admin", "admin@pentest.example.com", "PentestOrg"); err != nil {
			t.Fatalf("first bootstrap: unexpected error: %v", err)
		}

		// The C3 PoC: register a second user with a fresh email into the
		// existing organization, without an invitation token. This used to
		// grant role org_admin immediately; it must now be rejected with
		// ErrOrganizationTaken.
		err := bootstrap(t, svc, "Attacker", "pentest3@example.com", "PentestOrg")
		if !errors.Is(err, apperror.ErrOrganizationTaken) {
			t.Fatalf("expected ErrOrganizationTaken, got %v", err)
		}

		// No new member may have been created for the attacker email. The
		// mailer's last delivery must still be the first admin's, i.e. the
		// attacker's registration never reached Create.
		if mailer.lastTo != "admin@pentest.example.com" {
			t.Fatalf("attacker registration reached employee creation; last mail to %q", mailer.lastTo)
		}
	})

	t.Run("bootstrap into new organization succeeds", func(t *testing.T) {
		svc, _ := newTestService()
		created, err := svc.Register(context.Background(), "", &model.Employee{
			Name:             "Alice",
			OrganizationName: "FreshOrg",
			Email:            "alice@fresh.example.com",
			Password:         "supersecret",
		})
		if err != nil {
			t.Fatalf("Register: unexpected error: %v", err)
		}
		if created.Role != model.RoleOrgAdmin {
			t.Fatalf("got role %q, want org_admin for the first member", created.Role)
		}
	})

	t.Run("existence check failure aborts registration", func(t *testing.T) {
		// A repository failure must surface as an error, not as "org does
		// not exist" (a false negative would reopen the vulnerability).
		repo := &hasOrgErrRepo{}
		svc := newTestServiceWithRepo(repo)
		_, err := svc.Register(context.Background(), "", &model.Employee{
			Name:             "Alice",
			OrganizationName: "org-1",
			Email:            "alice@example.com",
			Password:         "supersecret",
		})
		if err == nil || !strings.Contains(err.Error(), "existence check failed") {
			t.Fatalf("expected repository error to surface on first register, got %v", err)
		}
	})
}

func TestRegister_WithInvitationToken(t *testing.T) {
	// Issue a real invitation token for "Acme" via the service's signer.
	invitationSvc := NewInvitationService(testInvitationSigner(t), nil)
	token, err := invitationSvc.CreateInvitationToken("inviter-1", "Acme", nil)
	if err != nil {
		t.Fatalf("CreateInvitationToken: %v", err)
	}

	t.Run("token overrides organization and sets role user", func(t *testing.T) {
		svc, _ := newTestService()
		// The client supplies a *different* org name; the token must win.
		emp := &model.Employee{
			Name:             "Bob",
			OrganizationName: "should-be-ignored",
			Email:            "bob@example.com",
			Password:         "supersecret",
		}
		created, err := svc.Register(context.Background(), token, emp)
		if err != nil {
			t.Fatalf("Register: unexpected error: %v", err)
		}
		if created.OrganizationName != "Acme" {
			t.Fatalf("got organization_name %q, want Acme (from token)", created.OrganizationName)
		}
		if created.Role != model.RoleUser {
			t.Fatalf("got role %q, want user", created.Role)
		}
	})

	t.Run("no token sets role org_admin and honors client org", func(t *testing.T) {
		svc, _ := newTestService()
		emp := &model.Employee{
			Name:             "Carol",
			OrganizationName: "CarolCo",
			Email:            "carol@example.com",
			Password:         "supersecret",
		}
		created, err := svc.Register(context.Background(), "", emp)
		if err != nil {
			t.Fatalf("Register: unexpected error: %v", err)
		}
		if created.OrganizationName != "CarolCo" {
			t.Fatalf("got organization_name %q, want CarolCo", created.OrganizationName)
		}
		if created.Role != model.RoleOrgAdmin {
			t.Fatalf("got role %q, want org_admin", created.Role)
		}
	})

	t.Run("invalid token is rejected", func(t *testing.T) {
		svc, _ := newTestService()
		emp := &model.Employee{
			Name:             "Dan",
			OrganizationName: "DanCo",
			Email:            "dan@example.com",
			Password:         "supersecret",
		}
		_, err := svc.Register(context.Background(), "not-a-jwt", emp)
		if !errors.Is(err, apperror.ErrInvalidInvitationToken) {
			t.Fatalf("expected ErrInvalidInvitationToken, got %v", err)
		}
	})

	t.Run("missing organization_name without token is required", func(t *testing.T) {
		svc, _ := newTestService()
		emp := &model.Employee{
			Name:     "Eve",
			Email:    "eve@example.com",
			Password: "supersecret",
		}
		_, err := svc.Register(context.Background(), "", emp)
		if err == nil || err.Error() != "organization_name is required" {
			t.Fatalf("expected organization_name is required, got %v", err)
		}
	})

	t.Run("with token, client organization_name is not required", func(t *testing.T) {
		svc, _ := newTestService()
		emp := &model.Employee{
			Name:     "Frank",
			Email:    "frank@example.com",
			Password: "supersecret",
		}
		created, err := svc.Register(context.Background(), token, emp)
		if err != nil {
			t.Fatalf("Register: unexpected error: %v", err)
		}
		if created.OrganizationName != "Acme" {
			t.Fatalf("got organization_name %q, want Acme", created.OrganizationName)
		}
	})
}

// testInvitationSigner builds an InvitationTokenSigner for service tests.
func testInvitationSigner(t *testing.T) *auth.InvitationTokenSigner {
	t.Helper()
	s, err := auth.NewInvitationTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatalf("NewInvitationTokenSigner: %v", err)
	}
	return s
}

func TestLogin(t *testing.T) {
	svc, _ := newTestService()

	const plaintext = "supersecret"
	created, err := svc.Register(context.Background(), "", &model.Employee{
		Name:           "Alice",
		OrganizationName: "org-1",
		Email:          "alice@example.com",
		Password:       plaintext,
	})
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
	// Verify the email so the success-path subtests can log in.
	token, err := testSigner(t, 0).Sign(created)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := svc.VerifyEmail(context.Background(), token); err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}

	t.Run("valid credentials", func(t *testing.T) {
		e, err := svc.Login(context.Background(), "alice@example.com", plaintext)
		if err != nil {
			t.Fatalf("Login: unexpected error: %v", err)
		}
		if e.Email != "alice@example.com" {
			t.Fatalf("got email %q, want alice@example.com", e.Email)
		}
	})

	t.Run("case-insensitive email", func(t *testing.T) {
		if _, err := svc.Login(context.Background(), "ALICE@example.com", plaintext); err != nil {
			t.Fatalf("Login with uppercased email: unexpected error: %v", err)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		_, err := svc.Login(context.Background(), "alice@example.com", "wrong")
		if !errors.Is(err, apperror.ErrInvalidCredentials) {
			t.Fatalf("expected apperror.ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("unknown email", func(t *testing.T) {
		_, err := svc.Login(context.Background(), "nobody@example.com", plaintext)
		if !errors.Is(err, apperror.ErrInvalidCredentials) {
			t.Fatalf("expected apperror.ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		if _, err := svc.Login(context.Background(), "", "x"); err == nil {
			t.Fatalf("expected error for empty email")
		}
		if _, err := svc.Login(context.Background(), "a@example.com", ""); err == nil {
			t.Fatalf("expected error for empty password")
		}
	})

	t.Run("unverified email is rejected", func(t *testing.T) {
		svc, _ := newTestService()
		const pw = "supersecret"
		if _, err := svc.Register(context.Background(), "", &model.Employee{
			Name:           "Bob",
			OrganizationName: "org-1",
			Email:          "bob@example.com",
			Password:       pw,
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		_, err := svc.Login(context.Background(), "bob@example.com", pw)
		if !errors.Is(err, apperror.ErrEmailNotVerified) {
			t.Fatalf("expected apperror.ErrEmailNotVerified, got %v", err)
		}
	})
}

func TestGetByID(t *testing.T) {
	svc, _ := newTestService()

	created, err := svc.Register(context.Background(), "", &model.Employee{
		Name:           "Alice",
		OrganizationName: "org-1",
		Email:          "alice@example.com",
		Password:       "supersecret",
	})
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}

	t.Run("existing id", func(t *testing.T) {
		got, err := svc.GetByID(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("GetByID: unexpected error: %v", err)
		}
		if got.ID != created.ID {
			t.Fatalf("got id %q, want %q", got.ID, created.ID)
		}
		if got.Email != "alice@example.com" {
			t.Fatalf("got email %q", got.Email)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		_, err := svc.GetByID(context.Background(), "does-not-exist")
		if !errors.Is(err, apperror.ErrEmployeeNotFound) {
			t.Fatalf("expected apperror.ErrEmployeeNotFound, got %v", err)
		}
	})

	t.Run("empty id", func(t *testing.T) {
		_, err := svc.GetByID(context.Background(), "")
		if !errors.Is(err, apperror.ErrEmployeeNotFound) {
			t.Fatalf("expected apperror.ErrEmployeeNotFound, got %v", err)
		}
	})
}

func TestRegister_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		emp     *model.Employee
		wantMsg string
	}{
		{
			name:    "nil employee",
			emp:     nil,
			wantMsg: "employee must not be nil",
		},
		{
			name: "missing name",
			emp: &model.Employee{
				OrganizationName: "org-1",
				Email:          "a@example.com",
				Password:       "pw",
			},
			wantMsg: "name is required",
		},
		{
			name: "missing email",
			emp: &model.Employee{
				Name:           "Bob",
				OrganizationName: "org-1",
				Password:       "pw",
			},
			wantMsg: "email is required",
		},
		{
			name: "missing organization_name",
			emp: &model.Employee{
				Name:     "Bob",
				Email:    "a@example.com",
				Password: "pw",
			},
			wantMsg: "organization_name is required",
		},
		{
			name: "missing password",
			emp: &model.Employee{
				Name:           "Bob",
				OrganizationName: "org-1",
				Email:          "a@example.com",
			},
			wantMsg: "password is required",
		},
		{
			name: "password too long",
			emp: &model.Employee{
				Name:           "Bob",
				OrganizationName: "org-1",
				Email:          "a@example.com",
				Password:       strings.Repeat("x", maxPasswordLen+1),
			},
			wantMsg: "password must be at most 64 characters",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService()
			_, err := svc.Register(context.Background(), "", tc.emp)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !apperror.IsInvalidEmployee(err) {
				t.Fatalf("expected ErrInvalidEmployee, got %T: %v", err, err)
			}
			if err.Error() != tc.wantMsg {
				t.Fatalf("expected message %q, got %q", tc.wantMsg, err.Error())
			}
		})
	}
}

func TestEmployeeService_ListByOrganization(t *testing.T) {
	// Seed 5 employees for "Acme" (Alice..Eve) and one for "Other", used
	// across the pagination subtests. Names are in ascending order so the
	// expected page slices are easy to reason about.
	seed := func() *fakeRepo {
		return &fakeRepo{byID: map[string]*model.Employee{
			"e-1": {ID: "e-1", Name: "Alice", OrganizationName: "Acme", Email: "a@acme.com"},
			"e-2": {ID: "e-2", Name: "Bob", OrganizationName: "Acme", Email: "b@acme.com"},
			"e-3": {ID: "e-3", Name: "Carol", OrganizationName: "Acme", Email: "c@acme.com"},
			"e-4": {ID: "e-4", Name: "Dave", OrganizationName: "Acme", Email: "d@acme.com"},
			"e-5": {ID: "e-5", Name: "Eve", OrganizationName: "Acme", Email: "e@acme.com"},
			"e-6": {ID: "e-6", Name: "Zoe", OrganizationName: "Other", Email: "z@other.com"},
		}}
	}
	newSvc := func(repo *fakeRepo) *EmployeeService {
		return NewEmployeeService(repo, &noopMailer{}, testSigner(t, 0), testInvitationSigner(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	t.Run("returns employees for the named organization", func(t *testing.T) {
		svc := newSvc(seed())
		got, next, err := svc.ListByOrganization(context.Background(), "Acme", 0, "")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if len(got) != 5 {
			t.Fatalf("got %d employees, want 5", len(got))
		}
		for _, e := range got {
			if e.OrganizationName != "Acme" {
				t.Fatalf("got employee with org %q, want Acme", e.OrganizationName)
			}
		}
		// With the default limit (20) >= total, there is no next page.
		if next != "" {
			t.Fatalf("expected empty next cursor, got %q", next)
		}
	})

	t.Run("empty organization returns an empty slice", func(t *testing.T) {
		svc := newSvc(seed())
		got, _, err := svc.ListByOrganization(context.Background(), "NoSuchOrg", 0, "")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil slice, got nil")
		}
		if len(got) != 0 {
			t.Fatalf("got %d employees, want 0", len(got))
		}
	})

	t.Run("missing organization_name is rejected", func(t *testing.T) {
		svc, _ := newTestService()
		_, _, err := svc.ListByOrganization(context.Background(), "", 0, "")
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee, got %T: %v", err, err)
		}
		if err.Error() != "organization_name is required" {
			t.Fatalf("expected organization_name is required, got %q", err.Error())
		}
	})

	t.Run("repository error propagates", func(t *testing.T) {
		repoErr := errors.New("firestore unavailable")
		svc := NewEmployeeService(&erroringRepo{err: repoErr}, &noopMailer{}, testSigner(t, 0), testInvitationSigner(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, _, err := svc.ListByOrganization(context.Background(), "Acme", 0, "")
		if err == nil {
			t.Fatal("expected repository error to propagate, got nil")
		}
		if !errors.Is(err, repoErr) {
			t.Fatalf("expected the repo error to propagate, got %v", err)
		}
	})

	t.Run("first page returns limit items and a next cursor", func(t *testing.T) {
		svc := newSvc(seed())
		got, next, err := svc.ListByOrganization(context.Background(), "Acme", 2, "")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d employees, want 2", len(got))
		}
		if got[0].Name != "Alice" || got[1].Name != "Bob" {
			t.Fatalf("expected Alice,Bob, got %s,%s", got[0].Name, got[1].Name)
		}
		if next != "e-2" {
			t.Fatalf("expected next cursor e-2, got %q", next)
		}
	})

	t.Run("second page starts after the cursor", func(t *testing.T) {
		svc := newSvc(seed())
		got, next, err := svc.ListByOrganization(context.Background(), "Acme", 2, "e-2")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d employees, want 2", len(got))
		}
		if got[0].Name != "Carol" || got[1].Name != "Dave" {
			t.Fatalf("expected Carol,Dave, got %s,%s", got[0].Name, got[1].Name)
		}
		if next != "e-4" {
			t.Fatalf("expected next cursor e-4, got %q", next)
		}
	})

	t.Run("last page has no next cursor", func(t *testing.T) {
		svc := newSvc(seed())
		got, next, err := svc.ListByOrganization(context.Background(), "Acme", 2, "e-4")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d employees, want 1", len(got))
		}
		if got[0].Name != "Eve" {
			t.Fatalf("expected Eve, got %s", got[0].Name)
		}
		if next != "" {
			t.Fatalf("expected empty next cursor on last page, got %q", next)
		}
	})

	t.Run("unknown cursor is rejected", func(t *testing.T) {
		svc := newSvc(seed())
		_, _, err := svc.ListByOrganization(context.Background(), "Acme", 2, "does-not-exist")
		if err == nil {
			t.Fatal("expected error for unknown cursor, got nil")
		}
		if !errors.Is(err, apperror.ErrEmployeeNotFound) {
			t.Fatalf("expected ErrEmployeeNotFound, got %v", err)
		}
	})

	t.Run("limit is capped at MaxEmployeeListLimit", func(t *testing.T) {
		svc := newSvc(seed())
		// Request an absurd limit; the service must cap it rather than pass it
		// through. The fake repo honors any limit, so assert via the result:
		// with 5 seeded and limit capped at 100, all 5 return in one page.
		got, next, err := svc.ListByOrganization(context.Background(), "Acme", 99999, "")
		if err != nil {
			t.Fatalf("ListByOrganization: unexpected error: %v", err)
		}
		if len(got) != 5 {
			t.Fatalf("got %d employees, want 5 (limit capped, no paging)", len(got))
		}
		if next != "" {
			t.Fatalf("expected empty next cursor, got %q", next)
		}
	})
}

// erroringRepo is a fakeRepo that always fails ListByOrganization with a
// canned error, used to assert the service propagates repo errors.
type erroringRepo struct{ err error }

func (e *erroringRepo) Create(_ context.Context, emp *model.Employee) (*model.Employee, error) {
	return emp, nil
}
func (e *erroringRepo) GetByEmail(_ context.Context, _ string) (*model.Employee, error) {
	return nil, apperror.ErrEmployeeNotFound
}
func (e *erroringRepo) GetByID(_ context.Context, _ string) (*model.Employee, error) {
	return nil, apperror.ErrEmployeeNotFound
}
func (e *erroringRepo) Update(_ context.Context, emp *model.Employee) (*model.Employee, error) {
	return emp, nil
}
func (e *erroringRepo) ListByOrganization(_ context.Context, _ string, _ int, _ string) ([]*model.Employee, string, error) {
	return nil, "", e.err
}
func (e *erroringRepo) ListByManager(_ context.Context, _ string, _ int, _ string) ([]*model.Employee, string, error) {
	return nil, "", e.err
}
func (e *erroringRepo) HasOrganization(_ context.Context, _ string) (bool, error) {
	return false, e.err
}

// testSigner builds an EmailVerificationTokenSigner for service tests.
func testSigner(t *testing.T, ttl time.Duration) *auth.EmailVerificationTokenSigner {
	t.Helper()
	s, err := auth.NewEmailVerificationTokenSigner("test-secret", ttl)
	if err != nil {
		t.Fatalf("NewEmailVerificationTokenSigner: %v", err)
	}
	return s
}

func TestSendVerification(t *testing.T) {
	svc, mailer := newTestService()

	created, err := svc.Register(context.Background(), "", &model.Employee{
		Name:           "Alice",
		OrganizationName: "org-1",
		Email:          "alice@example.com",
		Password:       "supersecret",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := svc.SendVerification(context.Background(), created); err != nil {
		t.Fatalf("SendVerification: %v", err)
	}
	if mailer.lastTo != "alice@example.com" {
		t.Fatalf("got to %q, want alice@example.com", mailer.lastTo)
	}
	if mailer.lastToken == "" {
		t.Fatalf("expected a non-empty token to be handed to the mailer")
	}
}

func TestVerifyEmail(t *testing.T) {
	// Register once and reuse the created employee + its signer-issued token.
	setup := func(t *testing.T) (*EmployeeService, *model.Employee, string) {
		svc, _ := newTestService()
		created, err := svc.Register(context.Background(), "", &model.Employee{
			Name:           "Alice",
			OrganizationName: "org-1",
			Email:          "alice@example.com",
			Password:       "supersecret",
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		token, err := testSigner(t, 0).Sign(created)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return svc, created, token
	}

	t.Run("valid token", func(t *testing.T) {
		svc, created, token := setup(t)

		if err := svc.VerifyEmail(context.Background(), token); err != nil {
			t.Fatalf("VerifyEmail: %v", err)
		}

		got, err := svc.GetByID(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if !got.IsMailVerified {
			t.Fatalf("expected IsMailVerified=true, got false")
		}
	})

	t.Run("already verified is idempotent", func(t *testing.T) {
		svc, _, token := setup(t)

		if err := svc.VerifyEmail(context.Background(), token); err != nil {
			t.Fatalf("first VerifyEmail: %v", err)
		}
		if err := svc.VerifyEmail(context.Background(), token); err != nil {
			t.Fatalf("second VerifyEmail: %v", err)
		}
	})

	t.Run("empty token", func(t *testing.T) {
		svc, _, _ := setup(t)
		if err := svc.VerifyEmail(context.Background(), ""); !errors.Is(err, apperror.ErrInvalidVerificationToken) {
			t.Fatalf("expected apperror.ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("garbage token", func(t *testing.T) {
		svc, _, _ := setup(t)
		if err := svc.VerifyEmail(context.Background(), "not-a-jwt"); !errors.Is(err, apperror.ErrInvalidVerificationToken) {
			t.Fatalf("expected apperror.ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("access token is not a verification token", func(t *testing.T) {
		svc, created, _ := setup(t)

		accessSigner, err := auth.NewTokenSigner("test-secret", 0)
		if err != nil {
			t.Fatalf("NewTokenSigner: %v", err)
		}
		accessToken, err := accessSigner.Sign(created)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if err := svc.VerifyEmail(context.Background(), accessToken); !errors.Is(err, apperror.ErrInvalidVerificationToken) {
			t.Fatalf("expected apperror.ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("token for unknown employee", func(t *testing.T) {
		svc, _, _ := setup(t)

		token, err := testSigner(t, 0).Sign(&model.Employee{ID: "ghost", Email: "ghost@example.com"})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if err := svc.VerifyEmail(context.Background(), token); !errors.Is(err, apperror.ErrInvalidVerificationToken) {
			t.Fatalf("expected apperror.ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		svc, created, _ := setup(t)

		// The signing key is derived only from the base secret, so a token
		// signed with a short-TTL signer is verifiable by the service's
		// default-TTL signer; only its embedded expiry differs.
		shortSigner := testSigner(t, time.Millisecond)
		token, err := shortSigner.Sign(created)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		time.Sleep(5 * time.Millisecond)

		if err := svc.VerifyEmail(context.Background(), token); !errors.Is(err, apperror.ErrInvalidVerificationToken) {
			t.Fatalf("expected apperror.ErrInvalidVerificationToken, got %v", err)
		}
	})
}

// newAssignTestService builds an EmployeeService over a fakeRepo seeded with
// the org chart the AssignManager tests need, and returns the service plus
// the seeded employees. The caller is always an org admin in "org-1".
func newAssignTestService(t *testing.T) (*EmployeeService, *fakeRepo, map[string]*model.Employee) {
	t.Helper()
	signer, err := auth.NewEmailVerificationTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	invSigner, err := auth.NewInvitationTokenSigner("test-secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	repo := &fakeRepo{
		byEmail: make(map[string]*model.Employee),
		byID:    make(map[string]*model.Employee),
	}
	people := map[string]*model.Employee{
		"admin":  {ID: "id-admin", Name: "Admin", OrganizationName: "org-1", Role: model.RoleOrgAdmin, Email: "admin@example.com"},
		"alice":  {ID: "id-alice", Name: "Alice", OrganizationName: "org-1", Role: model.RoleUser, Email: "alice@example.com"},
		"bob":    {ID: "id-bob", Name: "Bob", OrganizationName: "org-1", Role: model.RoleUser, Email: "bob@example.com"},
		"carol":  {ID: "id-carol", Name: "Carol", OrganizationName: "org-1", Role: model.RoleUser, Email: "carol@example.com"},
		"dave":   {ID: "id-dave", Name: "Dave", OrganizationName: "org-1", Role: model.RoleUser, Email: "dave@example.com"},
		"outsider": {ID: "id-outsider", Name: "Outsider", OrganizationName: "org-2", Role: model.RoleOrgAdmin, Email: "outsider@example.com"},
	}
	for _, e := range people {
		repo.byID[e.ID] = e
		repo.byEmail[e.Email] = e
	}

	svc := NewEmployeeService(repo, &noopMailer{}, signer, invSigner, logger)
	return svc, repo, people
}

func TestAssignManager(t *testing.T) {
	t.Run("happy path assigns manager", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)

		updated, err := svc.AssignManager(context.Background(), people["admin"].ID, people["carol"].ID, &people["bob"].ID)
		if err != nil {
			t.Fatalf("AssignManager: %v", err)
		}
		if updated.ManagerID == nil || *updated.ManagerID != people["bob"].ID {
			t.Fatalf("expected carol's manager_id to be %q, got %+v", people["bob"].ID, updated.ManagerID)
		}
		// The persisted record must reflect the assignment too.
		stored := people["carol"]
		if stored.ManagerID == nil || *stored.ManagerID != people["bob"].ID {
			t.Fatalf("expected stored carol manager_id to be %q, got %+v", people["bob"].ID, stored.ManagerID)
		}
	})

	t.Run("unassign clears manager", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		bobID := people["bob"].ID
		if _, err := svc.AssignManager(context.Background(), people["admin"].ID, people["carol"].ID, &bobID); err != nil {
			t.Fatalf("assign: %v", err)
		}

		updated, err := svc.AssignManager(context.Background(), people["admin"].ID, people["carol"].ID, nil)
		if err != nil {
			t.Fatalf("AssignManager(nil): %v", err)
		}
		if updated.ManagerID != nil {
			t.Fatalf("expected carol's manager_id to be nil, got %q", *updated.ManagerID)
		}
	})

	t.Run("non-admin caller is forbidden", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)

		_, err := svc.AssignManager(context.Background(), people["alice"].ID, people["carol"].ID, &people["bob"].ID)
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("expected ErrForbidden for non-admin caller, got %v", err)
		}
	})

	t.Run("unknown caller is forbidden", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)

		_, err := svc.AssignManager(context.Background(), "id-ghost", people["carol"].ID, &people["bob"].ID)
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("expected ErrForbidden for unknown caller, got %v", err)
		}
	})

	t.Run("unknown target is not found", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)

		_, err := svc.AssignManager(context.Background(), people["admin"].ID, "id-ghost", &people["bob"].ID)
		if !errors.Is(err, apperror.ErrEmployeeNotFound) {
			t.Fatalf("expected ErrEmployeeNotFound for unknown target, got %v", err)
		}
	})

	t.Run("cross-org target is forbidden", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)

		_, err := svc.AssignManager(context.Background(), people["admin"].ID, people["outsider"].ID, &people["bob"].ID)
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("expected ErrForbidden for cross-org target, got %v", err)
		}
	})

	t.Run("unknown manager is invalid", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		ghost := "id-ghost"

		_, err := svc.AssignManager(context.Background(), people["admin"].ID, people["carol"].ID, &ghost)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for unknown manager, got %v", err)
		}
	})

	t.Run("cross-org manager is invalid", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		outsider := people["outsider"].ID

		_, err := svc.AssignManager(context.Background(), people["admin"].ID, people["carol"].ID, &outsider)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for cross-org manager, got %v", err)
		}
	})

	t.Run("self-assignment is invalid", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		carol := people["carol"].ID

		_, err := svc.AssignManager(context.Background(), people["admin"].ID, carol, &carol)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for self-assignment, got %v", err)
		}
	})

	t.Run("direct cycle is rejected", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		// bob's manager is carol; assigning carol's manager = bob would
		// create bob -> carol -> bob.
		carol := people["carol"].ID
		if _, err := svc.AssignManager(context.Background(), people["admin"].ID, people["bob"].ID, &carol); err != nil {
			t.Fatalf("seed assign: %v", err)
		}

		bob := people["bob"].ID
		_, err := svc.AssignManager(context.Background(), people["admin"].ID, carol, &bob)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for cycle, got %v", err)
		}
	})

	t.Run("longer chain cycle is rejected", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		// Chain: dave -> bob -> alice. Assigning alice's manager = dave
		// would close dave -> bob -> alice -> dave.
		alice := people["alice"].ID
		bob := people["bob"].ID
		dave := people["dave"].ID
		ctx := context.Background()
		if _, err := svc.AssignManager(ctx, people["admin"].ID, bob, &alice); err != nil {
			t.Fatalf("seed bob->alice: %v", err)
		}
		if _, err := svc.AssignManager(ctx, people["admin"].ID, dave, &bob); err != nil {
			t.Fatalf("seed dave->bob: %v", err)
		}

		_, err := svc.AssignManager(ctx, people["admin"].ID, alice, &dave)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for longer-chain cycle, got %v", err)
		}
	})

	t.Run("deep valid chain within cap is allowed", func(t *testing.T) {
		svc, repo, _ := newAssignTestService(t)
		ctx := context.Background()

		// Build a chain of 10 employees under alice; then assign alice's
		// manager = carol. The walk from carol is short, so this must pass.
		prev := "id-alice"
		for i := 0; i < 10; i++ {
			id := "id-chain-" + strconv.Itoa(i)
			e := &model.Employee{ID: id, Name: "Chain" + strconv.Itoa(i), OrganizationName: "org-1", Role: model.RoleUser, Email: "chain" + strconv.Itoa(i) + "@example.com"}
			repo.byID[id] = e
			mgr := prev
			e.ManagerID = &mgr
			prev = id
		}
		carol := "id-carol"
		if _, err := svc.AssignManager(ctx, "id-admin", carol, &prev); err != nil {
			t.Fatalf("expected deep-but-valid chain assignment to succeed, got %v", err)
		}
	})

	t.Run("chain exceeding hop cap is rejected", func(t *testing.T) {
		svc, repo, _ := newAssignTestService(t)
		ctx := context.Background()

		// Build a pre-existing cycle among chain employees (loop of 60, none
		// of them the target); assigning a member of that loop as the target's
		// manager must hit the hop cap and be rejected.
		const n = 60
		for i := 0; i < n; i++ {
			id := "id-loop-" + strconv.Itoa(i)
			next := "id-loop-" + strconv.Itoa((i+1)%n)
			e := &model.Employee{ID: id, Name: "Loop" + strconv.Itoa(i), OrganizationName: "org-1", Role: model.RoleUser, Email: "loop" + strconv.Itoa(i) + "@example.com"}
			e.ManagerID = &next
			repo.byID[id] = e
		}
		loop0 := "id-loop-0"

		_, err := svc.AssignManager(ctx, "id-admin", "id-carol", &loop0)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for hop-cap violation, got %v", err)
		}
	})

	t.Run("missing caller id is invalid", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)

		_, err := svc.AssignManager(context.Background(), "", people["carol"].ID, &people["bob"].ID)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for missing caller, got %v", err)
		}
	})

	t.Run("missing target id is invalid", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)

		_, err := svc.AssignManager(context.Background(), people["admin"].ID, "", &people["bob"].ID)
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for missing target, got %v", err)
		}
	})
}

func TestListReportees(t *testing.T) {
	t.Run("returns only direct reportees", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		ctx := context.Background()

		// carol has two direct reportees; bob also reports to carol.
		for _, target := range []string{"id-alice", "id-bob", "id-dave"} {
			carol := "id-carol"
			if _, err := svc.AssignManager(ctx, people["admin"].ID, target, &carol); err != nil {
				t.Fatalf("seed assign %s: %v", target, err)
			}
		}

		got, nextCursor, err := svc.ListReportees(ctx, "id-carol", 0, "")
		if err != nil {
			t.Fatalf("ListReportees: %v", err)
		}
		if nextCursor != "" {
			t.Fatalf("expected no next cursor, got %q", nextCursor)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 reportees, got %d", len(got))
		}
		// Ordered by name ascending: Alice, Bob, Dave.
		wantNames := []string{"Alice", "Bob", "Dave"}
		for i, want := range wantNames {
			if got[i].Name != want {
				t.Fatalf("reportee[%d]: got name %q, want %q", i, got[i].Name, want)
			}
		}
	})

	t.Run("paginates via cursor", func(t *testing.T) {
		svc, _, people := newAssignTestService(t)
		ctx := context.Background()
		carol := "id-carol"
		for _, target := range []string{"id-alice", "id-bob", "id-dave"} {
			if _, err := svc.AssignManager(ctx, people["admin"].ID, target, &carol); err != nil {
				t.Fatalf("seed assign %s: %v", target, err)
			}
		}

		page1, cursor, err := svc.ListReportees(ctx, "id-carol", 2, "")
		if err != nil {
			t.Fatalf("page 1: %v", err)
		}
		if len(page1) != 2 || cursor == "" {
			t.Fatalf("expected 2 reportees and a cursor, got %d and %q", len(page1), cursor)
		}

		page2, cursor2, err := svc.ListReportees(ctx, "id-carol", 2, cursor)
		if err != nil {
			t.Fatalf("page 2: %v", err)
		}
		if len(page2) != 1 || cursor2 != "" {
			t.Fatalf("expected 1 reportee and no cursor, got %d and %q", len(page2), cursor2)
		}
		if page2[0].Name != "Dave" {
			t.Fatalf("expected Dave on page 2, got %q", page2[0].Name)
		}
	})

	t.Run("unknown cursor is employee-not-found", func(t *testing.T) {
		svc, _, _ := newAssignTestService(t)

		_, _, err := svc.ListReportees(context.Background(), "id-carol", 0, "id-ghost")
		if !errors.Is(err, apperror.ErrEmployeeNotFound) {
			t.Fatalf("expected ErrEmployeeNotFound for unknown cursor, got %v", err)
		}
	})

	t.Run("no reportees yields empty page", func(t *testing.T) {
		svc, _, _ := newAssignTestService(t)

		got, nextCursor, err := svc.ListReportees(context.Background(), "id-alice", 0, "")
		if err != nil {
			t.Fatalf("ListReportees: %v", err)
		}
		if len(got) != 0 || nextCursor != "" {
			t.Fatalf("expected empty page with no cursor, got %d and %q", len(got), nextCursor)
		}
	})

	t.Run("missing manager id is invalid", func(t *testing.T) {
		svc, _, _ := newAssignTestService(t)

		_, _, err := svc.ListReportees(context.Background(), "", 0, "")
		if !apperror.IsInvalidEmployee(err) {
			t.Fatalf("expected ErrInvalidEmployee for missing manager, got %v", err)
		}
	})
}
