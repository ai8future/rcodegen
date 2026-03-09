package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"rcodegen/pkg/bundle"
	_ "rcodegen/pkg/executor" // Register dispatcher factory via init()
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/settings"

	cerrors "github.com/ai8future/chassis-go/v9/errors"
	"github.com/ai8future/chassis-go/v9/logz"
)

// ToolFactory creates a fresh tool instance to avoid shared mutable state.
type ToolFactory func() runner.Tool

// Server implements the RServe gRPC service.
type Server struct {
	pb.UnimplementedRServeServer
	settings     *settings.Settings
	toolFactories map[string]ToolFactory
	registry     *RunRegistry
}

// NewServer creates a new gRPC server instance.
// toolFactories maps tool names to factory functions that create fresh instances.
func NewServer(s *settings.Settings, toolFactories map[string]ToolFactory, registry *RunRegistry) *Server {
	return &Server{
		settings:      s,
		toolFactories: toolFactories,
		registry:      registry,
	}
}

// RunTask executes a single tool task and streams events back.
func (s *Server) RunTask(req *pb.RunTaskRequest, stream pb.RServe_RunTaskServer) error {
	// Validate tool
	factory, ok := s.toolFactories[req.Tool]
	if !ok {
		return cerrors.NotFoundError("unknown tool: " + req.Tool).GRPCStatus().Err()
	}

	// Create a fresh tool instance (avoids shared mutable state between requests)
	tool := factory()

	// Acquire a concurrency slot
	runID, runCtx, cancel, err := s.registry.Acquire(stream.Context(), req.Tool, req.Task)
	if err != nil {
		return cerrors.Errorf(cerrors.RateLimitError, "failed to acquire run slot: %v", err).GRPCStatus().Err()
	}
	defer cancel()
	defer s.registry.Release(runID)

	// Send init event
	if err := stream.Send(&pb.RunEvent{
		RunId:       runID,
		TimestampMs: time.Now().UnixMilli(),
		Event:       &pb.RunEvent_Init{Init: &pb.InitEvent{Tool: req.Tool, Model: req.Model}},
	}); err != nil {
		return err
	}

	// Inject settings into the tool (mirrors CLI's SettingsAware path)
	if sa, ok := tool.(runner.SettingsAware); ok && s.settings != nil {
		sa.SetSettings(s.settings)
	}

	// Build config
	cfg := runner.NewConfig()
	cfg.Task = req.Task
	cfg.WorkDirs = req.WorkDirs
	cfg.Vars = req.Variables
	cfg.Output = io.Discard // CLI-formatted output suppressed; events go via callback
	cfg.Logger = logz.New("warn")

	// Capture stderr so we can report errors to the client
	var stderrBuf bytes.Buffer
	cfg.Stderr = &stderrBuf

	// Look up task shortcut if task matches a known shortcut name
	if s.settings != nil && s.settings.Tasks != nil {
		if taskDef, ok := s.settings.Tasks[req.Task]; ok {
			cfg.TaskShortcut = req.Task
			cfg.Task = taskDef.Prompt
		}
	}

	// Apply tool defaults first (sets model, budget, etc. from settings)
	tool.ApplyToolDefaults(cfg)

	// Then override with request-level values (user takes priority)
	if req.Model != "" {
		cfg.Model = req.Model
	}
	if req.MaxBudget != "" {
		cfg.MaxBudget = req.MaxBudget
	}

	// If model is still empty, use the tool's built-in default
	if cfg.Model == "" {
		cfg.Model = tool.DefaultModel()
	}

	// Mutex-protected send to guard against future concurrency
	var sendMu sync.Mutex
	safeSend := func(event *pb.RunEvent) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(event)
	}

	// Wire up stream callback: convert each StreamEvent to proto RunEvent(s).
	// Cancel the run context if sending fails (client disconnected).
	cfg.OnStreamEvent = func(event *runner.StreamEvent) {
		events := streamEventToProto(runID, event)
		for _, ev := range events {
			if err := safeSend(ev); err != nil {
				cancel() // Stop the subprocess — client is gone
				return
			}
		}
	}

	r := &runner.Runner{
		Tool:     tool,
		Settings: s.settings,
	}

	// Run with context for cancellation support
	result := r.RunWithContext(runCtx, cfg)

	// Send result event
	resultEvent := &pb.ResultEvent{
		ExitCode:     int32(result.ExitCode),
		TotalCostUsd: result.TotalCostUSD,
	}
	if stderrBuf.Len() > 0 {
		resultEvent.Output = stderrBuf.String()
	}
	if result.TokenUsage != nil {
		resultEvent.Usage = &pb.TokenUsage{
			InputTokens:         int32(result.TokenUsage.InputTokens),
			OutputTokens:        int32(result.TokenUsage.OutputTokens),
			CacheReadTokens:     int32(result.TokenUsage.CacheReadInputTokens),
			CacheCreationTokens: int32(result.TokenUsage.CacheCreationInputTokens),
		}
	}

	if err := safeSend(&pb.RunEvent{
		RunId:       runID,
		TimestampMs: time.Now().UnixMilli(),
		Event:       &pb.RunEvent_Result{Result: resultEvent},
	}); err != nil {
		return err
	}

	return nil
}

// streamEventToProto converts a runner.StreamEvent into zero or more proto RunEvents.
// Returns a slice to handle messages with multiple content blocks.
func streamEventToProto(runID string, event *runner.StreamEvent) []*pb.RunEvent {
	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			return []*pb.RunEvent{{
				RunId:       runID,
				TimestampMs: time.Now().UnixMilli(),
				Event:       &pb.RunEvent_Init{Init: &pb.InitEvent{}},
			}}
		}
		return nil

	case "assistant":
		if event.Message == nil {
			return nil
		}
		var events []*pb.RunEvent
		for _, block := range event.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					events = append(events, &pb.RunEvent{
						RunId:       runID,
						TimestampMs: time.Now().UnixMilli(),
						Event:       &pb.RunEvent_Text{Text: &pb.TextEvent{Content: block.Text}},
					})
				}
			case "tool_use":
				summary := ""
				if len(block.Input) > 0 {
					var inputMap map[string]interface{}
					if json.Unmarshal(block.Input, &inputMap) == nil {
						for _, key := range []string{"file_path", "command", "pattern", "description", "query"} {
							if v, ok := inputMap[key].(string); ok {
								summary = v
								break
							}
						}
					}
				}
				events = append(events, &pb.RunEvent{
					RunId:       runID,
					TimestampMs: time.Now().UnixMilli(),
					Event: &pb.RunEvent_ToolUse{ToolUse: &pb.ToolUseEvent{
						ToolName: block.Name,
						Summary:  summary,
					}},
				})
			}
		}
		return events

	case "result":
		// Result events are handled separately after the run completes.
		return nil

	default:
		return nil
	}
}

// RunBundle executes a multi-step bundle and streams progress events.
func (s *Server) RunBundle(req *pb.RunBundleRequest, stream pb.RServe_RunBundleServer) error {
	// Acquire concurrency slot
	runID, _, cancel, err := s.registry.Acquire(stream.Context(), "bundle", req.Bundle)
	if err != nil {
		return cerrors.Errorf(cerrors.RateLimitError, "failed to acquire run slot: %v", err).GRPCStatus().Err()
	}
	defer cancel()
	defer s.registry.Release(runID)

	// Send init event
	if err := stream.Send(&pb.RunEvent{
		RunId:       runID,
		TimestampMs: time.Now().UnixMilli(),
		Event:       &pb.RunEvent_Init{Init: &pb.InitEvent{Tool: "bundle"}},
	}); err != nil {
		return err
	}

	// Load the bundle
	b, err := bundle.Load(req.Bundle)
	if err != nil {
		return cerrors.NotFoundError("bundle load failed: " + err.Error()).GRPCStatus().Err()
	}

	// Build inputs map (defensive copy — proto maps should not be mutated)
	inputs := make(map[string]string)
	for k, v := range req.Inputs {
		inputs[k] = v
	}

	// Create orchestrator
	// NOTE: orchestrator.Run creates its own signal context internally.
	// Proper context propagation requires orchestrator API changes (future work).
	orch := orchestrator.New(s.settings)
	orch.SetLiveMode(false) // No animated display for gRPC
	if req.OpusOnly {
		orch.SetOpusOnly(true)
	}
	if req.FlashOnly {
		orch.SetFlashOnly(true)
	}

	env, runErr := orch.Run(b, inputs)

	// Send result
	resultEvent := &pb.ResultEvent{
		ExitCode: 0,
	}
	if runErr != nil {
		resultEvent.ExitCode = 1
		resultEvent.Output = runErr.Error()
	}
	if env != nil {
		if cost, ok := env.Result["total_cost_usd"].(float64); ok {
			resultEvent.TotalCostUsd = cost
		}
		if env.Result["input_tokens"] != nil && env.Result["output_tokens"] != nil {
			resultEvent.Usage = &pb.TokenUsage{
				InputTokens:         toInt32(env.Result["input_tokens"]),
				OutputTokens:        toInt32(env.Result["output_tokens"]),
				CacheReadTokens:     toInt32(env.Result["cache_read_tokens"]),
				CacheCreationTokens: toInt32(env.Result["cache_write_tokens"]),
			}
		}
	}

	if err := stream.Send(&pb.RunEvent{
		RunId:       runID,
		TimestampMs: time.Now().UnixMilli(),
		Event:       &pb.RunEvent_Result{Result: resultEvent},
	}); err != nil {
		return err
	}

	return nil
}

// ListTasks returns available task shortcuts and bundles.
func (s *Server) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	resp := &pb.ListTasksResponse{}

	// Task shortcuts
	if s.settings != nil {
		for name, taskDef := range s.settings.Tasks {
			resp.Tasks = append(resp.Tasks, &pb.TaskInfo{
				Name:        name,
				Description: truncateStr(taskDef.Prompt, 100),
			})
		}
	}

	// Bundles
	names, err := bundle.List()
	if err == nil {
		for _, name := range names {
			b, err := bundle.Load(name)
			if err != nil {
				continue
			}
			resp.Bundles = append(resp.Bundles, &pb.BundleInfo{
				Name:        b.Name,
				Description: b.Description,
				StepCount:   int32(len(b.Steps)),
			})
		}
	}

	return resp, nil
}

// GetStatus returns server health and active run info.
func (s *Server) GetStatus(ctx context.Context, req *pb.GetStatusRequest) (*pb.GetStatusResponse, error) {
	resp := &pb.GetStatusResponse{
		Version:       runner.GetVersion(),
		ActiveRuns:    int32(s.registry.ActiveCount()),
		MaxConcurrent: int32(s.registry.MaxConcurrent()),
	}

	for _, run := range s.registry.List() {
		resp.Runs = append(resp.Runs, &pb.ActiveRun{
			RunId:       run.ID,
			Tool:        run.Tool,
			Task:        run.Task,
			StartedAtMs: run.StartedAt.UnixMilli(),
		})
	}

	return resp, nil
}

// CancelRun cancels a running task by its run ID.
func (s *Server) CancelRun(ctx context.Context, req *pb.CancelRunRequest) (*pb.CancelRunResponse, error) {
	if s.registry.Cancel(req.RunId) {
		return &pb.CancelRunResponse{Cancelled: true, Message: "run cancelled"}, nil
	}
	return &pb.CancelRunResponse{Cancelled: false, Message: "run not found"}, nil
}

// toInt32 safely converts interface{} to int32, handling both int and float64.
func toInt32(v interface{}) int32 {
	switch n := v.(type) {
	case int:
		return int32(n)
	case float64:
		return int32(n)
	case int64:
		return int32(n)
	default:
		return 0
	}
}

func truncateStr(s string, max int) string {
	if max < 4 {
		max = 4
	}
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
