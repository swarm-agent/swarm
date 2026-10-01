import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { workspaceOverviewQueryOptions } from '../../queries/query-options'
import { desktopWorkers } from '../runtime/desktop-workers'
import { WorkerSettingsReview } from './worker-settings-review'
import type { WorkerRecord } from '../state/desktop-workers-api'
import { swarmWorkerHref } from './swarm-navigation'
import { proposalJobTiming } from './worker-schedule'
import { proposalGoal, proposalWorkspaces, type ProposalWorkspaceCatalog } from './worker-proposal-presentation'

export interface PendingWorkerCardProps {
  worker: WorkerRecord
  accountScopeId: string
  workspaceSlug?: string
  workspaceCatalog?: ProposalWorkspaceCatalog
  stale?: boolean
  mutationError?: string
  initialExpanded?: boolean
  onAccepted?: (worker: WorkerRecord) => void
  onOpenDetail?: (workerId: string) => void
  approvedWorker?: WorkerRecord
  showModelControls?: boolean
  acceptanceBlocked?: boolean
}

export function PendingWorkerCard(props: PendingWorkerCardProps) {
  const candidate = props.worker.pending_review
  const reviewProps = candidate ? { ...props, approvedWorker: props.worker, worker: { ...candidate, id: props.worker.id, account_scope_id: props.worker.account_scope_id, revision: props.worker.revision, local_bindings: props.worker.local_bindings, authorized_workspaces: props.worker.authorized_workspaces } } : props
  return reviewProps.workspaceCatalog ? <PendingWorkerPresentation key={`${props.accountScopeId}:${props.worker.id}`} {...reviewProps} /> : <CatalogProposal key={`${props.accountScopeId}:${props.worker.id}`} {...reviewProps} />
}

/** Reuse the authorized overview reader, never the active chat workspace. */
function CatalogProposal(props: PendingWorkerCardProps) {
  const authorized = getDesktopSessionIdentitySnapshot()?.accountScopeId === props.accountScopeId
  if (!authorized) return <PendingWorkerPresentation {...props} workspaceCatalog={{ accountScopeId: props.accountScopeId, workspaces: [] }} />
  return <AuthorizedCatalogProposal {...props} />
}

function AuthorizedCatalogProposal(props: PendingWorkerCardProps) {
  const authorized = getDesktopSessionIdentitySnapshot()?.accountScopeId === props.accountScopeId
  const options = workspaceOverviewQueryOptions([], 0, false)
  const catalog = useQuery({ ...options, queryKey: [...options.queryKey, props.accountScopeId], enabled: authorized, refetchOnWindowFocus: false, queryFn: async context => {
    if (getDesktopSessionIdentitySnapshot()?.accountScopeId !== props.accountScopeId) throw new Error('Workspace account changed')
    const result = await options.queryFn(context)
    if (getDesktopSessionIdentitySnapshot()?.accountScopeId !== props.accountScopeId) throw new Error('Workspace account changed')
    return result
  } })
  return <>
    {catalog.isPending && <p role="status" className="text-xs text-slate-400">Resolving workspace names…</p>}
    {catalog.isError && <p role="alert" className="text-xs text-amber-300">Workspace catalog unavailable; targets remain unresolved. {catalog.error.message}</p>}
    <PendingWorkerPresentation {...props} workspaceCatalog={{ accountScopeId: props.accountScopeId, workspaces: authorized && !catalog.isError ? catalog.data?.workspaces || [] : [] }} />
  </>
}

function PendingWorkerPresentation({ worker, accountScopeId, workspaceSlug, workspaceCatalog, stale = false, mutationError, initialExpanded = false, onAccepted, onOpenDetail, approvedWorker, showModelControls = true, acceptanceBlocked = false }: PendingWorkerCardProps) {
  const [settingsBlocked, setSettingsBlocked] = useState(false)
  const [expanded, setExpanded] = useState(initialExpanded)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const wrongAccount = worker.account_scope_id !== accountScopeId
  const targets = proposalWorkspaces(worker, accountScopeId, workspaceCatalog)
  const jobs = worker.automations
  const activeError = error || mutationError
  const handleAccept = async () => {
    if (busy || stale || wrongAccount || settingsBlocked || acceptanceBlocked) return
    setBusy(true)
    setError('')
    try {
      const result = await desktopWorkers.mutate({ action: 'accept', workerId: worker.id, expected_revision: worker.revision }, accountScopeId)
      if (!('worker' in result) || result.worker.pending_review || result.worker.lifecycle_state === 'pending') throw new Error('Acceptance did not return an approved worker. Refresh to check the saved revision.')
      onAccepted?.(result.worker)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Worker acceptance failed')
    } finally {
      setBusy(false)
    }
  }
  // The canonical WorkerRecord omits an empty automations slice (omitempty).
  const noJobs = !jobs?.length
  return <article className="min-w-0 space-y-3 rounded-2xl border border-amber-500/35 bg-slate-900/95 p-4 text-xs text-slate-300 shadow-lg" aria-label={`Pending durable worker: ${worker.name}`} data-testid="pending-worker-card">
    <header className="flex flex-wrap items-center gap-2">
      <h3 className="min-w-0 break-words font-bold text-white">{worker.name}</h3>
      <span className="rounded-full bg-amber-500/15 px-2 py-1 text-amber-300">Pending approval</span>
    </header>
    {approvedWorker && <section aria-label="Proposed changes" className="space-y-1"><p>Update to the same worker · review revision {approvedWorker.revision} · proposed {new Date(worker.updated_at).toLocaleString()}</p><p>Approved jobs: {approvedWorker.automations?.map(job => `${job.name} (revision ${job.revision})`).join(', ') || 'None'}</p><p>Proposed jobs: {worker.automations?.map(job => `${job.name} (revision ${job.revision})`).join(', ') || 'None'}</p><p>Approved execution: {approvedWorker.execution_mode === 'plan' ? 'Plan' : 'Swarm'} · Proposed execution: {worker.execution_mode === 'plan' ? 'Plan' : 'Swarm'}</p><p>Approved model: {approvedWorker.model_profile?.action.model || 'Swarm Default'}</p></section>}
    <p className="break-words leading-relaxed" data-testid="pending-worker-goal">{proposalGoal(worker)}</p>
    <section className="space-y-1 break-words" data-testid="pending-worker-workspaces" aria-label="Target workspaces">
      <h4 className="font-semibold text-slate-400">{targets.length > 1 ? 'Workspaces' : 'Workspace'} · Runs locally</h4>
      {targets.length ? targets.map(target => <p key={target.role} className={target.unresolved ? 'text-amber-300' : 'text-slate-200'}>{target.label} <span className="text-slate-400">· {target.status}{target.approvedTarget && target.approvedTarget !== target.target ? ` · replaces ${target.approvedName || 'unresolved previously approved workspace'}` : ''}</span></p>) : <p className="text-amber-300">No workspace target specified</p>}
    </section>
    {showModelControls && <WorkerSettingsReview worker={approvedWorker || worker} accountScopeId={accountScopeId} disabled={busy || stale || wrongAccount} onAcceptanceBlockedChange={setSettingsBlocked} />}
    <section className="space-y-2 break-words" aria-label="Jobs and timing" data-testid="pending-worker-job-intent">
      {approvedWorker && <p>Previously approved timing: {approvedWorker.automations?.map(job => `${job.name}: ${proposalJobTiming(job)}`).join('; ') || 'No jobs'}</p>}
      {noJobs ? <p data-testid="pending-worker-no-job">No job attached; waits for a task after acceptance</p> : jobs?.map(job => <div key={job.id}>
        <h4 className="font-semibold text-white">{job.name || job.plan_document?.title || 'Untitled job'}</h4>
        <p className="line-clamp-2">{job.description || job.plan_document?.info?.goal || job.plan_document?.title}</p>
        <p className="text-blue-300">{proposalJobTiming(job)}</p>
      </div>)}
    </section>
    <section className="space-y-1 border-t border-slate-800 pt-3" data-testid="pending-worker-capabilities" aria-label="Approval and requested access">
      <p className="break-words">Requested access: {worker.requested_capabilities?.length ? worker.requested_capabilities.map(cap => `${cap.description || `${cap.type}/${cap.name}`} (${cap.required ? 'required' : 'optional'})`).join('; ') : 'None requested.'}</p>
      <p className="text-slate-400">These are requests, not permission grants. The server checks authorization and workspace requirements on approval.</p>
      {targets.some(target => target.unresolved) && <p className="text-amber-300">Workspace requirements remain unresolved. Approval may be rejected until targets are available.</p>}
      {!!jobs?.some(job => job.input_requirements?.some(input => input.required)) && <p className="text-amber-300">Jobs require inputs; review the job plan for required values before approval.</p>}
      <p data-testid="pending-worker-execution-blocked">{approvedWorker ? 'Proposed changes cannot run before acceptance. Previously approved jobs remain under existing worker controls.' : 'Nothing runs while this worker is pending.'} Acceptance enables disclosed schedules and triggers for enabled jobs; manual jobs and workers without jobs wait for an explicit task.</p>
    </section>
    <button type="button" aria-expanded={expanded} onClick={() => setExpanded(!expanded)} className="rounded-lg border border-slate-700 px-3 py-2 text-blue-300 hover:bg-slate-800" data-testid="pending-worker-expand-toggle">{expanded ? 'Hide instructions and job plan' : 'View instructions and job plan'}</button>
    {expanded && <div className="space-y-3 border-t border-slate-800 pt-3" data-testid="pending-worker-expanded">
      <section data-testid="pending-worker-instructions"><h4 className="mb-2 font-semibold text-white">Full instructions</h4><p className="whitespace-pre-wrap break-words leading-relaxed">{worker.instructions || 'No instructions'}</p></section>
      {worker.description && <details><summary className="cursor-pointer">Full goal</summary><p className="whitespace-pre-wrap break-words">{worker.description}</p></details>}
      {jobs?.map(job => <details key={job.id} className="rounded-lg border border-slate-800 p-3" data-testid={`pending-worker-job-${job.id}`}>
        <summary className="cursor-pointer break-words font-semibold text-white">{job.name} · View full job plan</summary>
        <div className="mt-3 space-y-2 break-words">
          <p>{job.description}</p><p>{proposalJobTiming(job)}</p>
          <h5 className="font-semibold">{job.plan_document?.title}</h5><p className="whitespace-pre-wrap">{job.plan_document?.info?.goal}</p>
          {job.plan_document?.checkpoints?.map(cp => <div key={cp.id} className="space-y-1 border-l border-slate-700 pl-3">
            <h6 className="font-semibold">{cp.title || cp.id}</h6>
            {Array.isArray(cp.tasks) && <><p>Tasks</p><ul className="list-inside list-disc">{cp.tasks.map((task, i) => <li key={i} className="whitespace-pre-wrap">{typeof task === 'string' ? task : JSON.stringify(task, null, 2)}</li>)}</ul></>}
            {Array.isArray(cp.acceptance_criteria) && <><p>Acceptance criteria</p><ul className="list-inside list-disc">{cp.acceptance_criteria.map((criterion, i) => <li key={i} className="whitespace-pre-wrap">{typeof criterion === 'string' ? criterion : JSON.stringify(criterion, null, 2)}</li>)}</ul></>}
          </div>)}
          <p>Inputs: {job.input_requirements?.map(input => `${input.name} (${input.kind}${input.required ? ', required' : ', optional'})${input.description ? ` — ${input.description}` : ''}`).join('; ') || 'None specified'}</p>
          <p>Deliverables: {job.deliverable_requirements?.map(item => `${item.name} (${item.kind}${item.required ? ', required' : ', optional'})${item.description ? ` — ${item.description}` : ''}`).join('; ') || 'None specified'}</p>
          <details><summary className="cursor-pointer">Complete job definition (all plan fields)</summary><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words">{JSON.stringify(job, null, 2)}</pre></details>
        </div>
      </details>)}
      <details className="rounded-lg border border-slate-800 p-3"><summary className="cursor-pointer text-slate-400">Technical details and workspace roles</summary>
        <p className="mt-2 break-all">{worker.id} · revision {worker.revision}</p>
        {targets.map(target => <p key={target.role} className="break-all">{target.role}: {target.label} · {target.path || target.target || 'not assigned'} · {target.status}{target.approvedTarget ? ` · previously approved: ${target.approvedPath || target.approvedTarget}` : ''}</p>)}
        <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words">{JSON.stringify({ proposed_workspaces: worker.proposed_bindings, approved_workspaces: worker.local_bindings, workspace_requirements: worker.workspace_requirements, requested_access: worker.requested_capabilities }, null, 2)}</pre>
      </details>
    </div>}
    <footer className="flex flex-wrap items-center gap-3 border-t border-slate-800 pt-3">
      <button type="button" disabled={busy || stale || wrongAccount || settingsBlocked || acceptanceBlocked} onClick={handleAccept} className="rounded-lg bg-emerald-600 px-4 py-2 font-semibold text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50" data-testid="accept-pending-worker">{busy ? 'Accepting…' : approvedWorker ? 'Accept changes' : 'Accept worker'}</button>
      <a href={swarmWorkerHref(workspaceSlug, worker.id)} onClick={e => { if (onOpenDetail && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey && e.button === 0) { e.preventDefault(); onOpenDetail(worker.id) } }} className="text-blue-300 hover:underline" data-testid="pending-worker-detail-link">Open worker detail</a>
      {stale && <p role="alert" className="text-amber-300">Worker definition is refreshing. Acceptance is blocked on stale revisions.</p>}
      {wrongAccount && <p role="alert">Worker account does not match the current account. Acceptance is blocked.</p>}
      {(settingsBlocked || acceptanceBlocked) && <p role="status">Propose or discard your local model edits before accepting the saved revision.</p>}
      {activeError && <p role="alert" className="text-red-300">{activeError}</p>}
    </footer>
  </article>
}
