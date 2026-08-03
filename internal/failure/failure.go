// Package failure defines the CLI's stable failure categories.
package failure

import (
	"errors"
	"fmt"
)

const (
	Success    = 0
	Validation = 2
	Credential = 3
	Transport  = 4
	Provider   = 5
	Evidence   = 6
)

// Error is an error with a stable process exit code and machine-readable kind.
type Error struct {
	ExitCode int
	Kind     string
	Message  string
	Details  any
	Cause    error
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func New(exitCode int, kind, message string) *Error {
	return &Error{ExitCode: exitCode, Kind: kind, Message: message}
}

func Wrap(exitCode int, kind, message string, cause error) *Error {
	return &Error{ExitCode: exitCode, Kind: kind, Message: message, Cause: cause}
}

func WithDetails(err *Error, details any) *Error {
	err.Details = details
	return err
}

func Normalize(err error) *Error {
	if err == nil {
		return nil
	}
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	return Wrap(Validation, "internal_error", "command failed", err)
}

func Validationf(kind, format string, args ...any) *Error {
	return New(Validation, kind, fmt.Sprintf(format, args...))
}

func Evidencef(kind, format string, args ...any) *Error {
	return New(Evidence, kind, fmt.Sprintf(format, args...))
}
