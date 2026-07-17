---
name: review
description: Review code changes and report concrete findings with file references.
---

# Review Skill

Use this skill when the user asks for a code review.

Consult `references/README.md` for the review order, evidence standard, and reporting expectations.

Checklist:

1. Check correctness first.
2. Then check safety and edge cases.
3. Then check maintainability.
4. Report concrete file paths and line references when possible.
5. Prefer minimal changes over large refactors.

Output format:

- Summary
- Findings
- Suggested fixes
