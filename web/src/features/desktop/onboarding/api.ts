import { requestJson } from '../../../app/api'
import type { ContainerProfileMount } from '../swarm/types/container-mounts'
import type {
  DesktopOnboardingStatusWire,
  DesktopOnboardingTransport,
  DesktopOnboardingTransportWire,
  SaveDesktopOnboardingInput,
} from './types'

export interface SwarmInvite {
  id: string
  token: string
  primary_swarm_id: string
  primary_name: string
  transport_mode: string
  rendezvous_transports: DesktopOnboardingTransport[]
  expires_at: number
  consumed_at: number
  created_at: number
  updated_at: number
}

export interface SwarmEnrollment {
  id: string
  invite_id: string
  invite_token: string
  primary_swarm_id: string
  parent_swarm_id: string
  child_swarm_id: string
  child_name: string
  child_role: string
  child_public_key: string
  child_fingerprint: string
  transport_mode: string
  observed_remote_addr: string
  rendezvous_transports: DesktopOnboardingTransport[]
  status: string
  decision_reason: string
  reviewed_at: number
  created_at: number
  updated_at: number
}

export interface SwarmTrustedPeer {
  swarm_id: string
  name: string
  role: string
  public_key: string
  fingerprint: string
  relationship: string
  parent_swarm_id: string
  transport_mode: string
  rendezvous_transports: DesktopOnboardingTransport[]
  approved_at: number
  created_at: number
  updated_at: number
}

export interface SwarmLocalRuntimeStatus {
  recommended: 'podman' | 'docker' | ''
  available: Array<'podman' | 'docker'>
  installed: Array<'podman' | 'docker'>
  issues: Partial<Record<'podman' | 'docker', string>>
  warning: string
}

export interface SwarmLocalContainer {
  id: string
  name: string
  containerName: string
  runtime: string
  networkName: string
  status: string
  containerID: string
  hostAPIBaseURL: string
  hostPort: number
  runtimePort: number
  image: string
  warning: string
  mounts: ContainerProfileMount[]
  createdAt: number
  updatedAt: number
}

export interface SwarmLocalContainerDeleteItemResult {
  id: string
  name: string
  containerName: string
  deleted: boolean
  childSwarmID: string
  childDisplayName: string
  childInfoDetected: boolean
  removedDeployment: boolean
  removedTrustedPeer: boolean
  removedGroupMemberships: number
  error: string
}

export interface SwarmLocalContainerDeleteResult {
  deleted: string[]
  count: number
  failed: number
  childInfoRemoved: number
  items: SwarmLocalContainerDeleteItemResult[]
}

export interface ManagedHostRemoveResult {
  ok?: boolean
  role: string
  localRemoved: boolean
  remoteRemoved: boolean
  remoteError: string
  pairing?: {
    pairing_state?: string
    parent_swarm_id?: string
    last_updated_by_role?: string
  }
  cleanup?: {
    managed_swarm_id?: string
    removed_trusted_peer?: boolean
    removed_group_memberships?: number
  }
}

export interface SwarmLocalState {
  node: {
    swarm_id: string
    name: string
    role: string
    public_key: string
    fingerprint: string
    advertise_mode: string
    advertise_addr: string
    transports: DesktopOnboardingTransport[]
  }
  pairing: {
    pairing_state: string
    parent_swarm_id: string
    active_invite_id: string
    last_enrollment_id: string
    last_decision: string
    last_decision_reason: string
    last_updated_by_role: string
    rendezvous_transports: DesktopOnboardingTransport[]
    managed_auth_owner_swarm_id?: string
    managed_auth_snapshot_hash?: string
    managed_auth_applied_at?: number
    managed_auth_last_attempt_at?: number
    managed_auth_last_error?: string
  }
  trusted_peers: SwarmTrustedPeer[]
  current_group_id?: string
  groups?: Array<{
    group?: {
      id?: string
      name?: string
      network_name?: string
      host_swarm_id?: string
      created_at?: number
      updated_at?: number
    }
    members?: Array<{
      group_id?: string
      swarm_id?: string
      name?: string
      swarm_role?: string
      membership_role?: string
      created_at?: number
      updated_at?: number
    }>
  }>
}

export interface RemoteSwarmPairingCeremony {
  managed_swarm_id?: string
  managed_name?: string
  code?: string
  verification_only?: boolean
  child_swarm_id?: string
  child_name?: string
  auth_code?: string
}

export interface RemoteSwarmEndpointCandidate {
  kind: string
  url: string
  host: string
  port: number
  scheme: string
}

export interface RemoteSwarmCandidate {
  id: string
  source: string
  name: string
  dnsName: string
  tailnetURL: string
  endpoint: string
  endpointCandidates: RemoteSwarmEndpointCandidate[]
  ips: string[]
  os: string
  online: boolean
  transportMode: string
  rendezvousTransports: DesktopOnboardingTransport[]
}

export interface RemoteSwarmCandidatesResult {
  tailscale: {
    available: boolean
    connected: boolean
    tailnetName: string
    error: string
  }
  candidates: RemoteSwarmCandidate[]
  count: number
}

export interface RemoteSwarmPairingOffer {
  version: string
  type: string
  token: string
  single_use?: boolean
  swarm_id: string
  swarm_name: string
  public_key: string
  fingerprint: string
  endpoint: string
  endpoint_candidates?: RemoteSwarmEndpointCandidate[]
  api_port: number
  transport_mode: string
  rendezvous_transports?: DesktopOnboardingTransport[]
  expires_at: number
  created_at: number
  ceremony: {
    code: string
    verification_only?: boolean
    description?: string
  }
}

export interface RemoteSwarmPairingStartResult {
  invite?: SwarmInvite
  request: {
    request_id: string
    status: string
    managed_swarm_id: string
    managed_name: string
    managed_public_key?: string
    managed_fingerprint?: string
    ceremony_code: string
  }
  ceremony: RemoteSwarmPairingCeremony
}

export interface RemoteSwarmPendingPairing {
  request_id: string
  status: string
  manager_swarm_id?: string
  manager_name?: string
  manager_endpoint?: string
  managed_swarm_id?: string
  managed_name?: string
  managed_fingerprint?: string
  managed_endpoint?: string
  ceremony_code?: string
  transport_mode?: string
  created_at?: number
}

export interface RemoteSwarmPairingApprovalResult {
  ok?: boolean
  status: string
  request_id: string
  routing?: {
    managed_swarm_id?: string
    managed_name?: string
    backend_url?: string
    transport_mode?: string
    container_scope?: string
  }
}

export function buildDesktopOnboardingPayload(input: SaveDesktopOnboardingInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (Object.prototype.hasOwnProperty.call(input, 'username')) {
    payload.username = input.username
  }
  if (Object.prototype.hasOwnProperty.call(input, 'localOwnerConfirmation')) {
    payload.local_owner_confirmation = input.localOwnerConfirmation
  }
  if (Object.prototype.hasOwnProperty.call(input, 'swarmName')) {
    payload.swarm_name = input.swarmName
  }
  if (Object.prototype.hasOwnProperty.call(input, 'swarmMode')) {
    payload.swarm_mode = input.swarmMode
  }
  if (Object.prototype.hasOwnProperty.call(input, 'child')) {
    payload.child = input.child
  }
  if (Object.prototype.hasOwnProperty.call(input, 'mode')) {
    payload.mode = input.mode
  }
  if (Object.prototype.hasOwnProperty.call(input, 'port')) {
    payload.port = input.port
  }
  if (Object.prototype.hasOwnProperty.call(input, 'advertiseHost')) {
    payload.advertise_host = input.advertiseHost
  }
  if (Object.prototype.hasOwnProperty.call(input, 'advertisePort')) {
    payload.advertise_port = input.advertisePort
  }
  if (Object.prototype.hasOwnProperty.call(input, 'tailscaleURL')) {
    payload.tailscale_url = input.tailscaleURL
  }
  if (Object.prototype.hasOwnProperty.call(input, 'localTransportPort')) {
    payload.local_transport_port = input.localTransportPort
  }
  if (Object.prototype.hasOwnProperty.call(input, 'peerTransportPort')) {
    payload.peer_transport_port = input.peerTransportPort
  }
  return payload
}

export async function patchDesktopOnboarding(input: SaveDesktopOnboardingInput): Promise<DesktopOnboardingStatusWire> {
  return requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(buildDesktopOnboardingPayload(input)),
  })
}

export async function upgradeAccountToTeam(teamName: string): Promise<void> {
  await requestJson('/v1/account/team/upgrade', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ team_name: teamName }),
  })
}

export async function fetchSwarmLocalRuntimeStatus(): Promise<SwarmLocalRuntimeStatus> {
  const response = await requestJson<{ ok?: boolean; runtime?: {
    recommended?: string
    available?: string[]
    installed?: string[]
    issues?: Record<string, string>
    warning?: string
  } }>('/v1/swarm/containers/local/runtime')
  const runtime = response.runtime ?? {}
  return {
    recommended: runtime.recommended === 'docker' ? 'docker' : runtime.recommended === 'podman' ? 'podman' : '',
    available: Array.isArray(runtime.available)
      ? runtime.available.filter((value): value is 'podman' | 'docker' => value === 'podman' || value === 'docker')
      : [],
    installed: Array.isArray(runtime.installed)
      ? runtime.installed.filter((value): value is 'podman' | 'docker' => value === 'podman' || value === 'docker')
      : [],
    issues: runtime.issues && typeof runtime.issues === 'object'
      ? Object.fromEntries(
          Object.entries(runtime.issues)
            .filter(([key, value]) => (key === 'podman' || key === 'docker') && typeof value === 'string')
            .map(([key, value]) => [key, value.trim()])
        ) as Partial<Record<'podman' | 'docker', string>>
      : {},
    warning: String(runtime.warning ?? '').trim(),
  }
}

export async function fetchSwarmLocalContainers(): Promise<SwarmLocalContainer[]> {
  const response = await requestJson<{ ok?: boolean; containers?: any[] }>('/v1/swarm/containers/local')
  return Array.isArray(response.containers) ? response.containers.map(mapSwarmLocalContainer) : []
}

export async function createSwarmLocalContainer(input: {
  name: string
  runtime?: 'podman' | 'docker' | ''
  hostAPIBaseURL?: string
  mounts: ContainerProfileMount[]
}): Promise<SwarmLocalContainer> {
  const response = await requestJson<{ ok?: boolean; container?: any; error?: string }>('/v1/swarm/containers/local/create', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      name: input.name,
      runtime: input.runtime,
      host_api_base_url: input.hostAPIBaseURL,
      mounts: input.mounts.map((mount: ContainerProfileMount) => ({
        source_path: mount.sourcePath,
        target_path: mount.targetPath,
        mode: mount.mode,
        workspace_path: mount.workspacePath,
        workspace_name: mount.workspaceName,
      })),
    }),
  })
  if (!response.container) {
    throw new Error(response.error || 'local container creation response was missing container data')
  }
  return mapSwarmLocalContainer(response.container)
}

export async function actOnSwarmLocalContainer(input: { id: string; action: 'start' | 'stop' }): Promise<SwarmLocalContainer> {
  const response = await requestJson<{ ok?: boolean; container?: any; error?: string }>('/v1/swarm/containers/local/action', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ id: input.id, action: input.action }),
  })
  if (!response.container) {
    throw new Error(response.error || 'local container action response was missing container data')
  }
  return mapSwarmLocalContainer(response.container)
}

export async function deleteSwarmLocalContainers(ids: string[]): Promise<SwarmLocalContainerDeleteResult> {
  const response = await requestJson<{ ok?: boolean; result?: any; error?: string }>('/v1/swarm/containers/local/delete', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ ids }),
  })
  if (!response.result) {
    throw new Error(response.error || 'local container delete response was missing result data')
  }
  return mapSwarmLocalContainerDeleteResult(response.result)
}

export async function removeManagedHostLink(input: {
  managedSwarmID?: string
  managerSwarmID?: string
  endpoint?: string
  transportMode?: string
  rendezvousTransports?: DesktopOnboardingTransport[]
  propagate?: boolean
  reason?: string
}): Promise<ManagedHostRemoveResult> {
  const response = await requestJson<any>('/v1/swarm/managed-host/remove', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      managed_swarm_id: input.managedSwarmID,
      manager_swarm_id: input.managerSwarmID,
      endpoint: input.endpoint,
      transport_mode: input.transportMode,
      rendezvous_transports: input.rendezvousTransports,
      propagate: input.propagate,
      reason: input.reason,
    }),
  })
  return {
    ok: Boolean(response?.ok),
    role: String(response?.role ?? '').trim(),
    localRemoved: Boolean(response?.local_removed),
    remoteRemoved: Boolean(response?.remote_removed),
    remoteError: String(response?.remote_error ?? '').trim(),
    pairing: response?.pairing,
    cleanup: response?.cleanup,
  }
}

export async function pruneMissingSwarmLocalContainers(): Promise<SwarmLocalContainerDeleteResult> {
  const response = await requestJson<{ ok?: boolean; result?: any; error?: string }>('/v1/swarm/containers/local/prune-missing', {
    method: 'POST',
  })
  if (!response.result) {
    throw new Error(response.error || 'local container prune response was missing result data')
  }
  return mapSwarmLocalContainerDeleteResult(response.result)
}

export async function createSwarmInvite(ttlSeconds = 900): Promise<SwarmInvite> {
  const response = await requestJson<{ ok?: boolean; invite?: SwarmInvite }>('/v1/swarm/invites', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ ttl_seconds: ttlSeconds }),
  })
  if (!response.invite) {
    throw new Error('invite creation response was missing invite data')
  }
  return response.invite
}

export async function fetchRemoteSwarmCandidates(): Promise<RemoteSwarmCandidatesResult> {
  const response = await requestJson<{
    ok?: boolean
    tailscale?: { available?: boolean; connected?: boolean; tailnet_name?: string; error?: string }
    candidates?: any[]
    count?: number
  }>('/v1/swarm/remote-candidates')
  const candidates = Array.isArray(response.candidates) ? response.candidates : []
  return {
    tailscale: {
      available: Boolean(response.tailscale?.available),
      connected: Boolean(response.tailscale?.connected),
      tailnetName: String(response.tailscale?.tailnet_name ?? '').trim(),
      error: String(response.tailscale?.error ?? '').trim(),
    },
    candidates: candidates.map(mapRemoteSwarmCandidate),
    count: typeof response.count === 'number' ? response.count : candidates.length,
  }
}

export async function fetchPendingRemoteSwarmPairings(): Promise<RemoteSwarmPendingPairing[]> {
  const response = await requestJson<{ ok?: boolean; items?: RemoteSwarmPendingPairing[]; count?: number }>('/v1/swarm/remote-pairing/pending')
  return Array.isArray(response.items) ? response.items : []
}

export async function approveRemoteSwarmPairing(input: {
  requestID: string
  approve: boolean
  confirmed?: boolean
  ceremonyCode?: string
  reason?: string
}): Promise<RemoteSwarmPairingApprovalResult> {
  const response = await requestJson<RemoteSwarmPairingApprovalResult>('/v1/swarm/remote-pairing/approve', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      request_id: input.requestID,
      approve: input.approve,
      confirmed: input.confirmed,
      ceremony_code: input.ceremonyCode,
      reason: input.reason,
    }),
  })
  return response
}

export async function startRemoteSwarmPairing(input: {
  endpoint?: string
  dnsName?: string
  ips?: string[]
  groupID?: string
  managedSwarmID?: string
  managedName?: string
  offer?: RemoteSwarmPairingOffer
  ceremonyCode?: string
  rendezvousTransports?: DesktopOnboardingTransport[]
}): Promise<RemoteSwarmPairingStartResult> {
  const response = await requestJson<{ ok?: boolean; invite?: SwarmInvite; request?: RemoteSwarmPairingStartResult['request']; ceremony?: RemoteSwarmPairingCeremony }>('/v1/swarm/remote-pairing/start', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      endpoint: input.endpoint,
      dns_name: input.dnsName,
      ips: input.ips,
      group_id: input.groupID,
      managed_swarm_id: input.managedSwarmID,
      managed_name: input.managedName,
      offer: input.offer,
      ceremony_code: input.ceremonyCode,
      rendezvous_transports: input.rendezvousTransports,
    }),
  })
  if (!response.request) {
    throw new Error('remote pairing response was missing managed pairing request data')
  }
  if (!response.ceremony) {
    throw new Error('remote pairing response was missing ceremony data')
  }
  return {
    invite: response.invite,
    request: response.request,
    ceremony: response.ceremony,
  }
}

export async function submitSwarmEnrollment(input: {
  inviteToken: string
  primarySwarmID?: string
  childSwarmID?: string
  childName?: string
  childRole?: string
  childPublicKey?: string
  transportMode?: string
  rendezvousTransports?: DesktopOnboardingTransport[]
}): Promise<SwarmEnrollment> {
  const response = await requestJson<{ ok?: boolean; enrollment?: SwarmEnrollment }>('/v1/swarm/enroll', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      invite_token: input.inviteToken,
      primary_swarm_id: input.primarySwarmID,
      child_swarm_id: input.childSwarmID,
      child_name: input.childName,
      child_role: input.childRole,
      child_public_key: input.childPublicKey,
      transport_mode: input.transportMode,
      rendezvous_transports: input.rendezvousTransports,
    }),
  })
  if (!response.enrollment) {
    throw new Error('enrollment response was missing enrollment data')
  }
  return response.enrollment
}

export async function fetchPendingSwarmEnrollments(): Promise<SwarmEnrollment[]> {
  const response = await requestJson<{ ok?: boolean; items?: SwarmEnrollment[] }>('/v1/swarm/pending-children')
  return Array.isArray(response.items) ? response.items : []
}

export async function decideSwarmEnrollment(enrollmentID: string, approve: boolean, reason = ''): Promise<{ enrollment: SwarmEnrollment; trustedPeers: SwarmTrustedPeer[] }> {
  const action = approve ? 'approve' : 'reject'
  const response = await requestJson<{ ok?: boolean; enrollment?: SwarmEnrollment; trusted_peers?: SwarmTrustedPeer[] }>(`/v1/swarm/enrollment/${encodeURIComponent(enrollmentID)}/${action}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ approve, reason }),
  })
  if (!response.enrollment) {
    throw new Error('enrollment decision response was missing enrollment data')
  }
  return {
    enrollment: response.enrollment,
    trustedPeers: Array.isArray(response.trusted_peers) ? response.trusted_peers : [],
  }
}

export async function fetchSwarmState(): Promise<SwarmLocalState> {
  const response = await requestJson<{ ok?: boolean; state?: SwarmLocalState }>('/v1/swarm/state')
  if (!response.state) {
    throw new Error('swarm state response was missing state data')
  }
  return response.state
}

function mapTransport(record: DesktopOnboardingTransportWire): DesktopOnboardingTransport {
  return {
    kind: String(record.kind ?? '').trim(),
    primary: String(record.primary ?? '').trim(),
    all: Array.isArray(record.all)
      ? record.all.map((value) => String(value).trim()).filter((value) => value !== '')
      : [],
  }
}

function mapRemoteSwarmEndpointCandidate(record: any): RemoteSwarmEndpointCandidate {
  return {
    kind: String(record?.kind ?? '').trim(),
    url: String(record?.url ?? '').trim(),
    host: String(record?.host ?? '').trim(),
    port: typeof record?.port === 'number' ? record.port : 0,
    scheme: String(record?.scheme ?? '').trim(),
  }
}

function mapRemoteSwarmCandidate(record: any): RemoteSwarmCandidate {
  return {
    id: String(record?.id ?? '').trim(),
    source: String(record?.source ?? '').trim(),
    name: String(record?.name ?? '').trim(),
    dnsName: String(record?.dns_name ?? '').trim(),
    tailnetURL: String(record?.tailnet_url ?? '').trim(),
    endpoint: String(record?.endpoint ?? '').trim(),
    endpointCandidates: Array.isArray(record?.endpoint_candidates) ? record.endpoint_candidates.map(mapRemoteSwarmEndpointCandidate) : [],
    ips: Array.isArray(record?.ips) ? record.ips.map((value: unknown) => String(value ?? '').trim()).filter((value: string) => value !== '') : [],
    os: String(record?.os ?? '').trim(),
    online: Boolean(record?.online),
    transportMode: String(record?.transport_mode ?? '').trim(),
    rendezvousTransports: Array.isArray(record?.rendezvous_transports) ? record.rendezvous_transports.map(mapTransport) : [],
  }
}

function mapSwarmLocalContainer(record: any): SwarmLocalContainer {
  return {
    id: String(record?.id ?? '').trim(),
    name: String(record?.name ?? '').trim(),
    containerName: String(record?.container_name ?? '').trim(),
    runtime: String(record?.runtime ?? '').trim(),
    networkName: String(record?.network_name ?? '').trim(),
    status: String(record?.status ?? '').trim(),
    containerID: String(record?.container_id ?? '').trim(),
    hostAPIBaseURL: String(record?.host_api_base_url ?? '').trim(),
    hostPort: typeof record?.host_port === 'number' ? record.host_port : 0,
    runtimePort: typeof record?.runtime_port === 'number' ? record.runtime_port : 0,
    image: String(record?.image ?? '').trim(),
    warning: String(record?.warning ?? '').trim(),
    mounts: Array.isArray(record?.mounts) ? record.mounts.map((mount: any) => ({
      sourcePath: String(mount?.source_path ?? '').trim(),
      targetPath: String(mount?.target_path ?? '').trim(),
      mode: String(mount?.mode ?? '').trim() === 'ro' ? 'ro' : 'rw',
      workspacePath: String(mount?.workspace_path ?? '').trim(),
      workspaceName: String(mount?.workspace_name ?? '').trim(),
    })) : [],
    createdAt: typeof record?.created_at === 'number' ? record.created_at : 0,
    updatedAt: typeof record?.updated_at === 'number' ? record.updated_at : 0,
  }
}

function mapSwarmLocalContainerDeleteResult(record: any): SwarmLocalContainerDeleteResult {
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
