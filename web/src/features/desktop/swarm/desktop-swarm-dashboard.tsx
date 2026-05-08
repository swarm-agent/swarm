import { useEffect, useMemo, useState } from 'react'
import { Boxes, CheckSquare, Link2, Pencil, Play, Plus, Square, Trash2, TriangleAlert } from 'lucide-react'
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
  pruneMissingSwarmLocalContainers,
  saveDesktopOnboarding,
  type RemoteSwarmPendingPairing,
  type SwarmLocalContainer,
  type SwarmLocalContainerDeleteResult,
  type SwarmLocalRuntimeStatus,
  type SwarmLocalState,
} from '../onboarding/api'
import type { DesktopOnboardingStatus, DesktopSwarmGroupState } from '../onboarding/types'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveSwarmSettings } from '../settings/swarm/mutations/save-swarm-settings'
import { normalizeDefaultNewSessionMode } from '../settings/swarm/types/swarm-settings'
import type { UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { AddSwarmModal } from './components/add-swarm-modal'
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
import { upsertSwarmGroup } from './mutations/upsert-swarm-group'
import { suggestGroupNetworkName } from './services/group-network-name'

function parsePeerTransportPort(value: string): number {
  if (!/^\d+$/.test(value.trim())) {
    throw new Error('Peer transport port must be a whole number.')
  }
  const parsed = Number.parseInt(value.trim(), 10)
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    throw new Error('Peer transport port must be between 1 and 65535.')
  }
  return parsed
}

function transportSummary(items: Array<{ kind: string; primary: string; all: string[] }>): string {
  if (!Array.isArray(items) || items.length === 0) {
    return 'transport unknown'
  }
  return items
    .map((item) => {
      const primary = item.primary?.trim() || item.all?.[0] || ''
      return primary ? `${item.kind}: ${primary}` : item.kind
    })
    .filter(Boolean)
    .join(' · ')
}

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

function tailscaleServeStatus(status: DesktopOnboardingStatus | null): { summary: string; detail: string } {
  const serve = status?.network.tailscale.serve
  const connected = Boolean(status?.network.tailscale.connected)
  switch (serve?.mode) {
    case 'desktop':
      return { summary: 'Hosted on your tailnet', detail: 'This tailnet URL opens the full Swarm desktop and API for this machine.' }
    case 'api':
      return { summary: 'API only on your tailnet', detail: 'The tailnet URL reaches the backend API, but not the hosted desktop UI.' }
    case 'peer_transport':
      return { summary: 'Peer transport only', detail: 'The tailnet URL reaches the dedicated peer transport lane, not the hosted desktop UI.' }
    case 'other':
      return { summary: 'Tailscale Serve points elsewhere', detail: serve?.proxyTarget ? `Current proxy target: ${serve.proxyTarget}` : 'This device is serving a different target.' }
    default:
      if (serve?.error) {
        return { summary: 'Serve status unavailable', detail: serve.error }
      }
      if (connected) {
        return { summary: 'Not hosted yet', detail: 'Tailscale is connected, but this device is not currently served on the tailnet.' }
      }
      return { summary: 'Detected but not currently connected', detail: 'Connect Tailscale first, then choose whether to host the full Swarm or just peer transport.' }
  }
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

function discoveredSwarmEndpoint(status: DesktopOnboardingStatus['discoveredSwarms'][number]): string {
  return status.tailnetURL || status.endpoint || status.dnsName || status.ips[0] || 'No endpoint reported'
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

function formatTime(value: number): string {
  if (!value) {
    return 'just now'
  }
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
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
  const [localRuntime, setLocalRuntime] = useState<SwarmLocalRuntimeStatus>({ recommended: '', available: [], installed: [], issues: {}, warning: '' })
  const [localContainers, setLocalContainers] = useState<SwarmLocalContainer[]>([])
  const [deployments, setDeployments] = useState<DeployContainerDeployment[]>([])
  const [remoteSessions, setRemoteSessions] = useState<RemoteDeploySession[]>([])
  const [pendingPairings, setPendingPairings] = useState<RemoteSwarmPendingPairing[]>([])
  const [pairingDecisionBusyID, setPairingDecisionBusyID] = useState<string | null>(null)
  const [copyState, setCopyState] = useState<'idle' | 'desktop' | 'peer' | 'error'>('idle')
  const [editingGroupName, setEditingGroupName] = useState(false)
  const [editingLocalName, setEditingLocalName] = useState(false)
  const [groupNameDraft, setGroupNameDraft] = useState('')
  const [localNameDraft, setLocalNameDraft] = useState('')
  const [addSwarmOpen, setAddSwarmOpen] = useState(false)
  const [addSwarmInitialTarget, setAddSwarmInitialTarget] = useState<'local' | 'remote'>('local')
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

  const applyCoreDashboardState = (state: SwarmLocalState, onboarding: DesktopOnboardingStatus, nextUISettings: UISettingsWire) => {
    setSwarmState(state)
    setOnboardingStatus(onboarding)
    setUISettings(nextUISettings)
    setGroupNameDraft(currentGroup(onboarding)?.group.name || '')
    setLocalNameDraft(onboarding.config.swarmName || state.node.name || '')
  }

  const applySupplementalDashboardState = (
    runtimeStatus: SwarmLocalRuntimeStatus,
    launchedContainers: SwarmLocalContainer[],
    nextDeployments: DeployContainerDeployment[],
    nextRemoteSessions: RemoteDeploySession[],
    nextPendingPairings: RemoteSwarmPendingPairing[],
  ) => {
    setLocalRuntime(runtimeStatus)
    setLocalContainers(launchedContainers)
    setDeployments(nextDeployments)
    setRemoteSessions(nextRemoteSessions)
    setPendingPairings(nextPendingPairings)
  }

  const refresh = async () => {
    const [state, onboarding, nextUISettings, runtimeStatus, launchedContainers, nextDeployments, nextRemoteSessions, nextPendingPairings] = await Promise.all([
      fetchSwarmState(),
      fetchDesktopOnboardingStatus(),
      getUISettings(),
      fetchSwarmLocalRuntimeStatus(),
      fetchSwarmLocalContainers(),
      fetchDeployContainers(),
      fetchRemoteDeploySessions(),
      fetchPendingRemoteSwarmPairings(),
    ])
    applyCoreDashboardState(state, onboarding, nextUISettings)
    applySupplementalDashboardState(runtimeStatus, launchedContainers, nextDeployments, nextRemoteSessions, nextPendingPairings)
  }

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

    return () => {
      cancelled = true
    }
  }, [])

  const isSwarmMode = Boolean(onboardingStatus?.config.swarmMode)
  const group = useMemo(() => currentGroup(onboardingStatus), [onboardingStatus])
  const currentGroupMemberIDs = useMemo(() => new Set((group?.members ?? []).map((member) => member.swarmID).filter(Boolean)), [group])
  const discoveredSwarms = useMemo(
    () => (onboardingStatus?.discoveredSwarms ?? []).filter((item) => item.running && !item.inCurrentGroup && !currentGroupMemberIDs.has(item.id)),
    [currentGroupMemberIDs, onboardingStatus],
  )
  const localContainersSectionLoading = localContainersLoading || deploymentsLoading
  const staleAttachedLoading = deploymentsLoading
  const discoveredSwarmsLoading = loading && onboardingStatus === null
  const localSwarmID = onboardingStatus?.config.swarmID || swarmState?.node.swarm_id || ''
  const localSwarmName = onboardingStatus?.config.swarmName || swarmState?.node.name || 'Local swarm'
  const localSwarmRole = onboardingStatus?.config.swarmRole || swarmState?.node.role || (onboardingStatus?.config.child ? 'child' : 'master')
  const localSwarmRoleLabel = swarmRoleLabel(localSwarmRole)
  const localIsChild = localSwarmRoleLabel === 'Child'
  const localGroupEditable = Boolean(group && group.group.hostSwarmID === localSwarmID && !localIsChild)
  const groupDisplayName = group?.group.name.trim() || 'Swarm group'
  const groupNetworkName = group?.group.networkName.trim() || ''
  const currentGroupID = group?.group.id.trim() || ''
  const groupMasterID = group?.group.hostSwarmID || ''
  const groupMasterName = group?.members.find((member) => member.swarmID === groupMasterID)?.name || 'Current master'
  const localIsMaster = Boolean(localSwarmID && groupMasterID === localSwarmID && !localIsChild)
  const masterControlsDisabled = loading || busy || localIsChild || Boolean(group && !localIsMaster)
  const visiblePendingPairings = pendingPairings.filter((item) => item.status === 'pending_approval' || item.status === '')
  const groupNameDirty = groupNameDraft.trim() !== (group?.group.name || '').trim()
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
  const localTailscaleServe = onboardingStatus?.network.tailscale.serve
  const localTailscalePrimary = usesTailscaleAsPrimaryTransport(onboardingStatus)
  const localTailscaleHosting = tailscaleServeStatus(onboardingStatus)
  const localBindStatus = localBindLabel(configuredHost)
  const localTransportPort = onboardingStatus?.config.localTransportPort || 7790
  const localTransportActive = Boolean(onboardingStatus?.config.localTransportActive)
  const localTransportWarning = onboardingStatus?.config.localTransportWarning || ''
  const localTransportStatus = localBindStatus === 'Local only'
    ? (localTransportActive ? 'Active for local child containers' : 'Required, but not active')
    : 'Not required while network reachable'
  const localPeerTransportPort = onboardingStatus?.config.peerTransportPort || 7791
  const desktopServeCommand = `tailscale serve --bg http://127.0.0.1:${desktopPort}`
  const peerTransportServeCommand = `tailscale serve --bg http://127.0.0.1:${localPeerTransportPort}`
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
        ...(useTailscale ? { tailscaleURL: localTailscaleURL, peerTransportPort: parsePeerTransportPort(String(localPeerTransportPort)) } : {}),
      })
      await refresh()
      setStatus(useTailscale
        ? 'Swarm Mode is on with Tailscale reachability. Use Link Swarm to pair another Managed host.'
        : 'Swarm Mode is on. Tailscale was not connected, so reachability stayed on LAN for now.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to turn on Swarm Mode')
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
      await saveDesktopOnboarding({ swarmName: currentName, swarmMode: false, child: false })
      await refresh()
      setStatus('Swarm Mode is now off. This node is back to standalone local use.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to turn off Swarm Mode')
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
      const nextPort = parsePeerTransportPort(String(localPeerTransportPort))
      await saveDesktopOnboarding({
        swarmName: currentName,
        swarmMode: true,
        child: false,
        mode: 'tailscale',
        tailscaleURL: localTailscaleURL,
        peerTransportPort: nextPort,
      })
      await refresh()
      setStatus('Saved Tailscale reachability. Restart Swarm, then choose whether to host the full Swarm or only peer transport on the tailnet.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save Tailscale reachability')
    } finally {
      setBusy(false)
    }
  }

  const handleCopyTailscaleCommand = async (command: string, kind: 'desktop' | 'peer') => {
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(command)
      setCopyState(kind)
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
      await approveRemoteSwarmPairing({
        requestID,
        approve,
        ceremonyCode: approve ? normalizePairingCode(request.ceremony_code) : undefined,
        reason: approve ? undefined : 'Rejected from Swarm dashboard',
      })
      setPendingPairings((items) => items.filter((item) => item.request_id !== requestID))
      await refresh()
      setStatus(approve ? `Approved Managed Swarm ${request.managed_name || request.managed_swarm_id || requestID}.` : `Rejected pairing request ${requestID}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update pairing request')
    } finally {
      setPairingDecisionBusyID(null)
    }
  }

  const handleSaveGroupName = async () => {
    if (!group) {
      return
    }
    const normalized = groupNameDraft.trim()
    if (!normalized) {
      setError('Group name is required.')
      return
    }
    setBusy(true)
    setError(null)
    setStatus(null)
    try {
      await upsertSwarmGroup({ groupID: group.group.id, name: normalized, setCurrent: true })
      await refresh()
      setEditingGroupName(false)
      setStatus(`Saved group name as ${normalized}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save group name')
    } finally {
      setBusy(false)
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
        saveSwarmSettings({
          current: uiSettings,
          name: normalized,
          defaultNewSessionMode: normalizeDefaultNewSessionMode(uiSettings.chat?.default_new_session_mode),
        }),
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

  const openAddSwarm = (target: 'local' | 'remote' = 'local') => {
    setAddSwarmInitialTarget(target)
    setAddSwarmOpen(true)
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
      <div className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div className="space-y-2">
          <h1 className="text-2xl font-semibold">Swarm</h1>
          <p className="max-w-3xl text-sm text-[var(--app-text-muted)]">
            Add local containers from your existing workspaces or link a Managed Swarm over Tailscale.
          </p>
        </div>
      </div>

      {error ? <Card data-testid="swarm-dashboard-error" className="border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">{error}</Card> : null}
      {status ? <Card data-testid="swarm-dashboard-status" className="border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-4 text-sm text-[var(--app-success)]">{status}</Card> : null}

      {visiblePendingPairings.length > 0 ? (
        <Card data-testid="swarm-pending-pairings" className="border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4">
          <div className="flex flex-col gap-3">
            <div>
              <div className="text-sm font-semibold text-[var(--app-text)]">Pending Managed Swarm request</div>
              <div className="mt-1 text-sm text-[var(--app-text-muted)]">Confirm the 6-character code on both machines before approving.</div>
            </div>
            {visiblePendingPairings.map((request) => {
              const requestID = request.request_id.trim()
              const busyRequest = pairingDecisionBusyID === requestID
              return (
                <div key={requestID || request.managed_swarm_id || request.managed_name} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                  <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
                    <div className="min-w-0 space-y-1 text-sm">
                      <div className="font-medium text-[var(--app-text)]">{request.managed_name || 'Managed Swarm'}</div>
                      <div className="text-[var(--app-text-muted)]">{request.managed_endpoint || request.managed_swarm_id || requestID}</div>
                      {request.managed_fingerprint ? <div className="break-all text-xs text-[var(--app-text-muted)]">Fingerprint: {request.managed_fingerprint}</div> : null}
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone="warning">{formatPairingCode(request.ceremony_code) || 'No code'}</Badge>
                      <Button size="sm" variant="outline" disabled={busy || busyRequest} onClick={() => void handlePairingDecision(request, false)}>Reject</Button>
                      <Button size="sm" disabled={busy || busyRequest || normalizePairingCode(request.ceremony_code).length !== 6} onClick={() => void handlePairingDecision(request, true)}>Approve</Button>
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
        </Card>
      ) : null}

      <div className="flex flex-col gap-8">
        <Card className="p-5">
                <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
                  <div>
                    {editingGroupName ? (
                      <div className="flex flex-wrap items-center gap-2">
                        <Input value={groupNameDraft} onChange={(event) => setGroupNameDraft(event.target.value)} className="w-[280px]" />
                        <Button onClick={() => void handleSaveGroupName()} disabled={busy || !groupNameDirty}>Save</Button>
                        <Button variant="outline" onClick={() => { setGroupNameDraft(group?.group.name || ''); setEditingGroupName(false) }} disabled={busy}>Cancel</Button>
                      </div>
                    ) : (
                      <div className="flex flex-wrap items-center gap-2">
                        <h2 className="text-xl font-semibold">{groupDisplayName}</h2>
                        {localGroupEditable ? (
                          <Button variant="ghost" size="sm" className="h-8 w-8 min-h-8 min-w-8 rounded-full p-0" onClick={() => setEditingGroupName(true)} disabled={busy}>
                            <Pencil size={14} />
                          </Button>
                        ) : null}
                      </div>
                    )}
                    <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                      {group ? `${group.members.length} members` : 'No current group returned yet.'}
                    </p>
                    <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-muted)]">
                      <Badge tone={localIsMaster ? 'live' : localIsChild ? 'warning' : 'neutral'}>This device is {localSwarmRoleLabel}</Badge>
                      {!localIsMaster ? <Badge tone="neutral">Master: {groupMasterName}</Badge> : null}
                    </div>
                  </div>
                  <div className="flex flex-col items-start gap-2 md:items-end">
                    <Badge tone={localTailscalePrimary ? 'live' : tailscaleCandidate.connected ? 'warning' : 'neutral'}>
                      {localTailscalePrimary ? 'Tailscale' : tailscaleCandidate.connected ? 'Tailscale connected · LAN config' : formatUnderscoreLabel(onboardingStatus?.config.mode || swarmState?.node.advertise_mode || 'lan')}
                    </Badge>
                    <div className="flex flex-wrap items-center gap-2">
                      <Button type="button" data-testid="swarm-dashboard-add-container" onClick={() => openAddSwarm('local')} disabled={masterControlsDisabled} title={localIsChild ? 'Managed swarms cannot add local containers to the Manager group.' : undefined}>
                        <Plus size={14} />
                        Add Container
                      </Button>
                      <Button type="button" variant="outline" data-testid="swarm-dashboard-link-swarm" onClick={() => openAddSwarm('remote')} disabled={masterControlsDisabled} title={localIsChild ? 'Managed swarms cannot link other swarms to the Manager group.' : undefined}>
                        <Link2 size={14} />
                        Link Swarm
                      </Button>
                      <Button
                        variant="outline"
                        onClick={() => {
                          if (isSwarmMode) {
                            void handleDisableSwarmMode()
                            return
                          }
                          void handleEnableSwarmMode()
                        }}
                        disabled={masterControlsDisabled}
                        title={localIsChild ? 'Child swarms cannot toggle master Swarm Mode controls.' : undefined}
                      >
                        {isSwarmMode ? 'Turn Off Swarm Mode' : 'Turn On Swarm Mode'}
                      </Button>
                    </div>
                  </div>
                </div>

                {loading && onboardingStatus === null ? (
                  <div className="mt-4 rounded-2xl border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)]">
                    Loading swarm configuration…
                  </div>
                ) : !isSwarmMode ? (
                  <div className="mt-4 rounded-2xl border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)]">
                    Swarm Mode is off. Turn it on to name this machine automatically and use Tailscale reachability when Tailscale is connected.
                  </div>
                ) : group ? (
                  <div className="mt-5 grid gap-4 grid-cols-1 md:grid-cols-2 xl:grid-cols-4">
                    {group.members.map((member) => {
                        const isLocalMember = member.swarmID === localSwarmID
                        const isMasterMember = member.swarmID === groupMasterID || member.membershipRole === 'host'
                        const memberRoleLabel = isMasterMember ? 'Master' : swarmRoleLabel(member.swarmRole || 'child')
                        const attachedDeployment = groupDeploymentByChildSwarmID.get(member.swarmID) ?? null
                        const attachedRemoteSession = groupRemoteSessionByChildSwarmID.get(member.swarmID) ?? null
                        const attachedContainer = attachedDeployment
                          ? localContainers.find((container) => deploymentMatchesContainer(attachedDeployment, container)) ?? null
                          : null
                        const remoteDeleteCandidate = remoteDeleteSwarmCandidates.find((candidate) => candidate.swarmID === member.swarmID) ?? null
                        const staleRemoteDeleteCandidate = staleRemoteDeleteSwarmCandidates.find((candidate) => candidate.swarmID === member.swarmID) ?? null
                        const attachedBypassPermissions = Boolean(attachedDeployment?.bypass_permissions)
                        const attachedRemoteBypassPermissions = Boolean(attachedRemoteSession?.bypass_permissions)
                        const attachedContainerName = attachedDeployment?.container_name || attachedContainer?.containerName || ''
                        const attachedNetworkName = attachedContainer?.networkName || attachedDeployment?.group_network_name || ''
                        const attachedImage = attachedDeployment?.image || attachedContainer?.image || ''
                        const attachedRemoteStatus = String(attachedRemoteSession?.status ?? '').trim()
                        const attachedRemoteActive = Boolean(attachedRemoteSession && ['attached', 'approved', 'waiting_for_approval', 'waiting_for_child'].includes(attachedRemoteStatus))
                        const localWorkspaceSummaries = (attachedDeployment?.workspace_bootstrap ?? []).map(summarizeWorkspaceBootstrap)
                        const remoteWorkspaceSummaries = (attachedRemoteSession?.preflight.payloads ?? []).map(summarizeRemotePayload)
                        const childAPIURL = attachedDeployment?.child_backend_url
                          || urlForHostPort(browserProtocol, browserHost, attachedDeployment?.backend_host_port || 0)
                        const childDesktopURL = attachedDeployment?.child_desktop_url
                          || urlForHostPort(browserProtocol, browserHost, attachedDeployment?.desktop_host_port || 0)
                        const remoteTailnetURL = remoteTailnetVisitURL(attachedRemoteSession?.remote_tailnet_url, attachedRemoteSession?.remote_endpoint)
                        const attachStatus = String(attachedDeployment?.attach_status ?? '').trim()
                        const attachFailed = attachStatus === 'failed'
                        const deploymentRunning = attachedDeployment?.status === 'running' || attachedDeployment?.status === 'attached'
                        const canToggleDeployment = Boolean(attachedDeployment && !attachFailed)
                        const deploymentStatusLabel = attachFailed
                          ? 'failed attach'
                          : (attachedDeployment?.status || attachedContainer?.status || 'unknown')
                        const remoteStatusLabel = formatRemoteSessionStatus(attachedRemoteStatus || 'unknown')
                        const memberDisplayName = attachedContainerName || attachedRemoteSession?.child_name || attachedRemoteSession?.name || member.name || member.swarmID
                        
                        return (
                          <div
                            key={`${member.groupID}:${member.swarmID}`}
                            className={`flex flex-col rounded-xl border ${isLocalMember ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_5%,var(--app-surface))] shadow-sm md:col-span-2 xl:col-span-4' : 'border-[var(--app-border)] bg-[var(--app-surface)]'} overflow-hidden`}
                          >
                            <div className={`flex flex-wrap items-center justify-between gap-3 border-b p-3 ${isLocalMember ? 'border-[color-mix(in_oklab,var(--app-primary)_20%,transparent)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}`}>
                              <div className="flex flex-wrap items-center gap-2 min-w-0">
                                <Badge tone={isMasterMember ? 'live' : 'neutral'}>{memberRoleLabel}</Badge>
                                {isLocalMember ? <Badge tone="live">This device</Badge> : null}
                                {attachedDeployment ? <Badge tone={attachFailed ? 'warning' : 'live'}>Container</Badge> : null}
                                {!attachedDeployment && attachedRemoteSession ? <Badge tone={attachedRemoteActive ? 'live' : 'warning'}>Remote</Badge> : null}
                                {!attachedDeployment && !attachedRemoteSession && staleRemoteDeleteCandidate ? <Badge tone="warning">Stale</Badge> : null}
                              </div>
                              <div className="flex items-center gap-2">
                                {attachedDeployment ? (
                                  <Badge tone={attachFailed ? 'warning' : 'neutral'}>{deploymentStatusLabel}</Badge>
                                ) : null}
                                {!attachedDeployment && attachedRemoteSession ? (
                                  <Badge tone={attachedRemoteActive ? 'neutral' : 'warning'}>{remoteStatusLabel}</Badge>
                                ) : null}
                                {!attachedDeployment && !attachedRemoteSession && staleRemoteDeleteCandidate ? (
                                  <Badge tone="warning">stale record</Badge>
                                ) : null}
                              </div>
                            </div>
                            
                            <div className="p-4 flex flex-col gap-4 flex-1">
                              {isLocalMember && editingLocalName ? (
                                <div className="flex flex-col gap-2">
                                  <Input value={localNameDraft} onChange={(event) => setLocalNameDraft(event.target.value)} />
                                  <div className="flex items-center gap-2">
                                    <Button onClick={() => void handleSaveLocalName()} disabled={busy || !localNameDirty}>Save</Button>
                                    <Button variant="outline" onClick={() => { setLocalNameDraft(localSwarmName); setEditingLocalName(false) }} disabled={busy}>Cancel</Button>
                                  </div>
                                </div>
                              ) : (
                                <div className="flex flex-wrap items-start justify-between gap-2">
                                  <div className="flex items-center gap-2">
                                    {attachedDeployment || attachedRemoteSession ? <Boxes size={20} className="text-[var(--app-text-muted)] shrink-0" /> : null}
                                    <div className="truncate text-base font-semibold text-[var(--app-text)]">{memberDisplayName}</div>
                                  </div>
                                  {isLocalMember ? (
                                    <Button variant="ghost" size="sm" className="h-7 w-7 min-h-7 min-w-7 rounded-full p-0 shrink-0" onClick={() => setEditingLocalName(true)} disabled={busy}>
                                      <Pencil size={14} />
                                    </Button>
                                  ) : null}
                                </div>
                              )}

                              {isLocalMember ? (
                                <div className="flex-1 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
                                  <div className="flex flex-col border border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] bg-[color-mix(in_oklab,var(--app-surface-subtle)_50%,transparent)] rounded-xl p-4">
                                    <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--app-text-muted)] mb-3">Swarm.conf</div>
                                    <div className="grid grid-cols-2 gap-3 text-xs flex-1">
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Bind</span>
                                        <span className="font-medium text-[var(--app-text)] truncate">{localBindStatus}</span>
                                        <span className="text-[var(--app-text-muted)] truncate">{configuredHost}:{backendPort}</span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Primary Transport</span>
                                        <span className="font-medium text-[var(--app-text)] truncate">{localTailscalePrimary ? 'Tailscale' : tailscaleCandidate.connected ? 'Tailscale connected · LAN config' : (localBindStatus === 'Local only' ? (localTransportActive ? 'Local' : 'Local only') : formatUnderscoreLabel(onboardingStatus?.config.mode || swarmState?.node.advertise_mode || 'lan'))}</span>
                                      </div>
                                      <div className="flex flex-col gap-1 col-span-2">
                                        <span className="text-[var(--app-text-muted)]">Advertise Endpoint</span>
                                        <span className="font-medium text-[var(--app-text)] truncate">{(onboardingStatus?.config.advertiseHost || backendHost)}:{onboardingStatus?.config.advertisePort || backendPort}</span>
                                      </div>
                                    </div>
                                    <div className="mt-4 grid grid-cols-1 gap-2 text-xs border-t border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] pt-3">
                                      <div className="flex justify-between items-center gap-2">
                                        <span className="text-[var(--app-text-muted)]">Frontend</span>
                                        <span className="font-medium text-[var(--app-text)] truncate text-right">{frontendOrigin || 'Unavailable'}</span>
                                      </div>
                                      <div className="flex justify-between items-center gap-2">
                                        <span className="text-[var(--app-text-muted)]">Backend API</span>
                                        <span className="font-medium text-[var(--app-text)] truncate text-right">{backendURL}</span>
                                      </div>
                                    </div>
                                  </div>

                                  <div className="flex flex-col gap-4">
                                    <div className="flex-1 flex flex-col border border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] bg-[color-mix(in_oklab,var(--app-surface-subtle)_50%,transparent)] rounded-xl p-4">
                                      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--app-text-muted)] mb-3">Transports</div>
                                      <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-1 gap-4 text-xs flex-1">
                                        <div className="flex flex-col gap-1 border-b border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] sm:border-b-0 sm:border-r md:border-r-0 md:border-b pb-3 sm:pb-0 sm:pr-3 md:pr-0 md:pb-3">
                                          <span className="text-[var(--app-text-muted)]">Local [{localTransportPort}]</span>
                                          <span className="font-medium text-[var(--app-text)]">{localTransportStatus}</span>
                                          {localTransportWarning ? <span className="mt-1 text-[var(--app-danger)]">{localTransportWarning}</span> : null}
                                        </div>
                                        <div className="flex flex-col gap-1">
                                          <span className="text-[var(--app-text-muted)]">Tailscale URL</span>
                                          <span className="font-medium text-[var(--app-text)]">{localTailscaleHosting.summary}</span>
                                          <span className="text-[var(--app-text-muted)]">{localTailscaleURL || 'No tailnet URL detected yet'}</span>
                                        </div>
                                      </div>
                                    </div>
                                  </div>

                                  {(!localTailscalePrimary && tailscaleCandidate.available) || localTailscalePrimary ? (
                                    <div className="flex flex-col border border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] bg-[color-mix(in_oklab,var(--app-surface-subtle)_50%,transparent)] rounded-xl p-4">
                                      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--app-text-muted)] mb-3">Tailscale Access</div>
                                      <div className="space-y-3 text-xs">
                                        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                                          <div className="font-medium text-[var(--app-text)]">Live tailnet status</div>
                                          <div className="mt-1 text-[var(--app-text-muted)]">{localTailscaleHosting.detail}</div>
                                          {localTailscaleServe?.proxyTarget ? (
                                            <div className="mt-2 text-[var(--app-text-muted)]">Current proxy target: <span className="font-mono text-[var(--app-text)]">{localTailscaleServe.proxyTarget}</span></div>
                                          ) : null}
                                        </div>

                                        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                                          <div className="font-medium text-[var(--app-text)]">Host This Swarm</div>
                                          <div className="mt-1 text-[var(--app-text-muted)]">Serve the desktop port so this tailnet URL opens the full Swarm UI and API for this machine.</div>
                                          <div className="mt-2 flex items-center gap-2">
                                            <div className="flex-1 rounded border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2 font-mono text-[10px] truncate text-[var(--app-text)]" title={desktopServeCommand}>{desktopServeCommand}</div>
                                            <Button type="button" variant="outline" size="sm" className="h-8 text-[11px]" onClick={() => void handleCopyTailscaleCommand(desktopServeCommand, 'desktop')}>
                                              {copyState === 'desktop' ? 'Copied' : 'Copy'}
                                            </Button>
                                          </div>
                                        </div>

                                        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                                          <div className="font-medium text-[var(--app-text)]">Peer Transport Only</div>
                                          <div className="mt-1 text-[var(--app-text-muted)]">Serve only the dedicated peer lane. This is useful when you want remote swarm transport without hosting the desktop UI.</div>
                                          <div className="mt-2 flex items-center gap-2">
                                            <div className="flex-1 rounded border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2 font-mono text-[10px] truncate text-[var(--app-text)]" title={peerTransportServeCommand}>{peerTransportServeCommand}</div>
                                            <Button type="button" variant="outline" size="sm" className="h-8 text-[11px]" onClick={() => void handleCopyTailscaleCommand(peerTransportServeCommand, 'peer')}>
                                              {copyState === 'peer' ? 'Copied' : 'Copy'}
                                            </Button>
                                          </div>
                                        </div>

                                        {!localTailscalePrimary && tailscaleCandidate.available ? (
                                          <Button type="button" variant="outline" size="sm" className="h-8 text-[11px] w-full" onClick={() => void handleUseTailscaleReachability()} disabled={busy || !localTailscaleURL}>
                                            Use Tailscale as Swarm reachability
                                          </Button>
                                        ) : null}
                                        {localTailscalePrimary ? (
                                          <div className="text-[var(--app-text-muted)]">
                                            Swarm reachability is already set to Tailscale. Hosting this machine on the tailnet is a separate live Tailscale Serve choice.
                                          </div>
                                        ) : null}
                                      </div>
                                    </div>
                                  ) : null}
                                </div>
                              ) : attachedDeployment ? (
                                <div className="flex-1 flex flex-col justify-between space-y-4">
                                  <div className="space-y-4">
                                    <div className="grid grid-cols-2 gap-3 text-xs">
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Network</span>
                                        <span className="font-medium text-[var(--app-text)] truncate">{attachedNetworkName || 'unassigned'}</span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Swarm Sync</span>
                                        <span className="font-medium text-[var(--app-text)]">{attachedDeployment.sync_enabled ? `${attachedDeployment.sync_mode || 'managed'} (${(attachedDeployment.sync_modules ?? []).join(', ') || 'default'})` : 'off'}</span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Permissions</span>
                                        <span className="font-medium text-[var(--app-text)]">{attachedBypassPermissions ? 'Bypass ON' : 'Host-managed'}</span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Workspaces</span>
                                        <span className="font-medium text-[var(--app-text)]">{localWorkspaceSummaries.length}</span>
                                      </div>
                                      {attachedImage ? (
                                        <div className="col-span-2 flex flex-col gap-1">
                                          <span className="text-[var(--app-text-muted)]">Image</span>
                                          <span className="font-medium text-[var(--app-text)] truncate" title={attachedImage}>{attachedImage}</span>
                                        </div>
                                      ) : null}
                                    </div>

                                    <Button
                                      variant="outline"
                                      size="sm"
                                      onClick={() => {
                                        setSettingsDeployment(attachedDeployment)
                                        setSettingsError(null)
                                      }}
                                    >
                                      <Pencil size={14} />
                                      Edit sync settings
                                    </Button>

                                    {localWorkspaceSummaries.length > 0 ? (
                                      <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
                                        <div className="text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Replicated Workspaces</div>
                                        <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                                          {localWorkspaceSummaries.map((summary) => (
                                            <div key={summary}>{summary}</div>
                                          ))}
                                        </div>
                                      </div>
                                    ) : null}

                                    {(childDesktopURL || childAPIURL) ? (
                                      <div className="grid grid-cols-1 gap-2 text-xs">
                                        {childDesktopURL && (
                                          <div className="flex justify-between items-center gap-2 border-t border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] pt-2">
                                            <span className="text-[var(--app-text-muted)]">Desktop Link</span>
                                            <a href={childDesktopURL} target="_blank" rel="noreferrer" className="text-[var(--app-primary)] hover:underline truncate text-right">{childDesktopURL}</a>
                                          </div>
                                        )}
                                        {childAPIURL && (
                                          <div className="flex justify-between items-center gap-2 border-t border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] pt-2">
                                            <span className="text-[var(--app-text-muted)]">API Link</span>
                                            <a href={childAPIURL} target="_blank" rel="noreferrer" className="text-[var(--app-primary)] hover:underline truncate text-right">{childAPIURL}</a>
                                          </div>
                                        )}
                                      </div>
                                    ) : null}

                                    {attachedDeployment?.last_attach_error ? (
                                      <div className="mt-2 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-[var(--app-warning-text)] text-xs">
                                        {attachedDeployment.last_attach_error}
                                      </div>
                                    ) : null}
                                  </div>

                                  <div className="flex items-center justify-center gap-4 border-t border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] pt-4 mt-auto">
                                    {canToggleDeployment ? (
                                      <Button
                                        type="button"
                                        variant="outline"
                                        className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl p-0"
                                        title={deploymentRunning ? 'Turn off container' : 'Start container'}
                                        onClick={() => void handleDeploymentAction(attachedDeployment, deploymentRunning ? 'stop' : 'start')}
                                        disabled={busy}
                                      >
                                        {deploymentRunning ? <Square size={22} className="shrink-0" /> : <Play size={22} className="shrink-0" />}
                                      </Button>
                                    ) : null}
                                    {attachedContainer ? (
                                      <Button
                                        type="button"
                                        variant="outline"
                                        className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl p-0 text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)]"
                                        title="Remove linked container swarm"
                                        onClick={() => openDeleteSwarms([`local:${attachedContainer.id}`], [`local:${attachedContainer.id}`])}
                                        disabled={busy}
                                      >
                                        <Trash2 size={22} className="shrink-0" />
                                      </Button>
                                    ) : null}
                                  </div>
                                </div>
                              ) : (attachedRemoteSession || staleRemoteDeleteCandidate) ? (
                                <div className="flex-1 flex flex-col justify-between space-y-4">
                                  <div className="space-y-4">
                                    <div className="grid grid-cols-2 gap-3 text-xs">
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Remote Host</span>
                                        <span className="font-medium text-[var(--app-text)] truncate">{attachedRemoteSession?.ssh_session_target || 'unknown'}</span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Remote Runtime</span>
                                        <span className="font-medium text-[var(--app-text)]">{attachedRemoteSession?.remote_runtime || 'unknown'}</span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Swarm Sync</span>
                                        <span className="font-medium text-[var(--app-text)]">
                                          {attachedRemoteSession
                                            ? (attachedRemoteSession.sync_enabled ? (attachedRemoteSession.sync_mode || 'managed') : 'off')
                                            : 'unknown'}
                                        </span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Permissions</span>
                                        <span className="font-medium text-[var(--app-text)]">
                                          {attachedRemoteSession
                                            ? (attachedRemoteBypassPermissions ? 'Bypassed' : 'Enforced')
                                            : 'unknown'}
                                        </span>
                                      </div>
                                      <div className="flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Role</span>
                                        <span className="font-medium text-[var(--app-text)]">{memberRoleLabel}</span>
                                      </div>
                                      <div className="col-span-2 flex flex-col gap-1">
                                        <span className="text-[var(--app-text-muted)]">Child Swarm</span>
                                        <span className="font-medium text-[var(--app-text)] truncate">{member.swarmID}</span>
                                      </div>
                                    </div>

                                    {remoteWorkspaceSummaries.length > 0 ? (
                                      <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
                                        <div className="text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Remote Payloads</div>
                                        <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                                          {remoteWorkspaceSummaries.map((summary) => (
                                            <div key={summary}>{summary}</div>
                                          ))}
                                        </div>
                                      </div>
                                    ) : null}

                                    <AccessURLRow label="Child Tailscale URL" url={remoteTailnetURL} />

                                    {attachedRemoteSession?.last_error ? (
                                      <div className="mt-2 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-[var(--app-warning-text)] text-xs">
                                        {attachedRemoteSession.last_error}
                                      </div>
                                    ) : null}

                                    {!attachedRemoteActive ? (
                                      <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning-text)]">
                                        This remote child is no longer fully attached here. You can either SSH-delete the remote child or remove only the saved master-side records.
                                      </div>
                                    ) : null}
                                  </div>

                                  {remoteDeleteCandidate || staleRemoteDeleteCandidate ? (
                                    <div className="flex items-center justify-center gap-4 border-t border-[color-mix(in_oklab,var(--app-border)_50%,transparent)] pt-4 mt-auto">
                                      <Button
                                        type="button"
                                        variant="outline"
                                        className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl p-0 text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] hover:border-[var(--app-danger-border)] hover:text-[var(--app-danger)]"
                                        title={remoteDeleteCandidate ? 'Remove linked Managed Swarm' : 'Remove stale Managed Swarm record'}
                                        onClick={() => {
                                          const deleteCandidate = remoteDeleteCandidate ?? staleRemoteDeleteCandidate
                                          if (!deleteCandidate) {
                                            return
                                          }
                                          openDeleteSwarms([deleteCandidate.selectionID], [deleteCandidate.selectionID])
                                        }}
                                        disabled={busy}
                                      >
                                        <Trash2 size={22} className="shrink-0" />
                                      </Button>
                                    </div>
                                  ) : null}
                                </div>
                              ) : (
                                <div className="text-sm text-[var(--app-text-muted)]">No container attached.</div>
                              )}
                            </div>
                          </div>
                        )
                      })}
                    </div>
                ) : (
                  <div className="mt-4 rounded-2xl border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)]">
                    This swarm is on, but no current group was returned by the backend yet.
                  </div>
                )}
              </Card>

              <Card className="p-5">
                <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
                  <div>
                    <h2 className="text-xl font-semibold">Local swarm containers</h2>
                    <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                      Add Container chooses the runtime, creates or reuses the group network, and wires local children automatically for communication. Linked Managed Swarms stay visible here after attach.
                    </p>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button type="button" variant="outline" onClick={openDeleteContainers} disabled={busy || deleteCandidates.length === 0}>
                      <CheckSquare size={14} />
                      Delete containers
                    </Button>
                    {staleLocalContainers.length > 0 ? (
                      <Button type="button" variant="outline" onClick={() => void handlePruneMissingLocalContainers()} disabled={busy}>
                        Remove all stale
                      </Button>
                    ) : null}
                    {runtimeLoading ? (
                      <Badge tone="neutral">detecting runtime…</Badge>
                    ) : localRuntime.installed.length > 0 ? (
                      localRuntime.installed.map((runtime) => (
                        <Badge key={runtime} tone={localRuntime.available.includes(runtime) ? (runtime === localRuntime.recommended ? 'live' : 'neutral') : 'warning'}>
                          {runtime}{localRuntime.available.includes(runtime) ? '' : ' blocked'}
                        </Badge>
                      ))
                    ) : (
                      <Badge tone="warning">no runtime detected</Badge>
                    )}
                  </div>
                </div>

                {localRuntime.warning ? (
                  <div className="mt-4 rounded-2xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning-text)]">
                    <div className="flex items-start gap-3">
                      <TriangleAlert size={16} className="mt-0.5" />
                      <div>{localRuntime.warning}</div>
                    </div>
                  </div>
                ) : null}

                <div className="mt-5 grid gap-3 md:grid-cols-2 xl:grid-cols-4">
                  {localContainersSectionLoading ? (
                    <div className="rounded-2xl border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)] md:col-span-2 xl:col-span-4">
                      Loading local swarm containers…
                    </div>
                  ) : visibleLocalContainers.length === 0 ? (
                    <div className="rounded-2xl border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)] md:col-span-2 xl:col-span-4">
                      No local swarm containers yet. Standalone containers and replicated child swarms will both appear here.
                    </div>
                  ) : (
                    visibleLocalContainers.map((container) => {
                      const attachedDeployment = deployments.find((deployment) => (
                        String(deployment.attach_status ?? '').trim() === 'attached'
                        && deploymentMatchesContainer(deployment, container)
                      )) ?? null
                      const running = container.status === 'running'
                      const missing = container.status === 'missing'
                      const childDesktopURL = attachedDeployment?.child_desktop_url
                        || urlForHostPort(
                          browserProtocol,
                          browserHost,
                          attachedDeployment?.desktop_host_port || (container.hostPort > 0 ? container.hostPort + 1 : 0),
                        )
                      const childAPIURL = attachedDeployment?.child_backend_url
                        || urlForHostPort(browserProtocol, browserHost, attachedDeployment?.backend_host_port || container.hostPort)
                      const containerAction = attachedDeployment
                        ? () => handleDeploymentAction(attachedDeployment, running ? 'stop' : 'start')
                        : () => handleLocalContainerAction(container, running ? 'stop' : 'start')
                      return (
                        <div key={container.id} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                          <div className="flex flex-wrap items-center justify-between gap-2">
                            <div className="min-w-0">
                              <div className="truncate text-sm font-semibold text-[var(--app-text)]">{container.name}</div>
                              <div className="mt-1 text-xs text-[var(--app-text-muted)]">{container.containerName}</div>
                            </div>
                            <div className="flex flex-wrap items-center gap-2">
                              {attachedDeployment ? (
                                <Badge tone="live">{attachedDeployment.child_display_name || attachedDeployment.child_swarm_id || 'attached child'}</Badge>
                              ) : null}
                              <Badge tone={running ? 'live' : 'neutral'}>{container.status || 'created'}</Badge>
                            </div>
                          </div>
                          <div className="mt-3 grid gap-1 text-xs text-[var(--app-text-muted)]">
                            <div>Runtime: {container.runtime || 'unknown'}</div>
                            <div>Network: {container.networkName || groupNetworkName || suggestGroupNetworkName(group?.group.name || group?.group.id || localSwarmName || 'swarm-group')}</div>
                            <div>API port: {container.runtimePort || 'auto'}</div>
                            <div>Folders: {container.mounts.length}</div>
                            {attachedDeployment ? <div>Associated swarm: {attachedDeployment.child_display_name || attachedDeployment.child_swarm_id || 'attached'}</div> : null}
                            {attachedDeployment ? <div>Bypass permissions: {attachedDeployment.bypass_permissions ? 'Enabled' : 'Disabled'}</div> : null}
                            {attachedDeployment ? <div>Group: {attachedDeployment.group_name || attachedDeployment.group_id || 'unknown'}</div> : null}
                            <div>Updated: {formatTime(container.updatedAt)}</div>
                          </div>
                          <AccessURLRow label="Desktop" url={childDesktopURL} />
                          <AccessURLRow label="Backend API" url={childAPIURL} />
                          {container.warning ? (
                            <div className="mt-3 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning-text)]">
                              {container.warning}
                            </div>
                          ) : null}
                          <div className="mt-4 flex gap-2">
                            {missing ? (
                              <Button type="button" variant="outline" onClick={() => void handleRemoveMissingLocalContainer(container)} disabled={busy}>
                                Remove stale
                              </Button>
                            ) : (
                              <Button type="button" variant="outline" onClick={() => void containerAction()} disabled={busy}>
                                <Square size={14} />
                                {running ? 'Stop' : 'Start'}
                              </Button>
                            )}
                          </div>
                        </div>
                      )
                    })
                  )}
                </div>
              </Card>

              <Card className="p-5">
                <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
                  <div>
                    <h2 className="text-xl font-semibold">Stale attached swarms</h2>
                    <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                      Attached child swarm records that still exist in persisted deploy state but are not part of the current dashboard group or no longer have a local container record.
                    </p>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge tone={staleAttachedLoading ? 'neutral' : staleAttachedDeployments.length > 0 ? 'warning' : 'neutral'}>
                      {staleAttachedLoading ? '…' : staleAttachedDeployments.length}
                    </Badge>
                    {!staleAttachedLoading && staleAttachedDeployments.length > 0 ? (
                      <Button type="button" variant="outline" onClick={() => void handleDeleteStaleAttachedDeployments()} disabled={busy}>
                        <Trash2 size={14} />
                        Remove all stale
                      </Button>
                    ) : null}
                  </div>
                </div>

                <div className="mt-5 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                  {staleAttachedLoading ? (
                    <div className="rounded-2xl border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)] md:col-span-2 xl:col-span-3">
                      Loading persisted attached swarm records…
                    </div>
                  ) : staleAttachedDeployments.length === 0 ? (
                    <div className="rounded-2xl border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)] md:col-span-2 xl:col-span-3">
                      No stale attached swarm records are currently persisted.
                    </div>
                  ) : (
                    staleAttachedDeployments.map((deployment) => {
                      const deploymentGroupID = String(deployment.group_id ?? '').trim()
                      const matchedContainer = localContainers.find((container) => deploymentMatchesContainer(deployment, container)) ?? null
                      const outsideCurrentGroup = currentGroupID !== '' && deploymentGroupID !== '' && deploymentGroupID !== currentGroupID
                      const childLabel = deployment.child_display_name || deployment.child_swarm_id || deployment.name || deployment.id
                      return (
                        <div key={deployment.id} className="rounded-2xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4">
                          <div className="flex flex-wrap items-center justify-between gap-2">
                            <div className="min-w-0">
                              <div className="truncate text-sm font-semibold text-[var(--app-text)]">{childLabel}</div>
                              <div className="mt-1 text-xs text-[var(--app-text-muted)]">{deployment.container_name || deployment.id}</div>
                            </div>
                            <div className="flex flex-wrap items-center gap-2">
                              <Badge tone="warning">{deployment.attach_status || 'attached'}</Badge>
                              {outsideCurrentGroup ? <Badge tone="warning">old group</Badge> : null}
                              {!matchedContainer ? <Badge tone="warning">missing container</Badge> : null}
                            </div>
                          </div>

                          <div className="mt-3 grid gap-1 text-xs text-[var(--app-text-muted)]">
                            <div>Saved group: {deployment.group_name || deploymentGroupID || 'none'}</div>
                            {outsideCurrentGroup ? <div>Current dashboard group: {groupDisplayName}</div> : null}
                            <div>Child swarm: {deployment.child_swarm_id || 'unknown'}</div>
                            <div>Runtime: {deployment.runtime || 'unknown'}</div>
                            <div>Bypass permissions: {deployment.bypass_permissions ? 'Enabled' : 'Disabled'}</div>
                            <div>Status: {deployment.status || 'unknown'}</div>
                            <div>Updated: {formatTime(deployment.updated_at)}</div>
                          </div>

                          <AccessURLRow label="Desktop" url={deployment.child_desktop_url || ''} />
                          <AccessURLRow label="Backend API" url={deployment.child_backend_url || ''} />

                          {deployment.last_attach_error ? (
                            <div className="mt-3 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-surface)] px-3 py-2 text-xs text-[var(--app-warning-text)]">
                              {deployment.last_attach_error}
                            </div>
                          ) : null}

                          <div className="mt-3 grid gap-1 text-xs text-[var(--app-text-muted)]">
                            {!matchedContainer ? <div>No matching local container record exists for this attached deployment.</div> : null}
                            {outsideCurrentGroup ? <div>This record belongs to a different persisted group than the one currently shown on the dashboard.</div> : null}
                          </div>

                          <div className="mt-4 flex gap-2">
                            <Button
                              type="button"
                              variant="outline"
                              onClick={() => void handleDeleteAttachedSwarm(deployment, childLabel, deployment.child_swarm_id)}
                              disabled={busy}
                            >
                              <Trash2 size={14} />
                              Remove stale record
                            </Button>
                          </div>
                        </div>
                      )
                    })
                  )}
                </div>
              </Card>

              <div className="flex flex-col gap-6 border-t border-[var(--app-border)] pt-8">
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <h2 className="text-xl font-semibold">Other Swarms on Network</h2>
                    <p className="mt-1 text-sm text-[var(--app-text-muted)]">Running swarms discovered across available network transports, separate from the current group.</p>
                  </div>
                  <Badge tone={discoveredSwarmsLoading ? 'neutral' : discoveredSwarms.length > 0 ? 'live' : 'neutral'}>
                    {discoveredSwarmsLoading ? '…' : discoveredSwarms.length}
                  </Badge>
                </div>

                {discoveredSwarmsLoading ? (
                  <div className="rounded-xl border border-dashed border-[var(--app-border)] p-6 text-sm text-[var(--app-text-muted)] flex items-center justify-center">
                    Scanning available swarm transports…
                  </div>
                ) : discoveredSwarms.length === 0 ? (
                  <div className="rounded-xl border border-dashed border-[var(--app-border)] p-8 text-center text-sm text-[var(--app-text-muted)]">
                    No other running swarms are visible right now.
                  </div>
                ) : (
                  <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
                    {discoveredSwarms.map((candidate) => (
                      <div key={candidate.id || candidate.endpoint || candidate.dnsName} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] p-5 shadow-sm transition hover:border-[var(--app-border-strong)]">
                        <div className="flex items-start justify-between gap-3">
                          <div className="min-w-0">
                            <div className="truncate text-base font-semibold text-[var(--app-text)]">{candidate.name || candidate.dnsName || candidate.id}</div>
                            <div className="mt-1 truncate text-xs text-[var(--app-text-muted)]">{discoveredSwarmEndpoint(candidate)}</div>
                          </div>
                          <div className="flex flex-wrap justify-end gap-2 shrink-0">
                            <Badge tone={candidate.online ? 'live' : 'neutral'}>{candidate.online ? 'online' : 'seen'}</Badge>
                            <Badge tone="neutral">{candidate.source || candidate.transportMode || 'network'}</Badge>
                          </div>
                        </div>
                        <div className="mt-4 flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-muted)]">
                          <span>{formatUnderscoreLabel(candidate.role || 'standalone')}</span>
                          {candidate.transportMode ? <span>· {candidate.transportMode}</span> : null}
                          {candidate.currentRelationship ? <span>· {formatUnderscoreLabel(candidate.currentRelationship)}</span> : null}
                        </div>
                        {candidate.rendezvousTransports.length > 0 ? (
                          <div className="mt-2 text-xs text-[var(--app-text-muted)]">{transportSummary(candidate.rendezvousTransports)}</div>
                        ) : null}
                      </div>
                    ))}
                  </div>
                )}
              </div>
      </div>
    </>
  )

  return (
    <>
      {content}

      <AddSwarmModal
        open={addSwarmOpen}
        onboardingStatus={onboardingStatus}
        initialTarget={addSwarmInitialTarget}
        onOpenChange={setAddSwarmOpen}
        onComplete={handleAddSwarmComplete}
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
