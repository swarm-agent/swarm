export interface TaskSessionCandidate {
  id?: string
  sessionId?: string
  status?: string
  planBinding?: { sessionId?: string; session_id?: string }
  plan_binding?: { sessionId?: string; session_id?: string }
  taskProgram?: {
    jobs?: Array<{
      child_session_id?: string
      current_session_id?: string
      generation_history?: Array<{ session_id?: string }>
    }>
  }
  task_program?: {
    jobs?: Array<{
      child_session_id?: string
      current_session_id?: string
      generation_history?: Array<{ session_id?: string }>
    }>
  }
  taskProgramStatus?: {
    jobs?: Array<{
      child_session_id?: string
      current_session_id?: string
      generation_history?: Array<{ session_id?: string }>
    }>
  }
  task_program_status?: {
    jobs?: Array<{
      child_session_id?: string
      current_session_id?: string
      generation_history?: Array<{ session_id?: string }>
    }>
  }
  sessionIds?: string[]
  session_ids?: string[]
}

export function extractTaskSessionIds(task?: TaskSessionCandidate | null): string[] {
  if (!task) return []
  const set = new Set<string>()
  if (task.sessionId && typeof task.sessionId === 'string' && task.sessionId.trim()) {
    set.add(task.sessionId.trim())
  }
  const bindingSid =
    task.planBinding?.sessionId ||
    task.planBinding?.session_id ||
    task.plan_binding?.sessionId ||
    task.plan_binding?.session_id
  if (bindingSid && typeof bindingSid === 'string' && bindingSid.trim()) {
    set.add(bindingSid.trim())
  }

  const programSources = [
    task.taskProgramStatus?.jobs,
    task.task_program_status?.jobs,
    task.taskProgram?.jobs,
    task.task_program?.jobs,
  ]
  for (const jobs of programSources) {
    if (Array.isArray(jobs)) {
      for (const j of jobs) {
        // Current attempt supersedes historical sessions so old failures cannot poison state/counts.
        const currentSid =
          (j?.current_session_id && typeof j.current_session_id === 'string' && j.current_session_id.trim()) ||
          (j?.child_session_id && typeof j.child_session_id === 'string' && j.child_session_id.trim())
        if (currentSid) {
          set.add(currentSid)
        }
      }
    }
  }

  if (Array.isArray(task.sessionIds)) {
    for (const sid of task.sessionIds) {
      if (typeof sid === 'string' && sid.trim()) set.add(sid.trim())
    }
  }
  if (Array.isArray(task.session_ids)) {
    for (const sid of task.session_ids) {
      if (typeof sid === 'string' && sid.trim()) set.add(sid.trim())
    }
  }

  return Array.from(set).sort()
}

export interface SessionDemandLease {
  release: () => void
}

export interface RealtimeDemandController {
  acquireSessionDemand: (ownerKey: string, sessionId: string) => SessionDemandLease
}

export interface TaskSessionLeaseManagerDeps {
  getControllerReady?: () => Promise<RealtimeDemandController>
  hydrate?: (sessionId: string) => void | Promise<void>
  ownerKeyPrefix?: string
}

export function computeActiveTaskSessionIds(
  tasks: TaskSessionCandidate[],
  selectedTaskId?: string,
): string[] {
  const set = new Set<string>()
  for (const t of tasks) {
    const isSelected = Boolean(selectedTaskId && t.id === selectedTaskId)
    const isActive =
      t.status === 'running' ||
      t.status === 'in_progress' ||
      t.status === 'planning' ||
      t.status === 'pending_approval' ||
      t.status === 'needs_review' ||
      t.status === 'queued'
    if (isSelected || isActive) {
      for (const sid of extractTaskSessionIds(t)) {
        set.add(sid)
      }
    }
  }
  return Array.from(set).sort()
}

export function computeActiveTaskSessionIdsKey(
  tasks: TaskSessionCandidate[],
  selectedTaskId?: string,
): string {
  return computeActiveTaskSessionIds(tasks, selectedTaskId).join(',')
}

export class TaskSessionLeaseManager {
  readonly activeLeases = new Map<string, SessionDemandLease>()
  readonly hydratedSessions = new Set<string>()
  private currentDesiredSet = new Set<string>()
  private cancelPending?: () => void

  constructor(private readonly deps: TaskSessionLeaseManagerDeps = {}) {}

  reconcile(desiredSessionIds: Iterable<string>): { cancel: () => void } {
    const desiredSet = new Set(desiredSessionIds)
    this.currentDesiredSet = desiredSet

    // 1. Release leases for sessions no longer active
    for (const [sid, lease] of this.activeLeases.entries()) {
      if (!desiredSet.has(sid)) {
        try {
          lease.release()
        } catch {
          // ignore
        }
        this.activeLeases.delete(sid)
      }
    }

    // 2. Identify new sessions that need hydration and leases
    const newSessionIds: string[] = []
    for (const sid of desiredSet) {
      if (!this.activeLeases.has(sid)) {
        newSessionIds.push(sid)
      }
    }

    if (newSessionIds.length === 0) {
      return { cancel: () => {} }
    }

    // 3. Hydrate NEW sessions only
    for (const sid of newSessionIds) {
      if (!this.hydratedSessions.has(sid)) {
        this.hydratedSessions.add(sid)
        try {
          void this.deps.hydrate?.(sid)
        } catch {
          // ignore
        }
      }
    }

    // 4. Acquire realtime demand leases so live events stream for NEW sessions
    if (!this.deps.getControllerReady) {
      return { cancel: () => {} }
    }

    let cancelled = false
    this.cancelPending?.()
    this.cancelPending = () => {
      cancelled = true
    }

    const ownerKeyPrefix = this.deps.ownerKeyPrefix ?? 'orchestrate-task'

    void this.deps.getControllerReady()
      .then((controller) => {
        if (cancelled) return
        for (const sid of newSessionIds) {
          // Race check: still desired and not already leased
          if (this.currentDesiredSet.has(sid) && !this.activeLeases.has(sid)) {
            const ownerKey = `${ownerKeyPrefix}:${sid}`
            try {
              const lease = controller.acquireSessionDemand(ownerKey, sid)
              if (cancelled) {
                lease.release()
              } else {
                this.activeLeases.set(sid, lease)
              }
            } catch {
              // ignore
            }
          }
        }
      })
      .catch(() => undefined)

    return {
      cancel: () => {
        cancelled = true
      },
    }
  }

  cleanup(): void {
    this.cancelPending?.()
    this.cancelPending = undefined
    for (const lease of this.activeLeases.values()) {
      try {
        lease.release()
      } catch {
        // ignore
      }
    }
    this.activeLeases.clear()
    this.hydratedSessions.clear()
    this.currentDesiredSet.clear()
  }
}
