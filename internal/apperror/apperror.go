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

// ErrInvalidEmployee indicates a validation failure of an employee input. It
// carries a human-readable message describing the failed validation.
type ErrInvalidEmployee string

func (e ErrInvalidEmployee) Error() string { return string(e) }

// IsInvalidEmployee reports whether err is an ErrInvalidEmployee.
func IsInvalidEmployee(err error) bool {
	var target ErrInvalidEmployee
	return errors.As(err, &target)
}
