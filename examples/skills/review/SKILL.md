# review

Perform a focused code review of the current change set or a path given in Args.

## When to use

- User asks for a review, PR feedback, or “anything wrong here?”
- After a large edit session before commit

## Steps

1. Identify scope:
   - If Args names a path, review that path.
   - Else run `git status` / `git diff` (and `git diff --staged`) to find changes.
2. Read the changed files with View/Grep; do not re-litigate unrelated code.
3. Check for:
   - Correctness bugs and edge cases
   - Missing tests for new behavior
   - Security issues (injection, path traversal, secret leakage)
   - API / config compatibility breaks
   - Error handling and resource cleanup
4. Prefer evidence: cite file paths and short snippets; avoid vague advice.

## Output format

```markdown
## Summary
(1–3 sentences)

## Findings
### High
- …

### Medium
- …

### Low / nits
- …

## Test gaps
- …

## Suggested follow-ups
- …
```

If there are no high/medium issues, say so explicitly. Do not invent problems.
