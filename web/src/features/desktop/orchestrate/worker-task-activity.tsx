import { ScopeUsage } from './scope-usage'
import { useState } from 'react'
import { useWorkerPage } from '../runtime/desktop-workers'
import { PendingWorkerCard } from './pending-worker-card'
import { WorkerRunHistory } from './worker-hub'
import { workerDetailHref, workerLifecycleLabel } from './worker-presentation'

/** Ten demanded run pages at a time; no polling or session-derived worker identities. */
export function WorkerTaskActivity({ accountScopeId, workspaceSlug, onOpenWorkerDetail }: {
  accountScopeId: string; workspaceSlug?: string; onOpenWorkerDetail?: (workerId: string) => void
}) {
  const [cursor, setCursor] = useState<string | undefined>()
  const page = useWorkerPage({ kind: 'list', accountScopeId, limit: 10, cursor })
  const workers = page?.data && 'workers' in page.data ? page.data.workers : []
  const next = page?.data && 'workers' in page.data ? page.data.next_cursor : undefined
  return <section className="mx-3.5 my-3 max-h-[45vh] shrink-0 space-y-3 overflow-y-auto text-xs text-slate-300" data-testid="tasks-worker-activity">
    <header><h3 className="font-semibold text-white">Worker tasks</h3><p className="mt-1 text-slate-400">Account-wide activity · tagged by worker · latest runs and prior results</p></header>
    {page?.error && <p role="alert">Workers: {page.error}</p>}
    {page?.stale && page.data && <p className="text-amber-300">Refreshing workers; displayed entries may be out of date.</p>}
    {page?.loading && !page.data && <p role="status">Loading worker activity…</p>}
    {workers.map(worker => <section key={worker.id} className="rounded-xl border border-slate-800 p-3 space-y-3">
      <header className="flex flex-wrap items-center justify-between gap-2"><a href={workerDetailHref(worker.id, workspaceSlug)} onClick={onOpenWorkerDetail ? event => { event.preventDefault(); onOpenWorkerDetail(worker.id) } : undefined} className="font-semibold text-blue-300 hover:underline">{worker.name}</a><span className="text-slate-400">{workerLifecycleLabel(worker.lifecycle_state)} · {worker.automations?.length || 0} jobs</span></header>
      <ScopeUsage input={{ accountScopeId, scope: { kind: 'worker', id: worker.id } }} />
      <p className="break-words text-slate-400">{worker.description || worker.instructions}</p>
      {(worker.lifecycle_state === 'pending' || worker.pending_review) ? <PendingWorkerCard key={`${accountScopeId}:${worker.id}`} worker={worker} accountScopeId={accountScopeId} workspaceSlug={workspaceSlug} stale={!!page?.stale || !!page?.error} mutationError={page?.mutationError} onOpenDetail={onOpenWorkerDetail} /> : <WorkerRunHistory accountScopeId={accountScopeId} worker={worker} workspaceSlug={workspaceSlug} />}
    </section>)}
    {!page?.loading && !page?.error && !workers.length && <p>No workers on this page. Add one from Workers using Orchestrator.</p>}
    <div className="flex gap-3">{cursor && <button onClick={() => setCursor(undefined)}>First workers</button>}{next && <button onClick={() => setCursor(next)}>More workers</button>}</div>
  </section>
}
