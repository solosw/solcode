---
name: implement
description: Make a code change end to end — add a feature, fix a bug, refactor — by locating the right place, editing it, and running the project's own verification. Use when the user wants code written or modified. Pair with verify when validation is non-trivial.
allowed-tools: Glob Grep LS View Edit MultiEdit Write MultiWrite Patch Bash
---

# implement

Change code so it does what was asked, in the style already there, with the
smallest diff that accomplishes it.

## Workflow

1. **Find the seam.** Locate the code that owns the behavior. Read it, plus its
   tests, before editing. The tests tell you the intended contract.
2. **Match the neighbors.** Copy the naming, error handling, and structure of
   the surrounding code. A new idiom in a file that already has one is a bug in
   review even when it works.
3. **Prefer targeted edits.** Use `Edit` / `MultiEdit` / `Write` operations in
   `BatchTool`: `Edit` for one change, `MultiEdit` when one file needs several,
   `Write` only for genuinely new files. Read unknown content in an earlier
   call; `depends_on` cannot build replacement text from read results.
   MultiEdit is atomic per file by default, not across all files. Retry only
   failed files after partial success. For changes that must succeed together,
   use the MultiEdit operation's `input.atomic: true`, or `BatchTool.atomic: true`
   for the whole transaction. After a confirmed rollback, reapply the corrected
   transaction rather than just the failed edit; if rollback failed or its state
   is unknown, inspect the current files before editing. Do not rewrite a file to
   change three lines.
4. **Change only what was asked.** No unrequested features, abstractions,
   error handling, or refactors. If you notice something unrelated, report it
   instead of fixing it silently.
5. **Verify before claiming success.** Run the narrowest test that covers the
   change, then the package tests. See the `verify` skill for the full loop.

## Rules

- Read a file before editing it. Never edit from memory of a similar file.
- Keep the diff reviewable. If the change is large, several small edits beat one
  rewrite.
- If the request is ambiguous in a way that changes the design, ask — do not
  pick silently and build on the guess.
- Report failures with their output. "Tests pass" when they did not is worse
  than reporting the failure.