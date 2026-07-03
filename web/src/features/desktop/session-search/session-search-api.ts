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
}

export async function searchDesktopSessions(input: DesktopSessionSearchRequest, signal?: AbortSignal): Promise<Required<Pick<DesktopSessionSearchResponse, 'items' | 'pagination'>>> {
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
  }
}
