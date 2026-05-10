package service

import (
	"net/http"

	"github-release-notifier/internal/model"
)

// HTTPError is implemented by domain errors that know their own HTTP
// status code. Handlers use errors.As to map any business error to the
// correct response without a switch statement (OCP — adding a new error
// doesn't require touching the handler). The error itself is the
// Information Expert about its semantics.
type HTTPError interface {
	error
	Status() int
}

// statusError wraps a message with a status code. All plain domain
// errors below are *statusError instances.
type statusError struct {
	msg    string
	status int
}

func (e *statusError) Error() string { return e.msg }
func (e *statusError) Status() int   { return e.status }

func newHTTPError(status int, msg string) *statusError {
	return &statusError{msg: msg, status: status}
}

// Plain domain errors with their canonical HTTP status codes.
var (
	ErrInvalidEmail      = newHTTPError(http.StatusBadRequest, "invalid email address")
	ErrRepoNotFound      = newHTTPError(http.StatusNotFound, "repository not found on GitHub")
	ErrAlreadySubscribed = newHTTPError(http.StatusConflict, "email is already subscribed to this repository")
	ErrTokenNotFound     = newHTTPError(http.StatusNotFound, "subscription not found")
)

// ErrInvalidRepoFormat is special: the canonical sentinel lives in the
// model package (RepoSpec owns parsing rules — Information Expert).
// We wrap it here so handlers see an HTTPError with HTTP 400, while
// errors.Is(err, model.ErrInvalidRepoFormat) still matches via Is().
type repoFormatError struct{}

func (e *repoFormatError) Error() string { return model.ErrInvalidRepoFormat.Error() }
func (e *repoFormatError) Status() int   { return http.StatusBadRequest }
func (e *repoFormatError) Is(target error) bool {
	return target == model.ErrInvalidRepoFormat
}

var ErrInvalidRepoFormat HTTPError = &repoFormatError{}

// Compile-time assertions.
var (
	_ HTTPError = ErrInvalidEmail
	_ HTTPError = ErrRepoNotFound
	_ HTTPError = ErrAlreadySubscribed
	_ HTTPError = ErrTokenNotFound
	_ HTTPError = ErrInvalidRepoFormat
)
