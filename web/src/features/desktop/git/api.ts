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

export function gitStatusQueryKey(workspacePath: string, sessionId = '') {
  return ['workspace-git-status', sessionId.trim(), workspacePath.trim()] as const
}

export async function fetchGitStatus(workspacePath: string, recentLimit = 12, sessionId = ''): Promise<GitStatusResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  if (sessionId.trim()) params.set('session_id', sessionId.trim())
  params.set('recent_limit', String(recentLimit))
  const response = await requestJson<GitStatusResponse>(`/v1/workspace/git/status?${params.toString()}`)
  return { ...response, status: normalizeGitSnapshot(response.status) }
}

export async function startGitRealtime(workspacePath: string, sessionId = ''): Promise<GitRealtimeResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  if (sessionId.trim()) params.set('session_id', sessionId.trim())
  const response = await requestJson<GitRealtimeResponse>(`/v1/workspace/git/realtime?${params.toString()}`, { method: 'POST' })
  return { ...response, status: normalizeGitSnapshot(response.status) }
}

export async function commitWorkspaceChanges(input: {
  workspacePath: string
  cwd?: string
  message: string
  all?: boolean
  endpoint?: string
  sessionId?: string
}): Promise<GitCommitResponse> {
  const endpoint = input.endpoint ?? '/v1/workspace/git/commit'
  const params = new URLSearchParams()
  if (input.sessionId?.trim()) params.set('session_id', input.sessionId.trim())
  const requestEndpoint = params.size > 0 ? `${endpoint}?${params.toString()}` : endpoint
  return requestJson<GitCommitResponse>(requestEndpoint, {
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
