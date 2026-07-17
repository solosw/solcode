#!/usr/bin/env python3
"""PreToolUse — Bash safety guard (Python).

settings.json:
{
  "hooks": {
    "events": {
      "PreToolUse": [{
        "matcher": "Bash",
        "hooks": [{
          "type": "command",
          "command": "python examples/hooks/python/pre_tool_bash_guard.py",
          "timeout_ms": 5000,
          "fail_mode": "open"
        }]
      }]
    }
  }
}
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from lib.hook_io import allow, block, read_event  # noqa: E402

DENY = [
    re.compile(r"\brm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?/\b"),
    re.compile(r"\bformat\s+[a-z]:", re.I),
    re.compile(r"\b(curl|wget|Invoke-WebRequest)\b", re.I),
    re.compile(r"\bgit\s+push\s+.*--force\b", re.I),
    re.compile(r"\bdrop\s+database\b", re.I),
]


def main() -> None:
    event = read_event()
    command = str((event.get("tool_input") or {}).get("command") or "")
    for pattern in DENY:
        if pattern.search(command):
            block(f"Bash command blocked by pre_tool_bash_guard: {command}")
            return
    allow()


if __name__ == "__main__":
    main()
