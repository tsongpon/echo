package repository

import "errors"

// ErrNilEmployee is returned when a nil employee is provided to a repository
// method that requires a non-nil employee.
var ErrNilEmployee = errors.New("employee must not be nil")

var ErrNilEmployeeID = errors.New("employee ID must not be nil or empty")

// ErrConcurrentUpdate is returned when a record changed between the read and
// the write of an update, so the write was rejected rather than allowed to
// overwrite the newer state. The caller may retry with a fresh read.
var ErrConcurrentUpdate = errors.New("employee was modified concurrently")

// ErrNilFeedbackPeriod is returned when a nil feedback period is provided to a
// repository method that requires a non-nil period.
var ErrNilFeedbackPeriod = errors.New("feedback period must not be nil")

var ErrNilFeedbackPeriodID = errors.New("feedback period ID must not be nil or empty")

// ErrNilFeedback is returned when a nil feedback entry is provided to a
// repository method that requires a non-nil feedback.
var ErrNilFeedback = errors.New("feedback must not be nil")

var ErrNilFeedbackID = errors.New("feedback ID must not be nil or empty")
