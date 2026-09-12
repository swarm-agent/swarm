import { useEffect, useMemo, useState } from 'react'
import { desktopAutomations, useAutomationPage } from '../../runtime/desktop-automations'
import { automationProgressSummary, automationTime } from '../../state/desktop-automation-progress'

export function AutomationProgressView({ workspaceId, id, compact = false, revision }: { workspaceId: string; id: string; compact?: boolean; revision?: number }) {
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  const input = useMemo(() => ({ workspace_id: workspaceId, id, action: 'progress' as const, display_timezone: timezone }), [workspaceId, id, timezone])
  const page = useAutomationPage(input)
  const progress = page?.data?.progress
  const [now, setNow] = useState(Date.now)
  const [capacityError, setCapacityError] = useState(false)
  useEffect(() => {
    try { const lease = desktopAutomations.acquire(input); setCapacityError(false); return lease.release }
    catch { setCapacityError(true) }
  }, [input])
  useEffect(() => {
    // One civil-day boundary, not recurring network polling. Visibility repairs suspended tabs.
    if (capacityError) return
    const repair = () => { setNow(Date.now()); void desktopAutomations.refresh(input) }
    const timer = progress && progress.day_end > Date.now() ? window.setTimeout(repair, Math.min(progress.day_end - Date.now() + 1, 2147483647)) : undefined
    const visible = () => { if (document.visibilityState === 'visible') repair() }
    document.addEventListener('visibilitychange', visible)
    return () => { clearTimeout(timer); document.removeEventListener('visibilitychange', visible) }
  }, [input, progress?.day_end, capacityError])
  const stale = !!page?.stale || !!page?.error || (revision !== undefined && progress?.definition_revision !== revision)
  const summary = capacityError ? 'Progress unavailable — open automation for details' : automationProgressSummary(progress, stale, now)
  const usable = progress && !stale && now >= progress.day_start && now < progress.day_end
  if (compact) return <span className="block text-xs break-words" title={`${summary}. Forecasts are not admitted work. Display day: ${timezone}. Actual start/completion and unrecorded misses unavailable.`}>{summary}</span>
  return <section aria-label="Automation schedule and progress" className="space-y-2 break-words text-sm">
    <h3 className="font-semibold">Automation</h3><p role="status">{summary}</p>
    {page?.error && <p role="alert">{page.error}</p>}
    {usable && <>
      <p>Display day: {automationTime(progress.day_start, timezone)} – {automationTime(progress.day_end, timezone)} · {timezone}</p>
      {progress.interval_anchor && <p>Elapsed interval anchor: {automationTime(progress.interval_anchor, timezone)}. Display timezone does not change execution.</p>}
      <p>Today’s planned slots: {progress.forecast_complete ? '' : 'partial — '}{progress.planned_slots.length} forecasts, not a run quota or admitted work.</p>
      <dl className="grid grid-cols-2 gap-2">{['completed', 'running', 'pending', 'failed', 'cancelled', 'cancelling', 'blocked', 'skipped'].map(state => <div key={state}><dt>{state}</dt><dd>{progress.history_complete ? '' : 'at least '}{progress.counts[state] ?? 0}</dd></div>)}</dl>
      <p>Unrecorded skipped/missed runs: unavailable. Retries are revisions of unique occurrences, not additional completions.</p>
      <p>Manual runs (separate): {Object.entries(progress.manual_counts).map(([state, count]) => `${count} ${state}`).join(' · ')}{!progress.history_complete && ' · partial observations'}. Unknown trigger: {progress.unknown_trigger_count}.</p>
      <p>{progress.no_next_reason ? `No currently eligible upcoming run: ${progress.no_next_reason.replace(/_/g, ' ')}` : `Upcoming slots are conditional forecasts, not admissions (${progress.upcoming_complete ? 'complete within horizon' : 'partial'}).`}</p>
      {!progress.no_next_reason && <details><summary>Upcoming forecast times</summary><ul>{progress.upcoming_slots.map(slot => <li key={`${slot.definition_revision}:${slot.scheduled_at}`}>{automationTime(slot.scheduled_at, timezone)} · revision {slot.definition_revision}</li>)}</ul></details>}
      {progress.latest_recorded && <p>{progress.history_complete ? 'Latest recorded occurrence' : 'Latest observed on partial scan'}: scheduled {automationTime(progress.latest_recorded.scheduled_at, timezone)} · {progress.latest_recorded.state} · state written {automationTime(progress.latest_recorded.recorded_at, timezone)}.</p>}
      <p>Actual start/completion times and outcome details unavailable here; inspect occurrence and audit history. State-write time is not execution timing.</p>
      <p className="text-xs">As of {automationTime(progress.as_of, timezone)} · non-atomic read; live changes may invalidate this view.</p>
    </>}
    <button className="underline" disabled={page?.loading} onClick={() => { setNow(Date.now()); void desktopAutomations.refresh(input) }}>Refresh schedule</button>
  </section>
}
