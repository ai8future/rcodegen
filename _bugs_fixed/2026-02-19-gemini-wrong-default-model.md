# rgemini using wrong default model

**Date:** 2026-02-19

**Severity:** Medium — caused rate-limit errors on unintended model

**Symptom:** Running `rgemini` without `--flash` produced `gemini-3-flash-preview` rate-limit errors even though the intended default is `gemini-3-pro-preview`.

**Root cause:** `BuildCommand` in `pkg/tools/gemini/gemini.go` skipped the `-m` flag when the configured model matched our default (`gemini-3-pro-preview`). The `gemini` CLI's own built-in default is `gemini-3-flash-preview`, so without an explicit `-m`, flash was used.

**Fix:** Always pass `-m` when `cfg.Model` is set, regardless of whether it matches our default.
