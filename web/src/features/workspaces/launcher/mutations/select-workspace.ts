import { requestJson } from '../../../../app/api'
import type { WorkspaceResolution, WorkspaceResolutionWire } from '../types/workspace'
import { mapWorkspaceResolution } from '../types/workspace'

export async function selectWorkspace(path: string): Promise<WorkspaceResolution> {
  const response = await requestJson<{ ok: boolean; workspace: WorkspaceResolutionWire }>('/v1/workspace/select', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ path }),
  })

  return mapWorkspaceResolution(response.workspace)
}
