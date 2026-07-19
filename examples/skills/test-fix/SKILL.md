---
name: test-fix
description: Diagnose and fix failing tests. Use when tests fail or the user asks to make tests pass.
---

# test-fix

Diagnose and fix failing tests with a tight loop: reproduce → root cause → minimal fix → re-run.

## When to use

- CI or local tests failed
- Args may include package path, test name, or a paste of failure output

## Steps

1. Reproduce with the project’s test command (detect from files: `go test`, `npm test`, `pytest`, etc.).
   - Prefer the smallest scope: single package / single test name.
2. Read the failing test and the code under test.
3. Decide: test is wrong vs product bug vs flaky env.
4. Apply the **smallest** fix (prefer product correctness; only change the test if it asserted incorrect behavior).
5. Re-run the same scoped tests until green.
6. If failure is environmental (missing env/service), document requirements instead of weakening assertions.

## Output format

```markdown
## Failure
(short quote of the error)

## Root cause
…

## Fix
- files changed: …
- why this is minimal: …

## Verification
(command + pass/fail)
```

## Rules

- Do not skip or delete tests to “make CI green” without user approval.
- Do not broaden refactors while fixing a failure.
- Prefer deterministic fixes over sleeps/retries.
