/** Left rail: session identity, permission mode, plan, and changed files. */

import type { JSX } from 'react'
import { Check, CircleDot, FolderTree, ListChecks, Loader2 } from 'lucide-react'
import type { AcpPlanEntry, AcpUsage, SessionSnapshot, ToolCall } from '@shared/protocol'
import { cn, formatTokens, relativePath } from '../lib/utils'

/** Every file the agent has touched this session, newest first. */
function changedFiles(snapshot: SessionSnapshot): string[] {
  const seen = new Set<string>()
  const paths: string[] = []
  for (let index = snapshot.transcript.length - 1; index >= 0; index -= 1) {
    const entry = snapshot.transcript[index]
    if (entry === undefined || entry.type !== 'tool') continue
    const tool: ToolCall = entry.tool
    for (const diff of tool.diffs) {
      if (seen.has(diff.path)) continue
      seen.add(diff.path)
      paths.push(diff.path)
    }
  }
  return paths
}

/** Status pill for the agent connection. */
function StatusPill({ status }: { status: SessionSnapshot['status'] }): JSX.Element {
  const map: Record<SessionSnapshot['status'], { label: string; className: string }> = {
    starting: { label: 'starting', className: 'text-muted-foreground' },
    ready: { label: 'ready', className: 'text-added' },
    prompting: { label: 'working', className: 'text-primary' },
    error: { label: 'error', className: 'text-removed' },
    exited: { label: 'exited', className: 'text-removed' },
  }
  const state = map[status]
  return (
    <span className={cn('flex items-center gap-1.5 text-[11px] font-medium', state.className)}>
      {status === 'prompting' || status === 'starting'
        ? <Loader2 className="size-3 animate-spin" />
        : <CircleDot className="size-3" />}
      {state.label}
    </span>
  )
}

/** Plan entry status glyph. */
function PlanGlyph({ entry }: { entry: AcpPlanEntry }): JSX.Element {
  if (entry.status === 'completed') return <Check className="size-3 text-added" />
  if (entry.status === 'in_progress') return <Loader2 className="size-3 animate-spin text-primary" />
  return <span className="block size-3 rounded-full border border-muted-foreground/50" />
}

/** Context-window meter. */
function UsageMeter({ usage }: { usage: AcpUsage }): JSX.Element | null {
  if (usage.size === undefined || usage.size <= 0) return null
  const ratio = Math.min(1, usage.used / usage.size)
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between text-[11px] text-muted-foreground">
        <span>Context</span>
        <span className="font-mono">
          {formatTokens(usage.used)} / {formatTokens(usage.size)}
        </span>
      </div>
      <div className="h-1 overflow-hidden rounded-full bg-muted">
        <div
          className={cn('h-full rounded-full transition-[width]', ratio > 0.8 ? 'bg-removed' : 'bg-primary')}
          style={{ width: `${Math.max(2, ratio * 100)}%` }}
        />
      </div>
    </div>
  )
}

/** Sidebar sections: agent state, plan, and the session's changed files. */
export function Sidebar({ snapshot, onSetMode }: {
  snapshot: SessionSnapshot
  onSetMode(modeId: string): void
}): JSX.Element {
  const files = changedFiles(snapshot)

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r border-border bg-[var(--panel)]">
      <div className="drag-frame flex h-11 items-center px-3">
        <span className="text-[13px] font-semibold tracking-tight">Solcode</span>
        <span className="ml-auto"><StatusPill status={snapshot.status} /></span>
      </div>

      <div className="scroll-thin flex-1 space-y-5 overflow-y-auto px-3 py-3">
        <section className="space-y-1.5">
          <h2 className="px-1 text-[10.5px] font-semibold tracking-wider text-muted-foreground uppercase">
            Working directory
          </h2>
          <p
            className="truncate rounded-md bg-background/50 px-2 py-1.5 font-mono text-[11px] text-foreground/80"
            title={snapshot.cwd ?? ''}
          >
            {snapshot.cwd ?? '—'}
          </p>
        </section>

        <section className="space-y-1.5">
          <h2 className="flex items-center gap-1.5 px-1 text-[10.5px] font-semibold tracking-wider text-muted-foreground uppercase">
            <FolderTree className="size-3" /> Permission mode
          </h2>
          <div className="space-y-0.5">
            {snapshot.modes.map(mode => (
              <button
                key={mode.id}
                type="button"
                onClick={() => { onSetMode(mode.id) }}
                title={mode.description ?? ''}
                className={cn(
                  'flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-[12px]',
                  mode.id === snapshot.currentModeId
                    ? 'bg-muted text-foreground'
                    : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground',
                )}
              >
                <span className={cn(
                  'size-1.5 shrink-0 rounded-full',
                  mode.id === snapshot.currentModeId ? 'bg-primary' : 'bg-muted-foreground/40',
                )} />
                {mode.name}
              </button>
            ))}
          </div>
        </section>

        {snapshot.plan.length > 0 ? (
          <section className="space-y-1.5">
            <h2 className="flex items-center gap-1.5 px-1 text-[10.5px] font-semibold tracking-wider text-muted-foreground uppercase">
              <ListChecks className="size-3" /> Plan
            </h2>
            <ul className="space-y-1">
              {snapshot.plan.map((entry, index) => (
                <li key={index} className="flex items-start gap-2 px-1 text-[11.5px] leading-snug">
                  <span className="mt-0.5 shrink-0"><PlanGlyph entry={entry} /></span>
                  <span className={cn(
                    entry.status === 'completed' ? 'text-muted-foreground line-through' : 'text-foreground/85',
                  )}>
                    {entry.content}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        ) : null}

        {files.length > 0 ? (
          <section className="space-y-1.5">
            <h2 className="px-1 text-[10.5px] font-semibold tracking-wider text-muted-foreground uppercase">
              Changed files · {files.length}
            </h2>
            <ul className="space-y-0.5">
              {files.map(path => (
                <li
                  key={path}
                  title={path}
                  className="truncate rounded-md px-2 py-1 font-mono text-[11px] text-muted-foreground hover:bg-muted/50"
                >
                  {relativePath(path, snapshot.cwd)}
                </li>
              ))}
            </ul>
          </section>
        ) : null}
      </div>

      {snapshot.usage !== undefined ? (
        <div className="border-t border-border px-3 py-2.5">
          <UsageMeter usage={snapshot.usage} />
        </div>
      ) : null}
    </aside>
  )
}
