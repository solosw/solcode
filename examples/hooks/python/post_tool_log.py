#!/usr/bin/env python3
"""PostToolUse — append JSONL audit log (Python)."""

from __future__ import annotations

import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from lib.hook_io import allow, read_event  # noqa: E402


def main() -> None:
    event = read_event()
    work_dir = event.get("work_dir") or os.getcwd()
    log_path = Path(
        os.environ.get("SOLCODE_HOOK_LOG")
        or Path(work_dir) / ".solcode" / "hooks-tool.jsonl"
    )
    result = event.get("tool_result") or {}
    text = ""
    if isinstance(result, dict) and isinstance(result.get("text"), str):
        text = result["text"]

    line = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "event": event.get("event"),
        "tool": event.get("tool_name"),
        "session_id": event.get("session_id"),
        "is_error": bool(result.get("is_error")) if isinstance(result, dict) else False,
        "text_chars": len(text),
        "text_preview": text[:200],
        "input": event.get("tool_input"),
    }
    try:
        log_path.parent.mkdir(parents=True, exist_ok=True)
        with log_path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(line, ensure_ascii=False) + "\n")
    except OSError as err:
        print(f"post_tool_log write failed: {err}", file=sys.stderr)
    allow()


if __name__ == "__main__":
    main()
