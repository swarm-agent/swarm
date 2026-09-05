import { apiFetch } from '../../../../app/api'
import {
  mapWorkspaceRepositoryState,
  WorkspaceRepositoryPrerequisiteError,
  type WorkspaceRepositoryStateWire,
} from '../services/workspace-repository'
import type { WorkspaceResolution, WorkspaceResolutionWire } from '../types/workspace'
import { mapWorkspaceResolution } from '../types/workspace'

interface WorkspaceSelectResponseWire {
  ok?: boolean
  error?: string
  code?: string
  repository?: WorkspaceRepositoryStateWire
  workspace?: WorkspaceResolutionWire
}

export async function parseWorkspaceSelectResponse(response: Response): Promise<WorkspaceResolution> {
  const raw = await response.text()
  const payload = (() => {
    try {
      return JSON.parse(raw) as WorkspaceSelectResponseWire
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
  if (!payload?.workspace) {
    throw new Error('Workspace selection did not return a workspace resolution.')
  }
  return mapWorkspaceResolution(payload.workspace)
}

export async function selectWorkspace(path: string): Promise<WorkspaceResolution> {
  const response = await apiFetch('/v1/workspace/select', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ path }),
  })
  return parseWorkspaceSelectResponse(response)
}
