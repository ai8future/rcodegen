package batch

import (
	"strings"
	"testing"
)

func TestBuildSessionGroups(t *testing.T) {
	jobs := []JobDef{
		{Name: "a", Session: "chain-1"},
		{Name: "b", Session: "chain-2"},
		{Name: "c", Session: "chain-1"},
		{Name: "d", Session: ""},
		{Name: "e", Session: ""},
	}

	groups := BuildSessionGroups(jobs)

	// 2 session groups (chain-1, chain-2) + 2 standalone = 4 groups
	if len(groups) != 4 {
		t.Fatalf("expected 4 groups, got %d", len(groups))
	}

	// First group should be chain-1 (first appearance) with 2 jobs in manifest order.
	g0 := groups[0]
	if g0.Session != "chain-1" {
		t.Errorf("groups[0].Session = %q, want %q", g0.Session, "chain-1")
	}
	if len(g0.Jobs) != 2 {
		t.Fatalf("groups[0] should have 2 jobs, got %d", len(g0.Jobs))
	}
	if g0.Jobs[0].Name != "a" || g0.Jobs[1].Name != "c" {
		t.Errorf("chain-1 jobs = [%s, %s], want [a, c]", g0.Jobs[0].Name, g0.Jobs[1].Name)
	}

	// Second group should be chain-2 with 1 job.
	g1 := groups[1]
	if g1.Session != "chain-2" {
		t.Errorf("groups[1].Session = %q, want %q", g1.Session, "chain-2")
	}
	if len(g1.Jobs) != 1 || g1.Jobs[0].Name != "b" {
		t.Errorf("groups[1] jobs unexpected: %+v", g1.Jobs)
	}

	// Groups 2 and 3 are standalones (d, e) — each has 1 job, empty session.
	for i, idx := range []int{2, 3} {
		g := groups[idx]
		if g.Session != "" {
			t.Errorf("groups[%d].Session = %q, want empty", idx, g.Session)
		}
		if len(g.Jobs) != 1 {
			t.Errorf("groups[%d] should have 1 job, got %d", idx, len(g.Jobs))
		}
		expectedName := []string{"d", "e"}[i]
		if g.Jobs[0].Name != expectedName {
			t.Errorf("groups[%d].Jobs[0].Name = %q, want %q", idx, g.Jobs[0].Name, expectedName)
		}
	}

	// Every group should have a valid ID with "g-" prefix and 8 hex chars.
	for i, g := range groups {
		if !strings.HasPrefix(g.ID, "g-") {
			t.Errorf("groups[%d].ID = %q, missing 'g-' prefix", i, g.ID)
		}
		hexPart := strings.TrimPrefix(g.ID, "g-")
		if len(hexPart) != 8 {
			t.Errorf("groups[%d].ID hex part length = %d, want 8", i, len(hexPart))
		}
	}
}

func TestBuildSessionGroupsPreservesOrder(t *testing.T) {
	jobs := []JobDef{
		{Name: "z-last"},
		{Name: "a-first"},
		{Name: "m-middle"},
	}
	// All three share the same session.
	for i := range jobs {
		jobs[i].Session = "shared"
	}

	groups := BuildSessionGroups(jobs)

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}

	g := groups[0]
	if len(g.Jobs) != 3 {
		t.Fatalf("expected 3 jobs in group, got %d", len(g.Jobs))
	}

	// Order must match manifest order, NOT alphabetical.
	want := []string{"z-last", "a-first", "m-middle"}
	for i, w := range want {
		if g.Jobs[i].Name != w {
			t.Errorf("jobs[%d].Name = %q, want %q", i, g.Jobs[i].Name, w)
		}
	}
}
