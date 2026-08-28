package orchestrator

import (
	"testing"

	"rcodegen/pkg/settings"
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

func toolKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
