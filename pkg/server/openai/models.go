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

// BuildModelList constructs a ModelList response with ModelInfo entries for
// each available tool. The list Object is "list", and each entry uses
// Object="model", OwnedBy="rcodegen", and Created set to the current Unix time.
func BuildModelList(available []string) ModelList {
	now := nowUnix()
	data := make([]ModelInfo, len(available))
	for i, name := range available {
		data[i] = ModelInfo{
			ID:      name,
			Object:  "model",
			Created: now,
			OwnedBy: "rcodegen",
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
