import { useEffect, useMemo, useState } from 'react'
import { Check, HelpCircle, Loader2, Plus, RefreshCw, X } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import {
  fetchDesktopOnboardingStatus,
  fetchRemoteSwarmCandidates,
  fetchSwarmLocalRuntimeStatus,
  startRemoteSwarmPairing,
  type RemoteSwarmCandidate,
  type RemoteSwarmPairingStartResult,
  type SwarmLocalRuntimeStatus,
} from '../../onboarding/api'

import type { DesktopOnboardingStatus } from '../../onboarding/types'
import {
  fetchDeployContainerPackageDefaults,
  suggestDeployContainerPackages,
  validateDeployContainerPackage,
  type DeployContainerPackageSelection,
} from '../api/deploy-container'
import { replicateSwarm, ReplicateSwarmLaunchError } from '../api/replicate-swarm'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { useDesktopStore } from '../../state/use-desktop-store'

const ADD_SWARM_LOG_PREFIX = '[add-swarm]'

function logAddSwarm(step: string, details?: Record<string, unknown>) {
  const message = `${ADD_SWARM_LOG_PREFIX} ${step}`
  if (details && Object.keys(details).length > 0) {
    console.info(message, details)
    return
  }
  console.info(message)
}

function logAddSwarmError(step: string, error: unknown, details?: Record<string, unknown>) {
  console.error(`${ADD_SWARM_LOG_PREFIX} ${step}`, {
    ...details,
    error: error instanceof Error ? error.message : String(error),
  })
}

interface AddSwarmModalProps {
  open: boolean
  onboardingStatus: DesktopOnboardingStatus | null
  initialTarget?: LaunchTarget
  onOpenChange: (open: boolean) => void
  onComplete: (message: string) => Promise<void> | void
}

type LaunchTarget = 'local' | 'remote'
interface ReplicateWorkspaceDraft {
  workspacePath: string
  selected: boolean
  workspaceName: string
  directories: string[]
}

interface ContainerPackageDraft {
  name: string
  source: 'recommended' | 'user_added' | 'workspace_scan'
  reason?: string
}

const FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE = 'docker.io/ubuntu:26.04'
const FALLBACK_CONTAINER_PACKAGE_MANAGER = 'apt'

const DEFAULT_CONTAINER_PACKAGES: ContainerPackageDraft[] = [
  'bash',
  'ca-certificates',
  'curl',
  'file',
  'git',
  'jq',
  'less',
  'openssh-client',
  'procps',
  'psmisc',
  'python3',
  'ripgrep',
].map((name) => ({ name, source: 'recommended' as const }))

function buildContainerPackageManifest(packages: ContainerPackageDraft[], baseImage: string, packageManager: string) {
  return {
    base_image: baseImage,
    package_manager: packageManager,
    packages: packages.map((pkg) => ({
      name: pkg.name,
      source: pkg.source,
      reason: pkg.reason,
    })),
  }
}

function mergeContainerPackages(packages: ContainerPackageDraft[]): ContainerPackageDraft[] {
  const ordered = new Map<string, ContainerPackageDraft>()
  for (const pkg of packages) {
    const name = String(pkg.name ?? '').trim().toLowerCase()
    if (!name) {
      continue
    }
    const current = ordered.get(name)
    if (!current) {
      ordered.set(name, {
        name,
        source: pkg.source,
        reason: pkg.reason?.trim() || undefined,
      })
      continue
    }
    const nextSource: ContainerPackageDraft['source'] = current.source === 'user_added' || pkg.source === 'user_added'
      ? 'user_added'
      : current.source === 'workspace_scan' || pkg.source === 'workspace_scan'
        ? 'workspace_scan'
        : 'recommended'
    const reasons = [current.reason, pkg.reason]
      .map((value) => String(value ?? '').trim())
      .filter((value, index, array) => value.length > 0 && array.indexOf(value) === index)
    ordered.set(name, {
      name,
      source: nextSource,
      reason: reasons.length > 0 ? reasons.join('; ') : undefined,
    })
  }
  return Array.from(ordered.values())
}

function mapSuggestedPackages(packages: DeployContainerPackageSelection[]): ContainerPackageDraft[] {
  return (packages ?? []).map((pkg): ContainerPackageDraft => ({
    name: String(pkg.name ?? '').trim().toLowerCase(),
    source: pkg.source === 'workspace_scan' ? 'workspace_scan' : 'recommended',
    reason: String(pkg.reason ?? '').trim() || undefined,
  })).filter((pkg) => pkg.name.length > 0)
}

function describePackageSource(pkg: ContainerPackageDraft): string {
  if (pkg.source === 'user_added') {
    return 'Added manually'
  }
  if (pkg.source === 'workspace_scan') {
    return pkg.reason?.trim() ? `Suggested from workspace scan: ${pkg.reason}` : 'Suggested from workspace scan'
  }
  return 'Base recommendation'
}

const FALLBACK_RUNTIME_STATUS: SwarmLocalRuntimeStatus = {
  recommended: '',
  available: [],
  installed: [],
  issues: {},
  warning: 'Could not detect local container runtime.',
}

function currentGroup(status: DesktopOnboardingStatus | null) {
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

function buildWorkspaceDrafts(workspaces: WorkspaceEntry[]): ReplicateWorkspaceDraft[] {
  return workspaces.map((workspace) => ({
    workspacePath: workspace.path,
    selected: false,
    workspaceName: workspace.workspaceName || workspace.path.split('/').filter(Boolean).pop() || 'workspace',
    directories: workspace.directories,
  }))
}

function selectedWorkspaceCount(items: ReplicateWorkspaceDraft[]): number {
  return items.filter((item) => item.selected).length
}

function normalizePairingCode(value: string): string {
  return String(value ?? '').trim().replace(/[^a-fA-F0-9]/g, '').toUpperCase().slice(0, 6)
}

function formatPairingCode(value: string): string {
  const normalized = normalizePairingCode(value)
  return normalized ? normalized.match(/.{1,3}/g)?.join(' ') ?? normalized : ''
}

function endpointHostLabel(endpoint: string): string {
  const trimmed = String(endpoint ?? '').trim()
  if (!trimmed) {
    return ''
  }
  try {
    const parsed = new URL(trimmed)
    return parsed.host || trimmed
  } catch {
    return trimmed
  }
}

function selectCandidateEndpoint(candidate: RemoteSwarmCandidate | null): string {
  if (!candidate) {
    return ''
  }
  const apiEndpoint = candidate.endpointCandidates.find((item) => String(item.kind ?? '').includes('api') && item.url.trim() !== '')
  return apiEndpoint?.url.trim() || candidate.endpoint.trim() || candidate.endpointCandidates.find((item) => item.url.trim() !== '')?.url.trim() || ''
}

export function AddSwarmModal({ open, onboardingStatus, initialTarget = 'local', onOpenChange, onComplete }: AddSwarmModalProps) {
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [currentOnboardingStatus, setCurrentOnboardingStatus] = useState<DesktopOnboardingStatus | null>(onboardingStatus)
  const [workspaceDrafts, setWorkspaceDrafts] = useState<ReplicateWorkspaceDraft[]>([])
  const [runtimeStatus, setRuntimeStatus] = useState<SwarmLocalRuntimeStatus>(FALLBACK_RUNTIME_STATUS)
  const [remoteCandidates, setRemoteCandidates] = useState<RemoteSwarmCandidate[]>([])
  const [remoteCandidatesLoading, setRemoteCandidatesLoading] = useState(false)
  const [remoteCandidatesError, setRemoteCandidatesError] = useState<string | null>(null)
  const [selectedRemoteCandidateID, setSelectedRemoteCandidateID] = useState('')
  const [remoteCeremonyCode, setRemoteCeremonyCode] = useState('')
  const [remotePairingResult, setRemotePairingResult] = useState<RemoteSwarmPairingStartResult | null>(null)
  const [launchTarget, setLaunchTarget] = useState<LaunchTarget>(initialTarget)
  const [selectedRuntime, setSelectedRuntime] = useState<'podman' | 'docker' | ''>('')
  const [swarmName, setSwarmName] = useState('')
  const [syncEnabled, setSyncEnabled] = useState(true)
  const [syncVaultPassword, setSyncVaultPassword] = useState('')
  const [bypassPermissions, setBypassPermissions] = useState(false)
  const [alwaysOn, setAlwaysOn] = useState(true)
  const [containerPackageBaseImage, setContainerPackageBaseImage] = useState(FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE)
  const [containerPackageManager, setContainerPackageManager] = useState(FALLBACK_CONTAINER_PACKAGE_MANAGER)
  const [containerPackages, setContainerPackages] = useState<ContainerPackageDraft[]>(DEFAULT_CONTAINER_PACKAGES)
  const [packageInput, setPackageInput] = useState('')
  const [packageValidationError, setPackageValidationError] = useState<string | null>(null)
  const [validatingPackage, setValidatingPackage] = useState(false)
  const [suggestingPackages, setSuggestingPackages] = useState(false)
  const [packageSuggestionError, setPackageSuggestionError] = useState<string | null>(null)
  const [packagePlatformInfoOpen, setPackagePlatformInfoOpen] = useState(false)

  const vault = useDesktopStore((state) => state.vault)
  const runtimeChoice = useMemo(
    () => (selectedRuntime && runtimeStatus.available.includes(selectedRuntime) ? selectedRuntime : runtimeStatus.recommended || ''),
    [runtimeStatus, selectedRuntime],
  ) as 'podman' | 'docker' | ''
  const activeRuntimeLabel = runtimeChoice
  const devMode = Boolean(currentOnboardingStatus?.config.devMode)
  const selectedRemoteCandidate = useMemo(
    () => remoteCandidates.find((candidate) => candidate.id === selectedRemoteCandidateID) ?? null,
    [remoteCandidates, selectedRemoteCandidateID],
  )
  const selectedRemoteCandidateEndpoint = useMemo(() => selectCandidateEndpoint(selectedRemoteCandidate), [selectedRemoteCandidate])
  const activeRemoteCeremonyCode = normalizePairingCode(remotePairingResult?.ceremony.code ?? remoteCeremonyCode)

  const group = useMemo(() => currentGroup(currentOnboardingStatus), [currentOnboardingStatus])
  const hostSwarmID = group?.group.hostSwarmID || ''
  const hostVaultEnabled = Boolean(vault.enabled)
  const managerName = useMemo(
    () => group?.members.find((member) => member.swarmID === hostSwarmID)?.name || 'Current manager',
    [group, hostSwarmID],
  )

  const selectedWorkspaceCountValue = useMemo(() => selectedWorkspaceCount(workspaceDrafts), [workspaceDrafts])
  const selectedWorkspacePaths = useMemo(
    () => workspaceDrafts.filter((item) => item.selected).map((item) => item.workspacePath),
    [workspaceDrafts],
  )
  const invalidateLaunchDraft = () => undefined

  const clearRemotePairingState = () => {
    setRemotePairingResult(null)
  }

  const loadRemoteCandidates = async () => {
    setRemoteCandidatesLoading(true)
    setRemoteCandidatesError(null)
    try {
      const result = await fetchRemoteSwarmCandidates()
      setRemoteCandidates(result.candidates)
      setSelectedRemoteCandidateID((current) => (
        current && result.candidates.some((candidate) => candidate.id === current)
          ? current
          : result.candidates[0]?.id ?? ''
      ))
      if (!result.tailscale.connected) {
        setRemoteCandidatesError(result.tailscale.error || 'Tailscale is not connected on this host.')
      }
    } catch (err) {
      setRemoteCandidates([])
      setSelectedRemoteCandidateID('')
      setRemoteCandidatesError(err instanceof Error ? err.message : 'Failed to load Tailscale devices')
    } finally {
      setRemoteCandidatesLoading(false)
    }
  }

  const updateWorkspaceDraft = (
    workspacePath: string,
    transform: (draft: ReplicateWorkspaceDraft) => ReplicateWorkspaceDraft,
  ) => {
    invalidateLaunchDraft()
    setWorkspaceDrafts((current) => current.map((item) => (
      item.workspacePath === workspacePath
        ? transform(item)
        : item
    )))
  }

  const addPackage = async () => {
    const normalized = packageInput.trim().toLowerCase()
    if (!normalized) {
      setPackageValidationError('Package name is required.')
      return
    }
    if (containerPackages.some((pkg) => pkg.name === normalized)) {
      setPackageValidationError(`Package ${normalized} is already in the install list.`)
      return
    }
    setValidatingPackage(true)
    setPackageValidationError(null)
    try {
      const result = await validateDeployContainerPackage(normalized)
      if (!result.valid) {
        throw new Error(`apt package ${normalized} was not found on this host`)
      }
      invalidateLaunchDraft()
      setContainerPackages((current) => mergeContainerPackages([...current, { name: normalized, source: 'user_added' }]))
      setPackageInput('')
    } catch (err) {
      setPackageValidationError(err instanceof Error ? err.message : 'Failed to validate package')
    } finally {
      setValidatingPackage(false)
    }
  }

  const removePackage = (name: string) => {
    invalidateLaunchDraft()
    setContainerPackages((current) => current.filter((pkg) => pkg.name !== name))
    setPackageValidationError(null)
  }

  useEffect(() => {
    let cancelled = false
    if (selectedWorkspacePaths.length === 0) {
      invalidateLaunchDraft()
      setContainerPackages((current) => mergeContainerPackages(current.filter((pkg) => pkg.source !== 'workspace_scan')))
      setPackageSuggestionError(null)
      setSuggestingPackages(false)
      return () => {
        cancelled = true
      }
    }
    setSuggestingPackages(true)
    setPackageSuggestionError(null)
    void suggestDeployContainerPackages(selectedWorkspacePaths)
      .then((packages) => {
        if (cancelled) {
          return
        }
        const suggestions = mapSuggestedPackages(packages)
        invalidateLaunchDraft()
        setContainerPackages((current) => mergeContainerPackages([
          ...current.filter((pkg) => pkg.source !== 'workspace_scan'),
          ...suggestions,
        ]))
      })
      .catch((err) => {
        if (cancelled) {
          return
        }
        setPackageSuggestionError(err instanceof Error ? err.message : 'Failed to suggest packages from workspace contents')
        invalidateLaunchDraft()
        setContainerPackages((current) => mergeContainerPackages(current.filter((pkg) => pkg.source !== 'workspace_scan')))
      })
      .finally(() => {
        if (!cancelled) {
          setSuggestingPackages(false)
        }
      })
    return () => {
      cancelled = true
    }
  }, [selectedWorkspacePaths.join('|')])

  const finishSuccess = async (message: string) => {
    logAddSwarm('add swarm completed successfully', { message })
    await onComplete(message)
    onOpenChange(false)
  }

  useEffect(() => {
    if (!open) {
      return
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    setStatus(null)
    setLaunchTarget(initialTarget)
    setSelectedRuntime('')
    setSyncEnabled(true)
    setSyncVaultPassword('')
    setBypassPermissions(false)
    setAlwaysOn(true)
    setContainerPackageBaseImage(FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE)
    setContainerPackageManager(FALLBACK_CONTAINER_PACKAGE_MANAGER)
    setContainerPackages(DEFAULT_CONTAINER_PACKAGES)
    setPackageInput('')
    setPackageValidationError(null)
    setValidatingPackage(false)
    setSuggestingPackages(false)
    setPackageSuggestionError(null)
    logAddSwarm('modal opened; loading options', {
      current_group_id: onboardingStatus?.currentGroupID || '',
      current_swarm_id: onboardingStatus?.config.swarmID || '',
    })

    void Promise.all([
      listWorkspaces().catch(() => []),
      fetchSwarmLocalRuntimeStatus().catch(() => FALLBACK_RUNTIME_STATUS),
      fetchDeployContainerPackageDefaults().catch(() => ({
        baseImage: FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE,
        packageManager: FALLBACK_CONTAINER_PACKAGE_MANAGER,
      })),
      onboardingStatus ? Promise.resolve(onboardingStatus) : fetchDesktopOnboardingStatus().catch(() => null),
    ])
      .then(([nextWorkspaces, nextRuntimeStatus, nextPackageDefaults, nextOnboardingStatus]) => {
        if (cancelled) {
          return
        }
        setWorkspaceDrafts(buildWorkspaceDrafts(nextWorkspaces))
        setRuntimeStatus(nextRuntimeStatus)
        setContainerPackageBaseImage(nextPackageDefaults.baseImage || FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE)
        setContainerPackageManager(nextPackageDefaults.packageManager || FALLBACK_CONTAINER_PACKAGE_MANAGER)
        setCurrentOnboardingStatus(nextOnboardingStatus)
        setRemoteCandidates([])
        setRemoteCandidatesError(null)
        setRemoteCandidatesLoading(false)
        setSelectedRemoteCandidateID('')
        setRemoteCeremonyCode('')
        setRemotePairingResult(null)
        setSwarmName('')
        setSelectedRuntime((nextRuntimeStatus.recommended || '') as 'podman' | 'docker' | '')
        void loadRemoteCandidates()
        logAddSwarm('modal options loaded', {
          workspaces: nextWorkspaces.length,
          recommended_runtime: nextRuntimeStatus.recommended,
          available_runtimes: nextRuntimeStatus.available,
          group_id: currentGroup(nextOnboardingStatus)?.group.id || '',
        })
      })
      .catch((err) => {
        if (!cancelled) {
          logAddSwarmError('failed to load modal options', err)
          setError(err instanceof Error ? err.message : 'Failed to load swarm link options')
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [initialTarget, onboardingStatus, open])

  const closeModal = () => {
    if (submitting) {
      return
    }
    onOpenChange(false)
  }

  const handleLaunchLocal = async () => {
    if (!group?.group.id.trim()) {
      setError('Create or select a swarm group on the manager before adding a local swarm.')
      return
    }
    if (!runtimeChoice) {
      setError(runtimeStatus.warning || 'No supported local runtime is available.')
      return
    }
    if (!swarmName.trim()) {
      setError('Swarm name is required.')
      return
    }
    const selected = workspaceDrafts.filter((item) => item.selected)
    if (selected.length === 0) {
      setError('Select at least one workspace to add.')
      return
    }
    if (syncEnabled && hostVaultEnabled && !syncVaultPassword.trim()) {
      setError('Vault password is required to sync from a vaulted host.')
      return
    }

    setSubmitting(true)
    setError(null)
    setStatus('Replicating local swarm…')
    logAddSwarm('launch requested for replicate flow', {
      runtime: runtimeChoice,
      swarm_name: swarmName.trim(),
      group_id: group.group.id,
      workspace_count: selected.length,
      sync_enabled: syncEnabled,
      bypass_permissions: bypassPermissions,
    })

    try {
      const syncModules = ['credentials', 'agents', 'custom_tools', 'skills']
      const result = await replicateSwarm({
        mode: 'local',
        swarmName: swarmName.trim(),
        runtime: runtimeChoice,
        bypassPermissions,
        alwaysOn,
        sync: {
          enabled: syncEnabled,
          mode: 'managed',
          modules: syncEnabled ? syncModules : [],
          vaultPassword: syncEnabled && hostVaultEnabled ? syncVaultPassword.trim() : '',
        },
        workspaces: selected.map((item) => ({
          sourceWorkspacePath: item.workspacePath,
          replicationMode: 'bundle',
          writable: true,
        })),
        containerPackages: devMode ? buildContainerPackageManifest(containerPackages, containerPackageBaseImage, containerPackageManager) : undefined,
      })
      await finishSuccess(`Added ${result.swarm.name || swarmName.trim()} to the swarm.`)
    } catch (err) {
      logAddSwarmError('replicate local flow failed', err, {
        runtime: runtimeChoice,
        swarm_name: swarmName.trim(),
        group_id: group.group.id,
      })
      if (err instanceof ReplicateSwarmLaunchError) {
        const details = err.details
        const guidance: string[] = []
        if (details.failure.attachStatus) {
          guidance.push(`Attach status: ${details.failure.attachStatus}`)
        }
        if (details.failure.lastAttachError) {
          guidance.push(`Last attach error: ${details.failure.lastAttachError}`)
        }
        if (details.failure.runtime) {
          guidance.push(`Runtime: ${details.failure.runtime}`)
        }
        if (details.failure.containerName) {
          guidance.push(`Container: ${details.failure.containerName}`)
        }
        if (details.failure.backendHostPort > 0) {
          guidance.push(`Backend port: ${details.failure.backendHostPort}`)
        }
        if (details.failure.desktopHostPort > 0) {
          guidance.push(`Desktop port: ${details.failure.desktopHostPort}`)
        }
        if (details.failure.childBackendURL) {
          guidance.push(`Managed backend URL: ${details.failure.childBackendURL}`)
        }
        if (details.failure.childDesktopURL) {
          guidance.push(`Managed desktop URL: ${details.failure.childDesktopURL}`)
        }
        guidance.push('Check the Swarm dashboard deployment details and the host swarmd log for this deployment.')
        setError([details.error || 'Failed to add swarm', ...guidance].filter(Boolean).join('\n'))
      } else {
        setError(err instanceof Error ? err.message : 'Failed to add swarm')
      }
    } finally {
      setSubmitting(false)
    }
  }

  const handleLaunchRemote = async () => {
    if (!group?.group.id.trim()) {
      setError('Create or select a swarm group before adding a managed swarm.')
      return
    }
    if (!selectedRemoteCandidate) {
      setError('Select an online Tailscale device running swarmd.')
      return
    }
    if (!selectedRemoteCandidateEndpoint) {
      setError('Selected Tailscale device is missing a reachable swarmd endpoint.')
      return
    }

    const pairingEndpoint = selectedRemoteCandidateEndpoint
    const pairingName = selectedRemoteCandidate.name || selectedRemoteCandidate.dnsName || endpointHostLabel(pairingEndpoint) || 'managed swarm'

    setSubmitting(true)
    setError(null)
    setRemoteCeremonyCode('')
    setRemotePairingResult(null)
    setStatus('Requesting managed swarm pairing…')
    try {
      const result = await startRemoteSwarmPairing({
        endpoint: pairingEndpoint,
        dnsName: selectedRemoteCandidate.dnsName,
        ips: selectedRemoteCandidate.ips,
        groupID: group.group.id,
        managedName: pairingName,
        rendezvousTransports: selectedRemoteCandidate.rendezvousTransports,
      })
      const ceremonyCode = normalizePairingCode(result.ceremony.code || result.request.ceremony_code)
      setRemoteCeremonyCode(ceremonyCode)
      setRemotePairingResult(result)
      const displayName = result.ceremony.managed_name || result.request.managed_name || pairingName
      setStatus(`Pairing request sent to ${displayName}. On the managed swarm, approve the pending request after confirming code ${formatPairingCode(ceremonyCode)}.`)
      await onComplete(`Pairing request sent to ${displayName}. Approve it on the managed swarm to finish adding the Managed Swarm.`)
    } catch (err) {
      logAddSwarmError('managed swarm pairing failed', err, {
        endpoint: pairingEndpoint,
        group_id: group.group.id,
      })
      setError(err instanceof Error ? err.message : 'Failed to start managed swarm pairing')
    } finally {
      setSubmitting(false)
    }
  }

  const handleSubmit = async () => {
    logAddSwarm('submit clicked', {
      launch_target: launchTarget,
      runtime_choice: runtimeChoice,
      swarm_name: swarmName.trim(),
      workspace_count: selectedWorkspaceCountValue,
    })
    if (launchTarget === 'remote') {
      await handleLaunchRemote()
      return
    }
    await handleLaunchLocal()
  }

  if (!open) {
    return null
  }

  const panelClassName = 'mx-auto mt-[5vh] flex w-[min(840px,calc(100vw-24px))] max-w-[840px] flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(840px,calc(100vw-48px))]'
  const headerClassName = 'border-b border-[var(--app-border)] px-5 py-4'
  const bodyClassName = 'flex max-h-[min(78vh,820px)] flex-col gap-3 overflow-y-auto px-5 py-4'
  const sectionClassName = 'grid gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-none'
  const subtlePanelClassName = 'rounded-lg border border-[var(--app-border)] bg-transparent p-3'
  const optionClassName = (active: boolean, muted = false) => (
    `rounded-lg border px-3 py-2 text-left transition ${active ? 'border-[var(--app-primary)] bg-transparent text-[var(--app-text)]' : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)]'} ${muted && !active ? 'opacity-80' : ''}`
  )
  const launchPendingReason = (() => {
    if (loading) {
      return 'Loading launch options…'
    }
    if (!group?.group.id.trim()) {
      return 'Waiting for a swarm group.'
    }
    if (!swarmName.trim() && launchTarget === 'local') {
      return 'Please enter a swarm name.'
    }
    if (launchTarget === 'local') {
      if (!runtimeChoice) {
        return runtimeStatus.warning || 'Please choose an available runtime.'
      }
      if (selectedWorkspaceCountValue === 0) {
        return 'Please select a workspace.'
      }
      if (syncEnabled && hostVaultEnabled && !syncVaultPassword.trim()) {
        return 'Please enter the vault password to enable Swarm Sync.'
      }
      return null
    }
    if (!selectedRemoteCandidate) {
      return remoteCandidatesLoading ? 'Loading Tailscale candidates…' : 'Select an online Tailscale device running swarmd.'
    }
    if (!selectedRemoteCandidateEndpoint) {
      return 'Selected Tailscale device is missing a reachable swarmd endpoint.'
    }
    return null
  })()
  const footerStatusText = launchPendingReason || (launchTarget === 'remote'
    ? `Managed Swarm pairing will request approval from ${selectedRemoteCandidate?.name || selectedRemoteCandidate?.dnsName || 'the remote host'} using container scope managed_host_local.`
    : `${selectedWorkspaceCountValue} selected workspace${selectedWorkspaceCountValue === 1 ? '' : 's'} will be replicated using ${runtimeChoice || 'the selected runtime'} with Swarm Sync ${syncEnabled ? 'enabled' : 'disabled'}.`)
  const swarmNameCard = (
    <Card className={sectionClassName}>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(220px,320px)] sm:items-center">
        <div className="grid gap-1">
          <div className="text-sm font-semibold text-[var(--app-text)]">Name this swarm</div>
          <div className="text-xs text-[var(--app-text-muted)]">Choose the display name used to identify this managed local swarm in the group after launch.</div>
        </div>
        <div className="grid gap-2">
          <Input
            data-testid="add-swarm-local-name"
            value={swarmName}
            onChange={(event) => {
              invalidateLaunchDraft()
              setSwarmName(event.target.value)
            }}
            disabled={submitting}
            placeholder="Enter a managed swarm name"
          />
        </div>
      </div>
    </Card>
  )

  const packagePlatformPanel = (
    <div className={subtlePanelClassName}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <div className="text-sm font-semibold text-[var(--app-text)]">Package platform</div>
          <button
            type="button"
            className="inline-flex h-6 w-6 items-center justify-center rounded-full border border-[var(--app-border)] text-[var(--app-text-muted)] transition hover:border-[var(--app-primary)] hover:text-[var(--app-text)]"
            aria-label="Explain package platform"
            aria-expanded={packagePlatformInfoOpen}
            onClick={() => setPackagePlatformInfoOpen((current) => !current)}
          >
            <HelpCircle size={14} />
          </button>
        </div>
        <Badge tone={containerPackages.length > 0 ? 'live' : 'neutral'}>{containerPackages.length} apt packages</Badge>
      </div>
      {packagePlatformInfoOpen ? (
        <div className="mt-3 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs leading-5 text-[var(--app-text-muted)]">
          Package platform controls the container OS image and apt packages installed before your selected workspaces are copied in. Swarm starts with backend-recommended defaults, scans selected workspaces for likely package needs, and lets you add or remove packages when the workspace requires them.
        </div>
      ) : null}
      <div className="mt-2 text-xs text-[var(--app-text-muted)]">
        Base image <span className="font-medium text-[var(--app-text)]">{containerPackageBaseImage}</span> · manager <span className="font-medium text-[var(--app-text)]">{containerPackageManager}</span>
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        {containerPackages.slice(0, 18).map((pkg) => (
          <Badge key={pkg.name} tone={pkg.source === 'user_added' ? 'live' : pkg.source === 'workspace_scan' ? 'warning' : 'neutral'} className="gap-2 pr-1" title={describePackageSource(pkg)}>
            <span>{pkg.name}</span>
            <button
              type="button"
              onClick={() => removePackage(pkg.name)}
              className="inline-flex h-5 w-5 items-center justify-center rounded-md text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface)] hover:text-[var(--app-text)]"
              disabled={submitting || validatingPackage || suggestingPackages || false}
              aria-label={`Remove package ${pkg.name}`}
            >
              <X size={12} />
            </button>
          </Badge>
        ))}
        {containerPackages.length > 18 ? <Badge tone="neutral">+{containerPackages.length - 18} more</Badge> : null}
      </div>
      <div className="mt-3 flex gap-2">
        <Input
          value={packageInput}
          onChange={(event) => {
            setPackageInput(event.target.value)
            if (packageValidationError) {
              setPackageValidationError(null)
            }
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              void addPackage()
            }
          }}
          placeholder={suggestingPackages ? 'Scanning selected workspaces…' : 'Add apt package'}
          disabled={submitting || validatingPackage || false}
        />
        <Button type="button" variant="secondary" onClick={() => void addPackage()} disabled={submitting || validatingPackage || false}>
          {validatingPackage ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
          Add
        </Button>
      </div>
      {packageValidationError || packageSuggestionError ? (
        <div className="mt-2 text-xs text-[var(--app-danger)]">{packageValidationError || packageSuggestionError}</div>
      ) : null}
    </div>
  )

  const workspaceSelectionCard = (
    <Card className={sectionClassName}>
      <div className="flex items-start justify-between gap-3">
        <div className="grid gap-1">
          <div className="text-sm font-semibold text-[var(--app-text)]">{launchTarget === 'remote' ? 'Workspaces to send' : 'Workspaces'}</div>
          <div className="text-xs text-[var(--app-text-muted)]">
            Select the workspaces to add.
          </div>
        </div>
        <Badge tone={selectedWorkspaceCountValue > 0 ? 'live' : 'neutral'}>{selectedWorkspaceCountValue} selected</Badge>
      </div>

      {workspaceDrafts.length === 0 ? (
        <div className="rounded-xl border border-dashed border-[var(--app-border)] bg-transparent px-3 py-4 text-sm text-[var(--app-text-muted)]">
          No workspaces available yet.
        </div>
      ) : (
        <div className="grid gap-2 sm:grid-cols-2">
          {workspaceDrafts.map((workspace) => {
            const checked = workspace.selected
            const linkedDirectoryCount = Math.max(0, workspace.directories.length - 1)
            return (
              <label
                key={workspace.workspacePath}
                className={`flex min-w-0 items-center gap-3 rounded-xl border px-3 py-2 transition ${checked ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_7%,var(--app-surface))]' : 'border-[var(--app-border)] bg-transparent'}`}
              >
                <input
                  type="checkbox"
                  data-testid="add-swarm-workspace-checkbox"
                  data-workspace-path={workspace.workspacePath}
                  data-workspace-name={workspace.workspaceName}
                  className="h-4 w-4 rounded border-[var(--app-border)]"
                  checked={checked}
                  onChange={(event) => {
                    updateWorkspaceDraft(workspace.workspacePath, (item) => ({
                      ...item,
                      selected: event.target.checked,
                    }))
                  }}
                />
                <div className="min-w-0 flex-1">
                  <div className="flex min-w-0 items-center gap-2">
                    <div className="truncate text-sm font-medium text-[var(--app-text)]">{workspace.workspaceName}</div>
                    {linkedDirectoryCount > 0 ? (
                      <span className="shrink-0 text-xs text-[var(--app-text-muted)]">
                        {workspace.directories.length} director{workspace.directories.length === 1 ? 'y' : 'ies'}
                      </span>
                    ) : null}
                  </div>
                  <div className="truncate text-xs text-[var(--app-text-muted)]">{workspace.workspacePath}</div>
                </div>
                {checked ? <Check size={15} className="shrink-0 text-[var(--app-primary)]" /> : null}
              </label>
            )
          })}
        </div>
      )}

      {packagePlatformPanel}
    </Card>
  )

  return (
    <Dialog>
      <DialogBackdrop onClick={closeModal} />
      <DialogPanel
        data-testid="add-swarm-modal"
        className={panelClassName}
      >
        <div className={headerClassName}>
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div>
              <h2 className="text-xl font-semibold text-[var(--app-text)]">{launchTarget === 'remote' ? 'Link Swarm' : 'Add Container'}</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                {launchTarget === 'remote' ? 'Request a Managed Swarm relationship over Tailscale.' : 'Launch a local container swarm from selected workspaces.'}
              </p>
            </div>
            <Badge tone={launchTarget === 'remote' ? 'live' : activeRuntimeLabel ? 'live' : 'warning'}>
              {launchTarget === 'remote' ? 'Managed Swarm pairing' : activeRuntimeLabel ? `${activeRuntimeLabel} ready` : 'runtime required'}
            </Badge>
          </div>
        </div>

        <div className={bodyClassName}>
          {loading ? (
            <Card className="flex items-center gap-3 p-4 text-sm text-[var(--app-text-muted)]">
              <Loader2 size={16} className="animate-spin" />
              <span>Loading swarm options…</span>
            </Card>
          ) : null}

          {error ? (
            <Card data-testid="add-swarm-error" className="whitespace-pre-wrap border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">
              {error}
            </Card>
          ) : null}

          {status ? (
            <Card data-testid="add-swarm-status" className="border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-4 text-sm text-[var(--app-success)]">
              {status}
            </Card>
          ) : null}

          {launchTarget === 'local' ? swarmNameCard : null}

          <Card className={sectionClassName}>
            <div className="text-sm font-semibold text-[var(--app-text)]">Run target</div>
            <div className="grid gap-3 sm:grid-cols-2">
              <button
                type="button"
                data-testid="add-swarm-target-local"
                className={optionClassName(launchTarget === 'local')}
                onClick={() => setLaunchTarget('local')}
                disabled={submitting}
              >
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="text-sm font-semibold text-[var(--app-text)]">Local container</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">Launch here and replicate selected workspaces into a managed local swarm.</div>
                  </div>
                  {launchTarget === 'local' ? <Check size={16} className="shrink-0 text-[var(--app-primary)]" /> : null}
                </div>
              </button>

              <button
                type="button"
                data-testid="add-swarm-target-remote"
                className={optionClassName(launchTarget === 'remote')}
                onClick={() => {
                  setLaunchTarget('remote')
                  clearRemotePairingState()
                }}
                disabled={submitting}
              >
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="text-sm font-semibold text-[var(--app-text)]">Managed Swarm</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">Add a remote swarm over Tailscale with a high-entropy offer and short ceremony-code verification.</div>
                  </div>
                  {launchTarget === 'remote' ? <Check size={16} className="shrink-0 text-[var(--app-primary)]" /> : null}
                </div>
              </button>
            </div>
          </Card>

          {launchTarget === 'local' ? (
            <>
              <Card className={sectionClassName}>
                <div className="flex flex-col gap-1">
                  <div className="text-sm font-semibold text-[var(--app-text)]">Local runtime</div>
                  <div className="text-xs text-[var(--app-text-muted)]">
                    Choose which local container runtime should launch the replicated managed swarm.
                  </div>
                </div>

                <div className="grid gap-3 sm:grid-cols-2">
                  {(['podman', 'docker'] as const).map((runtime) => {
                    const available = runtimeStatus.available.includes(runtime)
                    const installed = runtimeStatus.installed.includes(runtime)
                    const issue = runtimeStatus.issues[runtime]?.trim() || ''
                    const active = runtimeChoice === runtime
                    return (
                      <button
                        key={runtime}
                        type="button"
                        className={`${optionClassName(active)} ${available ? '' : 'opacity-60'}`}
                        onClick={() => {
                          if (available && !submitting) {
                            setSelectedRuntime(runtime)
                          }
                        }}
                        disabled={submitting || !available}
                      >
                        <div className="flex items-start justify-between gap-3">
                          <div>
                            <div className="text-sm font-semibold text-[var(--app-text)]">{runtime}</div>
                            <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                              {available
                                ? (runtime === runtimeStatus.recommended ? 'Detected and recommended on this device.' : 'Detected and usable here.')
                                : installed
                                  ? (issue ? `Installed, but unavailable here: ${issue}` : 'Installed, but unavailable here.')
                                  : `Install ${runtime} to launch local swarms here.`}
                            </div>
                          </div>
                          {active ? <Check size={16} className="shrink-0 text-[var(--app-primary)]" /> : null}
                        </div>
                      </button>
                    )
                  })}
                </div>

                {!runtimeChoice && runtimeStatus.warning ? (
                  <div className="rounded-2xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning-text)]">
                    {runtimeStatus.warning}
                  </div>
                ) : null}
              </Card>


              {workspaceSelectionCard}

            </>
          ) : null}

          {launchTarget === 'remote' ? (
            <>
              <Card className={sectionClassName}>
                <div className="flex flex-col gap-1">
                  <div className="text-sm font-semibold text-[var(--app-text)]">Link Swarm</div>
                  <div className="text-xs text-[var(--app-text-muted)]">
                    Select an online Tailscale device running swarmd. Swarm requests pairing directly, then shows the 6-character ceremony code returned by the real API.
                  </div>
                </div>

                <div className={subtlePanelClassName}>
                    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                      <div>
                        <div className="text-sm font-semibold text-[var(--app-text)]">Tailscale candidates</div>
                        <div className="text-xs text-[var(--app-text-muted)]">Choose the already-installed swarmd host to manage.</div>
                      </div>
                      <Button type="button" variant="outline" onClick={() => void loadRemoteCandidates()} disabled={submitting || remoteCandidatesLoading}>
                        {remoteCandidatesLoading ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
                        Refresh
                      </Button>
                    </div>
                    {remoteCandidatesError ? (
                      <div className="mt-3 rounded-lg border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-3 text-sm text-[var(--app-warning-text)]">
                        {remoteCandidatesError}
                      </div>
                    ) : null}
                    {remoteCandidates.length === 0 ? (
                      <div className="mt-3 rounded-lg border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">
                        {remoteCandidatesLoading ? 'Loading Tailscale devices…' : 'No reachable swarmd hosts found on your Tailnet. Start swarmd on the managed host, confirm Tailscale is connected, then refresh.'}
                      </div>
                    ) : (
                      <div className="mt-3 grid gap-2">
                        {remoteCandidates.map((candidate) => {
                          const selected = candidate.id === selectedRemoteCandidateID
                          const endpoint = selectCandidateEndpoint(candidate)
                          return (
                            <button
                              key={candidate.id}
                              type="button"
                              className={optionClassName(selected)}
                              onClick={() => {
                                setSelectedRemoteCandidateID(candidate.id)
                                setRemoteCeremonyCode('')
                                clearRemotePairingState()
                              }}
                              disabled={submitting}
                            >
                              <div className="flex items-start justify-between gap-3">
                                <div className="min-w-0">
                                  <div className="truncate text-sm font-semibold text-[var(--app-text)]">{candidate.name || candidate.dnsName || endpointHostLabel(endpoint) || 'Tailscale device'}</div>
                                  <div className="mt-1 truncate text-xs text-[var(--app-text-muted)]">{endpoint || candidate.dnsName || candidate.ips.join(', ')}</div>
                                  <div className="mt-1 text-xs text-[var(--app-text-muted)]">{candidate.os || 'unknown OS'} · {candidate.transportMode || 'tailscale'}</div>
                                </div>
                                {selected ? <Check size={16} className="shrink-0 text-[var(--app-primary)]" /> : null}
                              </div>
                            </button>
                          )
                        })}
                      </div>
                    )}
                  </div>

                <div className={subtlePanelClassName}>
                  <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_180px] sm:items-center">
                    <div className="grid gap-1 text-sm">
                      <div className="font-semibold text-[var(--app-text)]">Pairing request</div>
                      <div className="text-xs text-[var(--app-text-muted)]">
                        Start pairing first. Swarm fetches the managed host offer and returns a verification-only ceremony code for approval on the managed host.
                      </div>
                      <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)]">
                        <div><span className="font-medium text-[var(--app-text)]">Managed host:</span> {remotePairingResult?.request.managed_name || selectedRemoteCandidate?.name || selectedRemoteCandidate?.dnsName || 'Select a host'}</div>
                        <div><span className="font-medium text-[var(--app-text)]">Endpoint:</span> {selectedRemoteCandidateEndpoint || 'Required'}</div>
                        {remotePairingResult?.request.managed_fingerprint ? <div><span className="font-medium text-[var(--app-text)]">Fingerprint:</span> {remotePairingResult.request.managed_fingerprint}</div> : null}
                      </div>
                    </div>
                    <div className="rounded-lg border border-[var(--app-border)] p-3 text-center">
                      <div className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Ceremony code</div>
                      <div className="mt-1 font-mono text-2xl font-semibold text-[var(--app-text)]">
                        {activeRemoteCeremonyCode ? formatPairingCode(activeRemoteCeremonyCode) : '—'}
                      </div>
                    </div>
                  </div>
                </div>

                {remotePairingResult ? (
                  <div className="rounded-lg border border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-3 text-sm text-[var(--app-success)]">
                    <div className="font-medium">Pairing request sent</div>
                    <div className="mt-1 text-[var(--app-text)]">Approve request {remotePairingResult.request.request_id} on the managed swarm after confirming code {formatPairingCode(remoteCeremonyCode || activeRemoteCeremonyCode)}.</div>
                  </div>
                ) : null}
              </Card>
            </>
          ) : null}



          <Card className={sectionClassName}>
            <div className="flex items-start justify-between gap-3">
              <div className="grid gap-1">
                <div className="text-sm font-semibold text-[var(--app-text)]">Ready Check</div>
                <div className="text-xs text-[var(--app-text-muted)]">
                  {launchTarget === 'remote'
                    ? 'Select a reachable Tailscale swarmd host, then start pairing. The ceremony code appears after the request is sent.'
                    : 'Smart defaults are selected. Adjust only what you need before launch.'}
                </div>
              </div>
              <Badge tone={launchTarget === 'remote' ? (selectedRemoteCandidate && selectedRemoteCandidateEndpoint ? 'live' : 'warning') : runtimeChoice ? 'live' : 'warning'}>
                {launchTarget === 'remote'
                  ? (selectedRemoteCandidate && selectedRemoteCandidateEndpoint ? 'ready to request' : 'host required')
                  : (runtimeChoice ? 'ready' : 'runtime required')}
              </Badge>
            </div>

            {launchTarget === 'local' ? (
              <>
                <div className="grid gap-2 sm:grid-cols-3">
                  <button
                    type="button"
                    role="switch"
                    aria-checked={alwaysOn}
                    data-testid="add-swarm-always-on"
                    className={optionClassName(alwaysOn)}
                    onClick={() => {
                      if (!submitting && !false) {
                        invalidateLaunchDraft()
                        setAlwaysOn((current) => !current)
                      }
                    }}
                    disabled={submitting || false}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div>
                        <div className="text-sm font-semibold text-[var(--app-text)]">Always On</div>
                        <div className="mt-1 text-xs text-[var(--app-text-muted)]">{alwaysOn ? 'Restart attached swarms on host startup.' : 'Manual start after host restart.'}</div>
                      </div>
                      {alwaysOn ? <Check size={15} className="shrink-0 text-[var(--app-primary)]" /> : null}
                    </div>
                  </button>

                  <button
                    type="button"
                    role="switch"
                    aria-checked={syncEnabled}
                    className={optionClassName(syncEnabled)}
                    onClick={() => {
                      if (!submitting && !false) {
                        invalidateLaunchDraft()
                        setSyncEnabled((current) => !current)
                      }
                    }}
                    disabled={submitting || false}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div>
                        <div className="text-sm font-semibold text-[var(--app-text)]">Swarm Sync</div>
                        <div className="mt-1 text-xs text-[var(--app-text-muted)]">{syncEnabled ? 'Continuously follows manager swarm changes.' : 'Standalone managed swarm auth.'}</div>
                      </div>
                      {syncEnabled ? <Check size={15} className="shrink-0 text-[var(--app-primary)]" /> : null}
                    </div>
                  </button>

                  <button
                    type="button"
                    role="switch"
                    aria-checked={bypassPermissions}
                    className={optionClassName(bypassPermissions)}
                    onClick={() => {
                      if (!submitting && !false) {
                        invalidateLaunchDraft()
                        setBypassPermissions((current) => !current)
                      }
                    }}
                    disabled={submitting || false}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div>
                        <div className="text-sm font-semibold text-[var(--app-text)]">Permission sync & bypass override</div>
                        <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                          {!syncEnabled
                            ? 'Swarm Sync is off — host permission policy will not sync.'
                            : bypassPermissions
                              ? 'Bypass override on — host permission policy will not sync to this managed swarm.'
                              : 'Permissions sync on — host policy syncs to this managed swarm. Click to enable container bypass override.'}
                        </div>
                      </div>
                      {bypassPermissions ? <Check size={15} className="shrink-0 text-[var(--app-primary)]" /> : null}
                    </div>
                  </button>
                </div>

                {syncEnabled ? (
                  <div className="rounded-lg border border-[var(--app-border)] bg-transparent p-3">
                    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                      <div className="grid gap-1">
                        <div className="text-sm font-medium text-[var(--app-text)]">What is Swarm Sync?</div>
                        <div className="text-xs text-[var(--app-text-muted)]">
                          When credentials, saved agents, custom tools, skills, or host permissions change on the manager swarm, Swarm Sync updates this managed swarm automatically unless the bypass override is on.
                        </div>
                      </div>
                      <Badge tone="live">automatic</Badge>
                    </div>
                    {hostVaultEnabled ? (
                      <div className="mt-3 grid gap-1">
                        <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Vault password</label>
                        <Input
                          data-testid="add-swarm-sync-vault-password"
                          type="password"
                          value={syncVaultPassword}
                          onChange={(event) => setSyncVaultPassword(event.target.value)}
                          placeholder="Required to unlock synced credentials"
                          disabled={submitting || false}
                        />
                      </div>
                    ) : null}
                  </div>
                ) : null}
              </>
            ) : null}

            <div className="grid gap-2 text-sm text-[var(--app-text-muted)] sm:grid-cols-2">
              {launchTarget === 'remote' ? (
                <>
                  <div><span className="font-medium text-[var(--app-text)]">Target:</span> Managed Swarm</div>
                  <div><span className="font-medium text-[var(--app-text)]">Mode:</span> Tailscale candidate</div>
                  <div><span className="font-medium text-[var(--app-text)]">Endpoint:</span> {selectedRemoteCandidateEndpoint || 'Required'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Ceremony:</span> {formatPairingCode(activeRemoteCeremonyCode) || 'Shown after request'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Container scope:</span> managed_host_local</div>
                  <div><span className="font-medium text-[var(--app-text)]">Remote containers:</span> stay local to the managed host</div>
                </>
              ) : (
                <>
                  <div><span className="font-medium text-[var(--app-text)]">Target:</span> Local container</div>
                  <div><span className="font-medium text-[var(--app-text)]">Manager:</span> {managerName}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Runtime:</span> {runtimeChoice || 'Unavailable'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Workspaces:</span> {selectedWorkspaceCountValue}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Always On:</span> {alwaysOn ? 'Enabled' : 'Disabled'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Swarm Sync:</span> {syncEnabled ? 'Enabled' : 'Disabled'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Permissions:</span> {!syncEnabled ? 'Not synced' : bypassPermissions ? 'Bypass override enabled' : 'Synced; bypass override available'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Swarm name:</span> {swarmName.trim() || 'Required'}</div>
                </>
              )}
            </div>
          </Card>
        </div>

        <div className="flex flex-col gap-3 border-t border-[var(--app-border)] px-6 py-5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-sm text-[var(--app-text-muted)]">
            {footerStatusText}
          </div>
          <div className="flex gap-3">
            <Button type="button" variant="outline" onClick={closeModal} disabled={submitting}>Cancel</Button>
            <Button
              type="button"
              data-testid="add-swarm-launch"
              onClick={() => void handleSubmit()}
              disabled={
                submitting
                || loading
                || !group?.group.id.trim()
                || (launchTarget === 'local'
                  ? !runtimeChoice || !swarmName.trim() || selectedWorkspaceCountValue === 0
                  : remoteCandidatesLoading || !selectedRemoteCandidate || !selectedRemoteCandidateEndpoint)
              }
            >
              {(submitting || remoteCandidatesLoading) ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
              {submitting
                ? 'Working…'
                : launchTarget === 'remote'
                  ? 'Start Pairing'
                  : 'Launch'}
            </Button>
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
