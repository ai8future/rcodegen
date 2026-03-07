package batch

import (
	"crypto/rand"
	"fmt"
)

// SessionGroup represents a group of jobs that share a session
// and must run sequentially within the group.
type SessionGroup struct {
	ID      string
	Session string
	Jobs    []JobDef
}

// BuildSessionGroups partitions a flat list of jobs into session groups.
//
// Jobs that share the same non-empty Session field are collected into a single
// group, preserving their manifest order. Jobs with an empty Session field
// each become a standalone group of one.
//
// The returned slice is ordered: session groups first (in order of first
// appearance), then standalone groups (in manifest order).
func BuildSessionGroups(jobs []JobDef) []SessionGroup {
	type entry struct {
		idx   int
		group *SessionGroup
	}
	seen := map[string]*entry{}
	var sessionGroups []SessionGroup
	var standalones []SessionGroup

	for _, j := range jobs {
		if j.Session == "" {
			standalones = append(standalones, SessionGroup{
				ID:   generateGroupID(),
				Jobs: []JobDef{j},
			})
			continue
		}

		if e, ok := seen[j.Session]; ok {
			e.group.Jobs = append(e.group.Jobs, j)
		} else {
			g := SessionGroup{
				ID:      generateGroupID(),
				Session: j.Session,
				Jobs:    []JobDef{j},
			}
			seen[j.Session] = &entry{idx: len(sessionGroups), group: &g}
			sessionGroups = append(sessionGroups, g)
		}
	}

	// Sync pointer-based mutations back into the slice.
	for _, e := range seen {
		sessionGroups[e.idx] = *e.group
	}

	// Session groups first, then standalones.
	result := make([]SessionGroup, 0, len(sessionGroups)+len(standalones))
	result = append(result, sessionGroups...)
	result = append(result, standalones...)
	return result
}

func generateGroupID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("g-%08x", b)
}
