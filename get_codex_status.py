#!/usr/bin/env python3
"""
get_codex_status.py - Capture Codex credit status using iTerm2 API

Creates a temporary tab, runs codex, sends /status, captures output,
parses credit percentages, and outputs JSON.

Usage:
    python3 get_codex_status.py

Output (JSON to stdout):
    {"5h_left": 64, "weekly_left": 89, "context_left": 52}
"""

import iterm2
import asyncio
import json
import re
import sys
import os
from datetime import datetime, timedelta

# Wrapper script that sets up PATH and runs codex
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
CODEX_WRAPPER = os.path.join(SCRIPT_DIR, "codex_wrapper.sh")


def parse_status_output(text: str) -> dict:
    """Parse /status output to extract credit percentages and reset times.

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
        "weekly_resets": None
    }

    # Pattern: look for "XX% left" after the label
    # Use DOTALL to handle any characters including unicode progress bars

    # 5h limit - find the percentage and reset time
    match = re.search(r'5h limit:[^\d]*(\d+)%\s*left[^(]*\(resets\s+(\d{1,2}:\d{2})\)', text, re.IGNORECASE | re.DOTALL)
    if match:
        result["5h_left"] = int(match.group(1))
        reset_time = match.group(2)
        # Interpolate date - if reset time has passed today, it's tomorrow
        now = datetime.now()
        reset_hour, reset_min = map(int, reset_time.split(':'))
        reset_dt = now.replace(hour=reset_hour, minute=reset_min, second=0, microsecond=0)
        if reset_dt <= now:
            reset_dt += timedelta(days=1)
        result["5h_resets"] = reset_dt.strftime("%Y-%m-%d %H:%M")

    # Weekly limit - has full date like "09:00 on 14 Jan"
    match = re.search(r'Weekly limit:[^\d]*(\d+)%\s*left[^(]*\(resets\s+(\d{1,2}:\d{2})\s+on\s+(\d{1,2})\s+(\w+)\)', text, re.IGNORECASE | re.DOTALL)
    if match:
        result["weekly_left"] = int(match.group(1))
        reset_time = match.group(2)
        reset_day = int(match.group(3))
        reset_month_str = match.group(4)
        # Parse month name
        months = {'jan': 1, 'feb': 2, 'mar': 3, 'apr': 4, 'may': 5, 'jun': 6,
                  'jul': 7, 'aug': 8, 'sep': 9, 'oct': 10, 'nov': 11, 'dec': 12}
        reset_month = months.get(reset_month_str.lower()[:3], 1)
        reset_hour, reset_min = map(int, reset_time.split(':'))
        now = datetime.now()
        reset_year = now.year
        # If month is earlier than current month, it's next year
        if reset_month < now.month or (reset_month == now.month and reset_day < now.day):
            reset_year += 1
        try:
            reset_dt = datetime(reset_year, reset_month, reset_day, reset_hour, reset_min)
            result["weekly_resets"] = reset_dt.strftime("%Y-%m-%d %H:%M")
        except ValueError:
            pass  # Invalid date, skip

    # Context window (may or may not have progress bar)
    match = re.search(r'context[^\d]*(\d+)%\s*left', text, re.IGNORECASE | re.DOTALL)
    if match:
        result["context_left"] = int(match.group(1))

    return result


def strip_ansi(text: str) -> str:
    """Remove ANSI escape codes from text."""
    ansi_escape = re.compile(r'\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])')
    return ansi_escape.sub('', text)


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
        print(json.dumps({"error": "No ITERM_SESSION_ID found"}), file=sys.stderr)
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

    # Create a new tab running codex via wrapper
    new_tab = await window.async_create_tab(command=CODEX_WRAPPER)
    if not new_tab:
        print(json.dumps({"error": "Failed to create tab"}), file=sys.stderr)
        sys.exit(1)

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

            # Debug: write raw output to temp file
            debug_file = "/tmp/rcodex_status_debug.txt"
            with open(debug_file, "w") as f:
                f.write(f"=== ATTEMPT {attempt} ===\n")
                f.write("=== RAW SCREEN ===\n")
                f.write(screen_text)
                f.write("\n\n=== CLEANED ===\n")
                f.write(clean_text)

            # Parse the status
            status = parse_status_output(clean_text)
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
        except:
            pass


if __name__ == "__main__":
    iterm2.run_until_complete(main)
