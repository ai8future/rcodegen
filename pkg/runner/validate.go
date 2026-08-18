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
	for _, valid := range validModels {
		if model == valid {
			return nil
		}
	}
	return fmt.Errorf("invalid model '%s'. Valid options: %s", model, strings.Join(validModels, ", "))
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
	for _, e := range tool.ValidEfforts() {
		suffix := "-" + e
		if strings.HasSuffix(model, suffix) {
			candidate := strings.TrimSuffix(model, suffix)
			if IsValidModel(tool, candidate) {
				return candidate, e
			}
		}
	}
	return model, ""
}
