package kilocode

import (
	"path/filepath"
	"reflect"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

func TestTool_Identity(t *testing.T) {
	tool := New()
	if tool.Name() != "rkilo" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "rkilo")
	}
	if tool.BinaryName() != "kilocode" {
		t.Errorf("BinaryName() = %q, want %q", tool.BinaryName(), "kilocode")
	}
	if tool.ReportPrefix() != "kilocode-" {
		t.Errorf("ReportPrefix() = %q, want %q", tool.ReportPrefix(), "kilocode-")
	}
	if tool.ReportDir() != "_rcodegen" {
		t.Errorf("ReportDir() = %q, want %q", tool.ReportDir(), "_rcodegen")
	}
}

func TestTool_BuildCommand_BasicFlags(t *testing.T) {
	tool := New()
	cfg := &runner.Config{
		Model: settings.DefaultKiloCodeModel,
	}
	cmd := tool.BuildCommand(cfg, "/tmp/work", "do a thing")

	if filepath.Base(cmd.Path) != "kilocode" {
		t.Errorf("Path = %q, want executable %q", cmd.Path, "kilocode")
	}

	want := []string{
		"kilocode",
		"run",
		"--dangerously-skip-permissions",
		"--format", "json",
		"-m", settings.DefaultKiloCodeModel,
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
		Model:     settings.DefaultKiloCodeModel,
		SessionID: "sess-abc",
	}
	cmd := tool.BuildCommand(cfg, "/tmp/work", "continue")

	want := []string{
		"kilocode",
		"run",
		"--dangerously-skip-permissions",
		"--format", "json",
		"-m", settings.DefaultKiloCodeModel,
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
			KiloCode: settings.KiloCodeDefaults{
				Model: "deepinfra/test/model",
			},
		},
	})

	cfg := &runner.Config{Model: settings.DefaultKiloCodeModel}
	tool.ApplyToolDefaults(cfg)
	if cfg.Model != "deepinfra/test/model" {
		t.Errorf("Model = %q, want settings default", cfg.Model)
	}
}

func TestTool_ImplementsRunnerToolInterface(t *testing.T) {
	var _ runner.Tool = (*Tool)(nil)
}
