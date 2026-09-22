---
name: research
description: Answer a question from external sources — official docs, API references, changelogs, upstream issues — instead of guessing. Use when the answer depends on a library version, a third-party API, or anything outside this repository, and when being wrong is expensive.
allowed-tools: WebSearch Fetch Glob Grep View
---

# research

Get the answer from the source. Your memory of an API is a hypothesis, not
documentation.

## When this is the right skill

- The question turns on a library, framework, or service that changes over time.
- You need an API's exact request/response shape, or a version's breaking change.
- The local code disagrees with what you believe about a dependency.

If the answer is in this repository, use `explore` — do not search the web for
something the code in front of you already states.

## Workflow

1. **Prefer authoritative sources, in this order:** the project's official docs,
   its API reference, its changelog or release notes, the source itself, then
   issues and discussions. A blog post is the last resort and should be labeled
   as such.
2. **Read the version that applies.** Check what this project actually pins
   (`go.mod`, lockfiles, vendor dirs) before trusting docs for `latest`.
3. **Fetch the primary page** rather than trusting a search snippet. Snippets
   lose the qualifiers that change the answer.
4. **Cross-check anything load-bearing.** If the whole design rests on one
   claim, confirm it in a second place.
5. **Tie it back to the code.** State how the finding applies here, with the
   local `path:line` it affects.

## Output

- The answer, stated directly first.
- The source URL for each non-obvious claim.
- The version the answer applies to.
- Anything you could not confirm, flagged as uncertain.
- Contradictions between sources, rather than silently picking one.