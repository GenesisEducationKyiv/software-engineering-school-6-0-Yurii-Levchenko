package model

import (
	"errors"
	"testing"
)

func TestParseRepoSpec(t *testing.T) {
	tests := []struct {
		input     string
		owner     string
		name      string
		expectErr bool
	}{
		{"golang/go", "golang", "go", false},
		{"facebook/react", "facebook", "react", false},
		{"owner/repo-name", "owner", "repo-name", false},
		{"invalid", "", "", true},
		{"/repo", "", "", true},
		{"owner/", "", "", true},
		{"", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			spec, err := ParseRepoSpec(tt.input)
			if tt.expectErr {
				if err == nil {
					t.Errorf("ParseRepoSpec(%q) expected error, got nil", tt.input)
				}
				if !errors.Is(err, ErrInvalidRepoFormat) {
					t.Errorf("ParseRepoSpec(%q) expected ErrInvalidRepoFormat, got %v", tt.input, err)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseRepoSpec(%q) unexpected error: %v", tt.input, err)
			}
			if spec.Owner != tt.owner || spec.Name != tt.name {
				t.Errorf("ParseRepoSpec(%q) = {%q, %q}, want {%q, %q}",
					tt.input, spec.Owner, spec.Name, tt.owner, tt.name)
			}
		})
	}
}

func TestRepoSpec_String(t *testing.T) {
	spec := RepoSpec{Owner: "golang", Name: "go"}
	if got := spec.String(); got != "golang/go" {
		t.Errorf("RepoSpec.String() = %q, want %q", got, "golang/go")
	}
}
