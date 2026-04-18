import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'

const ACTIVE_SESSION_STORAGE_KEY = 'swarm.web.desktop.activeSessionId.v1'
const ACTIVE_WORKSPACE_STORAGE_KEY = 'swarm.web.desktop.activeWorkspacePath.v1'

function normalizeStoredValue(value: string | null): string | null {
  const normalized = value?.trim() ?? ''
  return normalized === '' ? null : normalized
}

export function loadDesktopActiveSessionId(): string | null {
  return normalizeStoredValue(loadStoredValue(ACTIVE_SESSION_STORAGE_KEY))
}

export function saveDesktopActiveSessionId(sessionId: string | null): void {
  saveStoredValue(ACTIVE_SESSION_STORAGE_KEY, normalizeStoredValue(sessionId))
}

export function loadDesktopActiveWorkspacePath(): string | null {
  return normalizeStoredValue(loadStoredValue(ACTIVE_WORKSPACE_STORAGE_KEY))
}

export function saveDesktopActiveWorkspacePath(workspacePath: string | null): void {
  saveStoredValue(ACTIVE_WORKSPACE_STORAGE_KEY, normalizeStoredValue(workspacePath))
}
