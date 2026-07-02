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
  lane?: string
  command?: string
  helper_pid?: number
  log_path?: string
  hosts?: DesktopUpdateHostStatus[]
  started_at_unix_ms?: number
  updated_at_unix_ms?: number
  completed_at_unix_ms?: number
}

export interface DesktopUpdateHostStatus {
  host_id?: string
  name?: string
  role?: string
  current_phase?: string
  status?: 'idle' | 'running' | 'completed' | 'failed' | string
  message?: string
  error?: string
  phases?: DesktopUpdateHostPhase[]
  metadata?: Record<string, unknown>
}

export interface DesktopUpdateHostPhase {
  name: string
  status: 'idle' | 'running' | 'completed' | 'failed' | string
  message?: string
  error?: string
  started_at_unix_ms?: number
  updated_at_unix_ms?: number
  completed_at_unix_ms?: number
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

export async function fetchDesktopUpdateJob(): Promise<DesktopUpdateJob> {
  return requireJob(await requestJson<DesktopUpdateRunResponse>('/v1/update/run'))
}
