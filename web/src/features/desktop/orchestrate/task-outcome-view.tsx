import type { RunningTask } from './orchestrate-types'
import { taskOutcome } from './task-outcome'

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
        'Target after': task.integration.resulting_target_head }).map(([key, value]) => <div key={key}><dt>{key}</dt><dd className="break-all">{value || 'Not recorded'}</dd></div>)}</dl>
    </details>}
  </section>
}

export function ProjectTaskAttention({ tasks, onOpen }: { tasks: RunningTask[]; onOpen: (task: RunningTask) => void }) {
  const attention = tasks.filter(task => taskOutcome(task).needsAttention)
  return attention.length ? <section aria-label="Project task attention" className="p-3 space-y-2 text-xs">
    <strong>{attention.length} {attention.length === 1 ? 'task needs' : 'tasks need'} attention</strong>
    {attention.slice(0, 5).map(task => <button type="button" key={task.id} className="block text-left break-words" onClick={() => onOpen(task)}>
      {task.title} · {taskOutcome(task).blocker?.title || 'Delivery not verified'}
    </button>)}
    {attention.length > 5 && <p>Open Tasks for all {attention.length} items.</p>}
  </section> : null
}
