import { requestJson } from '../../../../app/api'
import type { WorkspaceResolution, WorkspaceResolutionWire } from '../types/workspace'
import { mapWorkspaceResolution } from '../types/workspace'

export async function moveWorkspace(path: string, delta: number): Promise<WorkspaceResolution> {
  const response = await requestJson<{ ok: boolean; workspace: WorkspaceResolutionWire }>('/v1/workspace/move', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ path, delta }),
  })

  return mapWorkspaceResolution(response.workspace)
}
