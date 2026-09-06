import { requestJson, requestStartupJson } from '../../../app/api'
import { SharedRequestPool } from '../../../app/request-lifecycle'
import type { GitCommitSuggestionResponse, GitRealtimeResponse, GitSnapshot, GitStatusResponse } from './types'

function normalizeGitSnapshot(snapshot: GitSnapshot): GitSnapshot {
  return {
    ...snapshot,
    files: Array.isArray(snapshot.files) ? snapshot.files : [],
    recent_commits: Array.isArray(snapshot.recent_commits) ? snapshot.recent_commits : [],
    session_commits: Array.isArray(snapshot.session_commits) ? snapshot.session_commits : [],
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

export async function fetchGitStatus(workspacePath: string, recentLimit = 12, sessionId = '', signal?: AbortSignal): Promise<GitStatusResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  if (sessionId.trim()) params.set('session_id', sessionId.trim())
  params.set('recent_limit', String(recentLimit))
  const response = await requestStartupJson<GitStatusResponse>(`/v1/workspace/git/status?${params.toString()}`, { signal })
  return { ...response, status: normalizeGitSnapshot(response.status) }
}

// The daemon holds an unchanged token for 25s; allow transport/body overhead.
export const GIT_REALTIME_TIMEOUT_MS = 35_000
const gitRealtimeRequests = new SharedRequestPool<GitRealtimeResponse>()

export function startGitRealtime(workspacePath: string, sessionId = '', watchToken = '', signal?: AbortSignal): Promise<GitRealtimeResponse> {
  const params = new URLSearchParams()
  params.set('workspace_path', workspacePath)
  if (sessionId.trim()) params.set('session_id', sessionId.trim())
  if (watchToken.trim()) params.set('watch_token', watchToken.trim())
  const endpoint = `/v1/workspace/git/realtime?${params.toString()}`
  return gitRealtimeRequests.run(endpoint, (sharedSignal) =>
    requestJson<GitRealtimeResponse>(endpoint, { method: 'POST', signal: sharedSignal }, true, GIT_REALTIME_TIMEOUT_MS)
      .then((response) => ({ ...response, status: normalizeGitSnapshot(response.status) })), signal)
}

export async function suggestWorkspaceCommitMessage(input: {
  workspacePath: string
  cwd?: string
  sessionId?: string
}): Promise<GitCommitSuggestionResponse> {
  const params = new URLSearchParams()
  if (input.sessionId?.trim()) params.set('session_id', input.sessionId.trim())
  const endpoint = `/v1/workspace/git/commit/suggestion${params.size > 0 ? `?${params.toString()}` : ''}`
  return requestJson<GitCommitSuggestionResponse>(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      workspace_path: input.workspacePath,
      cwd: input.cwd ?? input.workspacePath,
    }),
  })
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
