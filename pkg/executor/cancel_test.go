package executor

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestRunWithContext_KillsProcessOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.Command("sleep", "10")
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := runWithContext(ctx, cmd)
	elapsed := time.Since(start)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("returned after %v — process was not killed", elapsed)
	}
}

func TestRunWithContext_NilContextRuns(t *testing.T) {
	if err := runWithContext(nil, exec.Command("true")); err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestRunWithContext_CompletesNormally(t *testing.T) {
	if err := runWithContext(context.Background(), exec.Command("true")); err != nil {
		t.Fatalf("err: %v", err)
	}
}
