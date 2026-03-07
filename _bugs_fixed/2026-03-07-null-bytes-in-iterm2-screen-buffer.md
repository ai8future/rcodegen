# Null bytes in iTerm2 screen buffer break status parsing

**Date:** 2026-03-07

iTerm2's screen buffer pads empty cells with null bytes (`\x00`). The `strip_ansi()` function only removed ANSI escape codes but left nulls intact, causing the regex patterns in `parse_status_output()` to fail silently — they couldn't match "Current week (all models)" when null bytes were embedded in the text.

**Fix:** Replace `\x00` with spaces in `strip_ansi()`. Also fixed the weekly reset regex to handle the "at" keyword in "Resets Mar 13 at 8am" format.
