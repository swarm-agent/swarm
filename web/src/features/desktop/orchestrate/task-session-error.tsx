import { redactIntegrationDiagnostic } from './integration-recovery'
import type { RunningTask } from './orchestrate-types'

/** Presentation only: durable current-attempt evidence never dispatches recovery. */
export function TaskSessionErrors({ task, onInvestigate }: {
  task: RunningTask
  onInvestigate: (sessionId: string) => void
}) {
  const errors = task.sessionSummary?.sessionStates?.filter(state => state.lastError && ['failed', 'blocked', 'paused'].includes(state.status)) || []
  return <>{errors.map(state => {
    const detail = redactIntegrationDiagnostic(state.lastError!).slice(0, 2000)
    return <div key={state.sessionId} role="alert" data-testid="task-session-error" className="rounded border border-rose-500/40 p-2 text-xs text-rose-200" onClick={event => event.stopPropagation()}>
      <p>Execution session: {detail.split('\n')[0].slice(0, 240)}</p>
      <details><summary>Error detail</summary><pre className="whitespace-pre-wrap break-words">{detail}</pre></details>
      <button type="button" onClick={() => onInvestigate(state.sessionId)}>Investigate session</button>
    </div>
  })}</>
}
