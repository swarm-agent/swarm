import { useEffect, useState } from 'react'
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
  return <main className="min-w-0 flex-1 space-y-5 p-4 lg:p-6">
    <header><h2 className="text-2xl font-semibold">Daily activity</h2><p className="text-sm text-[var(--app-text-muted)]">Run states by scheduled date · {timezone}</p></header>
    <label>Display timezone <select className={control} value={timezone} onChange={event => setTimezone(event.target.value)}>{[...new Set([Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC'])].map(zone => <option key={zone}>{zone}</option>)}</select></label>
    {loading && <p role="status">Loading activity…</p>}
    {error && <p role="alert">Activity unavailable: {error}</p>}
    {stale && <p role="status">Activity is stale; refresh before relying on these counts.</p>}
    {partial && <p className="text-sm">Incomplete daily counts — this page contains up to 50 occurrence records, not daily totals. Other pages may contain runs for the same dates.</p>}
    {!loading && !error && !stale && records !== undefined && !groups.length && <p>{partial ? 'No occurrences on this page. Continue to the next page.' : 'No recorded runs yet.'}</p>}
    {groups.map(([date, rows]) => {
      const counts = new Map<string, number>()
      for (const row of rows) { const state = row.occurrence!.state; counts.set(state, (counts.get(state) ?? 0) + 1) }
      return <section key={date} aria-label={date} className="space-y-3 rounded-lg border border-[var(--app-border)] p-4">
        <h3 className="font-semibold">{date === today ? `Today · ${date}` : date} · {rows.length} {partial ? 'runs on this page' : 'runs'}</h3>
        <dl className="flex flex-wrap gap-4">{[...counts].map(([state, count]) => <div key={state}><dt className="capitalize text-sm text-[var(--app-text-muted)]">{state}</dt><dd className="text-xl font-semibold">{count}</dd></div>)}</dl>
        <ul className="space-y-2">{[...rows].sort((a, b) => b.occurrence!.scheduled_at - a.occurrence!.scheduled_at).map(row => <li key={row.id}><button className={`${control} w-full text-left break-words`} onClick={() => onSelect(row.automation_id)}><span className="font-medium">{names[row.automation_id] ?? row.automation_id}</span><span className="block text-sm">{row.occurrence!.state} · {new Intl.DateTimeFormat(undefined, { timeZone: timezone, hour: 'numeric', minute: '2-digit' }).format(row.occurrence!.scheduled_at)} · View history and configuration</span></button></li>)}</ul>
      </section>
    })}
    <div className="flex flex-wrap gap-2"><button className={control} disabled={loading} onClick={onRetry}>{error ? 'Retry activity' : 'Refresh activity'}</button>{onFirst && <button className={control} onClick={onFirst}>First activity page</button>}{onNext && <button className={control} disabled={loading || stale || !!error} onClick={onNext}>More activity</button>}</div>
  </main>
}
