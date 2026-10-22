/** Selectors reflect only the latest Agent-confirmed full snapshot. */
import { useState } from 'react'
import type { JSX } from 'react'
import type { SessionSnapshot } from '@shared/protocol'
import type { ConfigOption } from '@shared/config-options'

export function ConfigSelectors({ snapshot, onChange }: {
  snapshot: SessionSnapshot
  onChange(configId: string, value: string): Promise<void>
}): JSX.Element | null {
  const [error, setError] = useState<string>()
  if (snapshot.configOptions === undefined) return null
  const primary: ConfigOption[] = []
  const more: ConfigOption[] = []
  const seen = new Set<string>()
  for (const option of snapshot.configOptions) {
    if (option.category !== undefined && ['mode', 'model', 'thought_level'].includes(option.category) && !seen.has(option.category)) {
      seen.add(option.category)
      primary.push(option)
    } else more.push(option)
  }
  const render = (option: ConfigOption): JSX.Element => (
    <label key={option.id} title={option.description} className="flex min-w-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-[11.5px]">
      <span className="shrink-0 text-muted-foreground">{option.name}</span>
      <select
        aria-label={option.name}
        value={option.currentValue}
        disabled={snapshot.status !== 'ready' && snapshot.status !== 'prompting'}
        className="max-w-64 min-w-0 bg-card text-foreground outline-none disabled:opacity-50"
        onChange={event => {
          const value = event.target.value
          event.target.value = option.currentValue
          setError(undefined)
          void onChange(option.id, value).catch((cause: unknown) => {
            setError(cause instanceof Error ? cause.message : String(cause))
          })
        }}
      >
        {option.options.map(item => 'group' in item ? (
          <optgroup key={item.group} label={item.name}>
            {item.options.map(value => <option key={value.value} value={value.value} title={value.description}>{value.name}</option>)}
          </optgroup>
        ) : <option key={item.value} value={item.value} title={item.description}>{item.name}</option>)}
      </select>
    </label>
  )
  return (
    <div className="mb-2 space-y-1.5" aria-busy={(snapshot.configPending ?? 0) > 0}>
      <div className="flex flex-wrap items-center gap-2">
        {primary.map(render)}
        {more.length > 0 ? (
          <details className="text-[11.5px]">
            <summary className="cursor-pointer text-muted-foreground">More settings</summary>
            <div className="mt-2 flex flex-wrap gap-2">{more.map(render)}</div>
          </details>
        ) : null}
        {(snapshot.configPending ?? 0) > 0 ? <span role="status" className="text-[11px] text-muted-foreground">Applying settings…</span> : null}
      </div>
      {(snapshot.configError ?? error) !== undefined ? <p role="alert" className="text-[11.5px] text-removed">{snapshot.configError ?? error}</p> : null}
    </div>
  )
}
