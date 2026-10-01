import type { WorkerLifecycleState, WorkerRun } from '../state/desktop-workers-api'

export function workerLifecycleLabel(state: WorkerLifecycleState): string {
  return { active: 'Enabled', idle: 'Idle', pending: 'Awaiting approval', paused: 'Paused', stopping: 'Stopping', archived: 'Archived', deleted: 'Deleted' }[state]
}
export const workerRunCategories = ['All', 'In progress', 'Deliverables', 'Succeeded', 'Failed', 'Cancelled'] as const
export type WorkerRunCategory = typeof workerRunCategories[number]
export function workerRunCategory(run: WorkerRun): WorkerRunCategory {
  if (run.status === 'running' || run.status === 'admitted') return 'In progress'
  if (run.status === 'failed') return 'Failed'
  if (run.status === 'cancelled') return 'Cancelled'
  return run.deliverables?.length ? 'Deliverables' : 'Succeeded'
}
export function workerSessionHref(sessionId: string, workspaceSlug?: string): string {
  return workspaceSlug ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(sessionId)}` : `/${encodeURIComponent(sessionId)}`
}
export function workerDetailHref(workerId: string, workspaceSlug?: string): string {
  return `${workspaceSlug ? `/${encodeURIComponent(workspaceSlug)}` : ''}/workers/${encodeURIComponent(workerId)}`
}
