---
name: computer-use
description: Desktop GUI automation with the ComputerUse tool (screenshot, mouse, keyboard). Use when the user wants to operate apps on screen, click UI, type into windows, or verify visual state. Requires computer_use.enabled in settings.
allowed-tools: ComputerUse ToolSearch ViewImage
---

# computer-use

Drive the local desktop with the **ComputerUse** tool. Prefer a screenshot-first loop so you can see the UI before and after each action.

## Prerequisites

- Settings must include `"computer_use": { "enabled": true }`.
- Rebuild solcode with CGO and the robotgo build tag, e.g. `CGO_ENABLED=1 go build -tags computeruse -o solcode ./cmd/solcode` (Windows needs a working MinGW/gcc toolchain).
- ComputerUse is **not** a core tool. After this skill loads, it should become sticky for the session; if the schema is still missing, call **ToolSearch** with query `computer use screenshot mouse keyboard`.

## Actions

| action | purpose | notes |
|--------|---------|--------|
| `screen_info` | primary screen size | read-only |
| `screenshot` | capture primary screen (or region) | read-only; returns a vision image |
| `click` / `double_click` / `right_click` | mouse click at `x`,`y` | destructive |
| `move` | move pointer | destructive |
| `drag` | drag from `x`,`y` to `to_x`,`to_y` | destructive |
| `type` | type `text` | destructive; focus the field first |
| `key` | tap `key` with optional `modifiers` | e.g. `enter`, `ctrl`+`c` |
| `scroll` | scroll with `scroll_dx` / `scroll_dy` | optional focus `x`,`y` |

Optional: `return_screenshot=true` after a mutating action; `save_path` to write under the workdir.

## Workflow

1. Call `screen_info` if you need bounds.
2. `screenshot` and locate the target from the image (pixel coordinates, origin top-left on the **primary** display).
3. Perform the smallest action (`click` / `type` / `key`).
4. `screenshot` again (or `return_screenshot`) to verify.
5. Repeat until the user goal is met. Do not thrash clicks.

## Safety

- Plan mode: only `screenshot` / `screen_info` are allowed.
- Auto mode: mutating actions require permission approval.
- Avoid entering secrets into untrusted UI. Prefer app-native automation / files when possible.
- Multi-monitor / DPI scaling can shift coordinates; if clicks miss, re-screenshot and adjust.

## Args

Optional Args may name the app or goal (e.g. `open Notepad and type hello`). Use them as the success criteria.
