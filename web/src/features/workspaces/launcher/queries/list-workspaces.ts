import { requestJson } from '../../../../app/api'
import type { ListWorkspacesResponse, WorkspaceEntry, WorkspaceEntryWire } from '../types/workspace'
import { mapWorkspaceEntry } from '../types/workspace'

export async function listWorkspaces(limit = 200): Promise<WorkspaceEntry[]> {
  const query = new URLSearchParams({ limit: String(limit) })
  const response = await requestJson<ListWorkspacesResponse & { workspaces: WorkspaceEntryWire[] }>(`/v1/workspace/list?${query.toString()}`)
  return response.workspaces.map(mapWorkspaceEntry)
}
