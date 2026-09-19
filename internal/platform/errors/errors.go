// Package errors defines the application error type and its structured codes.
//
// Codes are stable strings shared by the REST, MCP and browser adapters.
package errors

import (
	goerrors "errors"
	"fmt"
	"net/http"
)

// Structured domain error codes (section 8 of the specification).
const (
	CodeUnknown             = "internal_error"
	CodeNotFound            = "not_found"
	CodeInvalidPath         = "invalid_path"
	CodeValidationFailed    = "validation_failed"
	CodeRevisionConflict    = "revision_conflict"
	CodePathConflict        = "path_conflict"
	CodeInsufficientScope   = "insufficient_scope"
	CodeQuotaExceeded       = "quota_exceeded"
	CodeRateLimited         = "rate_limited"
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodePreconditionReq     = "precondition_required"
	CodeIdempotencyMismatch = "idempotency_mismatch"
	CodeTooLarge            = "payload_too_large"
	CodeAccessEnded         = "access_ended"
	CodeSignupsClosed       = "signups_closed"
)

// FieldError is a validation error bound to a field or source line.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// AppError is the error type crossing package boundaries.
type AppError struct {
	Code             string
	Message          string
	Err              error
	FieldErrors      []FieldError
	CurrentRevision  int64
	ExpectedRevision int64
	SuggestedPath    string
	RetryAfter       int
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause.
func (e *AppError) Unwrap() error { return e.Err }

// New creates an AppError.
func New(code, message string) *AppError { return &AppError{Code: code, Message: message} }

// Newf creates an AppError with a formatted message.
func Newf(code, format string, args ...any) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a cause to a new AppError.
func Wrap(code, message string, err error) *AppError {
	return &AppError{Code: code, Message: message, Err: err}
}

// WithField adds a field error.
func (e *AppError) WithField(field string, line int, message string) *AppError {
	e.FieldErrors = append(e.FieldErrors, FieldError{Field: field, Line: line, Message: message})
	return e
}

// HTTPStatus maps codes to REST statuses (section 9).
func (e *AppError) HTTPStatus() int {
	switch e.Code {
	case CodeNotFound:
		return http.StatusNotFound
	case CodeInvalidPath:
		return http.StatusBadRequest
	case CodeValidationFailed:
		return http.StatusUnprocessableEntity
	case CodeRevisionConflict:
		return http.StatusPreconditionFailed
	case CodePathConflict, CodeIdempotencyMismatch:
		return http.StatusConflict
	case CodeInsufficientScope, CodeForbidden:
		return http.StatusForbidden
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeQuotaExceeded:
		return http.StatusInsufficientStorage
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodePreconditionReq:
		return http.StatusPreconditionRequired
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeAccessEnded:
		return http.StatusNotFound
	case CodeSignupsClosed:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// As extracts an AppError from err, or wraps err as an internal error.
func As(err error) *AppError {
	var ae *AppError
	if goerrors.As(err, &ae) {
		return ae
	}
	return &AppError{Code: CodeUnknown, Message: "internal error", Err: err}
}

// Is reports whether err is an AppError with the given code.
func Is(err error, code string) bool {
	var ae *AppError
	if goerrors.As(err, &ae) {
		return ae.Code == code
	}
	return false
}
