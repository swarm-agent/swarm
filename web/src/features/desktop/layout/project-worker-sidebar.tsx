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
  const workers = [...records.values()].filter(worker => worker.account_scope_id === accountScopeId && worker.metadata?.project_id === projectId).sort((a, b) => Number(b.lifecycle_state === 'pending') - Number(a.lifecycle_state === 'pending') || b.created_at - a.created_at)
  const pending = workers.filter(worker => worker.lifecycle_state === 'pending')
  const visible = workers.filter(worker => showHidden || !preferences.hidden?.includes(worker.id))
  const hiddenCount = workers.length - workers.filter(worker => !preferences.hidden?.includes(worker.id)).length
  return <section aria-label="Project workers" className="min-w-0 space-y-1 px-2 py-2 text-xs text-slate-300">
    <div className="flex flex-wrap items-center gap-2">
      <button type="button" aria-label={`${preferences.collapsed ? 'Expand' : 'Collapse'} project Workers`} aria-expanded={!preferences.collapsed} onClick={preferences.toggle}>{preferences.collapsed ? '▸' : '▾'} Workers</button>
      <button type="button" className="text-amber-300" onClick={() => pending[0] ? onInspect(pending[0].id) : onBrowse()}>{pendingData ? `${pending.length}${pendingData.next_cursor ? '+' : ''} pending approval` : 'Checking approvals…'}</button>
    </div>
    {(page?.error || pendingPage?.error) && <p role="alert">Workers unavailable: {page?.error || pendingPage?.error}</p>}
    {(page?.stale || pendingPage?.stale) && <p role="status">Refreshing · last-known workers</p>}
    {!preferences.collapsed && <>
      {visible.slice(0, 5).map(worker => <div key={worker.id} className="flex min-w-0 items-start gap-1">
        <button type="button" onClick={() => onInspect(worker.id)} className="min-w-0 flex-1 rounded-lg p-2 text-left hover:bg-slate-800 focus-visible:ring-2 focus-visible:ring-blue-400">
          <span className="block break-words font-semibold">{worker.name}</span>
          <span className="block text-[10px] text-slate-400">{worker.lifecycle_state === 'pending' ? 'Pending approval' : workerLifecycleLabel(worker.lifecycle_state)} · {workerType(worker)}</span>
          {showActivity ? <WorkerRowActivity accountScopeId={accountScopeId} workerId={worker.id} /> : <span className="block text-[10px] text-slate-400">Select project to load activity</span>}
        </button>
        <button type="button" className="shrink-0 rounded p-1 text-[10px] hover:bg-slate-800" aria-label={`${preferences.hidden?.includes(worker.id) ? 'Restore' : 'Hide'} ${worker.name} in sidebar`} onClick={() => { if (preferences.hidden?.includes(worker.id)) preferences.restore(worker.id); else { preferences.hide(worker.id); setShowHidden(false) } }}>{preferences.hidden?.includes(worker.id) ? 'Restore' : 'Hide'}</button>
      </div>)}
      {page?.loading && !data && <p role="status">Loading workers…</p>}
      {!page?.loading && data && !workers.length && <p>No workers on this page</p>}
      {hiddenCount > 0 && <button type="button" aria-pressed={showHidden} onClick={() => setShowHidden(!showHidden)}>{showHidden ? 'Hide hidden workers' : `Show hidden workers (${hiddenCount})`}</button>}
      {(visible.length > 5 || data?.next_cursor || pendingData?.next_cursor) && <button type="button" onClick={onBrowse}>Browse all workers · sidebar is partial</button>}
    </>}
  </section>
}
function WorkerRowActivity({ accountScopeId, workerId }: { accountScopeId: string; workerId: string }) {
  const page = useWorkerPage({ kind: 'summary', accountScopeId, workerId, timezone: 'UTC', date: new Date().toISOString().slice(0, 10) })
  const summary = page?.data && 'worker_id' in page.data ? page.data : undefined
  if (page?.error) return <span role="status" className="block text-[10px]">Activity unavailable</span>
  if (!summary) return <span className="block text-[10px]">Loading activity…</span>
  const running = summary.runs.active.filter(run => run.status === 'running').length
  return <span className="block text-[10px] text-slate-400">{running ? `${running}${summary.runs.active_truncated ? '+' : ''} running` : summary.runs.active_truncated ? 'Running count partial' : 'Not running'} · {summary.runs.active_runs} admitted / running{summary.runs.active_truncated ? '+' : ''} · {summary.runs.daily_runs} runs today (UTC){summary.runs.truncated ? ' · partial' : ''}{page?.stale ? ' · last-known' : ''}</span>
}
