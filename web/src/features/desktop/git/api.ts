import { requestJson } from '../../../app/api'
import type { GitRealtimeResponse, GitStatusResponse } from './types'

export function gitStatusQueryKey(workspacePath: string) {
  return ['workspace-git-status', workspacePath.trim()] as const
}

export async function fetchGitStatus(workspacePath: string, recentLimit = 12): Promise<GitStatusResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  params.set('recent_limit', String(recentLimit))
  return requestJson<GitStatusResponse>(`/v1/workspace/git/status?${params.toString()}`)
}

export async function startGitRealtime(workspacePath: string): Promise<GitRealtimeResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  return requestJson<GitRealtimeResponse>(`/v1/workspace/git/realtime?${params.toString()}`, { method: 'POST' })
}
