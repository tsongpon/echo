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

// ErrFeedbackPeriodNotFound is returned when no feedback period matches a
// lookup. Used by the feedback service when validating that a feedback entry's
// period_id refers to an existing period.
var ErrFeedbackPeriodNotFound = errors.New("feedback period not found")

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

// ErrInvalidFeedback indicates a validation failure of a feedback input. It
// carries a human-readable message describing the failed validation.
type ErrInvalidFeedback string

func (e ErrInvalidFeedback) Error() string { return string(e) }

// IsInvalidFeedback reports whether err is an ErrInvalidFeedback.
func IsInvalidFeedback(err error) bool {
	var target ErrInvalidFeedback
	return errors.As(err, &target)
}
