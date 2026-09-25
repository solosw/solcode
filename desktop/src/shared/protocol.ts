/**
 * Wire types shared by the ACP client (main process) and the renderer.
 *
 * These mirror solcode's `internal/acp/protocol.go`. Framing is newline-delimited
 * JSON: one JSON-RPC message per line, no Content-Length header.
 */

export const ACP_PROTOCOL_VERSION = 1

/** ACP agent methods solcode implements. */
export const ACP_METHOD = {
  initialize: 'initialize',
  authenticate: 'authenticate',
  sessionNew: 'session/new',
  sessionLoad: 'session/load',
  sessionPrompt: 'session/prompt',
  sessionCancel: 'session/cancel',
  sessionSetMode: 'session/set_mode',
  sessionUpdate: 'session/update',
  requestPermission: 'session/request_permission',
  fsReadTextFile: 'fs/read_text_file',
  fsWriteTextFile: 'fs/write_text_file',
} as const

/** Tool-call lifecycle states reported by the agent. */
export type ToolCallStatus = 'pending' | 'in_progress' | 'completed' | 'failed'

/** Semantic tool categories used to pick an icon and a colour. */
export type ToolKind = 'read' | 'edit' | 'execute' | 'fetch' | 'think' | 'other'

/** A unified-diff payload carried by an edit-like tool call. */
export interface AcpDiff {
  path: string
  /** Absent for a newly created file. */
  oldText: string | null
  newText: string
}

/** One entry of the agent's todo plan. */
export interface AcpPlanEntry {
  content: string
  priority: string
  status: string
}

/** One slash command advertised by the agent. */
export interface AcpAvailableCommand {
  name: string
  description: string
  hint?: string
}

/** Context-window occupancy reported by the agent. */
export interface AcpUsage {
  used: number
  size?: number
}

/** One session mode the agent offers (maps to a solcode permission mode). */
export interface AcpSessionMode {
  id: string
  name: string
  description?: string
}

/** A file the agent touched, used to drive the diff/review pane. */
export interface AcpToolLocation {
  path: string
  line?: number
}

/**
 * Normalised `session/update` payload.
 *
 * solcode sends a `sessionUpdate` discriminator; every field below is optional
 * because each variant populates a different subset.
 */
export interface AcpSessionUpdate {
  sessionUpdate: string
  /** Text or image content for message/thought chunks. */
  text?: string
  toolCallId?: string
  title?: string
  kind?: ToolKind
  status?: ToolCallStatus
  rawInput?: unknown
  rawOutput?: unknown
  /** Unified diffs and text content attached to a tool call. */
  diffs?: AcpDiff[]
  contentText?: string
  locations?: AcpToolLocation[]
  entries?: AcpPlanEntry[]
  availableCommands?: AcpAvailableCommand[]
  currentModeId?: string
  message?: string
  usage?: AcpUsage
}

/** `session/request_permission` options offered to the user. */
export interface AcpPermissionOption {
  optionId: string
  name: string
  kind: string
}

/** A pending permission request awaiting a user decision. */
export interface AcpPermissionRequest {
  requestId: number
  sessionId: string
  title: string
  kind?: ToolKind
  rawInput?: unknown
  diffs?: AcpDiff[]
  options: AcpPermissionOption[]
}

// ---------------------------------------------------------------------------
// Renderer-facing models
// ---------------------------------------------------------------------------

/** An assistant or user message in the transcript. */
export interface ChatMessage {
  id: string
  role: 'user' | 'assistant' | 'system'
  text: string
  /** Collapsible reasoning trace, kept separate from the visible answer. */
  thinking?: string
  createdAt: number
}

/** A tool invocation rendered as a card in the transcript. */
export interface ToolCall {
  id: string
  title: string
  kind: ToolKind
  status: ToolCallStatus
  rawInput?: unknown
  output?: string
  diffs: AcpDiff[]
  locations: AcpToolLocation[]
  startedAt: number
  finishedAt?: number
  isError: boolean
}

/** One entry of the session's visible transcript. */
export type TranscriptEntry =
  | { type: 'message'; message: ChatMessage }
  | { type: 'tool'; tool: ToolCall }

/** Snapshot of agent state pushed to the renderer. */
export interface SessionSnapshot {
  sessionId: string | null
  cwd: string | null
  status: 'starting' | 'ready' | 'prompting' | 'error' | 'exited'
  error?: string
  agentVersion?: string
  modes: AcpSessionMode[]
  currentModeId?: string
  commands: AcpAvailableCommand[]
  plan: AcpPlanEntry[]
  usage?: AcpUsage
  transcript: TranscriptEntry[]
  permission: AcpPermissionRequest | null
}

/** Actions the renderer can invoke on the main process. */
export interface DesktopBridge {
  getSnapshot(): Promise<SessionSnapshot>
  prompt(text: string): Promise<void>
  cancel(): Promise<void>
  setMode(modeId: string): Promise<void>
  newSession(cwd: string): Promise<void>
  pickDirectory(): Promise<string | null>
  respondPermission(requestId: number, optionId: string | null): Promise<void>
  restart(): Promise<void>
  onSnapshot(listener: (snapshot: SessionSnapshot) => void): () => void
}
