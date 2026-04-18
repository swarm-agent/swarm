import type { DesktopSessionRecord } from '../types/realtime'

export function sessionRequiresSnapshotHydration(session: DesktopSessionRecord, eventType: string): boolean {
  if (!session.id.trim()) {
    return false
  }

  if (eventType.trim().toLowerCase() === 'session.created') {
    return false
  }

  return !session.workspacePath.trim()
    || !session.workspaceName.trim()
    || session.createdAt <= 0
}
