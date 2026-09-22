---
name: verify
description: Independently validate that a change actually works and did not break anything: run the real build and test commands, check the failure baseline, and report results honestly. Use before claiming any task is done, especially after edits to shared code.
allowed-tools: Bash Glob Grep LS View
---

# verify

Prove the change works. Verification is evidence, not a formality.

## Workflow

1. **Establish the baseline first, when a suite is already failing.** Run the
   tests before your change, or compare against a pristine checkout (for Go:
   `git worktree add ../baseline HEAD`). Knowing which failures predate you is
   the difference between a real regression report and noise.
2. **Build.** Run the project's compile step
   (Go: `go build ./...`). A test that never compiled proves nothing.
3. **Run the narrowest relevant test**, then widen: the package, then the repo.
4. **Run the static checks the project uses** (Go: `go vet ./...`,
   `gofmt -l`). Formatting drift is a real review failure.
5. **Report exactly what happened.** Paste command and result. Distinguish
   "passed", "failed", and "not run" — never blur them.

## Rules

- **Never claim success you did not observe.** If you did not run it, say so.
- **Distinguish pre-existing failures from regressions.** Quote the baseline.
- **Reproduce before fixing.** If a test fails, read the failure before changing
  code; guessing at a fix wastes a cycle.
- A passing test that does not exercise the change is not verification. Check
  that the test would fail without your edit.
- If verification is impossible (no toolchain, missing credentials, needs a
  device), say that plainly and describe what is therefore unverified.

## Output

- Exact commands run.
- Their result, including failing output verbatim enough to act on.
- What remains unverified and why.