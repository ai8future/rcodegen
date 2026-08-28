# README local-runtime guidance had drifted

The README correctly listed the guarded E2E commands but omitted several prerequisites and overrides, described version flags too broadly, and omitted the local-runtime test/adapter paths from its repository map. It also repeated an Ollama compatibility claim that the current official page no longer supports.

The documentation now distinguishes rcodegen's deliberately minimal Phase 1 payload from Ollama's upstream capabilities and records the exact E2E safety requirements, ports, runtime origins, binary version flags, async server settings, and relevant source paths.
