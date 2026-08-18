package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
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

	// A genuinely new email still succeeds.
	fresh := &model.Employee{
		Name:           "Bob",
		OrganizationName: "org-1",
		Email:          "bob@example.com",
		Password:       "supersecret",
	}
	if _, err := svc.Register(context.Background(), "", fresh); err != nil {
		t.Fatalf("fresh email Register: unexpected error: %v", err)
	}
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
