/**
 * Session state machine.
 *
 * Owns the transcript the renderer displays and translates the agent's raw
 * `session/update` stream into it. Kept out of the client so transport framing
 * and product state stay independent.
 */

import {
  type AcpClient,
  type NewSessionResult,
  asLocations,
} from './acp-client.js'
import type {
  AcpDiff,
  AcpPermissionRequest,
  AcpPlanEntry,
  AcpSessionUpdate,
  AcpToolLocation,
  AcpUsage,
  ChatMessage,
  SessionSnapshot,
  ToolCall,
  ToolCallStatus,
  ToolKind,
  TranscriptEntry,
} from '../shared/protocol.js'

import { readConfigOptions, hasConfigValue, type ConfigOption } from '../shared/config-options.js'

/** Snapshot listeners; called on every observable change. */
type Listener = (snapshot: SessionSnapshot) => void

/** Strip ACP's unknown values down to a kind the UI knows how to colour. */
function normaliseKind(value: unknown): ToolKind {
  switch (value) {
    case 'read': case 'edit': case 'execute': case 'fetch': case 'think': case 'other':
      return value
    default:
      return 'other'
  }
}

/** Strip ACP's unknown values down to a status the UI knows how to render. */
function normaliseStatus(value: unknown): ToolCallStatus {
  switch (value) {
    case 'pending': case 'in_progress': case 'completed': case 'failed':
      return value
    default:
      return 'pending'
  }
}

/** Extract text and diff content from a tool-call `content` array. */
function readToolContent(content: unknown): { text: string; diffs: AcpDiff[] } {
  if (!Array.isArray(content)) return { text: '', diffs: [] }
  const texts: string[] = []
  const diffs: AcpDiff[] = []
  for (const item of content) {
    if (typeof item !== 'object' || item === null) continue
    const entry = item as { type?: unknown; path?: unknown; oldText?: unknown; newText?: unknown; content?: unknown }
    if (entry.type === 'diff' && typeof entry.path === 'string') {
      diffs.push({
        path: entry.path,
        oldText: typeof entry.oldText === 'string' ? entry.oldText : null,
        newText: typeof entry.newText === 'string' ? entry.newText : '',
      })
      continue
    }
    const nested = entry.content
    if (typeof nested === 'object' && nested !== null && (nested as { type?: unknown }).type === 'text') {
      const text = (nested as { text?: unknown }).text
      if (typeof text === 'string') texts.push(text)
    }
  }
  return { text: texts.join('\n'), diffs }
}

/** Best-effort string view of a tool's raw output for the collapsed card. */
function readOutputText(value: unknown): string {
  if (typeof value === 'string') return value
  if (typeof value === 'object' && value !== null) {
    const output = (value as { output?: unknown }).output
    if (typeof output === 'string') return output
  }
  return ''
}

/**
 * Read the text of a message/thought chunk.
 *
 * solcode wraps chunk text in `content: { type: 'text', text }` rather than a
 * top-level `text` field, so the nested shape is the one that must be read.
 */
function readChunkText(update: AcpSessionUpdate): string {
  const content = (update as { content?: unknown }).content
  if (typeof content === 'object' && content !== null && !Array.isArray(content)) {
    const text = (content as { text?: unknown }).text
    if (typeof text === 'string') return text
  }
  return typeof update.text === 'string' ? update.text : ''
}

/**
 * Read usage from the flattened wire shape.
 *
 * solcode puts `used` / `size` directly on the update object rather than
 * nesting them under `usage`.
 */
function readUsage(update: AcpSessionUpdate): AcpUsage | undefined {
  if (update.usage !== undefined) return update.usage
  const record = update as { used?: unknown, size?: unknown }
  if (typeof record.used !== 'number') return undefined
  return typeof record.size === 'number'
    ? { used: record.used, size: record.size }
    : { used: record.used }
}

export class AgentSession {
  private disposed = false
  private configOptions: ConfigOption[] | undefined
  private configSequence = -1
  private readonly earlyConfig = new Map<string, { options: unknown; sequence: number }>()
  private configTail: Promise<void> = Promise.resolve()
  private configPending = 0
  private configError: string | undefined
  private readonly listeners = new Set<Listener>()
  private transcript: TranscriptEntry[] = []
  private messageSeq = 0
  /** The assistant message still receiving streamed text, if any. */
  private openMessageId: string | null = null
  private status: SessionSnapshot['status'] = 'starting'
  private error: string | undefined
  private agentVersion: string | undefined
  private modes: SessionSnapshot['modes'] = []
  private currentModeId: string | undefined
  private commands: SessionSnapshot['commands'] = []
  private plan: AcpPlanEntry[] = []
  private usage: AcpUsage | undefined
  private permission: AcpPermissionRequest | null = null

  constructor(private client: AcpClient) {}

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  snapshot(): SessionSnapshot {
    const base: SessionSnapshot = {
      sessionId: this.sessionId,
      cwd: this.cwd,
      status: this.status,
      modes: this.modes,
      commands: this.commands,
      plan: this.plan,
      transcript: this.transcript,
      permission: this.permission,
    }
    if (this.configOptions !== undefined) base.configOptions = this.configOptions
    base.configPending = this.configPending
    if (this.configError !== undefined) base.configError = this.configError
    if (this.error !== undefined) base.error = this.error
    if (this.agentVersion !== undefined) base.agentVersion = this.agentVersion
    if (this.currentModeId !== undefined) base.currentModeId = this.currentModeId
    if (this.usage !== undefined) base.usage = this.usage
    return base
  }

  private sessionIdValue: string | null = null
  private cwdValue: string | null = null

  /** Active ACP session id, or null before the handshake completes. */
  get sessionId(): string | null { return this.sessionIdValue }

  /** Working directory the session is rooted at. */
  get cwd(): string | null { return this.cwdValue }

  /** Attach a freshly started client and perform the ACP handshake. */
  async boot(version: string): Promise<void> {
    this.agentVersion = version
    this.emit()
  }

  /** Record the negotiated `initialize` result. */
  setAgentVersion(version: string | undefined): void {
    this.agentVersion = version
    this.emit()
  }

  /** Record a new/loaded session and its advertised modes. */
  adoptSession(result: NewSessionResult, cwd: string): void {
    this.sessionIdValue = result.sessionId
    this.cwdValue = cwd
    this.modes = []
    this.currentModeId = undefined
    if (result.modes !== undefined) {
      this.modes = result.modes.availableModes
      this.currentModeId = result.modes.currentModeId
    }
    if (result.configOptions !== undefined) this.applyConfigSnapshot(result.sessionId, result.configOptions, result.sequence ?? 0)
    const early = this.earlyConfig.get(result.sessionId)
    if (early !== undefined) this.applyConfigSnapshot(result.sessionId, early.options, early.sequence)
    this.earlyConfig.clear()
    this.status = 'ready'
    this.error = undefined
    this.emit()
  }

  /** Transport calls this synchronously in receive order, before resolving RPCs. */
  applyConfigSnapshot(sessionId: string, options: unknown, sequence: number): void {
    if (this.disposed) return
    if (this.sessionId === null) {
      const previous = this.earlyConfig.get(sessionId)
      if (previous === undefined || sequence > previous.sequence) this.earlyConfig.set(sessionId, { options, sequence })
      return
    }
    if (sessionId !== this.sessionId || sequence <= this.configSequence) return
    this.configSequence = sequence
    this.configOptions = readConfigOptions(options)
    this.currentModeId = this.configOptions.find(option => option.category === 'mode')?.currentValue
    this.emit()
  }

  /** Serialize mutations; every queued value is revalidated against the latest
   * confirmed full snapshot, not the snapshot rendered when it was clicked. */
  async setConfigOption(configId: string, value: string): Promise<void> {
    const sessionId = this.sessionId
    if (this.disposed || sessionId === null) throw new Error('No active session')
    if (this.configPending === 0) {
      this.configTail = Promise.resolve()
      this.configError = undefined
    }
    this.configPending++
    this.emit()
    const operation = this.configTail.then(async () => {
      if (this.disposed || this.sessionId !== sessionId) throw new Error('Session changed; configuration cancelled')
      const option = this.configOptions?.find(candidate => candidate.id === configId)
      if (option === undefined || !hasConfigValue(option, value)) throw new Error('This configuration value is no longer available; choose again')
      if (option.currentValue === value) return
      const result = await this.client.setConfigOption(sessionId, configId, value)
      if (this.disposed || this.sessionId !== sessionId) throw new Error('Session changed; configuration cancelled')
      this.applyConfigSnapshot(sessionId, result.configOptions, result.sequence)
    })
    this.configTail = operation
    try {
      await operation
    } catch (cause) {
      if (!this.disposed) this.configError = cause instanceof Error ? cause.message : String(cause)
      throw cause
    } finally {
      this.configPending--
      if (!this.disposed) this.emit()
    }
  }

  async waitForConfiguration(): Promise<void> {
    // Keep waiting if another selection was queued during the previous await.
    let tail: Promise<void>
    do {
      tail = this.configTail
      await tail
    } while (tail !== this.configTail)
    if (this.disposed) throw new Error('Session changed')
    if (this.configError !== undefined) throw new Error(this.configError)
  }

  dispose(): void {
    this.disposed = true
    this.listeners.clear()
    this.earlyConfig.clear()
  }

  markReady(): void {
    this.status = 'ready'
    this.emit()
  }

  markError(message: string): void {
    this.status = 'error'
    this.error = message
    this.emit()
  }

  markExited(code: number | null): void {
    this.disposed = true
    this.status = 'exited'
    this.error = `solcode --acp exited (code ${String(code)})`
    this.emit()
  }

  /** Record the operator's prompt so it appears before the answer streams in. */
  addUserMessage(text: string): void {
    this.transcript = [...this.transcript, {
      type: 'message',
      message: { id: `m${this.messageSeq++}`, role: 'user', text, createdAt: Date.now() },
    }]
    this.status = 'prompting'
    this.openMessageId = null
    this.emit()
  }

  markTurnEnded(): void {
    this.status = 'ready'
    this.openMessageId = null
    this.emit()
  }

  setPermission(request: AcpPermissionRequest | null): void {
    this.permission = request
    this.emit()
  }

  /** Apply one `session/update` notification. */
  apply(update: AcpSessionUpdate, sessionId = this.sessionId, sequence = this.configSequence + 1): void {
    if (this.disposed || (this.sessionId !== null && sessionId !== this.sessionId)) return
    switch (update.sessionUpdate) {
      case 'agent_message_chunk':
        this.appendToOpenMessage(readChunkText(update))
        return
      case 'agent_thought_chunk':
        this.appendThinking(readChunkText(update))
        return
      case 'user_message_chunk':
        // Replayed history on session/load already echoes the user's turn.
        return
      case 'tool_call':
        this.upsertTool(update, true)
        return
      case 'tool_call_update':
        this.upsertTool(update, false)
        return
      case 'plan':
      case 'agent_plan_update':
        this.plan = Array.isArray(update.entries) ? update.entries : []
        this.emit()
        return
      case 'available_commands_update':
        this.commands = Array.isArray(update.availableCommands) ? update.availableCommands : []
        this.emit()
        return
      case 'config_option_update':
        if (sessionId !== null) this.applyConfigSnapshot(sessionId, update.configOptions, sequence)
        return
      case 'current_mode_update':
        if (this.configOptions !== undefined) return
        if (typeof update.currentModeId === 'string') this.currentModeId = update.currentModeId
        this.emit()
        return
      case 'usage_update': {
        const usage = readUsage(update)
        if (usage !== undefined) this.usage = usage
        this.emit()
        return
      }
      default:
        // status_update and future variants carry no transcript meaning yet.
        return
    }
  }

  /** Rebuild transcript from a replayed history (used by session/load). */
  reset(): void {
    this.transcript = []
    this.openMessageId = null
    this.plan = []
    this.usage = undefined
    this.emit()
  }

  private appendToOpenMessage(text: string): void {
    if (text === '') return
    const openId = this.openMessageId
    if (openId === null) {
      const id = `m${this.messageSeq++}`
      this.openMessageId = id
      this.transcript = [...this.transcript, {
        type: 'message',
        message: { id, role: 'assistant', text, createdAt: Date.now() },
      }]
    } else {
      this.transcript = this.transcript.map(entry => {
        if (entry.type !== 'message' || entry.message.id !== openId) return entry
        return { type: 'message', message: { ...entry.message, text: entry.message.text + text } }
      })
    }
    this.emit()
  }

  private appendThinking(text: string): void {
    if (text === '') return
    const openId = this.openMessageId
    if (openId === null) {
      const id = `m${this.messageSeq++}`
      this.openMessageId = id
      this.transcript = [...this.transcript, {
        type: 'message',
        message: { id, role: 'assistant', text: '', thinking: text, createdAt: Date.now() },
      }]
    } else {
      this.transcript = this.transcript.map(entry => {
        if (entry.type !== 'message' || entry.message.id !== openId) return entry
        const previous = entry.message.thinking ?? ''
        return { type: 'message', message: { ...entry.message, thinking: previous + text } }
      })
    }
    this.emit()
  }

  /** Insert or update a tool card, keyed by the agent's toolCallId. */
  private upsertTool(update: AcpSessionUpdate, isStart: boolean): void {
    const id = update.toolCallId
    if (typeof id !== 'string' || id === '') return

    const content = readToolContent((update as { content?: unknown }).content)
    const locations = asLocations(update.locations)
    const existing = this.transcript.find(
      (entry): entry is Extract<TranscriptEntry, { type: 'tool' }> =>
        entry.type === 'tool' && entry.tool.id === id,
    )

    if (existing === undefined) {
      const tool: ToolCall = {
        id,
        title: update.title ?? 'Tool',
        kind: normaliseKind(update.kind),
        status: normaliseStatus(update.status),
        diffs: content.diffs,
        locations,
        startedAt: Date.now(),
        isError: normaliseStatus(update.status) === 'failed',
      }
      if (update.rawInput !== undefined) tool.rawInput = update.rawInput
      if (content.text !== '') tool.output = content.text
      this.transcript = [...this.transcript, { type: 'tool', tool }]
    } else {
      const tool = existing.tool
      const status = update.status === undefined ? tool.status : normaliseStatus(update.status)
      const next: ToolCall = {
        ...tool,
        title: update.title ?? tool.title,
        kind: update.kind === undefined ? tool.kind : normaliseKind(update.kind),
        status,
        diffs: content.diffs.length > 0 ? content.diffs : tool.diffs,
        locations: locations.length > 0 ? locations : tool.locations,
        isError: status === 'failed',
      }
      if (update.rawInput !== undefined) next.rawInput = update.rawInput
      const output = content.text !== '' ? content.text : readOutputText(update.rawOutput)
      if (output !== '') next.output = output
      if (status === 'completed' || status === 'failed') next.finishedAt = Date.now()
      this.transcript = this.transcript.map(entry =>
        entry.type === 'tool' && entry.tool.id === id ? { type: 'tool', tool: next } : entry)
    }

    // A tool call ends the current assistant block: later text is a new block.
    if (isStart) this.openMessageId = null
    this.emit()
  }

  private emit(): void {
    const snapshot = this.snapshot()
    for (const listener of this.listeners) listener(snapshot)
  }
}

/** Convenience alias so callers need not import the entry union. */
export type { AcpToolLocation }
