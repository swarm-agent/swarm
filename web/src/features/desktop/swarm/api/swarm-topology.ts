import { requestJson } from '../../../../app/api'

export interface SwarmTopologyWorkspaceBindingRecord {
  binding_id: string
  source_workspace_path: string
  source_workspace_name?: string
  destination_runtime_swarm_id?: string
  destination_authority_host_swarm_id?: string
  destination_host_swarm_id?: string
  destination_workspace_path?: string
  writable: boolean
  created_at: number
  updated_at: number
}

export interface SwarmTopologyWorkspaceBindingsResponse {
  ok: boolean
  path_id: string
  source_workspace_path: string
  bindings: SwarmTopologyWorkspaceBindingRecord[]
}

export async function fetchSwarmTopologyWorkspaceBindings(sourceWorkspacePath: string): Promise<SwarmTopologyWorkspaceBindingsResponse> {
  const params = new URLSearchParams({ source_workspace_path: sourceWorkspacePath.trim() })
  return requestJson<SwarmTopologyWorkspaceBindingsResponse>(`/v1/swarm/topology/workspace-bindings?${params.toString()}`, {
    cache: 'no-store',
  })
}
