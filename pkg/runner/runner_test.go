package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	chassis "github.com/ai8future/chassis-go/v11"
)

func TestMain(m *testing.M) {
	chassis.RequireMajor(11)
	os.Exit(m.Run())
}

type directRunnerTestTool struct {
	validationTool
	exitCode int
}

func (t *directRunnerTestTool) ShouldUseDirectAPI(*Config) bool { return true }
func (t *directRunnerTestTool) RunDirectAPI(ctx context.Context, cfg *Config, _, _ string) int {
	if ctx.Err() != nil {
		return 130
	}
	_, _ = fmt.Fprint(cfg.Output, "direct output")
	return t.exitCode
}

func TestRunWithContextDirectAPIResultAndMessages(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exitCode int
		wantErr  bool
	}{
		{name: "success"},
		{name: "failure", exitCode: 7, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &directRunnerTestTool{validationTool: validationTool{efforts: []string{"high"}}, exitCode: tc.exitCode}
			var output bytes.Buffer
			cfg := &Config{Task: "hello", Model: "dynamic-high", Output: &output, Messages: []ChatMessage{
				{Role: "system", Content: "rules"}, {Role: "user", Content: "question"}, {Role: "assistant", Content: "prior"},
			}}
			result := NewRunner(tool).RunWithContext(context.Background(), cfg)
			if result.ExitCode != tc.exitCode || (result.Error != nil) != tc.wantErr {
				t.Fatalf("result = %+v", result)
			}
			if output.String() != "direct output" || len(cfg.Messages) != 3 || cfg.Messages[2].Role != "assistant" {
				t.Fatalf("output/messages = %q %+v", output.String(), cfg.Messages)
			}
		})
	}
}

func TestRunWithContextDirectAPICancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewRunner(&directRunnerTestTool{}).RunWithContext(ctx, &Config{Task: "hello", Output: &bytes.Buffer{}})
	if result.ExitCode != 130 || result.Error != context.Canceled {
		t.Fatalf("cancelled result = %+v", result)
	}
}

func TestRunError(t *testing.T) {
	result := runError(1, fmt.Errorf("test error"))

	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
	if result.Error == nil {
		t.Error("expected error to be set")
	}
	if result.Error.Error() != "test error" {
		t.Errorf("expected error message 'test error', got %q", result.Error.Error())
	}
}

func TestRunResult_SuccessResult(t *testing.T) {
	result := &RunResult{
		ExitCode:     0,
		TokenUsage:   &TokenUsage{InputTokens: 100, OutputTokens: 50},
		TotalCostUSD: 0.0015,
	}

	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Error != nil {
		t.Errorf("expected no error, got %v", result.Error)
	}
	if result.TokenUsage == nil {
		t.Error("expected TokenUsage to be set")
	}
	if result.TokenUsage.InputTokens != 100 {
		t.Errorf("expected input tokens 100, got %d", result.TokenUsage.InputTokens)
	}
	if result.TotalCostUSD != 0.0015 {
		t.Errorf("expected cost 0.0015, got %f", result.TotalCostUSD)
	}
}

func TestRunError_DifferentCodes(t *testing.T) {
	tests := []struct {
		code int
		msg  string
	}{
		{0, "success with error message"},
		{1, "general error"},
		{2, "usage error"},
		{127, "command not found"},
	}

	for _, tc := range tests {
		result := runError(tc.code, fmt.Errorf("%s", tc.msg))
		if result.ExitCode != tc.code {
			t.Errorf("runError(%d, %q): expected exit code %d, got %d",
				tc.code, tc.msg, tc.code, result.ExitCode)
		}
		if result.Error.Error() != tc.msg {
			t.Errorf("runError(%d, %q): expected error message %q, got %q",
				tc.code, tc.msg, tc.msg, result.Error.Error())
		}
	}
}

func TestFindSuiteDirs(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "serp_suite", "serp_svc", ".git"), 0755)
	os.MkdirAll(filepath.Join(base, "ai_suite", "infra_ai8", ".git"), 0755)
	os.MkdirAll(filepath.Join(base, "regular_dir"), 0755)
	os.MkdirAll(filepath.Join(base, "solstice", ".git"), 0755)

	dirs := findSuiteDirs(base, nil)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 suite dirs, got %d: %v", len(dirs), dirs)
	}

	// Verify caching works
	var cached []string
	_ = findSuiteDirs(base, &cached)
	if cached == nil {
		t.Fatal("expected cache to be populated")
	}
	dirs2 := findSuiteDirs(base, &cached)
	if len(dirs2) != 2 {
		t.Fatalf("cached call: expected 2 suite dirs, got %d", len(dirs2))
	}
}

func TestFindSuiteDirs_Empty(t *testing.T) {
	base := t.TempDir()
	dirs := findSuiteDirs(base, nil)
	if len(dirs) != 0 {
		t.Fatalf("expected 0 suite dirs, got %d", len(dirs))
	}
}

func TestFindSuiteDirs_CachesEmptyResult(t *testing.T) {
	base := t.TempDir()
	var cached []string
	_ = findSuiteDirs(base, &cached)
	// After first call with no results, cache should be non-nil empty slice
	if cached == nil {
		t.Fatal("expected cache to be populated even with empty result")
	}
	if len(cached) != 0 {
		t.Fatalf("expected 0 cached dirs, got %d", len(cached))
	}
}

func TestDiscoverDirectories_SingleLevel(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "repo1", ".git"), 0755)
	os.MkdirAll(filepath.Join(base, "repo2", ".git"), 0755)
	os.MkdirAll(filepath.Join(base, "not_a_repo"), 0755)

	dirs, err := discoverDirectories(base, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(dirs)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(dirs), dirs)
	}
	if filepath.Base(dirs[0]) != "repo1" {
		t.Errorf("expected repo1, got %s", filepath.Base(dirs[0]))
	}
	if filepath.Base(dirs[1]) != "repo2" {
		t.Errorf("expected repo2, got %s", filepath.Base(dirs[1]))
	}
}

func TestDiscoverDirectories_VersionFile(t *testing.T) {
	base := t.TempDir()
	// Project with only a VERSION file (no .git)
	os.MkdirAll(filepath.Join(base, "proj_version"), 0755)
	os.WriteFile(filepath.Join(base, "proj_version", "VERSION"), []byte("1.0.0"), 0644)
	// Project with both .git and VERSION
	os.MkdirAll(filepath.Join(base, "proj_both", ".git"), 0755)
	os.WriteFile(filepath.Join(base, "proj_both", "VERSION"), []byte("2.0.0"), 0644)
	// Directory with neither
	os.MkdirAll(filepath.Join(base, "plain_dir"), 0755)

	dirs, err := discoverDirectories(base, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(dirs)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(dirs), dirs)
	}
	if filepath.Base(dirs[0]) != "proj_both" {
		t.Errorf("expected proj_both, got %s", filepath.Base(dirs[0]))
	}
	if filepath.Base(dirs[1]) != "proj_version" {
		t.Errorf("expected proj_version, got %s", filepath.Base(dirs[1]))
	}
}

func TestExpandFileReferences(t *testing.T) {
	tmp := t.TempDir()
	specFile := filepath.Join(tmp, "spec.md")
	if err := os.WriteFile(specFile, []byte("do the thing"), 0644); err != nil {
		t.Fatal(err)
	}
	noExtFile := filepath.Join(tmp, "instructions")
	if err := os.WriteFile(noExtFile, []byte("step one"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "expands file reference with extension",
			input: "review @" + specFile + " carefully",
			want:  "review do the thing carefully",
		},
		{
			name:  "expands file reference without extension",
			input: "follow @" + noExtFile,
			want:  "follow step one",
		},
		{
			name:  "no reference left unchanged",
			input: "just a plain prompt",
			want:  "just a plain prompt",
		},
		{
			name:  "missing file left unchanged",
			input: "read @" + filepath.Join(tmp, "missing.txt") + " and act",
			want:  "read @" + filepath.Join(tmp, "missing.txt") + " and act",
		},
		{
			name:  "bare @word that is not a file left unchanged",
			input: "ping @someone about this",
			want:  "ping @someone about this",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandFileReferences(tc.input)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
