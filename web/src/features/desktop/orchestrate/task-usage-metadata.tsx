import type { ReactNode } from 'react'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { useUsagePage } from '../runtime/desktop-usage'
import type { UsagePage } from '../state/desktop-usage-state'
import type { RunningTask } from './orchestrate-types'
import { WorkerBudgetMetadata } from './scope-usage'

const compactTokens = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })

// Display the canonical receipt fields unchanged; never sum session snapshots or
// add cache/thinking to input/output (provider categories can overlap).
export function TaskTokenSplit({ page }: { page?: UsagePage }) {
  const usage = page?.recorded ? page.usage : undefined
  const fields = usage ? [
    ['I', 'Input', usage.input_tokens],
    ['O', 'Output', usage.output_tokens],
    ['CR', 'Cache read', usage.cache_read_tokens],
    ['CW', 'Cache write', usage.cache_write_tokens],
    ...(usage.thinking_tokens ? [['T', 'Thinking', usage.thinking_tokens] as const] : []),
  ] as const : []
  const status = page?.error ? 'Usage unavailable' : page?.loading ? 'Loading usage' : 'No recorded receipts'
  const description = usage
    ? `${fields.map(([, label, count]) => `${label}: ${Number(count).toLocaleString('en-US')}`).join('; ')} tokens${page?.stale || page?.error ? '; last known' : ''}${!usage.history_complete ? '; history incomplete' : ''}`
    : status
  return <span aria-label={description} title={description} data-testid="task-token-split"
    className="inline-flex shrink-0 items-center justify-end gap-1 whitespace-nowrap text-[10px] tabular-nums text-slate-400">
    {usage ? fields.map(([short, label, count]) => <span key={short} title={`${label}: ${Number(count).toLocaleString('en-US')} tokens`}>
      <span className="text-slate-500">{short}</span> {compactTokens.format(Number(count))}
    </span>) : <span>{page?.error ? 'Unavailable' : page?.loading ? 'Loading…' : 'No receipts'}</span>}
    {usage && (page?.stale || page?.error) && <span title="Last known recorded usage">*</span>}
  </span>
}

function RecordedTaskTokens({ accountScopeId, taskId, projectId }: { accountScopeId: string; taskId: string; projectId: string }) {
  const page = useUsagePage({ accountScopeId, scope: { kind: 'task', id: taskId, project_id: projectId } })
  return <TaskTokenSplit page={page} />
}

export function TaskUsageMetadata({ task, projectId }: { task: RunningTask; projectId?: string }) {
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  return <div className="ml-auto flex min-w-0 items-center justify-end overflow-x-auto text-right" aria-label="Task usage metadata" onClick={event => event.stopPropagation()}>
    {accountScopeId && projectId
      ? <RecordedTaskTokens accountScopeId={accountScopeId} taskId={task.id} projectId={projectId} />
      : <span className="whitespace-nowrap text-[10px] text-slate-400">Usage unavailable</span>}
  </div>
}

export function TaskWorkerBudgetMetadata({ task }: { task: RunningTask }) {
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  const workerId = task.workerId?.trim() || task.worker_id?.trim()
  return accountScopeId && workerId ? <WorkerBudgetMetadata accountScopeId={accountScopeId} workerId={workerId} /> : null
}

export function TaskUsageFooter({ task, projectId, children }: { task: RunningTask; projectId?: string; children: ReactNode }) {
  return <div className="swarm-task-usage-footer flex min-w-0 flex-nowrap items-center justify-between gap-2" data-testid="task-usage-footer">
    {children}
    <TaskUsageMetadata task={task} projectId={projectId} />
  </div>
}
