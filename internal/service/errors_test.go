package service

import (
	"errors"
	"net/http"
	"testing"

	"github-release-notifier/internal/model"
)

// TestPredeclared_Status verifies that each predeclared domain error
// carries the correct HTTP status code via the HTTPError contract.
// This is what the handler relies on when it does errors.As(err, &he).
func TestPredeclared_Status(t *testing.T) {
	tests := []struct {
		name string
		err  HTTPError
		want int
	}{
		{"ErrInvalidEmail", ErrInvalidEmail, http.StatusBadRequest},
		{"ErrRepoNotFound", ErrRepoNotFound, http.StatusNotFound},
		{"ErrAlreadySubscribed", ErrAlreadySubscribed, http.StatusConflict},
		{"ErrTokenNotFound", ErrTokenNotFound, http.StatusNotFound},
		{"ErrInvalidRepoFormat", ErrInvalidRepoFormat, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Status(); got != tt.want {
				t.Errorf("Status() = %d, want %d", got, tt.want)
			}
			if tt.err.Error() == "" {
				t.Error("Error() must not be empty")
			}
		})
	}
}

// TestRepoFormatError_IsMatchesModelSentinel verifies the custom Is()
// method on *repoFormatError: callers can do
//
//	errors.Is(err, model.ErrInvalidRepoFormat)
//
// on the HTTPError instance and still find a match. Without the custom
// Is(), this would silently return false because *repoFormatError and
// the sentinel are different concrete types.
func TestRepoFormatError_IsMatchesModelSentinel(t *testing.T) {
	if !errors.Is(ErrInvalidRepoFormat, model.ErrInvalidRepoFormat) {
		t.Error("errors.Is(ErrInvalidRepoFormat, model.ErrInvalidRepoFormat) = false, want true")
	}
}

// TestRepoFormatError_NotMatchesUnrelated guards against a false positive
// where Is() would accept anything: only the model sentinel must match.
func TestRepoFormatError_NotMatchesUnrelated(t *testing.T) {
	other := errors.New("some other error")
	if errors.Is(ErrInvalidRepoFormat, other) {
		t.Error("errors.Is matched an unrelated error; Is() is too permissive")
	}
}

// TestHTTPError_AsViaErrorsAs reproduces what the handler does:
// take a plain `error` value and unwrap it into an HTTPError variable.
// This must work for every predeclared domain error.
func TestHTTPError_AsViaErrorsAs(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"ErrInvalidEmail", ErrInvalidEmail, http.StatusBadRequest},
		{"ErrRepoNotFound", ErrRepoNotFound, http.StatusNotFound},
		{"ErrAlreadySubscribed", ErrAlreadySubscribed, http.StatusConflict},
		{"ErrTokenNotFound", ErrTokenNotFound, http.StatusNotFound},
		{"ErrInvalidRepoFormat", ErrInvalidRepoFormat, http.StatusBadRequest},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var he HTTPError
			if !errors.As(tt.err, &he) {
				t.Fatalf("errors.As(%v, &HTTPError) = false", tt.err)
			}
			if he.Status() != tt.wantStatus {
				t.Errorf("Status() after As = %d, want %d", he.Status(), tt.wantStatus)
			}
		})
	}
}

// TestHTTPError_AsFailsForPlainError makes sure errors.As correctly
// reports "not an HTTPError" so handlers fall back to 500.
func TestHTTPError_AsFailsForPlainError(t *testing.T) {
	var he HTTPError
	if errors.As(errors.New("plain error"), &he) {
		t.Error("errors.As on plain error must return false")
	}
}
