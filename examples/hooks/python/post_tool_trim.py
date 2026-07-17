#!/usr/bin/env python3
"""PostToolUse — hard-trim oversized tool results (Python demo)."""

from __future__ import annotations

import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from lib.hook_io import allow, modify_result, read_event  # noqa: E402

MAX_CHARS = int(os.environ.get("SOLCODE_HOOK_TRIM_CHARS", "12000"))


def main() -> None:
    event = read_event()
    block = event.get("tool_result")
    if not isinstance(block, dict):
        allow()
        return
    if block.get("type") == "image" or block.get("is_error"):
        allow()
        return

    text = block.get("text") if isinstance(block.get("text"), str) else ""
    if len(text) <= MAX_CHARS:
        allow()
        return

    head = MAX_CHARS * 7 // 10
    tail = max(MAX_CHARS - head - 80, 0)
    trimmed = (
        text[:head]
        + f"\n\n... [post_tool_trim: omitted {len(text) - MAX_CHARS} chars] ...\n\n"
        + (text[-tail:] if tail else "")
    )
    modify_result(
        {
            "type": "text",
            "text": trimmed,
            "is_error": bool(block.get("is_error")),
        },
        f"trimmed tool result {len(text)}→{len(trimmed)} chars",
    )


if __name__ == "__main__":
    main()
