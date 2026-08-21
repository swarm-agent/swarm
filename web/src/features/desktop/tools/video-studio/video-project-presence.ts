import { requestJson } from '../../../../app/api'
import type { DesktopV3CacheState } from '../../state/desktop-v3-cache-types'

interface VideoProjectListWire {
  projects?: unknown[]
}

export function videoProposalProjectionSequence(
  state: Pick<DesktopV3CacheState, 'eventsBySession'>,
  sessionId: string,
): number {
  const events = state.eventsBySession[sessionId] ?? []
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const event = events[index]
    if (event.event_type.startsWith('session.video_project.')) return event.seq
  }
  return 0
}

export function sessionVideoProjectPresenceKey(sessionId: string, sequence: number): readonly [string, string, number] {
  return ['session-video-project-presence', sessionId.trim(), Math.max(0, Math.floor(sequence))]
}

/** Canonical project presence check used to expose chat sessions in Video Studio. */
export async function sessionHasVideoProject(sessionId: string): Promise<boolean> {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return false
  try {
    const response = await requestJson<VideoProjectListWire>(
      `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/video/projects?limit=1`,
    )
    return Array.isArray(response.projects) && response.projects.length > 0
  } catch {
    return false
  }
}
