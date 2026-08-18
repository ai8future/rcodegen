package openai

import (
	"os/exec"
	"sort"
	"strings"

	rcodegenpkg "rcodegen"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/settings"
)

// ParseModel splits a model string like "claude:opus-4" into (tool, model).
// Only the first colon is used as the delimiter, so "claude:sonnet-4:thinking"
// returns ("claude", "sonnet-4:thinking"). Empty input returns ("", "").
func ParseModel(s string) (tool, model string) {
	if s == "" {
		return "", ""
	}
	idx := strings.Index(s, ":")
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx+1:]
}

// ExtractTaskPrompt collapses an OpenAI messages array into a single task string.
// System messages are concatenated with "\n" as a preamble. The last user message
// is used as the task. The result is "{system}\n\n{lastUser}". If there is no
// user message, an empty string is returned. If there are no system messages,
// just the last user message is returned.
func ExtractTaskPrompt(messages []Message) string {
	var systems []string
	var lastUser string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systems = append(systems, msg.Content)
		case "user":
			lastUser = msg.Content
		}
	}

	if lastUser == "" {
		return ""
	}

	if len(systems) == 0 {
		return lastUser
	}

	return strings.Join(systems, "\n") + "\n\n" + lastUser
}

// DetectAvailableTools checks which tool CLIs are available on PATH.
// For each tool factory, it creates a tool instance, calls BinaryName(),
// and uses exec.LookPath to verify the binary exists. Returns a slice of
// available tool names.
func DetectAvailableTools(toolFactories map[string]server.ToolFactory) []string {
	var available []string
	for name, factory := range toolFactories {
		tool := factory()
		if _, err := exec.LookPath(tool.BinaryName()); err == nil {
			available = append(available, name)
		}
	}
	sort.Strings(available)
	return available
}

// BuildModelList constructs a ModelList response with one bare entry per
// available tool (runs that tool's configured default model) plus one
// "tool:model" entry for every fixed model the tool accepts. Dynamic model
// namespaces include their configured default and advertise "dynamic": true
// on the bare tool entry. Model entries carry their model-specific efforts.
func BuildModelList(available []string, factories map[string]server.ToolFactory, configured *settings.Settings) ModelList {
	now := nowUnix()
	var data []ModelInfo
	for _, name := range available {
		info := ModelInfo{
			ID:      name,
			Object:  "model",
			Created: now,
			OwnedBy: "rcodegen",
		}
		factory, ok := factories[name]
		if !ok {
			data = append(data, info)
			continue
		}
		tool := factory()
		applyToolSettings(tool, configured)
		def := tool.DefaultModelSetting()
		info.Efforts = runner.EffortsForModel(tool, def)
		models := tool.ValidModels()
		info.Dynamic = len(models) == 0
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
				Efforts: runner.EffortsForModel(tool, m),
			})
		}
	}
	return ModelList{
		Object: "list",
		Data:   data,
	}
}

func applyToolSettings(tool runner.Tool, configured *settings.Settings) {
	if aware, ok := tool.(runner.SettingsAware); ok && configured != nil {
		aware.SetSettings(configured)
	}
}

// ToolVersion returns the current rcodegen version string.
func ToolVersion() string {
	return rcodegenpkg.AppVersion
}
