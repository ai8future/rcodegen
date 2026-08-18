package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/tools/claude"
	"rcodegen/pkg/tools/codex"
)

func claudeCodexFactories() map[string]server.ToolFactory {
	return map[string]server.ToolFactory{
		"claude": func() runner.Tool { return claude.New() },
		"codex":  func() runner.Tool { return codex.New() },
	}
}

func TestBuildModelList_EnumeratesToolModels(t *testing.T) {
	ml := BuildModelList([]string{"claude", "codex"}, claudeCodexFactories())

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
	if got := ids["codex"].Efforts; len(got) != 4 || got[3] != "xhigh" {
		t.Errorf("codex efforts = %v, want [low medium high xhigh]", got)
	}

	// The guessing failure from the field: luna must not exist.
	if _, ok := ids["codex:luna"]; ok {
		t.Error("codex:luna must not be in the model list")
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
	if _, _, ok := h.splitToolEffort("codex-max"); ok {
		t.Error("codex-max should not resolve (max is claude-only)")
	}
	if _, _, ok := h.splitToolEffort("claude-banana"); ok {
		t.Error("claude-banana should not resolve")
	}
}

func TestChatCompletions_InvalidEffortSuffixRejected(t *testing.T) {
	h := NewHandler(nil, claudeCodexFactories(), server.NewRunRegistry(2), []string{"claude", "codex"}, nil, nil)

	// "max" is not a codex effort, so gpt-5.5-max is neither a model nor a
	// valid model+effort → 400 listing real options.
	body := `{"model":"codex:gpt-5.5-max","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
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
