# solcode examples

End-to-end samples for configuration, skills, and hooks.

| Area | Path | What it shows |
|------|------|----------------|
| **Settings** | [`settings/`](settings/) | Model / provider / permissions / MCP / skills / hooks in `settings.json` |
| **Skills** | [`skills/`](skills/) | Reusable markdown workflows (`SKILL.md` or `name.md`) |
| **Hooks** | [`hooks/`](hooks/) | Event hooks in **Node**, **Python**, **Bash**, **PowerShell**, **Go** |

## Quick start

1. Copy a settings template into your user or project config:

```bash
# user-wide
mkdir -p ~/.solcode
cp examples/settings/settings.minimal.example.json ~/.solcode/settings.json

# or project-local (merged later, overrides user)
mkdir -p .solcode
cp examples/settings/settings.full.example.json .solcode/settings.json
```

2. Point skills at the sample directory (or copy into `~/.solcode/skills` / `.solcode/skills`):

```json
"skills": {
  "paths": ["examples/skills"]
}
```

3. Merge a hooks block from `examples/hooks/settings.hooks.example.json` (or language-specific snippets in `hooks/README.md`).

4. Run:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
solcode -workdir .
# invoke a skill from TUI: /review
# or let the model call the Skill tool
```

## Config discovery order

Later files merge on top of earlier ones:

1. `~/.solcode/settings.json`
2. `~/.solcode/settings.local.json` (runtime / secrets; preferred for API keys)
3. `./.solcode/settings.json`
4. `./.solcode/settings.local.json`

Also: `-config path/to.json` updates that explicit file for TUI custom providers/models.

## Layout

```
examples/
  README.md
  settings/
    settings.minimal.example.json
    settings.multimodel.example.json
    settings.full.example.json
  skills/
    README.md
    review/SKILL.md
    commit/SKILL.md
    pr/SKILL.md
    test-fix/SKILL.md
  hooks/
    README.md
    settings.hooks.example.json   # multi-language sample block
    lib/… + *.mjs                 # Node (existing)
    python/
    bash/
    powershell/
    go/
  config.multimodel.json          # legacy alias → see settings/multimodel
```

See each subdirectory README for protocol details and dry-run commands.
