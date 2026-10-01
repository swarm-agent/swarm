import { requestJson } from '../../../../../app/api'

export interface VideoCreateConstraint {
  supported: boolean
  reason?: string
  aspect_ratios?: string[]
  resolutions?: string[]
  durations?: number[]
  default_ratio?: string
  default_resolution?: string
  default_duration?: number
  resolution_durations?: Record<string, number[]>
  initial_image_supported?: boolean
  initial_image_max_inputs?: number
  supports_duration?: boolean
}

export interface VideoEditConstraint {
  supported: boolean
  reason?: string
  supports_duration?: boolean
  max_external_duration_sec?: number
  max_source_duration_sec?: number
  requires_handle_match?: boolean
  requires_handle_for_long_video?: boolean
  requires_interaction_handle?: boolean
  supported_providers?: string[]
  required_source_provider?: string
  required_source_transport?: string
  source_model_match?: string
}

export interface VideoExtendConstraint {
  supported: boolean
  reason?: string
  locked_duration_seconds?: number
  locked_resolution?: string
  locked_aspect_ratio_matches_source?: boolean
  supported_aspect_ratios?: string[]
  supports_duration?: boolean
  max_source_duration_sec?: number
  max_total_duration_sec?: number
  max_extension_count?: number
  requires_veo_source?: boolean
  requires_omni_source?: boolean
  disallows_veo_lite_source?: boolean
  source_observed_resolutions?: string[]
  requires_source_provenance?: boolean
  required_source_provider?: string
  required_source_transport?: string
  requires_provider_resource?: boolean
  requires_interaction_handle?: boolean
  requires_output_digest?: boolean
  requires_known_extension_count?: boolean
  max_reference_age_ms?: number
  allowed_source_models?: string[]
  disallowed_source_models?: string[]
  observed_dimension_pairs?: Array<[number, number]>
}

export interface VideoOperationConstraints {
  model: string
  provider: string
  create: VideoCreateConstraint
  edit: VideoEditConstraint
  extend: VideoExtendConstraint
}

export interface MediaInitialImageOption {
  supported: boolean
  max_inputs?: number
  supported_mime_types?: string[]
  notes?: string
}

export interface MediaModelGenerationOptions {
  aspect_ratios?: string[]
  resolutions?: string[]
  durations?: number[]
  default_ratio?: string
  default_resolution?: string
  default_duration?: number
  max_outputs?: number
  resolution_durations?: Record<string, number[]>
  initial_image?: MediaInitialImageOption
  constraints?: VideoOperationConstraints
}

export interface MediaCatalogModelOption {
  id: string
  provider: string
  model: string
  display_name: string
  kind: 'image_generation' | 'video_understanding' | 'video_generation' | 'video_iteration' | 'audio_generation'
  ready: boolean
  reason?: string
  pricing?: unknown
  generation_options?: MediaModelGenerationOptions
  constraints?: VideoOperationConstraints
}

export interface MediaSettingsCatalog {
  image_models: MediaCatalogModelOption[]
  transcription_models: MediaCatalogModelOption[]
  video_generation_models: MediaCatalogModelOption[]
  video_iteration_models: MediaCatalogModelOption[]
  video_models: MediaCatalogModelOption[]
  audio_models?: MediaCatalogModelOption[]
  default_image_model?: string
  default_video_model?: string
  default_audio_model?: string
  video_ready: boolean
  video_status: string
  audio_ready?: boolean
  audio_status?: string
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

export async function startVideoTranscription(workspacePath: string, videoRefs: string[], focusNotes: string): Promise<{ session_id: string; jobs: VideoTranscriptionJob[] }> {
  const response = await requestJson<{ session_id: string; jobs?: VideoTranscriptionJob[]; job?: VideoTranscriptionJob }>('/v1/workspace/video/transcribe', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, video_refs: videoRefs, focus_notes: focusNotes }),
  })
  const jobs = Array.isArray(response.jobs) ? response.jobs : response.job ? [response.job] : []
  return { session_id: response.session_id, jobs }
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
