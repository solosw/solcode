/**
 * Headless integration check for AcpClient + AgentSession.
 *
 * Exercises the real `solcode --acp` binary without Electron, so the protocol
 * layer can be verified independently of the window. Exits non-zero on failure.
 */

import { resolve } from 'node:path'
import { AcpClient } from '../dist/main/acp-client.js'
import { AgentSession } from '../dist/main/agent-session.js'

const cwd = process.argv[2] ?? resolve(import.meta.dirname, '..', '..')
const bin = process.argv[3] ?? resolve(cwd, 'solcode.exe')

const events = []
let session = null
const client = new AcpClient(bin, ['--acp'], cwd, process.env, {
  // Wire updates into the session exactly as the main process does.
  onUpdate: (update) => { events.push(update.sessionUpdate); session?.apply(update) },
  onRequestPermission: (id) => { console.log(`permission request ${id}`) },
  onStderr: () => {},
  onExit: (code) => { console.log(`exit ${code}`) },
})

session = new AgentSession(client)
let latest = null
session.subscribe((snapshot) => { latest = snapshot })

client.start()

const info = await client.initialize()
session.setAgentVersion(info.agentInfo?.version)
console.log(`initialize: protocolVersion=${info.protocolVersion} agent=${info.agentInfo?.name}@${info.agentInfo?.version}`)

const created = await client.newSession(cwd)
session.adoptSession(created, cwd)
console.log(`session/new: id=${created.sessionId}`)
console.log(`modes: ${created.modes?.availableModes.map(m => m.id).join(', ') ?? '(none)'}`)
console.log(`currentMode: ${created.modes?.currentModeId ?? '(none)'}`)

// Updates arrive asynchronously right after session/new; give them a moment.
await new Promise(r => setTimeout(r, 1500))
console.log(`commands advertised: ${latest.commands.length}`)
console.log(`usage: ${JSON.stringify(latest.usage)}`)

// A slash command exercises the full request/response path without needing an
// API key, and proves the agent answers prompts end to end.
session.addUserMessage('/status')
const stopReason = await client.prompt(created.sessionId, '/status')
session.markTurnEnded()
console.log(`prompt stopReason: ${stopReason}`)

const assistantText = latest.transcript
  .filter(e => e.type === 'message' && e.message.role === 'assistant')
  .map(e => e.message.text)
  .join('')
console.log(`assistant text length: ${assistantText.length}`)
if (assistantText.length > 0) console.log(`assistant preview: ${assistantText.slice(0, 160).replace(/\n/g, ' | ')}`)

console.log(`update kinds seen: ${[...new Set(events)].join(', ')}`)

client.stop()

const ok = info.protocolVersion === 1
  && created.sessionId.length > 0
  && assistantText.length > 0
console.log(ok ? '\nRESULT: PASS' : '\nRESULT: FAIL')
process.exit(ok ? 0 : 1)
