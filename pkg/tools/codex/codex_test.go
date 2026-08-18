package codex

import (
	"testing"

	"rcodegen/pkg/runner"
)

func TestValidEffortsForModel(t *testing.T) {
	tool := New()
	tests := []struct {
		model       string
		wantLast    string
		wantEfforts int
	}{
		{model: "gpt-5.6-sol", wantLast: "ultra", wantEfforts: 6},
		{model: "gpt-5.6-terra", wantLast: "ultra", wantEfforts: 6},
		{model: "gpt-5.6-luna", wantLast: "max", wantEfforts: 5},
		{model: "gpt-5.5", wantLast: "xhigh", wantEfforts: 4},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			efforts := tool.ValidEffortsForModel(test.model)
			if len(efforts) != test.wantEfforts || efforts[len(efforts)-1] != test.wantLast {
				t.Fatalf("efforts = %v, want %d ending in %q", efforts, test.wantEfforts, test.wantLast)
			}
		})
	}
}

func TestValidateConfig_ModelSpecificEffort(t *testing.T) {
	tool := New()
	tests := []struct {
		name    string
		model   string
		effort  string
		wantErr bool
	}{
		{name: "sol ultra", model: "gpt-5.6-sol", effort: "ultra"},
		{name: "terra max", model: "gpt-5.6-terra", effort: "max"},
		{name: "luna max", model: "gpt-5.6-luna", effort: "max"},
		{name: "luna ultra", model: "gpt-5.6-luna", effort: "ultra", wantErr: true},
		{name: "legacy max", model: "gpt-5.5", effort: "max", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := tool.ValidateConfig(&runner.Config{Model: test.model, Effort: test.effort})
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateConfig() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
