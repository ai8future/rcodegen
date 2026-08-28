// Package localai provides direct API adapters for local Ollama and LM Studio runtimes.
package localai

import (
	"fmt"
	"os/exec"
	"strings"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
)

// Flavor identifies the local runtime API contract.
type Flavor int

const (
	FlavorOllama Flavor = iota
	FlavorLMStudio
)

// Tool implements the shared runner contracts for a local model runtime.
type Tool struct {
	flavor   Flavor
	settings *settings.Settings
}

func NewOllama() *Tool   { return &Tool{flavor: FlavorOllama} }
func NewLMStudio() *Tool { return &Tool{flavor: FlavorLMStudio} }

func (t *Tool) SetSettings(s *settings.Settings) { t.settings = s }

// Name intentionally omits the repository's usual r-prefix: these are API
// namespaces and there are no rollama or rlmstudio binaries.
func (t *Tool) Name() string {
	if t.flavor == FlavorOllama {
		return "ollama"
	}
	return "lmstudio"
}

func (t *Tool) BinaryName() string    { return "" }
func (t *Tool) ReportDir() string     { return "_rcodegen" }
func (t *Tool) ReportPrefix() string  { return t.Name() + "-" }
func (t *Tool) ValidModels() []string { return nil }

func (t *Tool) ValidEfforts() []string {
	if t.flavor == FlavorOllama {
		return []string{"none", "low", "medium", "high", "max"}
	}
	return nil
}

func (t *Tool) DefaultModel() string        { return t.runtimeSettings().Model }
func (t *Tool) DefaultModelSetting() string { return t.runtimeSettings().Model }

// BuildCommand is a defensive fallback only. Every supported caller dispatches
// this API-only tool through DirectAPIRunner.
func (t *Tool) BuildCommand(*runner.Config, string, string) *exec.Cmd {
	return exec.Command("false")
}

func (t *Tool) ShouldUseDirectAPI(*runner.Config) bool      { return true }
func (t *Tool) ShowStatus()                                 {}
func (t *Tool) SupportsStatusTracking() bool                { return false }
func (t *Tool) CaptureStatusBefore() interface{}            { return nil }
func (t *Tool) CaptureStatusAfter() interface{}             { return nil }
func (t *Tool) PrintStatusSummary(interface{}, interface{}) {}
func (t *Tool) ToolSpecificFlags() []runner.FlagDef         { return nil }

func (t *Tool) ApplyToolDefaults(cfg *runner.Config) {
	if cfg.Model == "" {
		cfg.Model = t.runtimeSettings().Model
	}
}

func (t *Tool) PrepareForExecution(*runner.Config) {}

func (t *Tool) ValidateConfig(cfg *runner.Config) error {
	if strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("%s model is required: use %s:<model> or configure defaults.%s.model", t.Name(), t.Name(), t.Name())
	}
	for i, message := range cfg.Messages {
		if err := validateMessage(message); err != nil {
			return fmt.Errorf("message %d: %w", i, err)
		}
	}
	if err := runner.ValidateEffort(t, cfg.Model, cfg.Effort); err != nil {
		return err
	}
	return nil
}

func (t *Tool) BannerTitle() string                           { return strings.ToUpper(t.Name()) }
func (t *Tool) BannerSubtitle() string                        { return "local text generation API" }
func (t *Tool) PrintToolSpecificBannerFields(*runner.Config)  {}
func (t *Tool) PrintToolSpecificSummaryFields(*runner.Config) {}
func (t *Tool) SecurityWarning() []string {
	return []string{"This API generates text only; it does not edit files or execute commands."}
}
func (t *Tool) ToolSpecificHelpSections() []runner.HelpSection {
	label := "LM Studio"
	if t.flavor == FlavorOllama {
		label = "Ollama"
	}
	return []runner.HelpSection{{
		Title: label + " Options",
		Lines: []string{
			"  Generates text through the local runtime API.",
			"  It does not edit files or execute commands.",
			"  Select a model with " + t.Name() + ":<model> or configure defaults." + t.Name() + ".model.",
		},
	}}
}
func (t *Tool) StatsJSONFields(cfg *runner.Config) map[string]interface{} {
	return map[string]interface{}{"runtime": t.Name(), "model": cfg.Model, "base_url": t.safeBaseURL()}
}
func (t *Tool) UsesStreamOutput() bool { return false }
func (t *Tool) RunLogFields(cfg *runner.Config) []string {
	return []string{"Runtime: " + t.Name(), "Model: " + cfg.Model, "Base URL: " + t.safeBaseURL()}
}
func (t *Tool) ReportedUsage(res *runner.RunResult) (runner.RunUsage, bool) {
	if res == nil || res.TokenUsage == nil {
		return runner.RunUsage{}, false
	}
	return runner.RunUsage{
		InputTokens: res.TokenUsage.InputTokens, OutputTokens: res.TokenUsage.OutputTokens, CostUSD: res.TotalCostUSD,
	}, true
}

func (t *Tool) runtimeSettings() settings.LocalAIDefaults {
	var result settings.LocalAIDefaults
	if t.settings != nil {
		if t.flavor == FlavorOllama {
			result = t.settings.Defaults.Ollama
		} else {
			result = t.settings.Defaults.LMStudio
		}
	}
	if result.BaseURL == "" {
		if t.flavor == FlavorOllama {
			result.BaseURL = settings.DefaultOllamaBaseURL
		} else {
			result.BaseURL = settings.DefaultLMStudioBaseURL
		}
	}
	if result.TimeoutSeconds <= 0 {
		result.TimeoutSeconds = settings.DefaultLocalAITimeoutSeconds
	}
	return result
}

func (t *Tool) safeBaseURL() string {
	origin, err := validateOrigin(t.runtimeSettings())
	if err != nil {
		return "invalid"
	}
	return origin.String()
}

func validateMessage(message runner.ChatMessage) error {
	role := strings.TrimSpace(message.Role)
	if role != "system" && role != "user" && role != "assistant" {
		return fmt.Errorf("unsupported role %q (supported: system, user, assistant)", message.Role)
	}
	if strings.TrimSpace(message.Content) == "" {
		return fmt.Errorf("content must not be empty")
	}
	return nil
}

var (
	_ runner.Tool               = (*Tool)(nil)
	_ runner.DirectAPIRunner    = (*Tool)(nil)
	_ runner.DynamicModelLister = (*Tool)(nil)
	_ runner.UsageReporter      = (*Tool)(nil)
	_ runner.SettingsAware      = (*Tool)(nil)
)
