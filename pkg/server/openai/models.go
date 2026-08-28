package openai

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

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
		if tool.BinaryName() == "" {
			available = append(available, name)
		} else if _, err := exec.LookPath(tool.BinaryName()); err == nil {
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
func BuildModelList(ctx context.Context, available []string, factories map[string]server.ToolFactory, configured *settings.Settings) ModelList {
	now := nowUnix()
	if ctx == nil {
		ctx = context.Background()
	}
	type toolModels struct {
		data []ModelInfo
	}
	results := make([]toolModels, len(available))
	var wg sync.WaitGroup
	for idx, name := range available {
		name := name
		idx := idx
		wg.Add(1)
		go func() {
			defer wg.Done()
			info := ModelInfo{
				ID:      name,
				Object:  "model",
				Created: now,
				OwnedBy: "rcodegen",
			}
			factory, ok := factories[name]
			if !ok {
				results[idx] = toolModels{data: []ModelInfo{info}}
				return
			}
			tool := factory()
			applyToolSettings(tool, configured)
			def := tool.DefaultModelSetting()
			info.Efforts = runner.EffortsForModel(tool, def)
			models := tool.ValidModels()
			info.Dynamic = len(models) == 0
			entries := []ModelInfo{info}
			if len(models) == 0 {
				if lister, ok := tool.(runner.DynamicModelLister); ok {
					probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
					discovered, err := lister.ListAvailableModels(probeCtx)
					cancel()
					availableSet := make(map[string]bool, len(discovered))
					if err == nil {
						for _, model := range discovered {
							availableSet[model] = true
						}
					}
					if def != "" {
						if _, discovered := availableSet[def]; !discovered {
							availableSet[def] = false
						}
					}
					models = make([]string, 0, len(availableSet))
					for model := range availableSet {
						models = append(models, model)
					}
					sort.Strings(models)
					for _, model := range models {
						value := availableSet[model]
						entries = append(entries, ModelInfo{
							ID: name + ":" + model, Object: "model", Created: now, OwnedBy: "rcodegen",
							Default: model == def, Efforts: runner.EffortsForModel(tool, model), Available: &value,
						})
					}
					results[idx] = toolModels{data: entries}
					return
				}
				if def != "" {
					models = []string{def}
				}
			}
			for _, m := range models {
				entries = append(entries, ModelInfo{
					ID:      name + ":" + m,
					Object:  "model",
					Created: now,
					OwnedBy: "rcodegen",
					Default: m == def,
					Efforts: runner.EffortsForModel(tool, m),
				})
			}
			results[idx] = toolModels{data: entries}
		}()
	}
	wg.Wait()
	var data []ModelInfo
	for _, result := range results {
		data = append(data, result.data...)
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].ID < data[j].ID })
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
