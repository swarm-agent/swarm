import { requestJson } from '../../../app/api'

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

export async function startDesktopUpdate(): Promise<DesktopUpdateJob> {
  return requireJob(await requestJson<DesktopUpdateRunResponse>('/v1/update/run', { method: 'POST' }))
}

export async function fetchDesktopUpdateJob(): Promise<DesktopUpdateJob> {
  return requireJob(await requestJson<DesktopUpdateRunResponse>('/v1/update/run'))
}
