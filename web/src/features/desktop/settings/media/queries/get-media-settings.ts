import { requestJson } from '../../../../../app/api'

export interface MediaCatalogModelOption {
  id: string
  provider: string
  model: string
  display_name: string
  kind: 'image_generation' | 'video_understanding'
  ready: boolean
  reason?: string
  pricing?: unknown
}

export interface MediaSettingsCatalog {
  image_models: MediaCatalogModelOption[]
  transcription_models: MediaCatalogModelOption[]
  video_ready: boolean
  video_status: string
}

export interface SourceMediaDirectoriesResponse {
  ok: boolean
  source_media_directories: string[]
}

export async function getMediaSettingsCatalog(signal?: AbortSignal): Promise<MediaSettingsCatalog> {
  return requestJson<MediaSettingsCatalog>('/v1/media/settings/catalog', { signal })
}

export const sourceMediaDirectoriesQueryKey = (workspacePath: string) => ['source-media-directories', workspacePath] as const

export async function getSourceMediaDirectories(workspacePath: string, signal?: AbortSignal): Promise<string[]> {
  const query = new URLSearchParams({ workspace_path: workspacePath })
  const response = await requestJson<SourceMediaDirectoriesResponse>(`/v1/workspace/source-media/directories?${query.toString()}`, { signal })
  return Array.isArray(response.source_media_directories) ? response.source_media_directories : []
}

async function mutateSourceMediaDirectory(action: 'add' | 'remove', workspacePath: string, directoryPath: string): Promise<string[]> {
  const response = await requestJson<SourceMediaDirectoriesResponse>(`/v1/workspace/source-media/directories/${action}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, directory_path: directoryPath }),
  })
  return Array.isArray(response.source_media_directories) ? response.source_media_directories : []
}

export function addSourceMediaDirectory(workspacePath: string, directoryPath: string): Promise<string[]> {
  return mutateSourceMediaDirectory('add', workspacePath, directoryPath)
}

export function removeSourceMediaDirectory(workspacePath: string, directoryPath: string): Promise<string[]> {
  return mutateSourceMediaDirectory('remove', workspacePath, directoryPath)
}

export const VIDEO_FOCUS_NOTES_MAX_BYTES = 500

export function videoFocusNotesByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

export function truncateVideoFocusNotes(value: string, maxBytes = VIDEO_FOCUS_NOTES_MAX_BYTES): string {
  const encoder = new TextEncoder()
  let bytes = 0
  let result = ''
  for (const character of value) {
    const characterBytes = encoder.encode(character).byteLength
    if (bytes + characterBytes > maxBytes) break
    result += character
    bytes += characterBytes
  }
  return result
}

export type VideoTranscriptionJobStatus = 'queued' | 'uploading' | 'processing' | 'partial' | 'ready' | 'failed' | 'cancelled' | 'stale'

export interface VideoTranscriptionJob {
  ref: string
  transcript_ref: string
  status: VideoTranscriptionJobStatus
  failure_reason?: string
}

const terminalVideoTranscriptionStatuses = new Set<VideoTranscriptionJobStatus>(['ready', 'failed', 'cancelled', 'stale'])

export function isTerminalVideoTranscriptionStatus(status: VideoTranscriptionJobStatus): boolean {
  return terminalVideoTranscriptionStatuses.has(status)
}

export interface VideoTranscript {
  ref: string
  text: string
  segments: Array<{ start_ms: number; end_ms: number; speech?: string; audio?: string; visual?: string; on_screen_text?: string; text: string }>
  metadata: { language?: string; duration_ms?: number; summary?: string; content_empty?: boolean }
  validation: { state: string }
  text_truncated?: boolean
  segments_truncated?: boolean
  details_truncated?: boolean
}

export async function startVideoTranscription(workspacePath: string, videoRef: string, focusNotes: string): Promise<{ session_id: string; job: VideoTranscriptionJob }> {
  return requestJson<{ session_id: string; job: VideoTranscriptionJob }>('/v1/workspace/video/transcribe', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, video_ref: videoRef, focus_notes: focusNotes }),
  })
}

export async function getVideoTranscriptionStatus(workspacePath: string, sessionID: string, jobRef: string, signal?: AbortSignal): Promise<VideoTranscriptionJob> {
  const response = await requestJson<{ job: VideoTranscriptionJob }>('/v1/workspace/video/transcribe/status', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, session_id: sessionID, job_ref: jobRef }),
    signal,
  })
  return response.job
}

function pollingDelay(milliseconds: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error('Video transcription polling was cancelled.'))
      return
    }
    const timer = globalThis.setTimeout(resolve, milliseconds)
    signal?.addEventListener('abort', () => {
      globalThis.clearTimeout(timer)
      reject(new Error('Video transcription polling was cancelled.'))
    }, { once: true })
  })
}

export async function pollVideoTranscriptionJob({
  workspacePath,
  sessionID,
  jobRef,
  signal,
  maxAttempts = 300,
  intervalMs = 2_000,
  onUpdate,
  wait = pollingDelay,
}: {
  workspacePath: string
  sessionID: string
  jobRef: string
  signal?: AbortSignal
  maxAttempts?: number
  intervalMs?: number
  onUpdate?: (job: VideoTranscriptionJob) => void
  wait?: (milliseconds: number, signal?: AbortSignal) => Promise<void>
}): Promise<VideoTranscriptionJob> {
  const attempts = Math.max(1, Math.floor(maxAttempts))
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (attempt > 0) await wait(intervalMs, signal)
    const job = await getVideoTranscriptionStatus(workspacePath, sessionID, jobRef, signal)
    onUpdate?.(job)
    if (isTerminalVideoTranscriptionStatus(job.status)) return job
  }
  throw new Error('Video transcription is still running after the bounded polling window. You can return later to check its durable status.')
}

export async function cancelVideoTranscription(workspacePath: string, sessionID: string, jobRef: string): Promise<VideoTranscriptionJob> {
  const response = await requestJson<{ job: VideoTranscriptionJob }>('/v1/workspace/video/transcribe/cancel', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, session_id: sessionID, job_ref: jobRef }),
  })
  return response.job
}

export async function readVideoTranscript(workspacePath: string, sessionID: string, transcriptRef: string): Promise<VideoTranscript> {
  const response = await requestJson<{ transcript: VideoTranscript }>('/v1/workspace/video/transcribe/read', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, session_id: sessionID, transcript_ref: transcriptRef }),
  })
  return response.transcript
}
