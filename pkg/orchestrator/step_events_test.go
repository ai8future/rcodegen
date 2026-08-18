package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/workspace"
)

// stubExecutor returns canned success envelopes without spawning tools.
type stubExecutor struct {
	outputs map[string]string
}

func (s *stubExecutor) Execute(step *bundle.Step, ctx *Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	return envelope.New().
		Success().
		WithResult("stdout", s.outputs[step.Name]).
		WithResult("cost_usd", 0.25).
		WithResult("input_tokens", 3).
		WithResult("output_tokens", 4).
		WithResult("model", "stub-model").
		Build(), nil
}

func TestRun_EmitsStepEvents(t *testing.T) {
	// Keep the run workspace (~/.rcodegen/workspace) out of the real home dir.
	t.Setenv("HOME", t.TempDir())

	old := DispatcherFactory
	DispatcherFactory = func(tools map[string]runner.Tool) StepExecutor {
		return &stubExecutor{outputs: map[string]string{"one": "o1", "two": "o2"}}
	}
	defer func() { DispatcherFactory = old }()

	o := New(&settings.Settings{})
	var events []StepEvent
	o.SetStepCallback(func(ev StepEvent) { events = append(events, ev) })

	b := &bundle.Bundle{
		Name: "test-bundle",
		Steps: []bundle.Step{
			{Name: "one", Tool: "claude", Task: "t1"},
			{Name: "two", Tool: "gemini", Task: "t2"},
		},
	}
	env, err := o.Run(b, map[string]string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if env.Status != envelope.StatusSuccess {
		t.Fatalf("status = %q, want success", env.Status)
	}

	want := []struct {
		typ  StepEventType
		name string
	}{
		{StepEventStarted, "one"},
		{StepEventCompleted, "one"},
		{StepEventStarted, "two"},
		{StepEventCompleted, "two"},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i, w := range want {
		if events[i].Type != w.typ || events[i].Name != w.name {
			t.Errorf("events[%d] = %s/%s, want %s/%s", i, events[i].Type, events[i].Name, w.typ, w.name)
		}
	}

	// Completed events carry stats and the step envelope.
	c := events[1]
	if c.Status != string(envelope.StatusSuccess) {
		t.Errorf("completed status = %q, want success", c.Status)
	}
	if c.CostUSD != 0.25 || c.InputTokens != 3 || c.OutputTokens != 4 {
		t.Errorf("completed stats = cost %v, in %d, out %d; want 0.25/3/4", c.CostUSD, c.InputTokens, c.OutputTokens)
	}
	if c.Envelope == nil || c.Envelope.Result["stdout"] != "o1" {
		t.Errorf("completed envelope stdout = %v, want o1", c.Envelope)
	}
	if c.Model != "stub-model" {
		t.Errorf("completed model = %q, want stub-model", c.Model)
	}
}

// stubFuncExecutor adapts a function to the StepExecutor interface.
type stubFuncExecutor func(step *bundle.Step) (*envelope.Envelope, error)

func (f stubFuncExecutor) Execute(step *bundle.Step, ctx *Context, ws *workspace.Workspace) (*envelope.Envelope, error) {
	return f(step)
}

func TestRunWithContext_CancelStopsBetweenSteps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	old := DispatcherFactory
	DispatcherFactory = func(tools map[string]runner.Tool) StepExecutor {
		return stubFuncExecutor(func(step *bundle.Step) (*envelope.Envelope, error) {
			calls++
			cancel() // simulate client disconnect during step 1
			return envelope.New().Success().Build(), nil
		})
	}
	defer func() { DispatcherFactory = old }()

	o := New(&settings.Settings{})
	b := &bundle.Bundle{
		Name: "cancel-bundle",
		Steps: []bundle.Step{
			{Name: "a", Tool: "claude", Task: "t1"},
			{Name: "b", Tool: "claude", Task: "t2"},
		},
	}
	env, err := o.RunWithContext(ctx, b, map[string]string{})
	if err == nil {
		t.Fatal("expected error from cancelled run")
	}
	if calls != 1 {
		t.Errorf("executor calls = %d, want 1 (step 2 must not run)", calls)
	}
	if env == nil || env.Error == nil || env.Error.Code != "INTERRUPTED" {
		t.Errorf("envelope = %+v, want INTERRUPTED error", env)
	}
	if got := env.Result["input_tokens"]; got != 0 {
		t.Errorf("input_tokens = %v, want aggregate zero on cancellation", got)
	}
	if got := env.Result["output_tokens"]; got != 0 {
		t.Errorf("output_tokens = %v, want aggregate zero on cancellation", got)
	}
}

func TestStepOutput(t *testing.T) {
	if got := StepOutput(nil); got != "" {
		t.Errorf("nil envelope: got %q, want empty", got)
	}

	// Result["stdout"] takes precedence (also the synthetic-test path).
	env := envelope.New().Success().WithResult("stdout", "plain text").Build()
	if got := StepOutput(env); got != "plain text" {
		t.Errorf("result stdout: got %q", got)
	}

	// Stream-JSON in stdout is unwrapped to the final result.
	streamed := `{"type":"assistant","message":{}}` + "\n" + `{"type":"result","result":"the answer"}`
	env = envelope.New().Success().WithResult("stdout", streamed).Build()
	if got := StepOutput(env); got != "the answer" {
		t.Errorf("stream-json stdout: got %q", got)
	}

	// Production path: stdout persisted in the OutputRef file.
	dir := t.TempDir()
	ref := filepath.Join(dir, "step.json")
	if err := os.WriteFile(ref, []byte(`{"stdout":"from file","stderr":""}`), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	env = envelope.New().Success().Build()
	env.OutputRef = ref
	if got := StepOutput(env); got != "from file" {
		t.Errorf("output_ref stdout: got %q", got)
	}

	// Merge steps persist under "merged".
	if err := os.WriteFile(ref, []byte(`{"merged":"combined output","input_count":2}`), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	if got := StepOutput(env); got != "combined output" {
		t.Errorf("output_ref merged: got %q", got)
	}

	// Vote steps persist their verdict under "decision".
	if err := os.WriteFile(ref, []byte(`{"decision":"candidate-b","strategy":"majority"}`), 0o644); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	if got := StepOutput(env); got != "candidate-b" {
		t.Errorf("output_ref decision: got %q", got)
	}

	// Missing file degrades to empty, not an error.
	env.OutputRef = filepath.Join(dir, "gone.json")
	if got := StepOutput(env); got != "" {
		t.Errorf("missing ref: got %q, want empty", got)
	}
}

func TestRun_EmitsSkippedForFalseCondition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	old := DispatcherFactory
	DispatcherFactory = func(tools map[string]runner.Tool) StepExecutor {
		return &stubExecutor{outputs: map[string]string{"first": "ok"}}
	}
	defer func() { DispatcherFactory = old }()

	o := New(&settings.Settings{})
	var events []StepEvent
	o.SetStepCallback(func(ev StepEvent) { events = append(events, ev) })

	b := &bundle.Bundle{
		Name: "test-bundle",
		Steps: []bundle.Step{
			{Name: "first", Tool: "claude", Task: "t1"},
			{Name: "second", Tool: "claude", Task: "t2", If: "${steps.first.status} == 'failure'"},
		},
	}
	if _, err := o.Run(b, map[string]string{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Expect: started(first), completed(first), started(second), skipped(second).
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(events), events)
	}
	last := events[3]
	if last.Type != StepEventSkipped || last.Name != "second" {
		t.Errorf("last event = %s/%s, want skipped/second", last.Type, last.Name)
	}
}

func TestRun_ConditionalElseExecutesAndContributesTotals(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var executed *bundle.Step
	old := DispatcherFactory
	DispatcherFactory = func(map[string]runner.Tool) StepExecutor {
		return stubFuncExecutor(func(step *bundle.Step) (*envelope.Envelope, error) {
			executed = step
			return envelope.New().
				Success().
				WithResult("stdout", "fallback result").
				WithResult("cost_usd", 0.25).
				WithResult("input_tokens", 3).
				WithResult("output_tokens", 4).
				WithResult("model", "fallback-model").
				Build(), nil
		})
	}
	defer func() { DispatcherFactory = old }()

	o := New(&settings.Settings{})
	var events []StepEvent
	o.SetStepCallback(func(ev StepEvent) { events = append(events, ev) })
	b := &bundle.Bundle{
		Name: "conditional-bundle",
		Steps: []bundle.Step{{
			Name: "choice",
			If:   "false",
			Then: &bundle.Step{Name: "primary", Tool: "claude", Task: "primary"},
			Else: &bundle.Step{Name: "fallback", Tool: "gemini", Task: "fallback"},
		}},
	}

	env, err := o.Run(b, map[string]string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executed == nil || executed.Name != "fallback" || executed.Tool != "gemini" {
		t.Fatalf("executed step = %+v, want fallback Gemini branch", executed)
	}
	if got := env.Result["total_cost_usd"]; got != 0.25 {
		t.Errorf("total_cost_usd = %v, want 0.25", got)
	}
	if got := env.Result["input_tokens"]; got != 3 {
		t.Errorf("input_tokens = %v, want 3", got)
	}
	if got := env.Result["output_tokens"]; got != 4 {
		t.Errorf("output_tokens = %v, want 4", got)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want started + completed", events)
	}
	completed := events[1]
	if completed.Name != "choice" || completed.Tool != "gemini" || completed.Model != "fallback-model" {
		t.Errorf("completed event identity = %+v", completed)
	}
	if completed.CostUSD != 0.25 || completed.InputTokens != 3 || completed.OutputTokens != 4 {
		t.Errorf("completed event metrics = %+v", completed)
	}
}

func TestRun_FailedStepPreservesAggregateTotals(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	calls := 0
	old := DispatcherFactory
	DispatcherFactory = func(map[string]runner.Tool) StepExecutor {
		return stubFuncExecutor(func(*bundle.Step) (*envelope.Envelope, error) {
			calls++
			status := envelope.New().Success()
			if calls == 2 {
				status = envelope.New().Failure("FAILED", "boom")
			}
			return status.
				WithResult("cost_usd", 0.5).
				WithResult("input_tokens", 2).
				WithResult("output_tokens", 3).
				WithResult("model", "stub-model").
				Build(), nil
		})
	}
	defer func() { DispatcherFactory = old }()

	o := New(&settings.Settings{})
	b := &bundle.Bundle{Name: "failure-bundle", Steps: []bundle.Step{
		{Name: "one", Tool: "claude", Task: "one"},
		{Name: "two", Tool: "gemini", Task: "two"},
	}}
	env, err := o.Run(b, map[string]string{})
	if err == nil {
		t.Fatal("expected failed bundle error")
	}
	if got := env.Result["total_cost_usd"]; got != 1.0 {
		t.Errorf("failed total_cost_usd = %v, want 1.0", got)
	}
	if got := env.Result["input_tokens"]; got != 4 {
		t.Errorf("failed input_tokens = %v, want 4", got)
	}
	if got := env.Result["output_tokens"]; got != 6 {
		t.Errorf("failed output_tokens = %v, want 6", got)
	}
}
