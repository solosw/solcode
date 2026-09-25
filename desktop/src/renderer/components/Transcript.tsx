/** The scrollable transcript: messages, reasoning traces, and tool cards. */

import { useEffect, useRef, useState } from 'react'
import type { JSX } from 'react'
import { Brain, ChevronRight } from 'lucide-react'
import type { ChatMessage, TranscriptEntry } from '@shared/protocol'
import { cn } from '../lib/utils'
import { Markdown } from './Markdown'
import { ToolCard } from './ToolCard'

/** Collapsible reasoning trace, closed by default so it never dominates. */
function Thinking({ text }: { text: string }): JSX.Element {
  const [open, setOpen] = useState(false)
  return (
    <div className="mb-2">
      <button
        type="button"
        onClick={() => { setOpen(previous => !previous) }}
        className="flex items-center gap-1.5 rounded text-[11.5px] text-muted-foreground hover:text-foreground/80"
      >
        <ChevronRight className={cn('size-3 transition-transform', open && 'rotate-90')} />
        <Brain className="size-3" />
        <span>Reasoning</span>
      </button>
      {open ? (
        <div className="scroll-thin mt-1.5 max-h-72 overflow-auto border-l-2 border-border pl-3 text-[12px] leading-relaxed whitespace-pre-wrap text-muted-foreground/90">
          {text}
        </div>
      ) : null}
    </div>
  )
}

/** One user or assistant turn. */
function MessageBlock({ message }: { message: ChatMessage }): JSX.Element {
  if (message.role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[80%] rounded-xl rounded-br-sm border border-border bg-muted px-3.5 py-2.5">
          <p className="text-[13.5px] whitespace-pre-wrap">{message.text}</p>
        </div>
      </div>
    )
  }
  return (
    <div className="max-w-full">
      {message.thinking !== undefined && message.thinking !== '' ? <Thinking text={message.thinking} /> : null}
      {message.text !== '' ? <Markdown text={message.text} /> : null}
    </div>
  )
}

/** Scrolling transcript with follow-the-tail behaviour. */
export function Transcript({ entries, cwd, streaming }: {
  entries: TranscriptEntry[]
  cwd: string | null
  streaming: boolean
}): JSX.Element {
  const endRef = useRef<HTMLDivElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const [pinned, setPinned] = useState(true)

  // Only auto-scroll while the operator is already at the bottom; yanking the
  // viewport while they read earlier output is worse than missing new text.
  useEffect(() => {
    if (!pinned) return
    endRef.current?.scrollIntoView({ block: 'end' })
  }, [entries, pinned])

  const onScroll = (): void => {
    const element = containerRef.current
    if (element === null) return
    const distance = element.scrollHeight - element.scrollTop - element.clientHeight
    setPinned(distance < 80)
  }

  return (
    <div
      ref={containerRef}
      onScroll={onScroll}
      className="scroll-thin flex-1 overflow-y-auto"
    >
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-6 py-6">
        {entries.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-2 py-24 text-center">
            <p className="text-sm text-muted-foreground">
              {streaming ? 'Starting solcode…' : 'What should we build?'}
            </p>
          </div>
        ) : null}
        {entries.map(entry => (
          <div key={entry.type === 'message' ? entry.message.id : entry.tool.id}>
            {entry.type === 'message'
              ? <MessageBlock message={entry.message} />
              : <ToolCard tool={entry.tool} cwd={cwd} />}
          </div>
        ))}
        <div ref={endRef} />
      </div>
    </div>
  )
}
