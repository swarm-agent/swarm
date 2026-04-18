import { requestJson } from '../../../../app/api'
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

  const response = await requestJson<{ ok: boolean; workspace: WorkspaceResolutionWire }>('/v1/workspace/add', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
  })

  return mapWorkspaceResolution(response.workspace)
}
