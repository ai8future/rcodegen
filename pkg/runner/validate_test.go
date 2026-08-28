package runner

import (
	"os/exec"
	"testing"
)

type validationTool struct {
	models  []string
	efforts []string
}

func (t validationTool) Name() string                                   { return "test" }
func (t validationTool) BinaryName() string                             { return "false" }
func (t validationTool) ReportDir() string                              { return "_rcodegen" }
func (t validationTool) ReportPrefix() string                           { return "test-" }
func (t validationTool) ValidModels() []string                          { return t.models }
func (t validationTool) ValidEfforts() []string                         { return t.efforts }
func (t validationTool) DefaultModel() string                           { return "" }
func (t validationTool) DefaultModelSetting() string                    { return "" }
func (t validationTool) BuildCommand(*Config, string, string) *exec.Cmd { return exec.Command("false") }
func (t validationTool) ShowStatus()                                    {}
func (t validationTool) SupportsStatusTracking() bool                   { return false }
func (t validationTool) CaptureStatusBefore() interface{}               { return nil }
func (t validationTool) CaptureStatusAfter() interface{}                { return nil }
func (t validationTool) PrintStatusSummary(interface{}, interface{})    {}
func (t validationTool) ToolSpecificFlags() []FlagDef                   { return nil }
func (t validationTool) ApplyToolDefaults(*Config)                      {}
func (t validationTool) PrepareForExecution(*Config)                    {}
func (t validationTool) ValidateConfig(*Config) error                   { return nil }
func (t validationTool) BannerTitle() string                            { return "" }
func (t validationTool) BannerSubtitle() string                         { return "" }
func (t validationTool) PrintToolSpecificBannerFields(*Config)          {}
func (t validationTool) PrintToolSpecificSummaryFields(*Config)         {}
func (t validationTool) SecurityWarning() []string                      { return nil }
func (t validationTool) ToolSpecificHelpSections() []HelpSection        { return nil }
func (t validationTool) StatsJSONFields(*Config) map[string]interface{} { return nil }
func (t validationTool) UsesStreamOutput() bool                         { return false }
func (t validationTool) RunLogFields(*Config) []string                  { return nil }

func TestSplitModelEffortFixedAndDynamicModels(t *testing.T) {
	efforts := []string{"none", "low", "medium", "high", "max"}
	fixed := validationTool{models: []string{"model"}, efforts: efforts}
	if base, effort := SplitModelEffort(fixed, "model-high"); base != "model" || effort != "high" {
		t.Fatalf("fixed split = (%q, %q), want (model, high)", base, effort)
	}

	dynamic := validationTool{efforts: efforts}
	for _, effort := range efforts {
		model := "runtime-model-" + effort
		if base, gotEffort := SplitModelEffort(dynamic, model); base != model || gotEffort != "" {
			t.Errorf("dynamic split %q = (%q, %q), want unchanged", model, base, gotEffort)
		}
	}
}
