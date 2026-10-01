import type { RunningTask } from './orchestrate-types'
import { redactIntegrationDiagnostic } from './integration-recovery'

// Projection only: durable task/attempt/program receipts own these facts. Neither
// job assembly, a checklist nor prose mentioning tests proves validation/delivery.
export function taskOutcome(task: RunningTask) {
  const attempt = task.attempts?.find(item => item.id === task.activeAttemptId && item.session_id === task.sessionId)
  const program = task.taskProgramStatus || task.task_program_status
  const currentProgram = program && (!program.parent_session_id || program.parent_session_id === task.sessionId)
    && (!program.reservation_run_id || !task.currentRunId || program.reservation_run_id === task.currentRunId) ? program : undefined
  const receipt = task.integration
  const currentReceipt = receipt && (!receipt.session_id || receipt.session_id === task.sessionId)
    && (!receipt.attempt_id || !task.activeAttemptId || receipt.attempt_id === task.activeAttemptId) ? receipt : undefined
  const integrationFailed = Boolean(currentReceipt && ['failed', 'conflict'].includes(currentReceipt.state))
  const runStatus = task.currentRunStatus || attempt?.status
  const ownerError = task.sessionSummary?.sessionStates.find(session => session.sessionId === task.sessionId)?.lastError
  const running = runStatus ? ['running', 'in_progress'].includes(runStatus) : ['running', 'in_progress'].includes(task.status)
  const interrupted = ['cancelled', 'canceled', 'interrupted', 'expired'].includes(runStatus || '')
  const failed = !running && (interrupted || runStatus === 'failed' || task.status === 'failed')
  const blocked = !running && (task.status === 'blocked' || runStatus === 'dispatch_blocked')
  const programBlocker = currentProgram?.blocker || currentProgram?.jobs?.find(job => ['failed', 'conflict', 'blocked'].includes(job.state))?.blocker
  const launchIncomplete = Boolean(attempt?.launch_state && attempt.launch_state !== 'launched')
  const repairSessionId = attempt?.recovery && attempt.launch_state === 'launched' ? attempt.session_id : undefined
  const blocker = integrationFailed
    ? { title: currentReceipt?.state === 'conflict' ? 'Integration conflict' : 'Integration failed', message: currentReceipt?.error || 'Promotion did not complete. Inspect the retained receipt before retrying.' }
    : launchIncomplete ? { title: 'Follow-up launch incomplete', message: attempt?.last_error || 'Retry the retained request; do not create another attempt.' }
    : failed || blocked ? { title: interrupted ? 'Execution interrupted' : blocked ? 'Execution blocked' : 'Execution failed', message: ownerError || task.lastError || attempt?.last_error || programBlocker?.message || 'Open the execution session and review the incomplete work before continuing.' }
    : !running && programBlocker ? { title: 'Program blocked', message: programBlocker.message }
    : undefined
  const code = Boolean(task.worktreeBranch || task.agentType === 'coder' || currentProgram?.definition?.jobs?.some(job => job.agent_type === 'coder'))
  const assembled = currentProgram?.state === 'completed'
  const delivered = task.isIntegrated === true && !integrationFailed && task.gitStatus === 'clean' && !task.isDirty && !(task.unintegratedCommits && task.unintegratedCommits > 0)
  return {
    blocker: blocker ? { ...blocker, message: redactIntegrationDiagnostic(blocker.message) } : undefined,
    integrationFailed, launchIncomplete, repairSessionId,
    execution: running ? 'Execution running' : interrupted ? 'Execution interrupted' : failed ? 'Execution failed' : blocked ? 'Execution blocked'
      : assembled ? 'Program assembled' : task.status === 'completed' ? 'Execution complete' : task.status.replace(/_/g, ' '),
    verification: failed || blocked ? 'Verification incomplete — review retained results' : 'Verification not established by task status',
    delivery: code ? delivered ? `Integrated into ${task.baseBranch || 'captured target'}`
      : `Delivery to ${task.baseBranch || 'captured target'} not verified` : undefined,
    needsAttention: Boolean(blocker) || (code && (assembled || task.status === 'completed') && !delivered),
  }
}
