import { apiFetch } from '../../../../app/api'
import {
  mapWorkspaceRepositoryState,
  WorkspaceRepositoryPrerequisiteError,
  type WorkspaceRepositoryStateWire,
} from '../services/workspace-repository'
import type { WorkspaceResolution, WorkspaceResolutionWire } from '../types/workspace'
import { mapWorkspaceResolution } from '../types/workspace'

export async function saveWorkspace(path: string, name: string, themeId: string, makeCurrent: boolean): Promise<WorkspaceResolution> {
  const trimmedThemeId = themeId.trim()
  const body: Record<string, unknown> = {
    path,
    name,
    make_current: makeCurrent,
  }
  if (trimmedThemeId !== '') {
    body.theme_id = trimmedThemeId
  }

  const response = await apiFetch('/v1/workspace/add', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
  })
  const raw = await response.text()
  const payload = (() => {
    try {
      return JSON.parse(raw) as {
        ok?: boolean
        error?: string
        code?: string
        repository?: WorkspaceRepositoryStateWire
        workspace?: WorkspaceResolutionWire
      }
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
    throw new Error('Workspace save did not return a workspace resolution.')
  }
  return mapWorkspaceResolution(payload.workspace)
}
