// Package apperror defines the domain errors that cross the service-to-handler
// boundary. Centralizing them here keeps the transport (handler) layer from
// importing lower layers (e.g. repository) just to inspect error sentinels:
// the service is the single translator of any lower-layer or internal error
// into one of these, and the handler depends only on this package.
//
// Errors that are purely internal or construction-time (for example
// auth.ErrInvalidSecret) are intentionally NOT defined here; they are not part
// of the API-facing vocabulary.
package apperror

import "errors"

// ErrEmployeeNotFound is returned when no employee matches a lookup.
var ErrEmployeeNotFound = errors.New("employee not found")

// ErrInvalidCredentials is returned by authentication operations when the
// supplied email/password do not match a stored employee.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrEmailNotVerified is returned by Login when the credentials are valid but
// the employee's email has not been verified yet. It is distinct from
// ErrInvalidCredentials so the handler can respond with an actionable message
// rather than a generic auth failure.
var ErrEmailNotVerified = errors.New("email not verified")

// ErrInvalidVerificationToken is returned when an email-verification token is
// malformed, expired, or does not correspond to a known employee. A single
// sentinel for all failure modes avoids leaking why a token was rejected.
var ErrInvalidVerificationToken = errors.New("invalid or expired verification token")

// ErrInvalidInvitationToken is returned when an invitation token supplied at
// registration is malformed, expired, wrong-key, or wrong-purpose. As with
// ErrInvalidVerificationToken, a single sentinel avoids leaking the reason.
var ErrInvalidInvitationToken = errors.New("invalid or expired invitation token")

// ErrEmailTaken is returned by Create when an employee with the same (case-
// normalized) email already exists. Email is a global identity, so uniqueness
// spans organizations.
var ErrEmailTaken = errors.New("email already taken")

// ErrOrganizationTaken is returned by Register when a caller attempts to
// bootstrap (register without an invitation token into) an organization that
// already has members. The first admin of an organization registers freely;
// everyone after must present a valid invitation token.
var ErrOrganizationTaken = errors.New("organization already exists")

// ErrFeedbackPeriodNotFound is returned when no feedback period matches a
// lookup. Used by the feedback service when validating that a feedback entry's
// period_id refers to an existing period.
var ErrFeedbackPeriodNotFound = errors.New("feedback period not found")

// ErrFeedbackNotFound is returned when no feedback entry matches a lookup.
// Used by the feedback service's list path to signal that a cursor passed by
// the client does not refer to an existing feedback entry, so the handler can
// map it to a 400 "unknown cursor".
var ErrFeedbackNotFound = errors.New("feedback not found")

// ErrForbidden is returned when the authenticated caller is not allowed to
// perform the operation on the named resource (e.g. a caller who is not the
// reviewee's manager requesting the reviewee's feedback). It is distinct from
// an invalid input (400) and from a missing resource (404): the resource
// exists and the request is well-formed, but this caller may not access it.
// The handler maps it to 403.
var ErrForbidden = errors.New("forbidden")

// ErrInvalidEmployee indicates a validation failure of an employee input. It
// carries a human-readable message describing the failed validation.
type ErrInvalidEmployee string

func (e ErrInvalidEmployee) Error() string { return string(e) }

// IsInvalidEmployee reports whether err is an ErrInvalidEmployee.
func IsInvalidEmployee(err error) bool {
	var target ErrInvalidEmployee
	return errors.As(err, &target)
}

// ErrInvalidFeedbackPeriod indicates a validation failure of a feedback-period
// input. It carries a human-readable message describing the failed validation.
type ErrInvalidFeedbackPeriod string

func (e ErrInvalidFeedbackPeriod) Error() string { return string(e) }

// IsInvalidFeedbackPeriod reports whether err is an ErrInvalidFeedbackPeriod.
func IsInvalidFeedbackPeriod(err error) bool {
	var target ErrInvalidFeedbackPeriod
	return errors.As(err, &target)
}

// ErrFeedbackDraftAlreadyExists is returned when the reviewer already has a
// draft for the same (reviewee, period) pair. A unique-constraint ledger
// document backs this rule, so it is enforced atomically at create time.
var ErrFeedbackDraftAlreadyExists = errors.New("a draft for this reviewee and period already exists")

// ErrFeedbackPeriodClosed is returned when feedback is submitted outside the
// period's date window (before start_date or after end_date). The period's
// existence is a separate check (ErrFeedbackPeriodNotFound); this one means
// the period exists but is not open for submission.
var ErrFeedbackPeriodClosed = errors.New("feedback period is not open for submission")

// ErrFeedbackConcurrentUpdate is returned when a feedback draft changed
// between the caller's read and the write, so the write was rejected rather
// than allowed to overwrite the newer state. The caller may retry with a
// fresh read. It is defined here rather than in the repository package so
// the handler can map it to a 409 without importing lower layers.
var ErrFeedbackConcurrentUpdate = errors.New("feedback draft was modified concurrently")

// ErrInvalidFeedback indicates a validation failure of a feedback input. It
// carries a human-readable message describing the failed validation.
type ErrInvalidFeedback string

func (e ErrInvalidFeedback) Error() string { return string(e) }

// IsInvalidFeedback reports whether err is an ErrInvalidFeedback.
func IsInvalidFeedback(err error) bool {
	var target ErrInvalidFeedback
	return errors.As(err, &target)
}
