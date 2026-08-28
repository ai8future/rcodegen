package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"rcodegen/pkg/batch"
)

func TestReorderSubcommandArgs(t *testing.T) {
	valueFlags := map[string]bool{
		"--concurrency": true,
		"--server":      true,
	}
	got := reorderSubcommandArgs([]string{
		"batch.json", "--concurrency", "4", "--dry-run", "--server=localhost:14260",
	}, valueFlags)
	want := []string{
		"--concurrency", "4", "--dry-run", "--server=localhost:14260", "batch.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered args = %#v, want %#v", got, want)
	}
}

func TestReorderSubcommandArgs_PreservesFlagTerminator(t *testing.T) {
	got := reorderSubcommandArgs(
		[]string{"batch.json", "--server", "localhost:14260", "--", "--literal"},
		map[string]bool{"--server": true},
	)
	want := []string{"--server", "localhost:14260", "--", "batch.json", "--literal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered args = %#v, want %#v", got, want)
	}
}

func TestPersistJobResultsConcurrently(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &batch.Manifest{Name: "persist-test"}
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two", "three"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			persistJobResult(m, name, &batch.JobResult{Output: name, ExitCode: 0})
		}()
	}
	wg.Wait()
	for _, name := range []string{"one", "two", "three"} {
		path := filepath.Join(os.Getenv("HOME"), ".rcodegen", "batches", "persist-test", "results", name+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result batch.JobResult
		if json.Unmarshal(data, &result) != nil || result.Output != name {
			t.Fatalf("result %s = %s", name, data)
		}
	}
}
