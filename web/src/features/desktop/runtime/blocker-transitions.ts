import type { DesktopV3SidebarRow } from '../state/desktop-v3-cache-selectors'

// UI notifications derive from durable plan snapshots, never permission or run errors.
// Keep the last observed checkpoint attempt across temporary hydration gaps so replay
// and reconnect cannot announce the same blocker twice. Initial hydration is silent.
export function createBlockerTransitionTracker() {
  const observed = new Map<string, { blocked: boolean; key: string }>()
  return (rows: DesktopV3SidebarRow[]): string[] => {
    const messages: string[] = []
    for (const row of rows) {
      if (!row.activePlan || !row.planExecution || row.tombstone) continue
      const execution = row.planExecution
      const checkpoint = row.activePlan.document?.checkpoints.find((item) => item.id === execution.activeCheckpointId)
      const key = `${execution.planId}:${execution.activeCheckpointId}:${checkpoint?.attemptId || ''}`
      const previous = observed.get(row.sessionId)
      const blocked = execution.blocked
      if (previous && blocked && (!previous.blocked || previous.key !== key)) {
        const reason = checkpoint?.waitingReason || checkpoint?.finalHandoff?.overview || 'An external dependency needs resolution.'
        const action = checkpoint?.recommendation?.action || 'Tell Swarm when the dependency is resolved.'
        messages.push(`Blocked: ${execution.title}. ${reason} Next action: ${action}`)
      }
      observed.set(row.sessionId, { blocked, key })
    }
    return messages
  }
}
