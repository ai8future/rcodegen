# First-Class Ollama and LM Studio Support — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `ollama` and `lmstudio` as first-class rserve/rbatch/bundle tools that call local OpenAI-compatible endpoints directly (no CLI subprocess), with live model discovery in `/v1/models`.

**Architecture:** One new package `pkg/tools/localai` implements `runner.Tool` + `runner.DirectAPIRunner` (the same hook Gemini image models use, `pkg/runner/runner.go:578`), parameterized by a two-value `Flavor`. Registered server-side only (rserve, rbatch, orchestrator) — no standalone binaries, matching the `rkilo`/`ropencode` precedent. A new optional `runner.DynamicModelLister` interface lets `/v1/models` enumerate what the local runtime actually has loaded.

**Tech Stack:** Go stdlib only (`net/http`, `net/url`, `net`, `encoding/json`, `httptest` for tests). No new dependencies, no vendor changes.

**Scope:** Phases 0 + 1 of the study (`_studies/first-class-ollama-and-lm-studio-support-in-rcodegen.md`). Phase 2 (effort mapping via `reasoning_effort`, SSE streaming passthrough, `/api/v1` metadata) gets its own follow-up plan after this ships.

**Commit protocol (overrides per-task commits):** Per repo rules, code commits happen ONLY after VERSION increment + CHANGELOG annotation. So: implement all tasks, then Task 10 does VERSION/CHANGELOG/build/test/commit/push once. Do not commit per task.

**Dialect constraints (verified Aug 2026, see study §2.1):**
- Never send `tool_choice`, `logit_bias`, `n`, `user`, or logprobs fields to Ollama's `/v1` — they are unsupported.
- Ollama silently truncates prompts exceeding the model context; context length is not settable via `/v1`.
- LM Studio JIT-loads models on first request — first call can block for a model load. The per-request timeout must budget for this (default 600s).
- Local model listing: Ollama `GET /api/tags`, LM Studio `GET /v1/models`.

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `pkg/settings/settings.go` | Modify | `LocalAIDefaults` struct, `Ollama`/`LMStudio` fields on `Defaults`, constants, fallback fill, env overrides |
| `pkg/settings/settings_test.go` | Modify | defaults-fill test |
| `pkg/runner/tool.go` | Modify | add `DynamicModelLister` optional interface |
| `pkg/tools/localai/localai.go` | Create | `Tool` struct, `Flavor`, all ~25 `runner.Tool` methods |
| `pkg/tools/localai/api.go` | Create | `DirectAPIRunner` impl: chat call, base-URL policy, usage capture |
| `pkg/tools/localai/models.go` | Create | `ListModelsLive` for both flavors |
| `pkg/tools/localai/localai_test.go` | Create | interface compliance, defaults, base-URL policy |
| `pkg/tools/localai/api_test.go` | Create | chat happy path, error paths, cancellation, usage |
| `pkg/tools/localai/models_test.go` | Create | live listing both flavors |
| `pkg/server/openai/models.go` | Modify | `DetectAvailableTools` empty-BinaryName rule; `BuildModelList` live merge (+ctx) |
| `pkg/server/openai/types.go` | Modify | `Live bool` on `ModelInfo` |
| `pkg/server/openai/handler.go` | Modify | pass `r.Context()` to `BuildModelList` |
| `pkg/server/openai/models_list_test.go` | Modify | live-merge + detection tests |
| `cmd/rserve/main.go` | Modify | register two factories |
| `pkg/batch/executor_local.go` | Modify | register two factories |
| `pkg/orchestrator/orchestrator.go` | Modify | register two factories |
| `README.md`, `settings.json.example` | Modify | Phase 0 docs: native tools + opencode local-provider recipe |
| `VERSION`, `CHANGELOG.md` | Modify | Task 10 only, read VERSION at the last second |

---

### Task 1: Settings — `LocalAIDefaults`

**Files:**
- Modify: `pkg/settings/settings.go`
- Test: `pkg/settings/settings_test.go`

- [ ] **Step 1: Write the failing test** (append to `pkg/settings/settings_test.go`)

```go
func TestLocalAIDefaultsFill(t *testing.T) {
	s := GetDefaultSettings()
	if s.Defaults.Ollama.BaseURL != DefaultOllamaBaseURL {
		t.Errorf("ollama base_url = %q, want %q", s.Defaults.Ollama.BaseURL, DefaultOllamaBaseURL)
	}
	if s.Defaults.Ollama.TimeoutSeconds != DefaultLocalAITimeoutSeconds {
		t.Errorf("ollama timeout = %d, want %d", s.Defaults.Ollama.TimeoutSeconds, DefaultLocalAITimeoutSeconds)
	}
	if s.Defaults.LMStudio.BaseURL != DefaultLMStudioBaseURL {
		t.Errorf("lmstudio base_url = %q, want %q", s.Defaults.LMStudio.BaseURL, DefaultLMStudioBaseURL)
	}
	if s.Defaults.Ollama.Model != DefaultOllamaModel || s.Defaults.LMStudio.Model != DefaultLMStudioModel {
		t.Errorf("unexpected default models: %q / %q", s.Defaults.Ollama.Model, s.Defaults.LMStudio.Model)
	}
}
```

- [ ] **Step 2: Run it to verify failure**

Run: `go test ./pkg/settings/ -run TestLocalAIDefaultsFill -v`
Expected: FAIL (compile error: undefined `DefaultOllamaBaseURL` etc.)

- [ ] **Step 3: Implement**

In `pkg/settings/settings.go` add to the `const` block (after `DefaultKiloCodeProvider`):

```go
	DefaultOllamaBaseURL         = "http://localhost:11434"
	DefaultOllamaModel           = "qwen3:14b"
	DefaultLMStudioBaseURL       = "http://localhost:1234"
	DefaultLMStudioModel         = "qwen/qwen3-14b"
	DefaultLocalAITimeoutSeconds = 600 // local models can block on JIT model loads
```

After `KiloCodeDefaults` add:

```go
// LocalAIDefaults holds settings for a local OpenAI-compatible runtime
// (Ollama or LM Studio). BaseURL is operator-configured, never per-request.
type LocalAIDefaults struct {
	BaseURL        string `json:"base_url,omitempty"`        // e.g. "http://localhost:11434"
	Model          string `json:"model,omitempty"`           // runtime's own model naming
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"` // whole-request cap incl. JIT load
	AllowRemote    bool   `json:"allow_remote,omitempty"`    // permit non-loopback/non-private hosts
}
```

Extend `Defaults`:

```go
	OpenCode OpenCodeDefaults `json:"opencode,omitempty"`
	KiloCode KiloCodeDefaults `json:"kilocode,omitempty"`
	Ollama   LocalAIDefaults  `json:"ollama,omitempty"`
	LMStudio LocalAIDefaults  `json:"lmstudio,omitempty"`
```

In `GetDefaultSettings()` add to the `Defaults` literal:

```go
			Ollama: LocalAIDefaults{
				BaseURL:        DefaultOllamaBaseURL,
				Model:          DefaultOllamaModel,
				TimeoutSeconds: DefaultLocalAITimeoutSeconds,
			},
			LMStudio: LocalAIDefaults{
				BaseURL:        DefaultLMStudioBaseURL,
				Model:          DefaultLMStudioModel,
				TimeoutSeconds: DefaultLocalAITimeoutSeconds,
			},
```

In `LoadWithFallback()` add (alongside the other fills):

```go
	if settings.Defaults.Ollama.BaseURL == "" {
		settings.Defaults.Ollama.BaseURL = DefaultOllamaBaseURL
	}
	if settings.Defaults.Ollama.Model == "" {
		settings.Defaults.Ollama.Model = DefaultOllamaModel
	}
	if settings.Defaults.Ollama.TimeoutSeconds == 0 {
		settings.Defaults.Ollama.TimeoutSeconds = DefaultLocalAITimeoutSeconds
	}
	if settings.Defaults.LMStudio.BaseURL == "" {
		settings.Defaults.LMStudio.BaseURL = DefaultLMStudioBaseURL
	}
	if settings.Defaults.LMStudio.Model == "" {
		settings.Defaults.LMStudio.Model = DefaultLMStudioModel
	}
	if settings.Defaults.LMStudio.TimeoutSeconds == 0 {
		settings.Defaults.LMStudio.TimeoutSeconds = DefaultLocalAITimeoutSeconds
	}
```

In `EnvOverrides` add:

```go
	OllamaBaseURL   string `env:"RCODEGEN_OLLAMA_BASE_URL" required:"false"`
	LMStudioBaseURL string `env:"RCODEGEN_LMSTUDIO_BASE_URL" required:"false"`
```

And in `applyEnvOverrides`:

```go
	if env.OllamaBaseURL != "" {
		s.Defaults.Ollama.BaseURL = env.OllamaBaseURL
	}
	if env.LMStudioBaseURL != "" {
		s.Defaults.LMStudio.BaseURL = env.LMStudioBaseURL
	}
```

Note: do NOT add ollama/lmstudio to the `RCODEGEN_MODEL` fan-out in `applyEnvOverrides` — that variable holds cloud-tool names and would poison local model names.

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/settings/ -v`
Expected: PASS (all, including existing tests)

---

### Task 2: `runner.DynamicModelLister` interface

**Files:**
- Modify: `pkg/runner/tool.go`

- [ ] **Step 1: Add the interface** (after `ModelEffortProvider`, ~line 119)

```go
// DynamicModelLister is an optional interface for tools whose model list is
// knowable only by asking a live backend (local runtimes like Ollama and
// LM Studio). Callers must treat an error as "backend unreachable" and
// degrade gracefully — never fail an endpoint because a local runtime is off.
type DynamicModelLister interface {
	ListModelsLive(ctx context.Context) ([]string, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./pkg/runner/`
Expected: no output (success). `context` is already imported in tool.go.

---

### Task 3: `pkg/tools/localai` — Tool skeleton

**Files:**
- Create: `pkg/tools/localai/localai.go`
- Test: `pkg/tools/localai/localai_test.go`

- [ ] **Step 1: Write the failing test**

```go
package localai

import (
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

// Compile-time interface compliance is asserted in localai.go via var _.

func TestFlavorIdentity(t *testing.T) {
	o, l := NewOllama(), NewLMStudio()
	if o.Name() != "rollama" || l.Name() != "rlmstudio" {
		t.Errorf("names: %q, %q", o.Name(), l.Name())
	}
	if o.BinaryName() != "" || l.BinaryName() != "" {
		t.Error("localai tools must report no CLI binary")
	}
	if o.ReportPrefix() != "ollama-" || l.ReportPrefix() != "lmstudio-" {
		t.Errorf("prefixes: %q, %q", o.ReportPrefix(), l.ReportPrefix())
	}
}

func TestDefaultsFromSettings(t *testing.T) {
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.Model = "llama3.1:8b"
	o := NewOllama()
	o.SetSettings(s)
	if got := o.DefaultModelSetting(); got != "llama3.1:8b" {
		t.Errorf("DefaultModelSetting = %q", got)
	}
	cfg := runner.NewConfig()
	o.ApplyToolDefaults(cfg)
	if cfg.Model != "llama3.1:8b" {
		t.Errorf("ApplyToolDefaults model = %q", cfg.Model)
	}
}

func TestValidateConfigRequiresModel(t *testing.T) {
	o := NewOllama()
	cfg := runner.NewConfig()
	cfg.Model = ""
	if err := o.ValidateConfig(cfg); err == nil {
		t.Error("expected error for empty model")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/tools/localai/ -v`
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement `localai.go`**

```go
// Package localai implements runner.Tool for local OpenAI-compatible model
// runtimes (Ollama, LM Studio). Unlike every other tool package it spawns no
// CLI: it is a pure DirectAPIRunner, calling the runtime's HTTP endpoint.
package localai

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

var (
	_ runner.Tool               = (*Tool)(nil)
	_ runner.DirectAPIRunner    = (*Tool)(nil)
	_ runner.UsageReporter      = (*Tool)(nil)
	_ runner.DynamicModelLister = (*Tool)(nil)
	_ runner.SettingsAware      = (*Tool)(nil)
)

// Flavor selects which local runtime this Tool instance talks to.
type Flavor int

const (
	FlavorOllama Flavor = iota
	FlavorLMStudio
)

// Tool implements runner.Tool for a local OpenAI-compatible runtime.
type Tool struct {
	settings *settings.Settings
	flavor   Flavor
}

// NewOllama creates a tool targeting an Ollama server.
func NewOllama() *Tool { return &Tool{flavor: FlavorOllama} }

// NewLMStudio creates a tool targeting an LM Studio server.
func NewLMStudio() *Tool { return &Tool{flavor: FlavorLMStudio} }

// SetSettings sets loaded settings on the tool.
func (t *Tool) SetSettings(s *settings.Settings) { t.settings = s }

// defaults returns this flavor's settings block (zero value when unset).
func (t *Tool) defaults() settings.LocalAIDefaults {
	if t.settings == nil {
		return settings.LocalAIDefaults{}
	}
	if t.flavor == FlavorOllama {
		return t.settings.Defaults.Ollama
	}
	return t.settings.Defaults.LMStudio
}

// flavorName is the short machine name used in messages and prefixes.
func (t *Tool) flavorName() string {
	if t.flavor == FlavorOllama {
		return "ollama"
	}
	return "lmstudio"
}

// displayName is the human-facing runtime name. (Do not use strings.Title —
// it is deprecated; see pkg/orchestrator/progress.go:29 for the same rule.)
func (t *Tool) displayName() string {
	if t.flavor == FlavorOllama {
		return "Ollama"
	}
	return "LM Studio"
}

// baseURL resolves the endpoint: settings > OLLAMA_HOST (ollama only) > default.
func (t *Tool) baseURL() string {
	if d := t.defaults(); d.BaseURL != "" {
		return strings.TrimRight(d.BaseURL, "/")
	}
	if t.flavor == FlavorOllama {
		if h := os.Getenv("OLLAMA_HOST"); h != "" {
			if !strings.Contains(h, "://") {
				h = "http://" + h
			}
			return strings.TrimRight(h, "/")
		}
		return settings.DefaultOllamaBaseURL
	}
	return settings.DefaultLMStudioBaseURL
}

func (t *Tool) timeoutSeconds() int {
	if d := t.defaults(); d.TimeoutSeconds > 0 {
		return d.TimeoutSeconds
	}
	return settings.DefaultLocalAITimeoutSeconds
}

func (t *Tool) Name() string { return "r" + t.flavorName() }

// BinaryName is empty: there is no CLI. DetectAvailableTools treats an empty
// BinaryName as "API-based, always available"; reachability surfaces at
// request time and in live model listing instead.
func (t *Tool) BinaryName() string { return "" }

func (t *Tool) ReportDir() string    { return "_rcodegen" }
func (t *Tool) ReportPrefix() string { return t.flavorName() + "-" }

// ValidModels returns nil: the namespace is whatever the runtime has pulled.
func (t *Tool) ValidModels() []string { return nil }

// ValidEfforts returns nil in Phase 1. Phase 2 maps efforts onto the
// /v1 "reasoning_effort" field (see the study, §2.1).
func (t *Tool) ValidEfforts() []string { return nil }

func (t *Tool) DefaultModel() string {
	if t.flavor == FlavorOllama {
		return settings.DefaultOllamaModel
	}
	return settings.DefaultLMStudioModel
}

func (t *Tool) DefaultModelSetting() string {
	if d := t.defaults(); d.Model != "" {
		return d.Model
	}
	return t.DefaultModel()
}

// BuildCommand is never executed: ShouldUseDirectAPI always returns true and
// both runner dispatch sites check DirectAPIRunner before building a command
// (pkg/runner/runner.go:476,578). It exists only to satisfy runner.Tool.
func (t *Tool) BuildCommand(cfg *runner.Config, workDir, task string) *exec.Cmd {
	return exec.Command("false")
}

func (t *Tool) ShowStatus()                                  {}
func (t *Tool) SupportsStatusTracking() bool                 { return false }
func (t *Tool) CaptureStatusBefore() interface{}             { return nil }
func (t *Tool) CaptureStatusAfter() interface{}              { return nil }
func (t *Tool) PrintStatusSummary(before, after interface{}) {}
func (t *Tool) ToolSpecificFlags() []runner.FlagDef          { return nil }

func (t *Tool) ApplyToolDefaults(cfg *runner.Config) {
	if d := t.defaults(); d.Model != "" {
		cfg.Model = d.Model
		return
	}
	if cfg.Model == "" {
		cfg.Model = t.DefaultModel()
	}
}

func (t *Tool) PrepareForExecution(cfg *runner.Config) {}

func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	if cfg.Model == "" {
		return fmt.Errorf("model must be specified (e.g. %s)", t.DefaultModel())
	}
	return nil
}

func (t *Tool) BannerTitle() string {
	return strings.ToUpper(t.Name())
}

func (t *Tool) BannerSubtitle() string {
	return t.flavorName() + " (local OpenAI-compatible API)"
}

func (t *Tool) PrintToolSpecificBannerFields(cfg *runner.Config)  {}
func (t *Tool) PrintToolSpecificSummaryFields(cfg *runner.Config) {}

// SecurityWarning is nil: no subprocess is spawned and no permissions are
// skipped — the tool only POSTs a prompt to a local HTTP endpoint.
func (t *Tool) SecurityWarning() []string { return nil }

func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	return []runner.HelpSection{
		{
			Title: t.displayName() + " Options",
			Lines: []string{
				"  Chat-completion inference against a local runtime at " + t.baseURL() + ".",
				"  Models are whatever the runtime has installed; GET /v1/models on",
				"  rserve enumerates them live. No file editing or shell execution:",
				"  this tool generates text only.",
			},
		},
	}
}

func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{
		"model":    cfg.Model,
		"base_url": t.baseURL(),
	}
}

func (t *Tool) UsesStreamOutput() bool { return false }

func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	return []string{
		"Model: " + cfg.Model,
		"Endpoint: " + t.baseURL(),
	}
}
```

Note: the `var _ runner.DirectAPIRunner` / `UsageReporter` / `DynamicModelLister` assertions will not compile until Tasks 4–5 add those methods. If building this task standalone, comment those three assertions in and out — or simply implement Tasks 3–5 before the first `go test` run. (Recommended: treat Tasks 3–5 as one build unit; the tests are still separate.)

- [ ] **Step 4: Run the Task 3 tests** (after Tasks 4–5 if building as one unit)

Run: `go test ./pkg/tools/localai/ -run 'TestFlavorIdentity|TestDefaultsFromSettings|TestValidateConfigRequiresModel' -v`
Expected: PASS

---

### Task 4: DirectAPI chat call + base-URL policy

**Files:**
- Create: `pkg/tools/localai/api.go`
- Test: `pkg/tools/localai/api_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package localai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

// newTestTool points an Ollama-flavor tool at a fake backend.
func newTestTool(baseURL string) *Tool {
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = baseURL // httptest URLs are 127.0.0.1 — passes policy
	s.Defaults.Ollama.TimeoutSeconds = 5
	tool := NewOllama()
	tool.SetSettings(s)
	return tool
}

func TestRunDirectAPIHappyPath(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"content": "local hello"}},
			},
			"usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 7},
		})
	}))
	defer ts.Close()

	tool := newTestTool(ts.URL)
	cfg := runner.NewConfig()
	cfg.Model = "qwen3:14b"
	var out bytes.Buffer
	cfg.Output = &out

	code := tool.RunDirectAPI(context.Background(), cfg, "", "say hello")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(out.String(), "local hello") {
		t.Errorf("output missing content: %q", out.String())
	}
	if cfg.TokenUsage == nil || cfg.TokenUsage.InputTokens != 11 || cfg.TokenUsage.OutputTokens != 7 {
		t.Errorf("usage not captured: %+v", cfg.TokenUsage)
	}
	// Dialect guard: never send fields Ollama's /v1 rejects or ignores.
	for _, banned := range []string{"tool_choice", "logit_bias", "n", "user"} {
		if _, present := gotBody[banned]; present {
			t.Errorf("request contains unsupported field %q", banned)
		}
	}
}

func TestRunDirectAPIBackendError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
	}))
	defer ts.Close()

	tool := newTestTool(ts.URL)
	cfg := runner.NewConfig()
	cfg.Model = "nope"
	var errBuf bytes.Buffer
	cfg.Stderr = &errBuf
	if code := tool.RunDirectAPI(context.Background(), cfg, "", "x"); code == 0 {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(errBuf.String(), "model not found") {
		t.Errorf("stderr should surface backend message, got %q", errBuf.String())
	}
}

func TestRunDirectAPIUnreachable(t *testing.T) {
	tool := newTestTool("http://127.0.0.1:1") // nothing listens on port 1
	cfg := runner.NewConfig()
	cfg.Model = "m"
	cfg.Stderr = &bytes.Buffer{}
	if code := tool.RunDirectAPI(context.Background(), cfg, "", "x"); code == 0 {
		t.Fatal("expected non-zero exit for unreachable backend")
	}
}

func TestRunDirectAPICancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	tool := newTestTool(ts.URL)
	cfg := runner.NewConfig()
	cfg.Model = "m"
	cfg.Stderr = &bytes.Buffer{}
	if code := tool.RunDirectAPI(ctx, cfg, "", "x"); code == 0 {
		t.Fatal("expected non-zero exit for cancelled context")
	}
}

func TestCheckBaseURL(t *testing.T) {
	cases := []struct {
		url         string
		allowRemote bool
		wantErr     bool
	}{
		{"http://localhost:11434", false, false},
		{"http://127.0.0.1:1234", false, false},
		{"http://[::1]:11434", false, false},
		{"http://192.168.1.20:11434", false, false}, // RFC 1918 = LAN, allowed
		{"http://10.0.0.5:1234", false, false},
		{"http://0.0.0.0:11434", false, false},     // OLLAMA_HOST bind-address form
		{"http://8.8.8.8:11434", false, true},      // public IP blocked by default
		{"http://example.com:11434", false, true},  // DNS name blocked by default
		{"http://example.com:11434", true, false},  // explicit opt-in
		{"ftp://localhost:11434", false, true},     // scheme
		{"", false, true},
	}
	for _, c := range cases {
		err := checkBaseURL(c.url, c.allowRemote)
		if (err != nil) != c.wantErr {
			t.Errorf("checkBaseURL(%q, %v) err=%v, wantErr=%v", c.url, c.allowRemote, err, c.wantErr)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/tools/localai/ -run 'TestRunDirectAPI|TestCheckBaseURL' -v`
Expected: FAIL (undefined `RunDirectAPI`, `checkBaseURL`)

- [ ] **Step 3: Implement `api.go`**

```go
package localai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"rcodegen/pkg/runner"
)

// chatMessage is one OpenAI-dialect message.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the subset of the chat-completions request both runtimes
// support. Deliberately minimal: Ollama's /v1 rejects or ignores tool_choice,
// logit_bias, n, user, and logprobs — never add them here (study §2.1).
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// checkBaseURL enforces the outbound policy: http(s) only, and the host must
// be loopback or RFC 1918 private unless the operator set allow_remote. The
// base URL comes only from settings/env (operator-controlled, never from a
// request), so this is a guard against misconfiguration, not an auth boundary.
func checkBaseURL(raw string, allowRemote bool) error {
	if raw == "" {
		return fmt.Errorf("base URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid base URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL scheme must be http or https, got %q", u.Scheme)
	}
	if allowRemote {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("base URL host %q is a DNS name; set allow_remote: true to permit it", host)
	}
	// IsUnspecified covers 0.0.0.0/:: — common when OLLAMA_HOST holds the
	// server's *bind* address; connecting to it resolves locally, so allow it.
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return nil
	}
	return fmt.Errorf("base URL host %q is public; set allow_remote: true to permit it", host)
}

// ShouldUseDirectAPI always returns true: there is no CLI to fall back to.
func (t *Tool) ShouldUseDirectAPI(cfg *runner.Config) bool { return true }

// RunDirectAPI POSTs a chat completion to the local runtime, writes the
// response text to cfg.Output, and captures token usage on cfg. Cancelling
// ctx aborts the in-flight request. The timeout budget deliberately covers
// LM Studio's JIT model load, which blocks the first request to a model.
func (t *Tool) RunDirectAPI(ctx context.Context, cfg *runner.Config, workDir, task string) int {
	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}
	errw := io.Writer(os.Stderr)
	if cfg.Stderr != nil {
		errw = cfg.Stderr
	}

	base := t.baseURL()
	if err := checkBaseURL(base, t.defaults().AllowRemote); err != nil {
		fmt.Fprintf(errw, "%s: %v\n", t.flavorName(), err)
		return 1
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(t.timeoutSeconds())*time.Second)
	defer cancel()

	body, err := json.Marshal(chatRequest{
		Model:    cfg.Model,
		Messages: []chatMessage{{Role: "user", Content: task}},
	})
	if err != nil {
		fmt.Fprintf(errw, "%s: encoding request: %v\n", t.flavorName(), err)
		return 1
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(errw, "%s: building request: %v\n", t.flavorName(), err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(errw, "%s: request to %s failed: %v\n", t.flavorName(), base, err)
		fmt.Fprintf(errw, "%s: is the %s server running?\n", t.flavorName(), t.flavorName())
		return 1
	}
	defer resp.Body.Close()

	// Bound the read: a local runtime should never send unbounded output, but
	// a misbehaving one must not exhaust server memory (rserve holds a run
	// slot while this executes).
	const maxResponse = 32 << 20 // 32 MiB
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		fmt.Fprintf(errw, "%s: reading response: %v\n", t.flavorName(), err)
		return 1
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		fmt.Fprintf(errw, "%s: invalid JSON from backend (HTTP %d): %v\n", t.flavorName(), resp.StatusCode, err)
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		fmt.Fprintf(errw, "%s: backend returned HTTP %d: %s\n", t.flavorName(), resp.StatusCode, msg)
		return 1
	}
	if len(parsed.Choices) == 0 {
		fmt.Fprintf(errw, "%s: backend returned no choices\n", t.flavorName())
		return 1
	}

	fmt.Fprint(out, parsed.Choices[0].Message.Content)

	if parsed.Usage.PromptTokens > 0 || parsed.Usage.CompletionTokens > 0 {
		cfg.TokenUsage = &runner.TokenUsage{
			InputTokens:  parsed.Usage.PromptTokens,
			OutputTokens: parsed.Usage.CompletionTokens,
		}
	}
	return 0
}

// ReportedUsage reads the usage RunDirectAPI captured. CostUSD stays 0 with
// ok=true — local inference genuinely is free, the one case where $0.00 is
// a measurement rather than a fabrication.
func (t *Tool) ReportedUsage(res *runner.RunResult) (runner.RunUsage, bool) {
	if res == nil || res.TokenUsage == nil {
		return runner.RunUsage{}, false
	}
	return runner.RunUsage{
		InputTokens:  res.TokenUsage.InputTokens,
		OutputTokens: res.TokenUsage.OutputTokens,
	}, true
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/tools/localai/ -run 'TestRunDirectAPI|TestCheckBaseURL' -v`
Expected: PASS (all five)

---

### Task 5: Live model listing

**Files:**
- Create: `pkg/tools/localai/models.go`
- Test: `pkg/tools/localai/models_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package localai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"rcodegen/pkg/settings"
)

func TestListModelsLiveOllama(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]string{{"name": "llama3.1:8b"}, {"name": "qwen3:14b"}},
		})
	}))
	defer ts.Close()

	tool := newTestTool(ts.URL) // helper from api_test.go
	got, err := tool.ListModelsLive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"llama3.1:8b", "qwen3:14b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestListModelsLiveLMStudio(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{{"id": "qwen/qwen3-14b"}},
		})
	}))
	defer ts.Close()

	s := settings.GetDefaultSettings()
	s.Defaults.LMStudio.BaseURL = ts.URL
	tool := NewLMStudio()
	tool.SetSettings(s)

	got, err := tool.ListModelsLive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"qwen/qwen3-14b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestListModelsLiveUnreachable(t *testing.T) {
	tool := newTestTool("http://127.0.0.1:1")
	if _, err := tool.ListModelsLive(context.Background()); err == nil {
		t.Error("expected error for unreachable backend")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/tools/localai/ -run TestListModelsLive -v`
Expected: FAIL (undefined `ListModelsLive`)

- [ ] **Step 3: Implement `models.go`**

```go
package localai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ListModelsLive asks the runtime what models it has. Ollama exposes its
// installed set on the native /api/tags; LM Studio exposes its catalog on the
// OpenAI-compatible /v1/models. Errors mean "backend unreachable" — callers
// degrade to an empty list rather than failing.
func (t *Tool) ListModelsLive(ctx context.Context) ([]string, error) {
	base := t.baseURL()
	if err := checkBaseURL(base, t.defaults().AllowRemote); err != nil {
		return nil, err
	}

	path := "/v1/models"
	if t.flavor == FlavorOllama {
		path = "/api/tags"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s model listing returned HTTP %d", t.flavorName(), resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	if t.flavor == FlavorOllama {
		var body struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		names := make([]string, 0, len(body.Models))
		for _, m := range body.Models {
			names = append(names, m.Name)
		}
		return names, nil
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
```

- [ ] **Step 4: Run the full package tests**

Run: `go test ./pkg/tools/localai/ -v`
Expected: PASS (all tests from Tasks 3–5; interface assertions now compile)

---

### Task 6: Server integration — detection and live `/v1/models`

**Files:**
- Modify: `pkg/server/openai/models.go`
- Modify: `pkg/server/openai/types.go`
- Modify: `pkg/server/openai/handler.go:141`
- Test: `pkg/server/openai/models_list_test.go`

- [ ] **Step 1: Write the failing tests** (append to `models_list_test.go`; uses the **real** localai tool against an httptest backend so the merge path is exercised end to end — no fakes needed)

```go
// Imports to add: "context", "encoding/json", "net/http", "net/http/httptest",
// "rcodegen/pkg/tools/localai" (no import cycle: localai depends only on
// runner + settings).

// newOllamaFactoryAt returns a ToolFactory for an Ollama-flavor tool whose
// base URL points at the given fake backend.
func newOllamaFactoryAt(baseURL string) (server.ToolFactory, *settings.Settings) {
	s := settings.GetDefaultSettings()
	s.Defaults.Ollama.BaseURL = baseURL
	return func() runner.Tool { return localai.NewOllama() }, s
}

func TestDetectAvailableToolsIncludesAPIOnly(t *testing.T) {
	factory, _ := newOllamaFactoryAt("http://127.0.0.1:1")
	available := DetectAvailableTools(map[string]server.ToolFactory{"ollama": factory})
	if len(available) != 1 || available[0] != "ollama" {
		t.Errorf("available = %v, want [ollama]", available)
	}
}

func TestBuildModelListMergesLiveModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]string{{"name": "live-a"}, {"name": "live-b"}},
		})
	}))
	defer ts.Close()

	factory, cfg := newOllamaFactoryAt(ts.URL)
	list := BuildModelList(context.Background(), []string{"ollama"},
		map[string]server.ToolFactory{"ollama": factory}, cfg)

	found := map[string]bool{}
	for _, m := range list.Data {
		found[m.ID] = true
		if m.ID == "ollama:live-a" && !m.Live {
			t.Error("live entry not flagged Live")
		}
	}
	for _, want := range []string{"ollama", "ollama:live-a", "ollama:live-b"} {
		if !found[want] {
			t.Errorf("missing %q in %v", want, found)
		}
	}
}

func TestBuildModelListLiveFailureDegrades(t *testing.T) {
	// Unreachable backend: the bare tool entry must still appear, with the
	// configured default model as the fallback entry.
	factory, cfg := newOllamaFactoryAt("http://127.0.0.1:1")
	list := BuildModelList(context.Background(), []string{"ollama"},
		map[string]server.ToolFactory{"ollama": factory}, cfg)
	if len(list.Data) == 0 || list.Data[0].ID != "ollama" {
		t.Fatalf("bare tool entry missing when live listing fails: %+v", list.Data)
	}
	wantDefault := "ollama:" + settings.DefaultOllamaModel
	found := false
	for _, m := range list.Data {
		if m.ID == wantDefault {
			found = true
			if m.Live {
				t.Error("fallback default must not be flagged Live")
			}
		}
	}
	if !found {
		t.Errorf("missing fallback default entry %q", wantDefault)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./pkg/server/openai/ -run 'TestDetectAvailableToolsIncludesAPIOnly|TestBuildModelListMerges|TestBuildModelListLiveFailure' -v`
Expected: FAIL (BuildModelList has no ctx param; empty BinaryName not handled)

- [ ] **Step 3: Implement**

`types.go` — add to `ModelInfo` (next to `Dynamic`):

```go
	Live    bool     `json:"live,omitempty"`    // true when discovered from a running local backend
```

`models.go` — in `DetectAvailableTools`, before the LookPath call:

```go
		// API-based tools (empty BinaryName) need no CLI on PATH; whether the
		// backend is up surfaces at request time and in live model listing.
		if tool.BinaryName() == "" {
			available = append(available, name)
			continue
		}
```

`models.go` — change `BuildModelList` signature and add the live merge (imports: add `context`, `time`):

```go
func BuildModelList(ctx context.Context, available []string, factories map[string]server.ToolFactory, configured *settings.Settings) ModelList {
```

Inside the loop, replace the `models := tool.ValidModels()` block handling with:

```go
		def := tool.DefaultModelSetting()
		info.Efforts = runner.EffortsForModel(tool, def)
		models := tool.ValidModels()
		info.Dynamic = len(models) == 0
		live := false
		if len(models) == 0 {
			if lister, ok := tool.(runner.DynamicModelLister); ok {
				// A dead local backend must not stall /v1/models: cap the probe.
				probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				if names, err := lister.ListModelsLive(probeCtx); err == nil && len(names) > 0 {
					models, live = names, true
				}
				cancel()
			}
		}
		data = append(data, info)
		if len(models) == 0 && def != "" {
			models = []string{def}
		}
		for _, m := range models {
			data = append(data, ModelInfo{
				ID:      name + ":" + m,
				Object:  "model",
				Created: now,
				OwnedBy: "rcodegen",
				Default: m == def,
				Live:    live,
				Efforts: runner.EffortsForModel(tool, m),
			})
		}
```

`handler.go:141` — update the call site:

```go
	writeJSON(w, http.StatusOK, BuildModelList(r.Context(), h.availableTools, h.toolFactories, h.settings))
```

Fix any other `BuildModelList` callers the compiler reports (tests included) by passing `context.Background()`.

- [ ] **Step 4: Run the package tests**

Run: `go test ./pkg/server/openai/ -v`
Expected: PASS — including all pre-existing model-list tests (they now pass ctx).

---

### Task 7: Registration in rserve, rbatch, orchestrator

**Files:**
- Modify: `cmd/rserve/main.go:98-103`
- Modify: `pkg/batch/executor_local.go:37-42`
- Modify: `pkg/orchestrator/orchestrator.go:212-217`

- [ ] **Step 1: Add factories at all three sites** (import `"rcodegen/pkg/tools/localai"` in each file)

`cmd/rserve/main.go`:

```go
		"claude":   func() runner.Tool { return claude.New() },
		"codex":    func() runner.Tool { return codex.New() },
		"gemini":   func() runner.Tool { return gemini.New() },
		"kilocode": func() runner.Tool { return kilocode.New() },
		"opencode": func() runner.Tool { return opencode.New() },
		"ollama":   func() runner.Tool { return localai.NewOllama() },
		"lmstudio": func() runner.Tool { return localai.NewLMStudio() },
```

`pkg/batch/executor_local.go` — same two lines in its factory map.

`pkg/orchestrator/orchestrator.go`:

```go
		"claude":   claude.New(),
		"codex":    codex.New(),
		"gemini":   gemini.New(),
		"kilocode": kilocode.New(),
		"opencode": opencode.New(),
		"ollama":   localai.NewOllama(),
		"lmstudio": localai.NewLMStudio(),
```

- [ ] **Step 2: Build everything**

Run: `go build ./...`
Expected: success, no output.

- [ ] **Step 3: Smoke-test the wiring end to end** (requires a local Ollama; skip gracefully if absent)

```bash
make rserve
./bin/rserve &      # startup log prints the gRPC port; HTTP is gRPC+1
sleep 1
# rserve derives its gRPC port from chassis.Port("rserve", chassis.PortGRPC)
# (cmd/rserve/main.go:46) and serves the OpenAI HTTP API on gRPC+1
# (main.go:176). Read both from the startup log, then:
HTTP_PORT=<gRPC port from log + 1>
curl -s http://localhost:$HTTP_PORT/v1/models | python3 -m json.tool | grep -B1 -A4 'ollama'
curl -s http://localhost:$HTTP_PORT/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"ollama:qwen3:14b","messages":[{"role":"user","content":"Reply with exactly OK"}]}'
```

Expected: `/v1/models` lists `ollama` (Dynamic, with live entries if Ollama is running); the chat call returns a completion whose content came from the local model. If no local runtime is installed, verify instead that the request returns a clean JSON error (non-200) mentioning the backend is unreachable — not a hang, not an empty 200.

---

### Task 8: Docs — Phase 0 + Phase 1

**Files:**
- Modify: `README.md`
- Modify: `settings.json.example`

- [ ] **Step 1: settings.json.example** — add to the `defaults` object:

```json
    "ollama": {
      "base_url": "http://localhost:11434",
      "model": "qwen3:14b",
      "timeout_seconds": 600
    },
    "lmstudio": {
      "base_url": "http://localhost:1234",
      "model": "qwen/qwen3-14b",
      "timeout_seconds": 600
    }
```

- [ ] **Step 2: README** — add a "Local models (Ollama / LM Studio)" section covering:

```markdown
## Local Models (Ollama / LM Studio)

rserve can route chat-completion requests to a local model runtime. Two tool
names are built in: `ollama` (default endpoint http://localhost:11434) and
`lmstudio` (default http://localhost:1234).

    curl -s http://localhost:PORT/v1/chat/completions \
      -H 'Content-Type: application/json' \
      -d '{"model":"ollama:qwen3:14b","messages":[{"role":"user","content":"..."}]}'

- Model names are whatever the runtime has installed; `GET /v1/models`
  enumerates them live (entries flagged `"live": true`).
- These tools generate text only — no file editing, no shell. For *agentic*
  local-model work (audit/fix tasks), configure Ollama or LM Studio as a
  provider inside the opencode CLI and use `opencode` with
  `-m ollama/<model>`; rcodegen passes the provider/model string through.
- Endpoints are restricted to loopback/private addresses unless
  `allow_remote: true` is set for that tool in settings.
- Caveats: Ollama silently truncates prompts beyond the model's context
  window (set a larger `num_ctx` via a Modelfile); LM Studio JIT-loads
  models, so the first request to a model may take minutes — the
  `timeout_seconds` default (600) budgets for this.
```

Also update the README's tool table / `/v1/models` description to mention the two new tool names and the `live` flag.

---

### Task 9: Full verification

- [ ] **Step 1: Vendor check** (repo rule)

Run: `go mod vendor` — expect no changes (stdlib only). `git status` must show no vendor diff.

- [ ] **Step 2: Full build + tests**

Run: `make && make test`
Expected: all 6 binaries build; entire test suite passes. Paste failing output verbatim if anything fails — do not proceed to Task 10 until green.

---

### Task 10: VERSION, CHANGELOG, commit (single commit per repo rules)

- [ ] **Step 1: Only now read `VERSION`** (never earlier — collision rule), increment per repo convention (revisions roll to minor at 15).

- [ ] **Step 2: CHANGELOG entry**

```markdown
## X.Y.Z
- New first-class local-model tools: `ollama` and `lmstudio` (pkg/tools/localai) —
  direct OpenAI-compatible chat-completion calls, no CLI subprocess.
- /v1/models now live-enumerates installed local models (Ollama /api/tags,
  LM Studio /v1/models) with a "live" flag; API-based tools no longer require
  a CLI binary on PATH to be detected.
- Settings: defaults.ollama / defaults.lmstudio (base_url, model,
  timeout_seconds, allow_remote) + RCODEGEN_OLLAMA_BASE_URL /
  RCODEGEN_LMSTUDIO_BASE_URL env overrides. Base URLs restricted to
  loopback/private hosts unless allow_remote is set.
```

- [ ] **Step 3: Rebuild with version baked in**

Run: `make`
Expected: binaries report the new version via `-v`.

- [ ] **Step 4: Commit and push**

```bash
git add -A
git commit -m "Add first-class Ollama and LM Studio tools (X.Y.Z)

Agent: Claude:Opus 4.8"
git push
```

---

## Follow-up plan (not in this plan): Phase 2

Separate plan after this ships: effort mapping via `/v1` `reasoning_effort` (Ollama), SSE streaming passthrough (stream deltas to `cfg.Output`), LM Studio `/api/v1` model metadata (loaded state/quant/context) in listings, backend health in `/healthz`, and a prompt-size-vs-context-window preflight (Ollama `/api/show`). The preflight is the highest-value item — it closes the silent-truncation hazard.

## Known risks

1. `BuildModelList` signature change touches existing tests — the compiler will enumerate every call site; mechanical fix.
2. The 500 ms live-listing probe assumes the runtime answers listing calls fast even while inference runs; both runtimes serve listings from memory, but if `/v1/models` latency becomes an issue, cache the live list for ~10 s.
3. `orchestrator.go` uses shared tool instances (not factories) — `localai.Tool` holds only immutable flavor + settings, so sharing is safe today; revisit if the tool ever gains per-run state.
4. Handler `splitToolEffort` fallback: `ollama`/`lmstudio` return nil `ValidEfforts`, so names like `ollama-high` fail with "unknown tool" — correct behavior for Phase 1.
