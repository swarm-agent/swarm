import type { DesktopV3CacheState } from '../state/desktop-v3-cache-types'
import { extractTaskSessionIds, taskExcludedSessionIds } from '../runtime/desktop-projects-membership'
import type { RunningTask, TaskSessionStateItem } from './orchestrate-types'

/** Discover delegated children from canonical metadata, never transcript contents.
 * The task's current parent/run and program generation delimit the cohort.
 */
export function taskWithCurrentSessions(task: RunningTask, state: DesktopV3CacheState): RunningTask {
  const parent = task.sessionId || task.planBinding?.sessionId || task.planBinding?.session_id
  const jobs = task.boardSummary ? task.boardSummary.program?.jobs : task.taskProgramStatus?.jobs
  const ids = new Set(extractTaskSessionIds(task))
  const historical = taskExcludedSessionIds(task)
  const currentJobs = new Set(jobs?.map(job => job.current_session_id || ('child_session_id' in job ? job.child_session_id : undefined)).filter(Boolean))
  for (const id of historical) if (!currentJobs.has(id)) ids.delete(id)
  const parentRun = parent ? state.sessionViewsById[parent]?.current_run_state?.run_id : undefined
  if (parent) for (const [id, record] of Object.entries(state.sessionsById)) {
    if (record.kind !== 'full') continue
    const metadata = record.session.metadata
    if (metadata?.parent_session_id !== parent || metadata.lineage_kind === 'session_deploy') continue
    if (metadata.lineage_kind !== 'delegated_subagent' && !metadata.requested_subagent && !metadata.subagent) continue
    if (!currentJobs.has(id) && parentRun && metadata.parent_run_id && metadata.parent_run_id !== parentRun) {
      ids.delete(id)
      continue
    }
    if (jobs && !currentJobs.has(id)) continue
    if (historical.has(id) && !currentJobs.has(id)) continue
    ids.add(id)
  }
  return { ...task, sessionIds: [...ids].sort() }
}

/** Presentation only: runtime aggregation owns statuses; queued is not working. */
export function taskCardSessions(task: RunningTask): TaskSessionStateItem[] {
  const parent = task.sessionId || task.planBinding?.sessionId || task.planBinding?.session_id
  const jobs = task.boardSummary ? task.boardSummary.program?.jobs : task.taskProgramStatus?.jobs
  const currentJobs = jobs ? new Set(jobs.map(job => job.current_session_id || ('child_session_id' in job ? job.child_session_id : undefined))) : null
  const unique = new Map<string, TaskSessionStateItem>()
  for (const session of task.sessionSummary?.sessionStates || []) {
    if (!session.sessionId || (currentJobs && !currentJobs.has(session.sessionId))) continue
    unique.set(session.sessionId, session.hydrated === false && session.status === 'running'
      ? { ...session, status: 'unknown' } : session)
  }
  if (unique.size > 1 || currentJobs) unique.delete(parent || '')
  return [...unique.values()]
}
