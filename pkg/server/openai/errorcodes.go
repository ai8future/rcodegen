// errorcodes.go is the single source of truth for the HTTP API's error codes
// and their retryability. Every error response carries "retryable" so a caller
// with an automatic retry policy — Windmill's per-step retry, for one — can
// tell a transient failure from a doomed request without pattern-matching
// error messages.
//
// Adding an error code means declaring it here and classifying it in
// errorRetryable. errorcodes_test.go parses this package and fails on a code
// that is used at a call site but not declared, or declared but not
// classified, so an unclassified code can never reach a caller as a silent
// default.
package openai

// ErrorCode identifies an API error condition. Handlers pass declared
// constants rather than bare strings, which is what lets the completeness
// test see every code the package can emit.
type ErrorCode string

const (
	// Not retryable: the request itself is wrong or asks for something the
	// server refuses on policy grounds. Sending it again changes nothing.

	codeMethodNotAllowed       ErrorCode = "method_not_allowed"
	codeUnauthorized           ErrorCode = "unauthorized"
	codeInvalidJSON            ErrorCode = "invalid_json"
	codeUnknownTool            ErrorCode = "unknown_tool"
	codeEmptyTask              ErrorCode = "empty_task"
	codeInvalidModel           ErrorCode = "invalid_model"
	codeInvalidEffort          ErrorCode = "invalid_effort"
	codeInvalidWorkDir         ErrorCode = "invalid_work_dir"
	codeUnsafeSymlink          ErrorCode = "unsafe_symlink"
	codeUnsupportedGitWorktree ErrorCode = "unsupported_git_worktree"
	codeUnknownBundle          ErrorCode = "unknown_bundle"
	codeMissingInput           ErrorCode = "missing_input"
	codeInvalidUpload          ErrorCode = "invalid_upload"
	codeMissingFile            ErrorCode = "missing_file"
	codeInvalidID              ErrorCode = "invalid_id"
	codeNotFound               ErrorCode = "not_found"
	codeNoFileStore            ErrorCode = "no_file_store"
	codeInvalidCallbackURL     ErrorCode = "invalid_callback_url"
	codeInvalidCallbackHeaders ErrorCode = "invalid_callback_headers"
	codeCallbackStreamConflict ErrorCode = "callback_stream_conflict"
	codeRunCancelled           ErrorCode = "run_cancelled"

	// Retryable: a transient server-side or environmental failure. The same
	// request can succeed later.

	codeConcurrencyLimit ErrorCode = "concurrency_limit"
	codeCloneFailed      ErrorCode = "clone_failed"
	codeWorkDirFailed    ErrorCode = "work_dir_failed"
	codeBundleFailed     ErrorCode = "bundle_failed"
	codeBundleListFailed ErrorCode = "bundle_list_failed"
	codeSaveFailed       ErrorCode = "save_failed"
	codeServerShutdown   ErrorCode = "server_shutdown"
)

// errorRetryable classifies every declared error code. The mapping is the
// contract callers build retry policies on, so each entry states why.
var errorRetryable = map[ErrorCode]bool{
	// --- not retryable ------------------------------------------------------
	// Wrong verb, wrong credentials, or a body the server cannot parse.
	codeMethodNotAllowed: false,
	codeUnauthorized:     false,
	codeInvalidJSON:      false,
	// The request names something that does not exist or is not accepted.
	codeUnknownTool:   false,
	codeEmptyTask:     false,
	codeInvalidModel:  false,
	codeInvalidEffort: false,
	codeUnknownBundle: false,
	codeMissingInput:  false,
	// work_dirs policy rejections: the source will be refused every time until
	// the caller points at a different directory.
	codeInvalidWorkDir:         false,
	codeUnsafeSymlink:          false,
	codeUnsupportedGitWorktree: false,
	// Files API request errors, and a server built without a file store — a
	// deployment decision, not a passing condition.
	codeInvalidUpload: false,
	codeMissingFile:   false,
	codeInvalidID:     false,
	codeNotFound:      false,
	codeNoFileStore:   false,
	// Async callback mode request errors: a callback URL the server refuses to
	// POST to, headers it cannot send, or a callback asked for alongside a
	// stream. All three are properties of the request, unchanged by resending.
	codeInvalidCallbackURL:     false,
	codeInvalidCallbackHeaders: false,
	codeCallbackStreamConflict: false,
	// The caller cancelled this run through DELETE /v1/runs/{id}. Retrying is a
	// new decision, not a recovery.
	codeRunCancelled: false,

	// --- retryable ----------------------------------------------------------
	// The slot wait was interrupted (shutdown or a disconnected client); the
	// work never started.
	codeConcurrencyLimit: true,
	// Filesystem failures while cloning or preparing a work directory. These
	// are not policy rejections — those are classified above — so they are the
	// transient kind: a source that moved during the slot wait, a full disk, a
	// copy that lost a race.
	codeCloneFailed:   true,
	codeWorkDirFailed: true,
	// The CLI process crashed, exited unexpectedly, timed out, or the provider
	// refused the call (rate/usage limits all surface here).
	codeBundleFailed: true,
	// Reading the bundle directory or writing an upload failed on the server's
	// own filesystem.
	codeBundleListFailed: true,
	codeSaveFailed:       true,
	// An async run was still in flight when the server shut down. Nothing about
	// the request was wrong, and rserve keeps no durable state, so the caller's
	// only recovery is to submit it again.
	codeServerShutdown: true,
}

// retryableForCode reports whether a caller should retry an error with this
// code. An unclassified code is reported as not retryable: telling a client to
// retry something the server does not understand is the more expensive
// mistake, and the completeness test keeps this branch unreachable.
func retryableForCode(code ErrorCode) bool {
	return errorRetryable[code]
}
