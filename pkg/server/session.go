package server

import (
	"sync"
	"time"
)

// SessionEntry holds a stored session mapping.
type SessionEntry struct {
	ToolSessionID string
	Tool          string
	CreatedAt     time.Time
	LastUsed      time.Time
}

// SessionStore provides thread-safe in-memory session storage with TTL.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*SessionEntry
	ttl      time.Duration
	done     chan struct{}
}

// NewSessionStore creates a store that expires sessions after ttl.
// It starts a background goroutine that sweeps expired entries every ttl/2.
// Call Stop() to release the goroutine (important in tests).
func NewSessionStore(ttl time.Duration) *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*SessionEntry),
		ttl:      ttl,
		done:     make(chan struct{}),
	}
	go s.sweepLoop(ttl / 2)
	return s
}

// Stop shuts down the background sweep goroutine.
func (s *SessionStore) Stop() {
	select {
	case <-s.done:
		// already stopped
	default:
		close(s.done)
	}
}

// Store saves or updates a session mapping.
func (s *SessionStore) Store(sessionID, tool, toolSessionID string) {
	now := time.Now()
	s.mu.Lock()
	s.sessions[sessionID] = &SessionEntry{
		ToolSessionID: toolSessionID,
		Tool:          tool,
		CreatedAt:     now,
		LastUsed:      now,
	}
	s.mu.Unlock()
}

// Get retrieves a session entry, updating its last-used time.
// Returns false if not found or expired.
func (s *SessionStore) Get(sessionID string) (*SessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(entry.LastUsed) > s.ttl {
		delete(s.sessions, sessionID)
		return nil, false
	}
	entry.LastUsed = time.Now()
	return entry, true
}

// Delete removes a session.
func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

// sweepLoop periodically removes expired sessions until Stop() is called.
func (s *SessionStore) sweepLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for id, entry := range s.sessions {
				if now.Sub(entry.LastUsed) > s.ttl {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
