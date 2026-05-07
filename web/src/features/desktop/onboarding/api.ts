import { requestJson } from '../../../app/api'
import { debugLog, createDebugTimer } from '../../../lib/debug-log'
import { listAuthCredentials } from '../settings/queries/list-auth-credentials'
import { listProviders } from '../settings/queries/list-providers'
import { fetchVaultStatus } from '../vault/api'
import type { ContainerProfileMount } from '../swarm/types/container-mounts'
import type {
  DesktopOnboardingAuth,
  DesktopOnboardingConfig,
  DesktopOnboardingDiscoveredSwarmWire,
  DesktopOnboardingDiscoveredSwarm,
  DesktopSwarmGroupState,
  DesktopOnboardingHeuristics,
  DesktopOnboardingNetwork,
  DesktopOnboardingPairing,
  DesktopOnboardingStatus,
  DesktopOnboardingStatusWire,
  DesktopOnboardingTailscale,
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

export async function fetchDesktopOnboardingStatus(): Promise<DesktopOnboardingStatus> {
  const finish = createDebugTimer('desktop-onboarding-api', 'fetchDesktopOnboardingStatus')
  debugLog('desktop-onboarding-api', 'fetchDesktopOnboardingStatus:request')
  const [onboarding, swarmState, vault, providers, credentials] = await Promise.all([
    requestJson<DesktopOnboardingStatusWire>('/v1/onboarding'),
    fetchSwarmState(),
    fetchVaultStatus(),
    listProviders(),
    listAuthCredentials(),
  ])

  const mode = normalizeBootstrapMode(onboarding.config?.mode)
  const swarmMode = Boolean(onboarding.config?.swarm_mode)
  const child = Boolean(onboarding.config?.child)
  const tailscale = mapTailscale(onboarding)
  const lanAddresses = collectTransportValues(swarmState.node.transports, 'lan')
  const config: DesktopOnboardingConfig = {
    swarmName: String(onboarding.config?.swarm_name ?? '').trim(),
    child,
    swarmMode,
    swarmRole: !swarmMode ? 'standalone' : child ? 'child' : 'master',
    swarmID: String(swarmState.node.swarm_id ?? '').trim(),
    mode,
    host: String(onboarding.config?.host ?? '').trim(),
    port: normalizeAPIPort(onboarding.config?.port),
    desktopPort: normalizeAPIPort(onboarding.config?.desktop_port ?? 5555),
    advertiseHost: String(onboarding.config?.advertise_host ?? '').trim(),
    advertisePort: normalizeAPIPort(onboarding.config?.advertise_port ?? onboarding.config?.port),
    tailscaleURL: String(onboarding.config?.tailscale_url ?? '').trim(),
    bypassPermissions: Boolean(onboarding.config?.bypass_permissions),
    devMode: Boolean(onboarding.config?.dev_mode),
    localTransportPort: normalizeAPIPort(onboarding.config?.local_transport_port ?? 7790),
    localTransportActive: Boolean(onboarding.config?.local_transport_active),
    localTransportWarning: String(onboarding.config?.local_transport_warning ?? '').trim(),
    peerTransportPort: normalizeAPIPort(onboarding.config?.peer_transport_port ?? 7791),
    restartRequired: Boolean(onboarding.config?.restart_required),
    restartReason: String(onboarding.config?.restart_reason ?? '').trim(),
  }
  const heuristics: DesktopOnboardingHeuristics = {
    missingSwarmName: Boolean(onboarding.heuristics?.missing_swarm_name),
    credentialCount: typeof onboarding.heuristics?.credential_count === 'number' ? onboarding.heuristics.credential_count : credentials.total,
    savedWorkspaceCount: typeof onboarding.heuristics?.saved_workspace_count === 'number' ? onboarding.heuristics.saved_workspace_count : 0,
    vaultConfigured: Boolean(onboarding.heuristics?.vault_configured || vault.enabled),
  }
  const pairing: DesktopOnboardingPairing = {
    swarmID: String(swarmState.node.swarm_id ?? '').trim(),
    pairingState: String(swarmState.pairing?.pairing_state ?? '').trim(),
    parentSwarmID: String(swarmState.pairing?.parent_swarm_id ?? '').trim(),
    activeInviteID: String(swarmState.pairing?.active_invite_id ?? '').trim(),
    lastEnrollmentID: String(swarmState.pairing?.last_enrollment_id ?? '').trim(),
    lastDecision: String(swarmState.pairing?.last_decision ?? '').trim(),
    lastDecisionReason: String(swarmState.pairing?.last_decision_reason ?? '').trim(),
    lastUpdatedByRole: String(swarmState.pairing?.last_updated_by_role ?? '').trim(),
    rendezvousTransports: Array.isArray(swarmState.pairing?.rendezvous_transports)
      ? swarmState.pairing.rendezvous_transports.map(mapTransport)
      : [],
  }
  const auth: DesktopOnboardingAuth = {
    credentialCount: heuristics.credentialCount,
    activeProviders: Array.from(new Set(
      credentials.records
        .filter((record) => record.active && record.provider.trim() !== '')
        .map((record) => record.provider.trim()),
    )),
    providers,
  }
  const network: DesktopOnboardingNetwork = {
    lanAddresses,
    tailscale,
  }

  const result = {
    ok: Boolean(onboarding.ok),
    needsOnboarding: Boolean(onboarding.needs_onboarding),
    config,
    heuristics,
    pairing,
    network,
    currentGroupID: String(swarmState.current_group_id ?? '').trim(),
    groups: Array.isArray(swarmState.groups) ? swarmState.groups.map(mapGroupState) : [],
    discoveredSwarms: Array.isArray(onboarding.discovered_swarms)
      ? onboarding.discovered_swarms.map(mapDiscoveredSwarm)
      : [],
    vault,
    auth,
    workspace: {
      savedCount: heuristics.savedWorkspaceCount,
    },
  }
  finish({
    needsOnboarding: result.needsOnboarding,
    providerCount: providers.length,
    credentialCount: credentials.total,
    vaultEnabled: vault.enabled,
    vaultUnlocked: vault.unlocked,
  })
  return result
}

export async function saveDesktopOnboarding(input: SaveDesktopOnboardingInput): Promise<DesktopOnboardingStatus> {
  const payload: Record<string, unknown> = {}
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

  await requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(payload),
  })
  return fetchDesktopOnboardingStatus()
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

function normalizeBootstrapMode(mode: string | undefined): 'lan' | 'tailscale' {
  return String(mode ?? '').trim().toLowerCase() === 'tailscale' ? 'tailscale' : 'lan'
}

function normalizeAPIPort(port: number | undefined): number {
  return typeof port === 'number' && Number.isInteger(port) && port >= 1 && port <= 65535 ? port : 7781
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

function mapDiscoveredSwarm(record: DesktopOnboardingDiscoveredSwarmWire): DesktopOnboardingDiscoveredSwarm {
  return {
    id: String(record?.id ?? '').trim(),
    name: String(record?.name ?? '').trim(),
    role: String(record?.role ?? 'master').trim() || 'master',
    endpoint: String(record?.endpoint ?? '').trim(),
    tailnetURL: String(record?.tailnet_url ?? '').trim(),
    dnsName: String(record?.dns_name ?? '').trim(),
    ips: Array.isArray(record?.ips) ? record.ips.map((value) => String(value).trim()).filter((value) => value !== '') : [],
    online: Boolean(record?.online),
    source: String(record?.source ?? '').trim(),
    running: Boolean(record?.running),
    inCurrentGroup: Boolean(record?.in_current_group),
    currentRelationship: String(record?.current_relationship ?? '').trim(),
    transportMode: String(record?.transport_mode ?? '').trim(),
    rendezvousTransports: Array.isArray(record?.rendezvous_transports) ? record.rendezvous_transports.map(mapTransport) : [],
  }
}

function mapTailscale(onboarding: DesktopOnboardingStatusWire): DesktopOnboardingTailscale {
  const source = onboarding.tailscale ?? onboarding.network?.tailscale ?? {}
  return {
    available: Boolean(source.available),
    connected: Boolean(source.connected),
    dnsName: String(source.dns_name ?? '').trim(),
    tailnetName: String(source.tailnet_name ?? '').trim(),
    tailnetURL: String(source.tailnet_url ?? '').trim(),
    candidateURL: String(source.candidate_url ?? '').trim(),
    ips: Array.isArray(source.ips) ? source.ips.map((value) => String(value).trim()).filter((value) => value !== '') : [],
    authURL: String(source.auth_url ?? '').trim(),
    error: String(source.error ?? '').trim(),
    serve: {
      configured: Boolean(source.serve?.configured),
      mode: String(source.serve?.mode ?? '').trim(),
      url: String(source.serve?.url ?? '').trim(),
      proxyTarget: String(source.serve?.proxy_target ?? '').trim(),
      expectedDesktopProxy: String(source.serve?.expected_desktop_proxy ?? '').trim(),
      expectedAPIProxy: String(source.serve?.expected_api_proxy ?? '').trim(),
      expectedPeerTransportProxy: String(source.serve?.expected_peer_transport_proxy ?? '').trim(),
      error: String(source.serve?.error ?? '').trim(),
    },
  }
}

function mapGroupState(record: NonNullable<SwarmLocalState['groups']>[number]): DesktopSwarmGroupState {
  return {
    group: {
      id: String(record.group?.id ?? '').trim(),
      name: String(record.group?.name ?? '').trim(),
      networkName: String(record.group?.network_name ?? '').trim(),
      hostSwarmID: String(record.group?.host_swarm_id ?? '').trim(),
      createdAt: typeof record.group?.created_at === 'number' ? record.group.created_at : 0,
      updatedAt: typeof record.group?.updated_at === 'number' ? record.group.updated_at : 0,
    },
    members: Array.isArray(record.members)
      ? record.members.map((member) => ({
        groupID: String(member.group_id ?? '').trim(),
        swarmID: String(member.swarm_id ?? '').trim(),
        name: String(member.name ?? '').trim(),
        swarmRole: String(member.swarm_role ?? '').trim(),
        membershipRole: String(member.membership_role ?? '').trim(),
        createdAt: typeof member.created_at === 'number' ? member.created_at : 0,
        updatedAt: typeof member.updated_at === 'number' ? member.updated_at : 0,
      }))
      : [],
  }
}

function collectTransportValues(transports: DesktopOnboardingTransport[], kind: string): string[] {
  return transports
    .filter((transport) => transport.kind === kind)
    .flatMap((transport) => [transport.primary, ...transport.all])
    .map((value) => value.trim())
    .filter((value, index, array) => value !== '' && array.indexOf(value) === index)
}
