#!/usr/bin/env python3
"""
get_claude_status.py - Capture Claude Max credit status using iTerm2 API

Creates a temporary tab, runs claude, sends /status, captures output,
parses credit percentages, and outputs JSON.

Usage:
    python3 get_claude_status.py

Output (JSON to stdout):
    {"session_left": 75, "weekly_all_left": 89, ...}

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

# Debug mode - set RCLAUDE_DEBUG=1 to enable debug output
DEBUG_MODE = os.environ.get('RCLAUDE_DEBUG', '').lower() in ('1', 'true', 'yes')

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

# Wrapper script that sets up PATH and runs claude
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
CLAUDE_WRAPPER = os.path.join(SCRIPT_DIR, "claude_wrapper.sh")


def parse_status_output(text: str) -> dict:
    """Parse /status output to extract credit percentages.

    Claude Max status shows (on Usage tab):
        Current session
        ████████████████████████▌                          49% used
        Resets 11am (Europe/Paris)

        Current week (all models)
        ██████▌                                            13% used
        Resets Jan 15, 9am (Europe/Paris)

        Current week (Sonnet only)
        █                                                  2% used
        Resets 8pm (Europe/Paris)
    """
    result = {
        "session_left": None,
        "weekly_all_left": None,
        "weekly_sonnet_left": None,
        "session_resets": None,
        "weekly_resets": None
    }

    # Current session: look for "Current session" followed by "XX% used"
    match = re.search(r'Current session[^\d]*(\d+)%\s*used', text, re.IGNORECASE | re.DOTALL)
    if match:
        result["session_left"] = 100 - int(match.group(1))

    # Current week (all models): look for pattern
    match = re.search(r'Current week\s*\(all models\)[^\d]*(\d+)%\s*used', text, re.IGNORECASE | re.DOTALL)
    if match:
        result["weekly_all_left"] = 100 - int(match.group(1))

    # Current week (Sonnet only)
    match = re.search(r'Current week\s*\(Sonnet only\)[^\d]*(\d+)%\s*used', text, re.IGNORECASE | re.DOTALL)
    if match:
        result["weekly_sonnet_left"] = 100 - int(match.group(1))

    # Session reset time - look for "Resets" after "Current session"
    # Format: "Resets 11am" or "Resets 11pm"
    session_section = re.search(r'Current session.*?Resets\s+(\d{1,2}(?:am|pm))', text, re.IGNORECASE | re.DOTALL)
    if session_section:
        result["session_resets"] = session_section.group(1)

    # Weekly reset - look for "Resets" after "Current week (all models)"
    # Format: "Resets Jan 15, 9am"
    weekly_section = re.search(r'Current week\s*\(all models\).*?Resets\s+([A-Za-z]+\s+\d+,?\s+\d{1,2}(?:am|pm))', text, re.IGNORECASE | re.DOTALL)
    if weekly_section:
        result["weekly_resets"] = weekly_section.group(1)

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

    # Create a new tab running claude via wrapper
    new_tab = await window.async_create_tab(command=CLAUDE_WRAPPER)
    if not new_tab:
        print(json.dumps({"error": "Failed to create tab"}), file=sys.stderr)
        sys.exit(1)

    # Immediately switch back to the original tab to avoid stealing focus
    if original_tab:
        await original_tab.async_select()

    # Get the session in the new tab
    new_session = new_tab.current_session

    try:
        # Wait for claude to start up
        await asyncio.sleep(4)

        async def try_get_status(attempt: int) -> dict:
            """Send /status and capture result."""
            # Clear any existing text and send /status command
            await new_session.async_send_text("\x15")  # Ctrl+U to clear line
            await asyncio.sleep(0.1)
            await new_session.async_send_text("/status")
            await asyncio.sleep(0.1)
            await new_session.async_send_text("\r")

            # Wait for /status to load
            await asyncio.sleep(1.5)

            # Navigate to Usage tab (Status -> Config -> Usage)
            # Send Tab twice to move to Usage tab
            await new_session.async_send_text("\t")  # Tab to Config
            await asyncio.sleep(0.3)
            await new_session.async_send_text("\t")  # Tab to Usage
            await asyncio.sleep(1)

            # Capture screen contents
            screen_text = await get_screen_text(new_session)
            clean_text = strip_ansi(screen_text)

            # Debug: write raw output to secure temp file (only if debug mode enabled)
            debug_file = None
            if DEBUG_MODE:
                try:
                    fd, debug_file = tempfile.mkstemp(prefix='rclaude_status_', suffix='.txt')
                    with os.fdopen(fd, 'w') as f:
                        f.write(f"=== ATTEMPT {attempt} ===\n")
                        f.write("=== RAW SCREEN ===\n")
                        f.write(screen_text)
                        f.write("\n\n=== CLEANED ===\n")
                        f.write(clean_text)
                except OSError:
                    # If write fails, continue without debug
                    debug_file = None

            # Parse the status
            status = parse_status_output(clean_text)
            if debug_file:
                status["_debug"] = debug_file
            return status

        # First attempt
        status = await try_get_status(1)

        # If data not available, wait and retry once
        if status["session_left"] is None and status["weekly_all_left"] is None:
            await asyncio.sleep(3)
            status = await try_get_status(2)

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
