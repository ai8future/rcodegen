package orchestrator

import (
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
