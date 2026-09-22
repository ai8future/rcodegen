# Recent fixes verification gaps

An audit of the 4.2.3-4.2.6 bundle/server changes found several incomplete or adjacent regressions:

- gRPC `CancelRun` registered one context but executed bundles with the stream context, so explicit cancellation did not stop bundle work.
- `RSERVE_WORK_ROOT` used lexical containment only, allowing a pre-existing symlink component to redirect work outside the configured root.
- The artifact scan limited regular files rather than all inspected entries, leaving directory- or special-file-heavy trees effectively unbounded.
- Conditional `else` branches were skipped before dispatch, and conditional/failing steps lost aggregate usage totals and accurate step metrics.
- Vote output persisted through `OutputRef` was absent from the bundle API's final output.
- Gemini ignored its configured default model and fell back to an invalid `gemini-3` identifier.
- Local and remote `rbatch` execution did not reliably carry returned session IDs, and documented flags after the manifest path were not parsed.
- HTTP/gRPC task execution discarded stdout from Codex, OpenCode, and KiloCode because only structured stream callbacks were forwarded.
- Dashboard detail routes ignored `RCODEGEN_CODE_DIR`, and old-format OpenCode/KiloCode reports were not recognized as known tools.

The fixes add focused regression coverage for cancellation, rooted filesystem confinement, artifact bounds, conditional accounting, output selection, configured Gemini defaults, batch session propagation/argument ordering, and non-streaming server output. Full Go race tests, vet, dashboard lint/build, scheduler syntax validation, and all-binary compilation are part of the release verification.
