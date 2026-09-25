/**
 * Unified-diff rendering.
 *
 * Diffs arrive as whole old/new file texts rather than hunks, so the line diff
 * is computed here. A trimmed common prefix/suffix keeps the output short for
 * the common case of a small edit inside a long file.
 */

import { useMemo } from 'react'
import type { JSX } from 'react'
import type { AcpDiff } from '@shared/protocol'
import { cn, relativePath } from '../lib/utils'

/** One rendered diff line. */
interface DiffLine {
  kind: 'context' | 'added' | 'removed'
  text: string
}

/** Lines of surrounding context kept on each side of a change. */
const CONTEXT_LINES = 3

/** Split into lines without inventing a trailing empty line. */
function toLines(text: string): string[] {
  if (text === '') return []
  return text.split('\n')
}

/** Longest-common-subsequence diff, computed on trimmed input for speed. */
function computeDiff(oldText: string, newText: string): DiffLine[] {
  const before = toLines(oldText)
  const after = toLines(newText)

  // Trim the shared prefix and suffix first: edits are usually local, and this
  // reduces the quadratic table from file-sized to change-sized.
  let start = 0
  while (start < before.length && start < after.length && before[start] === after[start]) start += 1
  let endBefore = before.length
  let endAfter = after.length
  while (endBefore > start && endAfter > start && before[endBefore - 1] === after[endAfter - 1]) {
    endBefore -= 1
    endAfter -= 1
  }

  const midBefore = before.slice(start, endBefore)
  const midAfter = after.slice(start, endAfter)

  const table: number[][] = Array.from({ length: midBefore.length + 1 }, () =>
    new Array<number>(midAfter.length + 1).fill(0))
  for (let i = midBefore.length - 1; i >= 0; i -= 1) {
    for (let j = midAfter.length - 1; j >= 0; j -= 1) {
      const row = table[i]
      const next = table[i + 1]
      if (row === undefined || next === undefined) continue
      row[j] = midBefore[i] === midAfter[j]
        ? (next[j + 1] ?? 0) + 1
        : Math.max(next[j] ?? 0, row[j + 1] ?? 0)
    }
  }

  const middle: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < midBefore.length && j < midAfter.length) {
    if (midBefore[i] === midAfter[j]) {
      middle.push({ kind: 'context', text: midBefore[i] ?? '' })
      i += 1
      j += 1
      continue
    }
    const down = table[i + 1]?.[j] ?? 0
    const right = table[i]?.[j + 1] ?? 0
    if (down >= right) {
      middle.push({ kind: 'removed', text: midBefore[i] ?? '' })
      i += 1
    } else {
      middle.push({ kind: 'added', text: midAfter[j] ?? '' })
      j += 1
    }
  }
  while (i < midBefore.length) {
    middle.push({ kind: 'removed', text: midBefore[i] ?? '' })
    i += 1
  }
  while (j < midAfter.length) {
    middle.push({ kind: 'added', text: midAfter[j] ?? '' })
    j += 1
  }

  const head = before.slice(Math.max(0, start - CONTEXT_LINES), start)
    .map((text): DiffLine => ({ kind: 'context', text }))
  const tail = before.slice(endBefore, endBefore + CONTEXT_LINES)
    .map((text): DiffLine => ({ kind: 'context', text }))

  const trimmed: DiffLine[] = [...head]
  const firstMiddle = middle[0]
  if (head.length > 0 && firstMiddle !== undefined && start > CONTEXT_LINES) {
    trimmed.push({ kind: 'context', text: '…' })
  }
  trimmed.push(...middle)
  if (tail.length > 0 && middle.length > 0 && endBefore + CONTEXT_LINES < before.length) {
    trimmed.push({ kind: 'context', text: '…' })
  }
  trimmed.push(...tail)
  return trimmed
}

/** Count added and removed lines for the card's summary badge. */
export function diffStats(diff: AcpDiff): { added: number; removed: number } {
  const lines = computeDiff(diff.oldText ?? '', diff.newText)
  let added = 0
  let removed = 0
  for (const line of lines) {
    if (line.kind === 'added') added += 1
    else if (line.kind === 'removed') removed += 1
  }
  return { added, removed }
}

/** Render one file's unified diff. */
export function DiffView({ diff, cwd, className }: {
  diff: AcpDiff
  cwd: string | null
  className?: string
}): JSX.Element {
  const lines = useMemo(() => computeDiff(diff.oldText ?? '', diff.newText), [diff.oldText, diff.newText])
  const isNewFile = diff.oldText === null
  const stats = useMemo(() => {
    let added = 0
    let removed = 0
    for (const line of lines) {
      if (line.kind === 'added') added += 1
      else if (line.kind === 'removed') removed += 1
    }
    return { added, removed }
  }, [lines])

  return (
    <div className={cn('overflow-hidden rounded-lg border border-border bg-[oklch(0.12_0_0)]', className)}>
      <div className="flex items-center gap-2 border-b border-border bg-card px-3 py-1.5">
        <span className="truncate font-mono text-xs text-foreground/90" title={diff.path}>
          {relativePath(diff.path, cwd)}
        </span>
        {isNewFile ? <span className="text-[10px] font-medium text-added">new file</span> : null}
        <span className="ml-auto shrink-0 font-mono text-[11px]">
          {stats.added > 0 ? <span className="text-added">+{stats.added}</span> : null}
          {stats.added > 0 && stats.removed > 0 ? ' ' : null}
          {stats.removed > 0 ? <span className="text-removed">−{stats.removed}</span> : null}
        </span>
      </div>
      <div className="scroll-thin max-h-96 overflow-auto">
        <pre className="m-0 p-0 font-mono text-[11.5px] leading-[1.6]">
          {lines.map((line, index) => (
            <div
              key={index}
              className={cn(
                'flex px-3',
                line.kind === 'added' && 'bg-added/10',
                line.kind === 'removed' && 'bg-removed/10',
              )}
            >
              <span
                className={cn(
                  'w-3 shrink-0 select-none',
                  line.kind === 'added' && 'text-added',
                  line.kind === 'removed' && 'text-removed',
                  line.kind === 'context' && 'text-muted-foreground/40',
                )}
              >
                {line.kind === 'added' ? '+' : line.kind === 'removed' ? '−' : ' '}
              </span>
              <span className="whitespace-pre-wrap break-all text-foreground/85">{line.text || ' '}</span>
            </div>
          ))}
        </pre>
      </div>
    </div>
  )
}
