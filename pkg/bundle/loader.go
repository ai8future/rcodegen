package bundle

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

//go:embed builtin/*.json
var builtinBundles embed.FS

// validBundleNamePattern matches alphanumeric, hyphens, underscores only
var validBundleNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// validateBundleName checks if a bundle name is safe to use in file paths
func validateBundleName(name string) error {
	if name == "" {
		return fmt.Errorf("invalid bundle name: empty")
	}
	if len(name) > 100 {
		return fmt.Errorf("invalid bundle name: too long (max 100 chars)")
	}
	if !validBundleNamePattern.MatchString(name) {
		return fmt.Errorf("invalid bundle name: must contain only alphanumeric, hyphens, underscores")
	}
	return nil
}

func Load(name string) (*Bundle, error) {
	// Validate bundle name to prevent path traversal
	if err := validateBundleName(name); err != nil {
		return nil, err
	}

	// Try user bundles first
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = os.Getenv("HOME") // Fallback for compatibility
	}
	userPath := filepath.Join(homeDir, ".rcodegen", "bundles", name+".json")
	if data, err := os.ReadFile(userPath); err == nil {
		var b Bundle
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, fmt.Errorf("invalid bundle %s: %w", name, err)
		}
		return &b, nil
	}

	// Try builtin bundles
	data, err := builtinBundles.ReadFile("builtin/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("bundle not found: %s", name)
	}

	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("invalid builtin bundle %s: %w", name, err)
	}
	return &b, nil
}

func List() ([]string, error) {
	var names []string

	// List builtin
	entries, _ := builtinBundles.ReadDir("builtin")
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name()[:len(e.Name())-5])
		}
	}

	// List user bundles
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = os.Getenv("HOME") // Fallback for compatibility
	}
	userDir := filepath.Join(homeDir, ".rcodegen", "bundles")
	if entries, err := os.ReadDir(userDir); err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".json" {
				names = append(names, e.Name()[:len(e.Name())-5])
			}
		}
	}

	return names, nil
}
