/**
 * Markdown rendering for assistant output.
 *
 * Model output is untrusted input rendered as HTML, so it is sanitised before
 * it reaches the DOM rather than relying on the CSP alone.
 */

import DOMPurify from 'dompurify'
import { marked } from 'marked'
import { useMemo } from 'react'
import type { JSX } from 'react'
import { cn } from '../lib/utils'

marked.setOptions({ gfm: true, breaks: true })

const ALLOWED_TAGS = [
  'p', 'br', 'hr', 'strong', 'em', 'del', 'code', 'pre', 'blockquote',
  'ul', 'ol', 'li', 'a', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'table', 'thead', 'tbody', 'tr', 'th', 'td', 'span', 'div',
]

/** Render markdown to sanitised HTML. */
function render(text: string): string {
  const html = marked.parse(text, { async: false })
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR: ['href', 'title', 'class', 'target', 'rel'],
    ALLOWED_URI_REGEXP: /^(?:https?|mailto):/u,
  })
}

/** Render one block of markdown prose. */
export function Markdown({ text, className }: { text: string; className?: string }): JSX.Element {
  const html = useMemo(() => render(text), [text])
  return (
    <div
      className={cn('prose-solcode text-[13.5px]', className)}
      // Sanitised above; the CSP additionally forbids inline and remote script.
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
}
