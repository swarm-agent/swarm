import { integrationFailure, type IntegrationFailure } from './integration-recovery'
import { taskDelivery, taskOutcome } from './task-outcome'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

export interface TaskIntegrationResult {
  status: string
  task?: { id: string; session_id: string; is_integrated: boolean; active_attempt_id?: string; integration?: { state: string; source_head?: string; recovery_base?: string; recovered_head?: string; resulting_target_head?: string } }
}

export type TaskIntegrationOperation =
  | { phase: 'ready' }
  | { phase: 'pending' }
  | { phase: 'success'; refreshError?: string }
  | { phase: 'error'; failure: IntegrationFailure; failureId?: number }

// Canonical receipts also describe promotions initiated outside this card.
export function taskIntegrationPhase(task: RunningTask, local?: TaskIntegrationOperation): TaskIntegrationOperation['phase'] {
  const receipt = task.integration
  const currentReceipt = receipt && (!receipt.session_id || receipt.session_id === task.sessionId) &&
    (!receipt.attempt_id || !task.activeAttemptId || receipt.attempt_id === task.activeAttemptId)
  // Recovery retries are backend-serialized and may reconcile a retained
  // operation after restart. Only the live local request holds its UI lock.
  if (currentReceipt && receipt.state === 'in_progress' && !receipt.recovery_base) return 'pending'
  if (local?.phase === 'pending') return 'pending'
  if (taskOutcome(task).integrationFailed) return 'error'
  const delivery = taskDelivery(task)
  if (((delivery?.integrated || delivery?.recovered) ?? task.isIntegrated) && task.status === 'completed') return 'success'
  if (delivery && !delivery.integrated && !delivery.recovered && local?.phase === 'success') return 'ready'
  return local?.phase || 'ready'
}

// One request contract for the existing card action; recovery is never a bulk
// integration fallback and never accepts a UI-selected repository/base.
export function taskIntegrationRequest(projectId: string, task: RunningTask) {
  const recovery = taskDelivery(task)?.recoverable
  return {
    url: `/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(task.id)}/${recovery ? 'recover-integrate' : 'integrate'}`,
    body: { session_id: task.sessionId, source_branch: task.worktreeBranch, target_branch: task.baseBranch,
      revision: task.revision, attempt_id: task.activeAttemptId, source_head: task.deliveryAssessment?.source_oid, target_head: task.deliveryAssessment?.target_oid },
  }
}

const ready: TaskIntegrationOperation = Object.freeze({ phase: 'ready' })

// Shared interaction lock: task identity cannot change mid-flight with its lineage.
const taskMutationFlights = new Set<string>()
export const taskMutationPending = (projectId: string, taskId: string) => taskMutationFlights.has(JSON.stringify([projectId, taskId]))
// One conservative integration lane across cards and batches. Repository aliases
// cannot accidentally allow two writes to the same captured checkout.
let batchOwner: symbol | undefined
let integrationFlights = 0
export const integrationLanePending = () => Boolean(batchOwner || integrationFlights)
export function acquireIntegrationBatch() {
  if (batchOwner || integrationFlights) return
  const token = Symbol('integration batch')
  batchOwner = token
  return { token, release: () => { if (batchOwner === token) batchOwner = undefined } }
}
export type TaskIntegrationOutcome =
  | { status: 'integrated' | 'already_integrated' }
  | { status: 'skipped'; reason: string }
  | { status: 'failed'; reason: string }
export function acquireTaskMutation(projectId: string, taskId: string): (() => void) | undefined {
  const key = JSON.stringify([projectId, taskId])
  if (taskMutationFlights.has(key)) return
  taskMutationFlights.add(key)
  return () => { taskMutationFlights.delete(key) }
}

// Exact captured lineage, not the currently selected card or a mutable snapshot revision.
export function taskIntegrationKey(projectId: string, task: RunningTask): string {
  return JSON.stringify([projectId, task.id, task.sessionId, task.activeAttemptId, task.sourceWorkspaceId, task.sourceWorkspacePath, task.sourceWorkspaceGeneration, task.baseCommit, task.worktreeBranch, task.baseBranch])
}

// Identify retained receipts, not unrelated task revisions or render-time object identity.
// Include every failure source so acknowledging a local error cannot reveal its fallback.
export function taskIntegrationFailureIdentity(task: RunningTask, operation: TaskIntegrationOperation): string {
  const repair = task.attempts?.find(attempt => attempt.id === task.activeAttemptId && attempt.launch_state === 'launch_failed' && attempt.recovery)
  const receipt = task.integration
  return JSON.stringify([
    repair ? [repair.id, repair.client_request_id, repair.request_revision, repair.last_error] : null,
    operation.phase === 'error' ? [operation.failureId, operation.failure.error] : null,
    receipt && ['failed', 'conflict'].includes(receipt.state)
      ? [receipt.operation_id, receipt.attempt_id, receipt.state, receipt.error, receipt.source_head, receipt.previous_target_head] : null,
  ])
}

// Interaction receipts only. Never changes task.isIntegrated or claims Git ancestry.
// Retained outside React so navigation/remount cannot unlock an in-flight mutation.
export function createTaskIntegrationController() {
  let snapshot: ReadonlyMap<string, TaskIntegrationOperation> = new Map()
  const listeners = new Set<() => void>()
  const dismissedFailures = new Map<string, string>()
  let failureId = 0
  const publish = (key: string, operation: TaskIntegrationOperation) => {
    snapshot = new Map(snapshot).set(key, operation)
    listeners.forEach(listener => listener())
  }
  const get = (key: string) => snapshot.get(key) || ready
  return {
    get,
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) {
      listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    // Only a confirmed explicit reopen starts another generation on the same lane.
    reopened(key: string) {
      if (get(key).phase !== 'pending') publish(key, ready)
    },
    isDismissed(key: string, identity: string) {
      return dismissedFailures.get(key) === identity
    },
    dismiss(key: string, identity?: string) {
      if (!identity || dismissedFailures.get(key) === identity) return
      dismissedFailures.set(key, identity)
      // Presentation-only: retain diagnostics and the retry phase, and notify React.
      publish(key, get(key))
    },
    async run(project: ProjectSummary, task: RunningTask, mutate: () => Promise<TaskIntegrationResult>, refresh: () => unknown, batchToken?: symbol): Promise<TaskIntegrationOutcome> {
      const key = taskIntegrationKey(project.id, task)
      const current = get(key)
<<<<<<< 8d23a56d39c43aceacde66e7f758ddf3fa07eff3
      const delivery = taskDelivery(task)
      if (delivery && !delivery.actionable && !delivery.recoverable) return
      const phase = taskIntegrationPhase(task, current)
      if (phase === 'pending' || phase === 'success') return
      const release = acquireTaskMutation(project.id, task.id)
      if (!release) return
=======
      if (current.phase === 'success' || (task.isIntegrated && task.status === 'completed')) return { status: 'already_integrated' }
      if ((batchOwner && batchOwner !== batchToken) || integrationFlights || current.phase === 'pending' || task.integration?.state === 'in_progress') return { status: 'skipped', reason: 'Another integration is pending' }
      const releaseMutation = acquireTaskMutation(project.id, task.id)
      if (!releaseMutation) return { status: 'skipped', reason: 'Another task mutation is pending' }
      integrationFlights++
      const release = () => { integrationFlights--; releaseMutation() }
      let status: 'integrated' | 'already_integrated' = 'integrated'
>>>>>>> f647ba3f00a64c4256510918533e49a420624d2b
      // Lock and notify synchronously, before invoking the transport or yielding.
      dismissedFailures.delete(key)
      publish(key, { phase: 'pending' })
      const capturedTask = { ...task }
      try {
        if (!task.sessionId || !task.worktreeBranch || !task.baseBranch) {
          throw new Error('Captured Git lineage is unavailable. Refresh the project and retry.')
        }
        const result = await mutate()
        const recovery = delivery?.recoverable
        const confirmed = recovery
          ? ['recovered', 'equivalent'].includes(result.status) && result.task?.integration?.state === result.status &&
            result.task.active_attempt_id === capturedTask.activeAttemptId &&
            result.task.integration.source_head === capturedTask.deliveryAssessment?.source_oid &&
            result.task.integration.recovery_base === capturedTask.baseCommit && Boolean(result.task.integration.resulting_target_head) &&
            (result.status === 'equivalent' || Boolean(result.task.integration.recovered_head))
          : ['integrated', 'already_integrated'].includes(result.status) && result.task?.is_integrated
        if (!confirmed || result.task?.id !== capturedTask.id || result.task.session_id !== capturedTask.sessionId) {
          throw new Error('Integration returned no confirmed result for this task. Refresh Git details before retrying.')
        }
        status = result.status as typeof status
      } catch (error) {
        const failure = integrationFailure(project, capturedTask, error)
        publish(key, { phase: 'error', failure, failureId: ++failureId })
        release()
<<<<<<< 8d23a56d39c43aceacde66e7f758ddf3fa07eff3
        // Conflict receipts and prepared repair evidence arrive on canonical task
        // events; also invalidate after a transport failure/lost response.
        try { await refresh() } catch { /* retain the original diagnostic */ }
        return
=======
        return { status: 'failed', reason: failure.error }
>>>>>>> f647ba3f00a64c4256510918533e49a420624d2b
      }
      release()
      // A successful mutation receipt is independent of later cache repair failure.
      const receipt: TaskIntegrationOperation = { phase: 'success' }
      publish(key, receipt)
      try {
        await refresh()
      } catch {
        if (get(key) !== receipt) return { status }
        publish(key, { phase: 'success', refreshError: 'Integration completed, but refreshing the project failed. Refresh the project to update Git details.' })
      }
      return { status }
    },
  }
}

export const taskIntegrationOperations = createTaskIntegrationController()
