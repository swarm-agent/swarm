import type { DesktopSessionRecord } from '../types/realtime'

function uniqueSessionIDs(sessions: readonly DesktopSessionRecord[]): string[] {
  const seen = new Set<string>()
  const ids: string[] = []
  for (const session of sessions) {
    const id = session.id.trim()
    if (!id || seen.has(id)) {
      continue
    }
    seen.add(id)
    ids.push(id)
  }
  return ids
}

/**
 * Preserve established sidebar order for sessions that are already visible.
 *
 * Live events and overview refreshes can legitimately update row content without
 * representing a real list insertion/removal. Keeping existing ids in their
 * previous relative order avoids the "jump to first, then reset" layout shift
 * while still letting removals compact and new sessions enter at the position
 * reported by the incoming list.
 */
export function reconcileSidebarSessionOrder(
  previousOrder: readonly string[],
  incomingSessions: readonly DesktopSessionRecord[],
): string[] {
  const incomingIDs = uniqueSessionIDs(incomingSessions)
  if (previousOrder.length === 0) {
    return incomingIDs
  }

  const incomingIDSet = new Set(incomingIDs)
  const previousIDSet = new Set(previousOrder.map((id) => id.trim()).filter(Boolean))
  const nextOrder = previousOrder.filter((id) => incomingIDSet.has(id))

  for (let incomingIndex = 0; incomingIndex < incomingIDs.length; incomingIndex += 1) {
    const id = incomingIDs[incomingIndex]
    if (previousIDSet.has(id) || nextOrder.includes(id)) {
      continue
    }

    let insertIndex = -1
    for (let cursor = incomingIndex - 1; cursor >= 0; cursor -= 1) {
      const precedingIndex = nextOrder.indexOf(incomingIDs[cursor])
      if (precedingIndex >= 0) {
        insertIndex = precedingIndex + 1
        break
      }
    }

    if (insertIndex < 0) {
      for (let cursor = incomingIndex + 1; cursor < incomingIDs.length; cursor += 1) {
        const followingIndex = nextOrder.indexOf(incomingIDs[cursor])
        if (followingIndex >= 0) {
          insertIndex = followingIndex
          break
        }
      }
    }

    if (insertIndex < 0 || insertIndex > nextOrder.length) {
      insertIndex = nextOrder.length
    }
    nextOrder.splice(insertIndex, 0, id)
  }

  return nextOrder
}

export function orderSidebarSessions(
  sessions: readonly DesktopSessionRecord[],
  previousOrder: readonly string[],
): { order: string[]; sessions: DesktopSessionRecord[] } {
  const order = reconcileSidebarSessionOrder(previousOrder, sessions)
  const sessionByID = new Map<string, DesktopSessionRecord>()
  for (const session of sessions) {
    const id = session.id.trim()
    if (id && !sessionByID.has(id)) {
      sessionByID.set(id, session)
    }
  }
  return {
    order,
    sessions: order.map((id) => sessionByID.get(id)).filter((session): session is DesktopSessionRecord => Boolean(session)),
  }
}
