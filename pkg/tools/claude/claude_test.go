package claude

import (
	"sync"
	"testing"
)

func TestCheckClaudeMax_ThreadSafe(t *testing.T) {
	tool := New()

	// Run concurrent checks - should not race
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tool.IsClaudeMax()
		}()
	}
	wg.Wait()

	// If we get here without -race detecting issues, test passes
}

func TestNew_ReturnsNonNil(t *testing.T) {
	tool := New()
	if tool == nil {
		t.Error("New() returned nil")
	}
}
