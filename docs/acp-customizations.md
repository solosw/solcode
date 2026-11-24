# ACP core instructions and skills

solcode negotiates ACP v1 and ACP v2 Preview (`2.0.0-beta.3`). Both versions
use the standard `systemPrompt` capability for client-provided core
instructions. ACP v2 adds structured skill discovery and invocation.

## Client-provided core instructions

The initialize response advertises:


```json
{
  "agentCapabilities": {
    "sessionCapabilities": {
      "systemPrompt": { "mode": { "override": {} } }
    }
  }
}
```

In v2, the capability is under `capabilities.session.systemPrompt`. Clients
can send `systemPrompt` on `session/new`, v1 `session/load`, or v2
`session/resume`.


```json
{
  "cwd": "/path/to/project",
  "mcpServers": [],
  "systemPrompt": {
    "mode": "override",
    "content": [
      { "type": "text", "text": "Review changes carefully. Explain conclusions in Chinese." }
    ]
  }
}
```

Include `sessionId` when loading or resuming a session.

- append keeps the configured core preamble and solcode's authored default
  instructions, then adds the client instructions at system level.
- override replaces that core (the configured preamble and default behavioral
  instructions) with the supplied text. It changes how the agent works, without
  replacing the complete assembled system prompt.
- Both modes retain tool usage instructions and schemas, the Skills catalog,
  project rules, the working directory, and permission/mode policy. Actual tool
  permissions and MCP configuration continue to apply independently.

Only text blocks are accepted. mode is required; unsupported modes, non-text
blocks, empty content arrays, and text that is entirely whitespace return
JSON-RPC invalid_params (-32602) before a session is created or loaded. Multiple
text blocks are joined with blank lines.

Client instructions belong to the session runtime and are not written to user
configuration, ACP configuration sidecars, or conversation messages. Clients
should re-send them when loading a saved session. A supplied prompt takes effect
on the loaded runtime immediately; omitting it uses the configured/default core.
Changing model, mode, or enabled features retains the runtime instructions.

## Skills on ACP v1

The enabled native Skill registry is exposed through available_commands_update
after session/new and session/load. Each skill has its name, description, and an
optional-input hint. Discovery uses the same configured skills.paths and
skills.enabled / skills.disabled filters as the Skill tool. Client-installed
skill packages must be present in those directories on the agent's machine.

Send an ordinary text prompt starting with /name, for example:


```json
{
  "sessionId": "your-session-id",
  "prompt": [{ "type": "text", "text": "/review Check the public API changes" }]
}
```

solcode loads the selected skill before calling the model. The prompt includes
its instructions, arguments, Root, and bundled resource paths, using the same
activation renderer as the Skill tool. The activation is retained as ordinary
user text in conversation history, so v1 clients can replay it. Tool permission
grants are still enforced by the runtime.

- Builtin commands keep their names. A colliding skill is prefixed with skill.,
  repeatedly if needed: a skill named help is normally invoked as /skill.help.
  The advertised name is the one clients should use.
- Disabled skills and skills with user-invocable: false frontmatter are not
  exposed as commands. Model-only skills remain available to the Skill tool.
- /skills still opens the enable/disable picker. A successful change publishes
  the complete updated command list.
- Unknown slash names continue to ordinary chat. An advertised skill whose
  instructions cannot be read returns an explicit error.

## Skills on ACP v2

Send `protocolVersion: 2` and opt into structured commands with
`capabilities.session.commands.skill: {}` in `initialize`. The agent advertises
`capabilities.session.prompt.skill.inline: {}`. Skills appear as entries with
`type: "skill"`, a name, a description, and an optional absolute `SKILL.md`
path in `availableCommands`. The response to `session/new` or `session/resume`
includes the current list. `available_commands_update` replaces it when the
user changes enabled skills.

Each skill block references its advertised name. A prompt can include multiple
skill blocks, interleaved with text and other supported content.

```json
{
  "sessionId": "your-session-id",
  "idempotencyKey": "review-api-changes-1",
  "prompt": [
    { "type": "text", "text": "Check the public API changes." },
    { "type": "skill", "name": "review" },
    { "type": "text", "text": "Focus on backwards compatibility." }
  ]
}
```

The agent validates skill references before accepting the prompt. An unknown,
disabled, hidden, or unadvertised name yields `-32602`. Accepted prompts return a
`messageId` immediately. The matching `user_message` update carries the original
content blocks, and `state_update` reports progress and completion. Resubmitting
an accepted `idempotencyKey` returns the same ID without running the prompt again.

To replay a saved conversation, call `session/resume` with
`replayFrom: { "type": "start" }`. Persisted user prompts retain their message
IDs and skill references. A v2 client without the structured commands capability
gets ordinary slash commands and text references on replay. A v1 client gets
text references when it loads a v2 conversation.

References:

- [Client-Provided System Prompt](https://agentclientprotocol.com/rfds/client-system-prompt)
- [Agent Skills](https://agentclientprotocol.com/rfds/agent-skills)
