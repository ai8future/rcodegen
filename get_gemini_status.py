#!/usr/bin/env python3
"""
get_gemini_status.py - Capture Gemini CLI usage status using iTerm2 API

Creates a temporary tab, runs gemini, sends /usage, captures output,
parses credit percentages, and outputs JSON.

Usage:
    python3 get_gemini_status.py

Output (JSON to stdout):
    {"models": [{"name": "gemini-3-pro-preview", "pct_left": 100.0, "resets_in": "24h"}], ...}

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

# Debug mode - set RGEMINI_DEBUG=1 to enable debug output
DEBUG_MODE = os.environ.get('RGEMINI_DEBUG', '').lower() in ('1', 'true', 'yes')

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

# Wrapper script that sets up PATH and runs gemini
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
GEMINI_WRAPPER = os.path.join(SCRIPT_DIR, "gemini_wrapper.sh")


def parse_usage_output(text: str) -> dict:
    """Parse /usage output to extract model usage and reset times.

    Gemini /usage shows a table of models with usage remaining.
    Flexibly matches lines containing a percentage and optional reset info.

    Example lines:
        gemini-3-pro-preview    -    100.0% resets in 24h
        gemini-3-flash-preview  -    95.5%  resets in 23h
        gemini-2.5-pro          3    78.2%  resets in 18h 30m
    Also handles "resets in Xd Yh" format.
    """
    result = {
        "models": [],
        "tier": None,
    }

    lines = text.split('\n')

    for line in lines:
        # Look for tier/account info
        tier_m = re.search(r'(?:Account|Tier|Plan)[:\s]+(.+)', line, re.IGNORECASE)
        if tier_m and not result["tier"]:
            result["tier"] = tier_m.group(1).strip().rstrip('│').strip()

        # Find lines with "NN.N%" or "NN%" - model usage lines
        pct_m = re.search(r'(\d{1,3}(?:\.\d+)?)%', line)
        if not pct_m:
            continue

        pct = float(pct_m.group(1))

        # Check if this line contains a model name (gemini-*)
        model_m = re.search(r'(gemini[-\w.]+)', line, re.IGNORECASE)
        if not model_m:
            continue

        model_name = model_m.group(1)

        # Extract reset info: "resets in Xd Yh", "resets in Xh Ym", "resets in Xh"
        resets_in = None
        resets_iso = None
        reset_m = re.search(
            r'resets?\s+in\s+(\d+d\s*\d+h|\d+h\s*\d+m|\d+d|\d+h|\d+m)',
            line, re.IGNORECASE
        )
        if reset_m:
            resets_in = reset_m.group(1).strip()
            resets_iso = _resolve_duration(resets_in)

        result["models"].append({
            "name": model_name,
            "pct_left": pct,
            "resets_in": resets_in,
            "resets_iso": resets_iso,
        })

    return result


def _resolve_duration(duration_str: str) -> str | None:
    """Convert a duration like '24h', '18h 30m', '2d 5h' to an ISO datetime."""
    now = datetime.now()
    total = timedelta()

    days_m = re.search(r'(\d+)\s*d', duration_str)
    hours_m = re.search(r'(\d+)\s*h', duration_str)
    mins_m = re.search(r'(\d+)\s*m', duration_str)

    if days_m:
        total += timedelta(days=int(days_m.group(1)))
    if hours_m:
        total += timedelta(hours=int(hours_m.group(1)))
    if mins_m:
        total += timedelta(minutes=int(mins_m.group(1)))

    if total.total_seconds() == 0:
        return None

    dt = now + total
    return dt.strftime("%Y-%m-%dT%H:%M:%S")


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

    # Create a new tab running gemini via wrapper
    new_tab = await window.async_create_tab(command=GEMINI_WRAPPER)
    if not new_tab:
        print(json.dumps({"error": "Failed to create tab"}), file=sys.stderr)
        sys.exit(1)

    # Immediately switch back to the original tab to avoid stealing focus
    if original_tab:
        await original_tab.async_select()

    # Get the session in the new tab
    new_session = new_tab.current_session

    try:
        # Wait for gemini to start up
        await asyncio.sleep(3)

        async def try_get_usage(attempt: int) -> dict:
            """Send /usage and capture result."""
            # Clear any existing text and send /usage command
            await new_session.async_send_text("\x15")  # Ctrl+U to clear line
            await asyncio.sleep(0.1)
            await new_session.async_send_text("/usage")
            await asyncio.sleep(0.1)
            await new_session.async_send_text("\r")

            # Wait for /usage to execute and render
            await asyncio.sleep(2)

            # Capture screen contents
            screen_text = await get_screen_text(new_session)
            clean_text = strip_ansi(screen_text)

            # Debug: write raw output to secure temp file (only if debug mode enabled)
            debug_file = None
            if DEBUG_MODE:
                try:
                    fd, debug_file = tempfile.mkstemp(prefix='rgemini_status_', suffix='.txt')
                    with os.fdopen(fd, 'w') as f:
                        f.write(f"=== ATTEMPT {attempt} ===\n")
                        f.write("=== RAW SCREEN ===\n")
                        f.write(screen_text)
                        f.write("\n\n=== CLEANED ===\n")
                        f.write(clean_text)
                except OSError:
                    debug_file = None

            # Parse the usage
            status = parse_usage_output(clean_text)
            if debug_file:
                status["_debug"] = debug_file
            return status

        # First attempt
        status = await try_get_usage(1)

        # If no models found, wait and retry once
        if not status["models"]:
            await asyncio.sleep(5)
            status = await try_get_usage(2)

        # Output result
        print(json.dumps(status))

    finally:
        # Close the tab - send /quit first then close
        try:
            await new_session.async_send_text("/quit\r")
            await asyncio.sleep(0.5)
            await new_tab.async_close()
        except Exception as e:
            print(f"Warning: Failed to close iTerm2 tab: {e}", file=sys.stderr)


if __name__ == "__main__":
    iterm2.run_until_complete(main)
