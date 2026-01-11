// Package bundle provides loading and management of task bundles,
// which are JSON-defined workflows with steps, prompts, and variables.
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
		b.SourcePath = userPath
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
	// For builtin bundles, find the source path relative to the executable
	b.SourcePath = findBuiltinBundlePath(name)
	return &b, nil
}

// findBuiltinBundlePath attempts to locate the source file for a builtin bundle
// This is useful for copying the bundle to output directories
func findBuiltinBundlePath(name string) string {
	// Try common development locations
	candidates := []string{
		filepath.Join("pkg", "bundle", "builtin", name+".json"),
	}

	// Try relative to executable
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "pkg", "bundle", "builtin", name+".json"),
			filepath.Join(exeDir, "..", "pkg", "bundle", "builtin", name+".json"),
		)
	}

	// Try relative to working directory
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "pkg", "bundle", "builtin", name+".json"),
		)
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				return abs
			}
			return path
		}
	}

	// Return a placeholder path if we can't find it (the embedded data will still be used)
	return "builtin/" + name + ".json"
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
