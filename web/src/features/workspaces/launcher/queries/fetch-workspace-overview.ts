import { requestJson } from '../../../../app/api'
import { mapWorkspaceOverviewResponse, type WorkspaceOverviewResponse, type WorkspaceOverviewResponseWire } from '../types/workspace-overview'

export async function fetchWorkspaceOverview(roots: string[] = [], sessionLimit = 0): Promise<WorkspaceOverviewResponse> {
  const search = new URLSearchParams({
    workspace_limit: '1000',
    discover_limit: '1000',
    limit: '100',
  })
  if (sessionLimit > 0) {
    search.set('session_limit', String(sessionLimit))
  }

  const normalizedRoots = roots.map((value) => value.trim()).filter((value) => value !== '')
  if (normalizedRoots.length > 0) {
    search.set('roots', normalizedRoots.join(','))
  }

  const workspaces: NonNullable<WorkspaceOverviewResponseWire['workspaces']> = []
  let firstResponse: WorkspaceOverviewResponseWire | null = null
  let cursor = 0
  do {
    search.set('cursor', String(cursor))
    const response = await requestJson<WorkspaceOverviewResponseWire>(`/v1/workspace/overview?${search.toString()}`, {
      cache: 'no-store',
    })
    firstResponse ??= response
    workspaces.push(...(response.workspaces ?? []))
    cursor = response.has_more && typeof response.next_cursor === 'number' && response.next_cursor > cursor
      ? response.next_cursor
      : 0
  } while (cursor > 0)

  return mapWorkspaceOverviewResponse({
    ...(firstResponse ?? {}),
    workspaces,
  })
}
