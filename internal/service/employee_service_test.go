package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

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
	f.created = employee
	employee.ID = "emp-1"
	if f.byEmail == nil {
		f.byEmail = make(map[string]*model.Employee)
	}
	if f.byID == nil {
		f.byID = make(map[string]*model.Employee)
	}
	f.byEmail[strings.ToLower(employee.Email)] = employee
	f.byID[employee.ID] = employee
	return employee, nil
}

func (f *fakeRepo) GetByEmail(_ context.Context, email string) (*model.Employee, error) {
	if f.byEmail == nil {
		return nil, ErrEmployeeNotFound
	}
	if e, ok := f.byEmail[strings.ToLower(email)]; ok {
		return e, nil
	}
	return nil, ErrEmployeeNotFound
}

func (f *fakeRepo) GetByID(_ context.Context, id string) (*model.Employee, error) {
	if f.byID == nil {
		return nil, ErrEmployeeNotFound
	}
	if e, ok := f.byID[id]; ok {
		return e, nil
	}
	return nil, ErrEmployeeNotFound
}

func (f *fakeRepo) Update(_ context.Context, employee *model.Employee) (*model.Employee, error) {
	if f.byID == nil {
		f.byID = make(map[string]*model.Employee)
	}
	f.byID[employee.ID] = employee
	return employee, nil
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
	m := &noopMailer{}
	// Discard log output so service tests stay quiet.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewEmployeeService(&fakeRepo{}, m, signer, logger), m
}

func TestRegister_HashesPassword(t *testing.T) {
	svc, _ := newTestService()

	plaintext := "supersecret"
	emp := &model.Employee{
		Name:           "Alice",
		OrganizationID: "org-1",
		Email:          "alice@example.com",
		Password:       plaintext,
	}

	created, err := svc.Register(context.Background(), emp)
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

	created, err := svc.Register(context.Background(), &model.Employee{
		Name:           "Alice",
		OrganizationID: "org-1",
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

func TestLogin(t *testing.T) {
	svc, _ := newTestService()

	const plaintext = "supersecret"
	_, err := svc.Register(context.Background(), &model.Employee{
		Name:           "Alice",
		OrganizationID: "org-1",
		Email:          "alice@example.com",
		Password:       plaintext,
	})
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
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
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("unknown email", func(t *testing.T) {
		_, err := svc.Login(context.Background(), "nobody@example.com", plaintext)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected ErrInvalidCredentials, got %v", err)
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
}

func TestGetByID(t *testing.T) {
	svc, _ := newTestService()

	created, err := svc.Register(context.Background(), &model.Employee{
		Name:           "Alice",
		OrganizationID: "org-1",
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
		if !errors.Is(err, ErrEmployeeNotFound) {
			t.Fatalf("expected ErrEmployeeNotFound, got %v", err)
		}
	})

	t.Run("empty id", func(t *testing.T) {
		_, err := svc.GetByID(context.Background(), "")
		if !errors.Is(err, ErrEmployeeNotFound) {
			t.Fatalf("expected ErrEmployeeNotFound, got %v", err)
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
				OrganizationID: "org-1",
				Email:          "a@example.com",
				Password:       "pw",
			},
			wantMsg: "name is required",
		},
		{
			name: "missing email",
			emp: &model.Employee{
				Name:           "Bob",
				OrganizationID: "org-1",
				Password:       "pw",
			},
			wantMsg: "email is required",
		},
		{
			name: "missing organization_id",
			emp: &model.Employee{
				Name:     "Bob",
				Email:    "a@example.com",
				Password: "pw",
			},
			wantMsg: "organization_id is required",
		},
		{
			name: "missing password",
			emp: &model.Employee{
				Name:           "Bob",
				OrganizationID: "org-1",
				Email:          "a@example.com",
			},
			wantMsg: "password is required",
		},
		{
			name: "password too long",
			emp: &model.Employee{
				Name:           "Bob",
				OrganizationID: "org-1",
				Email:          "a@example.com",
				Password:       strings.Repeat("x", maxPasswordLen+1),
			},
			wantMsg: "password must be at most 64 characters",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService()
			_, err := svc.Register(context.Background(), tc.emp)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !IsInvalidEmployee(err) {
				t.Fatalf("expected ErrInvalidEmployee, got %T: %v", err, err)
			}
			if err.Error() != tc.wantMsg {
				t.Fatalf("expected message %q, got %q", tc.wantMsg, err.Error())
			}
		})
	}
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

	created, err := svc.Register(context.Background(), &model.Employee{
		Name:           "Alice",
		OrganizationID: "org-1",
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
		created, err := svc.Register(context.Background(), &model.Employee{
			Name:           "Alice",
			OrganizationID: "org-1",
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
		if err := svc.VerifyEmail(context.Background(), ""); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("garbage token", func(t *testing.T) {
		svc, _, _ := setup(t)
		if err := svc.VerifyEmail(context.Background(), "not-a-jwt"); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
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
		if err := svc.VerifyEmail(context.Background(), accessToken); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
		}
	})

	t.Run("token for unknown employee", func(t *testing.T) {
		svc, _, _ := setup(t)

		token, err := testSigner(t, 0).Sign(&model.Employee{ID: "ghost", Email: "ghost@example.com"})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if err := svc.VerifyEmail(context.Background(), token); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
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

		if err := svc.VerifyEmail(context.Background(), token); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("expected ErrInvalidVerificationToken, got %v", err)
		}
	})
}
