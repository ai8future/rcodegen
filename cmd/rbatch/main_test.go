package main

import (
	"reflect"
	"testing"
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
