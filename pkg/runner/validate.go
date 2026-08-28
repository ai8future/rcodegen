// Package runner provides the core execution framework for rcodegen tools.
package runner

import (
	"fmt"
	"strings"
)

// ValidateModel checks if the given model is valid for a tool.
// It uses the tool's ValidModels() method to determine valid models.
// Returns nil if valid, or an error with a helpful message if invalid.
func ValidateModel(tool Tool, model string) error {
	validModels := tool.ValidModels()
	// A nil/empty list is the Tool contract for a dynamic model namespace
	// (for example opencode and kilocode provider/model identifiers).
	if len(validModels) == 0 {
		return nil
	}
	for _, valid := range validModels {
		if model == valid {
			return nil
		}
	}
	return fmt.Errorf("invalid model '%s'. Valid options: %s", model, strings.Join(validModels, ", "))
}

// EffortsForModel returns the effort levels accepted by a specific model.
// Most tools use one tool-wide list; tools with model-dependent capabilities
// can implement ModelEffortProvider.
func EffortsForModel(tool Tool, model string) []string {
	if provider, ok := tool.(ModelEffortProvider); ok {
		return provider.ValidEffortsForModel(model)
	}
	return tool.ValidEfforts()
}

// ValidateEffort checks whether an effort is supported by a specific model.
func ValidateEffort(tool Tool, model, effort string) error {
	if effort == "" {
		return nil
	}
	validEfforts := EffortsForModel(tool, model)
	for _, valid := range validEfforts {
		if effort == valid {
			return nil
		}
	}
	return fmt.Errorf("invalid effort '%s' for model '%s'. Valid options: %s", effort, model, strings.Join(validEfforts, ", "))
}

// IsValidModel returns true if the model is valid for the given tool.
func IsValidModel(tool Tool, model string) bool {
	return ValidateModel(tool, model) == nil
}

// SplitModelEffort splits a model string carrying an optional trailing
// "-{effort}" suffix (e.g. "opus-max", "gpt-5.6-luna-high") into the base
// model and effort level. The suffix is only treated as an effort when the
// remainder is a valid model for the tool, so hyphenated model names like
// "gpt-5.6-luna" are never mangled. Returns the input unchanged (empty
// effort) when no valid suffix is present.
func SplitModelEffort(tool Tool, model string) (base, effort string) {
	// Runtime-defined model identifiers may legitimately end in "-high" or
	// another effort-looking suffix. Their effort must be supplied explicitly.
	if len(tool.ValidModels()) == 0 {
		return model, ""
	}
	for _, e := range tool.ValidEfforts() {
		suffix := "-" + e
		if strings.HasSuffix(model, suffix) {
			candidate := strings.TrimSuffix(model, suffix)
			if IsValidModel(tool, candidate) && ValidateEffort(tool, candidate, e) == nil {
				return candidate, e
			}
		}
	}
	return model, ""
}
