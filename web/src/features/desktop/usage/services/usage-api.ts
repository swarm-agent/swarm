import { requestJson } from '../../../../app/api'

export interface SessionUsageDashboardResponse {
  ok: boolean
  summary: SessionUsageDashboardSummary
  daily: SessionUsageDailyItem[]
  by_provider: SessionUsageProviderItem[]
  by_model: SessionUsageModelItem[]
  media: SessionUsageMediaSummary
  recent_sessions: SessionUsageSessionItem[]
  meta: SessionUsageDashboardMeta
}

export interface SessionUsageDashboardSummary {
  total_tokens: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  thinking_tokens: number
  total_cost_usd: number
  codex_nominal_cost_usd: number
  total_turns: number
  total_sessions: number
  total_media_calls: number
  media_cost_usd: number
}

export interface SessionUsageDailyItem {
  date: string
  timestamp: number
  total_tokens: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  thinking_tokens: number
  cost_usd: number
  codex_nominal_cost_usd: number
  turns: number
  media_calls: number
  media_cost_usd: number
  models_used: Record<string, number>
}

export interface SessionUsageProviderItem {
  provider: string
  display_name: string
  total_tokens: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  thinking_tokens: number
  cost_usd: number
  codex_nominal_cost_usd: number
  is_subscription: boolean
  turns: number
  sessions: number
  models: string[]
}

export interface SessionUsageModelItem {
  model: string
  provider: string
  display_name: string
  total_tokens: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  thinking_tokens: number
  cost_usd: number
  codex_nominal_cost_usd: number
  turns: number
  sessions: number
  input_price_per_million: number
  output_price_per_million: number
  cached_price_per_million: number
}

export interface SessionUsageMediaSummary {
  total_count: number
  total_cost_usd: number
  image_count: number
  image_cost_usd: number
  video_count: number
  video_cost_usd: number
  audio_count: number
  audio_cost_usd: number
  recent_items: SessionUsageMediaItem[]
}

export interface SessionUsageMediaItem {
  id: string
  session_id: string
  media_type: string
  kind: 'image' | 'video' | 'audio'
  filename: string
  label: string
  size: number
  cost_usd: number
  created_at: number
}

export interface SessionUsageSessionItem {
  session_id: string
  title: string
  provider: string
  model: string
  total_tokens: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  thinking_tokens: number
  cost_usd: number
  turn_count: number
  last_active_at: number
}

export interface SessionUsageDashboardMeta {
  generated_at: number
  time_range: string
  total_records_analyzed: number
}

export interface FetchUsageParams {
  timeRange?: string
  provider?: string
  model?: string
  sessionId?: string
  startDate?: string
  endDate?: string
}

export async function fetchSessionUsageDashboard(
  params?: FetchUsageParams,
  signal?: AbortSignal,
): Promise<SessionUsageDashboardResponse> {
  const query = new URLSearchParams()
  if (params?.timeRange) query.set('time_range', params.timeRange)
  if (params?.provider) query.set('provider', params.provider)
  if (params?.model) query.set('model', params.model)
  if (params?.sessionId) query.set('session_id', params.sessionId)
  if (params?.startDate) query.set('start_date', params.startDate)
  if (params?.endDate) query.set('end_date', params.endDate)

  const queryString = query.toString()
  const endpoint = queryString ? `/v3/usage?${queryString}` : '/v3/usage'
  return requestJson<SessionUsageDashboardResponse>(endpoint, { signal })
}
