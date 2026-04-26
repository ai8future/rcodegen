package opencode

import (
	"path/filepath"
	"reflect"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

func TestTool_Identity(t *testing.T) {
	tool := New()
	if tool.Name() != "ropencode" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "ropencode")
	}
	if tool.BinaryName() != "opencode" {
		t.Errorf("BinaryName() = %q, want %q", tool.BinaryName(), "opencode")
	}
	if tool.ReportPrefix() != "opencode-" {
		t.Errorf("ReportPrefix() = %q, want %q", tool.ReportPrefix(), "opencode-")
	}
	if tool.ReportDir() != "_rcodegen" {
		t.Errorf("ReportDir() = %q, want %q", tool.ReportDir(), "_rcodegen")
	}
}

func TestTool_BuildCommand_BasicFlags(t *testing.T) {
	tool := New()
	cfg := &runner.Config{
		Model: settings.DefaultOpenCodeModel,
	}
	cmd := tool.BuildCommand(cfg, "/tmp/work", "do a thing")

	if filepath.Base(cmd.Path) != "opencode" {
		t.Errorf("Path = %q, want executable %q", cmd.Path, "opencode")
	}

	want := []string{
		"opencode",
		"run",
		"--dangerously-skip-permissions",
		"--format", "json",
		"-m", settings.DefaultOpenCodeModel,
		"--dir", "/tmp/work",
		"do a thing",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("Args = %#v, want %#v", cmd.Args, want)
	}
}

func TestTool_BuildCommand_WithSession(t *testing.T) {
	tool := New()
	cfg := &runner.Config{
		Model:     settings.DefaultOpenCodeModel,
		SessionID: "sess-abc",
	}
	cmd := tool.BuildCommand(cfg, "/tmp/work", "continue")

	want := []string{
		"opencode",
		"run",
		"--dangerously-skip-permissions",
		"--format", "json",
		"-m", settings.DefaultOpenCodeModel,
		"--dir", "/tmp/work",
		"--session", "sess-abc",
		"continue",
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("Args = %#v, want %#v", cmd.Args, want)
	}
}

func TestTool_ApplyToolDefaults_UsesSettings(t *testing.T) {
	tool := New()
	tool.SetSettings(&settings.Settings{
		Defaults: settings.Defaults{
			OpenCode: settings.OpenCodeDefaults{
				Model: "deepinfra/test/model",
			},
		},
	})

	cfg := &runner.Config{Model: settings.DefaultOpenCodeModel}
	tool.ApplyToolDefaults(cfg)
	if cfg.Model != "deepinfra/test/model" {
		t.Errorf("Model = %q, want settings default", cfg.Model)
	}
}

func TestTool_ImplementsRunnerToolInterface(t *testing.T) {
	var _ runner.Tool = (*Tool)(nil)
}
