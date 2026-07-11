import { requestJson } from '../../../app/api'
import type { GitRealtimeResponse, GitSnapshot, GitStatusResponse } from './types'

function normalizeGitSnapshot(snapshot: GitSnapshot): GitSnapshot {
  return {
    ...snapshot,
    files: Array.isArray(snapshot.files) ? snapshot.files : [],
  }
}

export interface GitCommitResponse {
  ok?: boolean
  workspace_path?: string
  cwd?: string
  argv?: string[]
  exit_code?: number
  timed_out?: boolean
  output?: string
  summary?: string
  error?: string
}

export function gitStatusQueryKey(workspacePath: string) {
  return ['workspace-git-status', workspacePath.trim()] as const
}

export async function fetchGitStatus(workspacePath: string, recentLimit = 12): Promise<GitStatusResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  params.set('recent_limit', String(recentLimit))
  const response = await requestJson<GitStatusResponse>(`/v1/workspace/git/status?${params.toString()}`)
  return { ...response, status: normalizeGitSnapshot(response.status) }
}

export async function startGitRealtime(workspacePath: string): Promise<GitRealtimeResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  const response = await requestJson<GitRealtimeResponse>(`/v1/workspace/git/realtime?${params.toString()}`, { method: 'POST' })
  return { ...response, status: normalizeGitSnapshot(response.status) }
}

export async function commitWorkspaceChanges(input: {
  workspacePath: string
  cwd?: string
  message: string
  all?: boolean
  endpoint?: string
}): Promise<GitCommitResponse> {
  return requestJson<GitCommitResponse>(input.endpoint ?? '/v1/workspace/git/commit', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      workspace_path: input.workspacePath,
      cwd: input.cwd ?? input.workspacePath,
      message: input.message,
      all: input.all ?? true,
    }),
  })
}
