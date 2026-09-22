package jev

import (
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxStateBytes caps state plus the longest question before anything leaves
// the machine.
const MaxStateBytes = 64 << 10

const truncationMarker = "\n\n[state truncated: kept head and tail]\n\n"

// LocalIdentity returns the home directory and account name to scrub.
func LocalIdentity() (home, username string) {
	home, _ = os.UserHomeDir()
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = filepath.Base(u.Username)
	} else if home != "" {
		username = filepath.Base(home)
	}
	return home, username
}

// ScrubState masks the home directory path and the whole-word account name.
func ScrubState(state, home, username string) string {
	home = strings.TrimRight(home, "/")
	if home != "" && home != "/" {
		state = strings.ReplaceAll(state, home, "~")
		escaped := strings.ReplaceAll(home, "/", `\/`)
		state = strings.ReplaceAll(state, escaped, "~")
	}
	username = strings.TrimSpace(username)
	if username != "" && username != "/" && username != "~" {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(username) + `\b`)
		state = re.ReplaceAllString(state, "~user")
	}
	return state
}

// StateBudget is how much state fits alongside the questions.
func StateBudget(qs map[string]Question) int {
	longest := 0
	for _, q := range qs {
		if n := QuestionBytes(q); n > longest {
			longest = n
		}
	}
	if longest >= MaxStateBytes {
		return 0
	}
	return MaxStateBytes - longest
}

// CapState keeps the head and tail of an oversized state without splitting UTF-8 runes.
func CapState(state string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(state) <= maxBytes {
		return state, false
	}
	if maxBytes <= len(truncationMarker) {
		return cutRuneEnd(state[:maxBytes]), true
	}
	remaining := maxBytes - len(truncationMarker)
	headBytes := remaining / 2
	tailBytes := remaining - headBytes

	head := cutRuneEnd(state[:headBytes])
	tail := cutRuneStart(state[len(state)-tailBytes:])
	return head + truncationMarker + tail, true
}

func cutRuneEnd(s string) string {
	if len(s) == 0 || utf8.ValidString(s) {
		return s
	}
	i := len(s) - 1
	for i >= 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	if i < 0 {
		return ""
	}
	want := utf8SequenceLen(s[i])
	if want == 0 {
		return s
	}
	have := len(s) - i
	if have < want {
		return s[:i]
	}
	return s
}

func cutRuneStart(s string) string {
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r != utf8.RuneError || size != 1 || utf8.ValidString(s) {
			return s
		}
		s = s[1:]
	}
	return s
}

func utf8SequenceLen(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}
