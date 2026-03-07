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
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo

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


def _find_sections(text: str) -> list[dict]:
    """Split screen text into sections based on percentage-used lines.

    Instead of matching rigid section headers, we find every line containing
    'N% used' and walk backwards/forwards to grab context. This survives
    header renames, reordering, and extra whitespace/punctuation.

    Returns a list of dicts with keys:
        label  - lowercased text of the header line (e.g. 'current session')
        pct    - integer percent used (0-100)
        resets - raw reset string if found, else None
        tz     - timezone string if found, else None
    """
    lines = text.split('\n')
    sections = []

    for i, line in enumerate(lines):
        # Find lines matching "NN% used" anywhere
        m = re.search(r'(\d{1,3})%\s*used', line, re.IGNORECASE)
        if not m:
            continue

        pct = int(m.group(1))

        # Walk backwards to find the nearest non-empty, non-bar line as label
        label = ''
        for j in range(i - 1, max(i - 5, -1), -1):
            candidate = lines[j].strip()
            # Skip empty lines and lines that are just progress bars
            if not candidate or re.fullmatch(r'[█▌▐▏▎▍▋▊▉\s]+', candidate):
                continue
            label = candidate.lower()
            break

        # Walk forward to find "Resets ..." line
        resets_raw = None
        tz = None
        for j in range(i + 1, min(i + 4, len(lines))):
            rm = re.search(r'resets\s+(.+)', lines[j], re.IGNORECASE)
            if rm:
                resets_raw = rm.group(1).strip()
                # Extract timezone from parentheses
                tz_m = re.search(r'\(([^)]+)\)', resets_raw)
                if tz_m:
                    tz = tz_m.group(1).strip()
                    # Remove the timezone part from the display string
                    resets_raw = resets_raw[:tz_m.start()].strip().rstrip(',')
                break

        sections.append({
            'label': label,
            'pct': pct,
            'resets': resets_raw,
            'tz': tz,
        })

    return sections


def _classify_section(label: str) -> str | None:
    """Map a section label to a known category using fuzzy keyword matching.

    Returns 'session', 'weekly_all', 'weekly_sonnet', or None.
    """
    l = label.lower()
    if 'session' in l:
        return 'session'
    if 'week' in l:
        if 'sonnet' in l:
            return 'weekly_sonnet'
        # "all models" or just "week" without "sonnet"
        return 'weekly_all'
    return None


def _parse_reset_time(raw: str, tz_name: str | None) -> str | None:
    """Parse a reset time string into an ISO 8601 datetime.

    Handles formats like:
        '7am'
        '11pm'
        'Mar 13 at 8am'
        'Mar 13, 8am'
        'Jan 15 9am'
        'March 13 at 8am'
        'Mar 13'
    """
    if not raw:
        return None

    now = datetime.now(timezone.utc)

    # Resolve timezone
    tz = None
    if tz_name:
        try:
            tz = ZoneInfo(tz_name)
        except (KeyError, Exception):
            pass

    if tz:
        now_local = now.astimezone(tz)
    else:
        # Fall back to system local time
        now_local = now.astimezone()
        tz = now_local.tzinfo

    # Normalize: strip punctuation noise, collapse whitespace
    s = raw.strip().rstrip(',').strip()
    s = re.sub(r'\s+', ' ', s)

    # Try: "Mon DD [at] HHam/pm" or "Month DD [at] HHam/pm"
    m = re.match(
        r'([A-Za-z]+)\s+(\d{1,2}),?\s*(?:at\s+)?(\d{1,2})\s*(am|pm)',
        s, re.IGNORECASE
    )
    if m:
        month_str, day_str, hour_str, ampm = m.groups()
        hour = int(hour_str)
        if ampm.lower() == 'pm' and hour != 12:
            hour += 12
        elif ampm.lower() == 'am' and hour == 12:
            hour = 0

        # Parse month name
        month = _parse_month(month_str)
        if month:
            day = int(day_str)
            # Use current year, but if the date is in the past, use next year
            year = now_local.year
            try:
                dt = datetime(year, month, day, hour, 0, 0, tzinfo=tz)
                if dt < now_local - timedelta(hours=1):
                    dt = datetime(year + 1, month, day, hour, 0, 0, tzinfo=tz)
                return dt.isoformat()
            except ValueError:
                pass

    # Try: just "HHam/pm"
    m = re.match(r'(\d{1,2})\s*(am|pm)', s, re.IGNORECASE)
    if m:
        hour_str, ampm = m.groups()
        hour = int(hour_str)
        if ampm.lower() == 'pm' and hour != 12:
            hour += 12
        elif ampm.lower() == 'am' and hour == 12:
            hour = 0

        dt = now_local.replace(hour=hour, minute=0, second=0, microsecond=0)
        # If this time already passed today, it means tomorrow
        if dt <= now_local:
            dt += timedelta(days=1)
        return dt.isoformat()

    # Try: "Mon DD" without time (assume midnight)
    m = re.match(r'([A-Za-z]+)\s+(\d{1,2})', s, re.IGNORECASE)
    if m:
        month_str, day_str = m.groups()
        month = _parse_month(month_str)
        if month:
            day = int(day_str)
            year = now_local.year
            try:
                dt = datetime(year, month, day, 0, 0, 0, tzinfo=tz)
                if dt < now_local - timedelta(hours=1):
                    dt = datetime(year + 1, month, day, 0, 0, 0, tzinfo=tz)
                return dt.isoformat()
            except ValueError:
                pass

    return None


def _parse_month(s: str) -> int | None:
    """Parse a month name or abbreviation to a month number (1-12)."""
    months = {
        'jan': 1, 'feb': 2, 'mar': 3, 'apr': 4, 'may': 5, 'jun': 6,
        'jul': 7, 'aug': 8, 'sep': 9, 'oct': 10, 'nov': 11, 'dec': 12,
        'january': 1, 'february': 2, 'march': 3, 'april': 4,
        'june': 6, 'july': 7, 'august': 8, 'september': 9,
        'october': 10, 'november': 11, 'december': 12,
    }
    return months.get(s.lower())


def parse_status_output(text: str) -> dict:
    """Parse /status output to extract credit percentages and reset times.

    Uses a section-based approach: finds every 'N% used' line, walks
    backwards for the section header, and forwards for the reset time.
    This is resilient to layout changes, reordering, extra whitespace,
    and minor wording changes.
    """
    result = {
        "session_left": None,
        "weekly_all_left": None,
        "weekly_sonnet_left": None,
        "session_resets": None,
        "weekly_resets": None,
        "session_resets_iso": None,
        "weekly_resets_iso": None,
    }

    sections = _find_sections(text)

    for sec in sections:
        category = _classify_section(sec['label'])
        if not category:
            continue

        left = 100 - sec['pct']

        if category == 'session':
            result["session_left"] = left
            if sec['resets']:
                result["session_resets"] = sec['resets']
                result["session_resets_iso"] = _parse_reset_time(sec['resets'], sec['tz'])
        elif category == 'weekly_all':
            result["weekly_all_left"] = left
            if sec['resets']:
                result["weekly_resets"] = sec['resets']
                result["weekly_resets_iso"] = _parse_reset_time(sec['resets'], sec['tz'])
        elif category == 'weekly_sonnet':
            result["weekly_sonnet_left"] = left

    return result


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
