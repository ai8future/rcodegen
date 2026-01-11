package bundle

import (
	"strings"
	"testing"
)

func TestLoad_RejectsPathTraversal(t *testing.T) {
	maliciousNames := []string{
		"../../../etc/passwd",
		"..\\..\\..\\windows\\system32\\config\\sam",
		"foo/../bar",
		"./hidden",
		"foo/bar",
		".hidden",
	}

	for _, name := range maliciousNames {
		t.Run(name, func(t *testing.T) {
			_, err := Load(name)
			if err == nil {
				t.Errorf("Load(%q) should have failed for path traversal", name)
				return
			}
			if !strings.Contains(err.Error(), "invalid bundle name") {
				t.Errorf("Load(%q) error should mention 'invalid bundle name', got: %v", name, err)
			}
		})
	}
}

func TestLoad_AcceptsValidNames(t *testing.T) {
	validNames := []string{
		"compete",
		"security-review",
		"red_team",
		"test123",
		"my-bundle-v2",
	}

	for _, name := range validNames {
		t.Run(name, func(t *testing.T) {
			_, err := Load(name)
			// Should fail with "bundle not found", not "invalid bundle name"
			if err != nil && strings.Contains(err.Error(), "invalid bundle name") {
				t.Errorf("Load(%q) incorrectly rejected valid name: %v", name, err)
			}
		})
	}
}

func TestValidateBundleName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"compete", true},
		{"security-review", true},
		{"red_team", true},
		{"test123", true},
		{"../etc/passwd", false},
		{"foo/bar", false},
		{".hidden", false},
		{"", false},
		{"a", true},
		{strings.Repeat("a", 100), true},
		{strings.Repeat("a", 101), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBundleName(tt.name)
			if tt.valid && err != nil {
				t.Errorf("validateBundleName(%q) = %v, want nil", tt.name, err)
			}
			if !tt.valid && err == nil {
				t.Errorf("validateBundleName(%q) = nil, want error", tt.name)
			}
		})
	}
}
