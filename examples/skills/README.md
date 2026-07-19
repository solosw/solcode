# Skill examples

Skills are reusable **Agent Skills** packages loaded from directories. When the
model (or you via `/skillname`) invokes a skill, solcode injects the skill
instructions (and a listing of bundled resources) into the conversation as
tool output.

## Package layout

```
skills/
  review/
    SKILL.md          # required: YAML frontmatter + instructions
    scripts/          # optional: executable helpers
    references/       # optional: detail docs loaded on demand
    assets/           # optional: templates / static inputs
  commit/
    SKILL.md
  notes.md            # standalone skill (name = file stem); no resource dirs
```

`SKILL.md` frontmatter (Agent Skills spec):

```markdown
---
name: review
description: What it does and when to use it (shown in the system prompt catalog)
license: Apache-2.0          # optional
allowed-tools: Bash Read     # optional, experimental
---

# Instructions the model follows after Skill is invoked
```

Discovery uses `name` + `description` in the system prompt. Invoking the Skill
tool loads the body and lists `scripts/` / `references/` / `assets/` so the
model can open them on demand (progressive disclosure).

### Resource paths

Skill resources are always relative to the skill package root, even if the
package lives under `~/.solcode/skills` rather than the active project. Skill
output lists both the relative path and an absolute path. Tools also resolve
`references/...`, `scripts/...`, and `assets/...` against the currently active
skill before looking in the project work directory. Use absolute paths whenever
more than one skill could contain the same resource path.

## Discovery paths

Default scan paths (if `skills.paths` is empty after normalize):

- `~/.solcode/skills`
- `~/.solcode/my-skill` (legacy)
- `<workdir>/.solcode/skills`
- `<workdir>/.solcode/my-skill` (legacy)

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

Empty `enabled` = all discovered skills available. Use `disabled` to hide names.

## Invoke

- TUI: `/review`, `/commit fix login flaky test`, `/skills`
- Model: `Skill` tool with `{ "skill": "review", "args": "internal/hook" }`

Tool output includes:

- `Args`, `Root`, optional `Allowed-tools`
- `## Instructions` (body without frontmatter)
- `## Bundled resources` (relative paths under Root)

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

1. Put a clear `description` in frontmatter (what + when); this is the discovery signal.
2. Keep `SKILL.md` instructions short; move long checklists to `references/`.
3. Put deterministic helpers in `scripts/` and tell the model when to run them.
4. Prefer project-local skills for repo conventions; user-global for personal workflows.
