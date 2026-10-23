package orchestrator

import (
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/workspace"
)

func TestNew_RegistersOpencodeTool(t *testing.T) {
	o := New(&settings.Settings{})
	if _, ok := o.tools["opencode"]; !ok {
		t.Fatalf("expected opencode in orchestrator tools, got keys: %v", toolKeys(o.tools))
	}
}

func TestNew_RegistersKilocodeTool(t *testing.T) {
	o := New(&settings.Settings{})
	if _, ok := o.tools["kilocode"]; !ok {
		t.Fatalf("expected kilocode in orchestrator tools, got keys: %v", toolKeys(o.tools))
	}
}

func TestNewRegistersLocalRuntimeTools(t *testing.T) {
	o := New(settings.GetDefaultSettings())
	for _, name := range []string{"ollama", "lmstudio"} {
		if _, ok := o.tools[name]; !ok {
			t.Fatalf("expected %s in orchestrator tools, got keys: %v", name, toolKeys(o.tools))
		}
	}
}

func TestGetStepModel_Jev(t *testing.T) {
	old := DispatcherFactory
	DispatcherFactory = func(tools map[string]runner.Tool) StepExecutor {
		return stubFuncExecutor(func(step *bundle.Step) (*envelope.Envelope, error) {
			return envelope.New().Success().Build(), nil
		})
	}
	defer func() { DispatcherFactory = old }()

	s := settings.GetDefaultSettings()
	s.Defaults.Jev.Model = "jev-1.13.0"
	o := New(s)
	if got := o.getStepModel("jev", ""); got != "jev-1.13.0" {
		t.Fatalf("default jev model = %q", got)
	}
	if got := o.getStepModel("jev", "jev-latest"); got != "jev-latest" {
		t.Fatalf("step override = %q", got)
	}
	if got := New(&settings.Settings{}).getStepModel("jev", ""); got != "" {
		t.Fatalf("empty settings should yield empty model, got %q", got)
	}
}

func TestNew_HandsSettingsToDispatcher(t *testing.T) {
	old := DispatcherFactory
	var received *settings.Settings
	DispatcherFactory = func(tools map[string]runner.Tool) StepExecutor {
		return &settingsRecorder{onSet: func(s *settings.Settings) { received = s }}
	}
	defer func() { DispatcherFactory = old }()
	s := settings.GetDefaultSettings()
	New(s)
	if received != s {
		t.Fatal("dispatcher did not receive settings")
	}
}

type settingsRecorder struct{ onSet func(*settings.Settings) }

func (r *settingsRecorder) SetSettings(s *settings.Settings) { r.onSet(s) }
func (r *settingsRecorder) Execute(*bundle.Step, *Context, *workspace.Workspace) (*envelope.Envelope, error) {
	return envelope.New().Success().Build(), nil
}

func toolKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
