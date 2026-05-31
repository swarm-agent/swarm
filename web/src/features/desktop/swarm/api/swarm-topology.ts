import { requestJson } from '../../../../app/api'

export interface SwarmTopologyRuntimeRecord {
  swarm_id: string
  name: string
  role?: string
  relationship?: string
  backend_url?: string
  desktop_url?: string
  status?: string
  transport?: string
  owner_host_swarm_id?: string
  owner_host_container_id?: string
  group_ids?: string[]
  observed_sources?: string[]
  created_at: number
  updated_at: number
}

export interface SwarmTopologyHostContainerRecord {
  host_container_id: string
  host_swarm_id: string
  runtime_container_ref: string
  name: string
  container_name?: string
  container_id?: string
  runtime?: string
  image?: string
  status?: string
  host_api_base_url?: string
  host_port?: number
  runtime_port?: number
  observed_sources?: string[]
  created_at: number
  updated_at: number
}

export interface SwarmTopologyAttachmentRecord {
  attachment_id: string
  host_container_id: string
  runtime_swarm_id: string
  state?: string
  deployment_id?: string
  remote_deploy_session_id?: string
  last_error?: string
  created_at: number
  updated_at: number
}

export interface SwarmTopologyWorkspaceBindingRecord {
  binding_id: string
  source_workspace_path: string
  source_workspace_name?: string
  destination_runtime_swarm_id?: string
  destination_authority_host_swarm_id?: string
  destination_host_swarm_id?: string
  destination_container_id?: string
  destination_workspace_path?: string
  replication_mode?: string
  writable: boolean
  legacy_target_kind?: string
  created_at: number
  updated_at: number
}

export interface SwarmTopologySessionRouteRecord {
  session_id: string
  runtime_swarm_id?: string
  host_swarm_id?: string
  host_container_id?: string
  workspace_binding_id?: string
  backend_url?: string
  host_workspace_path?: string
  runtime_workspace_path?: string
  created_at: number
  updated_at: number
}

export interface SwarmTopologyMigrationStatus {
  id: string
  version: string
  rebuilt_at: number
  runtime_count: number
  host_container_count: number
  attachment_count: number
  workspace_binding_count: number
  session_route_count: number
}

export interface SwarmTopologySnapshotResponse {
  ok: boolean
  path_id: string
  runtimes: SwarmTopologyRuntimeRecord[]
  host_containers: SwarmTopologyHostContainerRecord[]
  attachments: SwarmTopologyAttachmentRecord[]
  workspace_bindings: SwarmTopologyWorkspaceBindingRecord[]
  session_routes: SwarmTopologySessionRouteRecord[]
  migration_status: SwarmTopologyMigrationStatus
}

export interface SwarmTopologyHostContainersResponse {
  ok: boolean
  path_id: string
  host_swarm_id: string
  host_containers: SwarmTopologyHostContainerRecord[]
}

export interface SwarmTopologyRuntimeOwnerResponse {
  ok: boolean
  path_id: string
  runtime_swarm_id: string
  attachment?: SwarmTopologyAttachmentRecord | null
  host_container?: SwarmTopologyHostContainerRecord | null
}

export interface SwarmTopologyWorkspaceBindingsResponse {
  ok: boolean
  path_id: string
  source_workspace_path: string
  bindings: SwarmTopologyWorkspaceBindingRecord[]
}

export interface SwarmTopologySessionRouteResponse {
  ok: boolean
  path_id: string
  route?: SwarmTopologySessionRouteRecord | null
}

export async function fetchSwarmTopologySnapshot(): Promise<SwarmTopologySnapshotResponse> {
  return requestJson<SwarmTopologySnapshotResponse>('/v1/swarm/topology', {
    cache: 'no-store',
  })
}

export async function fetchSwarmTopologyHostContainers(hostSwarmID: string): Promise<SwarmTopologyHostContainersResponse> {
  const params = new URLSearchParams({ host_swarm_id: hostSwarmID.trim() })
  return requestJson<SwarmTopologyHostContainersResponse>(`/v1/swarm/topology/host-containers?${params.toString()}`, {
    cache: 'no-store',
  })
}

export async function fetchSwarmTopologyRuntimeOwner(runtimeSwarmID: string): Promise<SwarmTopologyRuntimeOwnerResponse> {
  const params = new URLSearchParams({ runtime_swarm_id: runtimeSwarmID.trim() })
  return requestJson<SwarmTopologyRuntimeOwnerResponse>(`/v1/swarm/topology/runtime-owner?${params.toString()}`, {
    cache: 'no-store',
  })
}

export async function fetchSwarmTopologyWorkspaceBindings(sourceWorkspacePath: string): Promise<SwarmTopologyWorkspaceBindingsResponse> {
  const params = new URLSearchParams({ source_workspace_path: sourceWorkspacePath.trim() })
  return requestJson<SwarmTopologyWorkspaceBindingsResponse>(`/v1/swarm/topology/workspace-bindings?${params.toString()}`, {
    cache: 'no-store',
  })
}

export async function fetchSwarmTopologySessionRoute(sessionID: string): Promise<SwarmTopologySessionRouteResponse> {
  const params = new URLSearchParams({ session_id: sessionID.trim() })
  return requestJson<SwarmTopologySessionRouteResponse>(`/v1/swarm/topology/session-route?${params.toString()}`, {
    cache: 'no-store',
  })
}
