# Review Reference

This reference defines how the `review` skill should inspect code and report findings.

## Review Order

1. Correctness
2. Safety and edge cases
3. Maintainability
4. Test coverage proportional to risk

## What To Look For

### Correctness

- Behavioral regressions
- Broken assumptions across call sites
- Invalid state transitions
- Off-by-one, nil, empty, and fallback issues
- API or config mismatches

### Safety And Edge Cases

- Missing validation
- Error handling gaps
- Destructive behavior without guardrails
- Concurrency or ordering issues
- Unexpected behavior on Windows, macOS, or Linux when relevant

### Maintainability

- New code fighting existing local patterns
- Hidden coupling
- Overly broad changes for a narrow request
- Missing comments around non-obvious logic

## Output Expectations

Lead with findings, ordered by severity.

Each finding should include:

- Severity
- Short title
- Why it is a problem
- Concrete file path and line reference when available
- Recommended fix or direction

If there are no findings, say so clearly and mention any remaining test gaps or residual risk.

## Review Style

- Be direct and specific.
- Prefer evidence over speculation.
- Avoid broad refactor advice unless it is required to fix the issue.
- Keep summaries brief; findings come first.
