import type { MediaLibraryItem } from '../tools/media-library/types'
import type { MediaDeliverable, ProjectSummary, RunningTask } from './orchestrate-types'

// One mapping from a media task's deliverable to the media viewer's item, shared
// by the Orchestrate studio and the chat's inline media card.

/**
 * Helper to safely parse any date/time representation into a valid Date object.
 * Prevents "RangeError: Invalid time value" on Date.prototype.toISOString() or toLocaleDateString().
 */
export function parseSafeDate(val: unknown): Date {
  if (val instanceof Date && !isNaN(val.getTime())) {
    return val
  }
  if (typeof val === 'number' && !isNaN(val) && isFinite(val) && val > 0) {
    const d = new Date(val)
    if (!isNaN(d.getTime())) return d
  }
  if (typeof val === 'string' && val.trim().length > 0) {
    const parsed = Date.parse(val)
    if (!isNaN(parsed)) {
      return new Date(parsed)
    }
  }
  return new Date()
}

export function safeIsoDayKey(date: Date): string {
  try {
    return date.toISOString().slice(0, 10)
  } catch {
    const now = new Date()
    return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
  }
}

export function deliverableToMediaItem(
  d: MediaDeliverable,
  parentTask?: RunningTask,
  project?: ProjectSummary | null,
): MediaLibraryItem {
  const isVideo = d.type === 'video'
  const isImage = d.type === 'image' || (d.previewUrl && d.previewUrl.startsWith('data:image'))
  const kind: 'image' | 'video' | 'audio' | 'animation' = isVideo ? 'video' : isImage ? 'image' : d.type === 'audio' ? 'audio' : 'image'
  const createdDate = parseSafeDate(d.createdAt)
  const isJustNow = typeof d.createdAt === 'string' && d.createdAt.toLowerCase().includes('just now')
  const formattedDate = isJustNow ? 'Just now' : createdDate.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' })
  const formattedTime = createdDate.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const dayKey = safeIsoDayKey(createdDate)

    const prov = (d as any).videoProvenance || (d as any).video_provenance || null
    const model = prov?.model || (d as any).model || undefined
    const aspectRatio = prov?.aspect_ratio || (d as any).aspectRatio || (d as any).aspect_ratio || d.videoAspect || undefined // Deliverable actual aspect ratio only; parentTask?.aspectRatio is legacy requested settings
    const resolution = prov?.resolution || (d as any).resolution || undefined // Deliverable actual resolution only; parentTask?.resolution is legacy requested settings
    let durationSeconds: number | undefined = undefined
    if (typeof (d as any).durationSeconds === 'number' && (d as any).durationSeconds > 0) {
      durationSeconds = (d as any).durationSeconds
    } else if (typeof (d as any).duration_seconds === 'number' && (d as any).duration_seconds > 0) {
      durationSeconds = (d as any).duration_seconds
    } else if (typeof prov?.duration_seconds === 'number' && prov.duration_seconds > 0) {
      durationSeconds = prov.duration_seconds
    } // Deliverable actual durationSeconds only; parentTask?.durationSeconds is legacy requested settings

    const durationMs = prov?.observed_duration_ms && prov.observed_duration_ms > 0
      ? prov.observed_duration_ms
      : (typeof (d as any).durationMs === 'number' && (d as any).durationMs > 0
      ? (d as any).durationMs
      : (durationSeconds ? durationSeconds * 1000 : undefined))

    return {
    id: d.id,
    title: d.title,
    filename: `${d.title.replace(/[^a-zA-Z0-9_-]/g, '_')}.${kind === 'image' ? 'png' : kind === 'audio' ? 'wav' : 'mp4'}`,
    mediaType: kind === 'image' ? 'image/png' : kind === 'audio' ? 'audio/wav' : 'video/mp4',
    kind,
    createdAt: createdDate.getTime(),
    formattedDate,
    formattedTime,
    dayKey,
    dayLabel: formattedDate,
    sessionId: parentTask?.sessionId || project?.primarySessionId || '',
    sessionTitle: parentTask?.title || project?.name || 'Orchestrate Studio',
    workspacePath: parentTask?.workspacePath || project?.repoPath || '',
    workspaceName: project?.name || 'Project Canvas',
    iterationGroupId: parentTask?.id,
    iterationGroupTitle: parentTask?.title,
    dimensions: d.videoAspect || aspectRatio || undefined,
    durationMs,
    directUrl: d.mediaUrl || d.previewUrl || '',
    parentId: d.parentDeliverableId || (d as any).parent_deliverable_id,
    sourceMediaRef: d.sourceMediaRef || (d as any).source_media_ref,
    model,
    aspectRatio,
    resolution,
    durationSeconds,
    videoProvenance: prov,
    legacyRequestedSettings: parentTask ? {
      model: parentTask.model,
      aspectRatio: parentTask.aspectRatio,
      resolution: parentTask.resolution,
      durationSeconds: parentTask.durationSeconds,
    } : undefined,
    artifact: {
      artifactId: d.id,
      sessionId: parentTask?.sessionId || '',
      label: d.title,
      filename: d.title,
      kind,
      mediaType: kind === 'image' ? 'image/png' : kind === 'audio' ? 'audio/wav' : 'video/mp4',
      description: d.prompt || d.title,
      model,
      aspectRatio,
      resolution,
      durationSeconds,
      videoProvenance: prov,
      createdAt: createdDate.getTime(),
      updatedAt: createdDate.getTime(),
    } as any,
  }
}
