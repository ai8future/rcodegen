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
	if s.Defaults.OpenCode.Model != "opus" {
		t.Errorf("OpenCode.Model = %q, want %q", s.Defaults.OpenCode.Model, "opus")
	}
	if s.Defaults.KiloCode.Model != "opus" {
		t.Errorf("KiloCode.Model = %q, want %q", s.Defaults.KiloCode.Model, "opus")
	}
}

func TestOpenCodeDefaults_AppliedFromLoadFallback(t *testing.T) {
	s, _, err := LoadWithFallback()
	if err != nil {
		t.Fatalf("LoadWithFallback err: %v", err)
	}
	if s.Defaults.OpenCode.Model == "" {
		t.Errorf("expected opencode model default to be filled, got empty")
	}
	if s.Defaults.OpenCode.Provider == "" {
		t.Errorf("expected opencode provider default to be filled, got empty")
	}
}

func TestClaudeDefaults_AppliedFromLoadFallback(t *testing.T) {
	s, _, err := LoadWithFallback()
	if err != nil {
		t.Fatalf("LoadWithFallback err: %v", err)
	}
	if s.Defaults.Claude.Model == "" {
		t.Errorf("expected claude model default to be filled, got empty")
	}
	if s.Defaults.Claude.Budget == "" {
		t.Errorf("expected claude budget default to be filled, got empty")
	}
	if s.Defaults.Claude.Effort != DefaultClaudeEffort {
		t.Errorf("Claude.Effort = %q, want %q", s.Defaults.Claude.Effort, DefaultClaudeEffort)
	}
}

func TestDefaultSettings_UsesSupportedGeminiModel(t *testing.T) {
	s := GetDefaultSettings()
	if got := s.Defaults.Gemini.Model; got != "gemini-3.1-pro-preview" {
		t.Fatalf("Gemini.Model = %q, want gemini-3.1-pro-preview", got)
	}
}

func TestKiloCodeDefaults_AppliedFromLoadFallback(t *testing.T) {
	s, _, err := LoadWithFallback()
	if err != nil {
		t.Fatalf("LoadWithFallback err: %v", err)
	}
	if s.Defaults.KiloCode.Model == "" {
		t.Errorf("expected kilocode model default to be filled, got empty")
	}
	if s.Defaults.KiloCode.Provider == "" {
		t.Errorf("expected kilocode provider default to be filled, got empty")
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
	if s.Defaults.Claude.Effort != "low" {
		t.Errorf("Claude.Effort = %q, want %q", s.Defaults.Claude.Effort, "low")
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
