"""Shared helpers for solcode command hooks (Python).

Protocol:
  stdin  → one JSON object (hook.Event)
  stdout → one JSON object (hook.Result), or empty = allow
  stderr → free-form logs (ignored by solcode)
"""

from __future__ import annotations

import json
import sys
from typing import Any


def read_event() -> dict[str, Any]:
    raw = sys.stdin.read().strip()
    if not raw:
        return {}
    return json.loads(raw)


def reply(result: dict[str, Any] | None = None) -> None:
    sys.stdout.write(json.dumps(result or {"decision": "allow"}))


def allow(message: str | None = None) -> None:
    if message:
        reply({"decision": "allow", "message": message})
    else:
        reply({"decision": "allow"})


def block(message: str = "blocked by hook") -> None:
    reply({"decision": "block", "message": message})


def modify_input(tool_input: Any, message: str = "input modified by hook") -> None:
    reply(
        {
            "decision": "modify",
            "modified_input": tool_input,
            "message": message,
        }
    )


def modify_result(tool_result: Any, message: str = "result modified by hook") -> None:
    reply(
        {
            "decision": "modify",
            "modified_result": tool_result,
            "message": message,
        }
    )


def modify_prompt(prompt: str, message: str = "prompt modified by hook") -> None:
    reply(
        {
            "decision": "modify",
            "modified_prompt": prompt,
            "message": message,
        }
    )
