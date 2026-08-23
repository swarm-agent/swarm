import { loadStoredValue, saveStoredValue } from '../../../workspaces/launcher/services/workspace-storage'

export type VideoSessionViewPreference = 'studio' | 'chat'

const VIDEO_SESSION_VIEW_PREFERENCE_STORAGE_KEY = 'swarm.videoStudio.sessionView'

function videoSessionViewPreferenceStorageKey(sessionId: string): string {
  return `${VIDEO_SESSION_VIEW_PREFERENCE_STORAGE_KEY}:${sessionId.trim()}`
}

export function loadVideoSessionViewPreference(sessionId: string): VideoSessionViewPreference | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return null
  const value = loadStoredValue(videoSessionViewPreferenceStorageKey(normalizedSessionId))
  return value === 'studio' || value === 'chat' ? value : null
}

export function saveVideoSessionViewPreference(sessionId: string, preference: VideoSessionViewPreference): void {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return
  saveStoredValue(videoSessionViewPreferenceStorageKey(normalizedSessionId), preference)
}

export function preferredVideoSessionView(sessionId: string): VideoSessionViewPreference {
  return loadVideoSessionViewPreference(sessionId) ?? 'studio'
}
