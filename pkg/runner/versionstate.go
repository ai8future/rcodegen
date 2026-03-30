package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const versionStateFile = "version_state.json"

// VersionStateEntry records when a tool+task was last run against a specific version
type VersionStateEntry struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	Tool    string `json:"tool"`
	Task    string `json:"task"`
}

// VersionState maps "tool:task" keys to their last-run state
type VersionState map[string]VersionStateEntry

// versionStateKey returns the map key for a given tool and task
func versionStateKey(toolName, task string) string {
	return strings.ToLower(toolName) + ":" + task
}

// readRepoVersion reads the VERSION file from a repo directory.
// Returns empty string if no VERSION file exists.
func readRepoVersion(repoDir string) string {
	data, err := os.ReadFile(filepath.Join(repoDir, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// loadVersionState reads the version state file from the report directory.
// Returns an empty state if the file doesn't exist.
func loadVersionState(reportDir string) (VersionState, error) {
	path := filepath.Join(reportDir, versionStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(VersionState), nil
		}
		return nil, err
	}
	var state VersionState
	if err := json.Unmarshal(data, &state); err != nil {
		// Corrupted file — start fresh
		return make(VersionState), nil
	}
	return state, nil
}

// saveVersionState writes the version state file to the report directory.
func saveVersionState(reportDir string, state VersionState) error {
	if err := os.MkdirAll(reportDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(reportDir, versionStateFile), append(data, '\n'), 0644)
}

// CheckVersionState checks whether a task should be skipped because the repo VERSION
// hasn't changed since the last run. Returns true if the task should be SKIPPED.
// repoDir is the target repository root; reportDir is where _rcodegen lives.
func CheckVersionState(repoDir, reportDir, toolName, task string) (skip bool, msg string) {
	version := readRepoVersion(repoDir)
	if version == "" {
		return false, "" // No VERSION file — always run
	}

	state, err := loadVersionState(reportDir)
	if err != nil {
		return false, "" // Can't read state — run anyway
	}

	key := versionStateKey(toolName, task)
	entry, exists := state[key]
	if !exists {
		return false, ""
	}

	if entry.Version == version {
		msg = fmt.Sprintf("Skipping %s %s: VERSION %s unchanged since %s (use -f to force)",
			toolName, task, version, entry.Date)
		return true, msg
	}

	return false, ""
}

// RecordVersionState records that a tool+task was run against the current repo VERSION.
func RecordVersionState(repoDir, reportDir, toolName, task string) {
	version := readRepoVersion(repoDir)
	if version == "" {
		return // No VERSION file — nothing to record
	}

	state, err := loadVersionState(reportDir)
	if err != nil {
		return
	}

	key := versionStateKey(toolName, task)
	state[key] = VersionStateEntry{
		Version: version,
		Date:    time.Now().Format("2006-01-02"),
		Tool:    strings.ToLower(toolName),
		Task:    task,
	}

	_ = saveVersionState(reportDir, state)
}
