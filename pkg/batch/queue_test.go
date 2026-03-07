package batch

import (
	"sync"
	"testing"
)

func makeTestJobs(n int) []JobDef {
	names := []string{"job-alpha", "job-beta", "job-gamma", "job-delta"}
	jobs := make([]JobDef, n)
	for i := 0; i < n; i++ {
		jobs[i] = JobDef{
			Name: names[i%len(names)],
			Task: "echo hello",
			Tool: "rclaude",
			Dir:  "/tmp",
		}
	}
	return jobs
}

func TestQueueBasicFlow(t *testing.T) {
	jobs := makeTestJobs(2)
	q := NewQueue(jobs)

	// Initially all pending
	if q.PendingCount() != 2 {
		t.Fatalf("expected 2 pending, got %d", q.PendingCount())
	}
	if q.RunningCount() != 0 {
		t.Fatalf("expected 0 running, got %d", q.RunningCount())
	}
	if q.CompletedCount() != 0 {
		t.Fatalf("expected 0 completed, got %d", q.CompletedCount())
	}
	if q.FailedCount() != 0 {
		t.Fatalf("expected 0 failed, got %d", q.FailedCount())
	}

	// Next() returns first job
	j1, ok := q.Next()
	if !ok {
		t.Fatal("expected Next() to return a job")
	}
	if j1.Name != "job-alpha" {
		t.Fatalf("expected job-alpha, got %s", j1.Name)
	}
	if q.PendingCount() != 1 {
		t.Fatalf("expected 1 pending after first Next(), got %d", q.PendingCount())
	}
	if q.RunningCount() != 1 {
		t.Fatalf("expected 1 running after first Next(), got %d", q.RunningCount())
	}

	// Next() returns second job
	j2, ok := q.Next()
	if !ok {
		t.Fatal("expected Next() to return second job")
	}
	if j2.Name != "job-beta" {
		t.Fatalf("expected job-beta, got %s", j2.Name)
	}
	if q.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after second Next(), got %d", q.PendingCount())
	}
	if q.RunningCount() != 2 {
		t.Fatalf("expected 2 running after second Next(), got %d", q.RunningCount())
	}

	// No more jobs
	_, ok = q.Next()
	if ok {
		t.Fatal("expected Next() to return false when no pending jobs")
	}

	// Complete first job
	q.Complete(j1.Name, &JobResult{
		ExitCode:  0,
		Cost:      0.05,
		Duration:  "12s",
		SessionID: "sess-001",
	})
	if q.CompletedCount() != 1 {
		t.Fatalf("expected 1 completed, got %d", q.CompletedCount())
	}
	if q.RunningCount() != 1 {
		t.Fatalf("expected 1 running after complete, got %d", q.RunningCount())
	}

	// Complete second job
	q.Complete(j2.Name, &JobResult{
		ExitCode:  0,
		Cost:      0.10,
		Duration:  "30s",
		SessionID: "sess-002",
	})
	if q.CompletedCount() != 2 {
		t.Fatalf("expected 2 completed, got %d", q.CompletedCount())
	}
	if q.RunningCount() != 0 {
		t.Fatalf("expected 0 running after all complete, got %d", q.RunningCount())
	}
}

func TestQueueFail(t *testing.T) {
	jobs := makeTestJobs(2)
	q := NewQueue(jobs)

	j1, ok := q.Next()
	if !ok {
		t.Fatal("expected Next() to return a job")
	}

	q.Fail(j1.Name, &JobResult{
		ExitCode: 1,
		Error:    "something went wrong",
	})

	if q.FailedCount() != 1 {
		t.Fatalf("expected 1 failed, got %d", q.FailedCount())
	}
	if q.RunningCount() != 0 {
		t.Fatalf("expected 0 running after fail, got %d", q.RunningCount())
	}
	if q.PendingCount() != 1 {
		t.Fatalf("expected 1 still pending, got %d", q.PendingCount())
	}
}

func TestQueueEmpty(t *testing.T) {
	q := NewQueue(nil)

	_, ok := q.Next()
	if ok {
		t.Fatal("expected Next() to return false on empty queue")
	}

	if q.PendingCount() != 0 {
		t.Fatalf("expected 0 pending, got %d", q.PendingCount())
	}
	if q.RunningCount() != 0 {
		t.Fatalf("expected 0 running, got %d", q.RunningCount())
	}
	if q.CompletedCount() != 0 {
		t.Fatalf("expected 0 completed, got %d", q.CompletedCount())
	}
	if q.FailedCount() != 0 {
		t.Fatalf("expected 0 failed, got %d", q.FailedCount())
	}
}

func TestQueueSnapshot(t *testing.T) {
	jobs := makeTestJobs(3)
	q := NewQueue(jobs)

	// Take first two jobs
	j1, _ := q.Next()
	j2, _ := q.Next()

	// Complete first
	q.Complete(j1.Name, &JobResult{
		ExitCode:  0,
		Cost:      0.25,
		Duration:  "5s",
		SessionID: "sess-100",
	})

	// Fail second
	q.Fail(j2.Name, &JobResult{
		ExitCode: 1,
		Error:    "timeout exceeded",
	})

	// Third job is still pending
	snap := q.Snapshot()

	if len(snap.Completed) != 1 {
		t.Fatalf("expected 1 completed in snapshot, got %d", len(snap.Completed))
	}
	if snap.Completed[0].Name != "job-alpha" {
		t.Fatalf("expected completed job name job-alpha, got %s", snap.Completed[0].Name)
	}
	if snap.Completed[0].Cost != 0.25 {
		t.Fatalf("expected cost 0.25, got %f", snap.Completed[0].Cost)
	}
	if snap.Completed[0].Duration != "5s" {
		t.Fatalf("expected duration 5s, got %s", snap.Completed[0].Duration)
	}
	if snap.Completed[0].SessionID != "sess-100" {
		t.Fatalf("expected session sess-100, got %s", snap.Completed[0].SessionID)
	}

	if len(snap.Failed) != 1 {
		t.Fatalf("expected 1 failed in snapshot, got %d", len(snap.Failed))
	}
	if snap.Failed[0].Name != "job-beta" {
		t.Fatalf("expected failed job name job-beta, got %s", snap.Failed[0].Name)
	}
	if snap.Failed[0].Error != "timeout exceeded" {
		t.Fatalf("expected error 'timeout exceeded', got %s", snap.Failed[0].Error)
	}

	if len(snap.Pending) != 1 {
		t.Fatalf("expected 1 pending in snapshot, got %d", len(snap.Pending))
	}
	if snap.Pending[0].Name != "job-gamma" {
		t.Fatalf("expected pending job name job-gamma, got %s", snap.Pending[0].Name)
	}

	if snap.TotalCost != 0.25 {
		t.Fatalf("expected total cost 0.25, got %f", snap.TotalCost)
	}
}

func TestQueueSnapshotRunningGoesToPending(t *testing.T) {
	jobs := makeTestJobs(2)
	q := NewQueue(jobs)

	// Move one to running
	q.Next()

	snap := q.Snapshot()

	// Running jobs should appear as pending in snapshot
	if len(snap.Pending) != 2 {
		t.Fatalf("expected 2 pending in snapshot (1 pending + 1 running), got %d", len(snap.Pending))
	}
}

func TestQueueConcurrency(t *testing.T) {
	jobs := makeTestJobs(4)
	q := NewQueue(jobs)

	var wg sync.WaitGroup
	wg.Add(4)

	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			j, ok := q.Next()
			if ok {
				q.Complete(j.Name, &JobResult{Cost: 0.01})
			}
		}()
	}

	wg.Wait()

	total := q.CompletedCount() + q.PendingCount() + q.RunningCount() + q.FailedCount()
	if total != 4 {
		t.Fatalf("expected total of 4 jobs across all states, got %d", total)
	}
}
