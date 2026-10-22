/**
 * Context-isolated bridge.
 *
 * The renderer gets a small, fixed API and no Node access: it can prompt, cancel,
 * change session settings, and answer permission dialogs.
 *
 * This file is CommonJS (`.cts`) because Electron loads preload scripts before
 * the renderer's module system exists, and `sandbox: true` requires a classic
 * script. Type-only imports are erased, so nothing here imports renderer code.
 */

import electron = require('electron')
import type { DesktopBridge, SessionSnapshot } from '../shared/protocol.js'

const CHANNELS = {
  invoke: 'solcode:invoke',
  snapshot: 'solcode:snapshot',
} as const

/** Action names the main process accepts on the single invoke channel. */
type InvokeRequest =
  | { action: 'snapshot' }
  | { action: 'prompt'; text: string; sessionId: string }
  | { action: 'cancel' }
  | { action: 'setMode'; modeId: string; sessionId: string }
  | { action: 'setConfigOption'; configId: string; value: string; sessionId: string }
  | { action: 'newSession'; cwd: string }
  | { action: 'pickDirectory' }
  | { action: 'respondPermission'; requestId: number; optionId: string | null }
  | { action: 'restart' }

const bridge: DesktopBridge = {
  getSnapshot: () => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'snapshot' } satisfies InvokeRequest),
  prompt: (text, sessionId) => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'prompt', text, sessionId } satisfies InvokeRequest),
  cancel: () => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'cancel' } satisfies InvokeRequest),
  setMode: (modeId, sessionId) => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'setMode', modeId, sessionId } satisfies InvokeRequest),
  setConfigOption: (sessionId, configId, value) => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'setConfigOption', sessionId, configId, value } satisfies InvokeRequest),
  newSession: (cwd) => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'newSession', cwd } satisfies InvokeRequest),
  pickDirectory: () => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'pickDirectory' } satisfies InvokeRequest),
  respondPermission: (requestId, optionId) =>
    electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'respondPermission', requestId, optionId } satisfies InvokeRequest),
  restart: () => electron.ipcRenderer.invoke(CHANNELS.invoke, { action: 'restart' } satisfies InvokeRequest),
  onSnapshot: (listener) => {
    const handler = (_event: unknown, snapshot: SessionSnapshot): void => { listener(snapshot) }
    electron.ipcRenderer.on(CHANNELS.snapshot, handler)
    return () => { electron.ipcRenderer.removeListener(CHANNELS.snapshot, handler) }
  },
}

electron.contextBridge.exposeInMainWorld('solcode', bridge)
