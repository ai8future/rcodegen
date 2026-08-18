# Model discovery, effort, and rserve security fixes

The 4.2.8–4.2.9 model validation changes accidentally rejected every dynamic OpenCode/KiloCode model and reported compiled defaults instead of configured defaults. Codex GPT-5.6 effort capabilities were also incomplete.

Fixed dynamic namespace validation, configuration-aware `/v1/models` output, and per-model Codex effort validation. Added an rserve non-loopback bind interlock after the Windmill guide was found to recommend direct plaintext LAN exposure, and synchronized the README/API/deployment documentation with the corrected behavior.

Regression tests cover dynamic model requests, configured defaults, the Codex effort matrix, invalid combinations, and remote-bind refusal.
