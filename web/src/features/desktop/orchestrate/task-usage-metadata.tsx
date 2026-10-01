import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import type { RunningTask } from './orchestrate-types'
import { InlineUsage, WorkerBudgetMetadata } from './scope-usage'

export function TaskUsageMetadata({ task, projectId }: { task: RunningTask; projectId?: string }) {
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  const workerId = task.workerId?.trim() || task.worker_id?.trim()
  if (!accountScopeId) return <span className="text-xs">Usage unavailable · reconnect</span>
  return <div className="ml-auto flex min-w-0 max-w-full flex-1 basis-60 flex-wrap justify-end gap-x-3 gap-y-1 text-right" aria-label="Task usage metadata" onClick={event => event.stopPropagation()}>
    {workerId && <WorkerBudgetMetadata accountScopeId={accountScopeId} workerId={workerId} />}
    {projectId ? <InlineUsage input={{ accountScopeId, scope: { kind: 'task', id: task.id, project_id: projectId } }} /> : <span className="text-xs">Task usage unavailable</span>}
  </div>
}
