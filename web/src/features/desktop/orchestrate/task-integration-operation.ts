import { integrationFailure, type IntegrationFailure } from './integration-recovery'
import { taskOutcome } from './task-outcome'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

export interface TaskIntegrationResult {
  status: string
  task?: { id: string; session_id: string; is_integrated: boolean }
}

export type TaskIntegrationOperation =
  | { phase: 'ready' }
  | { phase: 'pending' }
  | { phase: 'success'; refreshError?: string }
  | { phase: 'error'; failure: IntegrationFailure; failureId?: number }

// Canonical receipts also describe promotions initiated outside this card.
export function taskIntegrationPhase(task: RunningTask, local?: TaskIntegrationOperation): TaskIntegrationOperation['phase'] {
  if (task.isIntegrated && task.status === 'completed') return 'success'
  if (task.integration?.state === 'in_progress') return 'pending'
  if (local?.phase === 'pending') return 'pending'
  if (taskOutcome(task).integrationFailed) return 'error'
  return local?.phase || 'ready'
}

const ready: TaskIntegrationOperation = Object.freeze({ phase: 'ready' })

// Shared interaction lock: task identity cannot change mid-flight with its lineage.
const taskMutationFlights = new Set<string>()
export function acquireTaskMutation(projectId: string, taskId: string): (() => void) | undefined {
  const key = JSON.stringify([projectId, taskId])
  if (taskMutationFlights.has(key)) return
  taskMutationFlights.add(key)
  return () => { taskMutationFlights.delete(key) }
}

// Exact captured lineage, not the currently selected card or a mutable snapshot revision.
export function taskIntegrationKey(projectId: string, task: RunningTask): string {
  return JSON.stringify([projectId, task.id, task.sessionId, task.activeAttemptId, task.sourceWorkspaceId, task.sourceWorkspacePath, task.worktreeBranch, task.baseBranch])
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
    async run(project: ProjectSummary, task: RunningTask, mutate: () => Promise<TaskIntegrationResult>, refresh: () => unknown) {
      const key = taskIntegrationKey(project.id, task)
      const current = get(key)
      if (current.phase === 'pending' || current.phase === 'success' || task.integration?.state === 'in_progress' || (task.isIntegrated && task.status === 'completed')) return
      const release = acquireTaskMutation(project.id, task.id)
      if (!release) return
      // Lock and notify synchronously, before invoking the transport or yielding.
      dismissedFailures.delete(key)
      publish(key, { phase: 'pending' })
      const capturedTask = { ...task }
      try {
        if (!task.sessionId || !task.worktreeBranch || !task.baseBranch) {
          throw new Error('Captured Git lineage is unavailable. Refresh the project and retry.')
        }
        const result = await mutate()
        if ((result.status !== 'integrated' && result.status !== 'already_integrated') ||
            result.task?.id !== capturedTask.id || result.task.session_id !== capturedTask.sessionId || !result.task.is_integrated) {
          throw new Error('Integration returned no confirmed result for this task. Refresh Git details before retrying.')
        }
      } catch (error) {
        publish(key, { phase: 'error', failure: integrationFailure(project, capturedTask, error), failureId: ++failureId })
        release()
        return
      }
      release()
      // A successful mutation receipt is independent of later cache repair failure.
      const receipt: TaskIntegrationOperation = { phase: 'success' }
      publish(key, receipt)
      try {
        await refresh()
      } catch {
        if (get(key) !== receipt) return
        publish(key, { phase: 'success', refreshError: 'Integration completed, but refreshing the project failed. Refresh the project to update Git details.' })
      }
    },
  }
}

export const taskIntegrationOperations = createTaskIntegrationController()
