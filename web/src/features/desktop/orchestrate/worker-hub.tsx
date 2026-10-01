import { ScopeUsage, WorkerBudget } from './scope-usage'
import { useState } from 'react'
import { useWorkerNavigationPreferences } from '../layout/worker-navigation-preferences'
import { WorkerSettingsReview } from './worker-settings-review'
import { proposalWorkspaces } from './worker-proposal-presentation'
import { PendingWorkerCard } from './pending-worker-card'
import { formatWorkerSchedule } from './worker-schedule'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { desktopWorkers, useWorkerPage } from '../runtime/desktop-workers'
import type { WorkerRecord, WorkerRun } from '../state/desktop-workers-api'
import { workerLifecycleLabel, workerRunCategories, workerRunCategory, workerSessionHref, type WorkerRunCategory } from './worker-presentation'

export interface SelectedWorker { id: string; revision: number; name: string }
const box = 'rounded-xl border border-slate-800 bg-slate-900/50 p-4 text-xs text-slate-300 space-y-3'
const button = 'rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-xs text-slate-200 hover:bg-slate-700 disabled:opacity-50'
const date = (value?: number) => value ? new Date(value).toLocaleString() : 'Not recorded'
type HubProps = {
  onSelectWorker: (selection: SelectedWorker) => void
  onAddWorker: () => void
  onInspectWorker?: (id: string) => void
  workspaceSlug?: string
  initialWorkerId?: string
}
export function WorkerHub(props: HubProps) {
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  if (!accountScopeId) return <section role="alert" className={box}>Reconnect to view your workers.</section>
  return <WorkerHubAccount key={accountScopeId} {...props} accountScopeId={accountScopeId} />
}
function WorkerHubAccount({ accountScopeId, onSelectWorker, onAddWorker, onInspectWorker, workspaceSlug, initialWorkerId }: HubProps & { accountScopeId: string }) {
  const [cursor, setCursor] = useState<string | undefined>()
  const [pendingOnly, setPendingOnly] = useState(false)
  const page = useWorkerPage({ kind: 'list', accountScopeId, cursor, limit: 100 })
  const workers = page?.data && 'workers' in page.data ? page.data.workers.filter(worker => !pendingOnly || worker.lifecycle_state === 'pending' || worker.pending_review) : []
  const next = page?.data && 'workers' in page.data ? page.data.next_cursor : undefined
  const inspected = initialWorkerId
  return <section className="flex min-h-0 min-w-0 flex-1 flex-col text-slate-300" aria-label="Durable workers" data-testid="durable-worker-hub">
    <header className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-800 p-5">
      <div><h2 className="text-lg font-bold text-white">Workers</h2><p className="text-xs text-slate-400">Your standing team · {workers.length}{next ? '+' : ''} on this page · account-wide</p></div>
      <div className="flex flex-wrap gap-2"><button className={button} aria-pressed={pendingOnly} onClick={() => { setPendingOnly(!pendingOnly); setCursor(undefined) }}>{pendingOnly ? 'Show all workers' : 'Pending approvals'}</button><button className={button} onClick={() => desktopWorkers.invalidate(undefined, accountScopeId)}>Refresh</button><button className="rounded-lg bg-blue-600 px-4 py-2 text-xs font-semibold text-white hover:bg-blue-500" onClick={onAddWorker}>+ Add worker</button></div>
    </header>
    {page?.error && <p role="alert" className="px-5 py-2 text-red-300">Worker list: {page.error}</p>}
    {page?.stale && page.data && <p className="px-5 py-2 text-xs text-amber-300">Refreshing workers; displayed entries may be out of date.</p>}
    <div className="swarm-workers-layout">
      <nav aria-label="Worker list" className="swarm-workers-list space-y-2 border-b border-slate-800 p-3">
        {workers.map(worker => <div key={worker.id}><WorkerSidebarRestore accountScopeId={accountScopeId} worker={worker} /><button type="button" aria-label={`Inspect ${worker.name}`} aria-pressed={inspected === worker.id} onClick={() => { onInspectWorker?.(worker.id) }} className={`w-full rounded-xl border p-3 text-left text-xs ${inspected === worker.id ? 'border-blue-500/60 bg-blue-500/10' : 'border-slate-800 hover:bg-slate-800/60'}`}>
          <span className="block break-words font-semibold text-white">{worker.name}</span>
          <span className="mt-1 block text-[10px] text-slate-400">{workerLifecycleLabel(worker.lifecycle_state)}{worker.pending_review ? ' · Changes pending approval' : ''} · {worker.automations?.length || 0} jobs</span>
          <span className="mt-2 line-clamp-2 break-words text-slate-400">{worker.description || worker.instructions || 'Purpose not recorded'}</span>
        </button></div>)}
        {page?.loading && !page.data && <p role="status">Loading workers…</p>}
        {!page?.loading && !page?.error && !workers.length && <p className="p-3 text-xs">No workers here yet. Ask Orchestrator to build one for you.</p>}
        {next && <button className={button} onClick={() => setCursor(next)}>Next workers</button>}
        {cursor && <button className={button} onClick={() => setCursor(undefined)}>First page</button>}
      </nav>
      <div className="swarm-workers-detail min-w-0 p-4">
        {inspected ? <WorkerDetail key={inspected} accountScopeId={accountScopeId} workerId={inspected} workspaceSlug={workspaceSlug} onSelectWorker={onSelectWorker} onClose={() => { onInspectWorker?.('') }} /> : <div className="p-8 text-center"><h3 className="font-semibold text-white">A worker for the work that repeats</h3><p className="mt-2 text-sm text-slate-400">Describe the job to Orchestrator. Review the proposal here before it can run.</p><button className={`${button} mt-4`} onClick={onAddWorker}>Add worker with Orchestrator</button></div>}
      </div>
    </div>
  </section>
}
export function WorkerDetail({ accountScopeId, workerId, workspaceSlug, onSelectWorker }: {
  accountScopeId: string; workerId: string; workspaceSlug?: string; onSelectWorker: (selection: SelectedWorker) => void; onClose: () => void
}) {
  const detail = useWorkerPage({ kind: 'detail', accountScopeId, workerId })
  const summaryPage = useWorkerPage({ kind: 'summary', accountScopeId, workerId, timezone: 'UTC', date: new Date().toISOString().slice(0, 10) })
  const worker = detail?.data && 'worker' in detail.data ? detail.data.worker : undefined
  const summary = summaryPage?.data && 'worker_id' in summaryPage.data ? summaryPage.data : undefined
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [acceptanceBlocked, setAcceptanceBlocked] = useState(false)
  const readOnly = Boolean(worker?.provenance?.migrated_at)
  return <article className="space-y-5 text-xs" data-testid="durable-worker-detail">
    {detail?.error && <p role="alert" className="text-red-300">Worker detail: {detail.error}</p>}
    {detail?.mutationError && <p role="alert">{detail.mutationError}</p>}
    {detail?.stale && worker && <p className="text-amber-300">Updating details; actions are unavailable until the current revision is loaded.</p>}
    {!worker && !detail?.error && <p role="status">Loading worker…</p>}
    {worker && <>
      <header className="space-y-3"><div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0"><p className="mb-1 text-[10px] uppercase tracking-widest text-slate-500">Worker · {workerLifecycleLabel(worker.lifecycle_state)}</p><h3 className="break-words text-xl font-semibold text-white">{worker.name}</h3></div>
        <button className={button} disabled={!!detail?.stale || !!detail?.error} onClick={() => onSelectWorker({ id: worker.id, revision: worker.revision, name: worker.name })}>Ask Orchestrator</button></div>
        {worker.lifecycle_state !== 'pending' && <p className="break-words text-sm leading-relaxed text-slate-300">{worker.description || worker.instructions || 'Purpose not recorded.'}</p>}
      {worker.lifecycle_state !== 'pending' && worker.model_profile?.resolution_warning && <p role="alert" className="text-xs text-amber-300">{worker.model_profile.resolution_warning}</p>}
        {readOnly && <p>Migrated snapshot · read-only</p>}
        <section aria-label="Approved workspaces" className="space-y-1">{proposalWorkspaces({ ...worker, proposed_bindings: worker.lifecycle_state === 'pending' ? worker.proposed_bindings : undefined }, accountScopeId).map(target => <p key={target.role} className="break-words">{target.label} · {target.status}</p>)}</section>
        {!readOnly && <WorkerSettingsReview key={worker.id} worker={worker} accountScopeId={accountScopeId} disabled={!!detail?.stale || !!detail?.error} onAcceptanceBlockedChange={setAcceptanceBlocked} />}
        {(worker.lifecycle_state === 'pending' || worker.pending_review) && <PendingWorkerCard acceptanceBlocked={acceptanceBlocked} showModelControls={false} key={`${worker.id}:${worker.revision}`} worker={worker} accountScopeId={accountScopeId} workspaceSlug={workspaceSlug} stale={!!detail?.stale || !!detail?.error} mutationError={detail?.mutationError} />}
      </header>
      {worker.lifecycle_state !== 'pending' && <>
      <ScopeUsage input={{ accountScopeId, scope: { kind: 'worker', id: workerId } }} />
      <WorkerBudget key={`${accountScopeId}:${workerId}`} accountScopeId={accountScopeId} workerId={workerId} disabled={readOnly || !!detail?.stale || !!detail?.error} />
      <section className={box}><h4 className="font-semibold text-white">Now</h4>
        {summaryPage?.error && <p role="alert">Activity unavailable: {summaryPage.error}</p>}
        {summaryPage?.stale && summary && <p className="text-amber-300">Activity is refreshing; these are last-known results.</p>}
        {!summary ? <p>Loading recorded activity…</p> : <>
          <p className="text-sm text-white">{summary.runs.active_runs > 0 ? `${summary.runs.active_runs} admitted / running` : worker.lifecycle_state === 'active' ? 'Waiting for the next job or request' : workerLifecycleLabel(worker.lifecycle_state)}</p>
          <p>{summary.runs.daily_runs} runs today (UTC){summary.runs.truncated ? ' · partial count' : ''} · {summary.runs.daily_success} succeeded · {summary.runs.daily_failed} failed</p>
          {summary.runs.active.map(run => <div key={run.id} className="flex flex-wrap justify-between gap-2"><span>{run.status === 'running' ? 'Running' : 'Admitted'} · {date(run.created_at)}</span>{run.session_id && <a className="text-blue-300 underline" href={workerSessionHref(run.session_id, workspaceSlug)}>Open live session</a>}</div>)}
          {summary.runs.active_truncated && <p>More active runs exist; open run history below.</p>}
          <p className="text-slate-400">Next eligible schedule: {summary.next_scheduled_at ? date(summary.next_scheduled_at) : 'None reported'}. Enabled means available to run, not executing.</p>
        </>}
        {!readOnly && (worker.lifecycle_state === 'active' || worker.lifecycle_state === 'paused') && <button className={button} disabled={busy || !!detail?.stale || !!detail?.error} onClick={async () => {
          setBusy(true); setError(''); setNotice('')
          try {
            const result = await desktopWorkers.mutate({ action: worker.lifecycle_state === 'active' ? 'pause' : 'resume', workerId, expected_revision: worker.revision }, accountScopeId)
            if ('worker' in result && result.worker.lifecycle_state === 'stopping') setNotice('Stopping: awaiting active run acknowledgement.')
          }
          catch (cause) { setError(cause instanceof Error ? cause.message : 'Worker change failed') } finally { setBusy(false) }
        }}>{worker.lifecycle_state === 'active' ? 'Pause worker' : 'Resume worker'}</button>}
        {error && <p role="alert" className="text-red-300">{error}</p>}{notice && <p role="status">{notice}</p>}
      </section>
      <section className="space-y-3"><h4 className="font-semibold text-white">Jobs <span className="text-slate-500">{worker.automations?.length || 0}</span></h4>
        {(worker.automations || []).map(job => <div key={job.id} className={box}><div className="flex flex-wrap justify-between gap-2"><h5 className="font-semibold text-white">{job.name}</h5><span className="text-slate-400">{job.enabled ? 'Enabled' : 'Disabled'}</span></div><p>{job.description || job.plan_document.info?.goal || job.plan_document.title}</p><p className="text-blue-300">{formatWorkerSchedule(job)}</p><ol className="list-inside list-decimal space-y-1">{job.plan_document.checkpoints?.map(cp => <li key={cp.id}>{cp.title || cp.id}</li>)}</ol>{!readOnly && <button type="button" className={button} disabled={busy || !!detail?.stale || !!detail?.error} onClick={async () => {
          setBusy(true); setError('')
          try { await desktopWorkers.mutate({ action: job.enabled ? 'disableAutomation' : 'enableAutomation', workerId, automationId: job.id, expected_revision: worker.revision }, accountScopeId) }
          catch (cause) { setError(cause instanceof Error ? cause.message : 'Job change failed') }
          finally { setBusy(false) }
        }}>{job.enabled ? 'Disable job' : 'Enable approved job'}</button>}<p>Job revision {job.revision} · created {date(job.created_at)} · updated {date(job.updated_at)}</p><p className="text-slate-400">Expected outputs: {job.deliverable_requirements?.map(item => item.name).join(', ') || 'Not specified'}</p></div>)}
        {!worker.automations?.length && <p className={box}>No jobs attached. Ask Orchestrator to add a job or give this worker a one-off request.</p>}
      </section>
      <WorkerChangeHistory accountScopeId={accountScopeId} worker={worker} />
      <WorkerRunHistory key={worker.id} accountScopeId={accountScopeId} worker={worker} workspaceSlug={workspaceSlug} />
      <details className={box}><summary className="cursor-pointer text-slate-400">Standing instructions &amp; identity</summary><p className="whitespace-pre-wrap break-words">{worker.instructions || 'No instructions recorded.'}</p><p className="break-all text-slate-500">{worker.id} · revision {worker.revision}</p><p>Workspace roles: {worker.workspace_requirements?.map(item => item.description || item.role).join(', ') || 'None specified'}</p></details>
      </>}
    </>}
  </article>
}
export function WorkerRunHistory({ accountScopeId, worker, workspaceSlug }: { accountScopeId: string; worker: WorkerRecord; workspaceSlug?: string }) {
  const [cursor, setCursor] = useState<string | undefined>()
  const [category, setCategory] = useState<WorkerRunCategory>('All')
  const page = useWorkerPage({ kind: 'runs', accountScopeId, workerId: worker.id, cursor, limit: 25 })
  const runs = page?.data && 'runs' in page.data && Array.isArray(page.data.runs) ? page.data.runs : []
  const next = page?.data && 'next_cursor' in page.data ? page.data.next_cursor : undefined
  const filtered = runs.filter(run => category === 'All' || workerRunCategory(run) === category)
  return <section className="space-y-3" aria-label="Worker run history"><h4 className="font-semibold text-white">Run history</h4><p className="text-slate-400">Newest first · categories apply to this page</p>
    <div className="flex flex-wrap gap-1">{workerRunCategories.map(item => <button key={item} className={`${button} ${category === item ? 'border-blue-500 text-blue-300' : ''}`} aria-pressed={category === item} onClick={() => setCategory(item)}>{item}</button>)}</div>
    {page?.error && <p role="alert">Run history: {page.error}</p>}{page?.stale && page.data && <p className="text-amber-300">Refreshing run history; displayed results may be stale.</p>}
    {page?.loading && !page.data && <p role="status">Loading runs…</p>}
    {filtered.map(run => <WorkerRunRow key={run.id} run={run} worker={worker} accountScopeId={accountScopeId} workspaceSlug={workspaceSlug} stale={!!page?.stale || !!page?.error} />)}
    {!page?.loading && !page?.error && !filtered.length && <p className={box}>No {category === 'All' ? '' : category.toLowerCase() + ' '}runs on this page.</p>}
    <div className="flex gap-2">{cursor && <button className={button} onClick={() => setCursor(undefined)}>Latest runs</button>}{next && <button className={button} onClick={() => setCursor(next)}>Older runs</button>}</div>
  </section>
}
export function WorkerRunRow({ run, worker, accountScopeId, workspaceSlug, stale }: { run: WorkerRun; worker: WorkerRecord; accountScopeId: string; workspaceSlug?: string; stale?: boolean }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const job = worker.automations?.find(item => item.id === run.automation_id)
  return <article className={box} data-testid="durable-worker-run">
    <div className="flex flex-wrap items-center gap-2"><span className="rounded bg-blue-500/10 px-2 py-1 text-blue-300">{worker.name}</span><strong className="break-words text-white">{job?.name || (run.automation_id ? 'Prior job' : 'One-off request')}</strong><span className="ml-auto text-slate-400">{run.status}{run.cancel_requested ? ' · cancellation requested' : ''}</span></div>
    <ScopeUsage input={{ accountScopeId, scope: { kind: 'worker_run', id: run.id, project_id: worker.id } }} label="Run observed usage" />
    <p>Worker revision {run.worker_revision}{run.automation_revision ? ` · job revision ${run.automation_revision}` : ''}</p>
    <p className="text-slate-400">{date(run.started_at || run.created_at)} · {run.request_source}{run.completed_at ? ` · finished ${date(run.completed_at)}` : ''}</p>
    {run.error && <p className="break-words text-red-300">{run.error}</p>}
    {!!run.deliverables?.length && <p>{run.deliverables.length} deliverable(s) · {run.session_id ? 'open the session to review outputs' : 'no execution session linked'}</p>}
    {run.session_id && <a className="inline-block text-blue-300 underline" href={workerSessionHref(run.session_id, workspaceSlug)}>Open execution session</a>}
    {!worker.provenance?.migrated_at && (run.status === 'admitted' || run.status === 'running') && <button className={`${button} ml-2`} disabled={busy || stale || run.cancel_requested} onClick={async () => {
      setBusy(true); setError(''); setNotice('')
      try { await desktopWorkers.mutate({ action: 'cancelRun', workerId: worker.id, runId: run.id }, accountScopeId); setNotice('Cancellation requested; awaiting acknowledgement.') }
      catch (cause) { setError(cause instanceof Error ? cause.message : 'Cancellation failed') } finally { setBusy(false) }
    }}>Stop run</button>}
    {error && <p role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}
  </article>
}

function WorkerSidebarRestore({ accountScopeId, worker }: { accountScopeId: string; worker: WorkerRecord }) {
  const projectId = typeof worker.metadata?.project_id === 'string' ? worker.metadata.project_id : ''
  const preferences = useWorkerNavigationPreferences(accountScopeId, projectId)
  return projectId && preferences.hidden?.includes(worker.id) ? <button type="button" className={`${button} mb-1`} onClick={() => preferences.restore(worker.id)}>Restore {worker.name} in project sidebar</button> : null
}

export function WorkerChangeHistory({ accountScopeId, worker }: { accountScopeId: string; worker: WorkerRecord }) {
  const [cursor, setCursor] = useState<string | undefined>()
  const page = useWorkerPage({ kind: 'history', accountScopeId, workerId: worker.id, limit: 25, cursor })
  const data = page?.data && 'revisions' in page.data ? page.data : undefined
  return <section className="space-y-3" aria-label="Worker change history"><h4 className="font-semibold text-white">Change history</h4><p>Oldest first · durable worker revisions</p>
    {page?.error && <p role="alert">Change history: {page.error}</p>}
    {page?.stale && <p className="text-amber-300">Refreshing; last-known revisions.</p>}
    {page?.loading && !data && <p role="status">Loading revisions…</p>}
    {data?.revisions.map(revision => <details key={revision.revision} className={box}><summary>Revision {revision.revision} · {date(revision.committed_at)} · {revision.change_summary || 'Worker changed'}</summary><p>{revision.worker.pending_review ? 'Proposal pending approval' : 'Recorded worker definition'} · {revision.worker.execution_mode === 'plan' ? 'Plan' : 'Swarm'}</p><p>Jobs: {revision.worker.automations?.map(job => `${job.name} (revision ${job.revision})`).join(', ') || 'None'}</p><pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words">{JSON.stringify(revision.worker, null, 2)}</pre></details>)}
    {cursor && <button className={button} onClick={() => setCursor(undefined)}>First changes</button>}{data?.next_cursor && <button className={button} onClick={() => setCursor(data.next_cursor)}>Later changes</button>}
  </section>
}
