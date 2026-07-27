import { requestJson } from '../../../app/api'

export interface ModelCatalogRefreshRecord {
  source?: string
  snapshot_id?: string
  snapshot_version?: string
  generated_at?: string
  fetched_at?: number
  last_checked_at?: number
  record_count?: number
  model_count?: number
  not_modified?: boolean
  used_cache?: boolean
  used_pinned?: boolean
  using_cache_fallback?: boolean
  last_refresh_reason?: string
}

export interface ModelCatalogCheckResponse {
  ok: boolean
  refresh: ModelCatalogRefreshRecord
}

export async function checkModelCatalogSnapshot(signal?: AbortSignal): Promise<ModelCatalogCheckResponse> {
  return requestJson<ModelCatalogCheckResponse>('/v1/model/catalog/check', {
    method: 'POST',
    signal,
  })
}
