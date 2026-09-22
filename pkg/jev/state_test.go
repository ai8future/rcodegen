package jev

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestScrubStateRemovesHomeInEveryForm(t *testing.T) {
	home := "/Users/someone"
	in := "plain " + home + "/repo/a.go\n" +
		"url file://" + home + "/x\n" +
		`json {"path":"` + home + `/y"}` + "\n" +
		`escaped {"p":"\/Users\/someone\/z"}` + "\n" +
		"bare user someone wrote it; someones is a different word\n" +
		"other /Users/other/z"
	out := ScrubState(in, home, "someone")
	if strings.Contains(out, home) || strings.Contains(out, `\/Users\/someone`) {
		t.Fatalf("home path survived: %q", out)
	}
	for _, want := range []string{"plain ~/repo/a.go", "url file://~/x", `"path":"~/y"`, `"p":"~\/z"`, "bare user ~user wrote it", "someones is a different word", "/Users/other/z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if ScrubState("no home here", "", "") != "no home here" {
		t.Fatal("empty home and user must be a no-op")
	}
	if ScrubState("a b", "/", "a") != "~user b" {
		t.Fatal("root home must not be replaced, username still masked")
	}
}

func TestCapStateKeepsHeadAndTailUnderLimit(t *testing.T) {
	head := "OBJECTIVE: build the thing\n"
	middle := strings.Repeat("noise line\n", 400)
	tail := "COMPLETION SUMMARY: done"
	in := head + middle + tail
	limit := 512
	out, truncated := CapState(in, limit)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(out) > limit {
		t.Fatalf("capped state is %d bytes, limit %d", len(out), limit)
	}
	if !strings.HasPrefix(out, head) {
		t.Fatalf("head lost: %q", out[:40])
	}
	if !strings.HasSuffix(out, tail) {
		t.Fatalf("tail lost: %q", out[len(out)-40:])
	}
	if !strings.Contains(out, "[state truncated") {
		t.Fatalf("missing marker: %q", out)
	}
	same, tr := CapState("short", 100)
	if tr || same != "short" {
		t.Fatalf("under-limit state changed: %q %v", same, tr)
	}
	tiny, tr := CapState(strings.Repeat("x", 300), 64)
	if !tr || len(tiny) > 64 {
		t.Fatalf("tiny limit not honored: %d bytes", len(tiny))
	}
}

func TestCapStateNeverSplitsMultibyteRunes(t *testing.T) {
	inputs := map[string]string{
		"two-byte":    strings.Repeat("é", 2000),
		"three-byte":  strings.Repeat("€", 2000),
		"four-byte":   strings.Repeat("😀", 2000),
		"mixed":       strings.Repeat("aé€😀", 800),
		"with-ascii":  "head:" + strings.Repeat("😀", 2000) + ":tail",
		"with-marker": strings.Repeat("résumé:", 1000),
	}
	for name, in := range inputs {
		for _, limit := range []int{512, 513, 514, 515, 1001, 100, 97} {
			out, truncated := CapState(in, limit)
			if !truncated || len(out) > limit {
				t.Fatalf("%s limit %d: truncated=%v len=%d", name, limit, truncated, len(out))
			}
			if !utf8.ValidString(out) {
				t.Fatalf("%s limit %d: output is not valid UTF-8", name, limit)
			}
			if strings.Contains(out, "�") {
				t.Fatalf("%s limit %d: replacement rune present", name, limit)
			}
		}
	}
	if got := cutRuneEnd("ok\xff"); got != "ok\xff" {
		t.Fatalf("cutRuneEnd removed a pre-existing invalid byte: %q", got)
	}
	if got := cutRuneEnd("ok\xf0\x9f"); got != "ok" {
		t.Fatalf("cutRuneEnd kept a partial four-byte rune: %q", got)
	}
	if got := cutRuneEnd("ok\xe2\x82"); got != "ok" {
		t.Fatalf("cutRuneEnd kept a partial three-byte rune: %q", got)
	}
}

func TestStateBudgetSubtractsLongestQuestion(t *testing.T) {
	small := map[string]Question{"q": {Type: TypeNoul, Instructions: "short"}}
	if got := StateBudget(small); got >= MaxStateBytes || got < MaxStateBytes-200 {
		t.Fatalf("budget for a small question = %d", got)
	}
	big := map[string]Question{
		"q":  {Type: TypeNoul, Instructions: "short"},
		"q2": {Type: TypeChoice, Instructions: strings.Repeat("i", 4000), Criteria: json.RawMessage(`{"a":"` + strings.Repeat("d", 10000) + `","b":"x"}`)},
	}
	if err := ValidateQuestions(big); err != nil {
		t.Fatalf("fixture must validate: %v", err)
	}
	got := StateBudget(big)
	want := MaxStateBytes - QuestionBytes(big["q2"])
	if got != want {
		t.Fatalf("budget = %d, want %d", got, want)
	}
	if got+QuestionBytes(big["q2"]) > MaxStateBytes {
		t.Fatalf("state plus longest question exceeds MaxStateBytes: %d", got+QuestionBytes(big["q2"]))
	}
	if got < MaxStateBytes-MaxQuestionBytes {
		t.Fatalf("validated questions must leave at least %d bytes, got %d", MaxStateBytes-MaxQuestionBytes, got)
	}
	if StateBudget(nil) != MaxStateBytes {
		t.Fatal("no questions should yield the full budget")
	}
	absurd := map[string]Question{"q": {Type: TypeNoul, Instructions: strings.Repeat("i", MaxStateBytes)}}
	if StateBudget(absurd) != 0 {
		t.Fatal("a question larger than the whole budget must leave zero state room")
	}
}
