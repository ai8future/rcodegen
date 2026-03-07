package batch

import (
	"context"
	"fmt"
	"io"
	"time"

	"rcodegen/pkg/server/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// RemoteExecutor runs jobs by delegating to a remote rserve instance via gRPC.
type RemoteExecutor struct {
	Addr string
	conn *grpc.ClientConn
}

// NewRemoteExecutor creates a gRPC client connection to the rserve at addr.
func NewRemoteExecutor(addr string) (*RemoteExecutor, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connecting to rserve at %s: %w", addr, err)
	}
	return &RemoteExecutor{Addr: addr, conn: conn}, nil
}

// Close closes the underlying gRPC connection.
func (r *RemoteExecutor) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// Execute runs a single job on the remote rserve and returns the result.
func (r *RemoteExecutor) Execute(ctx context.Context, job *JobDef, sessionID string) (*JobResult, error) {
	client := pb.NewRServeClient(r.conn)

	var workDirs []string
	if job.Dir != "" {
		workDirs = []string{job.Dir}
	}

	req := &pb.RunTaskRequest{
		Tool:      job.Tool,
		Task:      job.Task,
		Model:     job.Model,
		MaxBudget: job.MaxBudget,
		WorkDirs:  workDirs,
	}

	start := time.Now()

	stream, err := client.RunTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("RunTask RPC: %w", err)
	}

	var cost float64
	var exitCode int32

	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("receiving RunTask event: %w", err)
		}

		if result := event.GetResult(); result != nil {
			cost = result.GetTotalCostUsd()
			exitCode = result.GetExitCode()
		}
	}

	elapsed := time.Since(start)

	return &JobResult{
		ExitCode:  int(exitCode),
		Cost:      cost,
		Duration:  elapsed.Truncate(time.Millisecond).String(),
		SessionID: sessionID,
	}, nil
}

// CheckBudget is not supported via remote execution; returns -1.
func (r *RemoteExecutor) CheckBudget(ctx context.Context) (int, error) {
	return -1, nil
}
