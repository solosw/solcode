# commit

Draft a conventional commit message from the current git changes.

## When to use

- User asks to commit, write a commit message, or “summarize my diff”
- Args may include extra intent (e.g. `focus on tests only`)

## Steps

1. Run `git status`, `git diff`, and `git diff --staged`.
2. If nothing is staged but worktree has changes, say so and propose what to stage
   (`git add -p` / specific paths). Do **not** commit unless the user explicitly asked.
3. Infer type: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`, `ci`.
4. Keep subject ≤72 characters, imperative mood, no trailing period.
5. Body: why > what; mention breaking changes under `BREAKING CHANGE:` if needed.

## Output format

Provide a ready-to-paste message:

```text
<type>(optional-scope): <subject>

<body>

<footer>
```

Then offer the exact `git commit` command in a fenced block (message only; no
`--no-verify` unless the user requested it).

## Rules

- Never invent files that are not in the diff.
- Never put secrets from the diff into the message.
- Prefer one logical commit; if the diff mixes concerns, suggest a split.
