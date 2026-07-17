# Skill examples

Skills are reusable markdown workflows loaded from directories. When the model
(or you via `/skillname`) invokes a skill, solcode injects the file contents
into the conversation as tool output.

## Discovery

Default scan paths (if `skills.paths` is empty after normalize):

- `~/.solcode/skills`
- `~/.solcode/my-skill` (legacy)
- `<workdir>/.solcode/skills`
- `<workdir>/.solcode/my-skill` (legacy)

Layout options:

```
skills/
  review/
    SKILL.md          # skill name = directory name → "review"
  commit/
    SKILL.md
  notes.md            # skill name = file stem → "notes"
```

## settings.json

```json
{
  "skills": {
    "paths": ["examples/skills", ".solcode/skills"],
    "enabled": [],
    "disabled": []
  }
}
```

Empty `enabled` = all discovered skills available. Use `disabled` to hide names
(when the runtime filter is enabled in your build).

## Invoke

- TUI: `/review`, `/commit fix login flaky test`, `/skills`
- Model: `Skill` tool with `{ "skill": "review", "args": "internal/hook" }`

`args` are prepended as `Args: …` above the skill body.

## Samples in this folder

| Skill | Purpose |
|-------|---------|
| [`review/`](review/SKILL.md) | Structured code review |
| [`commit/`](commit/SKILL.md) | Conventional commit message from diff |
| [`pr/`](pr/SKILL.md) | Pull-request title + body |
| [`test-fix/`](test-fix/SKILL.md) | Diagnose and fix failing tests |

Copy into user/project skills:

```bash
mkdir -p ~/.solcode/skills
cp -r examples/skills/review examples/skills/commit ~/.solcode/skills/
```

## Authoring tips

1. Start with a one-line goal and when to use the skill.
2. List concrete steps and which tools to call (Bash, Grep, Edit…).
3. Define output format (bullet list, commit subject ≤72 chars, etc.).
4. Keep skills under ~2–4 KB; put long checklists in linked project docs if needed.
5. Prefer project-local skills for repo conventions; user-global for personal workflows.
