export interface TaskSessionCandidate {
  id?: string
  sessionId?: string
  status?: string
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
    if (t.sessionId && (t.status === 'running' || t.status === 'in_progress' || t.id === selectedTaskId)) {
      set.add(t.sessionId)
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
