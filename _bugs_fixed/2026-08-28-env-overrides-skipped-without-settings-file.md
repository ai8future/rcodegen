# Environment overrides were skipped without a settings file

`LoadWithFallback` returned default settings immediately when `~/.rcodegen/settings.json` was absent. That bypassed every `RCODEGEN_*` environment override, so isolated rserve and rbatch processes could silently ignore configured Ollama and LM Studio origins, models, and API keys.

The fallback now continues through the normal default-filling, environment-override, validation, and built-in-task merge path while still reporting that no settings file existed. A regression test covers both local-runtime configurations without a settings file.
