import type { ProjectSummary, RunningTask } from './orchestrate-types'
import { acquireIntegrationBatch, taskIntegrationKey, taskIntegrationOperations, taskIntegrationPhase, taskMutationPending, type TaskIntegrationOutcome } from './task-integration-operation'
import { taskDelivery, taskOutcome } from './task-outcome'
import { redactIntegrationDiagnostic } from './integration-recovery'

export const MAX_INTEGRATION_BATCH = 100
export function integrationSkipReason(projectId: string, task: RunningTask): string | undefined {
  const delivery = taskDelivery(task)
  if (delivery?.integrated) return 'Already integrated'
  if (delivery?.recovered) return delivery.summary
  if (delivery?.recoverable) return 'Recovery requires the individual Recover & integrate action'
  if (delivery && !delivery.actionable) return delivery.summary
  const phase = taskIntegrationPhase(task, taskIntegrationOperations.get(taskIntegrationKey(projectId, task)))
  if (phase === 'success' || (!delivery && task.isIntegrated)) return 'Already integrated'
  if (['image', 'video', 'sound', 'audio', 'plan'].includes(task.agentType) || !task.worktreeBranch) return 'No code Git lineage'
  if (['running', 'in_progress', 'pending', 'queued', 'planning', 'pending_approval'].includes(task.status) || ['running', 'in_progress', 'pending', 'queued'].includes(task.currentRunStatus || '')) return 'Execution or approval pending'
  if (taskOutcome(task).launchIncomplete) return 'Current attempt launch incomplete'
  if (!task.sessionId || !task.sourceWorkspacePath || !task.baseBranch) return 'Captured ownership or target unavailable'
  if (taskMutationPending(projectId, task.id) || phase === 'pending') return 'Another task mutation is pending'
}
export type BatchEntry = { id: string; title: string; status: TaskIntegrationOutcome['status'] | 'queued' | 'pending' | 'not_attempted'; reason?: string }
export type IntegrationBatch = { pending: boolean; entries: BatchEntry[]; refreshError?: string }

// Interaction results only; canonical project/task state still owns lineage.
// Survives navigation/remount without resurrecting or unlocking outstanding writes.
export function createTaskIntegrationBatchController() {
  let snapshot: ReadonlyMap<string, IntegrationBatch> = new Map()
  const listeners = new Set<() => void>()
  const publish = (id: string, batch: IntegrationBatch) => {
    snapshot = new Map(snapshot).set(id, { ...batch, entries: batch.entries.map(entry => ({ ...entry })) })
    listeners.forEach(listener => listener())
  }
  return {
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener) } },
    async run(project: ProjectSummary, selected: RunningTask[], current: (id: string) => RunningTask | undefined,
      integrate: (task: RunningTask, token: symbol) => Promise<TaskIntegrationOutcome>, refresh: () => unknown) {
      if (snapshot.get(project.id)?.pending) return
      const tasks = [...new Map(selected.map(task => [task.id, { ...task }])).values()].sort((a, b) => a.id.localeCompare(b.id))
      const batch: IntegrationBatch = { pending: false, entries: tasks.map(task => {
        const reason = integrationSkipReason(project.id, task)
        return { id: task.id, title: task.title, status: reason ? 'skipped' : 'queued', reason }
      }) }
      if (tasks.length > MAX_INTEGRATION_BATCH) {
        batch.entries.forEach(entry => { entry.status = 'not_attempted'; entry.reason = `Select at most ${MAX_INTEGRATION_BATCH} tasks` })
        publish(project.id, batch)
        return
      }
      if (!batch.entries.some(entry => entry.status === 'queued')) { publish(project.id, batch); return }
      const lease = acquireIntegrationBatch()
      if (!lease) {
        batch.entries.forEach(entry => { if (entry.status === 'queued') { entry.status = 'not_attempted'; entry.reason = 'Another integration is pending' } })
        publish(project.id, batch)
        return
      }
      batch.pending = true
      publish(project.id, batch)
      let stopped = false
      let attempted = false
      try {
        for (let index = 0; index < tasks.length; index++) {
          const task = tasks[index], entry = batch.entries[index]
          if (entry.status !== 'queued') continue
          const latest = current(task.id)
          if (stopped) {
            entry.status = 'not_attempted'; entry.reason = 'Batch stopped; inspect the failed integration before retrying'
          } else if (!latest || taskIntegrationKey(project.id, latest) !== taskIntegrationKey(project.id, task) || latest.deliveryAssessment?.source_oid !== task.deliveryAssessment?.source_oid) {
            entry.status = 'not_attempted'; entry.reason = 'Project, task or captured attempt changed; refresh and select again'
          } else {
            const reason = integrationSkipReason(project.id, latest)
            if (reason) { entry.status = 'skipped'; entry.reason = reason }
            else {
              entry.status = 'pending'; publish(project.id, batch)
              attempted = true
              try {
                const result = await integrate(latest, lease.token)
                entry.status = result.status
                entry.reason = 'reason' in result ? result.reason : undefined
                stopped = result.status === 'failed'
              } catch (error) {
                entry.status = 'failed'; entry.reason = redactIntegrationDiagnostic(String(error)); stopped = true
              }
            }
          }
          publish(project.id, batch)
        }
      } finally {
        batch.pending = false
        lease.release()
        publish(project.id, batch)
        if (attempted) {
          try { await refresh() }
          catch { batch.refreshError = 'Refreshing the project failed. Refresh before retrying; retained integration results are unchanged.'; publish(project.id, batch) }
        }
      }
    },
  }
}
export const taskIntegrationBatches = createTaskIntegrationBatchController()
