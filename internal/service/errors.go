package service

import "errors"

// ErrKind categorizes domain failures in transport-neutral terms.
// Handlers translate ErrKind into a concrete transport code
// (HTTP status, gRPC code, CLI exit). Service layer never knows
// about HTTP itself.
type ErrKind int

const (
	KindInternal ErrKind = iota // default — unexpected / wrapped infra error
	KindInvalid                 // user input violates a rule
	KindNotFound                // requested entity does not exist
	KindConflict                // operation conflicts with current state
)

// DomainError is the canonical service-layer error. The Kind drives
// transport translation; Message is human-readable.
type DomainError struct {
	Kind    ErrKind
	Message string
}

func (e *DomainError) Error() string { return e.Message }

// Predeclared sentinels for the well-known failure modes.
// Callers compare via errors.Is or service.KindOf.
var (
	ErrInvalidEmail      = &DomainError{Kind: KindInvalid, Message: "invalid email address"}
	ErrInvalidRepoFormat = &DomainError{Kind: KindInvalid, Message: "invalid repository format, expected owner/repo"}
	ErrRepoNotFound      = &DomainError{Kind: KindNotFound, Message: "repository not found on GitHub"}
	ErrAlreadySubscribed = &DomainError{Kind: KindConflict, Message: "email is already subscribed to this repository"}
	ErrTokenNotFound     = &DomainError{Kind: KindNotFound, Message: "subscription not found"}
)

// KindOf walks the error chain (errors.As) and returns the Kind of the
// nearest DomainError. Returns KindInternal for any non-domain error.
// Handlers use this for transport-code mapping.
func KindOf(err error) ErrKind {
	var de *DomainError
	if errors.As(err, &de) {
		return de.Kind
	}
	return KindInternal
}
