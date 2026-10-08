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
        'Recorded recovery base': task.integration.recovery_base, 'Recovered delta commit': task.integration.recovered_head,
        'Target after': task.integration.resulting_target_head }).map(([key, value]) => <div key={key}><dt>{key}</dt><dd className="break-all">{value || 'Not recorded'}</dd></div>)}</dl>
    </details>}
  </section>
}
