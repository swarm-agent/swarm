import type { MediaLibraryItem } from './types'

export interface VideoSection {
  source: MediaLibraryItem
  start: number
  end: number
}

// Only observed durations and explicit combined-extension provenance establish
// sections. Requested durations and chronological list position are not evidence.
export function videoSections(item: MediaLibraryItem, items: readonly MediaLibraryItem[]): VideoSection[] {
  const seen = new Set<string>()
  const visit = (current: MediaLibraryItem): VideoSection[] => {
    if (seen.has(current.id)) return []
    seen.add(current.id)
    const provenance = current.videoProvenance ?? current.artifact?.videoProvenance
    const duration = provenance?.observed_duration_ms
    if (current.kind !== 'video' || !duration || !Number.isFinite(duration) || duration <= 0) return []
    const end = duration / 1000
    if (provenance.operation !== 'extend') return [{ source: current, start: 0, end }]
    if (provenance.is_combined_output !== true) return []
    const link = provenance.source_link
    const parent = items.find(candidate => {
      if (link?.deliverable_id) return candidate.id === link.deliverable_id
      if (link?.media_ref_id) return candidate.id === link.media_ref_id
      const ref = candidate.artifact
      return Boolean(link?.session_id && link.collection_id && link.variant_id && link.event_seq &&
        ref?.sessionId === link.session_id && ref.collectionId === link.collection_id &&
        ref.artifactId === link.variant_id && ref.eventSeq === link.event_seq)
    })
    if (!parent) return []
    const previous = visit(parent)
    const start = previous.at(-1)?.end
    if (start === undefined || end <= start) return []
    return [...previous, { source: current, start, end }]
  }
  return visit(item)
}
