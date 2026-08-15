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
