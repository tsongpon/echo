package repository

import "errors"

// ErrNilEmployee is returned when a nil employee is provided to a repository
// method that requires a non-nil employee.
var ErrNilEmployee = errors.New("employee must not be nil")
