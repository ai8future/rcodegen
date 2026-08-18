package openai

import (
	"os/exec"
	"sort"
	"strings"

	rcodegenpkg "rcodegen"
	"rcodegen/pkg/server"
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
// available tool (runs that tool's default model) plus one "tool:model" entry
// for every model the tool accepts — the single source of truth for the model
// naming space, so agents discover valid names instead of guessing. The
// tool's default model entry is flagged with "default": true.
func BuildModelList(available []string, factories map[string]server.ToolFactory) ModelList {
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
		info.Efforts = tool.ValidEfforts()
		data = append(data, info)
		def := tool.DefaultModel()
		for _, m := range tool.ValidModels() {
			data = append(data, ModelInfo{
				ID:      name + ":" + m,
				Object:  "model",
				Created: now,
				OwnedBy: "rcodegen",
				Default: m == def,
			})
		}
	}
	return ModelList{
		Object: "list",
		Data:   data,
	}
}

// ToolVersion returns the current rcodegen version string.
func ToolVersion() string {
	return rcodegenpkg.AppVersion
}
