package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
	"rcodegen/pkg/tools/kilocode"
	"rcodegen/pkg/tools/opencode"
)

func claudeCodexFactories() map[string]server.ToolFactory {
	return map[string]server.ToolFactory{
		"claude": func() runner.Tool { return claude.New() },
		"codex":  func() runner.Tool { return codex.New() },
	}
}

func TestBuildModelList_EnumeratesToolModels(t *testing.T) {
	ml := BuildModelList(context.Background(), []string{"claude", "codex"}, claudeCodexFactories(), nil)

	ids := make(map[string]ModelInfo, len(ml.Data))
	for _, m := range ml.Data {
		ids[m.ID] = m
	}

	// Bare tool entries plus every tool:model combination.
	for _, want := range []string{"claude", "codex", "claude:opus", "claude:fable", "claude:sonnet", "claude:haiku",
		"codex:gpt-5.6-sol", "codex:gpt-5.6-terra", "codex:gpt-5.6-luna", "codex:gpt-5.5"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("model list missing %q; got %d entries", want, len(ml.Data))
		}
	}

	// Defaults flagged.
	if !ids["claude:opus"].Default {
		t.Error("claude:opus should be flagged default")
	}
	if ids["claude:haiku"].Default {
		t.Error("claude:haiku should not be flagged default")
	}
	if !ids["codex:gpt-5.6-sol"].Default {
		t.Error("codex:gpt-5.6-sol should be flagged default")
	}
	if ids["codex:gpt-5.5"].Default {
		t.Error("codex:gpt-5.5 should no longer be flagged default")
	}

	// Valid effort suffixes surfaced on bare tool entries.
	if got := ids["claude"].Efforts; len(got) != 5 || got[4] != "max" {
		t.Errorf("claude efforts = %v, want [low medium high xhigh max]", got)
	}
	if got := ids["codex"].Efforts; len(got) != 6 || got[4] != "max" || got[5] != "ultra" {
		t.Errorf("codex efforts = %v, want [low medium high xhigh max ultra]", got)
	}
	if got := ids["codex:gpt-5.6-sol"].Efforts; len(got) != 6 || got[5] != "ultra" {
		t.Errorf("sol efforts = %v, want max and ultra support", got)
	}
	if got := ids["codex:gpt-5.6-luna"].Efforts; len(got) != 5 || got[4] != "max" {
		t.Errorf("luna efforts = %v, want max but not ultra", got)
	}
	if got := ids["codex:gpt-5.5"].Efforts; len(got) != 4 || got[3] != "xhigh" {
		t.Errorf("gpt-5.5 efforts = %v, want through xhigh only", got)
	}

	// The guessing failure from the field: luna must not exist.
	if _, ok := ids["codex:luna"]; ok {
		t.Error("codex:luna must not be in the model list")
	}
}

func TestBuildModelList_DynamicToolIncludesConfiguredDefault(t *testing.T) {
	factories := map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}
	s := settings.GetDefaultSettings()
	s.Defaults.OpenCode.Model = "custom/provider-model"
	ml := BuildModelList(context.Background(), []string{"opencode"}, factories, s)

	want := "opencode:custom/provider-model"
	found := false
	for _, model := range ml.Data {
		if model.ID == "opencode" && !model.Dynamic {
			t.Error("bare opencode entry should advertise a dynamic namespace")
		}
		if model.ID == want {
			found = true
			if !model.Default {
				t.Errorf("dynamic default %q should be flagged default", want)
			}
		}
	}
	if !found {
		t.Errorf("model list missing dynamic default %q", want)
	}
}

func TestValidateModel_DynamicNamespacesAccepted(t *testing.T) {
	for _, tool := range []runner.Tool{opencode.New(), kilocode.New()} {
		if err := runner.ValidateModel(tool, "custom/provider-model"); err != nil {
			t.Errorf("ValidateModel(%s, dynamic model) = %v, want nil", tool.Name(), err)
		}
	}
}

func TestHandleModels_WithFactories(t *testing.T) {
	h := NewHandler(nil, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var ml ModelList
	if err := json.NewDecoder(rec.Body).Decode(&ml); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 2 bare tools + 4 claude models + 9 codex models.
	if len(ml.Data) != 15 {
		t.Errorf("expected 15 entries, got %d", len(ml.Data))
	}
}

func TestHandleModels_UsesConfiguredDefault(t *testing.T) {
	s := settings.GetDefaultSettings()
	s.Defaults.Codex.Model = "gpt-5.5"
	h := NewHandler(s, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var ml ModelList
	if err := json.NewDecoder(rec.Body).Decode(&ml); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make(map[string]ModelInfo, len(ml.Data))
	for _, model := range ml.Data {
		ids[model.ID] = model
	}
	if !ids["claude:sonnet"].Default {
		t.Error("configured default claude:sonnet should be flagged default")
	}
	if ids["claude:opus"].Default {
		t.Error("compiled default claude:opus must not override configured default")
	}
	if !ids["codex:gpt-5.5"].Default || ids["codex:gpt-5.6-sol"].Default {
		t.Error("configured default codex:gpt-5.5 should replace compiled default gpt-5.6-sol")
	}
	if got := ids["codex"].Efforts; len(got) != 4 || got[3] != "xhigh" {
		t.Errorf("bare codex efforts = %v, want configured gpt-5.5 efforts through xhigh", got)
	}
}

func TestChatCompletions_InvalidModelRejected(t *testing.T) {
	h := NewHandler(nil, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	body := `{"model":"codex:luna","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid model, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Valid options") || !strings.Contains(rec.Body.String(), "gpt-5.5") {
		t.Errorf("error should list valid options, got: %s", rec.Body.String())
	}
}

func TestSplitModelEffort(t *testing.T) {
	claudeTool := claude.New()
	codexTool := codex.New()
	cases := []struct {
		tool         runner.Tool
		in, base, ef string
	}{
		{claudeTool, "opus-max", "opus", "max"},
		{claudeTool, "opus", "opus", ""},
		{claudeTool, "fable-xhigh", "fable", "xhigh"},
		{codexTool, "gpt-5.6-luna-high", "gpt-5.6-luna", "high"},
		{codexTool, "gpt-5.6-sol-ultra", "gpt-5.6-sol", "ultra"},
		{codexTool, "gpt-5.6-terra-ultra", "gpt-5.6-terra", "ultra"},
		{codexTool, "gpt-5.6-luna-max", "gpt-5.6-luna", "max"},
		{codexTool, "gpt-5.6-luna-ultra", "gpt-5.6-luna-ultra", ""},
		{codexTool, "gpt-5.6-luna", "gpt-5.6-luna", ""}, // hyphenated model name untouched
		{codexTool, "gpt-5.5-xhigh", "gpt-5.5", "xhigh"},
		{codexTool, "gpt-5.5-max", "gpt-5.5-max", ""}, // max invalid for codex → left alone
	}
	for _, c := range cases {
		base, ef := runner.SplitModelEffort(c.tool, c.in)
		if base != c.base || ef != c.ef {
			t.Errorf("SplitModelEffort(%q) = (%q, %q), want (%q, %q)", c.in, base, ef, c.base, c.ef)
		}
	}
}

func TestSplitToolEffort_BareToolNames(t *testing.T) {
	h := NewHandler(nil, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	if tool, ef, ok := h.splitToolEffort("claude-max"); !ok || tool != "claude" || ef != "max" {
		t.Errorf("claude-max → (%q, %q, %v), want (claude, max, true)", tool, ef, ok)
	}
	if tool, ef, ok := h.splitToolEffort("codex-high"); !ok || tool != "codex" || ef != "high" {
		t.Errorf("codex-high → (%q, %q, %v), want (codex, high, true)", tool, ef, ok)
	}
	if tool, ef, ok := h.splitToolEffort("codex-max"); !ok || tool != "codex" || ef != "max" {
		t.Errorf("codex-max → (%q, %q, %v), want (codex, max, true)", tool, ef, ok)
	}
	if tool, ef, ok := h.splitToolEffort("codex-ultra"); !ok || tool != "codex" || ef != "ultra" {
		t.Errorf("codex-ultra → (%q, %q, %v), want (codex, ultra, true)", tool, ef, ok)
	}
	if _, _, ok := h.splitToolEffort("claude-banana"); ok {
		t.Error("claude-banana should not resolve")
	}
}

func TestSplitToolEffort_UsesConfiguredDefaultModel(t *testing.T) {
	s := settings.GetDefaultSettings()
	s.Defaults.Codex.Model = "gpt-5.5"
	h := NewHandler(s, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	if _, _, ok := h.splitToolEffort("codex-max"); ok {
		t.Error("codex-max should not resolve when configured default gpt-5.5 stops at xhigh")
	}
	if tool, ef, ok := h.splitToolEffort("codex-xhigh"); !ok || tool != "codex" || ef != "xhigh" {
		t.Errorf("codex-xhigh → (%q, %q, %v), want (codex, xhigh, true)", tool, ef, ok)
	}
}

func TestChatCompletions_InvalidEffortSuffixRejected(t *testing.T) {
	h := NewHandler(nil, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	// "max" is not valid for gpt-5.5, so gpt-5.5-max is neither a model nor a
	// valid model+effort → 400 listing real options.
	body := `{"model":"codex:gpt-5.5-max","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestChatCompletions_ConfiguredEffortMustMatchModel(t *testing.T) {
	s := settings.GetDefaultSettings()
	s.Defaults.Codex.Model = "gpt-5.5"
	s.Defaults.Codex.Effort = "ultra"
	h := NewHandler(s, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	body := `{"model":"codex","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_effort") {
		t.Fatalf("expected invalid_effort response, got %s", rec.Body.String())
	}
}

func TestChatCompletions_ValidModelPassesValidation(t *testing.T) {
	// A valid model must get past validation. It will then block on registry
	// Acquire and actually run the tool — so instead of running, verify only
	// that validation itself accepts every advertised model.
	for _, tc := range []struct{ toolName, model string }{
		{"claude", "opus"}, {"claude", "fable"}, {"codex", "gpt-5.5"},
	} {
		factory := claudeCodexFactories()[tc.toolName]
		if err := runner.ValidateModel(factory(), tc.model); err != nil {
			t.Errorf("ValidateModel(%s, %s) = %v, want nil", tc.toolName, tc.model, err)
		}
	}
}
