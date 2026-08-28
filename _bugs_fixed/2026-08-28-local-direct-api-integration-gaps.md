# Local direct-API integration gaps

- Bundle execution previously routed every tool through `BuildCommand`, bypassing tools that implement `DirectAPIRunner`.
- rserve previously converted nonzero tool exits into successful empty completions instead of reporting `tool_execution_failed`.
- Local and remote rbatch execution previously discarded generated output, and terminal jobs did not consistently write per-job result files.
- Regression coverage now exercises direct bundle execution, synchronous/streaming/async failure reporting, local and remote batch output retention, truncation, and terminal-result persistence.
