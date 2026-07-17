#!/usr/bin/env python3
"""UserPromptSubmit — prefix every user prompt (Python)."""

from __future__ import annotations

import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from lib.hook_io import allow, modify_prompt, read_event  # noqa: E402

PREFIX = os.environ.get(
    "SOLCODE_PROMPT_PREFIX",
    "[project note: prefer small diffs; run tests after edits]\n\n",
)


def main() -> None:
    event = read_event()
    prompt = str(event.get("prompt") or "")
    if not prompt or prompt.startswith(PREFIX.strip()):
        allow()
        return
    modify_prompt(PREFIX + prompt, "prefixed user prompt")


if __name__ == "__main__":
    main()
