import type { MediaGenerationJob } from './media-generation'
import type { MediaLibraryItem } from './types'

export function isMediaGenerationPending(status: string): boolean {
  return ['requested', 'accepted', 'submitting', 'pending', 'queued', 'in_progress', 'running'].includes(status)
}

// Keep requests in the source's connected thread, including descendants and
// multi-output turns. Task output IDs, not timestamps/titles, own result matching.
export function getMediaIterationJobs(
  sourceId: string | undefined,
  items: readonly MediaLibraryItem[],
  jobs: readonly MediaGenerationJob[],
): MediaGenerationJob[] {
  if (!sourceId) return []
  const connected = new Set([sourceId])
  let changed = true
  while (changed) {
    const before = connected.size
    for (const item of items) {
      const parent = item.parentId || item.sourceMediaRef
      if (parent && (connected.has(item.id) || connected.has(parent))) {
        connected.add(item.id)
        connected.add(parent)
      }
    }
    for (const job of jobs) {
      if (connected.has(job.sourceId) || job.outputIds?.some((id) => connected.has(id))) {
        connected.add(job.sourceId)
        for (const id of job.outputIds || []) connected.add(id)
      }
    }
    changed = before !== connected.size
  }
  return jobs.filter((job) => connected.has(job.sourceId))
    .sort((a, b) => (a.createdAt || 0) - (b.createdAt || 0) || a.id.localeCompare(b.id))
}

export function getMediaIterationOutputs(
  job: MediaGenerationJob | undefined,
  items: readonly MediaLibraryItem[],
): MediaLibraryItem[] {
  const byId = new Map(items.map((item) => [item.id, item]))
  return (job?.outputIds || []).flatMap((id) => {
    const item = byId.get(id)
    return item && (item.source === 'independent-design' || item.directUrl) ? [item] : []
  })
}
