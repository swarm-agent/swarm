import { projectTaskFollowupPayload } from '../runtime/project-task-followup'
import { acquireTaskMutation, taskIntegrationKey, taskIntegrationOperations, taskIntegrationPhase } from './task-integration-operation'
import type { RunningTask } from './orchestrate-types'

export type TaskReopenOutcome = { ok: true } | { ok: false; error: string }
export type TaskReopenOperation = { pending: boolean; error?: string; draft?: string }
export const taskReopenKey = (projectId: string, taskId: string) => JSON.stringify([projectId, taskId])
const ready: TaskReopenOperation = Object.freeze({ pending: false })

// Interaction state only; returned durable task authority is applied by the caller.
// Retained outside React to guard multiple cards and remounts synchronously.
export function createTaskReopenController() {
  let snapshot: ReadonlyMap<string, TaskReopenOperation> = new Map()
  const listeners = new Set<() => void>()
  const publish = (key: string, operation: TaskReopenOperation) => {
    snapshot = new Map(snapshot).set(key, operation)
    listeners.forEach(listener => listener())
  }
  return {
    get: (key: string) => snapshot.get(key) || ready,
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener) } },
    async run(projectId: string, taskId: string, task: RunningTask | undefined, feedback: string | undefined,
      mutate: (body: Awaited<ReturnType<typeof projectTaskFollowupPayload>>) => Promise<{ status: string; task?: any }>,
      apply: (task: any) => void): Promise<TaskReopenOutcome> {
      const key = taskReopenKey(projectId, taskId)
      if (snapshot.get(key)?.pending) return { ok: false, error: 'Reopen is already in progress. Wait for its result.' }
      const fail = (error: string): TaskReopenOutcome => { publish(key, { pending: false, error, draft: feedback ?? '' }); return { ok: false, error } }
      if (!task) return fail('Task authority is unavailable. Refresh the project and retry.')
      if (taskIntegrationPhase(task, taskIntegrationOperations.get(taskIntegrationKey(projectId, task))) === 'pending') {
        return fail('Integration is in progress. Wait for its result before reopening this task.')
      }
      const release = acquireTaskMutation(projectId, taskId)
      if (!release) return { ok: false, error: 'A task operation is already in progress. Wait for its result.' }
      publish(key, { pending: true, draft: feedback ?? '' })
      try {
        const request = feedback?.trim() ? feedback : 'Continue this task and address remaining requirements.'
        const active = task.attempts?.find(attempt => attempt.id === task.activeAttemptId && Boolean(attempt.launch_state) && attempt.launch_state !== 'launched')
        // Incomplete durable attempts must retain identity, revision, bytes AND repair.
        if (active && active.request !== request) throw new Error('An incomplete follow-up must be retried with its original instructions. Use Retry incomplete follow-up before submitting new instructions.')
        const body = active
          ? { client_request_id: active.client_request_id ?? '', revision: active.request_revision ?? 0, feedback: active.request ?? '', repair: Boolean(active.recovery) }
          : await projectTaskFollowupPayload(projectId, taskId, task.revision ?? 0, request)
        if (!body.client_request_id || !Number.isInteger(body.revision) || body.revision < 1) throw new Error('Missing retained request authority; refresh the project and retry.')
        const result = await mutate(body)
        if (result.status !== 'reopened' || result.task?.id !== taskId || !(result.task.session_id || result.task.sessionId) || !Number.isInteger(result.task.revision) || result.task.revision < 1) {
          throw new Error('Reopen returned no confirmed task authority. Refresh the project and retry the same instructions.')
        }
        apply(result.task)
        taskIntegrationOperations.reopened(taskIntegrationKey(projectId, task))
        publish(key, ready)
        return { ok: true }
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        return fail(`${message} Reopen was not confirmed; your instructions are retained. Resolve the reported problem, refresh if the revision is stale, then retry.`)
      } finally { release() }
    },
  }
}
export const taskReopenOperations = createTaskReopenController()
