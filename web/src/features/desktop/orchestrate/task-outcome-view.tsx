import type { RunningTask } from './orchestrate-types'
import { taskOutcome } from './task-outcome'
import { useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { taskAttentionPermissions, taskAttentionSessionIds } from '../state/task-attention'

export function TaskOutcomeDetails({ task }: { task: RunningTask }) {
  const outcome = taskOutcome(task)
  return <section aria-label="Execution, verification and delivery" className="space-y-1 text-xs break-words">
    <p>{outcome.execution}</p>
    <p>{outcome.verification}</p>
    {outcome.delivery && <p>{outcome.delivery}</p>}
    {outcome.blocker && <div role="alert"><strong>{outcome.blocker.title}</strong><p>{outcome.blocker.message}</p></div>}
    {task.integration && <details><summary>Retained integration receipt</summary>
      <dl>{Object.entries({ State: task.integration.state, 'Source commit': task.integration.source_head,
        'Captured target': task.integration.target_branch || task.baseBranch, 'Target before': task.integration.previous_target_head,
        'Recorded recovery base': task.integration.recovery_base, 'Recovered delta commit': task.integration.recovered_head,
        'Target after': task.integration.resulting_target_head }).map(([key, value]) => <div key={key}><dt>{key}</dt><dd className="break-all">{value || 'Not recorded'}</dd></div>)}</dl>
    </details>}
  </section>
}

export function ProjectTaskAttention({ tasks, onOpen }: { tasks: RunningTask[]; onOpen: (task: RunningTask) => void }) {
  // Read canonical permission summaries even when the affected card is filtered
  // out. Navigation opens its existing permission controls; this adds no hydration.
  const permissionTasks = useDesktopV3CacheSelector(state => tasks.filter(task => {
    const ids = taskAttentionSessionIds(state, task)
    return ids.some(id => !state.tombstonesBySession[id] && (state.permissionSummaryBySessionId[id]?.pendingApprovalCount || 0) > 0)
      || taskAttentionPermissions(state, ids).length > 0
  }).map(task => task.id), (a, b) => a.length === b.length && a.every((id, index) => id === b[index]))
  const pending = new Set(permissionTasks)
  const attention = tasks.map(task => ({ task, reason: taskOutcome(task).attentionReason || (pending.has(task.id) ? 'Permission requested' : undefined) }))
    .filter(item => item.reason)
  return attention.length ? <section aria-label="Project task attention" className="p-3 space-y-2 text-xs">
    <strong>{attention.length} {attention.length === 1 ? 'task needs' : 'tasks need'} attention</strong>
    {attention.slice(0, 5).map(({ task, reason }) => <button type="button" key={task.id} className="block text-left break-words" onClick={() => onOpen(task)}>
      {task.title} · {reason}
    </button>)}
    {attention.length > 5 && <p>Open Tasks for all {attention.length} items.</p>}
  </section> : null
}
