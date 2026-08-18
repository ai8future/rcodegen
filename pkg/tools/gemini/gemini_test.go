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

// Gemini's stream-json stats carry token counts but no cost, so the tokens are
// reported and the cost stays absent rather than becoming a free run.
func TestReportedUsage_ReportsTokensWithoutCost(t *testing.T) {
	tool := New()
	res := &runner.RunResult{TokenUsage: &runner.TokenUsage{InputTokens: 800, OutputTokens: 250}}

	usage, ok := tool.ReportedUsage(res)
	if !ok {
		t.Fatal("ReportedUsage said nothing for a run that reported tokens")
	}
	if usage.InputTokens != 800 || usage.OutputTokens != 250 {
		t.Errorf("tokens = %d/%d, want 800/250", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CostUSD != 0 {
		t.Errorf("cost = %v, want 0 (gemini reports none)", usage.CostUSD)
	}

	if usage, ok := tool.ReportedUsage(&runner.RunResult{}); ok {
		t.Errorf("ReportedUsage(empty) = %+v, want no report", usage)
	}
}
