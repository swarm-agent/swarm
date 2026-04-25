import type { ProviderStatus } from '../settings/types/auth'
import type { VaultStatus } from '../vault/types'

export interface DesktopOnboardingTransportWire {
  kind?: string
  primary?: string
  all?: string[]
}

export interface DesktopOnboardingConfigWire {
  swarm_name?: string
  swarm_mode?: boolean
  child?: boolean
  mode?: string
  host?: string
  port?: number
  desktop_port?: number
  advertise_host?: string
  advertise_port?: number
  tailscale_url?: string
  bypass_permissions?: boolean
  dev_mode?: boolean
  local_transport_port?: number
  local_transport_active?: boolean
  local_transport_warning?: string
  peer_transport_port?: number
  restart_required?: boolean
  restart_reason?: string
}

export interface DesktopOnboardingHeuristicsWire {
  missing_swarm_name?: boolean
  credential_count?: number
  saved_workspace_count?: number
  vault_configured?: boolean
}

export interface DesktopOnboardingPairingWire {
  swarm_id?: string
  pairing_state?: string
  parent_swarm_id?: string
  active_invite_id?: string
  last_enrollment_id?: string
  last_decision?: string
  last_decision_reason?: string
  last_updated_by_role?: string
  rendezvous_transports?: DesktopOnboardingTransportWire[]
}

export interface DesktopOnboardingTailscaleServeWire {
  configured?: boolean
  mode?: string
  url?: string
  proxy_target?: string
  expected_desktop_proxy?: string
  expected_api_proxy?: string
  expected_peer_transport_proxy?: string
  error?: string
}

export interface DesktopOnboardingTailscaleWire {
  available?: boolean
  connected?: boolean
  dns_name?: string
  tailnet_name?: string
  tailnet_url?: string
  candidate_url?: string
  ips?: string[]
  auth_url?: string
  error?: string
  serve?: DesktopOnboardingTailscaleServeWire
}

export interface DesktopOnboardingNetworkWire {
  lan_addresses?: string[]
  tailscale?: DesktopOnboardingTailscaleWire
}

export interface DesktopOnboardingDiscoveredSwarmWire {
  id?: string
  name?: string
  role?: string
  endpoint?: string
  tailnet_url?: string
  dns_name?: string
  ips?: string[]
  online?: boolean
  source?: string
  running?: boolean
  in_current_group?: boolean
  current_relationship?: string
  transport_mode?: string
  rendezvous_transports?: DesktopOnboardingTransportWire[]
}

export interface DesktopOnboardingAuthWire {
  credential_count?: number
  active_providers?: string[]
  providers?: ProviderStatus[]
}

export interface DesktopOnboardingWorkspaceWire {
  saved_count?: number
}

export interface DesktopOnboardingStatusWire {
  ok?: boolean
  needs_onboarding?: boolean
  config?: DesktopOnboardingConfigWire
  heuristics?: DesktopOnboardingHeuristicsWire
  pairing?: DesktopOnboardingPairingWire
  network?: DesktopOnboardingNetworkWire
  tailscale?: DesktopOnboardingTailscaleWire
  discovered_swarms?: DesktopOnboardingDiscoveredSwarmWire[]
  vault?: VaultStatus
  auth?: DesktopOnboardingAuthWire
  workspace?: DesktopOnboardingWorkspaceWire
}

export interface DesktopOnboardingTransport {
  kind: string
  primary: string
  all: string[]
}

export interface DesktopOnboardingConfig {
  swarmName: string
  child: boolean
  swarmMode: boolean
  swarmRole: 'standalone' | 'master' | 'child'
  swarmID: string
  mode: 'lan' | 'tailscale'
  host: string
  port: number
  desktopPort: number
  advertiseHost: string
  advertisePort: number
  tailscaleURL: string
  bypassPermissions: boolean
  devMode: boolean
  localTransportPort: number
  localTransportActive: boolean
  localTransportWarning: string
  peerTransportPort: number
  restartRequired: boolean
  restartReason: string
}

export interface DesktopOnboardingHeuristics {
  missingSwarmName: boolean
  credentialCount: number
  savedWorkspaceCount: number
  vaultConfigured: boolean
}

export interface DesktopOnboardingPairing {
  swarmID: string
  pairingState: string
  parentSwarmID: string
  activeInviteID: string
  lastEnrollmentID: string
  lastDecision: string
  lastDecisionReason: string
  lastUpdatedByRole: string
  rendezvousTransports: DesktopOnboardingTransport[]
}

export interface DesktopOnboardingTailscaleServe {
  configured: boolean
  mode: string
  url: string
  proxyTarget: string
  expectedDesktopProxy: string
  expectedAPIProxy: string
  expectedPeerTransportProxy: string
  error: string
}

export interface DesktopOnboardingTailscale {
  available: boolean
  connected: boolean
  dnsName: string
  tailnetName: string
  tailnetURL: string
  candidateURL: string
  ips: string[]
  authURL: string
  error: string
  serve: DesktopOnboardingTailscaleServe
}

export interface DesktopOnboardingNetwork {
  lanAddresses: string[]
  tailscale: DesktopOnboardingTailscale
}

export interface DesktopOnboardingDiscoveredSwarm {
  id: string
  name: string
  role: string
  endpoint: string
  tailnetURL: string
  dnsName: string
  ips: string[]
  online: boolean
  source: string
  running: boolean
  inCurrentGroup: boolean
  currentRelationship: string
  transportMode: string
  rendezvousTransports: DesktopOnboardingTransport[]
}

export interface DesktopOnboardingAuth {
  credentialCount: number
  activeProviders: string[]
  providers: ProviderStatus[]
}

export interface DesktopOnboardingWorkspace {
  savedCount: number
}

export interface DesktopSwarmGroup {
  id: string
  name: string
  networkName: string
  hostSwarmID: string
  createdAt: number
  updatedAt: number
}

export interface DesktopSwarmGroupMember {
  groupID: string
  swarmID: string
  name: string
  swarmRole: string
  membershipRole: string
  createdAt: number
  updatedAt: number
}

export interface DesktopSwarmGroupState {
  group: DesktopSwarmGroup
  members: DesktopSwarmGroupMember[]
}

export interface DesktopOnboardingStatus {
  ok: boolean
  needsOnboarding: boolean
  config: DesktopOnboardingConfig
  heuristics: DesktopOnboardingHeuristics
  pairing: DesktopOnboardingPairing
  network: DesktopOnboardingNetwork
  currentGroupID: string
  groups: DesktopSwarmGroupState[]
  discoveredSwarms: DesktopOnboardingDiscoveredSwarm[]
  vault: VaultStatus
  auth: DesktopOnboardingAuth
  workspace: DesktopOnboardingWorkspace
}

export interface SaveDesktopOnboardingInput {
  swarmName?: string
  swarmMode?: boolean
  child?: boolean
  mode?: 'lan' | 'tailscale'
  port?: number
  advertiseHost?: string
  advertisePort?: number
  tailscaleURL?: string
  localTransportPort?: number
  peerTransportPort?: number
}
