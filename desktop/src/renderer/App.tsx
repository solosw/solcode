/** Application shell: wires the bridge snapshot into the three-pane layout. */

import { useCallback, useEffect, useState } from 'react'
import type { JSX } from 'react'
import { AlertTriangle, RefreshCw } from 'lucide-react'
import type { DesktopBridge, SessionSnapshot } from '@shared/protocol'
import { Composer } from './components/Composer'
import { PermissionDialog } from './components/PermissionDialog'
import { Sidebar } from './components/Sidebar'
import { Transcript } from './components/Transcript'

/** The preload bridge is the only channel to the agent. */
const bridge = (globalThis as unknown as { solcode?: DesktopBridge }).solcode

/** Empty state shown until the first snapshot arrives. */
const BOOTING: SessionSnapshot = {
  sessionId: null,
  cwd: null,
  status: 'starting',
  modes: [],
  commands: [],
  plan: [],
  transcript: [],
  permission: null,
}

export function App(): JSX.Element {
  const [snapshot, setSnapshot] = useState<SessionSnapshot>(BOOTING)

  useEffect(() => {
    if (bridge === undefined) {
      setSnapshot(previous => ({
        ...previous,
        status: 'error',
        error: 'The Solcode bridge is unavailable. Restart the application.',
      }))
      return
    }
    const unsubscribe = bridge.onSnapshot(setSnapshot)
    void bridge.getSnapshot().then(current => { if (current !== null) setSnapshot(current) })
    return unsubscribe
  }, [])

  const onPrompt = useCallback((text: string) => {
    void bridge?.prompt(text).catch((cause: unknown) => {
      setSnapshot(previous => ({
        ...previous,
        status: 'error',
        error: cause instanceof Error ? cause.message : String(cause),
      }))
    })
  }, [])

  const onCancel = useCallback(() => { void bridge?.cancel() }, [])

  const onSetMode = useCallback((modeId: string) => { void bridge?.setMode(modeId) }, [])

  const onPickDirectory = useCallback(() => {
    void bridge?.pickDirectory().then((path) => {
      if (path === null || path === undefined) return
      void bridge?.newSession(path)
    })
  }, [])

  const onRespondPermission = useCallback((optionId: string | null) => {
    const request = snapshot.permission
    if (request === null) return
    void bridge?.respondPermission(request.requestId, optionId)
  }, [snapshot.permission])

  const onRestart = useCallback(() => { void bridge?.restart() }, [])

  const failed = snapshot.status === 'error' || snapshot.status === 'exited'

  return (
    <div className="relative flex h-full w-full overflow-hidden bg-background">
      <Sidebar snapshot={snapshot} onSetMode={onSetMode} />

      <main className="flex min-w-0 flex-1 flex-col">
        <div className="drag-frame flex h-11 shrink-0 items-center gap-2 border-b border-border px-4">
          <span className="truncate font-mono text-[11.5px] text-muted-foreground">
            {snapshot.sessionId ?? 'connecting to solcode…'}
          </span>
          {snapshot.agentVersion !== undefined ? (
            <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
              v{snapshot.agentVersion}
            </span>
          ) : null}
        </div>

        {failed && snapshot.error !== undefined ? (
          <div className="flex items-start gap-2 border-b border-removed/30 bg-removed/10 px-4 py-2.5">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-removed" />
            <p className="min-w-0 flex-1 text-[12px] text-foreground/90">{snapshot.error}</p>
            <button
              type="button"
              onClick={onRestart}
              className="flex shrink-0 items-center gap-1.5 rounded-md border border-border px-2 py-1 text-[11.5px] hover:bg-muted"
            >
              <RefreshCw className="size-3" /> Restart agent
            </button>
          </div>
        ) : null}

        <Transcript
          entries={snapshot.transcript}
          cwd={snapshot.cwd}
          streaming={snapshot.status === 'starting'}
        />

        <Composer
          snapshot={snapshot}
          onPrompt={onPrompt}
          onCancel={onCancel}
          onPickDirectory={onPickDirectory}
        />
      </main>

      {snapshot.permission !== null ? (
        <PermissionDialog request={snapshot.permission} onRespond={onRespondPermission} />
      ) : null}
    </div>
  )
}
