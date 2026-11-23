# ACP core instructions and skills

solcode currently negotiates ACP v1. Client-provided core instructions use the
standard systemPrompt capability. Skills use v1 slash commands; the v2 skill
command variant and structured skill content blocks are not advertised.

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

Clients can send the same object on session/new and session/load:


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

On session/load, include sessionId as usual.

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
The implementation does not add session/resume or session/fork methods.

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
- A structured type: skill content block is rejected with -32602 on v1, before
  message insertion. ACP v2 negotiation, structured references, and inline skill
  references require the separate v2 lifecycle and message-update work.

References:

- [Client-Provided System Prompt](https://agentclientprotocol.com/rfds/client-system-prompt)
- [Agent Skills](https://agentclientprotocol.com/rfds/agent-skills)
