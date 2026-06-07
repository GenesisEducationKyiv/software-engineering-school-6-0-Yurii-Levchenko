package service

import (
	"errors"
	"fmt"
	"testing"

	"github-release-notifier/internal/repospec"
)

// verifies that every predeclared domain error carries
// the correct categorical Kind — what handlers use for transport mapping
func TestPredeclared_Kind(t *testing.T) {
	tests := []struct {
		name string
		err  *DomainError
		want ErrKind
	}{
		{"ErrInvalidEmail", ErrInvalidEmail, KindInvalid},
		{"ErrInvalidRepoFormat", ErrInvalidRepoFormat, KindInvalid},
		{"ErrRepoNotFound", ErrRepoNotFound, KindNotFound},
		{"ErrAlreadySubscribed", ErrAlreadySubscribed, KindConflict},
		{"ErrTokenNotFound", ErrTokenNotFound, KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Kind != tt.want {
				t.Errorf("Kind = %v, want %v", tt.err.Kind, tt.want)
			}
			if tt.err.Error() == "" {
				t.Error("Error() must not be empty")
			}
		})
	}
}

// reproduces what the handler does: each predeclared sentinel passed
// as a plain `error` must yield the correct Kind via KindOf.
// Black-box equivalent of TestPredeclared_Kind — it catches bugs in
// KindOf (e.g., a missing errors.As call) that the white-box field test would not notice
func TestKindOf_OnPredeclaredErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrKind
	}{
		{"ErrInvalidEmail", ErrInvalidEmail, KindInvalid},
		{"ErrInvalidRepoFormat", ErrInvalidRepoFormat, KindInvalid},
		{"ErrRepoNotFound", ErrRepoNotFound, KindNotFound},
		{"ErrAlreadySubscribed", ErrAlreadySubscribed, KindConflict},
		{"ErrTokenNotFound", ErrTokenNotFound, KindNotFound},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindOf(tt.err); got != tt.want {
				t.Errorf("KindOf(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// verifies that fmt.Errorf("%w", ...) chains are walked correctly
// this is the path used when service translates a model error
func TestKindOf_UnwrapsWrappedDomainError(t *testing.T) {
	wrapped := fmt.Errorf("%w: %w", ErrInvalidRepoFormat, repospec.ErrInvalidRepoFormat)

	if got := KindOf(wrapped); got != KindInvalid {
		t.Errorf("KindOf(wrapped) = %v, want KindInvalid", got)
	}
	if !errors.Is(wrapped, repospec.ErrInvalidRepoFormat) {
		t.Error("errors.Is must still match the wrapped model sentinel")
	}
	if !errors.Is(wrapped, ErrInvalidRepoFormat) {
		t.Error("errors.Is must also match the service sentinel")
	}
}

// anything that isn't a DomainError defaults to KindInternal so the handler can return 500
func TestKindOf_NonDomainError_ReturnsInternal(t *testing.T) {
	if got := KindOf(errors.New("oops")); got != KindInternal {
		t.Errorf("KindOf(plain error) = %v, want KindInternal", got)
	}
}

// guards against handlers calling KindOf on a nil happy-path error (defensive)
func TestKindOf_NilError_ReturnsInternal(t *testing.T) {
	if got := KindOf(nil); got != KindInternal {
		t.Errorf("KindOf(nil) = %v, want KindInternal", got)
	}
}
