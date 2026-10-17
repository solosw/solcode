---
name: computer-use
description: Operate desktop apps through the embedded open-computer-use native runtime (accessibility tree, screenshots, semantic actions). Requires computer_use.enabled and backend=open-computer-use.
allowed-tools: ComputerUse ToolSearch
---

# computer-use

Use **ComputerUse**, backed by a persistent native MCP process. The old robotgo
screenshot/mouse API is disabled; do not use its `screenshot`, `screen_info`,
`type`, `key`, `return_screenshot`, or top-level coordinate parameters.

## Prerequisites

- Settings: `"computer_use": {"enabled": true, "backend": "open-computer-use"}`.
- Release builds embed the native runtime. Source builds can prepare it with
  `python3 scripts/prepare-computer-use.py --os <os> --arch <arch>` before building,
  or set `computer_use.binary` to an installed native executable.
- No robotgo build tag, CGO, npm, or Node is required for the embedded runtime.
- macOS requires 14+ and Accessibility / Screen Recording permissions for Open
  Computer Use. Windows/Linux require a logged-in desktop; Linux also needs
  Python 3, GTK/AT-SPI and a functioning accessibility session. Do not change
  system permissions or install privileged helpers without user approval.
- If the schema is missing, use ToolSearch: `computer use desktop accessibility`.

## Call format and workflow

Call **ComputerUse directly**, not through BatchTool/PTC: snapshots and even
action results can include vision images, which those text-only wrappers cannot
forward. All native MCP arguments go in **arguments**, not at the top level:

```json
{"action":"list_apps"}
{"action":"get_app_state","arguments":{"app":"TextEdit"}}
{"action":"describe","arguments":{"tool":"click"}}
{"action":"click","arguments":{"app":"TextEdit","element_index":"0"}}
{"action":"type_text","arguments":{"app":"TextEdit","text":"Hello"}}
{"action":"press_key","arguments":{"app":"TextEdit","key":"Return"}}
```

1. Identify the target using `list_apps`. Start each interaction turn with
   `get_app_state` to obtain the current screenshot and accessibility tree.
2. Prefer indexed accessibility elements, and `set_value` for editable controls.
   Never guess element indices; refresh state after navigation or major UI changes.
3. For unfamiliar operations, use `describe` with `arguments.tool` to retrieve
   that operation's exact schema from the installed runtime. `describe` without
   arguments lists available native actions. Do not guess platform-specific fields.
4. Act once, then use `get_app_state` again to verify. Coordinates refer to the
   target app screenshot, not the old primary-screen coordinate system.
5. The process persists across calls; element indices are process-local. After a
   runtime restart or error, reacquire state instead of replaying a prior click.

Available actions include `list_apps`, `get_app_state`, `click`,
`perform_secondary_action`, `scroll`, `drag`, `type_text`, `press_key`, and
`set_value`; `query` is macOS-only. Runtime discovery is authoritative.

## Safety

- Treat this as the user's real desktop. Do not inspect unrelated private apps.
- Ask before sending, deleting, purchasing, approving, uploading, or entering secrets.
- Plan mode permits only describe/list_apps/query. Native get_app_state can
  start or activate an app even with keep placement, so it and other desktop
  actions require the normal mutating-tool approval path.
- Do not enable global-pointer fallback environment variables or install Locked
  Use privileged components automatically. Leave these opt-in upstream features off.
- Never retry an uncertain mutating action automatically; inspect state first.
