# Workflow examples

Workflows are **user-authored Task graphs** (YAML). They are invoked **explicitly**
via slash commands — they are **not** exposed to the model as tools, are **blocked
in plan mode**, and are **not** written into memory.

| Concept | Who authors | How it runs |
|---------|-------------|-------------|
| **Skill** | Markdown playbook | Model/slash injects text; main agent follows |
| **Task** | Model (ad-hoc JSON) | Sub-agent DAG right now |
| **Workflow** | You (YAML on disk) | `/workflow name args` → same Task DAG runner |

## Discovery

Default paths (merged with `workflows.paths`):

- `~/.solcode/workflows`
- `<workdir>/.solcode/workflows`

Layouts:

```
workflows/
  test-then-review/
    workflow.yaml      # name defaults to directory name
  parallel-explore.yaml
```

Supported extensions: `.yaml`, `.yml`, `.json`.

## settings.json

```json
{
  "workflows": {
    "paths": ["examples/workflows", ".solcode/workflows"],
    "enabled": [],
    "disabled": []
  }
}
```

Empty `enabled` = all discovered workflows. `disabled` hides names.

## Invoke

- List: `/workflows`
- Run: `/workflow test-then-review focus on session package`
- Args are substituted into prompts as `{{args}}`

## Authoring

```yaml
name: my-flow
description: One-line purpose
execution_mode: auto   # optional; same semantics as Task tool
tasks:
  - id: a
    description: Short label
    prompt: |
      Do work. User args: {{args}}
    difficulty: easy          # easy → fast model when configured
    allowed_tools: [View, Grep]
  - id: b
    description: Depends on a
    prompt: Continue from a…
    depends_on: [a]
    difficulty: hard
```

Rules:

1. Every task needs `description` and `prompt`.
2. `id` should be unique; used by `depends_on`.
3. Cycles are rejected at load/run time.
4. Independent tasks run in parallel levels (same as Task).

## Samples

| Workflow | Purpose |
|----------|---------|
| [`test-then-review/`](test-then-review/workflow.yaml) | Tests then failure review |
| [`parallel-explore/`](parallel-explore/workflow.yaml) | Parallel code+docs explore, then merge |

Copy into project workflows:

```bash
mkdir -p .solcode/workflows
cp -r examples/workflows/test-then-review .solcode/workflows/
```
