import { requestJson } from '../../../../app/api'
import type { DiscoverWorkspacesResponse, WorkspaceDiscoverEntry, WorkspaceDiscoverEntryWire } from '../types/workspace'
import { mapWorkspaceDiscoverEntry } from '../types/workspace'

export async function discoverWorkspaces(limit = 200, roots: string[] = []): Promise<WorkspaceDiscoverEntry[]> {
  const query = new URLSearchParams({ limit: String(limit) })
  const cleanRoots = roots.map((value) => value.trim()).filter((value) => value !== '')
  if (cleanRoots.length > 0) {
    query.set('roots', cleanRoots.join(','))
  }
  const response = await requestJson<DiscoverWorkspacesResponse & { directories: WorkspaceDiscoverEntryWire[] }>(`/v1/workspace/discover?${query.toString()}`)
  return response.directories.map(mapWorkspaceDiscoverEntry)
}
