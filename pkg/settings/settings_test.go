package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("could not get home dir: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"tilde prefix", "~/foo/bar", filepath.Join(home, "foo/bar")},
		{"just tilde", "~", home},
		{"no tilde", "/absolute/path", "/absolute/path"},
		{"tilde in middle", "/foo/~/bar", "/foo/~/bar"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandTilde(tt.input)
			if result != tt.expected {
				t.Errorf("expandTilde(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
