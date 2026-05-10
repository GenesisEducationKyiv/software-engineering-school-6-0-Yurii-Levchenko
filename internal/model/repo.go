package model

import (
	"errors"
	"strings"
)

// ErrInvalidRepoFormat is returned when a repo string cannot be parsed
// It belongs to the model layer because parsing is RepoSpec's responsibility
// (GRASP Principle - Information Expert: the concept of "repo" knows how to validate itself)
var ErrInvalidRepoFormat = errors.New("invalid repository format, expected owner/repo")

// RepoSpec is a value object representing a GitHub repository identifier
// It encapsulates the "owner/repo" parsing rules so callers never have to
// split strings or validate the format themselves
type RepoSpec struct {
	Owner string
	Name  string
}

// ParseRepoSpec parses a string in "owner/repo" format into a RepoSpec
// Returns ErrInvalidRepoFormat if the format is wrong
func ParseRepoSpec(s string) (RepoSpec, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return RepoSpec{}, ErrInvalidRepoFormat
	}
	if strings.Contains(parts[0], "/") || strings.Contains(parts[1], "/") {
		return RepoSpec{}, ErrInvalidRepoFormat
	}
	return RepoSpec{Owner: parts[0], Name: parts[1]}, nil
}

// String returns the canonical "owner/repo" representation
func (r RepoSpec) String() string {
	return r.Owner + "/" + r.Name
}
