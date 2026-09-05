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
