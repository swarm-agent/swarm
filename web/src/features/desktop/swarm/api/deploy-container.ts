import { apiFetch, requestJson, readErrorMessage } from '../../../../app/api'
import type { ContainerProfileMount } from '../types/container-mounts'
import type { SwarmLocalContainerDeleteResult } from '../../onboarding/api'

export interface DeployContainerWorkspaceBootstrapDirectory {
  source_path: string
  target_path: string
}

export interface DeployContainerWorkspaceBootstrap {
  source_workspace_path: string
  source_workspace_name: string
  target_workspace_path: string
  theme_id?: string
  directories?: DeployContainerWorkspaceBootstrapDirectory[]
  replication_mode?: string
  writable: boolean
  sync?: {
    enabled?: boolean
    mode?: string
  }
  make_current?: boolean
}

export interface DeployContainerPackageSelection {
  name: string
  source?: 'recommended' | 'user_added' | 'workspace_scan'
  reason?: string
}

export interface DeployContainerPackageManifest {
  base_image?: string
  package_manager?: string
  packages?: DeployContainerPackageSelection[]
}

export interface DeployContainerPackageDefaults {
  baseImage: string
  packageManager: string
}

export interface RemoteDeployPayloadDirectory {
  source_path?: string
  target_path?: string
}

export interface DeployContainerDeployment {
  id: string
  kind: string
  name: string
  status: string
  runtime: string
  group_id?: string
  group_name?: string
  group_network_name?: string
  sync_enabled?: boolean
  sync_mode?: string
  sync_owner_swarm_id?: string
  container_name?: string
  container_id?: string
  host_api_base_url?: string
  backend_host_port: number
  desktop_host_port: number
  image?: string
  attach_status?: string
  last_attach_error?: string
  bootstrap_secret_sent: boolean
  bypass_permissions?: boolean
  always_on?: boolean
  child_swarm_id?: string
  child_display_name?: string
  child_backend_url?: string
  child_desktop_url?: string
  workspace_bootstrap?: DeployContainerWorkspaceBootstrap[]
  container_packages?: DeployContainerPackageManifest
  created_at: number
  updated_at: number
}

export interface DeployContainerAttachState {
  deployment_id: string
  attach_status: string
  child_swarm_id?: string
  child_display_name?: string
  child_backend_url?: string
  child_desktop_url?: string
  child_fingerprint?: string
  host_swarm_id?: string
  host_display_name?: string
  host_public_key?: string
  host_fingerprint?: string
  host_backend_url?: string
  host_desktop_url?: string
  group_id?: string
  group_name?: string
  bootstrap_secret_expires_at?: number
  last_error?: string
  decided_at?: number
  updated_at: number
}

export interface RemoteDeployPayload {
  id: string
  source_path?: string
  workspace_path?: string
  workspace_name?: string
  target_path?: string
  mode?: string
  directories?: RemoteDeployPayloadDirectory[]
  git_root?: string
  archive_name?: string
  included_files: number
  included_bytes: number
  excluded_note?: string
}

export interface RemoteDeployDiskInfo {
  path?: string
  available_bytes?: number
  required_bytes?: number
}

export interface RemoteDeployPreflight {
  path_id: string
  builder_runtime?: string
  remote_runtime?: string
  image_delivery_mode?: 'archive' | 'registry'
  image_prefix?: string
  requested_remote_runtime?: 'docker' | 'podman'
  ssh_reachable: boolean
  systemd_available: boolean
  systemd_unit?: string
  remote_root?: string
  remote_network_candidates?: string[]
  remote_disk?: RemoteDeployDiskInfo
  files_to_copy?: string[]
  payloads?: RemoteDeployPayload[]
  summary?: string
  checks?: string[]
}

export interface RemoteDeployTiming {
  step: string
  elapsed_ms: number
  status?: string
  fields?: Record<string, string>
}

export interface RemoteDeploySession {
  id: string
  name: string
  status: string
  ssh_session_target?: string
  transport_mode?: 'lan' | 'tailscale'
  master_endpoint?: string
  remote_endpoint?: string
  remote_advertise_host?: string
  group_id?: string
  group_name?: string
  builder_runtime?: string
  remote_runtime?: string
  image_delivery_mode?: 'archive' | 'registry'
  image_prefix?: string
  master_tailscale_url?: string
  remote_auth_url?: string
  remote_tailnet_url?: string
  enrollment_id?: string
  enrollment_status?: string
  child_swarm_id?: string
  child_name?: string
  host_swarm_id?: string
  host_name?: string
  host_public_key?: string
  host_fingerprint?: string
  host_api_base_url?: string
  host_desktop_url?: string
  bypass_permissions?: boolean
  always_on?: boolean
  sync_enabled?: boolean
  sync_mode?: string
  sync_owner_swarm_id?: string
  container_packages?: DeployContainerPackageManifest
  last_progress?: string
  last_error?: string
  last_remote_output?: string
  start_timings?: RemoteDeployTiming[]
  preflight: RemoteDeployPreflight
  created_at: number
  updated_at: number
  approved_at?: number
  attached_at?: number
}

export async function fetchDeployContainers(): Promise<DeployContainerDeployment[]> {
  const response = await requestJson<{ ok?: boolean; deployments?: DeployContainerDeployment[] }>('/v1/deploy/container')
  return Array.isArray(response.deployments) ? response.deployments : []
}

export async function createDeployContainer(input: {
  name: string
  runtime?: 'podman' | 'docker' | ''
  image?: string
  groupID: string
  groupName?: string
  groupNetworkName?: string
  syncEnabled?: boolean
  syncVaultPassword?: string
  bypassPermissions?: boolean
  alwaysOn?: boolean
  mounts: ContainerProfileMount[]
}): Promise<DeployContainerDeployment> {
  const response = await requestJson<{ ok?: boolean; deployment?: DeployContainerDeployment }>('/v1/deploy/container/create', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      name: input.name,
      runtime: input.runtime,
      image: input.image,
      group_id: input.groupID,
      group_name: input.groupName,
      group_network_name: input.groupNetworkName,
      sync_enabled: input.syncEnabled,
      sync_vault_password: input.syncVaultPassword,
      bypass_permissions: input.bypassPermissions,
      always_on: input.alwaysOn,
      mounts: input.mounts.map((mount) => ({
        source_path: mount.sourcePath,
        target_path: mount.targetPath,
        mode: mount.mode,
        workspace_path: mount.workspacePath,
        workspace_name: mount.workspaceName,
      })),
    }),
  })
  if (!response.deployment) {
    throw new Error('deployment creation response was missing deployment data')
  }
  return response.deployment
}

export async function actOnDeployContainer(input: {
  id: string
  action: 'start' | 'stop'
}): Promise<DeployContainerDeployment> {
  const response = await requestJson<{ ok?: boolean; deployment?: DeployContainerDeployment }>('/v1/deploy/container/action', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      id: input.id,
      action: input.action,
    }),
  })
  if (!response.deployment) {
    throw new Error('deployment action response was missing deployment data')
  }
  return response.deployment
}

export async function deleteDeployContainers(ids: string[]): Promise<SwarmLocalContainerDeleteResult> {
  const response = await requestJson<{ ok?: boolean; result?: any; error?: string }>('/v1/deploy/container/delete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ ids }),
  })
  if (!response.result) {
    throw new Error(response.error || 'deployment delete response was missing result data')
  }
  return mapDeleteResult(response.result)
}

export async function deleteDeployContainersViaHost(childBackendURL: string, ids: string[]): Promise<SwarmLocalContainerDeleteResult> {
  const target = String(childBackendURL ?? '').trim()
  if (!target) {
    throw new Error('child backend url is required')
  }
  const response = await apiFetch(`/v1/swarm/targets/proxy/v1/deploy/container/delete?swarm_id=${encodeURIComponent(target)}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ ids }),
  })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  const payload = await response.json() as { ok?: boolean; result?: any; error?: string }
  if (!payload.result) {
    throw new Error(payload.error || 'deployment delete response was missing result data')
  }
  return mapDeleteResult(payload.result)
}


export async function fetchRemoteDeploySessions(options?: {
  refresh?: boolean
  id?: string
}): Promise<RemoteDeploySession[]> {
  const params = new URLSearchParams()
  if (options?.refresh) {
    params.set('refresh', '1')
  }
  if (options?.id?.trim()) {
    params.set('id', options.id.trim())
  }
  const query = params.toString()
  const response = await requestJson<{ ok?: boolean; session?: RemoteDeploySession; sessions?: RemoteDeploySession[] }>(`/v1/deploy/remote/session${query ? `?${query}` : ''}`)
  if (response.session) {
    return [response.session]
  }
  return Array.isArray(response.sessions) ? response.sessions : []
}

export async function fetchRemoteDeploySession(sessionID: string, options?: {
  refresh?: boolean
}): Promise<RemoteDeploySession> {
  const sessions = await fetchRemoteDeploySessions({ id: sessionID, refresh: options?.refresh })
  const session = sessions.find((item) => item.id === sessionID) ?? sessions[0]
  if (!session) {
    throw new Error('remote deploy session response was missing session data')
  }
  return session
}

export interface RemoteDeployPreflightError {
  ok?: boolean
  path_id?: string
  error?: string
  session?: RemoteDeploySession
}

export interface RemoteDeployStartError {
  ok?: boolean
  path_id?: string
  error?: string
  session?: RemoteDeploySession
}

export async function createRemoteDeploySession(input: {
  name: string
  sshSessionTarget: string
  transportMode?: 'lan' | 'tailscale'
  remoteAdvertiseHost?: string
  groupID: string
  groupName?: string
  remoteRuntime?: 'docker' | 'podman'
  imageDeliveryMode?: 'archive' | 'registry'
  syncEnabled?: boolean
  bypassPermissions?: boolean
  alwaysOn?: boolean
  containerPackages?: DeployContainerPackageManifest
  payloads: Array<{
    sourcePath: string
    workspacePath?: string
    workspaceName?: string
    targetPath?: string
    mode?: 'ro' | 'rw'
    directories?: Array<{
      sourcePath: string
      targetPath?: string
    }>
  }>
}): Promise<RemoteDeploySession> {
  const response = await apiFetch('/v1/deploy/remote/session/create', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      name: input.name,
      ssh_session_target: input.sshSessionTarget,
      transport_mode: input.transportMode,
      remote_advertise_host: input.remoteAdvertiseHost,
      group_id: input.groupID,
      group_name: input.groupName,
      remote_runtime: input.remoteRuntime,
      image_delivery_mode: input.imageDeliveryMode,
      sync_enabled: input.syncEnabled,
      bypass_permissions: input.bypassPermissions,
      always_on: input.alwaysOn,
      container_packages: input.containerPackages ? {
        base_image: input.containerPackages.base_image,
        package_manager: input.containerPackages.package_manager,
        packages: (input.containerPackages.packages ?? []).map((pkg) => ({
          name: pkg.name,
          source: pkg.source,
          reason: pkg.reason,
        })),
      } : undefined,
      payloads: input.payloads.map((payload) => ({
        source_path: payload.sourcePath,
        workspace_path: payload.workspacePath,
        workspace_name: payload.workspaceName,
        target_path: payload.targetPath,
        mode: payload.mode,
        directories: (payload.directories ?? []).map((directory) => ({
          source_path: directory.sourcePath,
          target_path: directory.targetPath,
        })),
      })),
    }),
  })
  const payload = await response.json() as { ok?: boolean; session?: RemoteDeploySession; error?: string }
  if (!response.ok) {
    const error = typeof payload.error === 'string' && payload.error.trim().length > 0
      ? payload.error.trim()
      : `Request failed with status ${response.status}`
    const wrapped = new Error(error) as Error & { remotePreflight?: RemoteDeployPreflightError }
    wrapped.remotePreflight = {
      ok: payload.ok,
      path_id: 'deploy.remote.create.v1',
      error,
      session: payload.session,
    }
    throw wrapped
  }
  if (!payload.session) {
    throw new Error('remote deploy session response was missing session data')
  }
  return payload.session
}

export async function startRemoteDeploySession(sessionID: string, input?: {
  tailscaleAuthKey?: string
  syncVaultPassword?: string
}): Promise<RemoteDeploySession> {
  const response = await apiFetch('/v1/deploy/remote/session/start', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      session_id: sessionID,
      tailscale_auth_key: input?.tailscaleAuthKey,
      sync_vault_password: input?.syncVaultPassword,
    }),
  })
  const text = await response.text()
  let payload: { ok?: boolean; path_id?: string; session?: RemoteDeploySession; error?: string } = {}
  if (text.trim()) {
    try {
      payload = JSON.parse(text) as { ok?: boolean; path_id?: string; session?: RemoteDeploySession; error?: string }
    } catch {
      payload = { error: text.trim() }
    }
  }
  if (!response.ok) {
    const error = typeof payload.error === 'string' && payload.error.trim().length > 0
      ? payload.error.trim()
      : `Request failed with status ${response.status}`
    const wrapped = new Error(error) as Error & { remoteStart?: RemoteDeployStartError }
    wrapped.remoteStart = {
      ok: payload.ok,
      path_id: payload.path_id || 'deploy.remote.start.v1',
      error,
      session: payload.session,
    }
    throw wrapped
  }
  if (!payload.session) {
    throw new Error('remote deploy start response was missing session data')
  }
  return payload.session
}

export async function deleteRemoteDeploySessions(input: {
  ids?: string[]
  childSwarmIDs?: string[]
  teardownRemote?: boolean
}): Promise<SwarmLocalContainerDeleteResult> {
  const response = await requestJson<{ ok?: boolean; result?: any; error?: string }>('/v1/deploy/remote/session/delete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      ids: input.ids ?? [],
      child_swarm_ids: input.childSwarmIDs ?? [],
      teardown_remote: input.teardownRemote,
    }),
  })
  if (!response.result) {
    throw new Error(response.error || 'remote deploy delete response was missing result data')
  }
  return mapDeleteResult(response.result)
}

function mapDeleteResult(record: any): SwarmLocalContainerDeleteResult {
  return {
    deleted: Array.isArray(record?.deleted) ? record.deleted.map((value: unknown) => String(value ?? '').trim()).filter((value: string) => value !== '') : [],
    count: typeof record?.count === 'number' ? record.count : 0,
    failed: typeof record?.failed === 'number' ? record.failed : 0,
    childInfoRemoved: typeof record?.child_info_removed === 'number' ? record.child_info_removed : 0,
    items: Array.isArray(record?.items) ? record.items.map((item: any) => ({
      id: String(item?.id ?? '').trim(),
      name: String(item?.name ?? '').trim(),
      containerName: String(item?.container_name ?? '').trim(),
      deleted: Boolean(item?.deleted),
      childSwarmID: String(item?.child_swarm_id ?? '').trim(),
      childDisplayName: String(item?.child_display_name ?? '').trim(),
      childInfoDetected: Boolean(item?.child_info_detected),
      removedDeployment: Boolean(item?.removed_deployment),
      removedTrustedPeer: Boolean(item?.removed_trusted_peer),
      removedGroupMemberships: typeof item?.removed_group_memberships === 'number' ? item.removed_group_memberships : 0,
      error: String(item?.error ?? '').trim(),
    })) : [],
  }
}

export async function fetchDeployContainerPackageDefaults(): Promise<DeployContainerPackageDefaults> {
  const response = await requestJson<{ ok?: boolean; base_image?: string; package_manager?: string }>(
    '/v1/deploy/container/package/defaults',
  )
  return {
    baseImage: String(response.base_image ?? '').trim(),
    packageManager: String(response.package_manager ?? '').trim(),
  }
}

export async function validateDeployContainerPackage(packageName: string): Promise<{ packageName: string; valid: boolean }> {
  const response = await requestJson<{ ok?: boolean; package_name?: string; valid?: boolean }>(
    '/v1/deploy/container/package/validate',
    {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ package_name: packageName }),
    },
  )
  return {
    packageName: String(response.package_name ?? '').trim(),
    valid: Boolean(response.valid),
  }
}

export async function suggestDeployContainerPackages(workspacePaths: string[]): Promise<DeployContainerPackageSelection[]> {
  const normalized = Array.from(new Set((workspacePaths ?? []).map((value) => String(value ?? '').trim()).filter((value) => value.length > 0)))
  if (normalized.length === 0) {
    return []
  }
  const response = await requestJson<{ ok?: boolean; packages?: DeployContainerPackageSelection[] }>(
    '/v1/deploy/container/package/suggest',
    {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ workspace_paths: normalized }),
    },
  )
  return Array.isArray(response.packages)
    ? response.packages.map((pkg) => ({
        name: String(pkg?.name ?? '').trim().toLowerCase(),
        source: pkg?.source,
        reason: String(pkg?.reason ?? '').trim(),
      })).filter((pkg) => pkg.name.length > 0)
    : []
}

export async function approveRemoteDeploySession(sessionID: string): Promise<RemoteDeploySession> {
  const response = await requestJson<{ ok?: boolean; session?: RemoteDeploySession }>(`/v1/deploy/remote/session/${encodeURIComponent(sessionID)}/approve`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({}),
  })
  if (!response.session) {
    throw new Error('remote deploy approve response was missing session data')
  }
  return response.session
}
