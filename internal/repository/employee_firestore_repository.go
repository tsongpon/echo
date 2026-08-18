package repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tsongpon/echo/internal/apperror"
	"github.com/tsongpon/echo/internal/model"
	"github.com/tsongpon/echo/internal/service"
)

// Compile-time assertion that EmployeeFirestoreRepository satisfies the
// service.EmployeeRepository interface.
var _ service.EmployeeRepository = (*EmployeeFirestoreRepository)(nil)

// EmployeeCollection is the Firestore collection that holds employee records.
const EmployeeCollection = "employees"

// EmailClaimCollection is a dedicated collection that enforces global email
// uniqueness. Each document is keyed by the normalized email and points at the
// employee that claimed it. Firestore only enforces uniqueness on document
// IDs, not on fields, so the email-claim document acts as a unique constraint
// ledger: a Create on an existing claim ID fails atomically inside the
// registration transaction.
const EmailClaimCollection = "employee_emails"

// EmployeeFirestoreRepository is a service.EmployeeRepository backed by GCP
// Firestore. Each employee is one document in the EmployeeCollection keyed by
// the employee ID, so lookups by ID are a direct document read rather than a
// query.
type EmployeeFirestoreRepository struct {
	client *firestore.Client
	logger *slog.Logger
}

// NewEmployeeFirestoreRepository creates a repository over the given Firestore
// client. The client is owned by the caller: closing it is the caller's
// responsibility. The logger defaults to slog.Default() when nil.
func NewEmployeeFirestoreRepository(client *firestore.Client, logger *slog.Logger) *EmployeeFirestoreRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &EmployeeFirestoreRepository{client: client, logger: logger}
}

// logError records a failed Firestore call.
//
// The gRPC status code is logged as its own field because it is what actually
// distinguishes Firestore failure modes, and it is otherwise buried in the
// error string: Unauthenticated and PermissionDenied point at credentials or
// IAM, FailedPrecondition usually means a required index is missing,
// ResourceExhausted means a quota was hit, and Unavailable or DeadlineExceeded
// are transient and worth retrying. status.Code unwraps, so the code survives
// the fmt.Errorf wrapping applied at each call site.
//
// Errors that are ordinary outcomes rather than failures -- a missing document
// on lookup, an iterator running dry -- are deliberately not routed here.
func (r *EmployeeFirestoreRepository) logError(msg string, err error, args ...any) {
	r.logger.Error(msg, append([]any{"error", err, "code", status.Code(err).String()}, args...)...)
}

// employeeDocument is the Firestore representation of model.Employee. It is
// kept separate from the domain model so the stored field names are an
// explicit, stable contract rather than a by-product of Go field naming.
//
// The employee ID is the document ID and is deliberately not duplicated as a
// field. Email is always stored lowercased: Firestore has no case-insensitive
// comparison, so an address is canonicalized on write and GetByEmail
// canonicalizes the search term the same way to match it.
type employeeDocument struct {
	Name             string     `firestore:"name"`
	OrganizationName string     `firestore:"organization_name"`
	Role             model.Role `firestore:"role"`
	ManagerID        *string    `firestore:"manager_id"`
	Title            string     `firestore:"title"`
	Email            string     `firestore:"email"`
	Password         string     `firestore:"password"`
	IsMailVerified   bool       `firestore:"is_mail_verified"`
	CreatedAt        time.Time  `firestore:"created_at"`
	UpdatedAt        time.Time  `firestore:"updated_at"`
}

// Create stores the given employee as a new document keyed by its ID and
// returns the stored record. The ID is assigned by the caller (the service
// layer); Create does not generate one. CreatedAt and UpdatedAt are set here to
// the current time, overriding whatever the caller supplied.
//
// Create fails if a document with the same ID already exists, so a retried
// create cannot silently overwrite an existing employee.
//
// Email uniqueness is enforced globally and atomically: inside a Firestore
// transaction Create first reads the email-claim document keyed by the
// normalized email, returns apperror.ErrEmailTaken if it already exists, then
// creates the claim and the employee document. Reading the claim inside the
// transaction adds it to the read set, so concurrent registrations for the
// same email are serialized by Firestore's per-document conflict detection and
// only one can commit; the loser retries, observes the new claim, and returns
// ErrEmailTaken. If the employee ID already exists the transaction returns an
// error and the email claim is rolled back, leaving no orphan claim.
//
// TODO: releasing/reclaiming email-claim documents on email change (Update)
// and on employee deletion is not yet implemented. Until then, an email
// claimed by a deleted or renamed employee cannot be reused.
func (r *EmployeeFirestoreRepository) Create(ctx context.Context, employee *model.Employee) (*model.Employee, error) {
	if employee == nil {
		return nil, ErrNilEmployee
	}
	if strings.TrimSpace(employee.ID) == "" {
		return nil, ErrNilEmployeeID
	}

	stored := *employee
	stored.Email = normalizeEmail(stored.Email)
	now := storeTime(time.Now())
	stored.CreatedAt = now
	stored.UpdatedAt = now

	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// Read the email-claim document first. tx.Get adds the document to the
		// transaction's read set, so Firestore's per-document conflict
		// detection serializes concurrent registrations for the same email:
		// if two transactions both read the missing claim, the first to commit
		// creates it and the second's read is invalidated, forcing a retry
		// that then observes the claim and returns ErrEmailTaken.
		claimRef := r.emailClaimDoc(stored.Email)
		if _, err := tx.Get(claimRef); err == nil {
			// The claim already exists: the email is taken.
			return apperror.ErrEmailTaken
		} else if status.Code(err) != codes.NotFound {
			return fmt.Errorf("firestore: read email claim: %w", err)
		}

		// Claim the email. tx.Create is buffered and sent at commit time; it
		// does not return AlreadyExists synchronously. The authoritative
		// duplicate check is the Get above plus the read-set conflict check
		// at commit time. The Create here is what makes the claim durable.
		if err := tx.Create(claimRef, map[string]any{
			"email":       stored.Email,
			"employee_id": stored.ID,
			"created_at":  stored.CreatedAt,
		}); err != nil {
			return fmt.Errorf("firestore: claim email: %w", err)
		}

		// Write the employee document. On failure the transaction rolls back
		// and the email claim above is discarded, so no orphan claim remains.
		if err := tx.Create(r.doc(stored.ID), newEmployeeDocument(&stored)); err != nil {
			return fmt.Errorf("firestore: create employee: %w", err)
		}
		return nil
	})
	if err != nil {
		// Logged out here rather than inside the transaction body: Firestore
		// re-runs that body on contention, so logging in it would emit a line
		// per attempt for what is ultimately one failure.
		switch {
		case errors.Is(err, apperror.ErrEmailTaken):
			// An duplicate email is a caller error, not a Firestore failure.
			// The service layer decides how to report it.
		default:
			r.logError("firestore: create employee failed", err, "employee_id", stored.ID, "email", stored.Email)
		}
		return nil, err
	}

	return &stored, nil
}

// GetByEmail returns the employee with the given email. The comparison is
// case-insensitive: the search term is canonicalized the same way stored
// addresses are, so callers need not lowercase first. Returns
// apperror.ErrEmployeeNotFound when no employee matches.
func (r *EmployeeFirestoreRepository) GetByEmail(ctx context.Context, email string) (*model.Employee, error) {
	normalized := normalizeEmail(email)
	if normalized == "" {
		return nil, apperror.ErrEmployeeNotFound
	}

	iter := r.client.Collection(EmployeeCollection).
		Where("email", "==", normalized).
		Limit(1).
		Documents(ctx)
	defer iter.Stop()

	snapshot, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, apperror.ErrEmployeeNotFound
	}
	if err != nil {
		r.logError("firestore: query employee by email failed", err)
		return nil, fmt.Errorf("firestore: query employee by email: %w", err)
	}

	return r.toEmployee(snapshot)
}

// GetByID returns the employee with the given ID. Returns
// apperror.ErrEmployeeNotFound when no employee matches.
func (r *EmployeeFirestoreRepository) GetByID(ctx context.Context, id string) (*model.Employee, error) {
	if strings.TrimSpace(id) == "" {
		return nil, apperror.ErrEmployeeNotFound
	}

	snapshot, err := r.doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, apperror.ErrEmployeeNotFound
	}
	if err != nil {
		r.logError("firestore: get employee by ID failed", err, "employee_id", id)
		return nil, fmt.Errorf("firestore: get employee by ID: %w", err)
	}

	return r.toEmployee(snapshot)
}

// Update overwrites the mutable fields of the stored employee and returns the
// updated record. ID and CreatedAt are preserved from the stored document;
// UpdatedAt is refreshed.
//
// The write is guarded by an optimistic concurrency check inside a
// transaction: if the stored UpdatedAt no longer matches the one on the
// supplied employee, the record changed since the caller read it and
// ErrConcurrentUpdate is returned instead of clobbering the newer state.
// Callers must therefore pass a record obtained from GetByID or GetByEmail
// (or from Create) rather than a hand-built struct. Returns
// apperror.ErrEmployeeNotFound when the ID is unknown.
func (r *EmployeeFirestoreRepository) Update(ctx context.Context, employee *model.Employee) (*model.Employee, error) {
	if employee == nil {
		return nil, ErrNilEmployee
	}
	if strings.TrimSpace(employee.ID) == "" {
		return nil, ErrNilEmployeeID
	}

	doc := r.doc(employee.ID)
	updated := *employee
	updated.Email = normalizeEmail(updated.Email)

	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(doc)
		if status.Code(err) == codes.NotFound {
			return apperror.ErrEmployeeNotFound
		}
		if err != nil {
			return fmt.Errorf("firestore: read employee for update: %w", err)
		}

		current, err := r.toEmployee(snapshot)
		if err != nil {
			return err
		}
		if !current.UpdatedAt.Equal(employee.UpdatedAt) {
			return ErrConcurrentUpdate
		}

		// ID and CreatedAt are not the caller's to change.
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = storeTime(time.Now())

		return tx.Set(doc, newEmployeeDocument(&updated))
	})
	if err != nil {
		// Logged out here rather than inside the transaction body: Firestore
		// re-runs that body on contention, so logging in it would emit a line
		// per attempt for what is ultimately one failure.
		switch {
		case errors.Is(err, apperror.ErrEmployeeNotFound):
			// An update against an unknown ID is a caller error, not a
			// Firestore failure. The service layer decides how to report it.
		case errors.Is(err, ErrConcurrentUpdate):
			// Expected under concurrent writes to one employee, and the caller
			// can retry, so this is not an error -- but a sustained rate of it
			// points at a hot document worth knowing about.
			r.logger.Warn("firestore: employee update rejected, record changed since it was read",
				"employee_id", employee.ID)
		default:
			r.logError("firestore: update employee failed", err, "employee_id", employee.ID)
		}
		return nil, err
	}

	return &updated, nil
}

// doc returns the document reference for the given employee ID.
func (r *EmployeeFirestoreRepository) doc(id string) *firestore.DocumentRef {
	return r.client.Collection(EmployeeCollection).Doc(id)
}

// emailClaimDoc returns the document reference for the email-claim document
// keyed by the normalized email. The caller is responsible for normalizing the
// email so the ID is canonical; passing a raw email is a programming error.
func (r *EmployeeFirestoreRepository) emailClaimDoc(normalizedEmail string) *firestore.DocumentRef {
	return r.client.Collection(EmailClaimCollection).Doc(normalizedEmail)
}

// newEmployeeDocument projects a domain employee onto its stored form.
func newEmployeeDocument(employee *model.Employee) *employeeDocument {
	return &employeeDocument{
		Name:             employee.Name,
		OrganizationName: employee.OrganizationName,
		Role:             employee.Role,
		ManagerID:        employee.ManagerID,
		Title:            employee.Title,
		Email:            employee.Email,
		Password:         employee.Password,
		IsMailVerified:   employee.IsMailVerified,
		CreatedAt:        employee.CreatedAt,
		UpdatedAt:        employee.UpdatedAt,
	}
}

// toEmployee decodes a document snapshot into a domain employee, taking the ID
// from the document key.
func (r *EmployeeFirestoreRepository) toEmployee(snapshot *firestore.DocumentSnapshot) (*model.Employee, error) {
	var doc employeeDocument
	if err := snapshot.DataTo(&doc); err != nil {
		// A stored document that no longer fits the struct: a schema change or
		// a record written by something other than this repository. Logged
		// with the document ID because the bad record has to be found by hand.
		r.logError("firestore: decode employee document failed", err, "employee_id", snapshot.Ref.ID)
		return nil, fmt.Errorf("firestore: decode employee %s: %w", snapshot.Ref.ID, err)
	}

	return &model.Employee{
		ID:               snapshot.Ref.ID,
		Name:             doc.Name,
		OrganizationName: doc.OrganizationName,
		Role:             doc.Role,
		ManagerID:        doc.ManagerID,
		Title:            doc.Title,
		Email:            doc.Email,
		Password:         doc.Password,
		IsMailVerified:   doc.IsMailVerified,
		CreatedAt:        doc.CreatedAt,
		UpdatedAt:        doc.UpdatedAt,
	}, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// storeTime normalizes a timestamp to what Firestore will hand back on the
// next read: UTC, truncated to microseconds. Without this, a timestamp
// generated in Go keeps nanosecond precision that the round trip drops, and
// the UpdatedAt equality check in Update would spuriously report a concurrent
// update for a caller working from a freshly created record.
func storeTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
