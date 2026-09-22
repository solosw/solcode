---
name: review
description: Critique an existing change — a diff, a branch, a pull request, or a file you just wrote — looking for bugs, regressions, missing cases, and scope creep. Use when the user asks for a review or critique, or before handing work back on a risky change. Reports findings; does not silently rewrite.
allowed-tools: Bash Glob Grep LS View Diff LSP
---

# review

Find what is actually wrong. A review that only summarizes the diff has told the
reader nothing they could not see themselves.

## Workflow

1. **Get the change under review.** For a branch or working tree, read the real
   diff (`git diff`, the `Diff` tool, or a PR patch). Review what changed, not
   the whole file.
2. **Read the surrounding code.** A diff line is only correct relative to the
   contract of the function it sits in. Check callers too.
3. **Check the categories that actually bite:**
   - **Correctness** — off-by-one, wrong operator, inverted condition, ignored
     error, nil/zero-value path, wrong unit or boundary.
   - **Regressions** — behavior that other callers depend on, changed silently.
   - **Missing cases** — the input class the code forgot (empty, huge, unicode,
     concurrent, error path).
   - **Resource handling** — unclosed handles, leaked goroutines, missing cancel.
   - **Tests** — do they exercise the change? Would they fail without it?
   - **Scope** — unrelated changes smuggled in with the fix.
4. **Verify before reporting.** Reproduce the bug you think you found, or cite
   the exact line and reasoning. A false alarm costs the author more than a
   missed nitpick.
5. **Rank by severity.** A correctness bug outranks a naming preference. Say
   which is which.

## Rules

- Findings, not a rewrite. Do not edit unless the user asked you to fix.
- Every finding needs `path:line` and why it is wrong.
- "This looks odd" is not a finding. State the input that breaks it.
- If the change is correct, say so plainly rather than inventing filler.

## Output

Grouped by severity:

- **Blocking** — will break something. Each with location, the failing case, and
  the reason.
- **Should fix** — real but not fatal.
- **Nit** — style, naming, wording.
- **Verified good** — what you checked and found correct, so the author knows
  the review had depth.