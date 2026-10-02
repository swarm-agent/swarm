import type { MediaGenerationJob } from './media-generation'

// Preserve the submitted request identity across the optimistic -> durable
// transition. Source IDs may be canonicalized by the backend independently.
export function mediaJobIdentity(taskId: string, sourceId: string, localJobs: readonly MediaGenerationJob[]) {
  const local = localJobs.find(job => job.taskId === taskId)
  return { id: local?.id ?? taskId, sourceId: local?.sourceId ?? sourceId }
}
