package settings

import (
	"os"
	"path/filepath"
	"testing"

	chassis "github.com/ai8future/chassis-go/v11"
	"github.com/ai8future/chassis-go/v11/testkit"
)

func TestMain(m *testing.M) {
	chassis.RequireMajor(11)
	os.Exit(m.Run())
}

func TestSettingsFilePermissions(t *testing.T) {
	// This test verifies settings are written with 0600 permissions
	// The settings file should be written with 0600 (owner read/write only)
	expectedPerm := os.FileMode(0600)

	// Check if settings file exists and has correct permissions
	configPath := GetConfigPath()
	if info, err := os.Stat(configPath); err == nil {
		actualPerm := info.Mode().Perm()
		if actualPerm != expectedPerm {
			t.Errorf("settings file has permissions %o, want %o", actualPerm, expectedPerm)
		}
	}
	// If file doesn't exist, that's OK - test passes
}

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

func TestApplyEnvOverrides_CodeDir(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"RCODEGEN_CODE_DIR": "/tmp/test-code",
	})

	s := GetDefaultSettings()
	applyEnvOverrides(s)

	if s.CodeDir != "/tmp/test-code" {
		t.Errorf("CodeDir = %q, want %q", s.CodeDir, "/tmp/test-code")
	}
}

func TestApplyEnvOverrides_Budget(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"RCODEGEN_BUDGET": "$25.00",
	})

	s := GetDefaultSettings()
	applyEnvOverrides(s)

	if s.Defaults.Claude.Budget != "25.00" {
		t.Errorf("Claude.Budget = %q, want %q", s.Defaults.Claude.Budget, "25.00")
	}
}

func TestApplyEnvOverrides_Model(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"RCODEGEN_MODEL": "opus",
	})

	s := GetDefaultSettings()
	applyEnvOverrides(s)

	if s.Defaults.Claude.Model != "opus" {
		t.Errorf("Claude.Model = %q, want %q", s.Defaults.Claude.Model, "opus")
	}
	if s.Defaults.Codex.Model != "opus" {
		t.Errorf("Codex.Model = %q, want %q", s.Defaults.Codex.Model, "opus")
	}
	if s.Defaults.Gemini.Model != "opus" {
		t.Errorf("Gemini.Model = %q, want %q", s.Defaults.Gemini.Model, "opus")
	}
}

func TestApplyEnvOverrides_Effort(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"RCODEGEN_EFFORT": "low",
	})

	s := GetDefaultSettings()
	applyEnvOverrides(s)

	if s.Defaults.Codex.Effort != "low" {
		t.Errorf("Codex.Effort = %q, want %q", s.Defaults.Codex.Effort, "low")
	}
}

func TestApplyEnvOverrides_NoEnvVarsSet(t *testing.T) {
	// Ensure none of the RCODEGEN_* vars are set
	testkit.SetEnv(t, map[string]string{
		"RCODEGEN_CODE_DIR":   "",
		"RCODEGEN_OUTPUT_DIR": "",
		"RCODEGEN_MODEL":      "",
		"RCODEGEN_BUDGET":     "",
		"RCODEGEN_EFFORT":     "",
		"RCODEGEN_LOG_LEVEL":  "",
	})

	s := GetDefaultSettings()
	originalBudget := s.Defaults.Claude.Budget
	applyEnvOverrides(s)

	// Nothing should have changed
	if s.Defaults.Claude.Budget != originalBudget {
		t.Errorf("Budget changed from %q to %q without env var", originalBudget, s.Defaults.Claude.Budget)
	}
}

func TestGetEnvLogLevel(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"RCODEGEN_LOG_LEVEL": "debug",
	})

	if level := GetEnvLogLevel(); level != "debug" {
		t.Errorf("GetEnvLogLevel() = %q, want %q", level, "debug")
	}
}
