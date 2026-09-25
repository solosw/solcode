/** Manage the official reminder stack through its Profile-owned plugin rows. */
import { useEffect, useRef, useState } from 'react'
import type { Context } from '@deepseek-ai/cordis'
import type { PluginInfo } from '@deepseek-ai/dsh-api-remotes/client'
import { Button, Switch, Toast } from '@deepseek-ai/dsh-client-ui-primitives'

// Start the context and Host service before exposing the task UI; stop in reverse.
export const SCHEDULE_MODULES = ['@deepseek-ai/dsh-time-context', '@deepseek-ai/dsh-schedule', '@deepseek-ai/dsh-client-ui-schedule']

export function ScheduleSettings({ context, zh }: { context: Context; zh: boolean }) {
  const t = (cn: string, en: string): string => zh ? cn : en
  const [revision, refresh] = useState(0)
  const [rows, setRows] = useState<PluginInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const pending = useRef(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    const reload = (): void => { refresh(value => value + 1) }
    const off = context.remote.$on('plugin-manager/changed', reload)
    const reset = context.on('connection/reset', reload)
    window.addEventListener('focus', reload)
    return () => { off(); reset(); window.removeEventListener('focus', reload) }
  }, [context])
  useEffect(() => {
    let disposed = false
    setLoading(true)
    void context.remote.pluginManager.listPlugins().then(result => {
      if (disposed) return
      if (!result.ok) throw new Error(result.error.message)
      setRows(result.value.filter(row => SCHEDULE_MODULES.includes(row.moduleName)))
    }).catch(failure => { if (!disposed) { setRows([]); setError(String(failure)) } })
      .finally(() => { if (!disposed) setLoading(false) })
    return () => { disposed = true }
  }, [context, revision])
  const available = SCHEDULE_MODULES.every(name => rows.filter(row => row.moduleName === name).length === 1)
  const enabled = rows.some(row => row.enabled)
  const complete = available && rows.every(row => row.enabled)
  const locked = loading || busy || !available || rows.some(row => row.readOnlyReason !== undefined)
  const change = async (value: boolean): Promise<void> => {
    if (pending.current || locked) return
    pending.current = true
    setBusy(true); setError(''); setNotice('')
    try {
      let restart = false
      for (const name of value ? SCHEDULE_MODULES : [...SCHEDULE_MODULES].reverse()) {
        const row = rows.find(row => row.moduleName === name)!
        if (row.enabled === value) continue
        const result = await context.remote.pluginManager.setPluginEnabled(row.entryId, value)
        if (!result.ok) throw new Error(result.error.message)
        const outcome = result.value
        if (outcome.application === 'failed' || outcome.application === 'cancelled') {
          throw new Error(outcome.error?.diagnostic ?? t('无法更改定时任务状态。', 'Could not change scheduled tasks.'))
        }
        if (outcome.application === 'overridden') throw new Error(t('当前配置覆盖了此开关，请检查插件配置。', 'Another configuration overrides this switch. Check your plugin configuration.'))
        restart ||= outcome.application === 'restart-required'
      }
      if (restart) setNotice(t('已保存，请重启后台服务以应用。', 'Saved. Restart the background service to apply.'))
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)) }
    finally { pending.current = false; setBusy(false); refresh(value => value + 1) }
  }
  return <div className="dshNextPluginDetail" data-next-schedule>
    <div className="dshNextPluginActions">
      {enabled && !complete && <Button variant="outline" size="sm" disabled={locked} onClick={() => { void change(true) }}>{t('完成启用', 'Finish enabling')}</Button>}
      <Switch label={t('启用定时任务', 'Enable scheduled tasks')} checked={enabled} disabled={locked} onChange={value => { void change(value) }} />
    </div>
    {notice && <Toast text={notice} onDone={() => { setNotice('') }} />}
    {error && <Toast text={error} onDone={() => { setError('') }} />}
    {error && <Button variant="outline" size="sm" disabled={loading || busy} onClick={() => { refresh(value => value + 1) }}>{t('重试', 'Retry')}</Button>}
  </div>
}
