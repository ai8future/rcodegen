package server

import (
	"testing"
	"time"
)

func TestSessionStore_StoreAndGet(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()

	store.Store("sess-1", "claude", "tool-sess-abc")

	entry, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("expected to find session")
	}
	if entry.ToolSessionID != "tool-sess-abc" {
		t.Errorf("expected tool session 'tool-sess-abc', got %q", entry.ToolSessionID)
	}
	if entry.Tool != "claude" {
		t.Errorf("expected tool 'claude', got %q", entry.Tool)
	}
}

func TestSessionStore_GetMissing(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()

	_, ok := store.Get("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent session")
	}
}

func TestSessionStore_Expiry(t *testing.T) {
	store := NewSessionStore(50 * time.Millisecond)
	defer store.Stop()
	store.Store("sess-1", "claude", "tool-sess-abc")

	// Should exist immediately
	if _, ok := store.Get("sess-1"); !ok {
		t.Fatal("expected session to exist before expiry")
	}

	time.Sleep(100 * time.Millisecond)

	// Should be gone after TTL
	if _, ok := store.Get("sess-1"); ok {
		t.Error("expected session to be expired")
	}
}

func TestSessionStore_Update(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()

	store.Store("sess-1", "claude", "tool-sess-v1")
	store.Store("sess-1", "claude", "tool-sess-v2")

	entry, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("expected to find session")
	}
	if entry.ToolSessionID != "tool-sess-v2" {
		t.Errorf("expected updated tool session, got %q", entry.ToolSessionID)
	}
}

func TestSessionStore_Delete(t *testing.T) {
	store := NewSessionStore(10 * time.Minute)
	defer store.Stop()
	store.Store("sess-1", "claude", "tool-sess-abc")

	store.Delete("sess-1")

	if _, ok := store.Get("sess-1"); ok {
		t.Error("expected session to be deleted")
	}
}
