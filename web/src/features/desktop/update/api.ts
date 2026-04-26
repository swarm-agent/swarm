import { requestJson } from '../../../app/api'

export interface DesktopUpdateStatus {
  current_version: string
  current_lane?: string
  dev_mode: boolean
  suppressed: boolean
  reason?: string
  checked_at_unix_ms?: number
  latest_version?: string
  latest_url?: string
  update_available: boolean
  comparison_source?: string
  error?: string
  stale?: boolean
}

export interface DesktopUpdateJob {
  id: string
  kind: string
  status: 'idle' | 'running' | 'completed' | 'failed' | string
  message?: string
  error?: string
  started_at_unix_ms?: number
  updated_at_unix_ms?: number
  completed_at_unix_ms?: number
}

export interface LocalContainerUpdatePlan {
  path_id: string
  mode: string
  dev_mode: boolean
  target: LocalContainerUpdateTarget
  summary: LocalContainerUpdateSummary
  containers: LocalContainerUpdateItem[]
  contract: LocalContainerUpdateContract
  error?: string
  checked_at_unix_ms?: number
}

export interface LocalContainerUpdateTarget {
  image_ref?: string
  digest_ref?: string
  version?: string
  fingerprint?: string
  commit?: string
}

export interface LocalContainerUpdateSummary {
  total: number
  affected: number
  already_current: number
  needs_update: number
  unknown: number
  errors: number
}

export interface LocalContainerUpdateContract {
  warning_copy: string
  dismissal_scope: string
  failure_semantics: string
  replacement: string
}

export interface LocalContainerUpdateItem {
  id: string
  name: string
  container_name: string
  runtime: string
  status?: string
  container_id?: string
  stored_image_ref?: string
  current_image_ref?: string
  current_digest_ref?: string
  current_fingerprint?: string
  target_image_ref?: string
  target_digest_ref?: string
  target_version?: string
  target_fingerprint?: string
  state: string
  reason?: string
  error?: string
  labels?: Record<string, string>
}

interface DesktopUpdateRunResponse {
  ok?: boolean
  job?: DesktopUpdateJob
}

function requireJob(response: DesktopUpdateRunResponse): DesktopUpdateJob {
  if (!response.job) {
    throw new Error('Update response was missing job status')
  }
  return response.job
}

export async function fetchDesktopUpdateStatus(): Promise<DesktopUpdateStatus> {
  return requestJson<DesktopUpdateStatus>('/v1/update/status')
}

export async function startDesktopUpdate(): Promise<DesktopUpdateJob> {
  return requireJob(await requestJson<DesktopUpdateRunResponse>('/v1/update/run', { method: 'POST' }))
}

export async function fetchLocalContainerUpdatePlan(options?: {
  devMode?: boolean
  targetVersion?: string
}): Promise<LocalContainerUpdatePlan> {
  const params = new URLSearchParams()
  if (typeof options?.devMode === 'boolean') {
    params.set('dev_mode', String(options.devMode))
  }
  if (options?.targetVersion?.trim()) {
    params.set('target_version', options.targetVersion.trim())
  }
  const query = params.toString()
  return requestJson<LocalContainerUpdatePlan>(`/v1/update/local-containers${query ? `?${query}` : ''}`)
}

export async function fetchDesktopUpdateJob(): Promise<DesktopUpdateJob> {
  return requireJob(await requestJson<DesktopUpdateRunResponse>('/v1/update/run'))
}
