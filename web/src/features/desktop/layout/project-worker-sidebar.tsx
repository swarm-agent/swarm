import { useState } from 'react'
import { useWorkerPage } from '../runtime/desktop-workers'
import type { WorkerRecord } from '../state/desktop-workers-api'
import { workerLifecycleLabel } from '../orchestrate/worker-presentation'
import { useWorkerNavigationPreferences } from './worker-navigation-preferences'

export function workerType(worker: WorkerRecord): string {
  const jobs = worker.automations || []
  const scheduled = jobs.some(job => job.activation_mode !== 'manual')
  const manual = !jobs.length || jobs.some(job => job.activation_mode === 'manual')
  return scheduled ? manual ? 'Mixed' : 'Scheduled / triggered' : 'On-demand'
}
export function ProjectWorkerSidebar({ accountScopeId, projectId, onInspect, onBrowse, showActivity = false }: {
  accountScopeId: string; projectId: string; onInspect: (id: string) => void; onBrowse: () => void; showActivity?: boolean
}) {
  const page = useWorkerPage({ kind: 'list', accountScopeId, limit: 100 })
  // Pending has its own filtered page: old/hidden proposals cannot be pushed off the first account page.
  const pendingPage = useWorkerPage({ kind: 'list', accountScopeId, limit: 100, lifecycleState: 'pending' })
  const data = page?.data && 'workers' in page.data ? page.data : undefined
  const pendingData = pendingPage?.data && 'workers' in pendingPage.data ? pendingPage.data : undefined
  const preferences = useWorkerNavigationPreferences(accountScopeId, projectId)
  const [showHidden, setShowHidden] = useState(false)
  const records = new Map((data?.workers || []).map(worker => [worker.id, worker]))
  for (const worker of pendingData?.workers || []) records.set(worker.id, worker)
  const workers = [...records.values()].filter(worker => worker.account_scope_id === accountScopeId && worker.metadata?.project_id === projectId).sort((a, b) => Number(b.lifecycle_state === 'pending' || !!b.pending_review) - Number(a.lifecycle_state === 'pending' || !!a.pending_review) || b.created_at - a.created_at)
  const pending = workers.filter(worker => worker.lifecycle_state === 'pending' || worker.pending_review)
  const visible = workers.filter(worker => showHidden || !preferences.hidden?.includes(worker.id))
  const hiddenCount = workers.length - workers.filter(worker => !preferences.hidden?.includes(worker.id)).length
  const partial = visible.length > 5 || !!data?.next_cursor || !!pendingData?.next_cursor
  const buttonClass = 'rounded-lg px-2 py-1.5 text-left hover:bg-[var(--app-surface-hover)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--app-primary)]'
  return <section aria-label="Project workers" className="min-w-0 space-y-2 px-2 py-3 text-xs text-[var(--app-text)]">
    <header className="flex flex-wrap items-center justify-between gap-1">
      <h3 className="px-1 font-semibold">Workers</h3>
      <button type="button" className={`${buttonClass} text-[var(--app-warning)]`} onClick={() => pending[0] ? onInspect(pending[0].id) : onBrowse()}>{pendingData ? `${pending.length}${data?.next_cursor || pendingData.next_cursor ? '+' : ''} pending approval` : pendingPage?.error ? 'Approvals unavailable' : 'Checking approvals…'}</button>
    </header>
    {(page?.error || pendingPage?.error) && <p role="alert" className="break-words text-[var(--app-warning)]">Workers unavailable: {page?.error || pendingPage?.error}</p>}
    {((page?.stale && data) || (pendingPage?.stale && pendingData)) && <p role="status" className="text-[var(--app-text-muted)]">{page?.loading || pendingPage?.loading ? 'Refreshing · last-known workers' : 'Last-known workers · may be out of date'}</p>}
    <ul className="m-0 list-none space-y-2 p-0">
      {visible.slice(0, 5).map(worker => <li key={worker.id} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-2">
        <div className="break-words px-1 font-semibold [overflow-wrap:anywhere]">{worker.name}</div>
        <div className={`mt-1 break-words px-1 text-[10px] ${(worker.lifecycle_state === 'pending' || worker.pending_review) ? 'text-[var(--app-warning)]' : 'text-[var(--app-text-muted)]'}`}>
          {(worker.lifecycle_state === 'pending' || worker.pending_review) ? 'Pending approval' : workerLifecycleLabel(worker.lifecycle_state)} · {workerType(worker)}
        </div>
        <div className="mt-1 px-1 text-[var(--app-text-muted)]">
          {showActivity ? <WorkerRowActivity accountScopeId={accountScopeId} workerId={worker.id} /> : <span className="block text-[10px]">Select project to load activity</span>}
        </div>
        <div className="mt-2 flex flex-wrap items-center justify-between gap-1">
          <button type="button" aria-label={`Inspect ${worker.name}`} onClick={() => onInspect(worker.id)} className={`${buttonClass} font-medium text-[var(--app-primary)]`}>Inspect worker</button>
          <button type="button" className={`${buttonClass} text-[10px] text-[var(--app-text-muted)]`} aria-label={`${preferences.hidden?.includes(worker.id) ? 'Restore' : 'Hide'} ${worker.name} in sidebar`} onClick={() => { if (preferences.hidden?.includes(worker.id)) preferences.restore(worker.id); else { preferences.hide(worker.id); setShowHidden(false) } }}>{preferences.hidden?.includes(worker.id) ? 'Restore' : 'Hide'}</button>
        </div>
      </li>)}
    </ul>
    {(!page || page.loading) && !data && <p role="status">Loading workers…</p>}
    {!page?.loading && !pendingPage?.loading && !page?.error && !pendingPage?.error && data && pendingData && !workers.length && <p className="text-[var(--app-text-muted)]">No workers on this page</p>}
    {hiddenCount > 0 && <button type="button" className={buttonClass} aria-pressed={showHidden} onClick={() => setShowHidden(!showHidden)}>{showHidden ? 'Hide hidden workers' : `Show hidden workers (${hiddenCount})`}</button>}
    {partial && <button type="button" className={`${buttonClass} text-[var(--app-text-muted)]`} onClick={onBrowse}>Browse all workers · sidebar is partial</button>}
  </section>
}
function WorkerRowActivity({ accountScopeId, workerId }: { accountScopeId: string; workerId: string }) {
  const page = useWorkerPage({ kind: 'summary', accountScopeId, workerId, timezone: 'UTC', date: new Date().toISOString().slice(0, 10) })
  const summary = page?.data && 'worker_id' in page.data ? page.data : undefined
  if (page?.error) return <span role="status" className="block text-[10px]">Activity unavailable</span>
  if (!summary) return <span className="block text-[10px]">Loading activity…</span>
  const running = summary.runs.active.filter(run => run.status === 'running').length
  return <span className="block text-[10px]">{running ? `${running}${summary.runs.active_truncated ? '+' : ''} running` : summary.runs.active_truncated ? 'Running count partial' : 'Not running'} · {summary.runs.active_runs} admitted / running{summary.runs.active_truncated ? '+' : ''} · {summary.runs.daily_runs} runs today (UTC){summary.runs.truncated ? ' · partial' : ''}{page?.stale ? ' · last-known' : ''}</span>
}
