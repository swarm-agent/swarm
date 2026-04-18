import { requestJson } from '../../../../app/api'
import type { WorkspaceResolution, WorkspaceResolutionWire } from '../types/workspace'
import { mapWorkspaceResolution } from '../types/workspace'

export async function linkWorkspaceDirectory(workspacePath: string, directoryPath: string): Promise<WorkspaceResolution> {
  const response = await requestJson<{ ok: boolean; workspace: WorkspaceResolutionWire }>('/v1/workspace/directories/add', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      workspace_path: workspacePath,
      directory_path: directoryPath,
    }),
  })

  return mapWorkspaceResolution(response.workspace)
}
