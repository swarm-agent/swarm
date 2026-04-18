import { useEffect, useMemo, useState, type ChangeEvent, type ReactNode } from 'react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { cn } from '../../../../lib/cn'
import { ContainerProfilesPanel } from '../../containers/components/container-profiles-panel'
import {
  createSwarmInvite,
  decideSwarmEnrollment,
  fetchDesktopOnboardingStatus,
  fetchPendingSwarmEnrollments,
  fetchSwarmState,
  saveDesktopOnboarding,
  startRemoteSwarmPairing,
  type SwarmEnrollment,
  type SwarmLocalState,
} from '../../onboarding/api'
import type { DesktopOnboardingDiscoveredSwarm, DesktopOnboardingStatus } from '../../onboarding/types'
import { getUISettings } from '../../settings/swarm/queries/get-ui-settings'
import { saveSwarmSettings } from '../../settings/swarm/mutations/save-swarm-settings'
import { normalizeDefaultNewSessionMode, normalizeSwarmName, type UISettingsWire } from '../../settings/swarm/types/swarm-settings'
import { useDesktopStore } from '../../state/use-desktop-store'
import { SwarmSurfaceTabs, type SwarmSurfaceTab } from './swarm-surface-tabs'

type SwarmRole = 'standalone' | 'master' | 'child'

interface DesktopSwarmModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

interface GroupNode {
  id: string
  name: string
  role: string
  detail?: string
  highlight?: boolean
  locked?: boolean
  isLocal?: boolean
}

type PairingCopyState = 'idle' | 'copied' | 'error'
type PairingMode = 'manual' | 'remote'
type EditableSwarmRole = 'master' | 'child'

interface PairingFlowState {
  mode: PairingMode
  candidate: DesktopOnboardingDiscoveredSwarm | null
  inviteID: string
  inviteToken: string
  enrollment: SwarmEnrollment | null
  copyState: PairingCopyState
  childAuthCode: string
  childName: string
  childSwarmID: string
}

function roleLabel(role: string): string {
  if (role === 'master') return 'Master'
  if (role === 'child') return 'Child'
  return 'Standalone'
}

function canonicalEndpoint(status: DesktopOnboardingStatus | null, swarmState: SwarmLocalState | null): string {
  if (!status?.config.swarmMode) {
    return 'Local-only until Swarm Mode is enabled'
  }
  if (status?.config.mode === 'tailscale') {
    const tailscaleURL = status.config.tailscaleURL.trim()
    if (tailscaleURL) return tailscaleURL
    const tailnetURL = status.network.tailscale.tailnetURL.trim()
    if (tailnetURL) return tailnetURL
    const dnsName = status.network.tailscale.dnsName.trim()
    if (dnsName) return `https://${dnsName}`
  }
  if (status?.config.advertiseHost.trim()) {
    return `${status.config.advertiseHost.trim()}:${status.config.advertisePort}`
  }
  const primaryTransport = swarmState?.node.transports.find((transport) => transport.primary?.trim())
  return primaryTransport?.primary?.trim() || swarmState?.node.advertise_addr?.trim() || 'Not available yet'
}

function localSwarmName(onboardingStatus: DesktopOnboardingStatus | null, swarmState: SwarmLocalState | null): string {
  return onboardingStatus?.config.swarmName || swarmState?.node.name || 'Local swarm'
}

function localSwarmMetadata(status: DesktopOnboardingStatus | null, endpoint: string): string[] {
  const items: string[] = []
  const tailscaleURL = status?.config.tailscaleURL?.trim() || status?.network.tailscale.tailnetURL?.trim() || ''
  const dnsName = status?.network.tailscale.dnsName?.trim() || ''

  if (!status?.config.swarmMode) {
    items.push('Swarm Mode: Off (standalone local use)')
  } else {
    items.push(`Reachability: ${status.config.mode === 'tailscale' ? 'Tailscale HTTPS' : 'Direct/LAN'}`)
    items.push(`Bypass permissions: ${status.config.bypassPermissions ? 'Enabled' : 'Disabled'}`)
    if (endpoint && endpoint !== 'Not available yet') {
      items.push(`Advertised endpoint: ${endpoint}`)
    }
    if (status.config.mode === 'tailscale') {
      if (tailscaleURL) {
        items.push(`Tailscale URL: ${tailscaleURL}`)
      } else if (dnsName) {
        items.push(`Tailscale URL: https://${dnsName}`)
      }
    }
  }

  return items
}

function buildGroupNodes(onboardingStatus: DesktopOnboardingStatus | null, swarmState: SwarmLocalState | null): { title: string; description: string; nodes: GroupNode[] } {
  const localName = localSwarmName(onboardingStatus, swarmState)
  const localRole = onboardingStatus?.config.swarmRole || swarmState?.node.role || 'standalone'
  const localID = onboardingStatus?.config.swarmID || swarmState?.node.swarm_id || 'unassigned'
  const peers = swarmState?.trusted_peers ?? []
  const parentPeer = peers.find((peer) => peer.relationship === 'parent') ?? null
  const childPeers = peers.filter((peer) => peer.relationship === 'child')

  if (localRole === 'master') {
    return {
      title: 'Current Swarm Group',
      description: childPeers.length > 0 ? 'This swarm is acting as the master for its attached children.' : 'This swarm is ready to act as the master. Attach children manually when you are ready.',
      nodes: [
        {
          id: `local:${localID}`,
          name: localName,
          role: 'master',
          detail: 'This device',
          highlight: true,
          isLocal: true,
        },
        ...childPeers.map((peer) => ({
          id: `child:${peer.swarm_id}`,
          name: peer.name || peer.swarm_id,
          role: peer.role || 'child',
        })),
      ],
    }
  }

  if (localRole === 'child' && parentPeer) {
    return {
      title: 'Current Swarm Group',
      description: 'This swarm is currently attached to a master. Group membership is still controlled by the master swarm.',
      nodes: [
        {
          id: `parent:${parentPeer.swarm_id}`,
          name: parentPeer.name || parentPeer.swarm_id,
          role: parentPeer.role || 'master',
        },
        {
          id: `local:${localID}`,
          name: localName,
          role: 'child',
          detail: 'This device',
          highlight: true,
          locked: true,
          isLocal: true,
        },
      ],
    }
  }

  if (localRole === 'standalone') {
    return {
      title: 'Current Swarm Group',
      description: 'Swarm Mode is off. This device is running standalone and is not advertising itself for pairing or child management.',
      nodes: [
        {
          id: `local:${localID}`,
          name: localName,
          role: 'standalone',
          detail: 'This device',
          highlight: true,
          isLocal: true,
        },
      ],
    }
  }

  return {
    title: 'Current Swarm Group',
    description: 'This swarm is ready to act as the master. Add children when you are ready.',
    nodes: [
      {
        id: `local:${localID}`,
        name: localName,
        role: 'master',
        detail: 'This device',
        highlight: true,
        isLocal: true,
      },
    ],
  }
}

function GroupNodeCard({ node }: { node: GroupNode }) {
  return (
    <div
      className={cn(
        'rounded-2xl border p-4',
        node.highlight
          ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]'
          : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]',
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        <div className="text-sm font-semibold text-[var(--app-text)]">{node.name}</div>
        {node.locked ? <Badge tone="warning">managed</Badge> : null}
        <Badge tone={node.highlight ? 'live' : 'neutral'}>{roleLabel(node.role)}</Badge>
      </div>
      {node.detail ? <div className="mt-2 text-xs text-[var(--app-text-muted)]">{node.detail}</div> : null}
    </div>
  )
}

function PairingStep({
  number,
  title,
  state,
  children,
}: {
  number: number
  title: string
  state: 'pending' | 'active' | 'done' | 'danger'
  children: ReactNode
}) {
  return (
    <div
      className={cn(
        'rounded-2xl border p-4 transition-colors',
        state === 'done'
          ? 'border-[var(--app-success-border)] bg-[var(--app-success-bg)]'
          : state === 'active'
            ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]'
            : state === 'danger'
              ? 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)]'
              : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]',
      )}
    >
      <div className="flex items-center gap-3">
        <div
          className={cn(
            'grid h-8 w-8 place-items-center rounded-full text-xs font-semibold',
            state === 'done'
              ? 'bg-[var(--app-success)] text-white'
              : state === 'active'
                ? 'bg-[var(--app-primary)] text-white'
                : state === 'danger'
                  ? 'bg-[var(--app-danger)] text-white'
                  : 'bg-[var(--app-border)] text-[var(--app-text-muted)]',
          )}
        >
          {number}
        </div>
        <div className="text-sm font-semibold text-[var(--app-text)]">{title}</div>
      </div>
      <div className="mt-3 text-sm text-[var(--app-text-muted)]">{children}</div>
    </div>
  )
}

function LocalDeviceCard({
  currentRole,
  currentName,
  editedName,
  editedRole,
  editing,
  busy,
  loading,
  locked,
  canEdit,
  online,
  metadata,
  dirty,
  swarmMode,
  onEdit,
  onToggleSwarmMode,
  onCancel,
  onSave,
  onNameChange,
  onRoleChange,
}: {
  currentRole: SwarmRole
  currentName: string
  editedName: string
  editedRole: SwarmRole
  editing: boolean
  busy: boolean
  loading: boolean
  locked: boolean
  canEdit: boolean
  online: boolean
  metadata: string[]
  dirty: boolean
  swarmMode: boolean
  onEdit: () => void
  onToggleSwarmMode: () => void
  onCancel: () => void
  onSave: () => void
  onNameChange: (value: string) => void
  onRoleChange: (value: EditableSwarmRole) => void
}) {
  return (
    <div className="rounded-2xl border border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <div className="text-base font-semibold text-[var(--app-text)]">{editing ? 'Edit this device' : currentName}</div>
            <Badge tone="live">This device</Badge>
            <div className="inline-flex items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)]">
              <span
                className={cn(
                  'h-2 w-2 rounded-full',
                  online ? 'bg-[var(--app-success)]' : 'bg-[var(--app-text-muted)]',
                )}
              />
              <span>{online ? 'Online' : 'Offline'}</span>
            </div>
          </div>
          {!editing ? (
            <div className="mt-2 space-y-1 text-sm text-[var(--app-text-muted)]">
              <div>
                Type: {roleLabel(currentRole)}
                {locked ? ' · Group membership is currently controlled by the master swarm.' : ''}
              </div>
              {metadata.map((item) => (
                <div key={item} className="break-all">
                  {item}
                </div>
              ))}
            </div>
          ) : null}
        </div>
        {!editing ? (
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={onToggleSwarmMode} disabled={busy || loading}>
              {swarmMode ? 'Turn Off Swarm Mode' : 'Turn On Swarm Mode'}
            </Button>
            {canEdit ? (
              <Button variant="outline" onClick={onEdit} disabled={busy || loading}>
                Edit
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>

      {editing ? (
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <div className="flex flex-col gap-2">
            <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Name</label>
            <Input
              type="text"
              value={editedName}
              onChange={(event: ChangeEvent<HTMLInputElement>) => onNameChange(event.target.value)}
              disabled={busy || loading}
              autoComplete="off"
            />
          </div>
          <div className="flex flex-col gap-2">
            <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Type</label>
            <select
              value={editedRole}
              onChange={(event) => onRoleChange(event.target.value as EditableSwarmRole)}
              disabled={busy || loading}
              className="h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
            >
              <option value="master">Master</option>
              <option value="child">Child</option>
            </select>
          </div>

          <div className="md:col-span-2 flex flex-wrap gap-2">
            <Button onClick={onSave} disabled={busy || loading || !dirty}>
              {busy ? 'Saving…' : 'Save changes'}
            </Button>
            <Button variant="outline" onClick={onCancel} disabled={busy || loading}>
              Cancel
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  )
}

function candidateEndpoint(candidate: DesktopOnboardingDiscoveredSwarm): string {
  return candidate.tailnetURL || candidate.endpoint || candidate.dnsName || candidate.ips[0] || 'Unknown endpoint'
}

function candidateRemotePairingRequest(candidate: DesktopOnboardingDiscoveredSwarm) {
  return {
    endpoint: candidate.endpoint || candidate.tailnetURL || candidate.dnsName || candidate.ips[0] || '',
    dnsName: candidate.dnsName,
    ips: candidate.ips,
    childSwarmID: candidate.id,
    childName: candidate.name,
    rendezvousTransports: candidate.rendezvousTransports,
  }
}

function pairingTargetLabel(candidate: DesktopOnboardingDiscoveredSwarm | null): string {
  if (!candidate) {
    return 'manual child pairing'
  }
  return candidate.name || candidate.dnsName || candidateEndpoint(candidate)
}

export function DesktopSwarmModal({ open, onOpenChange }: DesktopSwarmModalProps) {
  const requestOnboardingFlow = useDesktopStore((state) => state.requestOnboardingFlow)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [statusMessage, setStatusMessage] = useState<string | null>(null)
  const [onboardingStatus, setOnboardingStatus] = useState<DesktopOnboardingStatus | null>(null)
  const [swarmState, setSwarmState] = useState<SwarmLocalState | null>(null)
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [editingLocal, setEditingLocal] = useState(false)
  const [editedName, setEditedName] = useState('')
  const [editedRole, setEditedRole] = useState<EditableSwarmRole>('master')
  const [pairingFlow, setPairingFlow] = useState<PairingFlowState | null>(null)
  const [activeTab, setActiveTab] = useState<SwarmSurfaceTab>('swarm')

  const syncEditableLocalFields = (onboarding: DesktopOnboardingStatus | null, state: SwarmLocalState | null) => {
    setEditedName(localSwarmName(onboarding, state))
    setEditedRole(((onboarding?.config.swarmRole || state?.node.role || 'master') === 'child' ? 'child' : 'master') as EditableSwarmRole)
  }

  const refresh = async () => {
    const [onboarding, state, nextUISettings] = await Promise.all([fetchDesktopOnboardingStatus(), fetchSwarmState(), getUISettings()])
    setOnboardingStatus(onboarding)
    setSwarmState(state)
    setUISettings(nextUISettings)
    syncEditableLocalFields(onboarding, state)
  }

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    setError(null)
    setStatusMessage(null)
    setEditingLocal(false)
    setPairingFlow(null)
    setActiveTab('swarm')

    void Promise.all([fetchDesktopOnboardingStatus(), fetchSwarmState(), getUISettings()])
      .then(([onboarding, state, nextUISettings]) => {
        if (cancelled) return
        setOnboardingStatus(onboarding)
        setSwarmState(state)
        setUISettings(nextUISettings)
        syncEditableLocalFields(onboarding, state)
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Failed to load swarm details')
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [open])

  const currentRole = (onboardingStatus?.config.swarmRole || swarmState?.node.role || 'standalone') as SwarmRole
  const currentName = localSwarmName(onboardingStatus, swarmState)
  const groupControlsLocked = currentRole === 'child'
  const group = useMemo(() => buildGroupNodes(onboardingStatus, swarmState), [onboardingStatus, swarmState])
  const endpoint = canonicalEndpoint(onboardingStatus, swarmState)
  const localOnline = Boolean(!onboardingStatus || !onboardingStatus.config.swarmMode || onboardingStatus.network.tailscale.connected || onboardingStatus.network.tailscale.tailnetURL || onboardingStatus.network.tailscale.dnsName || endpoint !== 'Not available yet')
  const localMetadata = useMemo(() => localSwarmMetadata(onboardingStatus, endpoint), [endpoint, onboardingStatus])
  const localNode = useMemo(() => group.nodes.find((node) => node.isLocal) ?? null, [group])
  const remoteGroupNodes = useMemo(() => group.nodes.filter((node) => !node.isLocal), [group])
  const discoveredCandidates = useMemo(() => (onboardingStatus?.discoveredSwarms ?? []).filter((item) => item.running && !item.inCurrentGroup), [onboardingStatus])
  const localDirty = normalizeSwarmName(editedName) !== normalizeSwarmName(currentName) || editedRole !== currentRole
  const remotePairing = pairingFlow?.mode === 'remote'
  const pairingInProgress = Boolean(pairingFlow && pairingFlow.enrollment?.status !== 'approved' && pairingFlow.enrollment?.status !== 'rejected')
  const pairingStepOneState = !pairingFlow ? 'pending' : pairingFlow.inviteID ? 'done' : busy ? 'active' : 'pending'
  const pairingStepTwoState = !pairingFlow
    ? 'pending'
    : remotePairing
      ? pairingFlow.childAuthCode
        ? 'done'
        : pairingFlow.inviteID
          ? 'active'
          : 'pending'
      : pairingFlow.enrollment
        ? 'done'
        : pairingFlow.inviteToken
          ? 'active'
          : 'pending'
  const pairingStepThreeState = pairingFlow?.enrollment?.status === 'approved'
    ? 'done'
    : pairingFlow?.enrollment?.status === 'rejected'
      ? 'danger'
      : pairingFlow?.enrollment
        ? 'active'
        : 'pending'

  useEffect(() => {
    if (currentRole === 'master') {
      return
    }
    setPairingFlow(null)
  }, [currentRole])

  useEffect(() => {
    if (!open || currentRole !== 'master' || !pairingFlow?.inviteID) {
      return
    }
    if (pairingFlow.enrollment?.status === 'approved' || pairingFlow.enrollment?.status === 'rejected') {
      return
    }

    let cancelled = false

    const syncPairing = async () => {
      try {
        const pending = await fetchPendingSwarmEnrollments()
        if (cancelled) {
          return
        }
        const nextEnrollment = pending.find((item) => item.invite_id === pairingFlow.inviteID) ?? null
        if (!nextEnrollment) {
          return
        }
        setPairingFlow((current) => {
          if (!current || current.inviteID !== pairingFlow.inviteID) {
            return current
          }
          if (current.enrollment?.id === nextEnrollment.id && current.enrollment?.updated_at === nextEnrollment.updated_at) {
            return current
          }
          return {
            ...current,
            enrollment: nextEnrollment,
          }
        })
      } catch {
        // Polling is best-effort here; leave the current pairing state visible until the next successful refresh.
      }
    }

    void syncPairing()
    const timer = window.setInterval(() => {
      void syncPairing()
    }, 1500)

    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [currentRole, open, pairingFlow?.enrollment?.status, pairingFlow?.inviteID])

  const handleSaveSettings = async () => {
    if (!uiSettings || !onboardingStatus) {
      setError('Swarm settings are not loaded yet.')
      return
    }

    const normalizedName = normalizeSwarmName(editedName)
    if (!localDirty) {
      setEditingLocal(false)
      return
    }

    setBusy(true)
    setError(null)
    setStatusMessage(null)
    try {
      const nextSwarm = await saveSwarmSettings({
        current: uiSettings,
        name: normalizedName,
        defaultNewSessionMode: normalizeDefaultNewSessionMode(uiSettings.chat?.default_new_session_mode),
      })
      const nextOnboarding = await saveDesktopOnboarding({
        swarmName: normalizedName,
        child: editedRole === 'child',
      })
      setUISettings({
        ...uiSettings,
        swarm: {
          ...(uiSettings.swarm ?? {}),
          name: nextSwarm.name,
        },
        updated_at: nextSwarm.updatedAt,
      })
      setOnboardingStatus(nextOnboarding)
      await refresh()
      setEditingLocal(false)
      setStatusMessage(`Saved swarm settings for ${nextSwarm.name}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save swarm settings')
    } finally {
      setBusy(false)
    }
  }

  const handleEditStart = () => {
    syncEditableLocalFields(onboardingStatus, swarmState)
    setEditingLocal(true)
  }

  const handleEditCancel = () => {
    syncEditableLocalFields(onboardingStatus, swarmState)
    setEditingLocal(false)
  }

  const handleEnableSwarmMode = async () => {
    setBusy(true)
    setError(null)
    setStatusMessage(null)
    try {
      await saveDesktopOnboarding({
        swarmName: localSwarmName(onboardingStatus, swarmState),
        swarmMode: true,
        child: false,
        mode: 'lan',
      })
      requestOnboardingFlow()
      setStatusMessage('Swarm Mode is now on. Continue through setup to confirm LAN or Tailscale reachability.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to turn on Swarm Mode')
    } finally {
      setBusy(false)
    }
  }

  const handleDisableSwarmMode = async () => {
    setBusy(true)
    setError(null)
    setStatusMessage(null)
    try {
      await saveDesktopOnboarding({
        swarmName: localSwarmName(onboardingStatus, swarmState),
        swarmMode: false,
        child: false,
      })
      setEditingLocal(false)
      setPairingFlow(null)
      await refresh()
      setStatusMessage('Swarm Mode is now off. This node is back to standalone local use.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to turn off Swarm Mode')
    } finally {
      setBusy(false)
    }
  }

  const handleGeneratePairingInvite = async (candidate: DesktopOnboardingDiscoveredSwarm | null) => {
    setBusy(true)
    setError(null)
    setStatusMessage(null)
    try {
      const invite = await createSwarmInvite()
      setPairingFlow({
        mode: 'manual',
        candidate,
        inviteID: invite.id,
        inviteToken: invite.token,
        enrollment: null,
        copyState: 'idle',
        childAuthCode: '',
        childName: '',
        childSwarmID: '',
      })
      setStatusMessage(`Pairing code ready for ${pairingTargetLabel(candidate)}. Paste it into the child swarm and this panel will wait for the enrollment request.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create pairing invite')
    } finally {
      setBusy(false)
    }
  }

  const handleStartRemotePairing = async (candidate: DesktopOnboardingDiscoveredSwarm) => {
    setBusy(true)
    setError(null)
    setStatusMessage(null)
    try {
      const result = await startRemoteSwarmPairing(candidateRemotePairingRequest(candidate))
      setPairingFlow({
        mode: 'remote',
        candidate,
        inviteID: result.invite.id,
        inviteToken: result.invite.token,
        enrollment: null,
        copyState: 'idle',
        childAuthCode: result.ceremony.auth_code,
        childName: result.ceremony.child_name,
        childSwarmID: result.ceremony.child_swarm_id,
      })
      setStatusMessage(`Requested pairing from ${pairingTargetLabel(candidate)}. The child was notified and returned auth code ${result.ceremony.auth_code}. Waiting for enrollment.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start remote child pairing')
    } finally {
      setBusy(false)
    }
  }

  const handleStartPairing = (candidate: DesktopOnboardingDiscoveredSwarm | null) => {
    setPairingFlow({
      mode: candidate ? 'remote' : 'manual',
      candidate,
      inviteID: '',
      inviteToken: '',
      enrollment: null,
      copyState: 'idle',
      childAuthCode: '',
      childName: '',
      childSwarmID: '',
    })
    if (candidate) {
      void handleStartRemotePairing(candidate)
      return
    }
    void handleGeneratePairingInvite(candidate)
  }

  const handleCopyPairingCode = async () => {
    if (!pairingFlow?.inviteToken) {
      return
    }
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(pairingFlow.inviteToken)
      setPairingFlow((current) => (current ? { ...current, copyState: 'copied' } : current))
      window.setTimeout(() => {
        setPairingFlow((current) => (current?.copyState === 'copied' ? { ...current, copyState: 'idle' } : current))
      }, 1600)
    } catch {
      setPairingFlow((current) => (current ? { ...current, copyState: 'error' } : current))
    }
  }

  const handlePairingDecision = async (approve: boolean) => {
    if (!pairingFlow?.enrollment) {
      return
    }
    setBusy(true)
    setError(null)
    setStatusMessage(null)
    try {
      const result = await decideSwarmEnrollment(pairingFlow.enrollment.id, approve)
      setPairingFlow((current) => (current
        ? {
            ...current,
            enrollment: result.enrollment,
          }
        : current))
      await refresh()
      setStatusMessage(`${approve ? 'Approved' : 'Rejected'} enrollment for ${result.enrollment.child_name}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update pairing enrollment')
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Swarm">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="mx-auto mt-[4vh] flex max-h-[min(90vh,960px)] w-[min(1120px,calc(100vw-24px))] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1120px,calc(100vw-48px))]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
          <div>
            <div className="flex items-center gap-3">
              <h2 className="text-xl font-semibold text-[var(--app-text)]">Swarm</h2>
              {activeTab === 'swarm' ? <Badge tone={groupControlsLocked ? 'warning' : 'neutral'}>{roleLabel(currentRole)}</Badge> : null}
            </div>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">
              {activeTab === 'swarm'
                ? 'Manage the current swarm relationship and inspect other running swarms on the network.'
                : 'Save reusable container shapes, starting from the same workspace directories you already use.'}
            </p>
            <div className="mt-4">
              <SwarmSurfaceTabs activeTab={activeTab} onChange={setActiveTab} />
            </div>
          </div>
          <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close swarm modal" />
        </div>

        {error ? <div className="mx-6 mt-5 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{error}</div> : null}
        {statusMessage ? <div className="mx-6 mt-5 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">{statusMessage}</div> : null}

        <div className="flex-1 overflow-y-auto px-6 py-6">
          {activeTab === 'containers' ? <ContainerProfilesPanel /> : null}
          {activeTab === 'swarm' ? (
          <div className="grid gap-6 xl:grid-cols-[minmax(0,1.2fr)_minmax(320px,0.8fr)]">
            <div className="space-y-6">
              <Card className="p-5">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <h3 className="text-lg font-semibold text-[var(--app-text)]">{group.title}</h3>
                    <p className="mt-1 text-sm text-[var(--app-text-muted)]">{group.description}</p>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button variant="outline" disabled={groupControlsLocked || currentRole !== 'master' || pairingInProgress || busy} onClick={() => handleStartPairing(null)}>
                      {pairingInProgress ? 'Pairing active' : 'Add Child'}
                    </Button>
                  </div>
                </div>

                {localNode ? (
                  <div className="mt-4">
                    <LocalDeviceCard
                      currentRole={currentRole}
                      currentName={currentName}
                      editedName={editedName}
                      editedRole={editedRole}
                      editing={editingLocal}
                      busy={busy}
                      loading={loading}
                      locked={Boolean(localNode.locked)}
                      canEdit={currentRole !== 'standalone'}
                      online={localOnline}
                      metadata={localMetadata}
                      dirty={localDirty}
                      swarmMode={Boolean(onboardingStatus?.config.swarmMode)}
                      onEdit={handleEditStart}
                      onToggleSwarmMode={() => {
                        if (onboardingStatus?.config.swarmMode) {
                          void handleDisableSwarmMode()
                          return
                        }
                        void handleEnableSwarmMode()
                      }}
                      onCancel={handleEditCancel}
                      onSave={() => void handleSaveSettings()}
                      onNameChange={setEditedName}
                      onRoleChange={setEditedRole}
                    />
                  </div>
                ) : null}

                {remoteGroupNodes.length > 0 ? (
                  <div className="mt-4 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                    {remoteGroupNodes.map((node) => <GroupNodeCard key={node.id} node={node} />)}
                  </div>
                ) : null}

                {groupControlsLocked ? (
                  <div className="mt-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                    This device can still update its own saved name and type here, but child-group membership is controlled by the current master swarm.
                  </div>
                ) : null}
              </Card>

              {pairingFlow ? (
                <Card className="overflow-hidden border-[var(--app-border-strong)] p-0">
                  <div className="border-b border-[var(--app-border)] bg-[linear-gradient(135deg,color-mix(in_oklab,var(--app-primary)_14%,var(--app-surface))_0%,var(--app-surface)_58%,color-mix(in_oklab,var(--app-success)_10%,var(--app-surface))_100%)] px-5 py-5">
                    <div className="flex flex-wrap items-start justify-between gap-3">
                      <div>
                        <div className="text-xs uppercase tracking-[0.18em] text-[var(--app-text-muted)]">1-2-3 Pairing Mode</div>
                        <h3 className="mt-2 text-xl font-semibold text-[var(--app-text)]">Bring a child into this swarm</h3>
                        <p className="mt-2 max-w-2xl text-sm text-[var(--app-text-muted)]">
                          {remotePairing
                            ? 'This is the network ceremony: request pairing from the child, verify the child-auth code, then review its fingerprint before trust is activated.'
                            : 'This is the real ceremony: generate a one-time invite, wait for the child to submit enrollment, then review its fingerprint before trust is activated.'}
                        </p>
                      </div>
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge tone={pairingFlow.enrollment?.status === 'approved' ? 'live' : pairingFlow.enrollment?.status === 'rejected' ? 'danger' : pairingFlow.inviteID ? 'warning' : 'neutral'}>
                          {pairingFlow.enrollment?.status || (remotePairing ? (pairingFlow.childAuthCode ? 'child notified' : 'contacting child') : (pairingFlow.inviteToken ? 'waiting for child' : 'preparing code'))}
                        </Badge>
                        <Button variant="outline" onClick={() => setPairingFlow(null)} disabled={busy}>
                          Close Pairing
                        </Button>
                      </div>
                    </div>
                    <div className="mt-4 flex flex-wrap items-center gap-2 text-sm text-[var(--app-text-muted)]">
                      <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 font-medium text-[var(--app-text)]">
                        Target: {pairingTargetLabel(pairingFlow.candidate)}
                      </span>
                      {pairingFlow.candidate ? (
                        <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1">
                          {candidateEndpoint(pairingFlow.candidate)}
                        </span>
                      ) : null}
                    </div>
                  </div>

                  <div className="grid gap-4 p-5">
                    <PairingStep number={1} title={remotePairing ? 'Send the request to the child' : 'Create the pairing code'} state={pairingStepOneState}>
                      {!pairingFlow.inviteID ? (
                        <div className="space-y-3">
                          <div>
                            {remotePairing
                              ? (busy ? 'Sending a pairing request to the child swarm over the network…' : 'Send the pairing request to the child swarm over the network.')
                              : (busy ? 'Generating a one-time invite for this master swarm…' : 'Generate a one-time invite code for the child swarm.')}
                          </div>
                          {!busy && !remotePairing ? (
                            <Button onClick={() => void handleGeneratePairingInvite(pairingFlow.candidate)} disabled={busy}>
                              Generate pairing code
                            </Button>
                          ) : null}
                        </div>
                      ) : (
                        <div className="space-y-3">
                          {remotePairing ? (
                            <div className="rounded-2xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-[var(--app-text)]">
                              Swarm delivered the invite to the child over the network. No child-side paste step is required.
                            </div>
                          ) : (
                            <>
                              <div>Share this code over a secure channel. The transport gets the two swarms talking; approval still happens here.</div>
                              <code className="block overflow-x-auto rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 font-mono text-xs text-[var(--app-text)]">
                                {pairingFlow.inviteToken}
                              </code>
                              <div className="flex flex-wrap items-center gap-2">
                                <Button variant="outline" onClick={() => void handleCopyPairingCode()} disabled={busy}>
                                  {pairingFlow.copyState === 'copied' ? 'Copied' : 'Copy code'}
                                </Button>
                                {pairingFlow.copyState === 'error' ? (
                                  <span className="text-xs text-[var(--app-danger)]">Clipboard unavailable. Copy the code manually.</span>
                                ) : (
                                  <span className="text-xs text-[var(--app-text-muted)]">Wait to rotate the code until this attempt is finished.</span>
                                )}
                              </div>
                            </>
                          )}
                        </div>
                      )}
                    </PairingStep>

                    <PairingStep number={2} title={remotePairing ? 'Verify the child auth code' : 'Place the code on the child swarm'} state={pairingStepTwoState}>
                      <div className="space-y-3">
                        {remotePairing ? (
                          <>
                            <div>The child received a swarm notification to start the ceremony. Verify this auth code on the child before approving trust.</div>
                            {pairingFlow.childAuthCode ? (
                              <code className="block overflow-x-auto rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 font-mono text-xs text-[var(--app-text)]">
                                {pairingFlow.childAuthCode}
                              </code>
                            ) : (
                              <div className="text-xs uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Waiting for child auth code…</div>
                            )}
                            {pairingFlow.childName || pairingFlow.childSwarmID ? (
                              <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
                                {pairingFlow.childName ? <div><strong className="text-[var(--app-text)]">{pairingFlow.childName}</strong></div> : null}
                                {pairingFlow.childSwarmID ? <div className="mt-1 break-all text-xs">{pairingFlow.childSwarmID}</div> : null}
                              </div>
                            ) : null}
                          </>
                        ) : (
                          <div>On the child device: switch it to `Child`, open its pairing screen, paste the invite code, and submit the enrollment request.</div>
                        )}
                        <div className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3">
                          This panel keeps watching for the incoming request automatically. You do not need to refresh or reopen the modal.
                        </div>
                        {pairingFlow.enrollment ? (
                          <div className="rounded-2xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-[var(--app-text)]">
                            Enrollment received from <strong>{pairingFlow.enrollment.child_name}</strong>.
                          </div>
                        ) : pairingFlow.inviteToken ? (
                          <div className="text-xs uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Listening for child enrollment…</div>
                        ) : null}
                      </div>
                    </PairingStep>

                    <PairingStep number={3} title="Review the child and finish auth" state={pairingStepThreeState}>
                      {pairingFlow.enrollment ? (
                        <div className="space-y-4">
                          <div className="grid gap-3 md:grid-cols-2">
                            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
                              <div className="text-xs uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Child</div>
                              <div className="mt-1 text-sm font-semibold text-[var(--app-text)]">{pairingFlow.enrollment.child_name}</div>
                              <div className="mt-1 break-all text-xs text-[var(--app-text-muted)]">{pairingFlow.enrollment.child_swarm_id}</div>
                            </div>
                            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
                              <div className="text-xs uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Transport</div>
                              <div className="mt-1 text-sm font-semibold text-[var(--app-text)]">{pairingFlow.enrollment.transport_mode || 'unknown'}</div>
                              <div className="mt-1 text-xs text-[var(--app-text-muted)]">{pairingFlow.enrollment.observed_remote_addr || 'Remote address unavailable'}</div>
                            </div>
                            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 md:col-span-2">
                              <div className="text-xs uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Fingerprint</div>
                              <div className="mt-1 break-all font-mono text-xs text-[var(--app-text)]">{pairingFlow.enrollment.child_fingerprint}</div>
                            </div>
                          </div>

                          {pairingFlow.enrollment.status === 'approved' ? (
                            <div className="rounded-2xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">
                              Trust is active. This child now belongs to the current swarm group.
                            </div>
                          ) : pairingFlow.enrollment.status === 'rejected' ? (
                            <div className="rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
                              This enrollment was rejected. Start a new pairing attempt if you still want to attach the child.
                            </div>
                          ) : (
                            <div className="flex flex-wrap gap-2">
                              <Button onClick={() => void handlePairingDecision(true)} disabled={busy}>
                                Approve child
                              </Button>
                              <Button variant="outline" onClick={() => void handlePairingDecision(false)} disabled={busy}>
                                Reject
                              </Button>
                            </div>
                          )}
                        </div>
                      ) : (
                        <div>Approval stays locked until the child actually submits its enrollment request.</div>
                      )}
                    </PairingStep>
                  </div>
                </Card>
              ) : null}
            </div>

            <div className="space-y-6">
              <Card className="p-5">
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <h3 className="text-lg font-semibold text-[var(--app-text)]">Other Swarms on Network</h3>
                    <p className="mt-1 text-sm text-[var(--app-text-muted)]">Running swarms discovered across available network transports, separate from the current group.</p>
                  </div>
                  <Badge tone={discoveredCandidates.length > 0 ? 'live' : 'neutral'}>{discoveredCandidates.length}</Badge>
                </div>

                {loading ? (
                  <div className="mt-4 rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                    Scanning available swarm transports…
                  </div>
                ) : discoveredCandidates.length === 0 ? (
                  <div className="mt-4 rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                    No other running swarms are visible right now. You can still add a child manually.
                  </div>
                ) : null}

                <div className="mt-4 grid gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
                  <div className="rounded-2xl border border-dashed border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-4">
                    <div className="flex items-start justify-between gap-3">
                      <div>
                        <div className="text-sm font-semibold text-[var(--app-text)]">Add manually</div>
                        <div className="mt-1 text-xs text-[var(--app-text-muted)]">Create a pairing code even when no discovered swarm is listed yet.</div>
                      </div>
                      <Badge tone="neutral">Invite</Badge>
                    </div>
                    <div className="mt-3 flex flex-wrap gap-2">
                      <Button
                        variant="outline"
                        disabled={currentRole !== 'master' || groupControlsLocked || pairingInProgress || busy}
                        onClick={() => handleStartPairing(null)}
                      >
                        {pairingFlow?.candidate == null && pairingInProgress ? 'Pairing…' : 'Add Child'}
                      </Button>
                    </div>
                  </div>

                  {discoveredCandidates.map((candidate) => (
                    <div key={candidate.id || candidate.endpoint || candidate.dnsName} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                      <div className="flex items-start justify-between gap-3">
                        <div>
                          <div className="text-sm font-semibold text-[var(--app-text)]">{candidate.name || candidate.dnsName}</div>
                          <div className="mt-1 break-all text-xs text-[var(--app-text-muted)]">{candidateEndpoint(candidate)}</div>
                        </div>
                        <div className="flex flex-wrap justify-end gap-2">
                          <Badge tone={candidate.online ? 'live' : 'neutral'}>{candidate.online ? 'online' : 'seen'}</Badge>
                          <Badge tone="neutral">{candidate.source || candidate.transportMode || 'network'}</Badge>
                        </div>
                      </div>
                      <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-muted)]">
                        <span>{roleLabel(candidate.role || 'standalone')}</span>
                        {candidate.transportMode ? <span>· {candidate.transportMode}</span> : null}
                      </div>
                      <div className="mt-3 flex flex-wrap gap-2">
                        <Button
                          variant="outline"
                          disabled={currentRole !== 'master' || groupControlsLocked || pairingInProgress || busy}
                          onClick={() => handleStartPairing(candidate)}
                        >
                          {pairingFlow?.candidate?.id === candidate.id && pairingInProgress ? 'Pairing…' : 'Add as Child'}
                        </Button>
                      </div>
                    </div>
                  ))}
                </div>
              </Card>
            </div>
          </div>
          ) : null}
        </div>
      </DialogPanel>
    </Dialog>
  )
}
