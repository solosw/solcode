/** Prompt composer: multiline input, slash-command palette, and stop control. */

import { useEffect, useMemo, useRef, useState } from 'react'
import type { JSX } from 'react'
import { ArrowUp, FolderOpen, Square, Zap } from 'lucide-react'
import type { AcpAvailableCommand, SessionSnapshot } from '@shared/protocol'
import { cn } from '../lib/utils'

/** Commands matching the token currently being typed after a leading slash. */
function matchingCommands(text: string, commands: AcpAvailableCommand[]): AcpAvailableCommand[] {
  if (!text.startsWith('/')) return []
  const token = text.slice(1)
  if (token.includes(' ')) return []
  return commands.filter(command => command.name.startsWith(token.toLowerCase())).slice(0, 8)
}

/** The prompt input row. */
export function Composer({ snapshot, onPrompt, onCancel, onPickDirectory }: {
  snapshot: SessionSnapshot
  onPrompt(text: string): void
  onCancel(): void
  onPickDirectory(): void
}): JSX.Element {
  const [text, setText] = useState('')
  const [selected, setSelected] = useState(0)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const busy = snapshot.status === 'prompting'
  const ready = snapshot.status === 'ready' || snapshot.status === 'prompting'
  const suggestions = useMemo(() => matchingCommands(text, snapshot.commands), [text, snapshot.commands])

  useEffect(() => { setSelected(0) }, [text])

  // Grow with the content up to a ceiling, then scroll inside the field.
  useEffect(() => {
    const element = textareaRef.current
    if (element === null) return
    element.style.height = 'auto'
    element.style.height = `${Math.min(element.scrollHeight, 200)}px`
  }, [text])

  const submit = (): void => {
    const value = text.trim()
    if (value === '' || !ready) return
    onPrompt(value)
    setText('')
  }

  const acceptSuggestion = (command: AcpAvailableCommand): void => {
    setText(`/${command.name} `)
    textareaRef.current?.focus()
  }

  return (
    <div className="mx-auto w-full max-w-3xl px-6 pb-5">
      {suggestions.length > 0 ? (
        <div className="mb-2 overflow-hidden rounded-lg border border-border bg-card shadow-lg">
          {suggestions.map((command, index) => (
            <button
              key={command.name}
              type="button"
              onMouseDown={(event) => { event.preventDefault(); acceptSuggestion(command) }}
              className={cn(
                'flex w-full items-baseline gap-3 px-3 py-1.5 text-left',
                index === selected ? 'bg-muted' : 'hover:bg-muted/60',
              )}
            >
              <span className="font-mono text-xs text-foreground">/{command.name}</span>
              <span className="truncate text-[11.5px] text-muted-foreground">{command.description}</span>
            </button>
          ))}
        </div>
      ) : null}

      <div className="rounded-xl border border-border bg-card focus-within:border-ring">
        <textarea
          ref={textareaRef}
          value={text}
          rows={1}
          onChange={event => { setText(event.target.value) }}
          onKeyDown={(event) => {
            if (suggestions.length > 0) {
              if (event.key === 'ArrowDown') {
                event.preventDefault()
                setSelected(previous => (previous + 1) % suggestions.length)
                return
              }
              if (event.key === 'ArrowUp') {
                event.preventDefault()
                setSelected(previous => (previous - 1 + suggestions.length) % suggestions.length)
                return
              }
              if (event.key === 'Tab') {
                event.preventDefault()
                const command = suggestions[selected]
                if (command !== undefined) acceptSuggestion(command)
                return
              }
            }
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault()
              submit()
            }
          }}
          placeholder={ready ? 'Ask solcode to build, explain, or fix something…' : 'Waiting for solcode…'}
          disabled={!ready}
          className="scroll-thin max-h-[200px] w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-[13.5px] outline-none placeholder:text-muted-foreground/70 disabled:opacity-60"
        />
        <div className="flex items-center gap-1 px-2.5 pb-2.5">
          <button
            type="button"
            onClick={onPickDirectory}
            title={snapshot.cwd ?? 'Choose a working directory'}
            className="flex max-w-[45%] items-center gap-1.5 rounded-md px-2 py-1 text-[11.5px] text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <FolderOpen className="size-3.5 shrink-0" />
            <span className="truncate font-mono">{snapshot.cwd ?? 'no directory'}</span>
          </button>

          {snapshot.usage !== undefined ? (
            <span className="ml-1 flex items-center gap-1 text-[11px] text-muted-foreground/70">
              <Zap className="size-3" />
              {snapshot.currentModeId ?? 'auto'}
            </span>
          ) : null}

          <div className="ml-auto flex items-center gap-1.5">
            {busy ? (
              <button
                type="button"
                onClick={onCancel}
                className="flex size-7 items-center justify-center rounded-md border border-border text-muted-foreground hover:bg-muted hover:text-foreground"
                title="Stop"
              >
                <Square className="size-3 fill-current" />
              </button>
            ) : null}
            <button
              type="button"
              onClick={submit}
              disabled={text.trim() === '' || !ready}
              className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground disabled:opacity-40"
              title="Send"
            >
              <ArrowUp className="size-4" />
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
