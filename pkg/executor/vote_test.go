package executor

import (
	"context"
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/workspace"
)

func TestExtractStepName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"full ref with output_ref", "${steps.analyze.output_ref}", "analyze"},
		{"full ref with status", "${steps.build.status}", "build"},
		{"with underscore", "${steps.test_runner.result}", "test_runner"},
		{"with dash", "${steps.test-runner.result}", "test-runner"},
		{"plain string passthrough", "not-a-ref", "not-a-ref"},
		{"empty string", "", ""},
		// Edge case: ${steps.name} without a second dot still extracts the name
		{"just steps prefix no dot", "${steps.name}", "name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractStepName(tc.input)
			if result != tc.expected {
				t.Errorf("extractStepName(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestVoteExecutor_Majority_Approved(t *testing.T) {
	ctx := orchestrator.NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{Status: envelope.StatusSuccess})
	ctx.SetResult("step2", &envelope.Envelope{Status: envelope.StatusSuccess})
	ctx.SetResult("step3", &envelope.Envelope{Status: envelope.StatusFailure})

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	step := &bundle.Step{
		Name: "vote-test",
		Vote: &bundle.VoteDef{
			Inputs:   []string{"${steps.step1.output_ref}", "${steps.step2.output_ref}", "${steps.step3.output_ref}"},
			Strategy: "majority",
		},
	}

	env, execErr := (&VoteExecutor{}).Execute(step, ctx, ws)
	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}

	if env.Result["decision"] != "approved" {
		t.Errorf("expected 'approved' with 2/3 success, got %v", env.Result["decision"])
	}

	votes := env.Result["votes"].(map[string]int)
	if votes["success"] != 2 {
		t.Errorf("expected 2 success votes, got %d", votes["success"])
	}
	if votes["failure"] != 1 {
		t.Errorf("expected 1 failure vote, got %d", votes["failure"])
	}
}

func TestVoteExecutor_Majority_Rejected(t *testing.T) {
	ctx := orchestrator.NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{Status: envelope.StatusSuccess})
	ctx.SetResult("step2", &envelope.Envelope{Status: envelope.StatusFailure})
	ctx.SetResult("step3", &envelope.Envelope{Status: envelope.StatusFailure})

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	step := &bundle.Step{
		Name: "vote-test",
		Vote: &bundle.VoteDef{
			Inputs:   []string{"${steps.step1.output_ref}", "${steps.step2.output_ref}", "${steps.step3.output_ref}"},
			Strategy: "majority",
		},
	}

	env, _ := (&VoteExecutor{}).Execute(step, ctx, ws)

	if env.Result["decision"] != "rejected" {
		t.Errorf("expected 'rejected' with 1/3 success, got %v", env.Result["decision"])
	}
}

func TestVoteExecutor_Majority_Tie(t *testing.T) {
	ctx := orchestrator.NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{Status: envelope.StatusSuccess})
	ctx.SetResult("step2", &envelope.Envelope{Status: envelope.StatusFailure})

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	step := &bundle.Step{
		Name: "vote-test",
		Vote: &bundle.VoteDef{
			Inputs:   []string{"${steps.step1.output_ref}", "${steps.step2.output_ref}"},
			Strategy: "majority",
		},
	}

	env, _ := (&VoteExecutor{}).Execute(step, ctx, ws)

	// Tie (1 vs 1) means not > half, so rejected
	if env.Result["decision"] != "rejected" {
		t.Errorf("expected 'rejected' for tie, got %v", env.Result["decision"])
	}
}

func TestVoteExecutor_Unanimous_Approved(t *testing.T) {
	ctx := orchestrator.NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{Status: envelope.StatusSuccess})
	ctx.SetResult("step2", &envelope.Envelope{Status: envelope.StatusSuccess})

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	step := &bundle.Step{
		Name: "vote-test",
		Vote: &bundle.VoteDef{
			Inputs:   []string{"${steps.step1.output_ref}", "${steps.step2.output_ref}"},
			Strategy: "unanimous",
		},
	}

	env, _ := (&VoteExecutor{}).Execute(step, ctx, ws)

	if env.Result["decision"] != "approved" {
		t.Errorf("expected 'approved' for unanimous success, got %v", env.Result["decision"])
	}
}

func TestVoteExecutor_Unanimous_Rejected(t *testing.T) {
	ctx := orchestrator.NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{Status: envelope.StatusSuccess})
	ctx.SetResult("step2", &envelope.Envelope{Status: envelope.StatusFailure})

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	step := &bundle.Step{
		Name: "vote-test",
		Vote: &bundle.VoteDef{
			Inputs:   []string{"${steps.step1.output_ref}", "${steps.step2.output_ref}"},
			Strategy: "unanimous",
		},
	}

	env, _ := (&VoteExecutor{}).Execute(step, ctx, ws)

	if env.Result["decision"] != "rejected" {
		t.Errorf("expected 'rejected' for unanimous with failure, got %v", env.Result["decision"])
	}
}

func TestVoteExecutor_UnknownStrategy(t *testing.T) {
	ctx := orchestrator.NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{Status: envelope.StatusSuccess})

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	step := &bundle.Step{
		Name: "vote-test",
		Vote: &bundle.VoteDef{
			Inputs:   []string{"${steps.step1.output_ref}"},
			Strategy: "consensus", // Unknown strategy
		},
	}

	env, _ := (&VoteExecutor{}).Execute(step, ctx, ws)

	if env.Result["decision"] != "unknown" {
		t.Errorf("expected 'unknown' for unknown strategy, got %v", env.Result["decision"])
	}
}

func TestVoteExecutor_MissingStep(t *testing.T) {
	ctx := orchestrator.NewContext(context.Background(), nil)
	ctx.SetResult("step1", &envelope.Envelope{Status: envelope.StatusSuccess})
	// step2 is NOT set

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	step := &bundle.Step{
		Name: "vote-test",
		Vote: &bundle.VoteDef{
			Inputs:   []string{"${steps.step1.output_ref}", "${steps.step2.output_ref}"},
			Strategy: "majority",
		},
	}

	env, _ := (&VoteExecutor{}).Execute(step, ctx, ws)

	// Only 1 vote counted (step2 is missing)
	votes := env.Result["votes"].(map[string]int)
	total := votes["success"] + votes["failure"]
	if total != 1 {
		t.Errorf("expected 1 total vote (missing step skipped), got %d", total)
	}
}
