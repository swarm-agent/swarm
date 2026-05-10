import { useEffect, useMemo, useState } from 'react'
import { Boxes, CheckSquare, Link2, Monitor, Pencil, Plus, Trash2, TriangleAlert } from 'lucide-react'
import { Badge } from '../../../components/ui/badge'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { Input } from '../../../components/ui/input'
import { ModalCloseButton } from '../../../components/ui/modal-close-button'
import { Select } from '../../../components/ui/select'
import {
  actOnSwarmLocalContainer,
  deleteSwarmLocalContainers,
  approveRemoteSwarmPairing,
  fetchDesktopOnboardingStatus,
  fetchPendingRemoteSwarmPairings,
  fetchSwarmLocalContainers,
  fetchSwarmLocalRuntimeStatus,
  fetchSwarmState,
  patchDesktopOnboarding,
  pruneMissingSwarmLocalContainers,
  removeManagedHostLink,
  saveDesktopOnboarding,
  type RemoteSwarmPendingPairing,
  type SwarmLocalContainer,
  type SwarmLocalContainerDeleteResult,
  type SwarmLocalRuntimeStatus,
  type SwarmLocalState,
} from '../onboarding/api'
import type { DesktopOnboardingStatus, DesktopSwarmGroupMember, DesktopSwarmGroupState } from '../onboarding/types'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveSwarmSettings } from '../settings/swarm/mutations/save-swarm-settings'
import type { UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { AddSwarmModal } from './components/add-swarm-modal'
import { fetchSwarmTargets, type SwarmTarget } from './api/swarm-targets'
import { fetchSwarmMirrorResources, type SwarmMirrorResources, type SwarmMirrorWorkspaceResource } from './api/swarm-mirror'
import { LinkSwarmModal } from './components/link-swarm-modal'
import { ManagedHostWorkspaceReplicationPanel } from './components/managed-host-workspace-replication-panel'
import {
  type DeployContainerDeployment,
  type DeployContainerWorkspaceBootstrap,
  type RemoteDeployPayload,
  type RemoteDeploySession,
  actOnDeployContainer,
  updateDeployContainerSettings,
  deleteDeployContainers,
  deleteDeployContainersViaHost,
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
  const managedHostingOffPrefix = status.config.swarmMode ? '' : 'Managed Hosting is off; linking/networking APIs stay disabled until you enable it. '
  if (serve.ready && serve.mode === 'desktop') {
    return { summary: 'Hosted on Tailscale', detail: `${managedHostingOffPrefix}Verified with Tailscale Serve status. The tailnet link opens this Swarm desktop and backend API.`, tone: 'live', badge: 'Serve verified' }
  }
  if (serve.ready && serve.mode === 'api') {
    return { summary: 'Backend API only', detail: `${managedHostingOffPrefix}Verified with Tailscale Serve status. The tailnet link reaches the backend API, not the desktop UI.`, tone: 'warning', badge: 'API verified' }
  }
  if (serve.error) {
    return { summary: 'Serve status unavailable', detail: serve.error, tone: 'warning', badge: 'Check failed' }
  }
  if (serve.configured) {
    return { summary: 'Serve points somewhere else', detail: serve.proxyTarget ? `Tailscale Serve points to ${serve.proxyTarget}, not this Swarm desktop/API.` : 'Tailscale Serve is configured, but not for this Swarm desktop/API.', tone: 'warning', badge: 'Wrong target' }
  }
  const hasTailnetURL = Boolean(status.config.tailscaleURL || tailscale.tailnetURL || tailscale.candidateURL || tailscale.dnsName)
  if (hasTailnetURL) {
    return { summary: 'Not hosted yet', detail: `${managedHostingOffPrefix}A tailnet URL is available, but Tailscale Serve status does not show this desktop/API being served yet.`, tone: 'neutral', badge: 'Not served' }
  }
  if (tailscale.connected) {
    return { summary: 'Not hosted yet', detail: `${managedHostingOffPrefix}Tailscale is connected. Run the Host Swarm command to publish this desktop on the tailnet.`, tone: 'neutral', badge: 'Tailscale connected' }
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

function displayNameFromHost(host: string | null | undefined): string {
  const value = String(host ?? '').trim().replace(/\.$/, '')
  if (!value) {
    return ''
  }
  return value.split('.')[0]?.trim() || value
}

function defaultLocalSwarmName(status: DesktopOnboardingStatus | null, nodeName?: string): string {
  return status?.config.swarmName.trim()
    || String(nodeName ?? '').trim()
    || displayNameFromHost(status?.network.tailscale.dnsName)
    || displayNameFromHost(hostnameFromURL(status?.network.tailscale.tailnetURL || status?.config.tailscaleURL))
    || 'Local swarm'
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

const PENDING_REPLICATION_TARGET_STORAGE_KEY = 'swarm.pendingReplicationTarget.v1'

function loadPendingReplicationTarget(): SwarmTarget | null {
  if (typeof window === 'undefined') {
    return null
  }
  try {
    const raw = window.localStorage.getItem(PENDING_REPLICATION_TARGET_STORAGE_KEY)
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

function savePendingReplicationTarget(target: SwarmTarget | null): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    if (target) {
      window.localStorage.setItem(PENDING_REPLICATION_TARGET_STORAGE_KEY, JSON.stringify(target))
      return
    }
    window.localStorage.removeItem(PENDING_REPLICATION_TARGET_STORAGE_KEY)
  } catch {
    // Ignore local persistence failures; the in-memory pending card still works.
  }
}

function mirrorWorkspaceName(workspace: SwarmMirrorWorkspaceResource): string {
  return String(workspace.workspace_name || workspace.name || workspace.path.split('/').filter(Boolean).pop() || 'workspace').trim()
}

function normalizePairingCode(value: string | null | undefined): string {
  return String(value ?? '').trim().replace(/[^a-zA-Z0-9]/g, '').toUpperCase().slice(0, 6)
}

function formatPairingCode(value: string | null | undefined): string {
  const normalized = normalizePairingCode(value)
  return normalized.length === 6 ? `${normalized.slice(0, 3)}-${normalized.slice(3)}` : normalized
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

interface DeleteCandidate {
  container: SwarmLocalContainer
  attachment: DeployContainerDeployment | null
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
    const modules = new Set(deployment.sync_modules ?? ['credentials', 'agents', 'custom_tools', 'skills'])
    setSyncEnabled(Boolean(deployment.sync_enabled))
    setSyncAgents(modules.has('agents'))
    setSyncCustomTools(modules.has('custom_tools'))
    setSyncSkills(modules.has('skills'))
    setBypassPermissions(Boolean(deployment.bypass_permissions))
  }, [deployment, open])

  if (!open || !deployment) {
    return null
  }

  const modules = ['credentials', ...(syncAgents ? ['agents'] : []), ...(syncCustomTools ? ['custom_tools'] : []), ...(syncSkills ? ['skills'] : [])]

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
                Remove selected local containers. If a container has an attached child swarm, its linked master-side child info will also be removed.
              </p>
            </div>
            <ModalCloseButton onClick={onClose} aria-label="Close delete containers dialog" />
          </div>
        </div>

        <div className="flex max-h-[min(76vh,760px)] flex-col gap-4 overflow-y-auto px-6 py-6">
          {candidates.length === 0 ? (
            <Card className="border-dashed p-5 text-sm text-[var(--app-text-muted)]">
              No local containers available to delete.
            </Card>
          ) : (
            <div className="grid gap-3">
              {candidates.map(({ container, attachment }) => {
                const checked = selectedIDs.has(container.id)
                const childLabel = attachment?.child_display_name || attachment?.child_swarm_id || ''
                return (
                  <label key={container.id} className={`flex cursor-pointer items-start gap-3 rounded-2xl border p-4 transition ${checked ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}`}>
                    <input
                      type="checkbox"
                      className="mt-1 h-4 w-4 rounded border-[var(--app-border)]"
                      checked={checked}
                      onChange={() => onToggle(container.id)}
                      disabled={busy}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <div className="truncate text-sm font-semibold text-[var(--app-text)]">{container.name}</div>
                        <Badge tone={container.status === 'running' ? 'live' : 'neutral'}>{container.status || 'created'}</Badge>
                        {attachment ? <Badge tone="warning">removes child info</Badge> : null}
                      </div>
                      <div className="mt-1 text-xs text-[var(--app-text-muted)]">{container.containerName}</div>
                      <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                        <div>Runtime: {container.runtime || 'unknown'}</div>
                        {childLabel ? <div>Connected child swarm: {childLabel}</div> : null}
                        {attachment ? <div>Also removes linked deployment, trusted peer, and group membership info from this master.</div> : null}
                      </div>
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
  const [localContainers, setLocalContainers] = useState<SwarmLocalContainer[]>([])
  const [deployments, setDeployments] = useState<DeployContainerDeployment[]>([])
  const [remoteSessions, setRemoteSessions] = useState<RemoteDeploySession[]>([])
  const [pendingPairings, setPendingPairings] = useState<RemoteSwarmPendingPairing[]>([])
  const [pairingDecisionBusyID, setPairingDecisionBusyID] = useState<string | null>(null)
  const [pairingConfirmations, setPairingConfirmations] = useState<Record<string, boolean>>({})
  const [copyState, setCopyState] = useState<'idle' | 'desktop' | 'error'>('idle')
  const [editingLocalName, setEditingLocalName] = useState(false)
  const [localNameDraft, setLocalNameDraft] = useState('')
  const [addSwarmOpen, setAddSwarmOpen] = useState(false)
  const [linkSwarmOpen, setLinkSwarmOpen] = useState(false)
  const [pendingReplicationTarget, setPendingReplicationTarget] = useState<SwarmTarget | null>(() => loadPendingReplicationTarget())
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

  const applyCoreDashboardState = (state: SwarmLocalState, onboarding: DesktopOnboardingStatus, nextUISettings: UISettingsWire) => {
    setSwarmState(state)
    setOnboardingStatus(onboarding)
    setUISettings(nextUISettings)
    setLocalNameDraft(onboarding.config.swarmName || state.node.name || '')
  }

  const applySupplementalDashboardState = (
    runtimeStatus: SwarmLocalRuntimeStatus,
    launchedContainers: SwarmLocalContainer[],
    nextDeployments: DeployContainerDeployment[],
    nextRemoteSessions: RemoteDeploySession[],
    nextPendingPairings: RemoteSwarmPendingPairing[],
    nextSwarmTargets: SwarmTarget[],
    nextMirrorResources: SwarmMirrorResources,
  ) => {
    setLocalRuntime(runtimeStatus)
    setLocalContainers(launchedContainers)
    setDeployments(nextDeployments)
    setRemoteSessions(nextRemoteSessions)
    setPendingPairings(nextPendingPairings)
    setSwarmTargets(nextSwarmTargets)
    setMirrorResources(nextMirrorResources)
  }

  const refresh = async () => {
    const [state, onboarding, nextUISettings, runtimeStatus, launchedContainers, nextDeployments, nextRemoteSessions, nextPendingPairings, nextTargets, nextMirrorResources] = await Promise.all([
      fetchSwarmState(),
      fetchDesktopOnboardingStatus(),
      getUISettings(),
      fetchSwarmLocalRuntimeStatus(),
      fetchSwarmLocalContainers(),
      fetchDeployContainers(),
      fetchRemoteDeploySessions(),
      fetchPendingRemoteSwarmPairings(),
      fetchSwarmTargets(),
      fetchSwarmMirrorResources(),
    ])
    applyCoreDashboardState(state, onboarding, nextUISettings)
    applySupplementalDashboardState(runtimeStatus, launchedContainers, nextDeployments, nextRemoteSessions, nextPendingPairings, nextTargets.targets, nextMirrorResources)
  }

  useEffect(() => {
    savePendingReplicationTarget(pendingReplicationTarget)
  }, [pendingReplicationTarget])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setRuntimeLoading(true)
    setLocalContainersLoading(true)
    setDeploymentsLoading(true)
    setRemoteSessionsLoading(true)
    setError(null)
    setStatus(null)

    void Promise.all([fetchSwarmState(), fetchDesktopOnboardingStatus(), getUISettings()])
      .then(([state, onboarding, nextUISettings]) => {
        if (!cancelled) {
          applyCoreDashboardState(state, onboarding, nextUISettings)
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

    return () => {
      cancelled = true
    }
  }, [])

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

  const isSwarmMode = Boolean(onboardingStatus?.config.swarmMode)
  const group = useMemo(() => currentGroup(onboardingStatus), [onboardingStatus])
  const localContainersSectionLoading = localContainersLoading || deploymentsLoading
  const staleAttachedLoading = deploymentsLoading
  const localSwarmID = onboardingStatus?.config.swarmID || swarmState?.node.swarm_id || ''
  const localSwarmName = onboardingStatus?.config.swarmName || swarmState?.node.name || 'Local swarm'
  const localSwarmRole = onboardingStatus?.config.swarmRole || swarmState?.node.role || (onboardingStatus?.config.child ? 'child' : 'master')
  const localSwarmRoleLabel = swarmRoleLabel(localSwarmRole)
  const localIsManagedHost = String(localSwarmRole).trim().toLowerCase() === 'managed'
  const localIsChild = localSwarmRoleLabel === 'Child' || localIsManagedHost
  const localManagerSwarmID = onboardingStatus?.pairing.parentSwarmID || ''
  const localPairingState = onboardingStatus?.pairing.pairingState || ''
  const localManagedLinked = localIsManagedHost && (localPairingState === 'paired' || localManagerSwarmID !== '')
  const currentGroupID = group?.group.id.trim() || ''
  const groupMasterID = group?.group.hostSwarmID || ''
  const resolvedGroupMasterName = group?.members.find((member) => member.swarmID === groupMasterID)?.name.trim() || ''
  const localManagerDisplay = resolvedGroupMasterName || localManagerSwarmID || 'Manager'
  const localIsMaster = Boolean(localSwarmID && groupMasterID === localSwarmID && !localIsChild)
  const managedHostControlTitle = localIsManagedHost ? 'This host is already linked to a Manager.' : undefined
  const managedHostingControlsDisabled = loading || busy || localIsChild || Boolean(group && !localIsMaster)
  const addContainerDisabled = loading || busy
  const visiblePendingPairings = pendingPairings.filter((item) => item.status === 'pending_approval' || item.status === '')
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
        const deploymentGroupID = String(deployment.group_id ?? '').trim()
        const matchedContainer = localContainers.find((container) => deploymentMatchesContainer(deployment, container)) ?? null
        const outsideCurrentGroup = currentGroupID !== '' && deploymentGroupID !== '' && deploymentGroupID !== currentGroupID
        const missingContainerRecord = matchedContainer == null
        return outsideCurrentGroup || missingContainerRecord
      })
      .sort((left, right) => right.updated_at - left.updated_at)
  ), [currentGroupID, deployments, localContainers])
  const deleteCandidates = useMemo<DeleteCandidate[]>(() => (
    localContainers.map((container) => ({
      container,
      attachment: deployments.find((deployment) => (
        String(deployment.attach_status ?? '').trim() === 'attached'
        && deploymentMatchesContainer(deployment, container)
      )) ?? null,
    }))
  ), [deployments, localContainers])
  const deleteSwarmCandidates = useMemo<DeleteSwarmCandidate[]>(() => {
    if (deleteSwarmCandidateContainerIDs.length === 0) {
      return baseDeleteSwarmCandidates
    }
    const allowed = new Set(deleteSwarmCandidateContainerIDs)
    return baseDeleteSwarmCandidates.filter((candidate) => allowed.has(candidate.selectionID))
  }, [baseDeleteSwarmCandidates, deleteSwarmCandidateContainerIDs])
  const selectedDeleteIDs = useMemo(() => new Set(selectedDeleteContainerIDs), [selectedDeleteContainerIDs])
  const selectedDeleteSwarmIDs = useMemo(() => new Set(selectedDeleteSwarmContainerIDs), [selectedDeleteSwarmContainerIDs])

  const handleEnableSwarmMode = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const currentName = defaultLocalSwarmName(onboardingStatus, swarmState?.node.name)
      const useTailscale = Boolean(tailscaleCandidate.connected && localTailscaleURL)
      await saveDesktopOnboarding({
        swarmName: currentName,
        swarmMode: true,
        child: false,
        mode: useTailscale ? 'tailscale' : 'lan',
        ...(useTailscale ? { tailscaleURL: localTailscaleURL } : {}),
      })
      await refresh()
      setStatus(useTailscale
        ? 'Managed Hosting is enabled with Tailscale reachability. Use Managed Hosting to link another host.'
        : 'Managed Hosting is enabled. Tailscale was not connected, so reachability stayed on LAN for now.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to turn on Managed Hosting')
    } finally {
      setBusy(false)
    }
  }

  const handleDisableSwarmMode = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const currentName = defaultLocalSwarmName(onboardingStatus, swarmState?.node.name)
      await patchDesktopOnboarding({ swarmMode: false })
      setOnboardingStatus((current) => current
        ? { ...current, config: { ...current.config, swarmName: currentName, swarmMode: false } }
        : current)
      setLocalNameDraft(currentName)
      setStatus('Managed Hosting is now off. Local containers remain available.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to turn off Managed Hosting')
    } finally {
      setBusy(false)
    }
  }

  const handleUseTailscaleReachability = async () => {
    if (!onboardingStatus) {
      return
    }
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const currentName = defaultLocalSwarmName(onboardingStatus, swarmState?.node.name)
      await saveDesktopOnboarding({
        swarmName: currentName,
        swarmMode: true,
        child: false,
        mode: 'tailscale',
        tailscaleURL: localTailscaleURL,
      })
      await refresh()
      setStatus('Saved Tailscale reachability. Run the Host Swarm command if you want the desktop available on the tailnet.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save Tailscale reachability')
    } finally {
      setBusy(false)
    }
  }

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
        const managedSwarmID = (result.routing?.managed_swarm_id || request.managed_swarm_id || '').trim()
        const managedName = (result.routing?.managed_name || request.managed_name || managedSwarmID || requestID).trim()
        const backendURL = (result.routing?.backend_url || request.managed_endpoint || '').trim()
        if (managedSwarmID) {
          setPendingReplicationTarget({
            swarm_id: managedSwarmID,
            name: managedName,
            role: 'managed',
            relationship: 'managed',
            kind: 'host',
            attach_status: result.status || 'paired',
            online: true,
            selectable: true,
            current: false,
            backend_url: backendURL,
          })
        }
        setStatus(`Approved Managed Swarm ${managedName}. Workspace replication is pending; replicate now or skip.`)
      } else {
        setStatus(`Rejected pairing request ${requestID}.`)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update pairing request')
    } finally {
      setPairingDecisionBusyID(null)
    }
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
      await Promise.all([
        saveSwarmSettings({ name: normalized }),
        saveDesktopOnboarding({ swarmName: normalized }),
      ])
      await refresh()
      setEditingLocalName(false)
      setStatus(`Saved swarm name as ${normalized}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save swarm name')
    } finally {
      setBusy(false)
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
    setSelectedDeleteContainerIDs((current) => (
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id]
    ))
  }

  const toggleDeleteSwarm = (id: string) => {
    setSelectedDeleteSwarmContainerIDs((current) => (
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id]
    ))
  }

  const handleDeleteContainers = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      const result = await deleteSwarmLocalContainers(selectedDeleteContainerIDs)
      await refresh()
      setDeleteResult(result)
      setSelectedDeleteContainerIDs([])
      setDeleteContainersOpen(false)
      setStatus(result.failed > 0
        ? `Deleted ${result.count} container${result.count === 1 ? '' : 's'} with ${result.failed} failure${result.failed === 1 ? '' : 's'}.`
        : `Deleted ${result.count} container${result.count === 1 ? '' : 's'}.${result.childInfoRemoved > 0 ? ` Removed linked child info for ${result.childInfoRemoved}.` : ''}`)
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
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="primary" data-testid="swarm-dashboard-add-container" onClick={() => openAddSwarm()} disabled={addContainerDisabled}>
            <Plus size={14} />
            Add Container
          </Button>
          {isSwarmMode ? (
            <Button type="button" variant="outline" data-testid="swarm-dashboard-link-swarm" onClick={() => openLinkSwarm()} disabled={managedHostingControlsDisabled} title={managedHostControlTitle || (localIsChild ? 'This host is already linked to a Manager.' : undefined)}>
              <Link2 size={14} />
              Managed Hosting
            </Button>
          ) : null}
          <Button
            variant="outline"
            onClick={() => {
              if (isSwarmMode) {
                void handleDisableSwarmMode()
                return
              }
              void handleEnableSwarmMode()
            }}
            disabled={managedHostingControlsDisabled}
            title={managedHostControlTitle || (localIsChild ? 'This host is already linked to a Manager.' : undefined)}
          >
            {isSwarmMode ? 'Disable Managed Hosting' : 'Enable Managed Hosting'}
          </Button>
        </div>
      </div>

      {error || status || pendingReplicationTarget || visiblePendingPairings.length > 0 ? (
        <div className="mt-4 space-y-3">
          {error ? <Card data-testid="swarm-dashboard-error" className="border-[var(--app-danger-border)] bg-transparent p-4 text-sm text-[var(--app-danger)]">{error}</Card> : null}
          {status ? <Card data-testid="swarm-dashboard-status" className="border-[var(--app-success-border)] bg-transparent p-4 text-sm text-[var(--app-success)]">{status}</Card> : null}
          {pendingReplicationTarget ? (
            <Card data-testid="swarm-replication-pending" className="border-[var(--app-warning-border)] bg-transparent p-4">
              <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                <div>
                  <div className="text-sm font-semibold text-[var(--app-text)]">Managed Host linked — workspace replication pending</div>
                  <div className="mt-1 text-sm text-[var(--app-text-muted)]">
                    {pendingReplicationTarget.name || pendingReplicationTarget.swarm_id} is trusted. Replicate git workspaces now or skip and run replication later.
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone="warning">Pending</Badge>
                  <Button size="sm" variant="outline" onClick={() => { setPendingReplicationTarget(null) }} disabled={busy}>Skip</Button>
                  <Button size="sm" onClick={() => setStatus('Choose workspaces in the replication options below.')} disabled={busy}>Review options</Button>
                </div>
              </div>
            </Card>
          ) : null}

          {pendingReplicationTarget ? (
            <ManagedHostWorkspaceReplicationPanel
              target={pendingReplicationTarget}
              busy={busy}
              onComplete={async (message) => {
                await refresh()
                setPendingReplicationTarget(null)
                setError(null)
                setStatus(message)
              }}
              onSkip={async (message) => {
                setPendingReplicationTarget(null)
                setError(null)
                setStatus(message)
              }}
            />
          ) : null}

          {visiblePendingPairings.length > 0 ? (
            <Card data-testid="swarm-pending-pairings" className="border-[var(--app-warning-border)] bg-transparent p-4">
              <div className="flex flex-col gap-3">
                <div>
                  <div className="text-sm font-semibold text-[var(--app-text)]">Pending Managed Host request</div>
                  <div className="mt-1 text-sm text-[var(--app-text-muted)]">Confirm the 6-character code on both machines before approving.</div>
                </div>
                {visiblePendingPairings.map((request) => {
                  const requestID = request.request_id.trim()
                  const busyRequest = pairingDecisionBusyID === requestID
                  return (
                    <div key={requestID || request.managed_swarm_id || request.managed_name} className="rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                      <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                        <div className="min-w-0 space-y-1 text-sm">
                          <div className="font-medium text-[var(--app-text)]">{request.managed_name || 'Managed Host'}</div>
                          <div className="text-[var(--app-text-muted)]">{request.managed_endpoint || request.managed_swarm_id || requestID}</div>
                        </div>
                        <div className="flex flex-wrap items-center gap-2">
                          <Badge tone="warning">{formatPairingCode(request.ceremony_code) || 'No code'}</Badge>
                          <label className="flex items-center gap-2 text-xs text-[var(--app-text-muted)]">
                            <input
                              type="checkbox"
                              checked={pairingConfirmations[requestID] === true}
                              disabled={busy || busyRequest}
                              onChange={(event) => {
                                const confirmed = event.target.checked
                                setPairingConfirmations((current) => ({ ...current, [requestID]: confirmed }))
                              }}
                            />
                            <span>I confirm the code matches</span>
                          </label>
                          <Button size="sm" variant="outline" disabled={busy || busyRequest} onClick={() => void handlePairingDecision(request, false)}>Reject</Button>
                          <Button size="sm" disabled={busy || busyRequest || normalizePairingCode(request.ceremony_code).length !== 6 || pairingConfirmations[requestID] !== true} onClick={() => void handlePairingDecision(request, true)}>Approve</Button>
                        </div>
                      </div>
                    </div>
                  )
                })}
              </div>
            </Card>
          ) : null}
        </div>
      ) : null}

      <div className="grid gap-6 pt-4">
        <section className="rounded-2xl border border-[var(--app-border)] bg-transparent p-6 pb-8">
          <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
            <div className="min-w-0 flex items-start gap-3">
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-[var(--app-border)] text-[var(--app-text-muted)]">
                <Monitor size={20} />
              </div>
              <div className="min-w-0">
                {editingLocalName ? (
                  <div className="flex flex-wrap items-center gap-2">
                    <Input value={localNameDraft} onChange={(event) => setLocalNameDraft(event.target.value)} className="w-[280px]" />
                    <Button onClick={() => void handleSaveLocalName()} disabled={busy || !localNameDirty}>Save</Button>
                    <Button variant="outline" onClick={() => { setLocalNameDraft(localSwarmName); setEditingLocalName(false) }} disabled={busy}>Cancel</Button>
                  </div>
                ) : (
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="truncate text-lg font-semibold text-[var(--app-text)]">{localSwarmName}</h2>
                    <Button variant="ghost" size="sm" className="h-7 w-7 min-h-7 min-w-7 rounded-full p-0" onClick={() => setEditingLocalName(true)} disabled={busy}>
                      <Pencil size={14} />
                    </Button>
                  </div>
                )}
                <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
                  <Badge tone={localIsMaster ? 'live' : localIsManagedHost ? 'live' : localIsChild ? 'warning' : 'neutral'}>{localSwarmRoleLabel}</Badge>
                  <Badge tone={isSwarmMode ? 'live' : 'neutral'}>{isSwarmMode ? 'Managed hosting on' : 'Managed hosting off'}</Badge>
                  <Badge tone={localTailscaleHosting.tone}>{localTailscaleHosting.summary}</Badge>
                  {localManagedLinked ? <Badge tone="neutral">Manager: {localManagerDisplay}</Badge> : null}
                  {localManagedLinked && localPairingState ? <Badge tone="neutral">Pairing: {formatUnderscoreLabel(localPairingState)}</Badge> : null}
                  {localManagedLinked ? <Badge tone={onboardingStatus?.pairing.managedAuthLastError ? 'warning' : onboardingStatus?.pairing.managedAuthSnapshotHash ? 'live' : 'neutral'}>{onboardingStatus?.pairing.managedAuthLastError ? 'Managed sync error' : onboardingStatus?.pairing.managedAuthSnapshotHash ? 'Managed sync current' : 'Managed sync pending'}</Badge> : null}
                </div>
              </div>
            </div>
            <div className="grid gap-1 text-xs text-[var(--app-text-muted)] lg:text-right">
              <div>Swarm ID: <span className="font-mono text-[var(--app-text)]">{localSwarmID || 'not assigned yet'}</span></div>
              <div>Backend: <span className="font-mono text-[var(--app-text)]">{configuredHost}:{backendPort}</span></div>
              <div>Desktop: <span className="font-mono text-[var(--app-text)]">:{desktopPort}</span></div>
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
          ) : !isSwarmMode ? (
            <div className="mt-4 rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">Managed Hosting is off. Local containers still work; enable Managed Hosting only to link other machines.</div>
          ) : null}

          <div className="mt-5 grid gap-4 lg:grid-cols-[1.2fr_1fr]">
            <div className="space-y-3">
              <div>
                <div className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--app-text-muted)]">swarm.conf</div>
                <div className="mt-3 grid gap-3 text-sm sm:grid-cols-2">
                  <div>
                    <div className="text-xs text-[var(--app-text-muted)]">Who can reach this host</div>
                    <div className="font-medium text-[var(--app-text)]">{localBindStatus}</div>
                    <div className="text-xs text-[var(--app-text-muted)]">Listening on {configuredHost}:{backendPort}</div>
                  </div>
                  <div>
                    <div className="text-xs text-[var(--app-text-muted)]">Advertised to swarms</div>
                    <div className="font-medium text-[var(--app-text)]">{(onboardingStatus?.config.advertiseHost || backendHost)}:{onboardingStatus?.config.advertisePort || backendPort}</div>
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
                <div className="flex items-center gap-2">
                  <div className="min-w-0 flex-1 truncate rounded-lg border border-[var(--app-border)] px-3 py-2 font-mono text-[11px] text-[var(--app-text)]" title={desktopServeCommand}>{desktopServeCommand}</div>
                  <Button type="button" variant="outline" size="sm" className="h-8 text-[11px]" onClick={() => void handleCopyTailscaleCommand(desktopServeCommand)}>
                    {copyState === 'desktop' ? 'Copied' : 'Copy'}
                  </Button>
                </div>
              ) : null}
              {!localTailscalePrimary && tailscaleCandidate.available ? (
                <Button type="button" variant="outline" size="sm" className="h-8 text-[11px]" onClick={() => void handleUseTailscaleReachability()} disabled={busy || !localTailscaleURL}>
                  Use Tailscale for Swarm links
                </Button>
              ) : null}
            </div>
          </div>

          <div className="mt-6 border-t border-[var(--app-border)] pt-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="text-sm font-semibold text-[var(--app-text)]">Containers on this host</div>
                <div className="text-xs text-[var(--app-text-muted)]">Container swarms running on {localSwarmName}.</div>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Button type="button" variant="outline" size="sm" onClick={openDeleteContainers} disabled={busy || deleteCandidates.length === 0}>
                  <CheckSquare size={14} />
                  Delete containers
                </Button>
                {staleLocalContainers.length > 0 ? (
                  <Button type="button" variant="outline" size="sm" onClick={() => void handlePruneMissingLocalContainers()} disabled={busy}>Remove stale</Button>
                ) : null}
                {runtimeLoading ? <Badge tone="neutral">detecting runtime…</Badge> : null}
              </div>
            </div>

            {localRuntime.warning ? (
              <div className="mt-3 rounded-xl border border-[var(--app-warning-border)] bg-transparent px-3 py-2 text-sm text-[var(--app-warning-text)]">
                <div className="flex items-start gap-2"><TriangleAlert size={16} className="mt-0.5" /><div>{localRuntime.warning}</div></div>
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
                  return (
                    <div key={container.id} className="rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                      <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                        <div className="min-w-0 flex items-center gap-2">
                          <Boxes size={16} className="text-[var(--app-text-muted)]" />
                          <div className="min-w-0">
                            <div className="truncate text-sm font-semibold text-[var(--app-text)]">{containerLocationLabel(localSwarmName, container.containerName || container.name)}</div>
                            <div className="truncate text-xs text-[var(--app-text-muted)]">Swarm: {attachedDeployment?.child_display_name || container.name} · Container: {container.containerName} · {container.runtime || 'runtime unknown'} · API {container.runtimePort || 'auto'}</div>
                          </div>
                        </div>
                        <div className="flex flex-wrap items-center gap-2">
                          {attachedDeployment ? <Badge tone="live">{attachedDeployment.child_display_name || attachedDeployment.child_swarm_id || 'attached swarm'}</Badge> : null}
                          <Badge tone={running ? 'live' : missing ? 'warning' : 'neutral'}>{container.status || 'created'}</Badge>
                          {childDesktopURL ? <a href={childDesktopURL} target="_blank" rel="noreferrer" className="text-xs text-[var(--app-primary)] hover:underline">Desktop</a> : null}
                          {childAPIURL ? <a href={childAPIURL} target="_blank" rel="noreferrer" className="text-xs text-[var(--app-primary)] hover:underline">API</a> : null}
                          {missing ? (
                            <Button type="button" variant="outline" size="sm" onClick={() => void handleRemoveMissingLocalContainer(container)} disabled={busy}>Remove stale</Button>
                          ) : (
                            <Button type="button" variant="outline" size="sm" onClick={() => void containerAction()} disabled={busy}>{running ? 'Stop' : 'Start'}</Button>
                          )}
                        </div>
                      </div>
                      {container.warning ? <div className="mt-2 rounded-lg border border-[var(--app-warning-border)] px-3 py-2 text-xs text-[var(--app-warning-text)]">{container.warning}</div> : null}
                    </div>
                  )
                })
              )}
            </div>
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border)] bg-transparent p-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Linked Managed Hosts</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Hosts linked to this Swarm. Container swarms are listed under the host where they run.</p>
            </div>
            <Badge tone="neutral">{managedHostMembers.length}</Badge>
          </div>

          {!isSwarmMode ? (
            <div className="mt-4 rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">Enable Managed Hosting to link Managed Hosts.</div>
          ) : managedHostMembers.length > 0 ? (
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
                  <div key={`${member.groupID}:${member.swarmID}`} className="rounded-xl border border-[var(--app-border)] bg-transparent p-4">
                    <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                      <div className="min-w-0 flex items-start gap-3">
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
                          <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                            <div>Swarm ID: <span className="font-mono text-[var(--app-text)]">{member.swarmID}</span></div>
                            <div>Runtime: host · Relationship: managed</div>
                            <div>Mirrored resources: {mirroredHostWorkspaces.length} workspace{mirroredHostWorkspaces.length === 1 ? '' : 's'} · {mirroredHostContainers.length} container{mirroredHostContainers.length === 1 ? '' : 's'}</div>
                            {target?.last_error ? <div className="text-[var(--app-warning-text)]">{target.last_error}</div> : null}
                          </div>
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-2 lg:justify-end">
                        {hostDesktopURL ? <a href={hostDesktopURL} target="_blank" rel="noreferrer" className="rounded-lg border border-[var(--app-border)] px-3 py-2 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">Desktop</a> : null}
                        {hostAPIURL ? <a href={hostAPIURL} target="_blank" rel="noreferrer" className="rounded-lg border border-[var(--app-border)] px-3 py-2 text-xs font-medium text-[var(--app-primary)] hover:border-[var(--app-border-strong)]">API</a> : null}
                        <Button type="button" variant="outline" size="sm" className="text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)]" onClick={() => void handleRemoveManagedHost(member, target)} disabled={busy || removingManagedHostID === member.swarmID}>
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
                          return (
                            <div key={session.id} className="rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                              <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                                <div className="min-w-0 flex items-center gap-2">
                                  <Boxes size={16} className="text-[var(--app-text-muted)]" />
                                  <div className="min-w-0">
                                    <div className="truncate text-sm font-semibold text-[var(--app-text)]">{containerLocationLabel(hostName, containerName)}</div>
                                    <div className="truncate text-xs text-[var(--app-text-muted)]">Swarm: {childName} · Container: {containerName} · {session.remote_runtime || 'runtime unknown'}</div>
                                  </div>
                                </div>
                                <div className="flex flex-wrap items-center gap-2">
                                  <Badge tone={running ? 'live' : 'neutral'}>{formatRemoteSessionStatus(session.status || 'unknown')}</Badge>
                                  {remoteDesktopURL ? <a href={remoteDesktopURL} target="_blank" rel="noreferrer" className="text-xs text-[var(--app-primary)] hover:underline">Desktop</a> : null}
                                  {remoteAPIURL ? <a href={remoteAPIURL} target="_blank" rel="noreferrer" className="text-xs text-[var(--app-primary)] hover:underline">API</a> : null}
                                  {deleteCandidate ? <Button type="button" variant="outline" size="sm" className="text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)]" onClick={() => openDeleteSwarms([deleteCandidate.selectionID], [deleteCandidate.selectionID])} disabled={busy}>Remove</Button> : null}
                                </div>
                              </div>
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
                          return (
                            <div key={`mirrored:${mirroredContainer.managedSwarmID}:${mirroredContainer.id}`} className="rounded-xl border border-[var(--app-border)] bg-transparent p-3">
                              <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                                <div className="min-w-0 flex items-center gap-2">
                                  <Boxes size={16} className="text-[var(--app-text-muted)]" />
                                  <div className="min-w-0">
                                    <div className="truncate text-sm font-semibold text-[var(--app-text)]">{containerLocationLabel(hostName, container.containerName || container.name)}</div>
                                    <div className="truncate text-xs text-[var(--app-text-muted)]">Swarm: {childName} · Container: {container.containerName || container.id} · {container.runtime || attachedDeployment?.runtime || 'runtime unknown'} · API {container.runtimePort || attachedDeployment?.backend_host_port || 'auto'}</div>
                                  </div>
                                </div>
                                <div className="flex flex-wrap items-center gap-2">
                                  {attachedDeployment ? <Badge tone="live">{attachedDeployment.child_display_name || attachedDeployment.child_swarm_id || 'attached swarm'}</Badge> : <Badge tone="neutral">mirrored</Badge>}
                                  <Badge tone={running ? 'live' : container.status === 'missing' ? 'warning' : 'neutral'}>{container.status || 'created'}</Badge>
                                  {childDesktopURL ? <a href={childDesktopURL} target="_blank" rel="noreferrer" className="text-xs text-[var(--app-primary)] hover:underline">Desktop</a> : null}
                                  {childAPIURL ? <a href={childAPIURL} target="_blank" rel="noreferrer" className="text-xs text-[var(--app-primary)] hover:underline">API</a> : null}
                                </div>
                              </div>
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
            <div className="mt-4 rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No linked Managed Hosts yet.</div>
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
