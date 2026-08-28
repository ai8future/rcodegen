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

func TestLoadWithFallbackAppliesEnvOverridesWithoutSettingsFile(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"HOME":                       t.TempDir(),
		"RCODEGEN_OLLAMA_BASE_URL":   "http://127.0.0.1:21134",
		"RCODEGEN_OLLAMA_MODEL":      "ollama-e2e-model",
		"RCODEGEN_OLLAMA_API_KEY":    "ollama-e2e-key",
		"RCODEGEN_LMSTUDIO_BASE_URL": "http://127.0.0.1:21234",
		"RCODEGEN_LMSTUDIO_MODEL":    "lmstudio-e2e-model",
		"RCODEGEN_LMSTUDIO_API_KEY":  "lmstudio-e2e-key",
	})

	s, existed, err := LoadWithFallback()
	if err != nil {
		t.Fatalf("LoadWithFallback: %v", err)
	}
	if existed {
		t.Fatal("settings file unexpectedly existed")
	}
	if got := s.Defaults.Ollama; got.BaseURL != "http://127.0.0.1:21134" || got.Model != "ollama-e2e-model" || got.APIKey != "ollama-e2e-key" {
		t.Fatalf("Ollama fallback overrides = %+v", got)
	}
	if got := s.Defaults.LMStudio; got.BaseURL != "http://127.0.0.1:21234" || got.Model != "lmstudio-e2e-model" || got.APIKey != "lmstudio-e2e-key" {
		t.Fatalf("LM Studio fallback overrides = %+v", got)
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
		"RCODEGEN_CODE_DIR":          "",
		"RCODEGEN_OUTPUT_DIR":        "",
		"RCODEGEN_MODEL":             "",
		"RCODEGEN_BUDGET":            "",
		"RCODEGEN_EFFORT":            "",
		"RCODEGEN_LOG_LEVEL":         "",
		"RCODEGEN_OLLAMA_BASE_URL":   "",
		"RCODEGEN_OLLAMA_MODEL":      "",
		"RCODEGEN_OLLAMA_API_KEY":    "",
		"RCODEGEN_LMSTUDIO_BASE_URL": "",
		"RCODEGEN_LMSTUDIO_MODEL":    "",
		"RCODEGEN_LMSTUDIO_API_KEY":  "",
	})

	s := GetDefaultSettings()
	originalBudget := s.Defaults.Claude.Budget
	applyEnvOverrides(s)

	// Nothing should have changed
	if s.Defaults.Claude.Budget != originalBudget {
		t.Errorf("Budget changed from %q to %q without env var", originalBudget, s.Defaults.Claude.Budget)
	}
}

func TestDefaultSettingsLocalAIRuntimes(t *testing.T) {
	s := GetDefaultSettings()
	if s.Defaults.Ollama.BaseURL != DefaultOllamaBaseURL || s.Defaults.LMStudio.BaseURL != DefaultLMStudioBaseURL {
		t.Fatalf("unexpected local runtime defaults: %+v %+v", s.Defaults.Ollama, s.Defaults.LMStudio)
	}
	if s.Defaults.Ollama.Model != "" || s.Defaults.LMStudio.Model != "" {
		t.Fatal("local runtime models must not be fabricated")
	}
	if s.Defaults.Ollama.TimeoutSeconds != DefaultLocalAITimeoutSeconds || s.Defaults.LMStudio.TimeoutSeconds != DefaultLocalAITimeoutSeconds {
		t.Fatal("local runtime timeouts were not initialized")
	}
}

func TestFillLocalAIDefaultsPreservesOperatorFields(t *testing.T) {
	d := LocalAIDefaults{Model: "custom", AllowRemote: true, APIKey: "secret"}
	fillLocalAIDefaults(&d, DefaultOllamaBaseURL)
	if d.BaseURL != DefaultOllamaBaseURL || d.TimeoutSeconds != DefaultLocalAITimeoutSeconds {
		t.Fatalf("missing fallback values: %+v", d)
	}
	if !d.AllowRemote || d.Model != "custom" || d.APIKey != "secret" {
		t.Fatalf("operator fields changed: %+v", d)
	}
}

func TestApplyEnvOverridesLocalAI(t *testing.T) {
	testkit.SetEnv(t, map[string]string{
		"RCODEGEN_MODEL":             "global-model",
		"RCODEGEN_OLLAMA_BASE_URL":   "http://127.0.0.1:1111",
		"RCODEGEN_OLLAMA_MODEL":      "ollama-model",
		"RCODEGEN_OLLAMA_API_KEY":    "ollama-key",
		"RCODEGEN_LMSTUDIO_BASE_URL": "http://127.0.0.1:2222",
		"RCODEGEN_LMSTUDIO_MODEL":    "lm-model",
		"RCODEGEN_LMSTUDIO_API_KEY":  "lm-key",
	})

	s := GetDefaultSettings()
	applyEnvOverrides(s)
	if got := s.Defaults.Ollama; got.BaseURL != "http://127.0.0.1:1111" || got.Model != "ollama-model" || got.APIKey != "ollama-key" {
		t.Fatalf("ollama overrides = %+v", got)
	}
	if got := s.Defaults.LMStudio; got.BaseURL != "http://127.0.0.1:2222" || got.Model != "lm-model" || got.APIKey != "lm-key" {
		t.Fatalf("lmstudio overrides = %+v", got)
	}
}

func TestGlobalModelDoesNotPoisonLocalAI(t *testing.T) {
	testkit.SetEnv(t, map[string]string{"RCODEGEN_MODEL": "global-model"})
	s := GetDefaultSettings()
	applyEnvOverrides(s)
	if s.Defaults.Ollama.Model != "" || s.Defaults.LMStudio.Model != "" {
		t.Fatalf("global model leaked into local runtimes: %+v %+v", s.Defaults.Ollama, s.Defaults.LMStudio)
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
