package bundle

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed builtin/*.json
var builtinBundles embed.FS

func Load(name string) (*Bundle, error) {
	// Try user bundles first
	userPath := filepath.Join(os.Getenv("HOME"), ".rcodegen", "bundles", name+".json")
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
	userDir := filepath.Join(os.Getenv("HOME"), ".rcodegen", "bundles")
	if entries, err := os.ReadDir(userDir); err == nil {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".json" {
				names = append(names, e.Name()[:len(e.Name())-5])
			}
		}
	}

	return names, nil
}
