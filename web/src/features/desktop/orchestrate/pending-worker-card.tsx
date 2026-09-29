import { useState } from 'react'
import { Bot, ChevronDown, ChevronUp, CheckCircle2, ExternalLink } from 'lucide-react'
import { desktopWorkers } from '../runtime/desktop-workers'
import type { WorkerAutomation, WorkerRecord } from '../state/desktop-workers-api'
import { swarmWorkerHref } from './swarm-navigation'
import { formatWorkerSchedule } from './worker-hub'

export interface PendingWorkerCardProps {
  worker: WorkerRecord
  accountScopeId: string
  workspaceSlug?: string
  stale?: boolean
  mutationError?: string
  initialExpanded?: boolean
  onAccepted?: (worker: WorkerRecord) => void
  onOpenDetail?: (workerId: string) => void
}

/**
 * Expandable card for pending durable workers proposed by Orchestrator.
 * Surfaces exact standing instructions, proposed vs approved workspaces,
 * requested capabilities (requests, not grants), optional job intent,
 * blocked execution controls, and human acceptance by exact revision.
 */
export function PendingWorkerCard({
  worker,
  accountScopeId,
  workspaceSlug,
  stale = false,
  mutationError,
  initialExpanded = false,
  onAccepted,
  onOpenDetail,
}: PendingWorkerCardProps) {
  const [expanded, setExpanded] = useState(initialExpanded)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const handleAccept = async () => {
    if (busy || stale) return
    setBusy(true)
    setError('')
    try {
      const result = await desktopWorkers.mutate(
        {
          action: 'accept',
          workerId: worker.id,
          expected_revision: worker.revision,
        },
        accountScopeId,
      )
      if ('worker' in result) {
        onAccepted?.(result.worker)
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Worker acceptance failed')
    } finally {
      setBusy(false)
    }
  }

  const detailHref = swarmWorkerHref(workspaceSlug, worker.id)
  const hasJobs = Boolean(worker.automations && worker.automations.length > 0)
  const jobsCount = worker.automations?.length ?? 0
  const activeError = error || mutationError

  return (
    <article
      className="rounded-2xl border border-amber-500/35 bg-gradient-to-r from-amber-500/10 via-slate-900/90 to-slate-900/95 p-3.5 shadow-lg transition-all"
      aria-label={`Pending durable worker: ${worker.name}`}
      data-testid="pending-worker-card"
    >
      {/* Header / Summary row */}
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-3 min-w-0">
          <div className="h-8 w-8 rounded-xl bg-amber-500/20 text-amber-400 flex items-center justify-center border border-amber-500/40 shrink-0 shadow-sm mt-0.5">
            <Bot size={17} />
          </div>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-xs font-bold text-white truncate">{worker.name}</h3>
              <span className="rounded-full bg-amber-500/20 text-amber-300 border border-amber-500/40 px-2 py-0.5 text-[10px] font-semibold animate-pulse">
                Pending Acceptance
              </span>
              <span className="text-[10px] font-mono text-slate-400">r{worker.revision}</span>
              <span className="rounded bg-slate-900 border border-slate-800 px-1.5 py-0.2 text-[10px] font-mono text-slate-400">
                {hasJobs ? `${jobsCount} job${jobsCount === 1 ? '' : 's'} planned` : 'No job attached'}
              </span>
            </div>
            <p className="text-[10px] font-mono text-slate-500 mt-0.5 truncate">{worker.id}</p>
            {worker.description && (
              <p className="text-xs text-slate-300 mt-1 line-clamp-1">{worker.description}</p>
            )}
          </div>
        </div>

        <div className="flex items-center gap-2 shrink-0">
          <button
            type="button"
            onClick={() => setExpanded(!expanded)}
            aria-expanded={expanded}
            className="flex items-center gap-1 px-2.5 py-1.5 rounded-lg border border-slate-700 bg-slate-800/80 hover:bg-slate-700 text-xs font-medium text-slate-300 hover:text-white transition"
            data-testid="pending-worker-expand-toggle"
          >
            <span>{expanded ? 'Collapse' : 'Expand'}</span>
            {expanded ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
          </button>
        </div>
      </div>

      {/* Expanded details */}
      {expanded && (
        <div className="mt-3.5 pt-3.5 border-t border-slate-800/80 space-y-3.5 text-xs" data-testid="pending-worker-expanded">
          {/* Standing instructions */}
          <div className="space-y-1" data-testid="pending-worker-instructions">
            <h4 className="text-[11px] font-bold text-slate-400 uppercase tracking-wider">Standing Instructions</h4>
            <p className="text-xs text-slate-200 whitespace-pre-wrap rounded-lg bg-slate-950/80 p-2.5 border border-slate-800 leading-relaxed font-mono">
              {worker.instructions || 'No instructions'}
            </p>
          </div>

          {/* Workspaces (Proposed vs Approved) */}
          <div className="space-y-1.5" data-testid="pending-worker-workspaces">
            <h4 className="text-[11px] font-bold text-slate-400 uppercase tracking-wider">Workspaces</h4>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
              <div className="p-2.5 rounded-lg bg-slate-950/60 border border-slate-800">
                <span className="font-semibold text-amber-300 text-[11px] block mb-1">Proposed Workspaces:</span>
                {worker.proposed_bindings && Object.keys(worker.proposed_bindings).length > 0 ? (
                  <ul className="space-y-0.5 font-mono text-[11px]">
                    {Object.entries(worker.proposed_bindings).map(([role, target]) => (
                      <li key={role}>
                        <span className="text-slate-400">{role}:</span>{' '}
                        <span className="text-slate-200">{target}</span>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <span className="text-slate-500 text-[11px]">None proposed</span>
                )}
              </div>
              <div className="p-2.5 rounded-lg bg-slate-950/60 border border-slate-800">
                <span className="font-semibold text-emerald-300 text-[11px] block mb-1">Approved Local Bindings:</span>
                {worker.local_bindings && Object.keys(worker.local_bindings).length > 0 ? (
                  <ul className="space-y-0.5 font-mono text-[11px]">
                    {Object.entries(worker.local_bindings).map(([role, target]) => (
                      <li key={role}>
                        <span className="text-slate-400">{role}:</span>{' '}
                        <span className="text-slate-200">{target}</span>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <span className="text-slate-500 text-[11px]">None approved (pending human acceptance)</span>
                )}
              </div>
            </div>
            {worker.workspace_requirements && worker.workspace_requirements.length > 0 && (
              <div className="text-[11px] text-slate-400 pt-0.5">
                <span className="font-medium text-slate-300">Declared requirements: </span>
                {worker.workspace_requirements.map(req => `${req.role}: ${req.description || 'No description'} (${req.required ? 'required' : 'optional'})`).join('; ')}
              </div>
            )}
          </div>

          {/* Requested Capabilities */}
          <div className="space-y-1" data-testid="pending-worker-capabilities">
            <div className="flex items-center justify-between">
              <h4 className="text-[11px] font-bold text-slate-400 uppercase tracking-wider">Requested Capabilities</h4>
              <span className="text-[10px] text-slate-500">Capabilities are requests, not grants</span>
            </div>
            <p className="text-[11px] text-slate-400">
              Authorization is verified by the server on acceptance. No grants are automatically issued.
            </p>
            {worker.requested_capabilities && worker.requested_capabilities.length > 0 ? (
              <div className="flex flex-wrap gap-1.5 pt-0.5">
                {worker.requested_capabilities.map(cap => (
                  <span
                    key={`${cap.type}-${cap.name}`}
                    className="px-2 py-0.5 rounded bg-slate-950 border border-slate-800 text-[11px] font-mono text-slate-300"
                  >
                    {cap.type}/{cap.name}
                    {cap.required ? ' (required)' : ' (optional)'}
                    {cap.description ? ` — ${cap.description}` : ''}
                  </span>
                ))}
              </div>
            ) : (
              <p className="text-xs text-slate-500">No capabilities requested.</p>
            )}
          </div>

          {/* Planned Job Intent */}
          <div className="space-y-2" data-testid="pending-worker-job-intent">
            <h4 className="text-[11px] font-bold text-slate-400 uppercase tracking-wider">Planned Job Intent</h4>
            {!hasJobs ? (
              <div
                className="p-3 rounded-xl border border-dashed border-slate-800 bg-slate-950/40 text-xs text-slate-400"
                data-testid="pending-worker-no-job"
              >
                No job attached; waits for a task after acceptance
              </div>
            ) : (
              <div className="space-y-2.5" data-testid="pending-worker-jobs-list">
                {worker.automations!.map((auto: WorkerAutomation) => (
                  <div
                    key={auto.id}
                    className="p-3 rounded-xl border border-slate-800 bg-slate-950/70 space-y-2.5"
                    data-testid={`pending-worker-job-${auto.id}`}
                  >
                    <div className="flex items-center justify-between pb-1.5 border-b border-slate-800/80">
                      <div>
                        <span className="text-xs font-bold text-white">{auto.name}</span>
                        {auto.description && <p className="text-[11px] text-slate-400">{auto.description}</p>}
                      </div>
                      <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-900 border border-slate-800 text-slate-300">
                        Timing: {formatWorkerSchedule(auto)}
                      </span>
                    </div>

                    {/* Plan title & Goal */}
                    <div className="text-xs space-y-0.5">
                      <div className="flex items-center gap-2">
                        <span className="font-semibold text-slate-200">Plan:</span>
                        <span className="text-slate-300">{auto.plan_document?.title || 'Untitled'}</span>
                      </div>
                      {auto.plan_document?.info?.goal && (
                        <p className="text-[11px] text-slate-400">Goal: {String(auto.plan_document.info.goal)}</p>
                      )}
                    </div>

                    {/* Complete checkpoints with tasks & acceptance criteria */}
                    {auto.plan_document?.checkpoints && auto.plan_document.checkpoints.length > 0 && (
                      <div className="space-y-2 pl-2 border-l-2 border-slate-800">
                        <span className="text-[10px] font-bold text-slate-400 uppercase tracking-wider block">
                          Checkpoints & Acceptance Criteria
                        </span>
                        {auto.plan_document.checkpoints.map((cp: Record<string, unknown>) => (
                          <div key={String(cp.id)} className="text-xs space-y-1 bg-slate-900/40 p-2 rounded-lg border border-slate-800/60">
                            <span className="font-semibold text-slate-200 block">
                              {String(cp.id)}: {String(cp.title || 'Untitled')}
                            </span>
                            {Array.isArray(cp.tasks) && cp.tasks.length > 0 && (
                              <div className="space-y-0.5">
                                <span className="text-[10px] text-slate-400 font-medium">Tasks:</span>
                                <ul className="list-disc list-inside text-[11px] text-slate-300 pl-1 space-y-0.5">
                                  {cp.tasks.map((task: unknown, idx: number) => (
                                    <li key={idx}>{String(task)}</li>
                                  ))}
                                </ul>
                              </div>
                            )}
                            {Array.isArray(cp.acceptance_criteria) && cp.acceptance_criteria.length > 0 && (
                              <div className="space-y-0.5">
                                <span className="text-[10px] text-emerald-400 font-medium">Acceptance criteria:</span>
                                <ul className="list-disc list-inside text-[11px] text-emerald-300/90 pl-1 space-y-0.5">
                                  {cp.acceptance_criteria.map((crit: unknown, idx: number) => (
                                    <li key={idx}>{String(crit)}</li>
                                  ))}
                                </ul>
                              </div>
                            )}
                          </div>
                        ))}
                      </div>
                    )}

                    {/* Inputs & Deliverables */}
                    <div className="grid grid-cols-1 md:grid-cols-2 gap-2 text-xs pt-1 border-t border-slate-850">
                      <div className="text-[11px]">
                        <span className="font-semibold text-slate-400">Inputs: </span>
                        {auto.input_requirements && auto.input_requirements.length > 0 ? (
                          <span className="font-mono text-slate-300">
                            {auto.input_requirements.map(req => `${req.name} (${req.kind}${req.required ? ', req' : ''})`).join(', ')}
                          </span>
                        ) : (
                          <span className="text-slate-500">None required</span>
                        )}
                      </div>
                      <div className="text-[11px]">
                        <span className="font-semibold text-slate-400">Deliverables: </span>
                        {auto.deliverable_requirements && auto.deliverable_requirements.length > 0 ? (
                          <span className="font-mono text-slate-300">
                            {auto.deliverable_requirements.map(req => `${req.name} (${req.kind}${req.required ? ', req' : ''})`).join(', ')}
                          </span>
                        ) : (
                          <span className="text-slate-500">None specified</span>
                        )}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Blocked Execution Controls Notice */}
          <div
            className="p-2.5 rounded-xl bg-slate-950/60 border border-slate-800 text-[11px] text-slate-400 italic"
            data-testid="pending-worker-execution-blocked"
          >
            Execution controls are blocked while worker is pending human acceptance. Direct and scheduled runs will only activate after acceptance.
          </div>

          {/* Actions & Granular URL */}
          <div className="flex flex-wrap items-center justify-between gap-3 pt-2.5 border-t border-slate-800/80">
            <div className="flex items-center gap-3">
              <button
                type="button"
                disabled={busy || stale}
                onClick={handleAccept}
                className="px-4 py-2 rounded-xl bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500 text-white text-xs font-bold shadow-lg shadow-emerald-900/30 transition-all flex items-center gap-1.5 disabled:opacity-50 disabled:cursor-not-allowed"
                data-testid="accept-pending-worker"
              >
                <CheckCircle2 size={13} />
                <span>{busy ? 'Accepting…' : 'Accept worker'}</span>
              </button>

              <a
                href={detailHref}
                onClick={(e) => {
                  if (onOpenDetail) {
                    e.preventDefault()
                    onOpenDetail(worker.id)
                  }
                }}
                className="flex items-center gap-1 text-xs font-medium text-blue-400 hover:text-blue-300 hover:underline transition-colors"
                data-testid="pending-worker-detail-link"
              >
                <span>Open worker detail</span>
                <ExternalLink size={12} />
              </a>
            </div>

            {stale && (
              <span role="alert" className="text-xs text-amber-400 font-medium">
                Worker definition is refreshing. Acceptance is blocked on stale revisions.
              </span>
            )}
            {activeError && (
              <span role="alert" className="text-xs text-red-400 font-medium">
                {activeError}
              </span>
            )}
          </div>
        </div>
      )}
    </article>
  )
}
