import { apiFetch } from '../../../../app/api'
import {
  mapWorkspaceRepositoryState,
  WorkspaceRepositoryPrerequisiteError,
  type WorkspaceRepositoryState,
  type WorkspaceRepositoryStateWire,
} from '../services/workspace-repository'

interface WorkspaceRepositoryResponseWire {
  ok?: boolean
  error?: string
  code?: string
  repository?: WorkspaceRepositoryStateWire
}

export async function setupWorkspaceRepository(path: string, expectedResolvedPath: string): Promise<WorkspaceRepositoryState> {
  const response = await apiFetch('/v1/workspace/repository/setup', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ path, expected_resolved_path: expectedResolvedPath }),
  })
  const raw = await response.text()
  const payload = (() => {
    try {
      return JSON.parse(raw) as WorkspaceRepositoryResponseWire
    } catch {
      return null
    }
  })()
  if (!response.ok) {
    if (payload?.code === 'workspace_repository_not_ready' && payload.repository) {
      throw new WorkspaceRepositoryPrerequisiteError(mapWorkspaceRepositoryState(payload.repository), payload.error)
    }
    throw new Error(payload?.error?.trim() || raw.trim() || `Request failed with status ${response.status}`)
  }
  if (!payload?.repository) {
    throw new Error('Repository setup did not return repository state.')
  }
  const repository = mapWorkspaceRepositoryState(payload.repository)
  if (repository.state !== 'ready' || !repository.headCommit) {
    throw new WorkspaceRepositoryPrerequisiteError(repository, 'Repository setup did not produce a committed Git repository.')
  }
  return repository
}
