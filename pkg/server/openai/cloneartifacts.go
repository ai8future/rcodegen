// cloneartifacts.go returns what an agent wrote inside its work_dir clone.
//
// A cloned run is a sandbox: the agent may write anything it likes, and the
// scratch root is removed the moment the run ends. Without this file the only
// thing that survives is the message text, so a run asked to "write the report
// to report.md" produces a report nobody can read.
//
//	"clone_work_dirs": true, "return_artifacts": true
//	  -> "artifacts":         [{"path", "content", "bytes"}]
//	     "artifacts_skipped": [{"path", "reason"}]
//
// The diff is against a manifest taken after the clone finishes and before the
// CLI starts, so what comes back is what this run wrote — not what the source
// tree already held. Collection always runs before cleanup, and it runs even
// when the run failed: a failed run's half-written files are usually the most
// diagnostic thing it produced.
package openai

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"unicode/utf8"
)

// Reasons a file under a clone was found but not returned. Every skipped file
// is named, so a caller never has to wonder whether an artifact is missing or
// was never written.
const (
	// artifactSkipBinary: the file is not text by the probe below.
	artifactSkipBinary = "binary"
	// artifactSkipOversize: the file is larger than artifactFileCap on its own.
	artifactSkipOversize = "oversize"
	// artifactSkipResponseCap: the response's total content budget was spent.
	artifactSkipResponseCap = "response_cap"
	// artifactSkipTooManyFiles: artifactMaxCount artifacts are already returned.
	artifactSkipTooManyFiles = "too_many_files"
	// artifactSkipCollectionError: the clone or the file could not be read.
	// Collection never fails a run — it reports what it could not do.
	artifactSkipCollectionError = "collection_error"
	// artifactSkipScanLimit: the clone holds more entries than one walk will
	// visit, so a created-or-modified diff over it cannot be trusted.
	artifactSkipScanLimit = "scan_limit"
	// artifactSkipEvicted: the artifact was delivered to the callback but not
	// kept in memory. See retainedCopy in asyncruns.go.
	artifactSkipEvicted = "evicted_from_retention"
	// artifactSkipInspectionCap: collection spent its inspection budget before
	// reaching this file. It is named rather than dropped, so a caller can tell
	// a file that was not returned from one the run never wrote.
	artifactSkipInspectionCap = "inspection_cap"
)

// The inspection budget: what collecting a response is allowed to cost,
// independently of what the response may contain.
//
// The response caps — 100 artifacts, 512KB each, 2MiB total — bound the answer.
// They do not bound the work, because a file that is not returned consumes
// neither: a clone of binary files, or of hard links to one binary file, spends
// no response budget at all while being read in full, once per name. These two
// bound that work instead, and are charged before a read rather than after it.
//
// They are variables only so tests can shrink them; production does not mutate
// them.
var (
	// artifactMaxCandidates is how many changed files one collection opens.
	artifactMaxCandidates = 1000
	// artifactMaxReadBytes is how many bytes one collection reads in total,
	// counting text probes and returned content alike.
	artifactMaxReadBytes int64 = 16 << 20
)

// cloneScanMaxEntries bounds one clone walk. A tree bigger than this is
// reported as unscannable rather than diffed against a partial manifest,
// because a partial manifest turns untouched files into false artifacts. Tests
// shrink it to reach that branch without building a huge tree.
var cloneScanMaxEntries = 200000

const (
	// artifactTextProbe is how many leading bytes decide text versus binary.
	artifactTextProbe = 8 << 10

	// artifactSkipCap bounds the skip report itself, so a clone full of
	// unreturnable files cannot grow the response without limit. Skips past it
	// are logged and counted, not sent.
	artifactSkipCap = artifactMaxCount
)

var (
	// errArtifactOversize marks a file that grew past the per-file cap between
	// the manifest and the read.
	errArtifactOversize = errors.New("artifact exceeds the per-file cap")

	// errArtifactBinary marks a candidate the text probe refused, before the
	// rest of it was read.
	errArtifactBinary = errors.New("artifact is not text")

	// errArtifactInspectionCap marks a candidate collection had no budget left
	// to inspect.
	errArtifactInspectionCap = errors.New("artifact inspection budget is spent")

	// errArtifactResponseCap marks a file that no longer fits the response's
	// remaining content budget.
	errArtifactResponseCap = errors.New("artifact does not fit the remaining response budget")

	// errArtifactNotRegular marks a candidate that was a regular file in the
	// manifest and is something else — a FIFO, a device — by the time it is
	// opened.
	errArtifactNotRegular = errors.New("artifact is no longer a regular file")

	// errCloneScanLimit marks a clone too large to walk. It is the one manifest
	// failure that reports as scan_limit rather than as a collection error.
	errCloneScanLimit = errors.New("clone holds more entries than one artifact scan visits")
)

// Artifact is one text file the run created or modified inside its clone,
// returned inline so a caller needs no filesystem access to this host. Content
// is always the whole file: a file too big to send is skipped, never truncated,
// so an artifact that arrives can be trusted as complete.
type Artifact struct {
	// Path is relative to the clone of the work_dir that holds it. When the
	// request cloned more than one work_dir it is prefixed with that clone's
	// directory name, so paths stay unambiguous across them.
	Path    string `json:"path"`
	Content string `json:"content"`
	Bytes   int64  `json:"bytes"`
}

// ArtifactSkipped names a file that was found but not returned, and why.
type ArtifactSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// cloneScan is one cloned work_dir's before-manifest and the handle it was
// taken through. The handle is an open descriptor on the clone directory,
// pinned before the CLI starts: a run that replaces its own clone directory
// with a symlink cannot redirect collection somewhere else.
type cloneScan struct {
	prefix string                  // clone directory name, empty for a single work_dir
	name   string                  // clone directory name, for log lines
	root   *os.Root                // pinned handle, closed by artifactCollector.close
	before map[string]artifactMeta // path -> size+mtime, taken before the run
	err    error                   // set when no usable manifest could be taken
}

// artifactPath renders one file's path as the response reports it.
func (s *cloneScan) artifactPath(rel string) string {
	rel = filepath.ToSlash(rel)
	if s.prefix == "" {
		return rel
	}
	return path.Join(s.prefix, rel)
}

// reportPath names the whole clone, for a skip that is the directory's rather
// than any one file's.
func (s *cloneScan) reportPath() string {
	if s.prefix == "" {
		return "."
	}
	return s.prefix
}

// artifactCollector holds one run's before-manifests and turns them into the
// run's artifacts once the CLI is done. A nil collector is the "not asked for"
// case and answers nothing, so callers need no branch.
type artifactCollector struct {
	logger *slog.Logger
	dirs   []cloneScan

	once      sync.Once
	artifacts []Artifact
	skipped   []ArtifactSkipped
	// What the one collection cost: candidates opened and bytes read. Kept so
	// the inspection budgets can be asserted rather than assumed.
	inspected int
	bytesRead int64
}

// newArtifactCollector takes the manifest of every cloned work_dir. Call it
// after cloneWorkDirs returns and before the CLI starts: everything that
// differs from this moment is the run's own work.
//
// Hidden entries are out of scope, matching the bundle scanner: a clone's .git
// index and a tool's own dot-directory state churn on every run and are not
// what a caller asked for.
func newArtifactCollector(clone *workDirClone, logger *slog.Logger) *artifactCollector {
	if clone == nil || len(clone.dirs) == 0 {
		return nil
	}
	c := &artifactCollector{logger: logger}
	multi := len(clone.dirs) > 1
	for _, dir := range clone.dirs {
		scan := cloneScan{name: filepath.Base(dir)}
		if multi {
			scan.prefix = scan.name
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			scan.err = err
			c.dirs = append(c.dirs, scan)
			continue
		}
		before, truncated := scanClone(root)
		if truncated {
			root.Close()
			scan.err = errCloneScanLimit
		} else {
			scan.root = root
			scan.before = before
		}
		c.dirs = append(c.dirs, scan)
	}
	return c
}

// close releases the pinned clone handles. Call it after collect, in the same
// teardown stack as the clone's own cleanup.
func (c *artifactCollector) close() {
	if c == nil {
		return
	}
	for i := range c.dirs {
		if c.dirs[i].root != nil {
			c.dirs[i].root.Close()
			c.dirs[i].root = nil
		}
	}
}

// collect diffs every clone against its manifest and returns the run's
// artifacts and skip report. It memoizes: the async path asks twice — once for
// the completion, once for the failure payload that replaces it — and the
// clones are walked only the first time.
func (c *artifactCollector) collect() ([]Artifact, []ArtifactSkipped) {
	if c == nil {
		return nil, nil
	}
	c.once.Do(func() {
		out := &artifactBudget{budget: artifactTotalCap}
		for i := range c.dirs {
			c.collectDir(out, &c.dirs[i])
		}
		if out.dropped > 0 {
			c.warn("artifact skip report truncated",
				"reported", len(out.skipped), "dropped", out.dropped)
		}
		c.artifacts, c.skipped = out.artifacts, out.skipped
		c.inspected, c.bytesRead = out.inspected, out.bytesRead
	})
	return c.artifacts, c.skipped
}

// collectDir appends one clone's artifacts to the run's budget.
func (c *artifactCollector) collectDir(out *artifactBudget, scan *cloneScan) {
	if scan.root == nil {
		reason := artifactSkipCollectionError
		if errors.Is(scan.err, errCloneScanLimit) {
			reason = artifactSkipScanLimit
		}
		c.warn("work_dir clone could not be scanned for artifacts",
			"clone", scan.name, "reason", reason, "error", scan.err)
		out.skip(scan.reportPath(), reason)
		return
	}

	after, truncated := scanClone(scan.root)
	if truncated {
		c.warn("work_dir clone grew past the artifact scan limit",
			"clone", scan.name, "max_entries", cloneScanMaxEntries)
		out.skip(scan.reportPath(), artifactSkipScanLimit)
		return
	}

	for _, rel := range changedPaths(scan.before, after) {
		reported := scan.artifactPath(rel)
		if out.full() {
			out.skip(reported, artifactSkipTooManyFiles)
			continue
		}
		if after[rel].size > artifactFileCap {
			out.skip(reported, artifactSkipOversize)
			continue
		}
		if after[rel].size > int64(out.budget) {
			out.skip(reported, artifactSkipResponseCap)
			continue
		}
		content, err := out.read(scan.root, rel)
		switch {
		case errors.Is(err, errArtifactInspectionCap):
			out.skip(reported, artifactSkipInspectionCap)
		case errors.Is(err, errArtifactBinary):
			out.skip(reported, artifactSkipBinary)
		case errors.Is(err, errArtifactOversize):
			out.skip(reported, artifactSkipOversize)
		case errors.Is(err, errArtifactResponseCap):
			// The file grew between the manifest and the read.
			out.skip(reported, artifactSkipResponseCap)
		case err != nil:
			c.warn("artifact could not be read",
				"clone", scan.name, "path", rel, "error", err)
			out.skip(reported, artifactSkipCollectionError)
		default:
			out.keep(reported, content)
		}
	}
}

// warn logs, tolerating a collector built without a logger (tests).
func (c *artifactCollector) warn(msg string, args ...any) {
	if c != nil && c.logger != nil {
		c.logger.Warn(msg, args...)
	}
}

// artifactBudget accumulates a run's artifacts against every cap at once: total
// content bytes, artifact count, and the size of the skip report.
type artifactBudget struct {
	budget    int
	artifacts []Artifact
	skipped   []ArtifactSkipped
	dropped   int

	// What inspection has actually cost so far, for the tests that assert the
	// budgets above are doing something.
	inspected int
	bytesRead int64
}

// full reports whether the artifact count cap is reached. Past it no file is
// read, only named.
func (b *artifactBudget) full() bool { return len(b.artifacts) >= artifactMaxCount }

func (b *artifactBudget) keep(path string, content []byte) {
	b.artifacts = append(b.artifacts, Artifact{
		Path:    path,
		Content: string(content),
		Bytes:   int64(len(content)),
	})
	b.budget -= len(content)
}

func (b *artifactBudget) skip(path, reason string) {
	if len(b.skipped) >= artifactSkipCap {
		b.dropped++
		return
	}
	b.skipped = append(b.skipped, ArtifactSkipped{Path: path, Reason: reason})
}

// chargeRead reserves n bytes against the read budget and reports whether there
// was room. The reservation is made before the read rather than after it: a
// budget that counted only bytes already read would be one that finds out it is
// over, which is the failure this bound exists to prevent.
func (b *artifactBudget) chargeRead(n int64) bool {
	if b.bytesRead+n > artifactMaxReadBytes {
		return false
	}
	b.bytesRead += n
	return true
}

// read inspects one candidate and returns its content, or the reason it is not
// an artifact.
//
// The order is the whole point. A candidate costs one inspection and a text
// probe before anything else is read, and a file the probe says is binary costs
// nothing more — so a clone of hard links to one binary file costs a probe per
// name rather than the file per name. Only a file that is text, and that fits
// what is left of the response, is read the rest of the way.
func (b *artifactBudget) read(root *os.Root, rel string) ([]byte, error) {
	if b.inspected >= artifactMaxCandidates {
		return nil, errArtifactInspectionCap
	}
	b.inspected++

	f, size, err := openArtifact(root, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if size > artifactFileCap {
		return nil, errArtifactOversize
	}

	probe := int64(artifactTextProbe)
	if size < probe {
		probe = size
	}
	if !b.chargeRead(probe) {
		return nil, errArtifactInspectionCap
	}
	head := make([]byte, probe)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	head = head[:n]
	if !looksTextualPrefix(head, int64(n) < size) {
		return nil, errArtifactBinary
	}

	remaining := size - int64(n)
	if remaining <= 0 {
		if len(head) > b.budget {
			return nil, errArtifactResponseCap
		}
		return head, nil
	}
	// Reserve the advertised size against both budgets before reading the rest.
	if size > int64(b.budget) {
		return nil, errArtifactResponseCap
	}
	if !b.chargeRead(remaining) {
		return nil, errArtifactInspectionCap
	}
	// One byte past the cap distinguishes a file that grew since the manifest
	// from one that is exactly at it.
	rest, err := io.ReadAll(io.LimitReader(f, remaining+1))
	if err != nil {
		return nil, err
	}
	content := append(head, rest...)
	if len(content) > artifactFileCap {
		return nil, errArtifactOversize
	}
	if len(content) > b.budget {
		return nil, errArtifactResponseCap
	}
	return content, nil
}

// openArtifact opens one candidate through the clone's pinned handle and
// re-verifies what it opened.
//
// O_NONBLOCK is what keeps this an open rather than a wait. A candidate that
// was a regular file when the clone was scanned and is a FIFO by the time it is
// opened would otherwise block until a writer appeared — and the run that chose
// to make it a FIFO is the one deciding whether that ever happens, while
// collection holds the run slot. With it the open returns and the stat below
// refuses the file for what it now is.
func openArtifact(root *os.Root, rel string) (*os.File, int64, error) {
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("%w: it is now %s", errArtifactNotRegular, info.Mode().Type())
	}
	return f, info.Size(), nil
}

// scanClone snapshots a clone and reports whether the walk hit its entry bound.
// One extra entry is asked for so a tree of exactly the bound is not mistaken
// for a truncated one.
func scanClone(root *os.Root) (map[string]artifactMeta, bool) {
	snap, visited := snapshotWorkDirLimited(root, cloneScanMaxEntries+1)
	return snap, visited > cloneScanMaxEntries
}

// changedPaths returns the files that are new or whose size or mtime moved,
// sorted so a run's artifacts arrive in the same order every time. A deleted
// file is not an artifact: there is nothing to return.
func changedPaths(before, after map[string]artifactMeta) []string {
	var paths []string
	for rel, meta := range after {
		if prev, ok := before[rel]; ok && prev == meta {
			continue
		}
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths
}

// looksTextual decides whether content can be returned inline, from its first
// artifactTextProbe bytes: no NUL, and valid UTF-8. An empty file is text.
//
// The probe is a prefix, so a multi-byte rune can straddle its end; that cut is
// trimmed before validating, or every large UTF-8 file would be a coin flip.
func looksTextual(data []byte) bool {
	head := data
	truncated := len(head) > artifactTextProbe
	if truncated {
		head = head[:artifactTextProbe]
	}
	return looksTextualPrefix(head, truncated)
}

// looksTextualPrefix is the same decision for a probe read from the front of a
// file rather than from content already in hand. truncated says whether more of
// the file follows, which is what decides whether a trailing partial rune is a
// boundary cut to trim or genuinely invalid bytes.
func looksTextualPrefix(head []byte, truncated bool) bool {
	if truncated {
		head = trimPartialRuneBytes(head)
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	return utf8.Valid(head)
}

// trimPartialRuneBytes drops a trailing incomplete UTF-8 sequence left by a
// byte-boundary cut, the byte-slice twin of trimPartialRune.
func trimPartialRuneBytes(b []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}
