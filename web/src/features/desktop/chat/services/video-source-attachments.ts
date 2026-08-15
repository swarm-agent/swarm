import { requestJson } from '../../../../app/api'

export const DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT = 8

export interface DesktopVideoSourceAttachment {
  ref: string
  name: string
  mime_type: string
  size_bytes: number
  source_fingerprint: string
  transcript_ref?: string
}

export interface DesktopVideoSourceDirectory {
  name: string
  relative_path: string
}

interface VideoSourceBrowseResponse {
  root_path?: string
  relative_path?: string
  directories?: DesktopVideoSourceDirectory[]
  clips?: Array<{
    ref?: string
    name?: string
    mime_type?: string
    size_bytes?: number
    source_fingerprint?: string
    transcript_ref?: string
  }>
}

export interface DesktopVideoSourceBrowseResult {
  rootPath: string
  relativePath: string
  directories: DesktopVideoSourceDirectory[]
  clips: DesktopVideoSourceAttachment[]
}

export async function browseDesktopVideoSource(
  workspacePath: string,
  rootPath: string,
  relativePath = '.',
  signal?: AbortSignal,
): Promise<DesktopVideoSourceBrowseResult> {
  const response = await requestJson<VideoSourceBrowseResponse>('/v1/workspace/video/scan', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      workspace_path: workspacePath,
      root_path: rootPath,
      relative_path: relativePath,
    }),
    signal,
  })
  return {
    rootPath: String(response.root_path ?? rootPath).trim(),
    relativePath: String(response.relative_path ?? relativePath).trim() || '.',
    directories: (response.directories ?? []).flatMap((entry) => {
      const name = String(entry?.name ?? '').trim()
      const nextRelativePath = String(entry?.relative_path ?? '').trim()
      return name && nextRelativePath ? [{ name, relative_path: nextRelativePath }] : []
    }),
    clips: (response.clips ?? []).flatMap((entry) => {
      const ref = String(entry?.ref ?? '').trim()
      const name = String(entry?.name ?? '').trim()
      const mimeType = String(entry?.mime_type ?? '').trim()
      const fingerprint = String(entry?.source_fingerprint ?? '').trim()
      const size = typeof entry?.size_bytes === 'number' ? entry.size_bytes : 0
      const transcriptRef = String(entry?.transcript_ref ?? '').trim()
      return ref && name && mimeType.startsWith('video/') && fingerprint && size > 0
        ? [{ ref, name, mime_type: mimeType, size_bytes: size, source_fingerprint: fingerprint, transcript_ref: transcriptRef || undefined }]
        : []
    }),
  }
}
