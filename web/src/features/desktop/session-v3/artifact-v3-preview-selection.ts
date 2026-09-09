import type { DesktopV3NativeArtifactPart } from './artifact-v3-api'

export const artifactV3SelectionProtocol = 'swarm.artifact/v3'

/** Preview messages are untrusted intent, never an edit or head mutation. */
export function nativeArtifactPreviewSelectionEvent(
  event: MessageEvent,
  source: Window | null | undefined,
  revisionRef: string,
  parts: readonly DesktopV3NativeArtifactPart[],
): { type: 'selection-ready' } | { type: 'toggle-part'; partId: string } | null {
  if (!source || event.source !== source || event.origin !== 'null' || !revisionRef) return null
  const message: unknown = event.data
  if (!message || typeof message !== 'object' || Array.isArray(message)) return null
  const value = message as Record<string, unknown>
  if (value.protocol !== artifactV3SelectionProtocol || value.revision_ref !== revisionRef) return null
  if (value.type === 'selection-ready') return { type: 'selection-ready' }
  if (value.type !== 'toggle-part' || typeof value.part_id !== 'string') return null
  if (!parts.some((part) => part.id === value.part_id && part.locator.kind === 'selector')) return null
  return { type: 'toggle-part', partId: value.part_id }
}

export function toggleNativeArtifactPart(ids: readonly string[], partId: string): string[] {
  return ids.includes(partId) ? ids.filter((id) => id !== partId) : [...ids, partId]
}

export interface NativeArtifactPlaybackState {
  commandId: number
  durationMs: number
  timeMs: number
  playing: boolean
  error: string
}

/** Exact iframe/revision and command sequence fence asynchronous preview replies. */
export function nativeArtifactPlaybackEvent(event: MessageEvent, source: Window | null | undefined, revision: string, commandId: number): NativeArtifactPlaybackState | 'error' | null {
  if (!source || event.source !== source || event.origin !== 'null' || !revision) return null
  const m = event.data
  if (!m || typeof m !== 'object' || Array.isArray(m) || m.protocol !== artifactV3SelectionProtocol || m.revision_ref !== revision) return null
  if (m.type === 'playback-error') return 'error'
  if (m.type !== 'playback-state' || !Number.isSafeInteger(m.command_id) || m.command_id !== commandId) return null
  if (!Number.isSafeInteger(m.duration_ms) || m.duration_ms < 100 || m.duration_ms > 36000000 || !Number.isSafeInteger(m.time_ms) || m.time_ms < 0 || m.time_ms > m.duration_ms || typeof m.playing !== 'boolean' || typeof m.error !== 'string' || m.error.length > 256) return null
  return { commandId: m.command_id, durationMs: m.duration_ms, timeMs: m.time_ms, playing: m.playing, error: m.error }
}
