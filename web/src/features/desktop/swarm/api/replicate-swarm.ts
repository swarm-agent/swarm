import { apiFetch } from '../../../../app/api'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'

export interface ReplicateSwarmSyncInput {
  enabled: boolean
  mode?: string
  modules?: string[]
  vaultPassword?: string
}

export interface ReplicateSwarmWorkspaceInput {
  sourceWorkspacePath: string
  replicationMode?: 'bundle' | 'copy'
  writable?: boolean
}

export interface ReplicateSwarmContainerPackageInput {
  name: string
  source?: 'recommended' | 'user_added' | 'workspace_scan'
  reason?: string
}

export interface ReplicateSwarmContainerPackagesInput {
  baseImage?: string
  packageManager?: string
  packages?: ReplicateSwarmContainerPackageInput[]
}

export interface ReplicateSwarmRequest {
  mode: 'local' | 'remote'
  swarmName: string
  runtime?: 'podman' | 'docker' | ''
  bypassPermissions?: boolean
  alwaysOn?: boolean
  sync: ReplicateSwarmSyncInput
  workspaces: ReplicateSwarmWorkspaceInput[]
  containerPackages?: ReplicateSwarmContainerPackagesInput
}

export interface ReplicateSwarmLinkWire {
  id: string
  target_kind: string
  target_swarm_id: string
  target_swarm_name: string
  target_workspace_path: string
  replication_mode: string
  writable: boolean
  sync?: {
    enabled?: boolean
    mode?: string
    modules?: string[]
  }
  created_at: number
  updated_at: number
}

export interface ReplicateSwarmWorkspaceWire {
  source_workspace_path: string
  source_workspace_name: string
  link: ReplicateSwarmLinkWire
}

export interface ReplicateSwarmFailureWire {
  deployment_id?: string
  attach_status?: string
  last_attach_error?: string
  runtime?: string
  container_name?: string
  backend_host_port?: number
  desktop_host_port?: number
  child_backend_url?: string
  child_desktop_url?: string
}

export interface ReplicateSwarmResponseWire {
  ok?: boolean
  swarm?: {
    id?: string
    name?: string
    mode?: string
    deployment_id?: string
    attach_status?: string
    group_id?: string
    bypass_permissions?: boolean
  }
  workspaces?: ReplicateSwarmWorkspaceWire[]
}

export interface ReplicateSwarmResponse {
  ok: boolean
  swarm: {
    id: string
    name: string
    mode: string
    deploymentId: string
    attachStatus: string
    groupId: string
    bypassPermissions: boolean
  }
  workspaces: Array<{
    sourceWorkspacePath: string
    sourceWorkspaceName: string
    link: {
      id: string
      targetKind: string
      targetSwarmId: string
      targetSwarmName: string
      targetWorkspacePath: string
      replicationMode: string
      writable: boolean
      sync: {
        enabled: boolean
        mode: string
        modules: string[]
      }
      createdAt: number
      updatedAt: number
    }
  }>
}

export interface ReplicateSwarmLaunchFailure {
  pathID: string
  error: string
  errorDetail: string
  failure: {
    deploymentId: string
    attachStatus: string
    lastAttachError: string
    runtime: string
    containerName: string
    backendHostPort: number
    desktopHostPort: number
    childBackendURL: string
    childDesktopURL: string
  }
}

export class ReplicateSwarmLaunchError extends Error {
  readonly details: ReplicateSwarmLaunchFailure

  constructor(details: ReplicateSwarmLaunchFailure) {
    super(details.errorDetail || details.error || 'Failed to replicate swarm')
    this.name = 'ReplicateSwarmLaunchError'
    this.details = details
  }
}

function normalizeReplicateSwarmFailure(payload: {
  path_id?: unknown
  error?: unknown
  error_detail?: unknown
  failure?: ReplicateSwarmFailureWire
}): ReplicateSwarmLaunchFailure {
  const failure = payload.failure ?? {}
  return {
    pathID: typeof payload.path_id === 'string' ? payload.path_id.trim() : '',
    error: typeof payload.error === 'string' ? payload.error.trim() : '',
    errorDetail: typeof payload.error_detail === 'string' ? payload.error_detail.trim() : '',
    failure: {
      deploymentId: String(failure.deployment_id ?? '').trim(),
      attachStatus: String(failure.attach_status ?? '').trim(),
      lastAttachError: String(failure.last_attach_error ?? '').trim(),
      runtime: String(failure.runtime ?? '').trim(),
      containerName: String(failure.container_name ?? '').trim(),
      backendHostPort: typeof failure.backend_host_port === 'number' ? failure.backend_host_port : 0,
      desktopHostPort: typeof failure.desktop_host_port === 'number' ? failure.desktop_host_port : 0,
      childBackendURL: String(failure.child_backend_url ?? '').trim(),
      childDesktopURL: String(failure.child_desktop_url ?? '').trim(),
    },
  }
}

export async function replicateSwarm(input: ReplicateSwarmRequest): Promise<ReplicateSwarmResponse> {
  const response = await apiFetch('/v1/swarm/replicate', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      mode: input.mode,
      swarm_name: input.swarmName,
      runtime: input.runtime,
      bypass_permissions: input.bypassPermissions,
      always_on: input.alwaysOn,
      sync: {
        enabled: input.sync.enabled,
        mode: input.sync.mode,
        modules: input.sync.modules,
        vault_password: input.sync.vaultPassword,
      },
      workspaces: input.workspaces.map((workspace) => ({
        source_workspace_path: workspace.sourceWorkspacePath,
        replication_mode: workspace.replicationMode,
        writable: workspace.writable,
      })),
      container_packages: input.containerPackages ? {
        base_image: input.containerPackages.baseImage,
        package_manager: input.containerPackages.packageManager,
        packages: (input.containerPackages.packages ?? []).map((pkg) => ({
          name: pkg.name,
          source: pkg.source,
          reason: pkg.reason,
        })),
      } : undefined,
    }),
  })
  const payload = await response.json() as ReplicateSwarmResponseWire & {
    path_id?: unknown
    error?: unknown
    error_detail?: unknown
    failure?: ReplicateSwarmFailureWire
  }

  if (!response.ok) {
    throw new ReplicateSwarmLaunchError(normalizeReplicateSwarmFailure(payload))
  }

  return {
    ok: Boolean(payload.ok),
    swarm: {
      id: String(payload.swarm?.id ?? '').trim(),
      name: String(payload.swarm?.name ?? '').trim(),
      mode: String(payload.swarm?.mode ?? '').trim(),
      deploymentId: String(payload.swarm?.deployment_id ?? '').trim(),
      attachStatus: String(payload.swarm?.attach_status ?? '').trim(),
      groupId: String(payload.swarm?.group_id ?? '').trim(),
      bypassPermissions: Boolean(payload.swarm?.bypass_permissions),
    },
    workspaces: Array.isArray(payload.workspaces)
      ? payload.workspaces.map((workspace) => ({
          sourceWorkspacePath: String(workspace.source_workspace_path ?? '').trim(),
          sourceWorkspaceName: String(workspace.source_workspace_name ?? '').trim(),
          link: {
            id: String(workspace.link?.id ?? '').trim(),
            targetKind: String(workspace.link?.target_kind ?? '').trim(),
            targetSwarmId: String(workspace.link?.target_swarm_id ?? '').trim(),
            targetSwarmName: String(workspace.link?.target_swarm_name ?? '').trim(),
            targetWorkspacePath: String(workspace.link?.target_workspace_path ?? '').trim(),
            replicationMode: String(workspace.link?.replication_mode ?? '').trim(),
            writable: Boolean(workspace.link?.writable),
            sync: {
              enabled: Boolean(workspace.link?.sync?.enabled),
              mode: String(workspace.link?.sync?.mode ?? '').trim(),
              modules: Array.isArray(workspace.link?.sync?.modules)
                ? workspace.link.sync.modules.map((value) => String(value ?? '').trim()).filter(Boolean)
                : [],
            },
            createdAt: typeof workspace.link?.created_at === 'number' ? workspace.link.created_at : 0,
            updatedAt: typeof workspace.link?.updated_at === 'number' ? workspace.link.updated_at : 0,
          },
        }))
      : [],
  }
}

export async function hydrateReplicationWorkspaces(): Promise<WorkspaceEntry[]> {
  return listWorkspaces()
}
