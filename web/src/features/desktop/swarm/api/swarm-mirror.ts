import { requestJson } from '../../../../app/api'
import type { SwarmLocalContainer } from '../../onboarding/api'
import type { DeployContainerDeployment } from './deploy-container'

export interface SwarmMirrorHostResource {
  swarm_id: string
  name: string
  role: string
  pairing_state?: string
  parent_swarm_id?: string
  current_group_id?: string
  backend_url?: string
  desktop_url?: string
  online: boolean
  updated_at: number
}

export interface SwarmMirrorWorkspaceResource {
  path: string
  workspace_name?: string
  name?: string
  theme_id?: string
  directories?: string[]
  is_git_repo?: boolean
  replication_links?: unknown[]
  sort_index?: number
  added_at?: number
  updated_at?: number
  last_selected_at?: number
  active?: boolean
  worktree_enabled?: boolean
}

export interface SwarmMirrorResource<T = unknown> {
  managedSwarmID: string
  kind: string
  id: string
  sequence: number
  updatedAt: number
  resource: T
}

export interface SwarmMirrorResources {
  hosts: SwarmMirrorResource<SwarmMirrorHostResource>[]
  workspaces: SwarmMirrorResource<SwarmMirrorWorkspaceResource>[]
  containers: SwarmMirrorResource<SwarmLocalContainer>[]
  deployments: SwarmMirrorResource<DeployContainerDeployment>[]
}

interface SwarmMirrorResourceWire {
  managed_swarm_id?: string
  kind?: string
  id?: string
  sequence?: number
  updated_at?: number
  resource?: unknown
}

export async function fetchSwarmMirrorResources(): Promise<SwarmMirrorResources> {
  const response = await requestJson<{ ok?: boolean; resources?: SwarmMirrorResourceWire[] }>('/v1/swarm/mirror/resources?resources=host,workspaces,containers,deployments', {
    cache: 'no-store',
  })
  const resources = Array.isArray(response.resources) ? response.resources.map(mapMirrorResource) : []
  return {
    hosts: resources.filter((item): item is SwarmMirrorResource<SwarmMirrorHostResource> => item.kind === 'host'),
    workspaces: resources.filter((item): item is SwarmMirrorResource<SwarmMirrorWorkspaceResource> => item.kind === 'workspace'),
    containers: resources.filter((item): item is SwarmMirrorResource<SwarmLocalContainer> => item.kind === 'container'),
    deployments: resources.filter((item): item is SwarmMirrorResource<DeployContainerDeployment> => item.kind === 'deployment'),
  }
}

function mapMirrorResource(record: SwarmMirrorResourceWire): SwarmMirrorResource {
  return {
    managedSwarmID: String(record.managed_swarm_id ?? '').trim(),
    kind: String(record.kind ?? '').trim(),
    id: String(record.id ?? '').trim(),
    sequence: typeof record.sequence === 'number' ? record.sequence : 0,
    updatedAt: typeof record.updated_at === 'number' ? record.updated_at : 0,
    resource: record.resource ?? {},
  }
}
