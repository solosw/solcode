---
name: explore
description: Read-only reconnaissance of an unfamiliar codebase or subsystem: locate entry points, trace how a feature is wired, map structure before changing anything. Use when the task starts with "where is", "how does X work", "find all the places that", or when you must understand code before editing it. Does not modify files.
allowed-tools: Glob Grep LS View LSP ReadObservation
---

# explore

Build an accurate mental model of the code **before** proposing changes. This
skill is deliberately read-only.

## When this is the right skill

- The user asks where something lives, or how a feature is wired end to end.
- You are about to change code you have not read.
- You need to know every caller of a symbol before altering it.

If the user wants the change made, use `implement` instead and let it do its own
reconnaissance. If they want a design without edits, that is plan mode, not this.

## Workflow

1. **Orient.** `LS` the tree, then `Glob` for the language's file patterns to see
   the shape of the project. Read the README and any architecture docs.
2. **Find entry points.** Locate `main`, routes, handlers, or the exported API
   surface. `Grep` for the user's nouns, not for likely English descriptions.
3. **Trace the path.** Follow the call chain one hop at a time with `Grep` plus
   `LSP go_to_definition` / `find_references`. Prefer LSP over text search when
   the symbol is typed — it does not lie about scope.
4. **Read narrowly.** `View` with `offset`/`limit` around the interesting lines
   instead of whole files. A 3000-line file read whole is context you cannot use.
5. **Confirm with evidence.** Every claim you report should cite a
   `path:line`. If you could not confirm something, say so rather than guessing.

## Output

Report:
- The components involved and their `path:line` locations.
- The actual control/data flow between them, in order.
- Constraints you discovered (invariants, config gates, tests that pin behavior).
- Explicit unknowns, and what you would read next to resolve them.

Do not propose an implementation unless asked. Findings first.