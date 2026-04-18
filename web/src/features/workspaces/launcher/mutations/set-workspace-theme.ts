import { requestJson } from '../../../../app/api'
import type { WorkspaceResolution, WorkspaceResolutionWire } from '../types/workspace'
import { mapWorkspaceResolution } from '../types/workspace'

export async function setWorkspaceTheme(path: string, themeId: string): Promise<WorkspaceResolution> {
  const response = await requestJson<{ ok: boolean; workspace: WorkspaceResolutionWire }>('/v1/workspace/theme', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      path,
      theme_id: themeId,
    }),
  })

  return mapWorkspaceResolution(response.workspace)
}
