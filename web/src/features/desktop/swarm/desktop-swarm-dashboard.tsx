import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { Boxes, CheckSquare, Link2, Monitor, Plus, Trash2, TriangleAlert } from 'lucide-react'
import { Badge } from '../../../components/ui/badge'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { Input } from '../../../components/ui/input'
import { ModalCloseButton } from '../../../components/ui/modal-close-button'
import { Select } from '../../../components/ui/select'
import { fetchSwarmState, type SwarmLocalState } from './api/swarm-state'
import {
  actOnSwarmLocalContainer,
  deleteSwarmLocalContainers,
  fetchSwarmLocalContainers,
  fetchSwarmLocalRuntimeStatus,
  pruneMissingSwarmLocalContainers,
  type SwarmLocalContainer,
  type SwarmLocalContainerDeleteResult,
  type SwarmLocalRuntimeStatus,
} from './api/local-containers'
import {
  approveRemoteSwarmPairing,
  fetchPendingRemoteSwarmPairings,
  removeManagedHostLink,
  type RemoteSwarmPendingPairing,
} from './api/remote-pairing'
import type { DesktopOnboardingStatus, DesktopSwarmGroupMember, DesktopSwarmGroupState, DesktopOnboardingConfig, DesktopOnboardingNetwork, DesktopOnboardingPairing } from './types/dashboard-status'
import { uiSettingsQueryKey } from '../../queries/query-options'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveLocalContainerUpdateWarningDismissal } from '../settings/swarm/mutations/save-local-container-update-warning-dismissal'
import { saveSwarmSettings } from '../settings/swarm/mutations/save-swarm-settings'
import { localContainerUpdateWarningDismissed, normalizeSwarmSettings, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { AddSwarmModal } from './components/add-swarm-modal'
import { fetchSwarmTargets, type SwarmTarget } from './api/swarm-targets'
import { deleteSwarmMirrorResources, fetchSwarmMirrorResources, type SwarmMirrorResource, type SwarmMirrorResources, type SwarmMirrorWorkspaceResource } from './api/swarm-mirror'
import { LinkSwarmModal } from './components/link-swarm-modal'
import { ManagedHostLinkRequestModal, activePendingPairings, managedHostTargetFromPairingResult } from './components/managed-host-link-request-modal'
import { fetchFlows, updateFlow, type FlowSummaryRecord } from '../settings/flows/api'
import {
  type DeployContainerDeployment,
  type DeployContainerWorkspaceBootstrap,
  type RemoteDeployPayload,
  type RemoteDeploySession,
  actOnDeployContainer,
  updateDeployContainerSettings,
  deleteDeployContainers,
  deleteDeployContainersViaHost,
  deleteManagedHostLocalContainersViaManager,
  deleteRemoteDeploySessions,
  fetchDeployContainers,
  fetchRemoteDeploySessions,
} from './api/deploy-container'

function localBindLabel(host: string | null | undefined): string {
  const normalized = String(host ?? '').trim() || '127.0.0.1'
  return normalized === '127.0.0.1' || normalized === 'localhost' ? 'Local only' : 'Network reachable'
}

function usesTailscaleAsPrimaryTransport(status: DesktopOnboardingStatus | null): boolean {
  if (!status) {
    return false
  }
  return status.config.mode === 'tailscale'
}

function tailscaleServeStatus(status: DesktopOnboardingStatus | null): { summary: string; detail: string; tone: 'live' | 'warning' | 'neutral'; badge: string } {
  if (!status) {
    return { summary: 'Checking Tailscale Serve', detail: 'Checking whether this Swarm desktop is served on the tailnet.', tone: 'neutral', badge: 'Checking' }
  }
  const tailscale = status.network.tailscale
  const serve = tailscale.serve
  if (serve.ready && serve.mode === 'desktop') {
    return { summary: 'Hosted on Tailscale', detail: 'Verified with Tailscale Serve status. The tailnet link opens this Swarm desktop and backend API.', tone: 'live', badge: 'Serve verified' }
  }
  if (serve.ready && serve.mode === 'api') {
    return { summary: 'Backend API only', detail: 'Verified with Tailscale Serve status. The tailnet link reaches the backend API, not the desktop UI.', tone: 'warning', badge: 'API verified' }
  }
  if (serve.error) {
    return { summary: 'Serve status unavailable', detail: serve.error, tone: 'warning', badge: 'Check failed' }
  }
  if (serve.configured) {
    return { summary: 'Serve points somewhere else', detail: serve.proxyTarget ? `Tailscale Serve points to ${serve.proxyTarget}, not this Swarm desktop/API.` : 'Tailscale Serve is configured, but not for this Swarm desktop/API.', tone: 'warning', badge: 'Wrong target' }
  }
  const hasTailnetURL = Boolean(status.config.tailscaleURL || tailscale.tailnetURL || tailscale.candidateURL || tailscale.dnsName)
  if (hasTailnetURL) {
    return { summary: 'Not hosted yet', detail: 'A tailnet URL is available, but Tailscale Serve status does not show this desktop/API being served yet.', tone: 'neutral', badge: 'Not served' }
  }
  if (tailscale.connected) {
    return { summary: 'Not hosted yet', detail: 'Tailscale is connected. Run the Host Swarm command to publish this desktop on the tailnet.', tone: 'neutral', badge: 'Tailscale connected' }
  }
  return { summary: 'Tailscale not detected', detail: tailscale.error || 'No tailnet URL or active Tailscale connection was reported for this host.', tone: 'neutral', badge: 'Not detected' }
}

function tailscaleTransportCandidate(status: DesktopOnboardingStatus | null): { url: string; connected: boolean; available: boolean } {
  const url = status?.config.tailscaleURL
    || status?.network.tailscale.tailnetURL
    || (status?.network.tailscale.dnsName ? `https://${status.network.tailscale.dnsName}` : '')
  const connected = Boolean(status?.network.tailscale.connected)
  const available = Boolean(url || status?.network.tailscale.available || connected || status?.network.tailscale.authURL)
  return { url, connected, available }
}

function formatUnderscoreLabel(value: string | null | undefined): string {
  return String(value ?? '').replace(/_/g, ' ')
}

function hostnameFromURL(raw: string | null | undefined): string {
  const value = String(raw ?? '').trim()
  if (!value) {
    return ''
  }
  try {
    const parsed = new URL(value.includes('://') ? value : `https://${value}`)
    return parsed.hostname.trim()
  } catch {
    return ''
  }
}

function emptyOnboardingNetwork(): DesktopOnboardingNetwork {
  return {
    lanAddresses: [],
    tailscale: {
      available: false,
      connected: false,
      dnsName: '',
      tailnetName: '',
      tailnetURL: '',
      candidateURL: '',
      ips: [],
      authURL: '',
      error: '',
      serve: {
        configured: false,
        ready: false,
        mode: '',
        url: '',
        proxyTarget: '',
        expectedDesktopProxy: '',
        expectedAPIProxy: '',
        expectedPeerTransportProxy: '',
        command: '',
        error: '',
      },
    },
  }
}

function onboardingConfigFromDashboardState(state: SwarmLocalState, settings: UISettingsWire | null): DesktopOnboardingConfig {
  const advertise = String(state.node.advertise_addr ?? '').trim()
  const advertiseURL = hostnameFromURL(advertise)
  const swarmRole = String(state.node.role ?? '').trim().toLowerCase()
  const configRole: DesktopOnboardingConfig['swarmRole'] = swarmRole === 'managed'
    ? 'managed'
    : swarmRole === 'child'
      ? 'child'
      : swarmRole === 'standalone'
        ? 'standalone'
        : 'master'

  return {
    swarmName: String(state.node.name ?? '').trim() || settings?.swarm?.name?.trim() || 'Local swarm',
    child: configRole === 'child' || configRole === 'managed',
    desktopOnboardingComplete: true,
    swarmRole: configRole,
    swarmID: String(state.node.swarm_id ?? '').trim(),
    mode: state.node.advertise_mode === 'tailscale' ? 'tailscale' : 'lan',
    host: '127.0.0.1',
    port: 7781,
    desktopPort: 5555,
    advertiseHost: advertiseURL || advertise,
    advertisePort: 7781,
    tailscaleURL: state.node.advertise_mode === 'tailscale' ? advertise : '',
    bypassPermissions: false,
    devMode: false,
    localTransportPort: 7790,
    localTransportActive: false,
    localTransportWarning: '',
    peerTransportPort: 7791,
    restartRequired: false,
    restartReason: '',
  }
}

function onboardingPairingFromSwarmState(state: SwarmLocalState): DesktopOnboardingPairing {
  return {
    swarmID: String(state.node.swarm_id ?? '').trim(),
    pairingState: String(state.pairing.pairing_state ?? '').trim(),
    parentSwarmID: String(state.pairing.parent_swarm_id ?? '').trim(),
    activeInviteID: String(state.pairing.active_invite_id ?? '').trim(),
    lastEnrollmentID: String(state.pairing.last_enrollment_id ?? '').trim(),
    lastDecision: String(state.pairing.last_decision ?? '').trim(),
    lastDecisionReason: String(state.pairing.last_decision_reason ?? '').trim(),
    lastUpdatedByRole: String(state.pairing.last_updated_by_role ?? '').trim(),
    rendezvousTransports: state.pairing.rendezvous_transports ?? [],
    managedAuthOwnerSwarmID: String(state.pairing.managed_auth_owner_swarm_id ?? '').trim(),
    managedAuthSnapshotHash: String(state.pairing.managed_auth_snapshot_hash ?? '').trim(),
    managedAuthAppliedAt: typeof state.pairing.managed_auth_applied_at === 'number' ? state.pairing.managed_auth_applied_at : 0,
    managedAuthLastAttemptAt: typeof state.pairing.managed_auth_last_attempt_at === 'number' ? state.pairing.managed_auth_last_attempt_at : 0,
    managedAuthLastError: String(state.pairing.managed_auth_last_error ?? '').trim(),
  }
}

function groupStateFromSwarmState(state: SwarmLocalState): DesktopSwarmGroupState[] {
  return (state.groups ?? []).map((record) => ({
    group: {
      id: String(record.group?.id ?? '').trim(),
      name: String(record.group?.name ?? '').trim(),
      networkName: String(record.group?.network_name ?? '').trim(),
      hostSwarmID: String(record.group?.host_swarm_id ?? '').trim(),
      createdAt: typeof record.group?.created_at === 'number' ? record.group.created_at : 0,
      updatedAt: typeof record.group?.updated_at === 'number' ? record.group.updated_at : 0,
    },
    members: (record.members ?? []).map((member) => ({
      groupID: String(member.group_id ?? '').trim(),
      swarmID: String(member.swarm_id ?? '').trim(),
      name: String(member.name ?? '').trim(),
      swarmRole: String(member.swarm_role ?? '').trim(),
      membershipRole: String(member.membership_role ?? '').trim(),
      createdAt: typeof member.created_at === 'number' ? member.created_at : 0,
      updatedAt: typeof member.updated_at === 'number' ? member.updated_at : 0,
    })),
  }))
}

function dashboardStatusFromSwarmState(state: SwarmLocalState, settings: UISettingsWire | null): DesktopOnboardingStatus {
  return {
    ok: true,
    needsOnboarding: false,
    identity: {
      bootstrapped: true,
      userID: '',
      accountScopeID: '',
      username: '',
      teamID: '',
      teamDisplayName: '',
      teamDefault: false,
      membershipRole: '',
    },
    config: onboardingConfigFromDashboardState(state, settings),
    heuristics: {
      missingSwarmName: false,
      credentialCount: 0,
      savedWorkspaceCount: 0,
      vaultConfigured: false,
    },
    pairing: onboardingPairingFromSwarmState(state),
    network: emptyOnboardingNetwork(),
    currentGroupID: String(state.current_group_id ?? '').trim(),
    groups: groupStateFromSwarmState(state),
    discoveredSwarms: [],
    vault: {
      enabled: false,
      unlocked: false,
      unlockRequired: false,
      storageMode: '',
      warning: '',
    },
    auth: {
      credentialCount: 0,
      activeProviders: [],
      providers: [],
    },
    workspace: {
      savedCount: 0,
    },
  }
}

function swarmRoleLabel(value: string | null | undefined): string {
  const role = String(value ?? '').trim().toLowerCase()
  switch (role) {
    case 'child':
      return 'Child'
    case 'managed':
      return 'Managed Host'
    case 'controller':
    case 'parent':
    case 'master':
      return 'Master'
    default:
      return role ? formatUnderscoreLabel(role) : 'Swarm'
  }
}

function remoteTailnetVisitURL(...candidates: Array<string | null | undefined>): string {
  const raw = candidates.map((candidate) => String(candidate ?? '').trim()).find(Boolean) || ''
  if (!raw) {
    return ''
  }
  try {
    const parsed = new URL(raw)
    if (!parsed.hostname.includes('.ts.net')) {
      return raw
    }
    parsed.port = ''
    parsed.pathname = ''
    parsed.search = ''
    parsed.hash = ''
    return parsed.toString().replace(/\/$/, '')
  } catch {
    return raw
  }
}

function currentGroup(status: DesktopOnboardingStatus | null): DesktopSwarmGroupState | null {
  if (!status) {
    return null
  }
  const currentGroupID = status.currentGroupID.trim()
  if (currentGroupID) {
    const exact = status.groups.find((group) => group.group.id === currentGroupID)
    if (exact) {
      return exact
    }
  }
  return status.groups[0] ?? null
}

function compactURL(raw: string | null | undefined): string {
  const value = String(raw ?? '').trim()
  if (!value) {
    return ''
  }
  try {
    const parsed = new URL(value)
    return parsed.host || value
  } catch {
    return value
  }
}

function swarmTargetByID(targets: SwarmTarget[], swarmID: string): SwarmTarget | null {
  const normalized = swarmID.trim()
  if (!normalized) {
    return null
  }
  return targets.find((target) => target.swarm_id.trim() === normalized) ?? null
}

function isManagedHostGroupMember(member: DesktopSwarmGroupMember, target?: SwarmTarget | null): boolean {
  const memberRole = String(member.swarmRole ?? '').trim().toLowerCase()
  const targetRole = String(target?.role ?? '').trim().toLowerCase()
  const relationship = String(target?.relationship ?? '').trim().toLowerCase()
  return memberRole === 'managed' || targetRole === 'managed' || relationship === 'managed' || target?.kind === 'host'
}

function containerLocationLabel(hostName: string, containerName: string): string {
  const host = hostName.trim() || 'host'
  const container = containerName.trim() || 'container'
  return `${host} / ${container}`
}

function emptySwarmMirrorResources(): SwarmMirrorResources {
  return { hosts: [], workspaces: [], containers: [], deployments: [] }
}

const PENDING_LINK_REVIEW_TARGET_STORAGE_KEY = 'swarm.pendingLinkReviewTarget.v1'

function loadPendingLinkReviewTarget(): SwarmTarget | null {
  if (typeof window === 'undefined') {
    return null
  }
  try {
    const raw = window.localStorage.getItem(PENDING_LINK_REVIEW_TARGET_STORAGE_KEY) || window.localStorage.getItem('swarm.pendingReplicationTarget.v1')
    if (!raw) {
      return null
    }
    const parsed = JSON.parse(raw) as Partial<SwarmTarget>
    const swarmID = String(parsed.swarm_id ?? '').trim()
    if (!swarmID) {
      return null
    }
    return {
      swarm_id: swarmID,
      name: String(parsed.name ?? swarmID).trim(),
      role: String(parsed.role ?? 'managed').trim(),
      relationship: String(parsed.relationship ?? 'managed').trim(),
      kind: parsed.kind === 'host' || parsed.kind === 'remote' || parsed.kind === 'local' || parsed.kind === 'self' ? parsed.kind : 'host',
      attach_status: String(parsed.attach_status ?? 'paired').trim(),
      online: parsed.online !== false,
      selectable: parsed.selectable !== false,
      current: Boolean(parsed.current),
      backend_url: String(parsed.backend_url ?? '').trim(),
    }
  } catch {
    return null
  }
}

function savePendingLinkReviewTarget(target: SwarmTarget | null): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    if (target) {
      window.localStorage.setItem(PENDING_LINK_REVIEW_TARGET_STORAGE_KEY, JSON.stringify(target))
      window.localStorage.removeItem('swarm.pendingReplicationTarget.v1')
      return
    }
    window.localStorage.removeItem(PENDING_LINK_REVIEW_TARGET_STORAGE_KEY)
    window.localStorage.removeItem('swarm.pendingReplicationTarget.v1')
  } catch {
    // Ignore local persistence failures; the in-memory pending card still works.
  }
}

function mirrorWorkspaceName(workspace: SwarmMirrorWorkspaceResource): string {
  return String(workspace.workspace_name || workspace.name || workspace.path.split('/').filter(Boolean).pop() || 'workspace').trim()
}

function deploymentMatchesContainer(deployment: DeployContainerDeployment, container: SwarmLocalContainer): boolean {
  if (!deployment || !container) {
    return false
  }
  const deploymentID = deployment.id.trim()
  const deploymentContainerID = String(deployment.container_id ?? '').trim()
  const deploymentContainerName = String(deployment.container_name ?? '').trim()
  return (
    (deploymentID !== '' && deploymentID === container.id.trim())
    || (deploymentContainerID !== '' && deploymentContainerID === container.containerID.trim())
    || (deploymentContainerName !== '' && deploymentContainerName === container.containerName.trim())
  )
}

function deploymentMatchesMirroredContainer(deployment: DeployContainerDeployment, mirrored: SwarmMirrorResource<SwarmLocalContainer>): boolean {
  if (!deployment || !mirrored) {
    return false
  }
  const container = mirrorResourceLocalContainer(mirrored.resource)
  return deploymentMatchesContainer(deployment, container)
}

function urlForHostPort(protocol: string, host: string, port: number): string {
  const normalizedProtocol = protocol.trim() || 'http:'
  const normalizedHost = host.trim()
  if (normalizedHost === '' || !Number.isInteger(port) || port < 1 || port > 65535) {
    return ''
  }
  return `${normalizedProtocol}//${normalizedHost}:${port}`
}

function AccessURLRow({ label, url }: { label: string; url: string }) {
  const normalizedURL = url.trim()
  if (!normalizedURL) {
    return null
  }
  return (
    <div className="mt-3">
      <div className="text-[11px] uppercase tracking-[0.18em] text-[var(--app-text-muted)]">{label}</div>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <div className="min-w-0 flex-1 break-all text-xs text-[var(--app-text-muted)]">{normalizedURL}</div>
        <a
          href={normalizedURL}
          target="_blank"
          rel="noreferrer"
          className="inline-flex h-8 items-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-subtle)]"
        >
          Open
        </a>
      </div>
    </div>
  )
}

function formatRemoteSessionStatus(value: string | null | undefined): string {
  const normalized = String(value ?? '').trim()
  if (!normalized) {
    return 'unknown'
  }
  switch (normalized) {
    case 'preflight_ready':
      return 'preflight ready'
    case 'waiting_for_child':
      return 'waiting for child'
    case 'auth_required':
      return 'auth required'
    case 'waiting_for_approval':
      return 'waiting for approval'
    default:
      return formatUnderscoreLabel(normalized)
  }
}

function formatManagedSyncTimestamp(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return 'not applied yet'
  }
  return new Date(value).toLocaleString()
}

function compactSnapshotHash(value: string | null | undefined): string {
  const normalized = String(value ?? '').trim()
  return normalized.length > 12 ? normalized.slice(0, 12) : normalized
}

function summarizeWorkspaceBootstrap(item: DeployContainerWorkspaceBootstrap): string {
  const label = item.source_workspace_name || item.source_workspace_path || item.target_workspace_path || 'Workspace'
  const details = [
    item.target_workspace_path || '',
    item.writable ? 'rw' : 'ro',
    item.sync?.enabled ? `sync ${item.sync.mode || 'managed'}` : '',
  ].filter(Boolean)
  return details.length > 0 ? `${label} · ${details.join(' · ')}` : label
}

function summarizeRemotePayload(payload: RemoteDeployPayload): string {
  const label = payload.workspace_name || payload.workspace_path || payload.source_path || 'Workspace'
  const details = [
    payload.target_path || '/workspaces',
    payload.mode || 'rw',
    Number.isFinite(payload.included_files) ? `${payload.included_files} tracked files` : '',
  ].filter(Boolean)
  return details.length > 0 ? `${label} · ${details.join(' · ')}` : label
}

function summarizeContainerMount(mount: SwarmLocalContainer['mounts'][number]): string {
  const label = mount.workspaceName || mount.workspacePath || mount.sourcePath || 'Workspace mount'
  const details = [
    mount.targetPath || '',
    mount.mode || 'rw',
    mount.sourcePath && mount.sourcePath !== label ? mount.sourcePath : '',
  ].filter(Boolean)
  return details.length > 0 ? `${label} · ${details.join(' · ')}` : label
}

function isMissingManagedHostContainerDeleteError(error: unknown): boolean {
  const message = (error instanceof Error ? error.message : String(error ?? '')).toLowerCase()
  return message.includes('local container not found')
    || message.includes('no such container')
    || message.includes('no such object')
    || (message.includes('container') && message.includes('not found'))
}

function mirrorResourceLocalContainer(record: unknown): SwarmLocalContainer {
  const item = record as Partial<SwarmLocalContainer> & Record<string, unknown>
  const rawMounts = Array.isArray(item.mounts) ? item.mounts : []
  return {
    id: String(item.id ?? '').trim(),
    name: String(item.name ?? '').trim(),
    containerName: String(item.containerName ?? item.container_name ?? '').trim(),
    runtime: String(item.runtime ?? '').trim(),
    networkName: String(item.networkName ?? item.network_name ?? '').trim(),
    status: String(item.status ?? '').trim(),
    containerID: String(item.containerID ?? item.container_id ?? '').trim(),
    hostAPIBaseURL: String(item.hostAPIBaseURL ?? item.host_api_base_url ?? '').trim(),
    hostPort: typeof item.hostPort === 'number' ? item.hostPort : (typeof item.host_port === 'number' ? item.host_port : 0),
    runtimePort: typeof item.runtimePort === 'number' ? item.runtimePort : (typeof item.runtime_port === 'number' ? item.runtime_port : 0),
    image: String(item.image ?? '').trim(),
    warning: String(item.warning ?? '').trim(),
    mounts: rawMounts.map((mount) => {
      const value = mount as unknown as Record<string, unknown>
      return {
        sourcePath: String(value.sourcePath ?? value.source_path ?? '').trim(),
        targetPath: String(value.targetPath ?? value.target_path ?? '').trim(),
        mode: String(value.mode ?? '').trim() === 'ro' ? 'ro' : 'rw',
        workspacePath: String(value.workspacePath ?? value.workspace_path ?? '').trim(),
        workspaceName: String(value.workspaceName ?? value.workspace_name ?? '').trim(),
      }
    }),
    createdAt: typeof item.createdAt === 'number' ? item.createdAt : (typeof item.created_at === 'number' ? item.created_at : 0),
    updatedAt: typeof item.updatedAt === 'number' ? item.updatedAt : (typeof item.updated_at === 'number' ? item.updated_at : 0),
  }
}

function flowDisplayName(flow: FlowSummaryRecord): string {
  return flow.definition.name || flow.definition.flow_id || 'Untitled flow'
}

function flowWorkspaceLabel(flow: FlowSummaryRecord): string {
  return flow.workspace_detail?.workspace_path
    || flow.definition.workspace.runtime_workspace_path
    || flow.definition.workspace.workspace_path
    || flow.definition.workspace.host_workspace_path
    || flow.definition.workspace.cwd
    || ''
}

function flowMatchesContainerTarget(flow: FlowSummaryRecord, input: {
  deployment?: DeployContainerDeployment | null
  container?: SwarmLocalContainer | null
  swarmID?: string
  deploymentID?: string
}): boolean {
  const target = flow.definition.target
  const targetDetail = flow.target_detail
  const normalizedSwarmIDs = new Set([
    input.swarmID,
    input.deployment?.child_swarm_id,
  ].map((value) => String(value ?? '').trim().toLowerCase()).filter(Boolean))
  const normalizedDeploymentIDs = new Set([
    input.deploymentID,
    input.deployment?.id,
    input.deployment?.container_id,
    input.deployment?.container_name,
    input.container?.id,
    input.container?.containerID,
    input.container?.containerName,
  ].map((value) => String(value ?? '').trim().toLowerCase()).filter(Boolean))
  const targetSwarmID = String(target.swarm_id ?? '').trim().toLowerCase()
  const targetDeploymentID = String(target.deployment_id ?? '').trim().toLowerCase()
  const detailSwarmID = String(targetDetail?.swarm_id ?? '').trim().toLowerCase()
  const detailDeploymentID = String((targetDetail as { deployment_id?: unknown } | null | undefined)?.deployment_id ?? '').trim().toLowerCase()
  return (
    (!!targetSwarmID && normalizedSwarmIDs.has(targetSwarmID))
    || (!!detailSwarmID && normalizedSwarmIDs.has(detailSwarmID))
    || (!!targetDeploymentID && normalizedDeploymentIDs.has(targetDeploymentID))
    || (!!detailDeploymentID && normalizedDeploymentIDs.has(detailDeploymentID))
  )
}

function flowImpactSummary(flows: FlowSummaryRecord[]): string {
  if (flows.length === 0) {
    return ''
  }
  return `${flows.length} assigned flow${flows.length === 1 ? '' : 's'} will be unassigned and turned off to avoid stale assignments.`
}

function FlowImpactDetails({ flows }: { flows: FlowSummaryRecord[] }) {
  if (flows.length === 0) {
    return null
  }
  return (
    <div className="mt-3 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning-text)]">
      <div className="flex items-start gap-2">
        <TriangleAlert size={14} className="mt-0.5 shrink-0" />
        <div className="min-w-0">
          <div className="font-medium">Deleting this container unassigns and turns off {flows.length} assigned flow{flows.length === 1 ? '' : 's'}.</div>
          <div className="mt-1 text-[var(--app-text-muted)]">Swarm keeps the flow definitions for later reuse, but clears their target first so they do not point at a removed container.</div>
          <div className="mt-2 grid gap-1">
            {flows.slice(0, 4).map((flow) => {
              const workspace = flowWorkspaceLabel(flow)
              return (
                <div key={flow.definition.flow_id} className="min-w-0 truncate">
                  {flowDisplayName(flow)}{workspace ? ` · ${workspace}` : ''}
                </div>
              )
            })}
            {flows.length > 4 ? <div>+{flows.length - 4} more flow{flows.length - 4 === 1 ? '' : 's'}</div> : null}
          </div>
        </div>
      </div>
    </div>
  )
}

function MountedWorkspaceDetails({ items }: { items: string[] }) {
  if (items.length === 0) {
    return null
  }
  return (
    <div className="mt-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-xs">
      <div className="font-medium text-[var(--app-text)]">Mounted workspaces</div>
      <div className="mt-2 grid gap-1 text-[var(--app-text-muted)]">
        {items.slice(0, 4).map((item, index) => <div key={`${item}:${index}`} className="min-w-0 truncate">{item}</div>)}
        {items.length > 4 ? <div>+{items.length - 4} more mount{items.length - 4 === 1 ? '' : 's'}</div> : null}
      </div>
    </div>
  )
}

interface DeleteCandidate {
  selectionID: string
  kind: 'local' | 'managed-host'
  hostSwarmID: string
  hostName: string
  container: SwarmLocalContainer
  attachment: DeployContainerDeployment | null
  flows: FlowSummaryRecord[]
  mounts: string[]
  canDelete: boolean
  disabledReason?: string
  mirrorResourceID?: string
  managedHostBackendURL?: string
}

interface DeleteSwarmCandidate {
  selectionID: string
  kind: 'local' | 'remote' | 'stale-remote'
  swarmID: string
  swarmName: string
  deployment: DeployContainerDeployment | null
  container: SwarmLocalContainer | null
  remoteSession: RemoteDeploySession | null
}

type RemoteDeleteMode = 'teardown' | 'detach'

function ManagedSwarmSettingsDialog({
  deployment,
  open,
  submitting,
  error,
  onClose,
  onSave,
}: {
  deployment: DeployContainerDeployment | null
  open: boolean
  submitting: boolean
  error: string | null
  onClose: () => void
  onSave: (input: { syncEnabled: boolean; syncModules: string[]; bypassPermissions: boolean }) => void
}) {
  const [syncEnabled, setSyncEnabled] = useState(false)
  const [syncAgents, setSyncAgents] = useState(true)
  const [syncCustomTools, setSyncCustomTools] = useState(true)
  const [syncSkills, setSyncSkills] = useState(true)
  const [bypassPermissions, setBypassPermissions] = useState(false)

  useEffect(() => {
    if (!deployment || !open) {
      return
    }
    const modules = new Set(deployment.sync_modules ?? ['credentials', 'agents', 'custom_tools', 'skills', 'permissions', 'model_defaults'])
    setSyncEnabled(Boolean(deployment.sync_enabled))
    setSyncAgents(modules.has('agents'))
    setSyncCustomTools(modules.has('custom_tools'))
    setSyncSkills(modules.has('skills'))
    setBypassPermissions(Boolean(deployment.bypass_permissions))
  }, [deployment, open])

  if (!open || !deployment) {
    return null
  }

  const modules = ['credentials', ...(syncAgents ? ['agents'] : []), ...(syncCustomTools ? ['custom_tools'] : []), ...(syncSkills ? ['skills'] : []), 'permissions', 'model_defaults']

  return (
    <Dialog>
      <DialogBackdrop />
      <DialogPanel className="mx-auto mt-[10vh] flex w-[min(560px,calc(100vw-24px))] max-w-[560px] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
          <div>
            <div className="text-lg font-semibold text-[var(--app-text)]">Managed swarm settings</div>
            <div className="mt-1 text-sm text-[var(--app-text-muted)]">{deployment.child_display_name || deployment.name}</div>
          </div>
          <ModalCloseButton onClick={onClose} disabled={submitting} />
        </div>
        <div className="space-y-4 px-6 py-5 text-sm">
          {error ? <div className="rounded-2xl border border-[color-mix(in_oklab,var(--app-error)_45%,var(--app-border))] bg-[color-mix(in_oklab,var(--app-error)_10%,var(--app-surface))] px-4 py-3 text-[var(--app-error)]">{error}</div> : null}
          <label className="flex items-center justify-between gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3">
            <span>
              <span className="block font-medium text-[var(--app-text)]">Swarm Sync</span>
              <span className="block text-xs text-[var(--app-text-muted)]">Mirror selected host state to this managed child.</span>
            </span>
            <input type="checkbox" checked={syncEnabled} onChange={(event) => setSyncEnabled(event.target.checked)} />
          </label>
          {syncEnabled ? (
            <div className="grid gap-3 md:grid-cols-3">
              <label className="rounded-2xl border border-[var(--app-border)] p-3"><input className="mr-2" type="checkbox" checked={syncAgents} onChange={(event) => setSyncAgents(event.target.checked)} />Agents</label>
              <label className="rounded-2xl border border-[var(--app-border)] p-3"><input className="mr-2" type="checkbox" checked={syncCustomTools} onChange={(event) => setSyncCustomTools(event.target.checked)} />Custom tools</label>
              <label className="rounded-2xl border border-[var(--app-border)] p-3"><input className="mr-2" type="checkbox" checked={syncSkills} onChange={(event) => setSyncSkills(event.target.checked)} />Skills</label>
            </div>
          ) : null}
          <label className="flex items-center justify-between gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3">
            <span>
              <span className="block font-medium text-[var(--app-text)]">Bypass permissions override</span>
              <span className="block text-xs text-[var(--app-text-muted)]">{bypassPermissions ? 'ON: child bypasses prompts; host policy is not mirrored.' : 'OFF: host-managed permissions mirror host policy and route approvals through the host.'}</span>
            </span>
            <input type="checkbox" checked={bypassPermissions} onChange={(event) => setBypassPermissions(event.target.checked)} />
          </label>
        </div>
        <div className="flex justify-end gap-2 border-t border-[var(--app-border)] px-6 py-4">
          <Button variant="outline" onClick={onClose} disabled={submitting}>Cancel</Button>
          <Button onClick={() => onSave({ syncEnabled, syncModules: syncEnabled ? modules : [], bypassPermissions })} disabled={submitting}>{submitting ? 'Saving…' : 'Save settings'}</Button>
        </div>
      </DialogPanel>
    </Dialog>
  )
}

function remoteDeleteCandidateSupportsSSHDelete(candidate: DeleteSwarmCandidate): boolean {
  if (candidate.kind !== 'remote') {
    return false
  }
  return Boolean(candidate.remoteSession?.id && candidate.remoteSession?.ssh_session_target)
}

function DeleteContainersModal({
  open,
  busy,
  candidates,
  selectedIDs,
  result,
  onToggle,
  onClose,
  onConfirm,
}: {
  open: boolean
  busy: boolean
  candidates: DeleteCandidate[]
  selectedIDs: Set<string>
  result: SwarmLocalContainerDeleteResult | null
  onToggle: (id: string) => void
  onClose: () => void
  onConfirm: () => void
}) {
  if (!open) {
    return null
  }

  return (
    <Dialog>
      <DialogBackdrop onClick={busy ? undefined : onClose} />
      <DialogPanel className="mx-auto mt-[8vh] flex w-[min(760px,calc(100vw-24px))] max-w-[760px] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(760px,calc(100vw-48px))]">
        <div className="border-b border-[var(--app-border)] px-6 py-5">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-xl font-semibold text-[var(--app-text)]">Delete containers</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                Remove selected local or managed-host containers. Assigned flows are unassigned first, then Swarm asks the owning host to delete the container and cleans linked records.
              </p>
            </div>
            <ModalCloseButton onClick={onClose} aria-label="Close delete containers dialog" />
          </div>
        </div>

        <div className="flex max-h-[min(76vh,760px)] flex-col gap-4 overflow-y-auto px-6 py-6">
          {candidates.length === 0 ? (
            <Card className="border-dashed p-5 text-sm text-[var(--app-text-muted)]">
              No containers available to delete.
            </Card>
          ) : (
            <div className="grid gap-3">
              {candidates.map(({ selectionID, kind, hostName, container, attachment, flows, mounts, canDelete, disabledReason }) => {
                const checked = selectedIDs.has(selectionID)
                const childLabel = attachment?.child_display_name || attachment?.child_swarm_id || ''
                return (
                  <label key={selectionID} className={`flex items-start gap-3 rounded-2xl border p-4 transition ${canDelete ? 'cursor-pointer' : 'cursor-not-allowed opacity-70'} ${checked ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}` }>
                    <input
                      type="checkbox"
                      className="mt-1 h-4 w-4 rounded border-[var(--app-border)]"
                      checked={checked}
                      onChange={() => onToggle(selectionID)}
                      disabled={busy || !canDelete}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <div className="truncate text-sm font-semibold text-[var(--app-text)]">{container.name}</div>
                        <Badge tone={container.status === 'running' ? 'live' : 'neutral'}>{container.status || 'created'}</Badge>
                        <Badge tone={kind === 'managed-host' ? 'warning' : 'neutral'}>{kind === 'managed-host' ? `managed host · ${hostName}` : hostName}</Badge>
                        {attachment ? <Badge tone="warning">removes child info</Badge> : null}
                      </div>
                      <div className="mt-1 text-xs text-[var(--app-text-muted)]">{container.containerName}</div>
                      <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                        <div>Host: {hostName}</div>
                        <div>Runtime: {container.runtime || 'unknown'}</div>
                        {childLabel ? <div>Connected child swarm: {childLabel}</div> : null}
                        {attachment ? <div>Also removes linked deployment, trusted peer, and group membership info from this manager.</div> : null}
                        {!canDelete && disabledReason ? <div className="text-[var(--app-warning-text)]">{disabledReason}</div> : null}
                        {flowImpactSummary(flows) ? <div className="text-[var(--app-warning-text)]">{flowImpactSummary(flows)}</div> : null}
                      </div>
                      <MountedWorkspaceDetails items={mounts} />
                      <FlowImpactDetails flows={flows} />
                    </div>
                  </label>
                )
              })}
            </div>
          )}

          {result ? (
            <Card className="border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm">
              <div className="font-medium text-[var(--app-text)]">Deleted {result.count} container{result.count === 1 ? '' : 's'}</div>
              <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                {result.childInfoRemoved > 0 ? `Removed linked child info for ${result.childInfoRemoved} selection${result.childInfoRemoved === 1 ? '' : 's'}. ` : ''}
                {result.failed > 0 ? `${result.failed} failed.` : 'All selected deletions completed.'}
              </div>
              {result.items.some((item) => item.error) ? (
                <div className="mt-3 grid gap-2">
                  {result.items.filter((item) => item.error).map((item) => (
                    <div key={item.id} className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">
                      {item.name || item.id}: {item.error}
                    </div>
                  ))}
                </div>
              ) : null}
            </Card>
          ) : null}
        </div>

        <div className="flex items-center justify-between gap-3 border-t border-[var(--app-border)] px-6 py-4">
          <div className="text-xs text-[var(--app-text-muted)]">{selectedIDs.size} selected</div>
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" onClick={onClose} disabled={busy}>Cancel</Button>
            <Button type="button" onClick={onConfirm} disabled={busy || selectedIDs.size === 0 || candidates.length === 0}>
              <CheckSquare size={14} />
              {busy ? 'Deleting…' : 'Delete selected'}
            </Button>
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}

function DeleteSwarmsModal({
  open,
  busy,
  candidates,
  selectedIDs,
  remoteDeleteMode,
  onToggle,
  onRemoteDeleteModeChange,
  onClose,
  onConfirm,
}: {
  open: boolean
  busy: boolean
  candidates: DeleteSwarmCandidate[]
  selectedIDs: Set<string>
  remoteDeleteMode: RemoteDeleteMode
  onToggle: (id: string) => void
  onRemoteDeleteModeChange: (value: RemoteDeleteMode) => void
  onClose: () => void
  onConfirm: () => void
}) {
  if (!open) {
    return null
  }

  const singleCandidate = candidates.length === 1 ? candidates[0] : null
  const effectiveSelectedIDs = singleCandidate ? new Set([singleCandidate.selectionID]) : selectedIDs
  const selectedCandidates = singleCandidate
    ? [singleCandidate]
    : candidates.filter((candidate) => effectiveSelectedIDs.has(candidate.selectionID))
  const selectedRemoteCandidates = selectedCandidates.filter((candidate) => candidate.kind !== 'local')
  const selectedRemoteCandidatesSupportSSHDelete = selectedRemoteCandidates.length > 0
    && selectedRemoteCandidates.every(remoteDeleteCandidateSupportsSSHDelete)
  const effectiveRemoteDeleteMode: RemoteDeleteMode = selectedRemoteCandidatesSupportSSHDelete ? remoteDeleteMode : 'detach'
  const showRemoteDeleteMode = selectedRemoteCandidates.length > 0
  const singleRemoteCleanup = singleCandidate?.kind === 'stale-remote'
  const singleRemoteCandidate = singleCandidate?.kind === 'remote' ? singleCandidate : null
  const singleRemoteDeleteBySSH = Boolean(singleRemoteCandidate && effectiveRemoteDeleteMode === 'teardown' && remoteDeleteCandidateSupportsSSHDelete(singleRemoteCandidate))
  const title = singleCandidate
    ? (singleRemoteCleanup
        ? 'Remove stale Managed Swarm record'
        : (singleRemoteCandidate
            ? (singleRemoteDeleteBySSH ? 'Remove linked Managed Swarm' : 'Remove Managed Swarm')
            : 'Remove linked container swarm'))
    : 'Remove swarms'
  const singleCandidateWorkspaceSummaries = singleCandidate == null
    ? []
    : (
        singleCandidate.kind === 'local'
          ? (singleCandidate.deployment?.workspace_bootstrap ?? []).map(summarizeWorkspaceBootstrap)
          : (singleCandidate.remoteSession?.preflight.payloads ?? []).map(summarizeRemotePayload)
      )

  return (
    <Dialog>
      <DialogBackdrop onClick={busy ? undefined : onClose} />
      <DialogPanel className="mx-auto mt-[8vh] flex w-[min(760px,calc(100vw-24px))] max-w-[760px] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(760px,calc(100vw-48px))]">
        <div className="border-b border-[var(--app-border)] px-6 py-5">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-xl font-semibold text-[var(--app-text)]">{title}</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                {singleCandidate
                  ? (singleRemoteCleanup
                      ? `Remove the stale master-side record for ${singleCandidate.swarmName}? This does not SSH into the remote host or delete remote files.`
                      : (singleRemoteCandidate
                          ? (singleRemoteDeleteBySSH
                              ? `Delete ${singleCandidate.swarmName}? Swarm will SSH into ${singleCandidate.remoteSession?.ssh_session_target || 'the remote host'}, remove the managed remote child runtime there, and then clean up the linked records on this master.`
                              : `Remove ${singleCandidate.swarmName} from this master only? This does not SSH into the remote host or delete remote files.`)
                          : `Delete ${singleCandidate.swarmName}? This removes its local child container and linked child info from this master.`))
                  : 'Choose which local and remote child swarms to remove. Local swarms delete their local containers. Remote swarms can either be deleted over SSH or removed from this master only.'}
              </p>
            </div>
            <ModalCloseButton onClick={onClose} aria-label={`Close ${title.toLowerCase()} dialog`} />
          </div>
        </div>

        <div className="flex max-h-[min(76vh,760px)] flex-col gap-4 overflow-y-auto px-6 py-6">
          <Card className="border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning-text)]">
            <div className="flex items-start gap-3">
              <TriangleAlert size={18} className="mt-0.5 shrink-0" />
              <div>
                Local child deletion removes the container and any writable contents stored inside that container.
                Mounted or shared host directories are not deleted. Remote SSH delete stops the remote child service,
                removes the remote child container, and deletes the remote install directory on the remote host.
                Remote replicated workspace target paths are not deleted yet. Master-only remote removal only clears saved records on this master.
              </div>
            </div>
          </Card>

          {showRemoteDeleteMode ? (
            <Card className="border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              <div className="text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Remote Delete Mode</div>
              <div className="mt-3">
                <Select
                  value={effectiveRemoteDeleteMode}
                  onChange={(event) => onRemoteDeleteModeChange(event.target.value as RemoteDeleteMode)}
                  disabled={busy || !selectedRemoteCandidatesSupportSSHDelete}
                >
                  {selectedRemoteCandidatesSupportSSHDelete ? <option value="teardown">Delete on remote host over SSH</option> : null}
                  <option value="detach">Remove from this master only</option>
                </Select>
              </div>
              <div className="mt-3 text-xs text-[var(--app-text-muted)]">
                {selectedRemoteCandidatesSupportSSHDelete
                  ? (effectiveRemoteDeleteMode === 'teardown'
                      ? 'Swarm will SSH to each selected remote host, stop and disable the managed remote child service, remove the remote child container, delete the remote install directory, and then remove the linked records on this master.'
                      : 'Swarm will only remove the saved session, trusted-peer, workspace replication, and group records on this master. The remote machine keeps running until you remove it there yourself.')
                  : 'The selected remote rows do not have enough SSH install metadata for a full remote teardown, so only remove-from-master is available.'}
              </div>
            </Card>
          ) : null}

          {candidates.length === 0 ? (
            <Card className="border-dashed p-5 text-sm text-[var(--app-text-muted)]">
              No deletable local or remote child swarms are available.
            </Card>
          ) : singleCandidate ? (
            <Card className="p-5">
              <div className="flex flex-wrap items-center gap-2">
                <div className="text-sm font-semibold text-[var(--app-text)]">{singleCandidate.swarmName}</div>
                <Badge tone={
                  singleCandidate.kind === 'local' && singleCandidate.container?.status === 'running'
                    ? 'live'
                    : (singleCandidate.kind === 'remote' ? 'neutral' : 'warning')
                }>
                  {singleCandidate.kind === 'local'
                    ? (singleCandidate.container?.status || 'created')
                    : (singleCandidate.kind === 'remote'
                        ? formatRemoteSessionStatus(singleCandidate.remoteSession?.status || 'unknown')
                        : 'stale record')}
                </Badge>
                <Badge tone={singleCandidate.kind === 'local' ? 'neutral' : (singleCandidate.kind === 'remote' ? 'warning' : 'warning')}>
                  {singleCandidate.kind === 'local'
                    ? 'local container'
                    : (singleCandidate.kind === 'remote'
                        ? (singleRemoteDeleteBySSH ? 'ssh remote delete' : 'master-only cleanup')
                        : 'master-only cleanup')}
                </Badge>
              </div>
              <div className="mt-3 grid gap-1 text-xs text-[var(--app-text-muted)]">
                {singleCandidate.kind === 'local' ? (
                  <>
                    <div>Container: {singleCandidate.container?.containerName || 'unknown'}</div>
                    <div>Runtime: {singleCandidate.container?.runtime || 'unknown'}</div>
                    <div>Swarm sync: {singleCandidate.deployment?.sync_enabled ? (singleCandidate.deployment.sync_mode || 'managed') : 'off'}</div>
                    <div>Permissions: {singleCandidate.deployment?.bypass_permissions ? 'Bypassed' : 'Enforced'}</div>
                    <div>Replicated workspaces: {singleCandidateWorkspaceSummaries.length}</div>
                  </>
                ) : singleCandidate.kind === 'remote' ? (
                  <>
                    <div>Remote host: {singleCandidate.remoteSession?.ssh_session_target || 'unknown'}</div>
                    <div>Remote runtime: {singleCandidate.remoteSession?.remote_runtime || 'unknown'}</div>
                    <div>Swarm sync: {singleCandidate.remoteSession?.sync_enabled ? (singleCandidate.remoteSession.sync_mode || 'managed') : 'off'}</div>
                    <div>Permissions: {singleCandidate.remoteSession?.bypass_permissions ? 'Bypassed' : 'Enforced'}</div>
                    <div>Child swarm id: {singleCandidate.swarmID}</div>
                    <div>Delete mode: {singleRemoteDeleteBySSH ? 'SSH remote delete' : 'Remove from this master only'}</div>
                  </>
                ) : (
                  <>
                    <div>Child swarm id: {singleCandidate.swarmID}</div>
                    <div>Delete mode: Remove from this master only</div>
                  </>
                )}
              </div>
              {singleCandidateWorkspaceSummaries.length > 0 ? (
                <div className="mt-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
                  <div className="text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-muted)]">
                    {singleCandidate.kind === 'local' ? 'Replicated Workspaces' : 'Remote Payloads'}
                  </div>
                  <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                    {singleCandidateWorkspaceSummaries.map((summary) => (
                      <div key={summary}>{summary}</div>
                    ))}
                  </div>
                </div>
              ) : null}
            </Card>
          ) : (
            <div className="grid gap-3">
              {candidates.map((candidate) => {
                const checked = selectedIDs.has(candidate.selectionID)
                const workspaceSummaries = candidate.kind === 'local'
                  ? (candidate.deployment?.workspace_bootstrap ?? []).map(summarizeWorkspaceBootstrap)
                  : (candidate.remoteSession?.preflight.payloads ?? []).map(summarizeRemotePayload)
                return (
                  <label key={candidate.selectionID} className={`flex cursor-pointer items-start gap-3 rounded-2xl border p-4 transition ${checked ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}`}>
                    <input
                      type="checkbox"
                      className="mt-1 h-4 w-4 rounded border-[var(--app-border)]"
                      checked={checked}
                      onChange={() => onToggle(candidate.selectionID)}
                      disabled={busy}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <div className="truncate text-sm font-semibold text-[var(--app-text)]">{candidate.swarmName}</div>
                        <Badge tone={
                          candidate.kind === 'local' && candidate.container?.status === 'running'
                            ? 'live'
                            : (candidate.kind === 'remote' ? 'neutral' : 'warning')
                        }>
                          {candidate.kind === 'local'
                            ? (candidate.container?.status || 'created')
                            : (candidate.kind === 'remote'
                                ? formatRemoteSessionStatus(candidate.remoteSession?.status || 'unknown')
                                : 'stale record')}
                        </Badge>
                        <Badge tone={candidate.kind === 'local' ? 'neutral' : 'warning'}>
                          {candidate.kind === 'local'
                            ? 'local container'
                            : (candidate.kind === 'remote' ? 'remote child' : 'master-only cleanup')}
                        </Badge>
                      </div>
                      <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                        {candidate.kind === 'local'
                          ? (candidate.container?.containerName || '')
                          : (candidate.remoteSession?.ssh_session_target || candidate.swarmID)}
                      </div>
                      <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                        {candidate.kind === 'local' ? (
                          <>
                            <div>Runtime: {candidate.container?.runtime || 'unknown'}</div>
                            <div>Swarm sync: {candidate.deployment?.sync_enabled ? (candidate.deployment.sync_mode || 'managed') : 'off'}</div>
                            <div>Permissions: {candidate.deployment?.bypass_permissions ? 'Bypassed' : 'Enforced'}</div>
                            <div>Replicated workspaces: {workspaceSummaries.length}</div>
                          </>
                        ) : candidate.kind === 'remote' ? (
                          <>
                            <div>Remote runtime: {candidate.remoteSession?.remote_runtime || 'unknown'}</div>
                            <div>Swarm sync: {candidate.remoteSession?.sync_enabled ? (candidate.remoteSession.sync_mode || 'managed') : 'off'}</div>
                            <div>Permissions: {candidate.remoteSession?.bypass_permissions ? 'Bypassed' : 'Enforced'}</div>
                            <div>Remote endpoint: {candidate.remoteSession?.remote_endpoint || candidate.remoteSession?.remote_tailnet_url || 'not recorded'}</div>
                            <div>Delete mode: {effectiveRemoteDeleteMode === 'teardown' && remoteDeleteCandidateSupportsSSHDelete(candidate) ? 'SSH remote delete' : 'Remove from this master only'}</div>
                          </>
                        ) : (
                          <>
                            <div>Child swarm id: {candidate.swarmID}</div>
                            <div>Delete mode: Remove from this master only</div>
                          </>
                        )}
                      </div>
                      {workspaceSummaries.length > 0 ? (
                        <div className="mt-2 text-xs text-[var(--app-text-muted)]">
                          {workspaceSummaries.join(' | ')}
                        </div>
                      ) : null}
                    </div>
                  </label>
                )
              })}
            </div>
          )}
        </div>

        <div className="flex items-center justify-between gap-3 border-t border-[var(--app-border)] px-6 py-4">
          <div className="text-xs text-[var(--app-text-muted)]">
            {singleCandidate ? 'Confirm deletion' : `${selectedIDs.size} selected`}
          </div>
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" onClick={onClose} disabled={busy}>Cancel</Button>
            <Button
              type="button"
              onClick={onConfirm}
              disabled={busy || candidates.length === 0 || (!singleCandidate && selectedIDs.size === 0)}
            >
              <Trash2 size={14} />
              {busy ? 'Deleting…' : (
                singleCandidate
                  ? (singleRemoteCleanup
                      ? 'Remove stale record'
                      : (singleRemoteCandidate
                          ? (singleRemoteDeleteBySSH ? 'Remove linked Managed Swarm' : 'Remove Managed Swarm')
                          : 'Remove linked container swarm'))
                  : 'Remove selected'
              )}
            </Button>
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}

export function DesktopSwarmDashboard() {
  const queryClient = useQueryClient()

  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [swarmState, setSwarmState] = useState<SwarmLocalState | null>(null)
  const [onboardingStatus, setOnboardingStatus] = useState<DesktopOnboardingStatus | null>(null)
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [runtimeLoading, setRuntimeLoading] = useState(true)
  const [localContainersLoading, setLocalContainersLoading] = useState(true)
  const [deploymentsLoading, setDeploymentsLoading] = useState(true)
  const [, setRemoteSessionsLoading] = useState(true)
  const [swarmTargets, setSwarmTargets] = useState<SwarmTarget[]>([])
  const [mirrorResources, setMirrorResources] = useState<SwarmMirrorResources>(emptySwarmMirrorResources)
  const [localRuntime, setLocalRuntime] = useState<SwarmLocalRuntimeStatus>({ recommended: '', available: [], installed: [], issues: {}, warning: '' })
  const [hidingLocalRuntimeWarning, setHidingLocalRuntimeWarning] = useState(false)
  const [localContainers, setLocalContainers] = useState<SwarmLocalContainer[]>([])
  const [deployments, setDeployments] = useState<DeployContainerDeployment[]>([])
  const [remoteSessions, setRemoteSessions] = useState<RemoteDeploySession[]>([])
  const [flows, setFlows] = useState<FlowSummaryRecord[]>([])
  const [pendingPairings, setPendingPairings] = useState<RemoteSwarmPendingPairing[]>([])
  const [pairingDecisionBusyID, setPairingDecisionBusyID] = useState<string | null>(null)
  const [pairingConfirmations, setPairingConfirmations] = useState<Record<string, boolean>>({})
  const [copyState, setCopyState] = useState<'idle' | 'desktop' | 'error'>('idle')
  const [editingLocalName, setEditingLocalName] = useState(false)
  const [localNameDraft, setLocalNameDraft] = useState('')
  const [addSwarmOpen, setAddSwarmOpen] = useState(false)
  const [linkSwarmOpen, setLinkSwarmOpen] = useState(false)
  const [linkRequestOpen, setLinkRequestOpen] = useState(false)
  const [pendingLinkReviewTarget, setPendingLinkReviewTarget] = useState<SwarmTarget | null>(() => loadPendingLinkReviewTarget())
  const [deleteContainersOpen, setDeleteContainersOpen] = useState(false)
  const [selectedDeleteContainerIDs, setSelectedDeleteContainerIDs] = useState<string[]>([])
  const [deleteSwarmsOpen, setDeleteSwarmsOpen] = useState(false)
  const [deleteSwarmCandidateContainerIDs, setDeleteSwarmCandidateContainerIDs] = useState<string[]>([])
  const [selectedDeleteSwarmContainerIDs, setSelectedDeleteSwarmContainerIDs] = useState<string[]>([])
  const [deleteSwarmRemoteMode, setDeleteSwarmRemoteMode] = useState<RemoteDeleteMode>('teardown')
  const [deleteResult, setDeleteResult] = useState<SwarmLocalContainerDeleteResult | null>(null)
  const [settingsDeployment, setSettingsDeployment] = useState<DeployContainerDeployment | null>(null)
  const [settingsSaving, setSettingsSaving] = useState(false)
  const [settingsError, setSettingsError] = useState<string | null>(null)
  const [removingManagedHostID, setRemovingManagedHostID] = useState<string | null>(null)

  const applyCoreDashboardState = (state: SwarmLocalState, nextUISettings: UISettingsWire) => {
    const dashboardStatus = dashboardStatusFromSwarmState(state, nextUISettings)
    setSwarmState(state)
    setOnboardingStatus(dashboardStatus)
    setUISettings(nextUISettings)
    setLocalNameDraft(state.node.name || nextUISettings.swarm?.name || '')
  }

  const applySupplementalDashboardState = (
    runtimeStatus: SwarmLocalRuntimeStatus,
    launchedContainers: SwarmLocalContainer[],
    nextDeployments: DeployContainerDeployment[],
    nextRemoteSessions: RemoteDeploySession[],
    nextPendingPairings: RemoteSwarmPendingPairing[],
    nextSwarmTargets: SwarmTarget[],
    nextMirrorResources: SwarmMirrorResources,
    nextFlows: FlowSummaryRecord[],
  ) => {
    setLocalRuntime(runtimeStatus)
    setLocalContainers(launchedContainers)
    setDeployments(nextDeployments)
    setRemoteSessions(nextRemoteSessions)
    setPendingPairings(nextPendingPairings)
    setSwarmTargets(nextSwarmTargets)
    setMirrorResources(nextMirrorResources)
    setFlows(nextFlows)
  }

  const refresh = async () => {
    const [state, nextUISettings, runtimeStatus, launchedContainers, nextDeployments, nextRemoteSessions, nextPendingPairings, nextTargets, nextMirrorResources, nextFlows] = await Promise.all([
      fetchSwarmState(),
      getUISettings(),
      fetchSwarmLocalRuntimeStatus(),
      fetchSwarmLocalContainers(),
      fetchDeployContainers(),
      fetchRemoteDeploySessions(),
      fetchPendingRemoteSwarmPairings(),
      fetchSwarmTargets(),
      fetchSwarmMirrorResources(),
      fetchFlows(),
    ])
    applyCoreDashboardState(state, nextUISettings)
    applySupplementalDashboardState(runtimeStatus, launchedContainers, nextDeployments, nextRemoteSessions, nextPendingPairings, nextTargets.targets, nextMirrorResources, nextFlows)
  }

  useEffect(() => {
    savePendingLinkReviewTarget(pendingLinkReviewTarget)
  }, [pendingLinkReviewTarget])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setRuntimeLoading(true)
    setLocalContainersLoading(true)
    setDeploymentsLoading(true)
    setRemoteSessionsLoading(true)
    setError(null)
    setStatus(null)

    void Promise.all([fetchSwarmState(), getUISettings()])
      .then(([state, nextUISettings]) => {
        if (!cancelled) {
          applyCoreDashboardState(state, nextUISettings)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Failed to load swarm dashboard')
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })

    void fetchSwarmLocalRuntimeStatus()
      .then((runtimeStatus) => {
        if (!cancelled) {
          setLocalRuntime(runtimeStatus)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load swarm dashboard details'))
        }
      })
      .finally(() => {
        if (!cancelled) {
          setRuntimeLoading(false)
        }
      })

    void fetchSwarmLocalContainers()
      .then((launchedContainers) => {
        if (!cancelled) {
          setLocalContainers(launchedContainers)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load swarm dashboard details'))
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLocalContainersLoading(false)
        }
      })

    void fetchDeployContainers()
      .then((nextDeployments) => {
        if (!cancelled) {
          setDeployments(nextDeployments)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load swarm dashboard details'))
        }
      })
      .finally(() => {
        if (!cancelled) {
          setDeploymentsLoading(false)
        }
      })

    void fetchRemoteDeploySessions()
      .then((nextRemoteSessions) => {
        if (!cancelled) {
          setRemoteSessions(nextRemoteSessions)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load swarm dashboard details'))
        }
      })
      .finally(() => {
        if (!cancelled) {
          setRemoteSessionsLoading(false)
        }
      })

    void fetchPendingRemoteSwarmPairings()
      .then((items) => {
        if (!cancelled) {
          setPendingPairings(items)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load pending pairing requests'))
        }
      })

    void fetchSwarmTargets()
      .then((result) => {
        if (!cancelled) {
          setSwarmTargets(result.targets)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load swarm targets'))
        }
      })

    void fetchSwarmMirrorResources()
      .then((resources) => {
        if (!cancelled) {
          setMirrorResources(resources)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load mirrored swarm resources'))
        }
      })

    void fetchFlows()
      .then((items) => {
        if (!cancelled) {
          setFlows(items)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load assigned flows'))
        }
      })

    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    const refreshLocalName = () => {
      void refresh().catch((err) => {
        setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to refresh swarm name'))
      })
    }
    window.addEventListener('swarm:name-updated', refreshLocalName)
    return () => window.removeEventListener('swarm:name-updated', refreshLocalName)
  }, [refresh])

  useEffect(() => {
    let cancelled = false
    const refreshMirrors = () => {
      void Promise.all([fetchSwarmMirrorResources(), fetchSwarmTargets()])
        .then(([resources, targets]) => {
          if (!cancelled) {
            setMirrorResources(resources)
            setSwarmTargets(targets.targets)
          }
        })
        .catch((err) => {
          if (!cancelled) {
            setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to refresh mirrored swarm resources'))
          }
        })
    }
    window.addEventListener('swarm:mirror-updated', refreshMirrors)
    return () => {
      cancelled = true
      window.removeEventListener('swarm:mirror-updated', refreshMirrors)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    const refreshPendingPairings = () => {
      void fetchPendingRemoteSwarmPairings()
        .then((items) => {
          if (!cancelled) {
            setPendingPairings(items)
          }
        })
        .catch((err) => {
          if (!cancelled) {
            setError((current) => current ?? (err instanceof Error ? err.message : 'Failed to load pending pairing requests'))
          }
        })
    }
    const timer = window.setInterval(refreshPendingPairings, 5_000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [])

  const group = useMemo(() => currentGroup(onboardingStatus), [onboardingStatus])
  const localContainersSectionLoading = localContainersLoading || deploymentsLoading
  const staleAttachedLoading = deploymentsLoading
  const localSwarmID = onboardingStatus?.config.swarmID || swarmState?.node.swarm_id || ''
  const localSwarmName = swarmState?.node.name || uiSettings?.swarm?.name || onboardingStatus?.config.swarmName || 'Local swarm'
  const localSwarmRole = onboardingStatus?.config.swarmRole || swarmState?.node.role || (onboardingStatus?.config.child ? 'child' : 'master')
  const localSwarmRoleLabel = swarmRoleLabel(localSwarmRole)
  const localIsManagedHost = String(localSwarmRole).trim().toLowerCase() === 'managed'
  const localIsChild = localSwarmRoleLabel === 'Child' || localIsManagedHost
  const localManagerSwarmID = onboardingStatus?.pairing.parentSwarmID || ''
  const localPairingState = onboardingStatus?.pairing.pairingState || ''
  const localManagedLinked = localIsManagedHost && (localPairingState === 'paired' || localManagerSwarmID !== '')
  const currentGroupID = group?.group.id.trim() || ''
  const hasPeerGroup = Boolean(currentGroupID || (onboardingStatus?.groups.length ?? 0) > 0)
  const groupMasterID = group?.group.hostSwarmID || ''
  const resolvedGroupMasterName = group?.members.find((member) => member.swarmID === groupMasterID)?.name.trim() || ''
  const localManagerDisplay = resolvedGroupMasterName || localManagerSwarmID || 'Manager'
  const localIsMaster = Boolean(localSwarmID && groupMasterID === localSwarmID && !localIsChild)
  const managedHostControlTitle = localIsManagedHost ? 'This host is already linked to a Manager.' : undefined
  const managedHostingControlsDisabled = loading || busy || localIsChild || Boolean(group && !localIsMaster)
  const addContainerDisabled = loading || busy
  const visiblePendingPairings = activePendingPairings(pendingPairings)
  const localNameDirty = localNameDraft.trim() !== localSwarmName.trim()
  const frontendOrigin = typeof window !== 'undefined' ? window.location.origin : ''
  const browserProtocol = typeof window !== 'undefined' ? window.location.protocol : 'http:'
  const configuredHost = onboardingStatus?.config.host || '127.0.0.1'
  const browserHost = (typeof window !== 'undefined' ? window.location.hostname : '') || onboardingStatus?.config.advertiseHost || '127.0.0.1'
  const backendHost = (typeof window !== 'undefined' ? window.location.hostname : '') || onboardingStatus?.config.advertiseHost || '127.0.0.1'
  const backendPort = String(onboardingStatus?.config.port || 7781)
  const desktopPort = onboardingStatus?.config.desktopPort || 5555
  const backendURL = `${typeof window !== 'undefined' ? window.location.protocol : 'http:'}//${backendHost}:${backendPort}`
  const tailscaleCandidate = tailscaleTransportCandidate(onboardingStatus)
  const localTailscaleURL = tailscaleCandidate.url
  const localTailscalePrimary = usesTailscaleAsPrimaryTransport(onboardingStatus)
  const localTailscaleHosting = tailscaleServeStatus(onboardingStatus)
  const localBindStatus = localBindLabel(configuredHost)
  const desktopServeCommand = `tailscale serve --bg http://127.0.0.1:${desktopPort}`
  const groupDeploymentByChildSwarmID = useMemo(() => {
    const mapped = new Map<string, DeployContainerDeployment>()
    if (!currentGroupID) {
      return mapped
    }
    deployments.forEach((deployment) => {
      const childSwarmID = String(deployment.child_swarm_id ?? '').trim()
      if (
        childSwarmID !== ''
        && String(deployment.group_id ?? '').trim() === currentGroupID
      ) {
        mapped.set(childSwarmID, deployment)
      }
    })
    return mapped
  }, [deployments, group?.group.id])
  const groupRemoteSessionByChildSwarmID = useMemo(() => {
    const mapped = new Map<string, RemoteDeploySession>()
    if (!currentGroupID) {
      return mapped
    }
    remoteSessions.forEach((session) => {
      const childSwarmID = String(session.child_swarm_id ?? '').trim()
      if (
        childSwarmID !== ''
        && String(session.group_id ?? '').trim() === currentGroupID
      ) {
        mapped.set(childSwarmID, session)
      }
    })
    return mapped
  }, [currentGroupID, remoteSessions])
  const managedHostMembers = useMemo(() => (
    (group?.members ?? [])
      .filter((member) => member.swarmID !== localSwarmID)
      .filter((member) => isManagedHostGroupMember(member, swarmTargetByID(swarmTargets, member.swarmID)))
      .sort((left, right) => left.name.localeCompare(right.name))
  ), [group?.members, localSwarmID, swarmTargets])
  const remoteContainerRowsByHostSwarmID = useMemo(() => {
    const mapped = new Map<string, RemoteDeploySession[]>()
    remoteSessions.forEach((session) => {
      const hostSwarmID = String(session.host_swarm_id ?? '').trim()
      const childSwarmID = String(session.child_swarm_id ?? '').trim()
      if (!hostSwarmID || !childSwarmID || String(session.group_id ?? '').trim() !== currentGroupID) {
        return
      }
      const next = mapped.get(hostSwarmID) ?? []
      next.push(session)
      mapped.set(hostSwarmID, next)
    })
    mapped.forEach((items) => items.sort((left, right) => (left.child_name || left.name).localeCompare(right.child_name || right.name)))
    return mapped
  }, [currentGroupID, remoteSessions])
  const mirroredContainersByHostSwarmID = useMemo(() => {
    const mapped = new Map<string, typeof mirrorResources.containers>()
    mirrorResources.containers.forEach((container) => {
      const hostSwarmID = container.managedSwarmID.trim()
      if (!hostSwarmID) {
        return
      }
      const next = mapped.get(hostSwarmID) ?? []
      next.push(container)
      mapped.set(hostSwarmID, next)
    })
    mapped.forEach((items) => items.sort((left, right) => (right.resource.updatedAt || right.updatedAt) - (left.resource.updatedAt || left.updatedAt)))
    return mapped
  }, [mirrorResources.containers])
  const mirroredWorkspacesByHostSwarmID = useMemo(() => {
    const mapped = new Map<string, typeof mirrorResources.workspaces>()
    mirrorResources.workspaces.forEach((workspace) => {
      const hostSwarmID = workspace.managedSwarmID.trim()
      const path = String(workspace.resource.path ?? '').trim()
      if (!hostSwarmID || !path) {
        return
      }
      const next = mapped.get(hostSwarmID) ?? []
      next.push(workspace)
      mapped.set(hostSwarmID, next)
    })
    mapped.forEach((items) => items.sort((left, right) => mirrorWorkspaceName(left.resource).localeCompare(mirrorWorkspaceName(right.resource))))
    return mapped
  }, [mirrorResources.workspaces])
  const mirroredDeploymentByContainerID = useMemo(() => {
    const mapped = new Map<string, DeployContainerDeployment>()
    mirrorResources.deployments.forEach((deployment) => {
      const resource = deployment.resource
      ;[resource.id, resource.container_id, resource.container_name].forEach((value) => {
        const key = String(value ?? '').trim()
        if (key) {
          mapped.set(key, resource)
        }
      })
    })
    return mapped
  }, [mirrorResources.deployments])
  const staleRelationshipMembers = useMemo(() => (
    (group?.members ?? [])
      .filter((member) => member.swarmID !== localSwarmID)
      .filter((member) => !isManagedHostGroupMember(member, swarmTargetByID(swarmTargets, member.swarmID)))
      .filter((member) => !groupDeploymentByChildSwarmID.has(member.swarmID) && !groupRemoteSessionByChildSwarmID.has(member.swarmID))
  ), [group?.members, groupDeploymentByChildSwarmID, groupRemoteSessionByChildSwarmID, localSwarmID, swarmTargets])
  const localDeleteSwarmCandidates = useMemo<DeleteSwarmCandidate[]>(() => (
    (group?.members ?? []).reduce<DeleteSwarmCandidate[]>((items, member) => {
      if (member.swarmID === localSwarmID) {
        return items
      }
      const deployment = groupDeploymentByChildSwarmID.get(member.swarmID) ?? null
      const container = deployment
        ? localContainers.find((candidate) => deploymentMatchesContainer(deployment, candidate)) ?? null
        : null
      if (!deployment || !container) {
        return items
      }
      items.push({
        selectionID: `local:${container.id}`,
        kind: 'local',
        swarmID: member.swarmID,
        swarmName: member.name || deployment.child_display_name || deployment.name || deployment.id,
        deployment,
        container,
        remoteSession: null,
      })
      return items
    }, [])
  ), [group?.members, groupDeploymentByChildSwarmID, localContainers, localSwarmID])
  const remoteDeleteSwarmCandidates = useMemo<DeleteSwarmCandidate[]>(() => (
    (group?.members ?? []).reduce<DeleteSwarmCandidate[]>((items, member) => {
      if (member.swarmID === localSwarmID) {
        return items
      }
      const deployment = groupDeploymentByChildSwarmID.get(member.swarmID) ?? null
      if (deployment) {
        return items
      }
      const remoteSession = groupRemoteSessionByChildSwarmID.get(member.swarmID) ?? null
      if (!remoteSession) {
        return items
      }
      items.push({
        selectionID: `remote:${remoteSession.id}`,
        kind: 'remote',
        swarmID: member.swarmID,
        swarmName: member.name || remoteSession?.child_name || remoteSession?.name || member.swarmID,
        deployment: null,
        container: null,
        remoteSession,
      })
      return items
    }, [])
  ), [group?.members, groupDeploymentByChildSwarmID, groupRemoteSessionByChildSwarmID, localSwarmID])
  const staleRemoteDeleteSwarmCandidates = useMemo<DeleteSwarmCandidate[]>(() => (
    (group?.members ?? []).reduce<DeleteSwarmCandidate[]>((items, member) => {
      if (member.swarmID === localSwarmID) {
        return items
      }
      const deployment = groupDeploymentByChildSwarmID.get(member.swarmID) ?? null
      if (deployment) {
        return items
      }
      const remoteSession = groupRemoteSessionByChildSwarmID.get(member.swarmID) ?? null
      if (remoteSession) {
        return items
      }
      items.push({
        selectionID: `stale-remote:${member.swarmID}`,
        kind: 'stale-remote',
        swarmID: member.swarmID,
        swarmName: member.name || member.swarmID,
        deployment: null,
        container: null,
        remoteSession: null,
      })
      return items
    }, [])
  ), [group?.members, groupDeploymentByChildSwarmID, groupRemoteSessionByChildSwarmID, localSwarmID])
  const baseDeleteSwarmCandidates = useMemo<DeleteSwarmCandidate[]>(() => (
    [...localDeleteSwarmCandidates, ...remoteDeleteSwarmCandidates, ...staleRemoteDeleteSwarmCandidates]
  ), [localDeleteSwarmCandidates, remoteDeleteSwarmCandidates, staleRemoteDeleteSwarmCandidates])
  const visibleLocalContainers = useMemo(
    () => localContainers.slice().sort((left, right) => {
      const leftAttached = deployments.some((deployment) => (
        String(deployment.attach_status ?? '').trim() === 'attached'
        && deploymentMatchesContainer(deployment, left)
      ))
      const rightAttached = deployments.some((deployment) => (
        String(deployment.attach_status ?? '').trim() === 'attached'
        && deploymentMatchesContainer(deployment, right)
      ))
      if (leftAttached !== rightAttached) {
        return leftAttached ? -1 : 1
      }
      return right.updatedAt - left.updatedAt
    }),
    [deployments, localContainers],
  )
  const staleLocalContainers = useMemo(
    () => visibleLocalContainers.filter((container) => (
      container.status === 'missing'
      && !deployments.some((deployment) => (
        String(deployment.attach_status ?? '').trim() === 'attached'
        && deploymentMatchesContainer(deployment, container)
      ))
    )),
    [deployments, visibleLocalContainers],
  )
  const staleAttachedDeployments = useMemo(() => (
    deployments
      .filter((deployment) => {
        const attachStatus = String(deployment.attach_status ?? '').trim()
        if (attachStatus !== 'attached') {
          return false
        }
        const mirroredContainer = mirrorResources.containers.find((container) => deploymentMatchesMirroredContainer(deployment, container)) ?? null
        if (mirroredContainer) {
          return false
        }
        const deploymentGroupID = String(deployment.group_id ?? '').trim()
        const matchedContainer = localContainers.find((container) => deploymentMatchesContainer(deployment, container)) ?? null
        const outsideCurrentGroup = currentGroupID !== '' && deploymentGroupID !== '' && deploymentGroupID !== currentGroupID
        const missingContainerRecord = matchedContainer == null
        return outsideCurrentGroup || missingContainerRecord
      })
      .sort((left, right) => right.updated_at - left.updated_at)
  ), [currentGroupID, deployments, localContainers, mirrorResources.containers])
  const flowsByLocalContainerID = useMemo(() => {
    const mapped = new Map<string, FlowSummaryRecord[]>()
    localContainers.forEach((container) => {
      const attachedDeployment = deployments.find((deployment) => (
        String(deployment.attach_status ?? '').trim() === 'attached'
        && deploymentMatchesContainer(deployment, container)
      )) ?? null
      mapped.set(container.id, flows.filter((flow) => flowMatchesContainerTarget(flow, {
        deployment: attachedDeployment,
        container,
        swarmID: attachedDeployment?.child_swarm_id,
        deploymentID: attachedDeployment?.id || container.id,
      })))
    })
    return mapped
  }, [deployments, flows, localContainers])
  const flowsByDeploymentID = useMemo(() => {
    const mapped = new Map<string, FlowSummaryRecord[]>()
    deployments.forEach((deployment) => {
      const matchedContainer = localContainers.find((container) => deploymentMatchesContainer(deployment, container)) ?? null
      mapped.set(deployment.id, flows.filter((flow) => flowMatchesContainerTarget(flow, {
        deployment,
        container: matchedContainer,
        swarmID: deployment.child_swarm_id,
        deploymentID: deployment.id,
      })))
    })
    return mapped
  }, [deployments, flows, localContainers])
  const deleteCandidates = useMemo<DeleteCandidate[]>(() => {
    const localCandidates = localContainers.map((container): DeleteCandidate => {
      const attachment = deployments.find((deployment) => (
        String(deployment.attach_status ?? '').trim() === 'attached'
        && deploymentMatchesContainer(deployment, container)
      )) ?? null
      const bootstrapMounts = attachment?.workspace_bootstrap?.map(summarizeWorkspaceBootstrap) ?? []
      const containerMounts = container.mounts.map(summarizeContainerMount)
      return {
        selectionID: `local:${container.id}`,
        kind: 'local',
        hostSwarmID: localSwarmID,
        hostName: localSwarmName,
        container,
        attachment,
        flows: flowsByLocalContainerID.get(container.id) ?? [],
        mounts: bootstrapMounts.length > 0 ? bootstrapMounts : containerMounts,
        canDelete: true,
      }
    })

    const managedCandidates = mirrorResources.containers.map((mirrored): DeleteCandidate => {
      const container = mirrorResourceLocalContainer(mirrored.resource)
      const attachment = [container.id, container.containerID, container.containerName]
        .map((value) => mirroredDeploymentByContainerID.get(String(value ?? '').trim()) ?? null)
        .find(Boolean) ?? null
      const target = swarmTargetByID(swarmTargets, mirrored.managedSwarmID)
      const member = managedHostMembers.find((item) => item.swarmID === mirrored.managedSwarmID) ?? null
      const hostName = target?.name || member?.name || mirrored.managedSwarmID || 'Managed Host'
      const bootstrapMounts = attachment?.workspace_bootstrap?.map(summarizeWorkspaceBootstrap) ?? []
      const containerMounts = container.mounts.map(summarizeContainerMount)
      const localDeleteID = container.id || container.containerID || container.containerName || mirrored.id
      const canDelete = Boolean(attachment?.id || localDeleteID)
      return {
        selectionID: `managed-host:${mirrored.managedSwarmID}:${attachment?.id || localDeleteID || mirrored.id}`,
        kind: 'managed-host',
        hostSwarmID: mirrored.managedSwarmID,
        hostName,
        container,
        attachment,
        flows: flows.filter((flow) => flowMatchesContainerTarget(flow, {
          deployment: attachment,
          container,
          swarmID: attachment?.child_swarm_id,
          deploymentID: attachment?.id || container.id,
        })),
        mounts: bootstrapMounts.length > 0 ? bootstrapMounts : containerMounts,
        canDelete,
        disabledReason: canDelete ? undefined : 'This mirrored container has no stable mirrored id yet; refresh the managed host before deleting it from the manager.',
        mirrorResourceID: mirrored.id,
        managedHostBackendURL: target?.backend_url || container.hostAPIBaseURL,
      }
    })

    return [...localCandidates, ...managedCandidates]
  }, [deployments, flows, flowsByLocalContainerID, localContainers, localSwarmID, localSwarmName, managedHostMembers, mirrorResources.containers, mirroredDeploymentByContainerID, swarmTargets])
  const deleteSwarmCandidates = useMemo<DeleteSwarmCandidate[]>(() => {
    if (deleteSwarmCandidateContainerIDs.length === 0) {
      return baseDeleteSwarmCandidates
    }
    const allowed = new Set(deleteSwarmCandidateContainerIDs)
    return baseDeleteSwarmCandidates.filter((candidate) => allowed.has(candidate.selectionID))
  }, [baseDeleteSwarmCandidates, deleteSwarmCandidateContainerIDs])
  const selectedDeleteIDs = useMemo(() => new Set(selectedDeleteContainerIDs), [selectedDeleteContainerIDs])
  const selectedDeleteSwarmIDs = useMemo(() => new Set(selectedDeleteSwarmContainerIDs), [selectedDeleteSwarmContainerIDs])

  const handleCopyTailscaleCommand = async (command: string) => {
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(command)
      setCopyState('desktop')
    } catch {
      setCopyState('error')
    }
  }

  const handlePairingDecision = async (request: RemoteSwarmPendingPairing, approve: boolean) => {
    const requestID = request.request_id.trim()
    if (!requestID) {
      setError('Pairing request id is missing.')
      return
    }
    setPairingDecisionBusyID(requestID)
    setError(null)
    setStatus(null)
    try {
      const result = await approveRemoteSwarmPairing({
        requestID,
        approve,
        confirmed: approve ? pairingConfirmations[requestID] === true : undefined,
        reason: approve ? undefined : 'Rejected from Swarm dashboard',
      })
      setPendingPairings((items) => items.filter((item) => item.request_id !== requestID))
      setPairingConfirmations((current) => {
        const next = { ...current }
        delete next[requestID]
        return next
      })
      await refresh()
      if (approve) {
        const target = managedHostTargetFromPairingResult({ request, result })
        if (target) {
          setPendingLinkReviewTarget(target)
          setLinkRequestOpen(true)
        }
        setStatus(`Approved Managed Host ${target?.name || request.managed_name || request.managed_swarm_id || requestID}. Workspace link/import review is ready.`)
      } else {
        setPendingLinkReviewTarget(null)
        setStatus(`Rejected pairing request ${requestID}.`)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update pairing request')
    } finally {
      setPairingDecisionBusyID(null)
    }
  }

  const handleStartLocalNameEdit = () => {
    setLocalNameDraft(localSwarmName)
    setError(null)
    setStatus(null)
    setEditingLocalName(true)
  }

  const handleSaveLocalName = async () => {
    const normalized = localNameDraft.trim()
    if (!normalized) {
      setError('Swarm name is required.')
      return
    }
    if (!uiSettings) {
      setError('Swarm settings are not loaded yet.')
      return
    }
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const savedSettings = await saveSwarmSettings({ name: normalized })
      const savedName = savedSettings.name.trim() || normalized
      setUISettings(savedSettings.raw)
      setSwarmState((current) => current
        ? { ...current, node: { ...current.node, name: savedName } }
        : current)
      setOnboardingStatus((current) => current
        ? { ...current, config: { ...current.config, swarmName: savedName } }
        : current)
      setLocalNameDraft(savedName)
      const nextTargets = await fetchSwarmTargets()
      setSwarmTargets(nextTargets.targets)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['ui-settings'] }),
        queryClient.invalidateQueries({ queryKey: ['ui-settings', 'swarm'] }),
        queryClient.invalidateQueries({ queryKey: ['swarm-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['workspace-overview'] }),
      ])
      window.dispatchEvent(new CustomEvent('swarm:name-updated', { detail: { name: savedName } }))
      await refresh()
      setEditingLocalName(false)
      setStatus(`Saved swarm name as ${savedName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save swarm name')
    } finally {
      setBusy(false)
    }
  }

  const handleHideLocalRuntimeWarning = async () => {
    setHidingLocalRuntimeWarning(true)
    setError(null)
    try {
      const saved = await saveLocalContainerUpdateWarningDismissal(true)
      setUISettings(saved)
      queryClient.setQueryData(uiSettingsQueryKey(), saved)
      queryClient.setQueryData(['ui-settings', 'swarm'], normalizeSwarmSettings(saved))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save Docker warning preference')
    } finally {
      setHidingLocalRuntimeWarning(false)
    }
  }

  const openAddSwarm = () => {
    setAddSwarmOpen(true)
    setError(null)
    setStatus(null)
  }

  const openLinkSwarm = () => {
    setLinkSwarmOpen(true)
    setError(null)
    setStatus(null)
  }


  const openDeleteContainers = () => {
    setDeleteResult(null)
    setSelectedDeleteContainerIDs([])
    setDeleteContainersOpen(true)
    setError(null)
    setStatus(null)
  }

  const openDeleteSwarms = (candidateContainerIDs: string[], selectedContainerIDs: string[]) => {
    setDeleteSwarmCandidateContainerIDs(candidateContainerIDs)
    setSelectedDeleteSwarmContainerIDs(selectedContainerIDs)
    setDeleteSwarmRemoteMode('teardown')
    setDeleteSwarmsOpen(true)
    setError(null)
    setStatus(null)
  }

  const toggleDeleteContainer = (id: string) => {
    const candidate = deleteCandidates.find((item) => item.selectionID === id)
    if (candidate && !candidate.canDelete) {
      return
    }
    setSelectedDeleteContainerIDs((current) => (
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id]
    ))
  }

  const toggleDeleteSwarm = (id: string) => {
    setSelectedDeleteSwarmContainerIDs((current) => (
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id]
    ))
  }

  const unassignImpactedFlows = async (impactedFlows: FlowSummaryRecord[]): Promise<number> => {
    const flowIDs = Array.from(new Set(impactedFlows.map((flow) => flow.definition.flow_id.trim()).filter(Boolean)))
    await Promise.all(flowIDs.map((flowID) => updateFlow(flowID, { enabled: false, target: {}, unassign_target: true })))
    if (flowIDs.length > 0) {
      setFlows((current) => current.map((flow) => (
        flowIDs.includes(flow.definition.flow_id.trim())
          ? { ...flow, definition: { ...flow.definition, enabled: false, target: {} }, target_detail: null, assignment_statuses: [] }
          : flow
      )))
    }
    return flowIDs.length
  }

  const handleDeleteContainers = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const selectedCandidates = deleteCandidates.filter((candidate) => selectedDeleteIDs.has(candidate.selectionID) && candidate.canDelete)
      if (selectedCandidates.length === 0) {
        setStatus('No containers were selected.')
        return
      }
      const removedFlowCount = await unassignImpactedFlows(selectedCandidates.flatMap((candidate) => candidate.flows))
      const localContainerIDs = selectedCandidates
        .filter((candidate) => candidate.kind === 'local' && !candidate.attachment?.id)
        .map((candidate) => candidate.container.id)
      const deploymentIDs = selectedCandidates
        .map((candidate) => candidate.attachment?.id || '')
        .filter((id) => id.trim() !== '')
      const staleManagedByHost = new Map<string, { hostName: string; backendURL?: string; ids: string[]; mirrorIDs: string[] }>()
      selectedCandidates
        .filter((candidate) => candidate.kind === 'managed-host' && !candidate.attachment?.id)
        .forEach((candidate) => {
          const deleteID = candidate.container.id || candidate.container.containerID || candidate.container.containerName || candidate.mirrorResourceID || ''
          if (!deleteID.trim()) {
            return
          }
          const current = staleManagedByHost.get(candidate.hostSwarmID) ?? { hostName: candidate.hostName, backendURL: candidate.managedHostBackendURL, ids: [], mirrorIDs: [] }
          current.ids.push(deleteID)
          if (candidate.mirrorResourceID) {
            current.mirrorIDs.push(candidate.mirrorResourceID)
          }
          staleManagedByHost.set(candidate.hostSwarmID, current)
        })

      const staleManagedDeletes = Array.from(staleManagedByHost.entries()).map(async ([managedSwarmID, group]) => {
        try {
          const result = await deleteManagedHostLocalContainersViaManager({
            managedSwarmID,
            managedHostName: group.hostName,
            backendURL: group.backendURL,
            ids: group.ids,
          })
          if (group.mirrorIDs.length > 0) {
            await deleteSwarmMirrorResources({ managedSwarmID, kind: 'container', ids: group.mirrorIDs })
          }
          return result
        } catch (err) {
          if (isMissingManagedHostContainerDeleteError(err) && group.mirrorIDs.length > 0) {
            await deleteSwarmMirrorResources({ managedSwarmID, kind: 'container', ids: group.mirrorIDs })
            return { deleted: group.ids, count: group.ids.length, failed: 0, childInfoRemoved: 0, items: [] }
          }
          throw err
        }
      })

      const [localOutcome, deploymentOutcome, staleManagedOutcome] = await Promise.allSettled([
        localContainerIDs.length > 0 ? deleteSwarmLocalContainers(localContainerIDs) : Promise.resolve(null),
        deploymentIDs.length > 0 ? deleteDeployContainers(deploymentIDs) : Promise.resolve(null),
        staleManagedDeletes.length > 0 ? Promise.all(staleManagedDeletes) : Promise.resolve([]),
      ])
      await refresh()
      setSelectedDeleteContainerIDs([])
      setDeleteContainersOpen(false)

      const messages: string[] = []
      const failures: string[] = []
      let combinedCount = 0
      let combinedFailed = 0
      let combinedChildInfoRemoved = 0

      if (localOutcome.status === 'fulfilled' && localOutcome.value) {
        combinedCount += localOutcome.value.count
        combinedFailed += localOutcome.value.failed
        combinedChildInfoRemoved += localOutcome.value.childInfoRemoved
        messages.push(localOutcome.value.failed > 0
          ? `Deleted ${localOutcome.value.count} local container${localOutcome.value.count === 1 ? '' : 's'} with ${localOutcome.value.failed} failure${localOutcome.value.failed === 1 ? '' : 's'}.`
          : `Deleted ${localOutcome.value.count} local container${localOutcome.value.count === 1 ? '' : 's'}.`)
      } else if (localOutcome.status === 'rejected') {
        failures.push(localOutcome.reason instanceof Error ? localOutcome.reason.message : 'Failed to delete local containers')
      }

      if (deploymentOutcome.status === 'fulfilled' && deploymentOutcome.value) {
        combinedCount += deploymentOutcome.value.count
        combinedFailed += deploymentOutcome.value.failed
        combinedChildInfoRemoved += deploymentOutcome.value.childInfoRemoved
        messages.push(deploymentOutcome.value.failed > 0
          ? `Deleted ${deploymentOutcome.value.count} managed container${deploymentOutcome.value.count === 1 ? '' : 's'} with ${deploymentOutcome.value.failed} failure${deploymentOutcome.value.failed === 1 ? '' : 's'}.`
          : `Deleted ${deploymentOutcome.value.count} managed container${deploymentOutcome.value.count === 1 ? '' : 's'} and cleaned linked records.`)
      } else if (deploymentOutcome.status === 'rejected') {
        failures.push(deploymentOutcome.reason instanceof Error ? deploymentOutcome.reason.message : 'Failed to delete managed containers')
      }

      if (staleManagedOutcome.status === 'fulfilled') {
        staleManagedOutcome.value.forEach((result) => {
          combinedCount += result.count
          combinedFailed += result.failed
          combinedChildInfoRemoved += result.childInfoRemoved
        })
        if (staleManagedOutcome.value.length > 0) {
          const deleted = staleManagedOutcome.value.reduce((sum, result) => sum + result.count, 0)
          const failed = staleManagedOutcome.value.reduce((sum, result) => sum + result.failed, 0)
          messages.push(failed > 0
            ? `Deleted ${deleted} stale managed-host container${deleted === 1 ? '' : 's'} with ${failed} failure${failed === 1 ? '' : 's'}.`
            : `Deleted ${deleted} stale managed-host container${deleted === 1 ? '' : 's'} and removed mirrored records.`)
        }
      } else if (staleManagedOutcome.status === 'rejected') {
        failures.push(staleManagedOutcome.reason instanceof Error ? staleManagedOutcome.reason.message : 'Failed to delete stale managed-host containers')
      }

      if (removedFlowCount > 0) {
        messages.push(`Unassigned and turned off ${removedFlowCount} assigned flow${removedFlowCount === 1 ? '' : 's'} first.`)
      }
      if (combinedChildInfoRemoved > 0) {
        messages.push(`Removed linked child info for ${combinedChildInfoRemoved}.`)
      }
      setDeleteResult({ deleted: [], count: combinedCount, failed: combinedFailed, childInfoRemoved: combinedChildInfoRemoved, items: [] })
      if (messages.length > 0) {
        setStatus(messages.join(' '))
      }
      if (failures.length > 0) {
        setError(failures.join(' '))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete selected containers')
    } finally {
      setBusy(false)
    }
  }

  const handleRemoveLocalManagedHostLink = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const managerPeer = swarmState?.trusted_peers.find((peer) => peer.swarm_id === localManagerSwarmID) ?? null
      const result = await removeManagedHostLink({
        managerSwarmID: localManagerSwarmID,
        endpoint: managerPeer?.rendezvous_transports?.find((transport) => transport.kind === 'tailscale')?.primary,
        transportMode: managerPeer?.transport_mode,
        rendezvousTransports: managerPeer?.rendezvous_transports,
        propagate: true,
        reason: 'Removed from Managed Host dashboard',
      })
      await refresh()
      setStatus(result.remoteError
        ? `Removed this Managed Host link locally. Manager cleanup did not complete: ${result.remoteError}`
        : 'Removed this Managed Host link on both hosts.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove Managed Host link')
    } finally {
      setBusy(false)
    }
  }

  const handleRemoveManagedHost = async (member: DesktopSwarmGroupMember, target: SwarmTarget | null) => {
    const managedSwarmID = member.swarmID.trim()
    if (!managedSwarmID) {
      setError('Managed Host swarm id is missing.')
      return
    }
    setRemovingManagedHostID(managedSwarmID)
    setError(null)
    setStatus(null)
    try {
      const result = await removeManagedHostLink({
        managedSwarmID,
        endpoint: target?.backend_url,
        propagate: true,
        reason: 'Removed from Manager dashboard',
      })
      await refresh()
      setStatus(result.remoteError
        ? `Removed ${member.name || managedSwarmID} from this Manager. Managed Host cleanup did not complete: ${result.remoteError}`
        : `Removed ${member.name || managedSwarmID} on both hosts.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to remove ${member.name || managedSwarmID}`)
    } finally {
      setRemovingManagedHostID(null)
    }
  }

  const handleDeleteSelectedSwarms = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const selectedCandidates = deleteSwarmCandidates.filter((candidate) => selectedDeleteSwarmIDs.has(candidate.selectionID))
      if (selectedCandidates.length === 0) {
        setStatus('No swarms were selected.')
        return
      }
      const removedFlowCount = await unassignImpactedFlows(selectedCandidates.flatMap((candidate) => {
        if (candidate.kind === 'local' && candidate.deployment) {
          return flowsByDeploymentID.get(candidate.deployment.id) ?? []
        }
        return flows.filter((flow) => flowMatchesContainerTarget(flow, { swarmID: candidate.swarmID }))
      }))
      const localDeploymentIDs = selectedCandidates
        .filter((candidate) => candidate.kind === 'local' && candidate.deployment?.id)
        .map((candidate) => candidate.deployment!.id)
      const selectedRemoteCandidates = selectedCandidates.filter((candidate) => candidate.kind !== 'local')
      const remoteSessionIDs = selectedCandidates
        .filter((candidate) => candidate.kind === 'remote' && candidate.remoteSession?.id)
        .map((candidate) => candidate.remoteSession!.id)
      const staleRemoteChildSwarmIDs = selectedCandidates
        .filter((candidate) => candidate.kind === 'stale-remote')
        .map((candidate) => candidate.swarmID)
      const selectedRemoteCandidatesSupportSSHDelete = selectedRemoteCandidates.length > 0
        && selectedRemoteCandidates.every(remoteDeleteCandidateSupportsSSHDelete)
      const remoteDeleteMode = selectedRemoteCandidatesSupportSSHDelete ? deleteSwarmRemoteMode : 'detach'

      const [localOutcome, remoteOutcome] = await Promise.allSettled([
        localDeploymentIDs.length > 0 ? deleteDeployContainers(localDeploymentIDs) : Promise.resolve(null),
        (remoteSessionIDs.length > 0 || staleRemoteChildSwarmIDs.length > 0)
          ? deleteRemoteDeploySessions({
              ids: remoteSessionIDs,
              childSwarmIDs: staleRemoteChildSwarmIDs,
              teardownRemote: remoteDeleteMode === 'teardown',
            })
          : Promise.resolve(null),
      ])
      await refresh()
      setDeleteSwarmsOpen(false)
      setDeleteSwarmCandidateContainerIDs([])
      setSelectedDeleteSwarmContainerIDs([])
      setDeleteSwarmRemoteMode('teardown')

      const messages: string[] = []
      const failures: string[] = []

      if (localOutcome.status === 'fulfilled' && localOutcome.value) {
        const localResult = localOutcome.value
        messages.push(
          localResult.failed > 0
            ? `Deleted ${localResult.count} local swarm${localResult.count === 1 ? '' : 's'} with ${localResult.failed} failure${localResult.failed === 1 ? '' : 's'}.`
            : `Deleted ${localResult.count} local swarm${localResult.count === 1 ? '' : 's'}.${localResult.childInfoRemoved > 0 ? ` Removed linked child info for ${localResult.childInfoRemoved}.` : ''}`,
        )
      } else if (localOutcome.status === 'rejected') {
        failures.push(localOutcome.reason instanceof Error ? localOutcome.reason.message : 'Failed to delete local swarms')
      }

      if (remoteOutcome.status === 'fulfilled' && remoteOutcome.value) {
        const remoteResult = remoteOutcome.value
        messages.push(
          remoteDeleteMode === 'teardown'
            ? (
                remoteResult.failed > 0
                  ? `Deleted ${remoteResult.count} remote swarm${remoteResult.count === 1 ? '' : 's'} over SSH with ${remoteResult.failed} failure${remoteResult.failed === 1 ? '' : 's'}.`
                  : `Deleted ${remoteResult.count} remote swarm${remoteResult.count === 1 ? '' : 's'} over SSH and removed the linked records from this master.`
              )
            : (
                remoteResult.failed > 0
                  ? `Removed ${remoteResult.count} remote swarm record${remoteResult.count === 1 ? '' : 's'} from this master with ${remoteResult.failed} failure${remoteResult.failed === 1 ? '' : 's'}.`
                  : `Removed ${remoteResult.count} remote swarm record${remoteResult.count === 1 ? '' : 's'} from this master.`
              ),
        )
      } else if (remoteOutcome.status === 'rejected') {
        failures.push(remoteOutcome.reason instanceof Error ? remoteOutcome.reason.message : 'Failed to remove selected remote swarms')
      }

      if (removedFlowCount > 0) {
        messages.push(`Unassigned and turned off ${removedFlowCount} assigned flow${removedFlowCount === 1 ? '' : 's'} before deleting swarms.`)
      }
      if (messages.length > 0) {
        setStatus(messages.join(' '))
      }
      if (failures.length > 0) {
        setError(failures.join(' '))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete selected swarms')
    } finally {
      setBusy(false)
    }
  }

  const handleAddSwarmComplete = async (message: string) => {
    await refresh()
    setError(null)
    setStatus(message)
  }

  const handleSaveManagedSwarmSettings = async (input: { syncEnabled: boolean; syncModules: string[]; bypassPermissions: boolean }) => {
    if (!settingsDeployment) {
      return
    }
    setSettingsSaving(true)
    setSettingsError(null)
    try {
      const updated = await updateDeployContainerSettings({
        id: settingsDeployment.id,
        syncEnabled: input.syncEnabled,
        syncModules: input.syncModules,
        bypassPermissions: input.bypassPermissions,
      })
      setDeployments((current) => current.map((deployment) => (deployment.id === updated.id ? updated : deployment)))
      setSettingsDeployment(null)
      setStatus('Managed swarm settings updated.')
      await refresh()
    } catch (err) {
      setSettingsError(err instanceof Error ? err.message : 'Failed to update managed swarm settings')
    } finally {
      setSettingsSaving(false)
    }
  }

  const handleDeploymentAction = async (deployment: DeployContainerDeployment, action: 'start' | 'stop') => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      await actOnDeployContainer({ id: deployment.id, action })
      await refresh()
      setStatus(`${action === 'start' ? 'Started' : 'Stopped'} ${deployment.name}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to ${action} ${deployment.name}`)
    } finally {
      setBusy(false)
    }
  }

  const handleLocalContainerAction = async (container: SwarmLocalContainer, action: 'start' | 'stop') => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      await actOnSwarmLocalContainer({ id: container.id, action })
      await refresh()
      setStatus(`${action === 'start' ? 'Started' : 'Stopped'} ${container.name}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to ${action} ${container.name}`)
    } finally {
      setBusy(false)
    }
  }

  const handleRemoveMissingLocalContainer = async (container: SwarmLocalContainer) => {
    const attachedDeployment = deployments.find((deployment) => (
      String(deployment.attach_status ?? '').trim() === 'attached'
      && deploymentMatchesContainer(deployment, container)
    )) ?? null
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const result = attachedDeployment
        ? await deleteDeployContainers([attachedDeployment.id])
        : await deleteSwarmLocalContainers([container.id])
      await refresh()
      const targetName = attachedDeployment?.child_display_name || container.name
      setStatus(result.count > 0
        ? `Removed stale entry for ${targetName}.`
        : `No stale entry removed for ${targetName}.`)
    } catch (err) {
      const targetName = attachedDeployment?.child_display_name || container.name
      setError(err instanceof Error ? err.message : `Failed to remove stale entry for ${targetName}`)
    } finally {
      setBusy(false)
    }
  }

  const handlePruneMissingLocalContainers = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const result = await pruneMissingSwarmLocalContainers()
      await refresh()
      setStatus(result.count > 0 ? `Removed ${result.count} stale local swarm ${result.count === 1 ? 'entry' : 'entries'}.` : 'No stale local swarm entries found.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove stale local swarm entries')
    } finally {
      setBusy(false)
    }
  }

  const handleDeleteAttachedSwarm = async (deployment: DeployContainerDeployment, fallbackName?: string, fallbackSwarmID?: string) => {
    const attachedContainer = localContainers.find((container) => deploymentMatchesContainer(deployment, container)) ?? null
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const result = deployment.child_backend_url
        ? await deleteDeployContainersViaHost(deployment.child_backend_url, [deployment.id])
        : await deleteDeployContainers([deployment.id])
      await refresh()
      const targetName = fallbackName || deployment.child_display_name || attachedContainer?.name || deployment.name || deployment.id || fallbackSwarmID
      setStatus(result.count > 0
        ? `Deleted swarm ${targetName}.${result.childInfoRemoved > 0 ? ` Removed linked child info for ${result.childInfoRemoved}.` : ''}`
        : `No swarm was deleted for ${targetName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to delete swarm ${fallbackName || deployment.child_display_name || deployment.name || deployment.id}`)
    } finally {
      setBusy(false)
    }
  }

  const handleDeleteStaleAttachedDeployments = async () => {
    if (staleAttachedDeployments.length === 0) {
      return
    }
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const result = await deleteDeployContainers(staleAttachedDeployments.map((deployment) => deployment.id))
      await refresh()
      setStatus(result.count > 0
        ? `Removed ${result.count} stale attached swarm ${result.count === 1 ? 'record' : 'records'}.${result.childInfoRemoved > 0 ? ` Removed linked child info for ${result.childInfoRemoved}.` : ''}`
        : 'No stale attached swarm records were removed.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove stale attached swarm records')
    } finally {
      setBusy(false)
    }
  }

  const content = (
    <>
      <div className="space-y-3">
        <div className="space-y-2">
          <h1 className="text-2xl font-semibold">Swarm</h1>
          <p className="max-w-3xl text-sm text-[var(--app-text-muted)]">
            Manage this host, linked Managed Hosts, and local containers from one flat view.
          </p>
        </div>
        <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center">
          <Button type="button" variant="primary" data-testid="swarm-dashboard-add-container" className="w-full sm:w-auto" onClick={() => openAddSwarm()} disabled={addContainerDisabled}>
            <Plus size={14} />
            <span className="truncate">Add Container</span>
          </Button>
          <Button type="button" variant="outline" data-testid="swarm-dashboard-link-swarm" className="w-full sm:w-auto" onClick={() => openLinkSwarm()} disabled={managedHostingControlsDisabled} title={managedHostControlTitle || (localIsChild ? 'This host is already linked to a Manager.' : undefined)}>
            <Link2 size={14} />
            <span className="truncate">Link Host</span>
          </Button>
          {visiblePendingPairings.length > 0 || pendingLinkReviewTarget ? (
            <Button type="button" variant="primary" data-testid="swarm-dashboard-link-request" className="w-full sm:w-auto" onClick={() => setLinkRequestOpen(true)}>
              <Link2 size={14} />
              Link request{visiblePendingPairings.length > 0 ? ` (${visiblePendingPairings.length})` : ''}
            </Button>
          ) : null}
        </div>
      </div>

      {error || status || pendingLinkReviewTarget || visiblePendingPairings.length > 0 ? (
        <div className="mt-4 space-y-3">
          {error ? <Card data-testid="swarm-dashboard-error" className="border-[var(--app-danger-border)] bg-transparent p-4 text-sm text-[var(--app-danger)]">{error}</Card> : null}
          {status ? <Card data-testid="swarm-dashboard-status" className="border-[var(--app-success-border)] bg-transparent p-4 text-sm text-[var(--app-success)]">{status}</Card> : null}
          {pendingLinkReviewTarget || visiblePendingPairings.length > 0 ? (
            <Card data-testid="swarm-link-request-summary" className="border-[var(--app-warning-border)] bg-transparent p-4">
              <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                <div>
                  <div className="text-sm font-semibold text-[var(--app-text)]">
                    {pendingLinkReviewTarget ? 'Workspace link/import review pending' : 'Pending Managed Host request'}
                  </div>
                  <div className="mt-1 text-sm text-[var(--app-text-muted)]">
                    {pendingLinkReviewTarget
                      ? `${pendingLinkReviewTarget.name || pendingLinkReviewTarget.swarm_id} is trusted. Open the link modal to review live inventory, link existing workspaces, or transfer missing ones.`
                      : 'Confirm the 6-character code in the link request modal before approving.'}
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone="warning">{pendingLinkReviewTarget ? 'Review pending' : `${visiblePendingPairings.length} pending`}</Badge>
                  <Button size="sm" onClick={() => setLinkRequestOpen(true)} disabled={busy}>Link request</Button>
                </div>
              </div>
            </Card>
          ) : null}
        </div>
      ) : null}

      <div className="grid min-w-0 gap-4 pt-4 sm:gap-6">
        <section className="min-w-0 rounded-2xl border border-[var(--app-border)] bg-transparent p-4 pb-6 sm:p-6 sm:pb-8">
          <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
            <div className="min-w-0 flex items-start gap-3 overflow-hidden">
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-[var(--app-border)] text-[var(--app-text-muted)]">
                <Monitor size={20} />
              </div>
              <div className="min-w-0">
                {editingLocalName ? (
                  <form
                    className="grid gap-2 sm:flex sm:flex-wrap sm:items-center"
                    onSubmit={(event) => {
                      event.preventDefault()
                      void handleSaveLocalName()
                    }}
                  >
                    <Input value={localNameDraft} onChange={(event) => setLocalNameDraft(event.target.value)} className="w-full min-w-0 sm:w-[280px]" autoFocus aria-label="Swarm name" />
                    <Button type="submit" disabled={busy || !localNameDirty}>Save</Button>
                    <Button type="button" variant="outline" onClick={() => { setLocalNameDraft(localSwarmName); setEditingLocalName(false) }} disabled={busy}>Cancel</Button>
                  </form>
                ) : (
                  <button
                    type="button"
                    className="max-w-full truncate rounded-md text-left text-lg font-semibold text-[var(--app-text)] hover:text-[var(--app-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
                    onClick={handleStartLocalNameEdit}
                    disabled={busy}
                    aria-label="Edit swarm name"
                    title="Click to rename swarm"
                  >
                    {localSwarmName}
                  </button>
                )}
                <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
                  <Badge tone={localIsMaster ? 'live' : localIsManagedHost ? 'live' : localIsChild ? 'warning' : 'neutral'}>{localSwarmRoleLabel}</Badge>
                  <Badge tone={hasPeerGroup ? 'live' : 'neutral'}>{hasPeerGroup ? 'Peer group ready' : 'No peer group'}</Badge>
                  <Badge tone={localTailscaleHosting.tone}>{localTailscaleHosting.summary}</Badge>
                  {localManagedLinked ? <Badge tone="neutral">Manager: {localManagerDisplay}</Badge> : null}
                  {localManagedLinked && localPairingState ? <Badge tone="neutral">Pairing: {formatUnderscoreLabel(localPairingState)}</Badge> : null}
                  {localManagedLinked ? <Badge tone={onboardingStatus?.pairing.managedAuthLastError ? 'warning' : onboardingStatus?.pairing.managedAuthSnapshotHash ? 'live' : 'neutral'}>{onboardingStatus?.pairing.managedAuthLastError ? 'Managed sync error' : onboardingStatus?.pairing.managedAuthSnapshotHash ? 'Managed sync current' : 'Managed sync pending'}</Badge> : null}
                </div>
              </div>
            </div>
            <div className="grid gap-1 text-xs text-[var(--app-text-muted)] lg:text-right">
              <div>Swarm ID: <span className="break-all font-mono text-[var(--app-text)]">{localSwarmID || 'not assigned yet'}</span></div>
              <div>Backend: <span className="break-all font-mono text-[var(--app-text)]">{configuredHost}:{backendPort}</span></div>
              <div>Desktop: <span className="break-all font-mono text-[var(--app-text)]">:{desktopPort}</span></div>
            </div>
          </div>

          {localManagedLinked ? (
            <div className="mt-4 rounded-xl border border-[var(--app-border)] bg-transparent p-3 text-sm text-[var(--app-text-muted)]">
              <div>This host is linked to {localManagerDisplay}{localManagerSwarmID ? ` (${localManagerSwarmID})` : ''}. Manager-only controls are disabled here. Managed sync pulls credentials/API keys, agents, custom tools, skills, and permissions from the Manager.</div>
              <div className="mt-2 grid gap-1 text-xs">
                <div>Snapshot: <span className="font-mono text-[var(--app-text)]">{compactSnapshotHash(onboardingStatus?.pairing.managedAuthSnapshotHash) || 'pending'}</span></div>
                <div>Last applied: <span className="text-[var(--app-text)]">{formatManagedSyncTimestamp(onboardingStatus?.pairing.managedAuthAppliedAt ?? 0)}</span></div>
                {onboardingStatus?.pairing.managedAuthLastError ? <div className="text-[var(--app-warning-text)]">{onboardingStatus.pairing.managedAuthLastError}</div> : null}
              </div>
              <div className="mt-3">
                <Button type="button" variant="outline" size="sm" className="text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)]" onClick={() => void handleRemoveLocalManagedHostLink()} disabled={busy}>
                  Remove Managed Host link
                </Button>
              </div>
            </div>
          ) : null}

          {loading && onboardingStatus === null ? (
            <div className="mt-4 rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">Loading swarm configuration…</div>
          ) : !hasPeerGroup ? (
            <div className="mt-4 rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No peer group exists yet. Local containers still work; use Link a Managed Host or create a peer group when you want to connect another machine.</div>
          ) : null}

          <div className="mt-5 grid gap-4 lg:grid-cols-[1.2fr_1fr]">
            <div className="space-y-3">
              <div>
                <div className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--app-text-muted)]">swarm.conf</div>
                <div className="mt-3 grid min-w-0 gap-3 text-sm sm:grid-cols-2">
                  <div>
                    <div className="text-xs text-[var(--app-text-muted)]">Who can reach this host</div>
                    <div className="font-medium text-[var(--app-text)]">{localBindStatus}</div>
                    <div className="text-xs text-[var(--app-text-muted)]">Listening on {configuredHost}:{backendPort}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--app-text-muted)]">Advertised to swarms</div>
                    <div className="break-all font-medium text-[var(--app-text)]">{(onboardingStatus?.config.advertiseHost || backendHost)}:{onboardingStatus?.config.advertisePort || backendPort}</div>
                    <div className="text-xs text-[var(--app-text-muted)]">Mode: {localTailscalePrimary ? 'Tailscale' : formatUnderscoreLabel(onboardingStatus?.config.mode || swarmState?.node.advertise_mode || 'lan')}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--app-text-muted)]">Desktop link</div>
                    <a href={frontendOrigin} target="_blank" rel="noreferrer" className="break-all font-medium text-[var(--app-primary)] hover:underline">{compactURL(frontendOrigin) || 'Unavailable'}</a>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--app-text-muted)]">Backend API</div>
                    <a href={backendURL} target="_blank" rel="noreferrer" className="break-all font-medium text-[var(--app-primary)] hover:underline">{compactURL(backendURL)}</a>
                  </div>
                </div>
              </div>
            </div>

            <div className="space-y-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Tailscale Serve</div>
                  <div className="mt-1 text-sm font-medium text-[var(--app-text)]">{localTailscaleHosting.summary}</div>
                </div>
                <Badge tone={localTailscaleHosting.tone}>{localTailscaleHosting.badge}</Badge>
              </div>
              <div className="text-sm text-[var(--app-text-muted)]">{localTailscaleHosting.detail}</div>
              {localTailscaleURL ? <AccessURLRow label="Tailnet link" url={localTailscaleURL} /> : null}
              {tailscaleCandidate.available ? (
                <div className="grid gap-2 sm:flex sm:items-center">
                  <div className="min-w-0 break-all rounded-lg border border-[var(--app-border)] px-3 py-2 font-mono text-[11px] text-[var(--app-text)] sm:flex-1 sm:truncate" title={desktopServeCommand}>{desktopServeCommand}</div>
                  <Button type="button" variant="outline" size="sm" className="h-8 text-[11px]" onClick={() => void handleCopyTailscaleCommand(desktopServeCommand)}>
                    {copyState === 'desktop' ? 'Copied' : 'Copy'}
                  </Button>
                </div>
              ) : null}
            </div>
          </div>

          <div className="mt-6 min-w-0 border-t border-[var(--app-border)] pt-4">
            <div className="grid gap-3 sm:flex sm:flex-wrap sm:items-center sm:justify-between">
              <div>
                <div className="text-sm font-semibold text-[var(--app-text)]">Containers on this host</div>
                <div className="text-xs text-[var(--app-text-muted)]">Container swarms running on {localSwarmName}.</div>
              </div>
              <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center">
                <Button type="button" variant="outline" size="sm" className="w-full sm:w-auto" onClick={openDeleteContainers} disabled={busy || deleteCandidates.length === 0}>
                  <CheckSquare size={14} />
                  Delete containers
                </Button>
                {staleLocalContainers.length > 0 ? (
                  <Button type="button" variant="outline" size="sm" className="w-full sm:w-auto" onClick={() => void handlePruneMissingLocalContainers()} disabled={busy}>Remove stale</Button>
                ) : null}
                {runtimeLoading ? <Badge tone="neutral">detecting runtime…</Badge> : null}
              </div>
            </div>

            {localRuntime.warning && uiSettings && !localContainerUpdateWarningDismissed(uiSettings) ? (
              <div className="mt-3 rounded-xl border border-[var(--app-warning-border)] bg-transparent px-3 py-2 text-sm text-[var(--app-warning-text)]">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                  <div className="flex items-start gap-2"><TriangleAlert size={16} className="mt-0.5 shrink-0" /><div>{localRuntime.warning}</div></div>
                  <Button type="button" variant="outline" size="sm" className="h-8 text-[11px]" onClick={() => void handleHideLocalRuntimeWarning()} disabled={busy || hidingLocalRuntimeWarning}>
                    Hide
                  </Button>
                </div>
              </div>
            ) : null}

            <div className="mt-3 grid gap-2">
              {localContainersSectionLoading ? (
                <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">Loading local containers…</div>
              ) : visibleLocalContainers.length === 0 ? (
                <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No containers yet.</div>
              ) : (
                visibleLocalContainers.map((container) => {
                  const attachedDeployment = deployments.find((deployment) => (
                    String(deployment.attach_status ?? '').trim() === 'attached'
                    && deploymentMatchesContainer(deployment, container)
                  )) ?? null
                  const running = container.status === 'running'
                  const missing = container.status === 'missing'
                  const childDesktopURL = attachedDeployment?.child_desktop_url
                    || urlForHostPort(browserProtocol, browserHost, attachedDeployment?.desktop_host_port || (container.hostPort > 0 ? container.hostPort + 1 : 0))
                  const childAPIURL = attachedDeployment?.child_backend_url
                    || urlForHostPort(browserProtocol, browserHost, attachedDeployment?.backend_host_port || container.hostPort)
                  const containerAction = attachedDeployment
                    ? () => handleDeploymentAction(attachedDeployment, running ? 'stop' : 'start')
                    : () => handleLocalContainerAction(container, running ? 'stop' : 'start')
                  const mountedWorkspaces = attachedDeployment?.workspace_bootstrap?.map(summarizeWorkspaceBootstrap) ?? container.mounts.map(summarizeContainerMount)
                  const assignedFlows = flowsByLocalContainerID.get(container.id) ?? []
                  return (
                    <div key={container.id} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                      <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
                        <div className="min-w-0 flex items-start gap-2 overflow-hidden">
                          <Boxes size={16} className="mt-0.5 shrink-0 text-[var(--app-text-muted)]" />
                          <div className="min-w-0">
                            <div className="truncate text-sm font-semibold text-[var(--app-text)]">{containerLocationLabel(localSwarmName, container.containerName || container.name)}</div>
                            <div className="break-words text-xs text-[var(--app-text-muted)]">Swarm: {attachedDeployment?.child_display_name || container.name} · Container: {container.containerName} · {container.runtime || 'runtime unknown'} · API {container.runtimePort || 'auto'}</div>
                          </div>
                        </div>
                        <div className="grid grid-cols-2 items-center gap-2 sm:flex sm:flex-wrap md:justify-end">
                          {attachedDeployment ? <Badge tone="live">{attachedDeployment.child_display_name || attachedDeployment.child_swarm_id || 'attached swarm'}</Badge> : null}
                          <Badge tone={running ? 'live' : missing ? 'warning' : 'neutral'}>{container.status || 'created'}</Badge>
                          {childDesktopURL ? <a href={childDesktopURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">Desktop</a> : null}
                          {childAPIURL ? <a href={childAPIURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">API</a> : null}
                          {missing ? (
                            <Button type="button" variant="outline" size="sm" className="col-span-2 w-full sm:col-span-1 sm:w-auto" onClick={() => void handleRemoveMissingLocalContainer(container)} disabled={busy}>Remove stale</Button>
                          ) : (
                            <Button type="button" variant="outline" size="sm" className="col-span-2 w-full sm:col-span-1 sm:w-auto" onClick={() => void containerAction()} disabled={busy}>{running ? 'Stop' : 'Start'}</Button>
                          )}
                        </div>
                      </div>
                      <MountedWorkspaceDetails items={mountedWorkspaces} />
                      <FlowImpactDetails flows={assignedFlows} />
                      {container.warning ? <div className="mt-2 rounded-lg border border-[var(--app-warning-border)] px-3 py-2 text-xs text-[var(--app-warning-text)]">{container.warning}</div> : null}
                    </div>
                  )
                })
              )}
            </div>
            <div className="mt-3 text-xs text-[var(--app-text-muted)]">To remount containers with new paths, please delete the old container and recreate a new one.</div>
          </div>
        </section>

        <section className="min-w-0 rounded-2xl border border-[var(--app-border)] bg-transparent p-4 sm:p-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Linked Managed Hosts</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Hosts linked to this Swarm. Container swarms are listed under the host where they run.</p>
            </div>
            <Badge tone="neutral">{managedHostMembers.length}</Badge>
          </div>

          {managedHostMembers.length > 0 ? (
            <div className="mt-4 grid gap-4">
              {managedHostMembers.map((member) => {
                const target = swarmTargetByID(swarmTargets, member.swarmID)
                const hostName = member.name || target?.name || member.swarmID
                const hostDesktopURL = target?.desktop_url || ''
                const hostAPIURL = target?.backend_url || ''
                const hostContainers = remoteContainerRowsByHostSwarmID.get(member.swarmID) ?? []
                const mirroredHostContainers = mirroredContainersByHostSwarmID.get(member.swarmID) ?? []
                const mirroredHostWorkspaces = mirroredWorkspacesByHostSwarmID.get(member.swarmID) ?? []
                const totalHostContainers = hostContainers.length + mirroredHostContainers.length
                return (
                  <div key={`${member.groupID}:${member.swarmID}`} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-transparent p-3 sm:p-4">
                    <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-start">
                      <div className="min-w-0 flex items-start gap-3 overflow-hidden">
                        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl border border-[var(--app-border)] text-[var(--app-text-muted)]">
                          <Monitor size={18} />
                        </div>
                        <div className="min-w-0">
                          <div className="truncate text-sm font-semibold text-[var(--app-text)]">{hostName}</div>
                          <div className="mt-1 flex flex-wrap items-center gap-2 text-xs">
                            <Badge tone="live">Managed Host</Badge>
                            <Badge tone="neutral">paired</Badge>
                            <Badge tone="live">Managed sync linked</Badge>
                            {target && !target.online ? <Badge tone="warning">stale record</Badge> : null}
                          </div>
                          <div className="mt-2 grid min-w-0 gap-1 text-xs text-[var(--app-text-muted)]">
                            <div>Swarm ID: <span className="break-all font-mono text-[var(--app-text)]">{member.swarmID}</span></div>
                            <div>Runtime: host · Relationship: managed</div>
                            <div>Mirrored resources: {mirroredHostWorkspaces.length} workspace{mirroredHostWorkspaces.length === 1 ? '' : 's'} · {mirroredHostContainers.length} container{mirroredHostContainers.length === 1 ? '' : 's'}</div>
                            {target?.last_error ? <div className="text-[var(--app-warning-text)]">{target.last_error}</div> : null}
                          </div>
                        </div>
                      </div>
                      <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap lg:justify-end">
                        {hostDesktopURL ? <a href={hostDesktopURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">Desktop</a> : null}
                        {hostAPIURL ? <a href={hostAPIURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">API</a> : null}
                        <Button type="button" variant="outline" size="sm" className="col-span-2 w-full text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)] sm:col-span-1 sm:w-auto" onClick={() => void handleRemoveManagedHost(member, target)} disabled={busy || removingManagedHostID === member.swarmID}>
                          {removingManagedHostID === member.swarmID ? 'Removing…' : 'Remove Host'}
                        </Button>
                      </div>
                    </div>

                    <div className="mt-4 border-t border-[var(--app-border)] pt-4">
                      <div className="text-sm font-semibold text-[var(--app-text)]">Workspaces on {hostName}</div>
                      <div className="mt-2 grid gap-2">
                        {mirroredHostWorkspaces.length === 0 ? (
                          <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No workspaces mirrored for this host yet.</div>
                        ) : mirroredHostWorkspaces.map((workspace) => {
                          const resource = workspace.resource
                          const workspaceName = mirrorWorkspaceName(resource)
                          const directories = Array.isArray(resource.directories) ? resource.directories : []
                          return (
                            <div key={workspace.id} className="rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                              <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                                <div className="min-w-0">
                                  <div className="truncate text-sm font-semibold text-[var(--app-text)]">{workspaceName}</div>
                                  <div className="break-all text-xs text-[var(--app-text-muted)]">{resource.path}</div>
                                </div>
                                <div className="flex flex-wrap items-center gap-2">
                                  {resource.is_git_repo ? <Badge tone="live">git</Badge> : <Badge tone="neutral">folder</Badge>}
                                  {directories.length > 1 ? <Badge tone="neutral">{directories.length} dirs</Badge> : null}
                                </div>
                              </div>
                            </div>
                          )
                        })}
                      </div>
                    </div>

                    <div className="mt-4 border-t border-[var(--app-border)] pt-4">
                      <div className="text-sm font-semibold text-[var(--app-text)]">Containers on {hostName}</div>
                      <div className="mt-2 grid gap-2">
                        {totalHostContainers === 0 ? (
                          <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No container swarms reported for this host yet.</div>
                        ) : hostContainers.map((session) => {
                          const running = session.status === 'attached' || session.status === 'running'
                          const remoteDesktopURL = remoteTailnetVisitURL(session.remote_tailnet_url, session.remote_endpoint)
                          const remoteAPIURL = session.remote_endpoint || session.remote_tailnet_url || ''
                          const deleteCandidate = remoteDeleteSwarmCandidates.find((candidate) => candidate.swarmID === session.child_swarm_id) ?? null
                          const childName = session.child_name || session.name || session.child_swarm_id || 'container swarm'
                          const containerName = session.ssh_session_target || session.id
                          const mountedWorkspaces = session.preflight.payloads?.map(summarizeRemotePayload) ?? []
                          const assignedFlows = flows.filter((flow) => flowMatchesContainerTarget(flow, { swarmID: session.child_swarm_id }))
                          return (
                            <div key={session.id} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                              <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
                                <div className="min-w-0 flex items-start gap-2 overflow-hidden">
                                  <Boxes size={16} className="mt-0.5 shrink-0 text-[var(--app-text-muted)]" />
                                  <div className="min-w-0">
                                    <div className="truncate text-sm font-semibold text-[var(--app-text)]">{containerLocationLabel(hostName, containerName)}</div>
                                    <div className="break-words text-xs text-[var(--app-text-muted)]">Swarm: {childName} · Container: {containerName} · {session.remote_runtime || 'runtime unknown'}</div>
                                  </div>
                                </div>
                                <div className="grid grid-cols-2 items-center gap-2 sm:flex sm:flex-wrap md:justify-end">
                                  <Badge tone={running ? 'live' : 'neutral'}>{formatRemoteSessionStatus(session.status || 'unknown')}</Badge>
                                  {remoteDesktopURL ? <a href={remoteDesktopURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">Desktop</a> : null}
                                  {remoteAPIURL ? <a href={remoteAPIURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">API</a> : null}
                                  {deleteCandidate ? <Button type="button" variant="outline" size="sm" className="col-span-2 w-full text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)] sm:col-span-1 sm:w-auto" onClick={() => openDeleteSwarms([deleteCandidate.selectionID], [deleteCandidate.selectionID])} disabled={busy}>Remove</Button> : null}
                                </div>
                              </div>
                              <MountedWorkspaceDetails items={mountedWorkspaces} />
                              <FlowImpactDetails flows={assignedFlows} />
                              {session.last_error ? <div className="mt-2 rounded-lg border border-[var(--app-warning-border)] px-3 py-2 text-xs text-[var(--app-warning-text)]">{session.last_error}</div> : null}
                            </div>
                          )
                        })}
                        {mirroredHostContainers.map((mirroredContainer) => {
                          const container = mirroredContainer.resource
                          const attachedDeployment = [container.id, container.containerID, container.containerName]
                            .map((value) => mirroredDeploymentByContainerID.get(String(value ?? '').trim()) ?? null)
                            .find(Boolean) ?? null
                          const running = container.status === 'running'
                          const childName = attachedDeployment?.child_display_name || attachedDeployment?.child_swarm_id || container.name || 'container swarm'
                          const childDesktopURL = attachedDeployment?.child_desktop_url || ''
                          const childAPIURL = attachedDeployment?.child_backend_url || container.hostAPIBaseURL || ''
                          const mountedWorkspaces = attachedDeployment?.workspace_bootstrap?.map(summarizeWorkspaceBootstrap) ?? container.mounts.map(summarizeContainerMount)
                          const assignedFlows = flows.filter((flow) => flowMatchesContainerTarget(flow, {
                            deployment: attachedDeployment,
                            container,
                            swarmID: attachedDeployment?.child_swarm_id,
                            deploymentID: attachedDeployment?.id || container.id,
                          }))
                          return (
                            <div key={`mirrored:${mirroredContainer.managedSwarmID}:${mirroredContainer.id}`} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                              <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
                                <div className="min-w-0 flex items-start gap-2 overflow-hidden">
                                  <Boxes size={16} className="mt-0.5 shrink-0 text-[var(--app-text-muted)]" />
                                  <div className="min-w-0">
                                    <div className="truncate text-sm font-semibold text-[var(--app-text)]">{containerLocationLabel(hostName, container.containerName || container.name)}</div>
                                    <div className="break-words text-xs text-[var(--app-text-muted)]">Swarm: {childName} · Container: {container.containerName || container.id} · {container.runtime || attachedDeployment?.runtime || 'runtime unknown'} · API {container.runtimePort || attachedDeployment?.backend_host_port || 'auto'}</div>
                                  </div>
                                </div>
                                <div className="grid grid-cols-2 items-center gap-2 sm:flex sm:flex-wrap md:justify-end">
                                  {attachedDeployment ? <Badge tone="live">{attachedDeployment.child_display_name || attachedDeployment.child_swarm_id || 'attached swarm'}</Badge> : <Badge tone="neutral">mirrored</Badge>}
                                  <Badge tone={running ? 'live' : container.status === 'missing' ? 'warning' : 'neutral'}>{container.status || 'created'}</Badge>
                                  {childDesktopURL ? <a href={childDesktopURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">Desktop</a> : null}
                                  {childAPIURL ? <a href={childAPIURL} target="_blank" rel="noreferrer" className="inline-flex min-h-9 items-center justify-center rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">API</a> : null}
                                  <Button type="button" variant="outline" size="sm" className="col-span-2 w-full text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)] sm:col-span-1 sm:w-auto" onClick={() => {
                                    const localDeleteID = container.id || container.containerID || container.containerName || mirroredContainer.id
                                    setSelectedDeleteContainerIDs([`managed-host:${mirroredContainer.managedSwarmID}:${attachedDeployment?.id || localDeleteID}`])
                                    setDeleteResult(null)
                                    setDeleteContainersOpen(true)
                                    setError(null)
                                    setStatus(null)
                                  }} disabled={busy || !(attachedDeployment?.id || container.id || container.containerID || container.containerName || mirroredContainer.id)}>Delete</Button>
                                </div>
                              </div>
                              <MountedWorkspaceDetails items={mountedWorkspaces} />
                              <FlowImpactDetails flows={assignedFlows} />
                              {container.warning ? <div className="mt-2 rounded-lg border border-[var(--app-warning-border)] px-3 py-2 text-xs text-[var(--app-warning-text)]">{container.warning}</div> : null}
                              {attachedDeployment?.last_attach_error ? <div className="mt-2 rounded-lg border border-[var(--app-warning-border)] px-3 py-2 text-xs text-[var(--app-warning-text)]">{attachedDeployment.last_attach_error}</div> : null}
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          ) : (
            <div className="mt-4 rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No Managed Hosts are linked. Use Link a Managed Host to pair a trusted host; endpoint reachability and pairing trust are checked during linking.</div>
          )}
        </section>

        {staleRelationshipMembers.length > 0 ? (
          <section className="rounded-2xl border border-[var(--app-warning-border)] bg-transparent p-5">
            <div>
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Stale relationship records</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Group members without a matching Managed Host, local container deployment, or remote session.</p>
            </div>
            <div className="mt-4 grid gap-2">
              {staleRelationshipMembers.map((member) => {
                const staleRemoteDeleteCandidate = staleRemoteDeleteSwarmCandidates.find((candidate) => candidate.swarmID === member.swarmID) ?? null
                return (
                  <div key={`${member.groupID}:${member.swarmID}`} className="rounded-xl border border-[var(--app-warning-border)] bg-transparent p-3 text-sm">
                    <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                      <div className="min-w-0">
                        <div className="truncate font-medium text-[var(--app-text)]">{member.name || member.swarmID}</div>
                        <div className="text-xs text-[var(--app-text-muted)]">Swarm ID: {member.swarmID} · Role: {swarmRoleLabel(member.swarmRole || 'child')}</div>
                      </div>
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge tone="warning">stale record</Badge>
                        {staleRemoteDeleteCandidate ? <Button type="button" variant="outline" size="sm" onClick={() => openDeleteSwarms([staleRemoteDeleteCandidate.selectionID], [staleRemoteDeleteCandidate.selectionID])} disabled={busy}>Remove stale record</Button> : null}
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          </section>
        ) : null}

        {staleAttachedDeployments.length > 0 ? (
          <section className="rounded-2xl border border-[var(--app-warning-border)] bg-transparent p-5">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <h2 className="text-lg font-semibold text-[var(--app-text)]">Needs cleanup</h2>
                <p className="mt-1 text-sm text-[var(--app-text-muted)]">Attached swarm records that no longer match a current local container.</p>
              </div>
              <Button type="button" variant="outline" onClick={() => void handleDeleteStaleAttachedDeployments()} disabled={busy || staleAttachedLoading}>
                <Trash2 size={14} />
                Remove all stale
              </Button>
            </div>
            <div className="mt-4 grid gap-2">
              {staleAttachedDeployments.map((deployment) => {
                const deploymentGroupID = String(deployment.group_id ?? '').trim()
                const matchedContainer = localContainers.find((container) => deploymentMatchesContainer(deployment, container)) ?? null
                const childLabel = deployment.child_display_name || deployment.child_swarm_id || deployment.name || deployment.id
                return (
                  <div key={deployment.id} className="rounded-xl border border-[var(--app-warning-border)] bg-transparent p-3 text-sm">
                    <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                      <div className="min-w-0">
                        <div className="truncate font-medium text-[var(--app-text)]">{childLabel}</div>
                        <div className="text-xs text-[var(--app-text-muted)]">Saved swarm {deployment.child_swarm_id || 'unknown'} · Runtime {deployment.runtime || 'unknown'}{deploymentGroupID ? ` · old saved group ${deploymentGroupID}` : ''}</div>
                      </div>
                      <div className="flex flex-wrap items-center gap-2">
                        {!matchedContainer ? <Badge tone="warning">missing container</Badge> : null}
                        <Button type="button" variant="outline" size="sm" onClick={() => void handleDeleteAttachedSwarm(deployment, childLabel, deployment.child_swarm_id)} disabled={busy}>Remove stale record</Button>
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          </section>
        ) : null}
      </div>
    </>
  )

  return (
    <>
      {content}

      <AddSwarmModal
        open={addSwarmOpen}
        onboardingStatus={onboardingStatus}
        onOpenChange={setAddSwarmOpen}
        onComplete={handleAddSwarmComplete}
      />
      <LinkSwarmModal
        open={linkSwarmOpen}
        onboardingStatus={onboardingStatus}
        onOpenChange={setLinkSwarmOpen}
        onPairingSent={handleAddSwarmComplete}
        onOnboardingStatusChange={setOnboardingStatus}
      />
      <ManagedHostLinkRequestModal
        open={linkRequestOpen}
        requests={visiblePendingPairings}
        busyID={pairingDecisionBusyID}
        confirmations={pairingConfirmations}
        error={error}
        status={status}
        now={Date.now()}
        linkReviewTarget={pendingLinkReviewTarget}
        linkReviewBusy={busy}
        onOpenChange={setLinkRequestOpen}
        onRefresh={() => { void refresh() }}
        onConfirmationChange={(requestID, confirmed) => setPairingConfirmations((current) => ({ ...current, [requestID]: confirmed }))}
        onDecision={(request, approve) => { void handlePairingDecision(request, approve) }}
        onLinkReviewComplete={async (message) => {
          await refresh()
          setPendingLinkReviewTarget(null)
          setError(null)
          setStatus(message)
        }}
        onLinkReviewSkip={(message) => {
          setPendingLinkReviewTarget(null)
          setError(null)
          setStatus(message)
        }}
      />
      <ManagedSwarmSettingsDialog
        deployment={settingsDeployment}
        open={Boolean(settingsDeployment)}
        submitting={settingsSaving}
        error={settingsError}
        onClose={() => {
          if (settingsSaving) {
            return
          }
          setSettingsDeployment(null)
          setSettingsError(null)
        }}
        onSave={(input) => void handleSaveManagedSwarmSettings(input)}
      />
      <DeleteContainersModal
        open={deleteContainersOpen}
        busy={busy}
        candidates={deleteCandidates}
        selectedIDs={selectedDeleteIDs}
        result={deleteResult}
        onToggle={toggleDeleteContainer}
        onClose={() => {
          if (busy) {
            return
          }
          setDeleteContainersOpen(false)
        }}
        onConfirm={() => void handleDeleteContainers()}
      />
      <DeleteSwarmsModal
        open={deleteSwarmsOpen}
        busy={busy}
        candidates={deleteSwarmCandidates}
        selectedIDs={selectedDeleteSwarmIDs}
        remoteDeleteMode={deleteSwarmRemoteMode}
        onToggle={toggleDeleteSwarm}
        onRemoteDeleteModeChange={setDeleteSwarmRemoteMode}
        onClose={() => {
          if (busy) {
            return
          }
          setDeleteSwarmsOpen(false)
          setDeleteSwarmCandidateContainerIDs([])
          setSelectedDeleteSwarmContainerIDs([])
          setDeleteSwarmRemoteMode('teardown')
        }}
        onConfirm={() => void handleDeleteSelectedSwarms()}
      />
    </>
  )
}
