import { useEffect, useState } from 'react'
import { Activity, ArrowUpRight, Clock3, RefreshCcw } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import type { AutomationRecord } from '../../state/desktop-automation-api'
import { automationControl as control } from './automation-editor'
import { dayKey, nextDayDelay } from './automation-view'

// Occurrences are grouped by scheduled time, not the latest revision's write time.
export function dailyOccurrences(records: AutomationRecord[], timezone: string): [string, AutomationRecord[]][] {
  const groups = new Map<string, AutomationRecord[]>()
  for (const record of records) {
    if (!record.occurrence) continue
    const date = dayKey(record.occurrence.scheduled_at, timezone)
    groups.set(date, [...(groups.get(date) ?? []), record])
  }
  return [...groups].sort(([a], [b]) => b.localeCompare(a))
}

export function AutomationOverview({ records, names, loading, stale, error, partial, onRetry, onNext, onFirst, onSelect }: {
  records?: AutomationRecord[] | null; names: Record<string, string>; loading: boolean; stale: boolean; error?: string;
  partial: boolean; onRetry: () => void; onNext?: () => void; onFirst?: () => void; onSelect: (id: string) => void
}) {
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone)
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setTimeout(() => setNow(Date.now()), nextDayDelay(Date.now(), timezone))
    const resume = () => { if (document.visibilityState === 'visible') setNow(Date.now()) }
    document.addEventListener('visibilitychange', resume)
    return () => { clearTimeout(timer); document.removeEventListener('visibilitychange', resume) }
  }, [now, timezone])
  const today = dayKey(now, timezone)
  const groups = dailyOccurrences(records ?? [], timezone)
  return <main className="min-w-0 flex-1 space-y-5 px-5 py-7 sm:px-8">
    <header className="flex flex-wrap items-center justify-between gap-3"><div><h2 className="flex items-center gap-2 text-sm font-semibold"><Activity size={16} className="text-[var(--app-text-muted)]" />Daily activity</h2><p className="mt-1 text-xs text-[var(--app-text-subtle)]">Run states by scheduled date · {timezone}</p></div></header>
    <label className="flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-muted)]">Display timezone <select className={control} value={timezone} onChange={event => setTimezone(event.target.value)}>{[...new Set([Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC'])].map(zone => <option key={zone}>{zone}</option>)}</select></label>
    {loading && <p role="status">Loading activity…</p>}
    {error && <p role="alert">Activity unavailable: {error}</p>}
    {stale && <p role="status">Activity is stale; refresh before relying on these counts.</p>}
    {partial && <p className="text-sm">Incomplete daily counts — this page contains up to 50 occurrence records, not daily totals. Other pages may contain runs for the same dates.</p>}
    {!loading && !error && !stale && records !== undefined && !groups.length && <div className="flex items-center gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-6"><Clock3 size={22} className="shrink-0 text-[var(--app-text-subtle)]" /><div><p className="text-sm font-medium">{partial ? 'No occurrences on this page. Continue to the next page.' : 'No recorded runs yet.'}</p><p className="mt-1 text-xs text-[var(--app-text-muted)]">Recorded runs will appear here with their status and history.</p></div></div>}
    {groups.map(([date, rows]) => {
      const counts = new Map<string, number>()
      for (const row of rows) { const state = row.occurrence!.state; counts.set(state, (counts.get(state) ?? 0) + 1) }
      return <section key={date} aria-label={date} className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)]">
        <h3 className="border-b border-[var(--app-border)] px-5 py-4 text-xs font-medium text-[var(--app-text-muted)]">{date === today ? `Today · ${date}` : date} · {rows.length} {partial ? 'runs on this page' : 'runs'}</h3>
        <dl className="flex flex-wrap gap-8 px-5 py-4">{[...counts].map(([state, count]) => <div key={state}><dt className="capitalize text-sm text-[var(--app-text-muted)]">{state}</dt><dd className="text-xl font-semibold">{count}</dd></div>)}</dl>
        <ul className="divide-y divide-[var(--app-border)] border-t border-[var(--app-border)]">{[...rows].sort((a, b) => b.occurrence!.scheduled_at - a.occurrence!.scheduled_at).map(row => <li key={row.id}><button className="flex w-full items-center justify-between gap-3 px-5 py-4 text-left transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:outline-[var(--app-focus-ring)]" onClick={() => onSelect(row.automation_id)}><span className="min-w-0"><span className="block break-words text-sm font-medium">{names[row.automation_id] ?? row.automation_id}</span><span className="mt-1 block text-xs text-[var(--app-text-muted)]">{row.occurrence!.state} · {new Intl.DateTimeFormat(undefined, { timeZone: timezone, hour: 'numeric', minute: '2-digit' }).format(row.occurrence!.scheduled_at)} · View history and configuration</span></span><ArrowUpRight size={15} className="shrink-0 text-[var(--app-text-subtle)]" /></button></li>)}</ul>
      </section>
    })}
    <div className="flex flex-wrap gap-2"><Button variant="ghost" size="sm" disabled={loading} onClick={onRetry}><RefreshCcw size={13} />{error ? 'Retry activity' : 'Refresh activity'}</Button>{onFirst && <button className={control} onClick={onFirst}>First activity page</button>}{onNext && <button className={control} disabled={loading || stale || !!error} onClick={onNext}>More activity</button>}</div>
  </main>
}
