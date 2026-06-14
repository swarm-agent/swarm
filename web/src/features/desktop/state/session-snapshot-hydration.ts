import type { DesktopSessionRecord } from '../types/realtime'

export function sessionRequiresSnapshotHydration(session: DesktopSessionRecord, eventType: string): boolean {
  if (!session.id.trim()) {
    return false
  }

  const normalizedEventType = eventType.trim().toLowerCase()
  if (normalizedEventType === 'session.created' || normalizedEventType === 'session.updated') {
    return false
  }

  return !session.workspacePath.trim()
    || !session.workspaceName.trim()
    || session.createdAt <= 0
}
