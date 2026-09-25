/** One tool invocation, collapsed by default and expandable for detail. */

import { useState } from 'react'
import type { JSX } from 'react'
import {
  AlertTriangle, Check, ChevronRight, FileText, Globe, Loader2, Pencil, Terminal, Wrench,
} from 'lucide-react'
import type { ToolCall, ToolKind } from '@shared/protocol'
import { cn, relativePath } from '../lib/utils'
import { DiffView } from './DiffView'

/** Pick the glyph that reads as the tool's category at a glance. */
function iconFor(kind: ToolKind): JSX.Element {
  switch (kind) {
    case 'read': return <FileText className="size-3.5" />
    case 'edit': return <Pencil className="size-3.5" />
    case 'execute': return <Terminal className="size-3.5" />
    case 'fetch': return <Globe className="size-3.5" />
    default: return <Wrench className="size-3.5" />
  }
}

/** Short human label for the tool's category. */
function labelFor(kind: ToolKind): string {
  switch (kind) {
    case 'read': return 'read'
    case 'edit': return 'edit'
    case 'execute': return 'run'
    case 'fetch': return 'fetch'
    case 'think': return 'think'
    default: return 'tool'
  }
}

/** Leading glyph reflecting the tool's current status. */
function StatusGlyph({ tool }: { tool: ToolCall }): JSX.Element {
  if (tool.isError) return <AlertTriangle className="size-3.5 text-removed" />
  if (tool.status === 'completed') return <Check className="size-3.5 text-added" />
  return <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
}

/** Render the tool's arguments compactly, favouring the meaningful field. */
function summarise(tool: ToolCall): string {
  const input = tool.rawInput
  if (typeof input === 'object' && input !== null) {
    const record = input as Record<string, unknown>
    for (const key of ['command', 'path', 'file_path', 'pattern', 'query', 'url', 'prompt']) {
      const value = record[key]
      if (typeof value === 'string' && value !== '') return value
    }
  }
  return ''
}

/** One tool call card. */
export function ToolCard({ tool, cwd }: { tool: ToolCall; cwd: string | null }): JSX.Element {
  const [open, setOpen] = useState(false)
  const summary = summarise(tool)
  const hasDetail = tool.diffs.length > 0 || (tool.output ?? '') !== ''
  const running = tool.status === 'pending' || tool.status === 'in_progress'

  return (
    <div data-tool-card={tool.id} className="overflow-hidden rounded-lg border border-border bg-card/60">
      <button
        type="button"
        onClick={() => { if (hasDetail) setOpen(previous => !previous) }}
        className={cn(
          'flex w-full items-center gap-2 px-3 py-2 text-left',
          hasDetail && 'hover:bg-muted/50',
        )}
      >
        {hasDetail ? (
          <ChevronRight className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')} />
        ) : (
          <span className="size-3.5 shrink-0" />
        )}
        <span className="shrink-0 text-muted-foreground">{iconFor(tool.kind)}</span>
        <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
          {labelFor(tool.kind)}
        </span>
        <span className={cn('truncate text-xs', running ? 'text-foreground/80' : 'text-foreground/70')}>
          {tool.title}
        </span>
        {summary !== '' ? (
          <span className="truncate font-mono text-[11px] text-muted-foreground/80">{summary}</span>
        ) : null}
        <span className="ml-auto shrink-0"><StatusGlyph tool={tool} /></span>
      </button>

      {/* File chips stay visible even when collapsed: they are the review affordance. */}
      {tool.locations.length > 0 && !open ? (
        <div className="flex flex-wrap gap-1 px-3 pb-2 pl-11">
          {tool.locations.slice(0, 4).map(location => (
            <span
              key={location.path}
              title={location.path}
              className="rounded border border-border bg-background/60 px-1.5 py-0.5 font-mono text-[10.5px] text-muted-foreground"
            >
              {relativePath(location.path, cwd)}
            </span>
          ))}
          {tool.locations.length > 4 ? (
            <span className="px-1 py-0.5 text-[10.5px] text-muted-foreground">
              +{tool.locations.length - 4}
            </span>
          ) : null}
        </div>
      ) : null}

      {open ? (
        <div className="space-y-2 border-t border-border px-3 py-2.5">
          {tool.diffs.map(diff => (
            <DiffView key={diff.path} diff={diff} cwd={cwd} />
          ))}
          {(tool.output ?? '') !== '' ? (
            <pre className="scroll-thin max-h-80 overflow-auto rounded-lg border border-border bg-[oklch(0.12_0_0)] p-3 font-mono text-[11.5px] leading-[1.6] whitespace-pre-wrap text-foreground/80">
              {tool.output}
            </pre>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
