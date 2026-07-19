---
name: pr
description: Draft a pull-request title and body from the current branch changes. Use when the user asks for a PR description.
---

# pr

Draft a pull-request title and description from branch commits and the diff vs base.

## When to use

- User asks for a PR description, merge request text, or release note for a branch
- Args may name the base branch (default: `main`, then `master`)

## Steps

1. Detect current branch (`git branch --show-current`).
2. Resolve base: Args → `main` → `master` → upstream default if available.
3. Collect:
   - `git log --oneline <base>..HEAD`
   - `git diff <base>...HEAD` (three-dot when possible)
4. Group commits into themes; ignore pure merge noise.
5. Call out migrations, config flags, and test plan.

## Output format

```markdown
## Title
<concise PR title>

## Summary
…

## Changes
- …

## Test plan
- [ ] …

## Risks / rollout
…
```

If the branch is empty vs base, say so and stop.
