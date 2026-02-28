package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"rcodegen/pkg/bundle"
	_ "rcodegen/pkg/executor" // Register dispatcher factory via init()
	"rcodegen/pkg/orchestrator"
	"rcodegen/pkg/runner"
	"rcodegen/pkg/server/pb"
	"rcodegen/pkg/settings"

	"github.com/ai8future/chassis-go/v5/logz"
)

// Server implements the RServe gRPC service.
type Server struct {
	pb.UnimplementedRServeServer
	settings *settings.Settings
	tools    map[string]runner.Tool
	registry *RunRegistry
}

// NewServer creates a new gRPC server instance.
func NewServer(s *settings.Settings, tools map[string]runner.Tool, registry *RunRegistry) *Server {
	return &Server{
		settings: s,
		tools:    tools,
		registry: registry,
	}
}

// RunTask executes a single tool task and streams events back.
func (s *Server) RunTask(req *pb.RunTaskRequest, stream pb.RServe_RunTaskServer) error {
	// Validate tool
	tool, ok := s.tools[req.Tool]
	if !ok {
		return fmt.Errorf("unknown tool: %s", req.Tool)
	}

	// Acquire a concurrency slot
	runID, runCtx, _, err := s.registry.Acquire(stream.Context(), req.Tool, req.Task)
	if err != nil {
		return fmt.Errorf("failed to acquire run slot: %w", err)
	}
	defer s.registry.Release(runID)

	// Send init event
	if err := stream.Send(&pb.RunEvent{
		RunId:       runID,
		TimestampMs: time.Now().UnixMilli(),
		Event:       &pb.RunEvent_Init{Init: &pb.InitEvent{Tool: req.Tool, Model: req.Model}},
	}); err != nil {
		return err
	}

	// Build config
	cfg := runner.NewConfig()
	cfg.Task = req.Task
	cfg.Model = req.Model
	cfg.MaxBudget = req.MaxBudget
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

	// Apply tool defaults
	if s.settings != nil {
		tool.ApplyToolDefaults(cfg)
	}

	// Wire up stream callback: convert each StreamEvent to a proto RunEvent
	cfg.OnStreamEvent = func(event *runner.StreamEvent) {
		protoEvent := streamEventToProto(runID, event)
		if protoEvent != nil {
			stream.Send(protoEvent)
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

	if err := stream.Send(&pb.RunEvent{
		RunId:       runID,
		TimestampMs: time.Now().UnixMilli(),
		Event:       &pb.RunEvent_Result{Result: resultEvent},
	}); err != nil {
		return err
	}

	return nil
}

// streamEventToProto converts a runner.StreamEvent into a proto RunEvent.
func streamEventToProto(runID string, event *runner.StreamEvent) *pb.RunEvent {
	base := &pb.RunEvent{
		RunId:       runID,
		TimestampMs: time.Now().UnixMilli(),
	}

	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			base.Event = &pb.RunEvent_Init{Init: &pb.InitEvent{}}
			return base
		}
		return nil

	case "assistant":
		if event.Message == nil {
			return nil
		}
		for _, block := range event.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					base.Event = &pb.RunEvent_Text{Text: &pb.TextEvent{Content: block.Text}}
					return base
				}
			case "tool_use":
				summary := ""
				if len(block.Input) > 0 {
					var inputMap map[string]interface{}
					if json.Unmarshal(block.Input, &inputMap) == nil {
						// Extract the most useful field for summary
						for _, key := range []string{"file_path", "command", "pattern", "description", "query"} {
							if v, ok := inputMap[key].(string); ok {
								summary = v
								break
							}
						}
					}
				}
				base.Event = &pb.RunEvent_ToolUse{ToolUse: &pb.ToolUseEvent{
					ToolName: block.Name,
					Summary:  summary,
				}}
				return base
			}
		}
		return nil

	case "result":
		// Result events are handled separately in RunTask after the run completes,
		// so we skip them here to avoid duplicates.
		return nil

	default:
		return nil
	}
}

// RunBundle executes a multi-step bundle and streams progress events.
func (s *Server) RunBundle(req *pb.RunBundleRequest, stream pb.RServe_RunBundleServer) error {
	// Acquire concurrency slot
	runID, runCtx, _, err := s.registry.Acquire(stream.Context(), "bundle", req.Bundle)
	if err != nil {
		return fmt.Errorf("failed to acquire run slot: %w", err)
	}
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
		return fmt.Errorf("bundle load failed: %w", err)
	}

	// Build inputs map
	inputs := make(map[string]string)
	for k, v := range req.Inputs {
		inputs[k] = v
	}

	// Create orchestrator
	orch := orchestrator.New(s.settings)
	orch.SetLiveMode(false) // No animated display for gRPC
	if req.OpusOnly {
		orch.SetOpusOnly(true)
	}
	if req.FlashOnly {
		orch.SetFlashOnly(true)
	}

	// Run the bundle (orchestrator handles context internally via signal)
	env, runErr := orch.Run(b, inputs)
	_ = runCtx // context propagation is handled internally by orchestrator

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
			inTok, _ := env.Result["input_tokens"].(int)
			outTok, _ := env.Result["output_tokens"].(int)
			cacheRead, _ := env.Result["cache_read_tokens"].(int)
			cacheWrite, _ := env.Result["cache_write_tokens"].(int)
			resultEvent.Usage = &pb.TokenUsage{
				InputTokens:        int32(inTok),
				OutputTokens:       int32(outTok),
				CacheReadTokens:    int32(cacheRead),
				CacheCreationTokens: int32(cacheWrite),
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

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
