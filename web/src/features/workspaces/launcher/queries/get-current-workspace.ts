import { requestJson } from '../../../../app/api'
import type { WorkspaceResolution, WorkspaceResolutionWire } from '../types/workspace'
import { mapWorkspaceResolution } from '../types/workspace'

export async function getCurrentWorkspace(): Promise<WorkspaceResolution | null> {
  const response = await requestJson<WorkspaceResolutionWire>('/v1/workspace/current')
  return mapWorkspaceResolution(response)
}
