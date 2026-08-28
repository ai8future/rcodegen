package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"rcodegen/pkg/runner"
	"rcodegen/pkg/server"
	"rcodegen/pkg/tools/opencode"

	chassis "github.com/ai8future/chassis-go/v11"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// stageClone builds a clone-shaped scratch tree and the workDirClone that names
// it, so a collector can be pointed at it without running a real copy.
func stageClone(t *testing.T, names ...string) *workDirClone {
	t.Helper()
	clone := &workDirClone{root: t.TempDir()}
	for _, name := range names {
		dir := filepath.Join(clone.root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		clone.dirs = append(clone.dirs, dir)
	}
	return clone
}

// putFile writes rel under dir, creating parents.
func putFile(t *testing.T, dir, rel string, body []byte) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return full
}

// touchLater moves a file's mtime forward, so "modified in place, same size" is
// a deterministic condition rather than a race with the clock's resolution.
func touchLater(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	later := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// collectFrom takes the manifest, runs write against the clone, and returns
// what collection makes of it — the whole lifecycle a run goes through.
func collectFrom(t *testing.T, clone *workDirClone, write func()) ([]Artifact, []ArtifactSkipped) {
	t.Helper()
	c := newArtifactCollector(clone, quietLogger())
	defer c.close()
	if write != nil {
		write()
	}
	return c.collect()
}

// artifactPaths lists artifact paths in order, for a single comparison against
// what a run was expected to produce.
func artifactPaths(artifacts []Artifact) []string {
	out := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, a.Path)
	}
	return out
}

// skipReason returns the reason reported for a path, or "" when it was not
// skipped at all.
func skipReason(skipped []ArtifactSkipped, path string) string {
	for _, s := range skipped {
		if s.Path == path {
			return s.Reason
		}
	}
	return ""
}

func wantPaths(t *testing.T, got []Artifact, want ...string) {
	t.Helper()
	if paths := artifactPaths(got); !equalStrings(paths, want) {
		t.Errorf("artifact paths = %v, want %v", paths, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// contentOf returns one artifact's content.
func contentOf(t *testing.T, artifacts []Artifact, path string) string {
	t.Helper()
	for _, a := range artifacts {
		if a.Path == path {
			return a.Content
		}
	}
	t.Fatalf("no artifact %q in %v", path, artifactPaths(artifacts))
	return ""
}

// installFakeOpenCodeWriting installs a fake CLI that finds the --dir argument
// rserve pointed it at — its clone — runs body with $dir bound to it, and then
// prints output. It is how a test makes an agent "write the report".
func installFakeOpenCodeWriting(t *testing.T, body, output string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"dir=.\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--dir\" ]; then dir=\"$2\"; fi\n" +
		"  shift\n" +
		"done\n" +
		body + "\n" +
		"printf '%s' '" + output + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// artifactHandler builds a handler over a fake opencode with the given slot
// count.
func artifactHandler(slots int) *Handler {
	return NewHandler(nil, map[string]server.ToolFactory{
		"opencode": func() runner.Tool { return opencode.New() },
	}, server.NewRunRegistry(slots), []string{"opencode"}, nil, nil)
}

// chatBody builds a chat completion request body with the given extra fields.
func chatBody(extra string) string {
	body := `{"model":"opencode","messages":[{"role":"user","content":"hello"}]`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

// postChat sends a chat completion and decodes the completion it answers with.
func postChat(t *testing.T, h *Handler, body string) ChatCompletionResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// The created / modified / untouched matrix
// ---------------------------------------------------------------------------

// The diff is against the clone as it stood when the CLI started, so what comes
// back is what this run wrote — and nothing the source tree already held.
func TestArtifacts_CreatedAndModifiedOnly(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]
	putFile(t, dir, "untouched.txt", []byte("as it was"))
	putFile(t, dir, "grown.txt", []byte("v1"))
	inPlace := putFile(t, dir, "in-place.txt", []byte("aaaa"))
	putFile(t, dir, "deleted.txt", []byte("goodbye"))

	artifacts, skipped := collectFrom(t, clone, func() {
		putFile(t, dir, "created.txt", []byte("brand new"))
		putFile(t, dir, "grown.txt", []byte("v1 plus more"))
		// Same size, later mtime: a rewrite in place is still a modification.
		putFile(t, dir, "in-place.txt", []byte("bbbb"))
		touchLater(t, inPlace)
		if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
			t.Fatalf("remove deleted.txt: %v", err)
		}
	})

	wantPaths(t, artifacts, "created.txt", "grown.txt", "in-place.txt")
	if len(skipped) != 0 {
		t.Errorf("artifacts_skipped = %v, want none", skipped)
	}
	if got := contentOf(t, artifacts, "created.txt"); got != "brand new" {
		t.Errorf("created.txt content = %q, want %q", got, "brand new")
	}
	for _, a := range artifacts {
		if a.Bytes != int64(len(a.Content)) {
			t.Errorf("%s bytes = %d, content is %d bytes", a.Path, a.Bytes, len(a.Content))
		}
	}
}

// A run that writes nothing reports nothing, rather than the tree it was given.
func TestArtifacts_UntouchedCloneYieldsNothing(t *testing.T) {
	clone := stageClone(t, "repo")
	putFile(t, clone.dirs[0], "README.md", []byte("# existing"))

	artifacts, skipped := collectFrom(t, clone, nil)
	if len(artifacts) != 0 || len(skipped) != 0 {
		t.Errorf("artifacts = %v, skipped = %v; want both empty", artifactPaths(artifacts), skipped)
	}
}

// Nested output is reported at its path under the clone. Hidden entries are
// not: a clone's .git index and a tool's own dot-directory churn on every run.
func TestArtifacts_NestedPathsAndHiddenEntries(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	artifacts, _ := collectFrom(t, clone, func() {
		putFile(t, dir, filepath.Join("docs", "reports", "final.md"), []byte("# final"))
		putFile(t, dir, filepath.Join(".git", "index"), []byte("gitstate"))
		putFile(t, dir, ".env", []byte("SECRET=2"))
	})

	wantPaths(t, artifacts, "docs/reports/final.md")
}

// With more than one work_dir a bare relative path would be ambiguous, so each
// artifact says which clone it came from.
func TestArtifacts_MultipleWorkDirsArePrefixed(t *testing.T) {
	clone := stageClone(t, "alpha", "beta")

	artifacts, _ := collectFrom(t, clone, func() {
		putFile(t, clone.dirs[0], "notes.md", []byte("from alpha"))
		putFile(t, clone.dirs[1], "notes.md", []byte("from beta"))
	})

	wantPaths(t, artifacts, "alpha/notes.md", "beta/notes.md")
	if got := contentOf(t, artifacts, "beta/notes.md"); got != "from beta" {
		t.Errorf("beta/notes.md content = %q, want %q", got, "from beta")
	}
}

// ---------------------------------------------------------------------------
// Caps and skip reporting
// ---------------------------------------------------------------------------

func TestArtifacts_BinaryFilesAreSkippedByName(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	artifacts, skipped := collectFrom(t, clone, func() {
		putFile(t, dir, "report.md", []byte("# readable"))
		putFile(t, dir, "image.png", []byte{0x89, 'P', 'N', 'G', 0x00, 0x1a, 0x0a})
		putFile(t, dir, "invalid.txt", []byte{0xff, 0xfe, 0xfd, 0xfc})
	})

	wantPaths(t, artifacts, "report.md")
	for _, path := range []string{"image.png", "invalid.txt"} {
		if got := skipReason(skipped, path); got != artifactSkipBinary {
			t.Errorf("%s skipped as %q, want %q", path, got, artifactSkipBinary)
		}
	}
}

func TestArtifacts_OversizeFileIsSkippedNotTruncated(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	artifacts, skipped := collectFrom(t, clone, func() {
		putFile(t, dir, "huge.log", []byte(strings.Repeat("x", artifactFileCap+1)))
		putFile(t, dir, "at-the-cap.log", []byte(strings.Repeat("y", artifactFileCap)))
	})

	// The file exactly at the cap is returned whole; one byte more is refused
	// rather than cut, so an artifact that arrives is always complete.
	wantPaths(t, artifacts, "at-the-cap.log")
	if got := len(contentOf(t, artifacts, "at-the-cap.log")); got != artifactFileCap {
		t.Errorf("at-the-cap.log content = %d bytes, want %d", got, artifactFileCap)
	}
	if got := skipReason(skipped, "huge.log"); got != artifactSkipOversize {
		t.Errorf("huge.log skipped as %q, want %q", got, artifactSkipOversize)
	}
}

func TestArtifacts_FileCountCapIsEnforced(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	artifacts, skipped := collectFrom(t, clone, func() {
		for i := 0; i < artifactMaxCount+3; i++ {
			putFile(t, dir, fmt.Sprintf("note-%03d.txt", i), []byte("x"))
		}
	})

	if len(artifacts) != artifactMaxCount {
		t.Fatalf("returned %d artifacts, want the cap of %d", len(artifacts), artifactMaxCount)
	}
	// Paths are sorted, so the cap falls in a predictable place.
	if last := artifacts[len(artifacts)-1].Path; last != fmt.Sprintf("note-%03d.txt", artifactMaxCount-1) {
		t.Errorf("last artifact = %s, want the %dth by path", last, artifactMaxCount)
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped %d files, want 3", len(skipped))
	}
	for _, s := range skipped {
		if s.Reason != artifactSkipTooManyFiles {
			t.Errorf("%s skipped as %q, want %q", s.Path, s.Reason, artifactSkipTooManyFiles)
		}
	}
}

func TestArtifacts_ResponseSizeCapIsEnforced(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]
	const each = 500 << 10

	artifacts, skipped := collectFrom(t, clone, func() {
		for i := 0; i < 5; i++ {
			putFile(t, dir, fmt.Sprintf("chunk-%d.txt", i), []byte(strings.Repeat("z", each)))
		}
	})

	total := 0
	for _, a := range artifacts {
		total += len(a.Content)
	}
	if total > artifactTotalCap {
		t.Errorf("returned %d bytes of content, over the %d cap", total, artifactTotalCap)
	}
	if len(artifacts) != artifactTotalCap/each {
		t.Errorf("returned %d artifacts, want %d before the budget ran out", len(artifacts), artifactTotalCap/each)
	}
	if got := skipReason(skipped, "chunk-4.txt"); got != artifactSkipResponseCap {
		t.Errorf("chunk-4.txt skipped as %q, want %q", got, artifactSkipResponseCap)
	}
}

// The skip report is a report, not a directory listing: it cannot grow the
// response without limit however many files a run leaves behind.
func TestArtifacts_SkipReportIsBounded(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	_, skipped := collectFrom(t, clone, func() {
		for i := 0; i < artifactSkipCap+25; i++ {
			putFile(t, dir, fmt.Sprintf("blob-%03d.bin", i), []byte{0x00, 0x01})
		}
	})

	if len(skipped) != artifactSkipCap {
		t.Errorf("skip report holds %d entries, want the cap of %d", len(skipped), artifactSkipCap)
	}
}

// ---------------------------------------------------------------------------
// Inspection budgets
// ---------------------------------------------------------------------------

// shrinkInspectionBudget makes the inspection bounds small enough to reach
// without building a tree the size of the ones they exist for.
func shrinkInspectionBudget(t *testing.T, candidates int, readBytes int64) {
	t.Helper()
	oldCandidates, oldBytes := artifactMaxCandidates, artifactMaxReadBytes
	artifactMaxCandidates, artifactMaxReadBytes = candidates, readBytes
	t.Cleanup(func() {
		artifactMaxCandidates, artifactMaxReadBytes = oldCandidates, oldBytes
	})
}

// hardLinkFanout writes one file and points n names at the same data blocks —
// the shape that turns a small clone into an enormous amount of reading. The
// storage cost is one copy; the cost of reading each name in full is n copies.
func hardLinkFanout(t *testing.T, dir, name string, body []byte, n int) {
	t.Helper()
	first := putFile(t, dir, name+"-000", body)
	for i := 1; i < n; i++ {
		link := filepath.Join(dir, fmt.Sprintf("%s-%03d", name, i))
		if err := os.Link(first, link); err != nil {
			t.Fatalf("hard link %d: %v", i, err)
		}
	}
}

// The response caps bound what a caller receives; they do not bound what
// producing it costs. A run that leaves 200,000 hard links to one binary file
// used to be read in full, once per name, because a binary consumes neither the
// artifact count nor the content budget — roughly 100GB of reading for a clone
// holding 512KB. Collection holds a run slot while it does that.
//
// Inspection is therefore bounded on its own terms: a candidate count and a
// read-byte budget, charged before the read rather than after it.
func TestArtifacts_InspectionBudgetBoundsReadAmplification(t *testing.T) {
	const (
		links    = 300
		fileSize = 64 << 10
	)
	shrinkInspectionBudget(t, 25, 512<<10)

	clone := stageClone(t, "repo")
	dir := clone.dirs[0]
	binary := append([]byte{0x00, 0x01, 0x02}, make([]byte, fileSize-3)...)

	c := newArtifactCollector(clone, quietLogger())
	defer c.close()
	hardLinkFanout(t, dir, "blob", binary, links)
	artifacts, skipped := c.collect()

	t.Logf("inspected %d candidates and read %d bytes across %d hard links of %d bytes "+
		"(budgets: %d candidates, %d bytes)",
		c.inspected, c.bytesRead, links, fileSize, artifactMaxCandidates, artifactMaxReadBytes)

	if c.inspected > artifactMaxCandidates {
		t.Errorf("inspected %d candidates, over the budget of %d", c.inspected, artifactMaxCandidates)
	}
	if c.bytesRead > artifactMaxReadBytes {
		t.Errorf("read %d bytes, over the budget of %d", c.bytesRead, artifactMaxReadBytes)
	}
	// A binary candidate costs the text probe, not the whole file: classifying
	// 25 of these must not cost 25 × 64KB.
	if want := int64(artifactMaxCandidates) * artifactTextProbe; c.bytesRead > want {
		t.Errorf("read %d bytes classifying binaries, want at most the probe per candidate (%d)",
			c.bytesRead, want)
	}
	if len(artifacts) != 0 {
		t.Errorf("binary hard links returned %d artifacts", len(artifacts))
	}
	// Nothing vanishes silently: every candidate is named, up to the skip cap.
	if len(skipped) == 0 {
		t.Error("no skips were reported for a clone full of unreturnable files")
	}
}

// A budget that hid an artifact without saying so would be worse than no budget:
// the caller would read "the agent wrote nothing". A text file behind a wall of
// binaries is either returned or explicitly named as capped.
func TestArtifacts_InspectionCapIsReportedNotSilent(t *testing.T) {
	shrinkInspectionBudget(t, 10, 1<<20)

	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	c := newArtifactCollector(clone, quietLogger())
	defer c.close()
	// Paths are collected in sorted order, so "z-report.md" is behind every one
	// of these.
	hardLinkFanout(t, dir, "blob", []byte{0x00, 0x01, 0x02, 0x03}, 30)
	putFile(t, dir, "z-report.md", []byte("# the one file worth having"))
	artifacts, skipped := c.collect()

	switch {
	case len(artifacts) == 1 && artifacts[0].Path == "z-report.md":
		// Returned within budget: fine.
	case skipReason(skipped, "z-report.md") == artifactSkipInspectionCap:
		// Named as capped: also fine.
	default:
		t.Errorf("z-report.md was neither returned nor reported as %s: artifacts = %v, skipped = %v",
			artifactSkipInspectionCap, artifactPaths(artifacts), skipped)
	}
	if c.inspected > artifactMaxCandidates {
		t.Errorf("inspected %d candidates, over the budget of %d", c.inspected, artifactMaxCandidates)
	}
}

// The response caps are unchanged by the inspection budgets: a text file within
// them still comes back whole.
func TestArtifacts_InspectionBudgetLeavesOrdinaryRunsAlone(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	c := newArtifactCollector(clone, quietLogger())
	defer c.close()
	putFile(t, dir, "report.md", []byte("# digest"))
	putFile(t, dir, "notes.txt", []byte("some notes"))
	artifacts, skipped := c.collect()

	wantPaths(t, artifacts, "notes.txt", "report.md")
	if len(skipped) != 0 {
		t.Errorf("artifacts_skipped = %v, want none", skipped)
	}
	if c.bytesRead > int64(len("# digest")+len("some notes")+2*artifactTextProbe) {
		t.Errorf("read %d bytes for two small files", c.bytesRead)
	}
}

// A candidate that was a regular file when the clone was scanned and is a FIFO
// by the time it is opened must not turn collection into a wait for a writer
// that never comes — the run that made it a FIFO is the one deciding whether a
// writer ever appears, and collection holds the run slot meanwhile.
//
// The open is where that is caught, so this tests the open: the window between
// the after-scan and the read cannot be staged deterministically from outside.
func TestOpenArtifact_RefusesANonRegularFileWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("ordinary"), 0o644); err != nil {
		t.Fatalf("write plain.txt: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	done := make(chan error, 1)
	go func() {
		f, _, err := openArtifact(root, "pipe")
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errArtifactNotRegular) {
			t.Errorf("opening a FIFO = %v, want %v", err, errArtifactNotRegular)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("opening a FIFO blocked waiting for a writer")
	}

	// An ordinary file still opens, with the size the read budget is charged on.
	f, size, err := openArtifact(root, "plain.txt")
	if err != nil {
		t.Fatalf("opening a regular file: %v", err)
	}
	defer f.Close()
	if size != int64(len("ordinary")) {
		t.Errorf("size = %d, want %d", size, len("ordinary"))
	}
}

// And a clone holding a FIFO is collected without incident: it is not an
// artifact, and everything else the run wrote still comes back.
func TestArtifacts_FIFOInTheCloneIsNotAnArtifact(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	c := newArtifactCollector(clone, quietLogger())
	defer c.close()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	t.Cleanup(func() { os.Remove(fifo) })
	putFile(t, dir, "report.md", []byte("# still returned"))

	done := make(chan struct{})
	var artifacts []Artifact
	go func() {
		defer close(done)
		artifacts, _ = c.collect()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("collection blocked on a clone holding a FIFO")
	}
	wantPaths(t, artifacts, "report.md")
}

// ---------------------------------------------------------------------------
// Collection failures never fail the run
// ---------------------------------------------------------------------------

func TestArtifacts_UnreadableFileIsReportedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not deny reads")
	}
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	artifacts, skipped := collectFrom(t, clone, func() {
		putFile(t, dir, "readable.md", []byte("fine"))
		locked := putFile(t, dir, "locked.md", []byte("nope"))
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatalf("chmod locked.md: %v", err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o644) })
	})

	wantPaths(t, artifacts, "readable.md")
	if got := skipReason(skipped, "locked.md"); got != artifactSkipCollectionError {
		t.Errorf("locked.md skipped as %q, want %q", got, artifactSkipCollectionError)
	}
}

// A clone that cannot be opened at all is reported as one skipped entry naming
// the clone, and the run still answers.
func TestArtifacts_UnopenableCloneIsReported(t *testing.T) {
	clone := stageClone(t, "repo")
	clone.dirs[0] = filepath.Join(clone.root, "was-never-here")

	artifacts, skipped := collectFrom(t, clone, nil)

	if len(artifacts) != 0 {
		t.Errorf("artifacts = %v, want none", artifactPaths(artifacts))
	}
	if got := skipReason(skipped, "."); got != artifactSkipCollectionError {
		t.Errorf("clone skipped as %q, want %q", got, artifactSkipCollectionError)
	}
}

// A tree too big to walk is refused outright: a partial manifest would turn
// untouched files into invented artifacts, which is worse than saying no.
func TestArtifacts_ScanLimitIsReported(t *testing.T) {
	original := cloneScanMaxEntries
	t.Cleanup(func() { cloneScanMaxEntries = original })

	t.Run("at manifest time", func(t *testing.T) {
		clone := stageClone(t, "repo")
		for i := 0; i < 8; i++ {
			putFile(t, clone.dirs[0], fmt.Sprintf("seed-%d.txt", i), []byte("x"))
		}
		cloneScanMaxEntries = 3

		_, skipped := collectFrom(t, clone, nil)
		if got := skipReason(skipped, "."); got != artifactSkipScanLimit {
			t.Errorf("clone skipped as %q, want %q", got, artifactSkipScanLimit)
		}
	})

	t.Run("after the run grew the tree", func(t *testing.T) {
		clone := stageClone(t, "repo")
		cloneScanMaxEntries = 6
		putFile(t, clone.dirs[0], "seed.txt", []byte("x"))

		_, skipped := collectFrom(t, clone, func() {
			for i := 0; i < 20; i++ {
				putFile(t, clone.dirs[0], fmt.Sprintf("out-%d.txt", i), []byte("x"))
			}
		})
		if got := skipReason(skipped, "."); got != artifactSkipScanLimit {
			t.Errorf("clone skipped as %q, want %q", got, artifactSkipScanLimit)
		}
	})
}

// The async path asks for the artifacts twice — once for the completion, once
// for the failure payload that replaces it — and must not walk the clone again
// or answer differently the second time.
func TestArtifacts_CollectIsMemoized(t *testing.T) {
	clone := stageClone(t, "repo")
	dir := clone.dirs[0]

	c := newArtifactCollector(clone, quietLogger())
	defer c.close()
	putFile(t, dir, "first.md", []byte("one"))

	first, _ := c.collect()
	putFile(t, dir, "second.md", []byte("two"))
	second, _ := c.collect()

	wantPaths(t, first, "first.md")
	if !equalStrings(artifactPaths(first), artifactPaths(second)) {
		t.Errorf("second collect = %v, want the memoized %v", artifactPaths(second), artifactPaths(first))
	}
}

// A run that did not ask for artifacts carries no collector, and every call
// site goes through it unguarded.
func TestArtifacts_NilCollectorAnswersNothing(t *testing.T) {
	var c *artifactCollector
	artifacts, skipped := c.collect()
	if artifacts != nil || skipped != nil {
		t.Errorf("nil collector returned %v / %v, want nothing", artifacts, skipped)
	}
	c.close()

	if got := newArtifactCollector(nil, quietLogger()); got != nil {
		t.Error("a nil clone produced a collector")
	}
	if got := newArtifactCollector(&workDirClone{}, quietLogger()); got != nil {
		t.Error("a clone with no directories produced a collector")
	}
}

func TestLooksTextual(t *testing.T) {
	straddling := strings.Repeat("a", artifactTextProbe-1) + "é" + strings.Repeat("b", 64)
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", nil, true},
		{"plain text", []byte("# report\n"), true},
		{"utf-8 text", []byte("héllo — wörld"), true},
		{"embedded NUL", []byte("head\x00tail"), false},
		{"invalid utf-8", []byte{0xff, 0xfe}, false},
		{"rune cut by the probe boundary", []byte(straddling), true},
		{"binary past the probe", append([]byte(strings.Repeat("a", artifactTextProbe)), 0x00), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksTextual(tc.data); got != tc.want {
				t.Errorf("looksTextual = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Request contract
// ---------------------------------------------------------------------------

func TestChatCompletions_ReturnArtifactsRequiresAClone(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCode(t, "unused")
	src := t.TempDir()
	h := artifactHandler(1)

	cases := []struct {
		name string
		body string
	}{
		{"no clone asked for", chatBody(`"work_dirs":["` + src + `"],"return_artifacts":true`)},
		{"clone asked for with no work_dirs", chatBody(`"clone_work_dirs":true,"return_artifacts":true`)},
		{"neither", chatBody(`"return_artifacts":true`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			var resp ErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if resp.Error.Code != codeArtifactsRequireClone {
				t.Errorf("code = %q, want %q", resp.Error.Code, codeArtifactsRequireClone)
			}
			if resp.Error.Retryable {
				t.Error("retryable = true; a request that contradicts itself never becomes valid")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// End to end: synchronous, streaming, failed, concurrent
// ---------------------------------------------------------------------------

func TestChatCompletions_ReturnsWhatTheRunWroteInItsClone(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeWriting(t,
		`mkdir -p "$dir/out"; printf '# digest' > "$dir/out/report.md"; printf '\0\0' > "$dir/blob.bin"`,
		"wrote the report")
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "input.txt"), []byte("source"), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	h := artifactHandler(1)

	resp := postChat(t, h, chatBody(
		`"work_dirs":["`+src+`"],"clone_work_dirs":true,"return_artifacts":true`))

	wantPaths(t, resp.Artifacts, "out/report.md")
	if got := contentOf(t, resp.Artifacts, "out/report.md"); got != "# digest" {
		t.Errorf("report content = %q, want %q", got, "# digest")
	}
	if got := skipReason(resp.ArtifactsSkipped, "blob.bin"); got != artifactSkipBinary {
		t.Errorf("blob.bin skipped as %q, want %q", got, artifactSkipBinary)
	}
	// The clone is gone; the artifact is the only copy that survived it.
	if _, err := os.Stat(filepath.Join(src, "out", "report.md")); !os.IsNotExist(err) {
		t.Errorf("the run wrote into the caller's source tree (err = %v)", err)
	}
}

// Without return_artifacts the response is byte-for-byte what it was before
// this feature existed.
func TestChatCompletions_ArtifactsAreOptIn(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeWriting(t, `printf '# digest' > "$dir/report.md"`, "wrote the report")
	src := t.TempDir()
	h := artifactHandler(1)

	resp := postChat(t, h, chatBody(`"work_dirs":["`+src+`"],"clone_work_dirs":true`))

	if len(resp.Artifacts) != 0 || len(resp.ArtifactsSkipped) != 0 {
		t.Errorf("artifacts = %v / %v, want neither without return_artifacts",
			artifactPaths(resp.Artifacts), resp.ArtifactsSkipped)
	}
}

// A run's files exist only once it is over, so they ride the final chunk
// alongside session_id and usage.
func TestChatCompletions_ArtifactsRideTheStreamingFinalChunk(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeWriting(t, `printf 'streamed report' > "$dir/report.md"`, "done")
	src := t.TempDir()
	h := artifactHandler(1)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		chatBody(`"stream":true,"work_dirs":["`+src+`"],"clone_work_dirs":true,"return_artifacts":true`))))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	chunks := parseSSEChunks(t, rec.Body.String())
	final := chunks[len(chunks)-1]
	if final.Choices[0].FinishReason == nil {
		t.Fatal("last chunk is not the final one")
	}
	wantPaths(t, final.Artifacts, "report.md")
	if got := contentOf(t, final.Artifacts, "report.md"); got != "streamed report" {
		t.Errorf("report content = %q, want %q", got, "streamed report")
	}
	for _, c := range chunks[:len(chunks)-1] {
		if len(c.Artifacts) != 0 {
			t.Errorf("artifacts arrived on a mid-stream chunk: %v", artifactPaths(c.Artifacts))
		}
	}
}

// A failed synchronous run returns the generic execution-failure envelope
// rather than a false successful completion.
func TestChatCompletions_ArtifactsSurviveAFailedRun(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeWriting(t,
		`printf 'got this far' > "$dir/progress.md"; printf 'boom' >&2; exit 1`, "")
	src := t.TempDir()
	h := artifactHandler(1)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody(
		`"work_dirs":["`+src+`"],"clone_work_dirs":true,"return_artifacts":true`))))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tool_execution_failed") || !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("failure body = %s", rec.Body.String())
	}
}

// Concurrent runs each diff their own clone. A collector that leaked across
// runs would show up here as one request receiving another's file.
func TestChatCompletions_ConcurrentRunsGetOnlyTheirOwnArtifacts(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeWriting(t, `cp "$dir/id.txt" "$dir/echo.txt"`, "copied")
	h := artifactHandler(6)

	const runs = 6
	var wg sync.WaitGroup
	got := make([]string, runs)
	for i := 0; i < runs; i++ {
		src := t.TempDir()
		id := fmt.Sprintf("run-%d", i)
		if err := os.WriteFile(filepath.Join(src, "id.txt"), []byte(id), 0o644); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		wg.Add(1)
		go func(i int, src string) {
			defer wg.Done()
			resp := postChat(t, h, chatBody(
				`"work_dirs":["`+src+`"],"clone_work_dirs":true,"return_artifacts":true`))
			for _, a := range resp.Artifacts {
				if a.Path == "echo.txt" {
					got[i] = a.Content
				}
			}
		}(i, src)
	}
	wg.Wait()

	for i := 0; i < runs; i++ {
		if want := fmt.Sprintf("run-%d", i); got[i] != want {
			t.Errorf("run %d received echo.txt = %q, want %q", i, got[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// Async: the callback carries artifacts, retention holds a budget
// ---------------------------------------------------------------------------

func TestAsyncCallback_CarriesTheRunsArtifacts(t *testing.T) {
	chassis.RequireMajor(11)
	installFakeOpenCodeWriting(t, `printf '# async digest' > "$dir/report.md"`, "done")
	src := t.TempDir()
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(receiver.server.URL,
		`"work_dirs":["`+src+`"],"clone_work_dirs":true,"return_artifacts":true`), "wm-artifacts-1")
	payload := receiver.await(t, 1)

	if payload.Status != runStatusSuccess {
		t.Fatalf("callback status = %q, want %s", payload.Status, runStatusSuccess)
	}
	wantPaths(t, payload.Artifacts, "report.md")
	if got := contentOf(t, payload.Artifacts, "report.md"); got != "# async digest" {
		t.Errorf("callback report content = %q, want %q", got, "# async digest")
	}

	// Small enough to keep: polling shows the same thing the callback carried.
	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	wantPaths(t, retained.Artifacts, "report.md")
}

// Retention keeps the same 64KB discipline as step output; a callback has no
// such limit. Over the budget the two diverge on purpose: the POST carries the
// bytes, the retained copy keeps only the names.
func TestAsyncCallback_OverCapArtifactsAreDeliveredButNotRetained(t *testing.T) {
	chassis.RequireMajor(11)
	big := asyncRetainedArtifactCap + (8 << 10)
	installFakeOpenCodeWriting(t,
		fmt.Sprintf(`awk 'BEGIN{while(i++<%d)printf "z"}' > "$dir/big.log"`, big), "done")
	src := t.TempDir()
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(receiver.server.URL,
		`"work_dirs":["`+src+`"],"clone_work_dirs":true,"return_artifacts":true`), "wm-artifacts-2")
	payload := receiver.await(t, 1)

	wantPaths(t, payload.Artifacts, "big.log")
	if got := len(contentOf(t, payload.Artifacts, "big.log")); got != big {
		t.Errorf("callback delivered %d bytes, want the whole %d", got, big)
	}

	rec := do(t, h, http.MethodGet, "/v1/runs/"+submitted.RunID+"/result")
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var retained AsyncCompletion
	if err := json.NewDecoder(rec.Body).Decode(&retained); err != nil {
		t.Fatalf("decode retained result: %v", err)
	}
	if len(retained.Artifacts) != 0 {
		t.Errorf("retained artifacts = %v, want none over the cap", artifactPaths(retained.Artifacts))
	}
	if got := skipReason(retained.ArtifactsSkipped, "big.log"); got != artifactSkipEvicted {
		t.Errorf("retained big.log reason = %q, want %q", got, artifactSkipEvicted)
	}
}

// A cancelled run is reported as the failure it is — carrying whatever it wrote
// before the kill.
func TestAsyncCancel_ReportsTheArtifactsWrittenBeforeTheKill(t *testing.T) {
	chassis.RequireMajor(11)
	marker := filepath.Join(t.TempDir(), "started")
	// exec, so the process rserve kills is the sleep itself: a forked child
	// would outlive the kill still holding the run's stdout.
	installFakeOpenCodeWriting(t,
		`printf 'partial work' > "$dir/progress.md"; printf 'x' > '`+marker+`'; exec sleep 60`, "")
	src := t.TempDir()
	receiver := newCallbackReceiver(t, http.StatusOK)
	h := asyncHandler(t, server.NewRunRegistry(1), openCodeFactory())

	submitted := submitAsync(t, h, callBody(receiver.server.URL,
		`"work_dirs":["`+src+`"],"clone_work_dirs":true,"return_artifacts":true`), "wm-artifacts-3")

	// Wait for the CLI to have written its file, so the cancellation lands
	// after the work it is meant to preserve.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake CLI never reached its marker")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if rec := do(t, h, http.MethodDelete, "/v1/runs/"+submitted.RunID); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := receiver.await(t, 1)

	if payload.Status != runStatusFailure {
		t.Fatalf("callback status = %q, want %s", payload.Status, runStatusFailure)
	}
	if payload.Error == nil || payload.Error.Code != codeRunCancelled {
		t.Fatalf("error = %+v, want %s", payload.Error, codeRunCancelled)
	}
	wantPaths(t, payload.Artifacts, "progress.md")
	if got := contentOf(t, payload.Artifacts, "progress.md"); got != "partial work" {
		t.Errorf("progress.md content = %q, want %q", got, "partial work")
	}
}

// ---------------------------------------------------------------------------
// Retention copy
// ---------------------------------------------------------------------------

func TestRetainedCopy(t *testing.T) {
	small := &AsyncCompletion{RunID: "r1"}
	small.Artifacts = []Artifact{{Path: "a.md", Content: "short", Bytes: 5}}
	if got := retainedCopy(small); got != small {
		t.Error("a payload within the budget was copied instead of retained as is")
	}

	over := &AsyncCompletion{RunID: "r2"}
	over.Artifacts = []Artifact{
		{Path: "a.md", Content: strings.Repeat("a", asyncRetainedArtifactCap), Bytes: asyncRetainedArtifactCap},
		{Path: "b.md", Content: "b", Bytes: 1},
	}
	over.ArtifactsSkipped = []ArtifactSkipped{{Path: "c.bin", Reason: artifactSkipBinary}}

	retained := retainedCopy(over)
	if len(retained.Artifacts) != 0 {
		t.Errorf("retained artifacts = %v, want none", artifactPaths(retained.Artifacts))
	}
	if got := skipReason(retained.ArtifactsSkipped, "c.bin"); got != artifactSkipBinary {
		t.Errorf("an earlier skip was lost: c.bin reason = %q", got)
	}
	for _, path := range []string{"a.md", "b.md"} {
		if got := skipReason(retained.ArtifactsSkipped, path); got != artifactSkipEvicted {
			t.Errorf("%s retained as %q, want %q", path, got, artifactSkipEvicted)
		}
	}
	// The payload the callback carries is untouched by what retention decided.
	if len(over.Artifacts) != 2 || len(over.ArtifactsSkipped) != 1 {
		t.Errorf("retention mutated the delivered payload: %d artifacts, %d skipped",
			len(over.Artifacts), len(over.ArtifactsSkipped))
	}

	if got := retainedCopy(nil); got != nil {
		t.Error("a nil payload produced something")
	}
}
