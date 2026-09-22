# Jev decision validation and gate safety

The implementation proposal contained several paths that could turn malformed
vendor data into successful gates, lose output or accounting, expose an
environment key through settings/test output, or continue after a failed gate
inside a parallel group.

The implemented contract rejects unusable answers (including null probabilities),
keeps secrets out of persisted settings and diagnostics, bounds and scrubs state,
preserves UTF-8 when retaining the head and tail, and excludes decision steps
from parallel groups. Output-write failures stop a decision step. Model overrides
are reflected in step events. The HTTP endpoint preserves permanent upstream
failure classifications and rejects oversized or trailing request content.

Tests use local fake upstreams, isolate environment keys, exercise cancellation
during backoff, and keep negative fixtures limited to the intended defect.

Verified: full race/coverage suite, static analysis, all platform builds, and a
local fake-vendor binary smoke test. Live vendor access was not available.

Agent: Codex:GPT-6
