#!/usr/bin/env python3
"""
get_codex_status.py - Capture Codex credit status using iTerm2 API

Creates a temporary tab, runs codex, sends /status, captures output,
parses credit percentages, and outputs JSON.

Usage:
    python3 get_codex_status.py

Output (JSON to stdout):
    {"5h_left": 64, "weekly_left": 89, "context_left": 52}

Requirements:
    - iTerm2 (not macOS Terminal)
    - iTerm2 Python API enabled (Preferences > General > Magic > Enable Python API)
    - iterm2 Python package: pip install iterm2
"""

import asyncio
import json
import re
import sys
import os
import tempfile
from datetime import datetime, timedelta

# Debug mode - set RCODEX_DEBUG=1 to enable debug output
DEBUG_MODE = os.environ.get('RCODEX_DEBUG', '').lower() in ('1', 'true', 'yes')

# Check for iTerm2 environment before importing iterm2 package
if not os.environ.get('ITERM_SESSION_ID'):
    print(json.dumps({
        "error": "not_iterm2",
        "message": "Not running in iTerm2. Credit tracking requires iTerm2."
    }))
    sys.exit(0)

# Try to import iterm2 package
try:
    import iterm2
except ImportError:
    print(json.dumps({
        "error": "no_iterm2_package",
        "message": "iterm2 Python package not installed. Run: pip install iterm2"
    }))
    sys.exit(0)

# Wrapper script that sets up PATH and runs codex
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
CODEX_WRAPPER = os.path.join(SCRIPT_DIR, "codex_wrapper.sh")


def parse_status_output(text: str) -> dict:
    """Parse /status output to extract credit percentages and reset times.

    Uses a flexible approach: scans for lines containing 'N% left' and
    classifies them by nearby keywords. This survives layout changes,
    reordering, and minor wording changes.

    Example input lines:
        5h limit:       [████████████░░░░░░░░] 62% left (resets 14:00)
        Weekly limit:   [██████████████████░░] 89% left (resets 09:00 on 14 Jan)
        Context window:   52% left (129K used / 258K)
    """
    result = {
        "5h_left": None,
        "weekly_left": None,
        "context_left": None,
        "5h_resets": None,
        "weekly_resets": None,
    }

    lines = text.split('\n')

    for i, line in enumerate(lines):
        # Find lines with "NN% left" or "NN% used"
        m = re.search(r'(\d{1,3})%\s*(?:left|used)', line, re.IGNORECASE)
        if not m:
            continue

        pct = int(m.group(1))
        is_left = 'left' in m.group(0).lower()
        value = pct if is_left else (100 - pct)

        # Classify by keywords in this line and nearby lines
        # Check current line + up to 3 lines above for context
        context = line.lower()
        for j in range(max(0, i - 3), i):
            context += ' ' + lines[j].lower()

        # Extract reset time from parentheses on same line: (resets HH:MM) or (resets HH:MM on DD Mon)
        reset_str = None
        reset_m = re.search(r'resets\s+(\d{1,2}:\d{2})\s+on\s+(\d{1,2})\s+(\w+)', line, re.IGNORECASE)
        if reset_m:
            reset_str = _resolve_weekly_reset(reset_m.group(1), int(reset_m.group(2)), reset_m.group(3))
        else:
            reset_m = re.search(r'resets\s+(\d{1,2}:\d{2})', line, re.IGNORECASE)
            if reset_m:
                reset_str = _resolve_hourly_reset(reset_m.group(1))

        # Also check the line below for reset info
        if not reset_str and i + 1 < len(lines):
            next_line = lines[i + 1]
            reset_m = re.search(r'resets\s+(\d{1,2}:\d{2})\s+on\s+(\d{1,2})\s+(\w+)', next_line, re.IGNORECASE)
            if reset_m:
                reset_str = _resolve_weekly_reset(reset_m.group(1), int(reset_m.group(2)), reset_m.group(3))
            else:
                reset_m = re.search(r'resets\s+(\d{1,2}:\d{2})', next_line, re.IGNORECASE)
                if reset_m:
                    reset_str = _resolve_hourly_reset(reset_m.group(1))

        # Classify section
        if '5h' in context or '5 hour' in context or 'five hour' in context or 'session' in context:
            if result["5h_left"] is None:
                result["5h_left"] = value
                if reset_str:
                    result["5h_resets"] = reset_str
        elif 'week' in context:
            if result["weekly_left"] is None:
                result["weekly_left"] = value
                if reset_str:
                    result["weekly_resets"] = reset_str
        elif 'context' in context:
            if result["context_left"] is None:
                result["context_left"] = value

    return result


def _resolve_hourly_reset(time_str: str) -> str:
    """Resolve 'HH:MM' to a full datetime string. If past today, use tomorrow."""
    now = datetime.now()
    hour, minute = map(int, time_str.split(':'))
    dt = now.replace(hour=hour, minute=minute, second=0, microsecond=0)
    if dt <= now:
        dt += timedelta(days=1)
    return dt.strftime("%Y-%m-%d %H:%M")


def _resolve_weekly_reset(time_str: str, day: int, month_str: str) -> str | None:
    """Resolve 'HH:MM on DD Mon' to a full datetime string."""
    months = {
        'jan': 1, 'feb': 2, 'mar': 3, 'apr': 4, 'may': 5, 'jun': 6,
        'jul': 7, 'aug': 8, 'sep': 9, 'oct': 10, 'nov': 11, 'dec': 12,
        'january': 1, 'february': 2, 'march': 3, 'april': 4,
        'june': 6, 'july': 7, 'august': 8, 'september': 9,
        'october': 10, 'november': 11, 'december': 12,
    }
    month = months.get(month_str.lower()[:3])
    if not month:
        return None
    hour, minute = map(int, time_str.split(':'))
    now = datetime.now()
    year = now.year
    if month < now.month or (month == now.month and day < now.day):
        year += 1
    try:
        dt = datetime(year, month, day, hour, minute)
        return dt.strftime("%Y-%m-%d %H:%M")
    except ValueError:
        return None


def strip_ansi(text: str) -> str:
    """Remove ANSI escape codes and null bytes from text."""
    ansi_escape = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')
    text = ansi_escape.sub('', text)
    text = text.replace('\x00', ' ')
    return text


async def get_screen_text(session) -> str:
    """Get all text currently visible in the session."""
    contents = await session.async_get_screen_contents()
    lines = []
    for i in range(contents.number_of_lines):
        line = contents.line(i)
        lines.append(line.string)
    return '\n'.join(lines)


async def main(connection):
    app = await iterm2.app.async_get_app(connection)

    # Get the session where this script was launched from
    session_id = os.environ.get('ITERM_SESSION_ID')
    if not session_id:
        print(json.dumps({"error": "No ITERM_SESSION_ID found - must run in iTerm2"}), file=sys.stderr)
        sys.exit(1)

    # Extract the actual session ID (format: w0t0p0:actual-session-id)
    if ':' in session_id:
        session_id = session_id.split(':', 1)[1]

    # Find the session and its window
    current_session = app.get_session_by_id(session_id)
    if not current_session:
        print(json.dumps({"error": "Session not found"}), file=sys.stderr)
        sys.exit(1)

    # Find which window contains this session
    window = None
    for w in app.terminal_windows:
        for tab in w.tabs:
            if current_session in tab.sessions:
                window = w
                break
        if window:
            break

    if window is None:
        print(json.dumps({"error": "Window not found"}), file=sys.stderr)
        sys.exit(1)

    # Find the current tab so we can switch back to it
    original_tab = None
    for tab in window.tabs:
        if current_session in tab.sessions:
            original_tab = tab
            break

    # Create a new tab running codex via wrapper
    new_tab = await window.async_create_tab(command=CODEX_WRAPPER)
    if not new_tab:
        print(json.dumps({"error": "Failed to create tab"}), file=sys.stderr)
        sys.exit(1)

    # Immediately switch back to the original tab to avoid stealing focus
    if original_tab:
        await original_tab.async_select()

    # Get the session in the new tab
    new_session = new_tab.current_session

    try:
        # Wait for codex to start up (needs time to load rate limit data)
        await asyncio.sleep(3)

        async def try_get_status(attempt: int) -> dict:
            """Send /status and capture result."""
            # Clear any existing text and send /status command
            await new_session.async_send_text("\x15")  # Ctrl+U to clear line
            await asyncio.sleep(0.1)
            await new_session.async_send_text("/status")
            await asyncio.sleep(0.1)
            await new_session.async_send_text("\r")

            # Wait for /status to execute and render
            await asyncio.sleep(2)

            # Capture screen contents
            screen_text = await get_screen_text(new_session)
            clean_text = strip_ansi(screen_text)

            # Debug: write raw output to secure temp file (only if debug mode enabled)
            debug_file = None
            if DEBUG_MODE:
                try:
                    fd, debug_file = tempfile.mkstemp(prefix='rcodex_status_', suffix='.txt')
                    with os.fdopen(fd, 'w') as f:
                        f.write(f"=== ATTEMPT {attempt} ===\n")
                        f.write("=== RAW SCREEN ===\n")
                        f.write(screen_text)
                        f.write("\n\n=== CLEANED ===\n")
                        f.write(clean_text)
                except OSError:
                    debug_file = None

            # Parse the status
            status = parse_status_output(clean_text)
            if debug_file:
                status["_debug"] = debug_file
            return status

        # First attempt
        status = await try_get_status(1)

        # If data not available, wait and retry once
        if status["5h_left"] is None:
            await asyncio.sleep(5)
            status = await try_get_status(2)

        # Output result
        print(json.dumps(status))

    finally:
        # Close the tab
        try:
            await new_tab.async_close()
        except Exception as e:
            print(f"Warning: Failed to close iTerm2 tab: {e}", file=sys.stderr)


if __name__ == "__main__":
    iterm2.run_until_complete(main)
