package claude

import (
	"reflect"
	"sync"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

func TestCheckClaudeMax_ThreadSafe(t *testing.T) {
	tool := New()

	// Run concurrent checks - should not race
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tool.IsClaudeMax()
		}()
	}
	wg.Wait()

	// If we get here without -race detecting issues, test passes
}

func TestNew_ReturnsNonNil(t *testing.T) {
	tool := New()
	if tool == nil {
		t.Error("New() returned nil")
	}
}

func TestTool_BuildCommand_IncludesEffort(t *testing.T) {
	tool := New()
	cfg := &runner.Config{
		Model:      "sonnet",
		MaxBudget:  "10.00",
		Effort:     "xhigh",
		OutputJSON: true,
	}
	cmd := tool.BuildCommand(cfg, "/tmp/work", "do a thing")

	want := []string{
		"claude",
		"-p", "do a thing",
		"--dangerously-skip-permissions",
		"--model", "sonnet",
		"--max-budget-usd", "10.00",
		"--effort", "xhigh",
		"--output-format", "json",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("Args = %#v, want %#v", cmd.Args, want)
	}
	if cmd.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want %q", cmd.Dir, "/tmp/work")
	}
}

func TestTool_ApplyToolDefaults_DefaultsEffortXhigh(t *testing.T) {
	tool := New()
	cfg := &runner.Config{Model: "sonnet"}

	tool.ApplyToolDefaults(cfg)

	if cfg.Effort != settings.DefaultClaudeEffort {
		t.Errorf("Effort = %q, want %q", cfg.Effort, settings.DefaultClaudeEffort)
	}
}

func TestTool_ApplyToolDefaults_UsesSettingsEffort(t *testing.T) {
	tool := New()
	tool.SetSettings(&settings.Settings{
		Defaults: settings.Defaults{
			Claude: settings.ClaudeDefaults{
				Model:  "sonnet",
				Budget: "25.00",
				Effort: "high",
			},
		},
	})
	cfg := &runner.Config{}

	tool.ApplyToolDefaults(cfg)

	if cfg.Model != "sonnet" {
		t.Errorf("Model = %q, want %q", cfg.Model, "sonnet")
	}
	if cfg.MaxBudget != "25.00" {
		t.Errorf("MaxBudget = %q, want %q", cfg.MaxBudget, "25.00")
	}
	if cfg.Effort != "high" {
		t.Errorf("Effort = %q, want %q", cfg.Effort, "high")
	}
}

func TestTool_ValidateConfig_Effort(t *testing.T) {
	tool := New()

	valid := &runner.Config{Model: "sonnet", MaxBudget: "10.00", Effort: "max"}
	if err := tool.ValidateConfig(valid); err != nil {
		t.Fatalf("ValidateConfig valid effort err: %v", err)
	}

	invalid := &runner.Config{Model: "sonnet", MaxBudget: "10.00", Effort: "extreme"}
	if err := tool.ValidateConfig(invalid); err == nil {
		t.Fatalf("ValidateConfig expected invalid effort error")
	}
}
