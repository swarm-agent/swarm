import { requestJson } from '../../../app/api'

export interface DesktopSessionSearchSnippet {
  source: string
  role?: string
  message_id?: string
  text: string
  created_at?: number
}

export interface DesktopSessionSearchItem {
  id: string
  workspace_path: string
  workspace_name: string
  title: string
  mode: string
  metadata?: Record<string, unknown>
  created_at: number
  updated_at: number
  message_count: number
  last_message_at: number
  archived: boolean
  deleted?: boolean
  snippets?: DesktopSessionSearchSnippet[]
  library_metric?: DesktopSessionLibraryMetric
}

export interface DesktopSessionLibraryMetric {
  session_id: string
  parent_session_id?: string
  root_session_id: string
  lineage_kind?: string
  unlinked_child?: boolean
  updated_at: number
  logical_bytes: number
  conversation_updated_at: number
  conversation_logical_bytes: number
  conversation_session_count: number
}

export interface DesktopSessionLibrarySummary {
  active_conversation_count: number
  archived_conversation_count: number
  raw_session_count: number
  agent_child_count: number
  logical_content_bytes: number
}

export interface DesktopSessionSearchPagination {
  next_cursor?: string
  next_before_updated_at?: number
  next_before_session_id?: string
  has_more: boolean
}

export interface DesktopSessionSearchRequest {
  query?: string
  archived_mode?: 'exclude' | 'include' | 'only'
  global: boolean
  from_updated_at?: number
  to_updated_at?: number
  cursor?: string
  limit: number
}

export interface DesktopSessionSearchResponse {
  ok?: boolean
  items?: DesktopSessionSearchItem[]
  pagination?: DesktopSessionSearchPagination
  summary?: DesktopSessionLibrarySummary
}

export interface DesktopSessionDeletePreview {
  conversation_count: number
  session_count: number
  child_count: number
  logical_bytes: number
  active_run_count: number
  pending_approval_count: number
  recent_75_overlap_count: number
  session_ids: string[]
  confirmation_token: string
}

export interface DesktopSessionDeleteRequest {
  session_ids?: string[]
  updated_before?: number
  archived_mode?: 'exclude' | 'include' | 'only'
  global: boolean
  dry_run?: boolean
  confirmation_token?: string
  confirm_recent?: boolean
}

export async function deleteDesktopSessions(input: DesktopSessionDeleteRequest, signal?: AbortSignal): Promise<DesktopSessionDeletePreview> {
  const response = await requestJson<{ preview?: DesktopSessionDeletePreview }>('/v3/sessions:delete', {
    method: 'POST',
    signal,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!response.preview) throw new Error('Session deletion did not return a preview')
  return response.preview
}

export async function searchDesktopSessions(input: DesktopSessionSearchRequest, signal?: AbortSignal): Promise<Required<Pick<DesktopSessionSearchResponse, 'items' | 'pagination' | 'summary'>>> {
  const response = await requestJson<DesktopSessionSearchResponse>('/v3/sessions:search', {
    method: 'POST',
    signal,
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })

  return {
    items: Array.isArray(response.items) ? response.items : [],
    pagination: response.pagination ?? { has_more: false },
    summary: response.summary ?? { active_conversation_count: 0, archived_conversation_count: 0, raw_session_count: 0, agent_child_count: 0, logical_content_bytes: 0 },
  }
}
