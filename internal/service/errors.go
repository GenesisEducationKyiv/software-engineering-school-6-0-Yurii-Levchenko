package service

import "net/http"

// HTTPError is implemented by domain errors that know their own HTTP
// status code. Handlers use errors.As to map any business error to the
// correct response without a switch statement (OCP — adding a new error
// doesn't require touching the handler). The error itself is the
// Information Expert about its semantics: only it knows whether it's a
// 400, 404, 409 etc.
type HTTPError interface {
	error
	Status() int
}

// statusError wraps a message with a status code. All package-level
// Err* values below are *statusError instances.
type statusError struct {
	msg    string
	status int
}

func (e *statusError) Error() string { return e.msg }
func (e *statusError) Status() int   { return e.status }

// newHTTPError is the canonical constructor for domain errors that carry
// their HTTP status. Use it for new domain errors instead of fmt.Errorf.
func newHTTPError(status int, msg string) *statusError {
	return &statusError{msg: msg, status: status}
}

// Domain errors with their canonical HTTP status codes. Handler doesn't
// need to know which one is which — it asks via Status().
var (
	ErrInvalidEmail      = newHTTPError(http.StatusBadRequest, "invalid email address")
	ErrInvalidRepoFormat = newHTTPError(http.StatusBadRequest, "invalid repository format, expected owner/repo")
	ErrRepoNotFound      = newHTTPError(http.StatusNotFound, "repository not found on GitHub")
	ErrAlreadySubscribed = newHTTPError(http.StatusConflict, "email is already subscribed to this repository")
	ErrTokenNotFound     = newHTTPError(http.StatusNotFound, "subscription not found")
)

// Compile-time assertion that all our errors implement HTTPError.
var (
	_ HTTPError = ErrInvalidEmail
	_ HTTPError = ErrInvalidRepoFormat
	_ HTTPError = ErrRepoNotFound
	_ HTTPError = ErrAlreadySubscribed
	_ HTTPError = ErrTokenNotFound
)
