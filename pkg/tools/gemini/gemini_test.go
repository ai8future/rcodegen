package gemini

import (
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

func TestApplyToolDefaultsUsesConfiguredModel(t *testing.T) {
	tool := New()
	tool.SetSettings(&settings.Settings{Defaults: settings.Defaults{
		Gemini: settings.GeminiDefaults{Model: "gemini-2.5-flash"},
	}})
	cfg := runner.NewConfig()
	cfg.Model = tool.DefaultModel()

	tool.ApplyToolDefaults(cfg)

	if cfg.Model != "gemini-2.5-flash" {
		t.Fatalf("model = %q, want configured Gemini model", cfg.Model)
	}
	if got := tool.DefaultModelSetting(); got != "gemini-2.5-flash" {
		t.Fatalf("DefaultModelSetting = %q", got)
	}
}
