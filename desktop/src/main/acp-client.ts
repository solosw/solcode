/**
 * Minimal ACP client over newline-delimited JSON-RPC on a child process's stdio.
 *
 * Framing matters: solcode writes one JSON object per line (`internal/acp/conn.go`),
 * so a partial read must be buffered until a newline arrives. There is no
 * Content-Length header to wait for.
 */

import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import type { AcpSessionMode, AcpSessionUpdate, AcpToolLocation } from '../shared/protocol.js'

/** A JSON-RPC request still waiting for its response. */
interface PendingCall {
  resolve(value: unknown): void
  reject(error: Error): void
}

/** Callbacks the session layer supplies to react to agent traffic. */
export interface AcpClientEvents {
  onUpdate(update: AcpSessionUpdate): void
  /** The agent asked permission; the returned value answers `requestId`. */
  onRequestPermission(requestId: number, params: unknown): void
  onStderr(line: string): void
  onExit(code: number | null, signal: NodeJS.Signals | null): void
}

/** Result of a successful `initialize`. */
export interface InitializeResult {
  protocolVersion: number
  agentInfo: { name?: string; title?: string; version?: string }
}

/** Result of a successful `session/new`. */
export interface NewSessionResult {
  sessionId: string
  modes?: { currentModeId: string; availableModes: AcpSessionMode[] }
}

/**
 * ACP client bound to one `solcode --acp` child process.
 *
 * The client owns framing and request/response correlation. It deliberately
 * holds no product state; the session layer decides what an update means.
 */
export class AcpClient {
  private child: ChildProcessWithoutNullStreams | null = null
  private buffer = ''
  private nextId = 1
  private readonly pending = new Map<number, PendingCall>()
  private closed = false
  private stderrBuffer = ''

  constructor(
    private readonly command: string,
    private readonly args: readonly string[],
    private readonly cwd: string,
    private readonly env: NodeJS.ProcessEnv,
    private readonly events: AcpClientEvents,
  ) {}

  /** Spawn the agent process and start consuming its output. */
  start(): void {
    if (this.child !== null) throw new Error('solcode acp: client already started')
    const child = spawn(this.command, [...this.args], {
      cwd: this.cwd,
      env: this.env,
      stdio: ['pipe', 'pipe', 'pipe'],
      windowsHide: true,
    })
    this.child = child

    child.stdout.setEncoding('utf8')
    child.stdout.on('data', (chunk: string) => { this.consume(chunk) })

    // ACP diagnostics arrive on stderr; forward them line by line so a crash
    // reason is visible instead of silently buffered.
    child.stderr.setEncoding('utf8')
    child.stderr.on('data', (chunk: string) => {
      this.stderrBuffer += chunk
      let index: number
      while ((index = this.stderrBuffer.indexOf('\n')) >= 0) {
        const line = this.stderrBuffer.slice(0, index).replace(/\r$/u, '')
        this.stderrBuffer = this.stderrBuffer.slice(index + 1)
        if (line !== '') this.events.onStderr(line)
      }
    })

    child.on('error', (cause: Error) => {
      this.fail(new Error(`failed to start ${this.command}: ${cause.message}`))
    })
    child.on('exit', (code, signal) => {
      this.closed = true
      // Reject in-flight calls so the UI never waits on a dead process.
      this.fail(new Error(`solcode --acp exited (code ${String(code)})`))
      this.events.onExit(code, signal)
    })
  }

  /** Send `initialize` and return the negotiated protocol details. */
  async initialize(): Promise<InitializeResult> {
    return await this.call('initialize', {
      protocolVersion: 1,
      clientInfo: { name: 'solcode-desktop', title: 'Solcode Desktop', version: '0.1.0' },
      // solcode routes file reads/writes through the client only when these are
      // advertised; we keep them false so the agent owns the filesystem it was
      // already given as its working directory.
      clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: false },
    }) as InitializeResult
  }

  /** Create a session rooted at `cwd`. */
  async newSession(cwd: string): Promise<NewSessionResult> {
    return await this.call('session/new', { cwd, mcpServers: [] }) as NewSessionResult
  }

  /** Resume a persisted session by id. */
  async loadSession(sessionId: string, cwd: string): Promise<NewSessionResult> {
    return await this.call('session/load', { sessionId, cwd, mcpServers: [] }) as NewSessionResult
  }

  /**
   * Submit a prompt. Resolves when the turn ends, which may be long after the
   * streaming updates have already reached the UI.
   */
  async prompt(sessionId: string, text: string): Promise<string> {
    const result = await this.call('session/prompt', {
      sessionId,
      prompt: [{ type: 'text', text }],
    }) as { stopReason?: string }
    return result.stopReason ?? 'end_turn'
  }

  /** Ask the agent to stop the in-flight turn. */
  async cancel(sessionId: string): Promise<void> {
    await this.call('session/cancel', { sessionId })
  }

  /** Switch the session's permission mode. */
  async setMode(sessionId: string, modeId: string): Promise<void> {
    await this.call('session/set_mode', { sessionId, modeId })
  }

  /** Answer a `session/request_permission` call. */
  respondPermission(requestId: number, optionId: string | null): void {
    this.respond(requestId, optionId === null
      ? { outcome: { outcome: 'cancelled' } }
      : { outcome: { outcome: 'selected', optionId } })
  }

  /** Terminate the agent process. Safe to call more than once. */
  stop(): void {
    const child = this.child
    this.child = null
    this.closed = true
    if (child !== null && child.exitCode === null && !child.killed) child.kill()
  }

  // -------------------------------------------------------------------------

  /** Append raw output and dispatch every complete line found in the buffer. */
  private consume(chunk: string): void {
    this.buffer += chunk
    let index: number
    while ((index = this.buffer.indexOf('\n')) >= 0) {
      const line = this.buffer.slice(0, index).trim()
      this.buffer = this.buffer.slice(index + 1)
      if (line === '') continue
      this.dispatch(line)
    }
  }

  /** Route one decoded JSON-RPC message by its shape. */
  private dispatch(line: string): void {
    let message: JsonRpcMessage
    try {
      message = JSON.parse(line) as JsonRpcMessage
    } catch {
      // A non-JSON line is agent noise, not a protocol error worth failing on.
      this.events.onStderr(line)
      return
    }
    if (typeof message.method === 'string') {
      this.handleIncoming(message)
      return
    }
    if (message.id === undefined || message.id === null) return
    if (typeof message.id !== 'number') return
    const call = this.pending.get(message.id)
    if (call === undefined) return
    this.pending.delete(message.id)
    if (message.error !== undefined) {
      call.reject(new Error(message.error.message))
      return
    }
    call.resolve(message.result)
  }

  /** Handle an agent-initiated request (permission) or notification (update). */
  private handleIncoming(message: JsonRpcMessage): void {
    if (message.method === 'session/update') {
      const params = message.params as { update?: AcpSessionUpdate } | undefined
      if (params?.update !== undefined) this.events.onUpdate(params.update)
      return
    }
    if (message.method === 'session/request_permission') {
      // A request carries an id and needs an answer; a notification does not.
      if (typeof message.id === 'number') this.events.onRequestPermission(message.id, message.params)
      return
    }
    // Unknown server-initiated requests must still be answered, or the agent
    // blocks waiting for a reply that will never come.
    if (typeof message.id === 'number') {
      this.respondError(message.id, -32601, `method not found: ${message.method}`)
    }
  }

  /** Issue a request and await its response. */
  private async call(method: string, params: unknown): Promise<unknown> {
    if (this.closed || this.child === null) throw new Error('solcode --acp is not running')
    const id = this.nextId++
    const promise = new Promise<unknown>((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
    })
    this.write({ jsonrpc: '2.0', id, method, params })
    return await promise
  }

  private respond(id: number, result: unknown): void {
    this.write({ jsonrpc: '2.0', id, result })
  }

  private respondError(id: number, code: number, message: string): void {
    this.write({ jsonrpc: '2.0', id, error: { code, message } })
  }

  private write(message: JsonRpcMessage): void {
    const child = this.child
    if (child === null || child.stdin.destroyed) return
    child.stdin.write(`${JSON.stringify(message)}\n`)
  }

  /** Reject every in-flight call; used on transport failure and on exit. */
  private fail(error: Error): void {
    for (const call of this.pending.values()) call.reject(error)
    this.pending.clear()
  }
}

/** Structural shape of a JSON-RPC envelope, as far as this client cares. */
interface JsonRpcMessage {
  jsonrpc?: string
  id?: number | string | null
  method?: string
  params?: unknown
  result?: unknown
  error?: { code: number; message: string }
}

/** Narrow an unknown `locations` payload into typed tool locations. */
export function asLocations(value: unknown): AcpToolLocation[] {
  if (!Array.isArray(value)) return []
  const out: AcpToolLocation[] = []
  for (const entry of value) {
    if (typeof entry !== 'object' || entry === null) continue
    const path = (entry as { path?: unknown }).path
    if (typeof path !== 'string' || path === '') continue
    const line = (entry as { line?: unknown }).line
    out.push(typeof line === 'number' ? { path, line } : { path })
  }
  return out
}
