package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rcodegen/pkg/bundle"
	"rcodegen/pkg/envelope"
	"rcodegen/pkg/jev"
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/settings"
	"rcodegen/pkg/workspace"
)

const decideOK = `{"model":"jev-1.13.0","answers":{` +
	`"verdict":{"type":"choice","choice":"incomplete","probabilities":{"complete":0.3,"incomplete":0.7},"confidence":0.4},` +
	`"done":{"type":"noul","noul":0.25}},"usage":{"input_tokens":300,"output_tokens":0}}`

func decideFixture(t *testing.T, upstream http.HandlerFunc) (*DecideExecutor, *orchestrator.Context, *workspace.Workspace) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	s := settings.GetDefaultSettings()
	s.Defaults.Jev = settings.JevDefaults{BaseURL: srv.URL, Model: "jev-1.13.0", TimeoutSeconds: 2, MaxConcurrent: 2, APIKey: "unit-key"}
	e := &DecideExecutor{}
	e.SetSettings(s)

	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := orchestrator.NewContext(context.Background(), map[string]string{"task": "build a CLI"})
	path, err := ws.WriteOutput("build", map[string]interface{}{"stdout": "COMPLETION SUMMARY: wrote " + t.TempDir() + "/src/main.go, tests not run"})
	if err != nil {
		t.Fatal(err)
	}
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})
	return e, ctx, ws
}

func gateStep() *bundle.Step {
	return &bundle.Step{Name: "gate", Decide: &bundle.DecideDef{
		State: "OBJECTIVE:\n${inputs.task}\n\nREPORT:\n${steps.build.stdout}",
		Questions: map[string]jev.Question{
			"verdict": {Type: "choice", Instructions: "pick", Criteria: json.RawMessage(`{"complete":"c","incomplete":"i"}`)},
			"done":    {Type: "noul", Instructions: "done?"},
		},
	}}
}

func TestDecideExecutorResolvesScrubsAndWritesResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var seen jev.Request
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = w.Write([]byte(decideOK))
	})
	path, _ := ws.WriteOutput("build", map[string]interface{}{"stdout": "wrote " + home + "/src/main.go"})
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})

	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != envelope.StatusSuccess {
		t.Fatalf("status = %s (%+v)", env.Status, env.Error)
	}
	if !strings.Contains(seen.State, "OBJECTIVE:\nbuild a CLI") || !strings.Contains(seen.State, "wrote ~/src/main.go") || strings.Contains(seen.State, home) {
		t.Fatalf("state sent = %q", seen.State)
	}
	if env.Result["verdict"] != "incomplete" || env.Result["done"] != "0.2500" {
		t.Fatalf("results = %+v", env.Result)
	}
	if _, present := env.Result["decision"]; present {
		t.Fatal("no synthesized decision key: conditions name the question")
	}
	if env.Result["verdict_confidence"] != "0.4000" {
		t.Fatalf("confidence = %v", env.Result["verdict_confidence"])
	}
	stdout, _ := env.Result["stdout"].(string)
	var shown struct {
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil || shown.Answers["verdict"] != "incomplete" || shown.Answers["done"] != "0.2500" {
		t.Fatalf("stdout = %q (err %v)", stdout, err)
	}
	if got := orchestrator.StepOutput(env); got != stdout {
		t.Fatalf("StepOutput = %q, want %q", got, stdout)
	}
	fileCtx := orchestrator.NewContext(context.Background(), nil)
	fileCtx.SetResult("gate", env)
	if got := fileCtx.Resolve("${steps.gate.stdout}"); got != stdout {
		t.Fatalf("${steps.gate.stdout} = %q, want %q", got, stdout)
	}
	if c, ok := env.Result["cost_usd"].(float64); !ok || c != jev.CostUSD(300) || c <= 0 {
		t.Fatalf("cost = %v (%T), want %v", env.Result["cost_usd"], env.Result["cost_usd"], jev.CostUSD(300))
	}
	if in, ok := env.Result["input_tokens"].(int); !ok || in != 300 {
		t.Fatalf("input tokens = %v (%T)", env.Result["input_tokens"], env.Result["input_tokens"])
	}
	if env.Result["model"] != "jev-1.13.0" || env.Metrics == nil || env.Metrics.Tool != "jev" {
		t.Fatalf("model/tool = %v / %+v", env.Result["model"], env.Metrics)
	}
	if env.OutputRef == "" {
		t.Fatal("no output file written")
	}

	ctx.SetResult("gate", env)
	if !orchestrator.EvaluateCondition("${steps.gate.result.verdict} != 'complete'", ctx) {
		t.Fatal("choice condition should be true")
	}
	if orchestrator.EvaluateCondition("${steps.gate.result.done} >= 0.6", ctx) {
		t.Fatal("noul 0.25 must not pass a 0.6 threshold")
	}
	if !orchestrator.EvaluateCondition("${steps.gate.result.done} < 0.6", ctx) {
		t.Fatal("noul numeric compare failed")
	}
}

func TestDecideExecutorUnusableAnswersFail(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing question", `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete","probabilities":{"complete":1,"incomplete":0},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":0}}`, "done"},
		{"choice outside options", `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"maybe","probabilities":{"complete":0.5,"incomplete":0.5},"confidence":0.1},"done":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`, "maybe"},
		{"choice without distribution", `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"complete"},"done":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":0}}`, "probabilities"},
		{"empty answers", `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`, "no answers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			env, err := e.Execute(gateStep(), ctx, ws)
			if err != nil {
				t.Fatal(err)
			}
			if env.Status != envelope.StatusFailure || env.Error == nil || env.Error.Code != "JEV_INVALID_RESPONSE" || !strings.Contains(env.Error.Message, tc.want) {
				t.Fatalf("envelope = %+v", env)
			}
		})
	}
}

func TestDecideExecutorUpstreamFailureIsFailureEnvelope(t *testing.T) {
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"message":"bad criteria unit-key"}}`))
	})
	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil {
		t.Fatalf("upstream failure must not abort with a Go error: %v", err)
	}
	if env.Status != envelope.StatusFailure || env.Error.Code != "JEV_ERROR" {
		t.Fatalf("envelope = %+v", env)
	}
	if strings.Contains(env.Error.Message, "unit-key") {
		t.Fatal("key leaked into envelope")
	}
}

func TestDecideExecutorFailsWhenOutputCannotBeWritten(t *testing.T) {
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decideOK)) })
	ws.JobDir = filepath.Join(t.TempDir(), "missing")
	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if env.Status != envelope.StatusFailure || env.Error == nil || env.Error.Code != "OUTPUT_WRITE_FAILED" {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Result["model"] != "jev-1.13.0" || env.Result["input_tokens"] != 300 || env.Result["cost_usd"] != jev.CostUSD(300) {
		t.Fatalf("failed write should preserve accounting metadata, got %+v", env.Result)
	}
}

func TestDecideExecutorUnconfigured(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	e := &DecideExecutor{}
	e.SetSettings(settings.GetDefaultSettings())
	ws, _ := workspace.New(t.TempDir())
	ctx := orchestrator.NewContext(context.Background(), nil)
	env, err := e.Execute(gateStep(), ctx, ws)
	if err != nil || env.Status != envelope.StatusFailure || env.Error.Code != "JEV_UNCONFIGURED" {
		t.Fatalf("env = %+v err = %v", env, err)
	}
	nilSettings := &DecideExecutor{}
	env, err = nilSettings.Execute(gateStep(), ctx, ws)
	if err != nil || env.Error == nil || env.Error.Code != "JEV_UNCONFIGURED" {
		t.Fatalf("nil settings env = %+v err = %v", env, err)
	}
}

func TestDecideExecutorCancelledContextReturnsError(t *testing.T) {
	e, _, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(decideOK)) })
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := orchestrator.NewContext(cctx, map[string]string{"task": "t"})
	path, _ := ws.WriteOutput("build", map[string]interface{}{"stdout": "x"})
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})
	env, err := e.Execute(gateStep(), ctx, ws)
	if err == nil || env == nil || env.Error == nil || env.Error.Code != "CANCELLED" {
		t.Fatalf("env = %+v err = %v", env, err)
	}
}

func TestDecideExecutorCapsOversizedStateKeepingHeadAndTail(t *testing.T) {
	var seen jev.Request
	e, ctx, ws := decideFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = w.Write([]byte(decideOK))
	})
	big := strings.Repeat("noise\n", jev.MaxStateBytes/6+10) + "COMPLETION SUMMARY: end"
	path, _ := ws.WriteOutput("build", map[string]interface{}{"stdout": big})
	ctx.SetResult("build", &envelope.Envelope{Status: envelope.StatusSuccess, OutputRef: path})
	step := gateStep()
	env, err := e.Execute(step, ctx, ws)
	if err != nil || env.Status != envelope.StatusSuccess {
		t.Fatalf("env = %+v err = %v", env, err)
	}
	budget := jev.StateBudget(step.Decide.Questions)
	if len(seen.State) > budget {
		t.Fatalf("state len %d exceeds budget %d", len(seen.State), budget)
	}
	if !strings.HasPrefix(seen.State, "OBJECTIVE:\nbuild a CLI") {
		t.Fatalf("head lost: %q", seen.State[:40])
	}
	if !strings.HasSuffix(seen.State, "COMPLETION SUMMARY: end") || !strings.Contains(seen.State, "[state truncated") {
		t.Fatalf("tail or marker lost: %q", seen.State[len(seen.State)-60:])
	}
	if env.Result["state_truncated"] != true {
		t.Fatalf("state_truncated = %v", env.Result["state_truncated"])
	}
}

func TestDispatcherRoutesDecideAndAcceptsSettings(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	d := NewDispatcher(nil)
	d.SetSettings(settings.GetDefaultSettings())
	ws, _ := workspace.New(t.TempDir())
	ctx := orchestrator.NewContext(context.Background(), nil)
	env, err := d.Execute(gateStep(), ctx, ws)
	if err != nil || env == nil || env.Error == nil || env.Error.Code != "JEV_UNCONFIGURED" {
		t.Fatalf("dispatcher did not route to decide executor: env = %+v err = %v", env, err)
	}
}

func TestOrchestratorBranchesOnDecideVerdict(t *testing.T) {
	for _, tc := range []struct {
		name           string
		verdict        string
		wantRepairDone bool
	}{
		{name: "complete skips repair", verdict: "complete", wantRepairDone: false},
		{name: "incomplete runs repair", verdict: "incomplete", wantRepairDone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("TYPESAFE_API_KEY", "")
			body := `{"model":"jev-1.13.0","answers":{"verdict":{"type":"choice","choice":"` + tc.verdict + `","probabilities":{"complete":0.5,"incomplete":0.5},"confidence":0.5}},"usage":{"input_tokens":10,"output_tokens":0}}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			old := orchestrator.DispatcherFactory
			orchestrator.DispatcherFactory = func(map[string]runner.Tool) orchestrator.StepExecutor {
				return NewDispatcher(nil)
			}
			defer func() { orchestrator.DispatcherFactory = old }()

			s := settings.GetDefaultSettings()
			s.Defaults.Jev = settings.JevDefaults{BaseURL: srv.URL, Model: "jev-1.13.0", TimeoutSeconds: 2, MaxConcurrent: 2, APIKey: "unit-key"}
			o := orchestrator.New(s)
			var events []orchestrator.StepEvent
			o.SetStepCallback(func(ev orchestrator.StepEvent) { events = append(events, ev) })
			b := &bundle.Bundle{Name: "decide-branch", Steps: []bundle.Step{
				{Name: "gate", Decide: &bundle.DecideDef{
					State: "report",
					Questions: map[string]jev.Question{
						"verdict": {Type: "choice", Instructions: "pick", Criteria: json.RawMessage(`{"complete":"done","incomplete":"missing"}`)},
					},
				}},
				{Name: "repair", If: "${steps.gate.result.verdict} != 'complete'", Then: &bundle.Step{
					Name: "repair-vote",
					Vote: &bundle.VoteDef{Inputs: []string{"gate"}, Strategy: "majority"},
				}},
			}}

			env, err := o.Run(b, map[string]string{})
			if err != nil || env.Status != envelope.StatusSuccess {
				t.Fatalf("Run env = %+v err = %v", env, err)
			}
			repairDone := false
			repairSkipped := false
			for _, ev := range events {
				if ev.Name == "repair" && ev.Type == orchestrator.StepEventCompleted {
					repairDone = true
				}
				if ev.Name == "repair" && ev.Type == orchestrator.StepEventSkipped {
					repairSkipped = true
				}
			}
			if repairDone != tc.wantRepairDone {
				t.Fatalf("repair completed = %v, want %v; events = %+v", repairDone, tc.wantRepairDone, events)
			}
			if repairSkipped == tc.wantRepairDone {
				t.Fatalf("repair skipped = %v, want %v; events = %+v", repairSkipped, !tc.wantRepairDone, events)
			}
		})
	}
}
