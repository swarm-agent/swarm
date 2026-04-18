import { requestJson } from '../../../../app/api'
import { mapWorkspaceOverviewResponse, type WorkspaceOverviewResponse, type WorkspaceOverviewResponseWire } from '../types/workspace-overview'

export async function fetchWorkspaceOverview(roots: string[] = [], sessionLimit = 0): Promise<WorkspaceOverviewResponse> {
  const search = new URLSearchParams({
    workspace_limit: '200',
    discover_limit: '200',
  })
  if (sessionLimit > 0) {
    search.set('session_limit', String(sessionLimit))
  }

  const normalizedRoots = roots.map((value) => value.trim()).filter((value) => value !== '')
  if (normalizedRoots.length > 0) {
    search.set('roots', normalizedRoots.join(','))
  }

  const response = await requestJson<WorkspaceOverviewResponseWire>(`/v1/workspace/overview?${search.toString()}`, {
    cache: 'no-store',
  })
  return mapWorkspaceOverviewResponse(response)
}
