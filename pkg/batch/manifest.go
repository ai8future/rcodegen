// Package batch provides manifest parsing and job orchestration for rbatch,
// a batch job runner that executes multiple coding agent tasks with
// concurrency control and budget awareness.
package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// validTools lists the allowed tool values for a job.
var validTools = map[string]bool{
	"claude": true,
	"codex":  true,
	"gemini": true,
}

// validOnBudget lists the allowed on_budget strategies.
var validOnBudget = map[string]bool{
	"stop": true,
	"wait": true,
	"ask":  true,
}

// Manifest describes a batch of jobs to execute with concurrency control
// and budget awareness.
type Manifest struct {
	Name        string       `json:"name,omitempty"`
	Concurrency int          `json:"concurrency,omitempty"`
	Budget      BudgetConfig `json:"budget,omitempty"`
	Jobs        []JobDef     `json:"jobs"`
}

// BudgetConfig controls how the batch runner responds to budget thresholds.
type BudgetConfig struct {
	ThresholdPct  int    `json:"threshold_pct,omitempty"`
	OnBudget      string `json:"on_budget,omitempty"`
	CheckInterval string `json:"check_interval,omitempty"`
	MaxWait       string `json:"max_wait,omitempty"`
}

// CheckIntervalDuration parses the CheckInterval string as a time.Duration.
// Returns 3 minutes if the string is empty or missing.
func (b *BudgetConfig) CheckIntervalDuration() (time.Duration, error) {
	if b.CheckInterval == "" {
		return 3 * time.Minute, nil
	}
	return time.ParseDuration(b.CheckInterval)
}

// MaxWaitDuration parses the MaxWait string as a time.Duration.
// Returns 1 hour if the string is empty or missing.
func (b *BudgetConfig) MaxWaitDuration() (time.Duration, error) {
	if b.MaxWait == "" {
		return 1 * time.Hour, nil
	}
	return time.ParseDuration(b.MaxWait)
}

// JobDef defines a single job within a batch manifest.
type JobDef struct {
	Name      string `json:"name,omitempty"`
	Task      string `json:"task"`
	Tool      string `json:"tool,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Session   string `json:"session,omitempty"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	MaxBudget string `json:"max_budget,omitempty"`
}

// LoadManifest reads a JSON manifest file, applies defaults, and validates it.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	applyDefaults(&m)

	if err := validate(&m); err != nil {
		return nil, err
	}

	return &m, nil
}

// applyDefaults fills in missing fields with sensible default values.
func applyDefaults(m *Manifest) {
	if m.Concurrency < 1 {
		m.Concurrency = 1
	}

	// Budget defaults
	if m.Budget.OnBudget == "" {
		m.Budget.OnBudget = "stop"
	}
	if m.Budget.CheckInterval == "" {
		m.Budget.CheckInterval = "3m"
	}
	if m.Budget.MaxWait == "" {
		m.Budget.MaxWait = "1h"
	}

	// Job defaults
	for i := range m.Jobs {
		if m.Jobs[i].Tool == "" {
			m.Jobs[i].Tool = "claude"
		}
		if m.Jobs[i].Name == "" {
			m.Jobs[i].Name = fmt.Sprintf("job-%d", i+1)
		}
	}
}

// validate checks that the manifest is well-formed.
func validate(m *Manifest) error {
	if len(m.Jobs) == 0 {
		return fmt.Errorf("manifest must contain at least one job")
	}

	if !validOnBudget[m.Budget.OnBudget] {
		return fmt.Errorf("invalid on_budget value %q: must be one of stop, wait, ask", m.Budget.OnBudget)
	}

	for i, j := range m.Jobs {
		if j.Task == "" {
			return fmt.Errorf("job %d (%s): task is required", i+1, j.Name)
		}
		if !validTools[j.Tool] {
			return fmt.Errorf("job %d (%s): invalid tool %q: must be one of claude, codex, gemini", i+1, j.Name, j.Tool)
		}
	}

	return nil
}
