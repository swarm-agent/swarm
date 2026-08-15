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

export interface VideoTranscriptionJob {
  ref: string
  transcript_ref: string
  status: 'queued' | 'uploading' | 'processing' | 'partial' | 'ready' | 'failed' | 'cancelled' | 'stale'
  failure_reason?: string
}

export interface VideoTranscript {
  ref: string
  text: string
  segments: Array<{ start_ms: number; end_ms: number; speech?: string; audio?: string; visual?: string; on_screen_text?: string; text: string }>
  metadata: { language?: string; duration_ms?: number; summary?: string; content_empty?: boolean }
  validation: { state: string }
}

export async function startVideoTranscription(workspacePath: string, videoRef: string, focusNotes: string): Promise<{ session_id: string; job: VideoTranscriptionJob }> {
  return requestJson<{ session_id: string; job: VideoTranscriptionJob }>('/v1/workspace/video/transcribe', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, video_ref: videoRef, focus_notes: focusNotes }),
  })
}

export async function getVideoTranscriptionStatus(workspacePath: string, sessionID: string, jobRef: string): Promise<VideoTranscriptionJob> {
  const response = await requestJson<{ job: VideoTranscriptionJob }>('/v1/workspace/video/transcribe/status', {
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
