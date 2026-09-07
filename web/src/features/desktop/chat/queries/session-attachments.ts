import { requestJson } from '../../../../app/api'

// Read-only projection of /v3/sessions/{id}/repositories, not attachment authority.
export interface AttachmentRepository {
  id: string
  workspace_id: string
  workspace_name: string
  source_path: string
  kind: string
  attached: boolean
  default: boolean
  availability: string
  error?: string
}
export interface AttachmentRepositoryPage {
  ok: boolean
  items: AttachmentRepository[]
  next_cursor?: string
}

export async function fetchAttachmentPage(sessionId: string, cursor: string, signal?: AbortSignal): Promise<AttachmentRepositoryPage> {
  const params = new URLSearchParams({ limit: '20' })
  if (cursor) params.set('cursor', cursor)
  const page = await requestJson<AttachmentRepositoryPage>(`/v3/sessions/${encodeURIComponent(sessionId)}/repositories?${params}`, { signal })
  if (!page.ok || !Array.isArray(page.items)) throw new Error('Workspace inventory unavailable')
  return page
}

export function selectSessionAttachments(pages: AttachmentRepositoryPage[]): AttachmentRepository[] {
  const attachments = new Map<string, AttachmentRepository>()
  for (const page of pages) {
    for (const item of page.items) {
      if (item.attached !== true || item.kind !== 'source' || !item.workspace_id) continue
      // The first occurrence is the current context; later retained contexts must
      // not replace it. Names and physical paths are not workspace identities.
      if (!attachments.has(item.workspace_id)) attachments.set(item.workspace_id, item)
    }
  }
  return [...attachments.values()].slice(0, 64)
}
