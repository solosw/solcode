/** Modal dialog for an agent permission request. */

import type { JSX } from 'react'
import { ShieldAlert } from 'lucide-react'
import type { AcpPermissionRequest } from '@shared/protocol'
import { cn } from '../lib/utils'

/** Colour an option by whether it grants or withholds access. */
function optionTone(kind: string): string {
  if (kind.startsWith('reject')) return 'border-border hover:bg-muted'
  if (kind === 'allow_always') return 'border-primary/50 bg-primary/10 hover:bg-primary/20'
  return 'border-border bg-muted hover:bg-accent'
}

/** Blocking decision dialog; the agent is paused until it is answered. */
export function PermissionDialog({ request, onRespond }: {
  request: AcpPermissionRequest
  onRespond(optionId: string | null): void
}): JSX.Element {
  return (
    <div className="absolute inset-0 z-50 flex items-center justify-center bg-black/50 p-6">
      <div className="w-full max-w-lg overflow-hidden rounded-xl border border-border bg-card shadow-2xl">
        <div className="flex items-start gap-3 border-b border-border px-4 py-3">
          <ShieldAlert className="mt-0.5 size-4 shrink-0 text-warn" />
          <div className="min-w-0">
            <h2 className="text-[13px] font-semibold">Permission required</h2>
            <p className="truncate text-[12px] text-muted-foreground" title={request.title}>
              {request.title}
            </p>
          </div>
        </div>

        {request.rawInput !== undefined ? (
          <pre className="scroll-thin max-h-56 overflow-auto border-b border-border bg-[oklch(0.12_0_0)] px-4 py-3 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-foreground/80">
            {JSON.stringify(request.rawInput, null, 2)}
          </pre>
        ) : null}

        <div className="flex flex-wrap justify-end gap-2 px-4 py-3">
          {request.options.map(option => (
            <button
              key={option.optionId}
              type="button"
              onClick={() => { onRespond(option.optionId) }}
              className={cn(
                'rounded-lg border px-3 py-1.5 text-[12.5px] font-medium transition-colors',
                optionTone(option.kind),
              )}
            >
              {option.name}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
