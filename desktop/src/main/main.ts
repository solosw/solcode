/**
 * Electron main process.
 *
 * Owns the `solcode --acp` child process and the single application window.
 * The DSH/DeepSeek Host of the reference project is deliberately absent: this
 * app talks to solcode over ACP and nothing else.
 */

import { app, BrowserWindow, dialog, ipcMain, shell } from 'electron'
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { AcpClient } from './acp-client.js'
import { AgentSession } from './agent-session.js'
import type { AcpPermissionRequest, SessionSnapshot } from '../shared/protocol.js'

const here = fileURLToPath(new URL('.', import.meta.url))
/** `dist/main/` at runtime; the project root is two levels above it. */
const projectRoot = resolve(here, '..', '..')

const INVOKE_CHANNEL = 'solcode:invoke'
const SNAPSHOT_CHANNEL = 'solcode:snapshot'

/** Request shapes mirrored from the preload bridge. */
type InvokeRequest =
  | { action: 'snapshot' }
  | { action: 'prompt'; text: string }
  | { action: 'cancel' }
  | { action: 'setMode'; modeId: string }
  | { action: 'newSession'; cwd: string }
  | { action: 'pickDirectory' }
  | { action: 'respondPermission'; requestId: number; optionId: string | null }
  | { action: 'restart' }

let window: BrowserWindow | null = null
let client: AcpClient | null = null
let session: AgentSession | null = null
let workingDirectory = process.cwd()
/** Permission requests are answered against this id, never a renderer-supplied one. */
let pendingPermissionId: number | null = null

/**
 * Locate the solcode executable.
 *
 * A packaged build ships the binary beside the app resources; development uses
 * the repository build or whatever is on PATH.
 */
function resolveSolcodeCommand(): { command: string, args: string[] } {
  const candidates = [
    process.env.SOLCODE_BIN,
    // Packaged: the binary sits beside the app resources.
    join(process.resourcesPath, 'solcode.exe'),
    join(process.resourcesPath, 'solcode'),
    // Development: the repository build, in or above the desktop directory.
    join(projectRoot, 'solcode.exe'),
    join(projectRoot, 'solcode'),
    resolve(projectRoot, '..', 'solcode.exe'),
    resolve(projectRoot, '..', 'solcode'),
  ].filter((value): value is string => typeof value === 'string' && value !== '')
  for (const candidate of candidates) {
    if (existsSync(candidate)) return { command: candidate, args: ['--acp'] }
  }
  // Fall back to PATH; a failure here surfaces as an actionable error in the UI.
  return { command: 'solcode', args: ['--acp'] }
}

/** Push the current snapshot to the renderer. */
function broadcast(): void {
  if (window === null || window.isDestroyed() || session === null) return
  window.webContents.send(SNAPSHOT_CHANNEL, session.snapshot())
}

/** Start the agent and complete the ACP handshake. */
async function startAgent(cwd: string): Promise<void> {
  client?.stop()
  const { command, args } = resolveSolcodeCommand()
  const created = new AcpClient(command, args, cwd, process.env, {
    onUpdate: (update) => { session?.apply(update) },
    onRequestPermission: (requestId, params) => { handlePermissionRequest(requestId, params) },
    onStderr: (line) => { process.stderr.write(`[solcode] ${line}\n`) },
    onExit: (code) => { session?.markExited(code) },
  })
  const active = new AgentSession(created)
  client = created
  session = active
  active.subscribe(() => { broadcast() })

  created.start()
  try {
    const info = await created.initialize()
    active.setAgentVersion(info.agentInfo.version)
    const result = await created.newSession(cwd)
    active.adoptSession(result, cwd)
  } catch (cause) {
    active.markError(cause instanceof Error ? cause.message : String(cause))
  }
}

/** Surface an agent permission request as a modal decision in the UI. */
function handlePermissionRequest(requestId: number, params: unknown): void {
  if (session === null) return
  const payload = (params ?? {}) as {
    toolCall?: { title?: unknown, kind?: unknown, rawInput?: unknown, content?: unknown }
    options?: unknown
  }
  const options = Array.isArray(payload.options)
    ? payload.options.flatMap((option) => {
      if (typeof option !== 'object' || option === null) return []
      const entry = option as { optionId?: unknown, name?: unknown, kind?: unknown }
      if (typeof entry.optionId !== 'string') return []
      return [{
        optionId: entry.optionId,
        name: typeof entry.name === 'string' ? entry.name : entry.optionId,
        kind: typeof entry.kind === 'string' ? entry.kind : 'allow_once',
      }]
    })
    : []
  const toolCall = payload.toolCall ?? {}
  const kind = typeof toolCall.kind === 'string' ? toolCall.kind as AcpPermissionRequest['kind'] : undefined
  const request: AcpPermissionRequest = {
    requestId,
    sessionId: session.sessionId ?? '',
    title: typeof toolCall.title === 'string' ? toolCall.title : 'Permission required',
    options,
  }
  if (kind !== undefined) request.kind = kind
  if (toolCall.rawInput !== undefined) request.rawInput = toolCall.rawInput
  pendingPermissionId = requestId
  session.setPermission(request)
}

/** Handle one renderer action. */
async function handleInvoke(request: InvokeRequest): Promise<unknown> {
  switch (request.action) {
    case 'snapshot':
      return session?.snapshot() ?? null
    case 'prompt': {
      if (client === null || session === null) throw new Error('agent is not running')
      const current = session.snapshot()
      if (current.sessionId === null) throw new Error('no active session')
      session.addUserMessage(request.text)
      // The turn resolves only when the whole agent loop finishes, so the reply
      // is deliberately not awaited before returning to the renderer.
      void client.prompt(current.sessionId, request.text)
        .then(() => { session?.markTurnEnded() })
        .catch((cause: unknown) => {
          session?.markError(cause instanceof Error ? cause.message : String(cause))
        })
      return null
    }
    case 'cancel': {
      if (client === null || session === null) return null
      const current = session.snapshot()
      if (current.sessionId !== null) await client.cancel(current.sessionId)
      return null
    }
    case 'setMode': {
      if (client === null || session === null) return null
      const current = session.snapshot()
      if (current.sessionId !== null) await client.setMode(current.sessionId, request.modeId)
      return null
    }
    case 'newSession': {
      if (request.cwd !== '') workingDirectory = request.cwd
      await startAgent(workingDirectory)
      return null
    }
    case 'pickDirectory': {
      if (window === null) return null
      const result = await dialog.showOpenDialog(window, {
        properties: ['openDirectory', 'createDirectory'],
        title: 'Choose a working directory',
      })
      return result.canceled || result.filePaths.length === 0 ? null : result.filePaths[0] ?? null
    }
    case 'respondPermission': {
      if (client === null || requestIdMismatch(request.requestId)) return null
      pendingPermissionId = null
      client.respondPermission(request.requestId, request.optionId)
      session?.setPermission(null)
      return null
    }
    case 'restart': {
      await startAgent(workingDirectory)
      return null
    }
    default:
      throw new Error('unsupported action')
  }
}

/** Guard against a renderer answering a request the agent already replaced. */
function requestIdMismatch(requestId: number): boolean {
  return pendingPermissionId !== null && pendingPermissionId !== requestId
}

function createWindow(): void {
  const created = new BrowserWindow({
    width: 1280,
    height: 840,
    minWidth: 900,
    minHeight: 600,
    backgroundColor: '#0d0d0f',
    title: 'Solcode',
    show: false,
    autoHideMenuBar: true,
    webPreferences: {
      preload: join(here, 'preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webviewTag: false,
    },
  })
  window = created

  created.once('ready-to-show', () => { created.show() })
  created.on('closed', () => { window = null })
  // External links open in the user's browser, never inside the app shell.
  created.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith('https://') || url.startsWith('http://')) void shell.openExternal(url)
    return { action: 'deny' }
  })

  const devServer = process.env.SOLCODE_DESKTOP_DEV_SERVER
  if (typeof devServer === 'string' && devServer !== '') {
    void created.loadURL(devServer)
  } else {
    void created.loadFile(join(projectRoot, 'dist', 'renderer', 'index.html'))
  }
}

app.whenReady().then(async () => {
  ipcMain.handle(INVOKE_CHANNEL, async (_event, request: InvokeRequest) => await handleInvoke(request))
  createWindow()
  await startAgent(workingDirectory)
  broadcast()

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow()
  })
}).catch((cause: unknown) => {
  process.stderr.write(`failed to start Solcode Desktop: ${String(cause)}\n`)
  app.exit(1)
})

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit()
})

app.on('before-quit', () => { client?.stop() })

/** Keeps the snapshot type reachable for the preload's declaration merging. */
export type { SessionSnapshot }
