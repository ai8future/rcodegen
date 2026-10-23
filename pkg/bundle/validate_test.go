package bundle

import (
	"encoding/json"
	"strings"
	"testing"

	"rcodegen/pkg/jev"
)

func decideStep() Step {
	return Step{Name: "gate", Decide: &DecideDef{
		State: "${steps.build.stdout}",
		Questions: map[string]jev.Question{
			"verdict": {Type: "choice", Instructions: "pick", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
		},
	}}
}

func TestValidateAcceptsDecideStep(t *testing.T) {
	b := &Bundle{Name: "x", Steps: []Step{{Name: "build", Tool: "claude", Task: "t"}, decideStep()}}
	if err := b.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateRejectsBadDecideSteps(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Step)
		want string
	}{
		{"missing state", func(s *Step) { s.Decide.State = "" }, "state"},
		{"bad question", func(s *Step) { s.Decide.Questions["verdict"] = jev.Question{Type: "essay", Instructions: "x"} }, "type"},
		{"no questions", func(s *Step) { s.Decide.Questions = nil }, "at least one"},
		{"decide plus tool", func(s *Step) { s.Tool = "claude" }, "cannot combine"},
		{"decide plus parallel", func(s *Step) { s.Parallel = []Step{{Name: "x", Tool: "claude", Task: "x"}} }, "cannot combine"},
		{"decide plus merge", func(s *Step) { s.Merge = &MergeDef{Inputs: []string{"a"}, Strategy: "concat"} }, "cannot combine"},
		{"decide plus vote", func(s *Step) { s.Vote = &VoteDef{Inputs: []string{"a"}, Strategy: "majority"} }, "cannot combine"},
		{"decide plus then", func(s *Step) { s.If = "true"; s.Then = &Step{Name: "t", Tool: "claude", Task: "x"} }, "then/else"},
		{"decide plus else", func(s *Step) { s.If = "true"; s.Else = &Step{Name: "e", Tool: "claude", Task: "x"} }, "then/else"},
		{"no name", func(s *Step) { s.Name = "" }, "name"},
		{"reserved name cost_usd", func(s *Step) {
			s.Decide.Questions["cost_usd"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "reserved"},
		{"reserved name stdout", func(s *Step) {
			s.Decide.Questions["stdout"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "reserved"},
		{"reserved name model", func(s *Step) {
			s.Decide.Questions["model"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "reserved"},
		{"derived suffix confidence", func(s *Step) {
			s.Decide.Questions["verdict_confidence"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "_confidence"},
		{"derived suffix probabilities", func(s *Step) {
			s.Decide.Questions["x_probabilities"] = jev.Question{Type: "noul", Instructions: "x"}
		}, "_probabilities"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := decideStep()
			tc.mut(&s)
			b := &Bundle{Name: "x", Steps: []Step{s}}
			err := b.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateWalksNestedSteps(t *testing.T) {
	bad := decideStep()
	bad.Decide.State = ""
	b := &Bundle{Name: "x", Steps: []Step{
		{Name: "c", If: "true", Then: &bad, Else: &Step{Name: "e", Tool: "claude", Task: "t"}},
	}}
	if err := b.Validate(); err == nil {
		t.Fatal("then-branch decide step not validated")
	}
	good := decideStep()
	b = &Bundle{Name: "x", Steps: []Step{
		{Name: "c", If: "true", Then: &Step{Name: "e", Tool: "claude", Task: "t"}, Else: &good},
	}}
	if err := b.Validate(); err != nil {
		t.Fatalf("decide in else branch must be allowed: %v", err)
	}
}

func TestValidateRejectsDecideInsideParallel(t *testing.T) {
	good := decideStep()
	b := &Bundle{Name: "x", Steps: []Step{
		{Name: "p", Parallel: []Step{{Name: "a", Tool: "claude", Task: "t"}, good}},
	}}
	err := b.Validate()
	if err == nil || !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("decide directly inside parallel must be rejected, err = %v", err)
	}
	nested := &Bundle{Name: "x", Steps: []Step{
		{Name: "p", Parallel: []Step{{Name: "inner", Parallel: []Step{good}}}},
	}}
	if err := nested.Validate(); err == nil || !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("decide nested two levels inside parallel must be rejected, err = %v", err)
	}
	branch := &Bundle{Name: "x", Steps: []Step{
		{Name: "p", Parallel: []Step{{Name: "c", If: "true", Then: &good}}},
	}}
	if err := branch.Validate(); err == nil || !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("decide in a branch inside parallel must be rejected, err = %v", err)
	}
}

func TestBuiltinBundlesValidate(t *testing.T) {
	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := Load(name); err != nil {
			t.Fatalf("builtin %s: %v", name, err)
		}
	}
}

func TestModelHint(t *testing.T) {
	if got := (Step{Tool: "claude", Model: "opus"}).ModelHint(); got != "opus" {
		t.Fatalf("tool model hint = %q", got)
	}
	s := decideStep()
	s.Decide.Model = "jev-latest"
	if got := s.ModelHint(); got != "jev-latest" {
		t.Fatalf("decide model hint = %q", got)
	}
	if got := decideStep().ModelHint(); got != "" {
		t.Fatalf("decide without override should hint empty, got %q", got)
	}
}

func TestToolLabel(t *testing.T) {
	if got := (Step{Tool: "claude"}).ToolLabel(); got != "claude" {
		t.Fatalf("tool label = %q", got)
	}
	if got := decideStep().ToolLabel(); got != "jev" {
		t.Fatalf("decide label = %q", got)
	}
	if got := (Step{Parallel: []Step{{Tool: "claude"}}}).ToolLabel(); got != "parallel" {
		t.Fatalf("parallel label = %q", got)
	}
	if got := (Step{Vote: &VoteDef{}}).ToolLabel(); got != "" {
		t.Fatalf("vote label = %q", got)
	}
}

func TestDecideJSONRoundTrip(t *testing.T) {
	src := `{"name":"gate","decide":{"state":"s","model":"jev-latest","questions":{"done":{"type":"noul","instructions":"done?"}}}}`
	var s Step
	if err := json.Unmarshal([]byte(src), &s); err != nil {
		t.Fatal(err)
	}
	if s.Decide == nil || s.Decide.Model != "jev-latest" || s.Decide.Questions["done"].Type != "noul" {
		t.Fatalf("decoded = %+v", s)
	}
	out, _ := json.Marshal(s)
	if !strings.Contains(string(out), `"decide":{`) {
		t.Fatalf("encoded = %s", out)
	}
}
