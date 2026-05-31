import { useEffect, useMemo, useState } from 'react'
import { Check, HelpCircle, Loader2, Plus, X } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import {
  Dialog,
  DialogBackdrop,
  DialogPanel,
} from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { Select } from '../../../../components/ui/select'
import { fetchSwarmState, type SwarmLocalState } from '../api/swarm-state'
import { fetchSwarmLocalRuntimeStatus, type SwarmLocalRuntimeStatus } from '../api/local-containers'
import { fetchDesktopUpdateStatus } from '../../update/api'
import type { DesktopOnboardingStatus } from '../types/dashboard-status'
import {
  fetchDeployContainerPackageDefaults,
  suggestDeployContainerPackages,
  validateDeployContainerPackage,
  type DeployContainerPackageSelection,
} from '../api/deploy-container'
import {
  replicateSwarm,
  ReplicateSwarmLaunchError,
} from '../api/replicate-swarm'
import { fetchSwarmTargets, type SwarmTarget } from '../api/swarm-targets'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { useDesktopStore } from '../../state/use-desktop-store'

interface AddSwarmModalProps {
  open: boolean
  onboardingStatus: DesktopOnboardingStatus | null
  onOpenChange: (open: boolean) => void
  onComplete: (message: string) => Promise<void> | void
}

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
const LOCAL_TARGET_SWARM_ID = '__local__'
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

function buildContainerPackageManifest(
  packages: ContainerPackageDraft[],
  baseImage: string,
  packageManager: string,
) {
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

function mergeContainerPackages(
  packages: ContainerPackageDraft[],
): ContainerPackageDraft[] {
  const ordered = new Map<string, ContainerPackageDraft>()
  for (const pkg of packages) {
    const name = String(pkg.name ?? '')
      .trim()
      .toLowerCase()
    if (!name) continue
    const current = ordered.get(name)
    if (!current) {
      ordered.set(name, {
        name,
        source: pkg.source,
        reason: pkg.reason?.trim() || undefined,
      })
      continue
    }
    const nextSource: ContainerPackageDraft['source'] =
      current.source === 'user_added' || pkg.source === 'user_added'
        ? 'user_added'
        : current.source === 'workspace_scan' || pkg.source === 'workspace_scan'
          ? 'workspace_scan'
          : 'recommended'
    const reasons = [current.reason, pkg.reason]
      .map((value) => String(value ?? '').trim())
      .filter(
        (value, index, array) =>
          value.length > 0 && array.indexOf(value) === index,
      )
    ordered.set(name, {
      name,
      source: nextSource,
      reason: reasons.length > 0 ? reasons.join('; ') : undefined,
    })
  }
  return Array.from(ordered.values())
}

function mapSuggestedPackages(
  packages: DeployContainerPackageSelection[],
): ContainerPackageDraft[] {
  return (packages ?? [])
    .map(
      (pkg): ContainerPackageDraft => ({
        name: String(pkg.name ?? '')
          .trim()
          .toLowerCase(),
        source:
          pkg.source === 'workspace_scan' ? 'workspace_scan' : 'recommended',
        reason: String(pkg.reason ?? '').trim() || undefined,
      }),
    )
    .filter((pkg) => pkg.name.length > 0)
}

function describePackageSource(pkg: ContainerPackageDraft): string {
  if (pkg.source === 'user_added') return 'Added manually'
  if (pkg.source === 'workspace_scan')
    return pkg.reason?.trim()
      ? `Suggested from workspace scan: ${pkg.reason}`
      : 'Suggested from workspace scan'
  return 'Base recommendation'
}

const FALLBACK_RUNTIME_STATUS: SwarmLocalRuntimeStatus = {
  recommended: '',
  available: [],
  installed: [],
  issues: {},
  warning: 'Could not detect local container runtime.',
}

type SwarmGroupState = NonNullable<SwarmLocalState['groups']>[number]

function currentGroup(state: SwarmLocalState | null): SwarmGroupState | null {
  if (!state) return null
  const groups = state.groups ?? []
  const currentGroupID = String(state.current_group_id ?? '').trim()
  if (currentGroupID) {
    const exact = groups.find(
      (group) => String(group.group?.id ?? '').trim() === currentGroupID,
    )
    if (exact) return exact
  }
  return groups[0] ?? null
}

function buildWorkspaceDrafts(
  workspaces: WorkspaceEntry[],
): ReplicateWorkspaceDraft[] {
  return workspaces.map((workspace) => ({
    workspacePath: workspace.path,
    selected: false,
    workspaceName:
      workspace.workspaceName ||
      workspace.path.split('/').filter(Boolean).pop() ||
      'workspace',
    directories: workspace.directories,
  }))
}

function selectedWorkspaceCount(items: ReplicateWorkspaceDraft[]): number {
  return items.filter((item) => item.selected).length
}

function managedHostTargets(targets: SwarmTarget[]): SwarmTarget[] {
  return (targets ?? []).filter((target) => {
    const relationship = String(target.relationship ?? '').trim().toLowerCase()
    return (
      relationship === 'managed' &&
      Boolean(target.selectable) &&
      Boolean(target.online) &&
      String(target.swarm_id ?? '').trim().length > 0
    )
  })
}

export function AddSwarmModal({
  open,
  onboardingStatus,
  onOpenChange,
  onComplete,
}: AddSwarmModalProps) {
  const inheritedDevMode = Boolean(onboardingStatus?.config.devMode)
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [modalSwarmState, setModalSwarmState] = useState<SwarmLocalState | null>(null)
  const [devMode, setDevMode] = useState(inheritedDevMode)
  const [workspaceDrafts, setWorkspaceDrafts] = useState<
    ReplicateWorkspaceDraft[]
  >([])
  const [runtimeStatus, setRuntimeStatus] = useState<SwarmLocalRuntimeStatus>(
    FALLBACK_RUNTIME_STATUS,
  )
  const [selectedRuntime, setSelectedRuntime] = useState<
    'podman' | 'docker' | ''
  >('')
  const [swarmName, setSwarmName] = useState('')
  const [targetHosts, setTargetHosts] = useState<SwarmTarget[]>([])
  const [selectedTargetHostSwarmID, setSelectedTargetHostSwarmID] = useState(LOCAL_TARGET_SWARM_ID)
  const [syncVaultPassword, setSyncVaultPassword] = useState('')
  const [bypassPermissions, setBypassPermissions] = useState(false)
  const [containerPackageBaseImage, setContainerPackageBaseImage] = useState(
    FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE,
  )
  const [containerPackageManager, setContainerPackageManager] = useState(
    FALLBACK_CONTAINER_PACKAGE_MANAGER,
  )
  const [containerPackages, setContainerPackages] = useState<
    ContainerPackageDraft[]
  >(DEFAULT_CONTAINER_PACKAGES)
  const [packageInput, setPackageInput] = useState('')
  const [packageValidationError, setPackageValidationError] = useState<
    string | null
  >(null)
  const [validatingPackage, setValidatingPackage] = useState(false)
  const [suggestingPackages, setSuggestingPackages] = useState(false)
  const [packageSuggestionError, setPackageSuggestionError] = useState<
    string | null
  >(null)
  const [packagePlatformInfoOpen, setPackagePlatformInfoOpen] = useState(false)

  const vault = useDesktopStore((state) => state.vault)
  const runtimeChoice = useMemo(
    () =>
      selectedRuntime && runtimeStatus.available.includes(selectedRuntime)
        ? selectedRuntime
        : runtimeStatus.recommended || '',
    [runtimeStatus, selectedRuntime],
  ) as 'podman' | 'docker' | ''
  const group = useMemo(
    () => currentGroup(modalSwarmState),
    [modalSwarmState],
  )
  const hostSwarmID = String(group?.group?.host_swarm_id ?? '').trim() || String(modalSwarmState?.node.swarm_id ?? '').trim()
  const hostVaultEnabled = Boolean(vault.enabled)
  const currentSelfTarget = useMemo(
    () => targetHosts.find((target) => target.current || target.kind === 'self') ?? null,
    [targetHosts],
  )
  const managerName = useMemo(
    () =>
      currentSelfTarget?.name?.trim() ||
      group?.members?.find(
        (member) => String(member.swarm_id ?? '').trim() === hostSwarmID,
      )?.name ||
      modalSwarmState?.node.name ||
      'This host',
    [currentSelfTarget?.name, group, hostSwarmID, modalSwarmState?.node.name],
  )
  const managedTargetHosts = useMemo(
    () => managedHostTargets(targetHosts),
    [targetHosts],
  )
  const selectedManagedTargetHost = useMemo(
    () =>
      managedTargetHosts.find(
        (target) => target.swarm_id === selectedTargetHostSwarmID,
      ) ?? null,
    [managedTargetHosts, selectedTargetHostSwarmID],
  )
  const launchTargetLabel = selectedManagedTargetHost?.name?.trim() || managerName
  const launchTargetIsManaged = Boolean(selectedManagedTargetHost)
  const selectedWorkspaceCountValue = useMemo(
    () => selectedWorkspaceCount(workspaceDrafts),
    [workspaceDrafts],
  )
  const selectedWorkspacePaths = useMemo(
    () =>
      workspaceDrafts
        .filter((item) => item.selected)
        .map((item) => item.workspacePath),
    [workspaceDrafts],
  )

  const invalidateLaunchDraft = () => undefined

  useEffect(() => {
    let cancelled = false
    if (selectedWorkspacePaths.length === 0) {
      setContainerPackages((current) =>
        mergeContainerPackages(
          current.filter((pkg) => pkg.source !== 'workspace_scan'),
        ),
      )
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
        if (cancelled) return
        setContainerPackages((current) =>
          mergeContainerPackages([
            ...current.filter((pkg) => pkg.source !== 'workspace_scan'),
            ...mapSuggestedPackages(packages),
          ]),
        )
      })
      .catch((err) => {
        if (cancelled) return
        setPackageSuggestionError(
          err instanceof Error
            ? err.message
            : 'Failed to suggest packages from workspace contents',
        )
        setContainerPackages((current) =>
          mergeContainerPackages(
            current.filter((pkg) => pkg.source !== 'workspace_scan'),
          ),
        )
      })
      .finally(() => {
        if (!cancelled) setSuggestingPackages(false)
      })
    return () => {
      cancelled = true
    }
  }, [selectedWorkspacePaths.join('|')])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    setError(null)
    setStatus(null)
    setSelectedRuntime('')
    setModalSwarmState(null)
    setDevMode(inheritedDevMode)
    setTargetHosts([])
    setSelectedTargetHostSwarmID(LOCAL_TARGET_SWARM_ID)
    setSyncVaultPassword('')
    setBypassPermissions(false)
    setContainerPackageBaseImage(FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE)
    setContainerPackageManager(FALLBACK_CONTAINER_PACKAGE_MANAGER)
    setContainerPackages(DEFAULT_CONTAINER_PACKAGES)
    setPackageInput('')
    setPackageValidationError(null)
    setSuggestingPackages(false)
    setPackageSuggestionError(null)
    void Promise.all([
      listWorkspaces().catch(() => []),
      fetchSwarmLocalRuntimeStatus().catch(() => FALLBACK_RUNTIME_STATUS),
      fetchDeployContainerPackageDefaults().catch(() => ({
        baseImage: FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE,
        packageManager: FALLBACK_CONTAINER_PACKAGE_MANAGER,
      })),
      fetchSwarmTargets().catch(() => ({ ok: false, targets: [] })),
      fetchSwarmState(),
      fetchDesktopUpdateStatus(),
    ])
      .then(
        ([
          nextWorkspaces,
          nextRuntimeStatus,
          nextPackageDefaults,
          nextTargetsResponse,
          nextSwarmState,
          nextUpdateStatus,
        ]) => {
          if (cancelled) return
          setWorkspaceDrafts(buildWorkspaceDrafts(nextWorkspaces))
          const nextTargets = Array.isArray(nextTargetsResponse.targets)
            ? nextTargetsResponse.targets
            : []
          setTargetHosts(nextTargets)
          setSelectedTargetHostSwarmID((current) => {
            if (current === LOCAL_TARGET_SWARM_ID) return current
            return managedHostTargets(nextTargets).some(
              (target) => target.swarm_id === current,
            )
              ? current
              : LOCAL_TARGET_SWARM_ID
          })
          setRuntimeStatus(nextRuntimeStatus)
          setContainerPackageBaseImage(
            nextPackageDefaults.baseImage ||
              FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE,
          )
          setContainerPackageManager(
            nextPackageDefaults.packageManager ||
              FALLBACK_CONTAINER_PACKAGE_MANAGER,
          )
          setModalSwarmState(nextSwarmState)
          setDevMode(Boolean(nextUpdateStatus.dev_mode))
          setSwarmName('')
          setSelectedRuntime(
            (nextRuntimeStatus.recommended || '') as 'podman' | 'docker' | '',
          )
        },
      )
      .catch((err) => {
        if (!cancelled)
          setError(
            err instanceof Error
              ? err.message
              : 'Failed to load container launch options',
          )
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [inheritedDevMode, open])

  const closeModal = () => {
    if (!submitting) onOpenChange(false)
  }

  const addPackage = async () => {
    const normalized = packageInput.trim().toLowerCase()
    if (!normalized) {
      setPackageValidationError('Package name is required.')
      return
    }
    if (containerPackages.some((pkg) => pkg.name === normalized)) {
      setPackageValidationError(
        `Package ${normalized} is already in the install list.`,
      )
      return
    }
    setValidatingPackage(true)
    setPackageValidationError(null)
    try {
      const result = await validateDeployContainerPackage(normalized)
      if (!result.valid)
        throw new Error(`apt package ${normalized} was not found on this host`)
      setContainerPackages((current) =>
        mergeContainerPackages([
          ...current,
          { name: normalized, source: 'user_added' },
        ]),
      )
      setPackageInput('')
    } catch (err) {
      setPackageValidationError(
        err instanceof Error ? err.message : 'Failed to validate package',
      )
    } finally {
      setValidatingPackage(false)
    }
  }

  const removePackage = (name: string) => {
    setContainerPackages((current) =>
      current.filter((pkg) => pkg.name !== name),
    )
    setPackageValidationError(null)
  }

  const updateWorkspaceDraft = (
    workspacePath: string,
    transform: (draft: ReplicateWorkspaceDraft) => ReplicateWorkspaceDraft,
  ) => {
    invalidateLaunchDraft()
    setWorkspaceDrafts((current) =>
      current.map((item) =>
        item.workspacePath === workspacePath ? transform(item) : item,
      ),
    )
  }

  const finishSuccess = async (message: string) => {
    await onComplete(message)
    onOpenChange(false)
  }

  const handleLaunchLocal = async () => {
    if (!runtimeChoice) {
      setError(
        runtimeStatus.warning || 'No supported local runtime is available.',
      )
      return
    }
    if (!swarmName.trim()) {
      setError('Container name is required.')
      return
    }
    const selected = workspaceDrafts.filter((item) => item.selected)
    if (selected.length === 0) {
      setError('Select at least one workspace to add.')
      return
    }
    if (hostVaultEnabled && !syncVaultPassword.trim()) {
      setError('Vault password is required to sync from a vaulted host.')
      return
    }
    setSubmitting(true)
    setError(null)
    setStatus(
      launchTargetIsManaged
        ? `Creating container on ${launchTargetLabel}…`
        : 'Creating local container…',
    )
    try {
      const syncModules = ['credentials', 'agents', 'custom_tools', 'skills', 'permissions', 'model_defaults']
      const result = await replicateSwarm({
        mode: 'local',
        swarmName: swarmName.trim(),
        targetHostSwarmID: launchTargetIsManaged
          ? selectedManagedTargetHost?.swarm_id.trim()
          : undefined,
        runtime: runtimeChoice,
        bypassPermissions,
        alwaysOn: true,
        sync: {
          enabled: true,
          mode: 'managed',
          modules: syncModules,
          vaultPassword:
            hostVaultEnabled ? syncVaultPassword.trim() : '',
        },
        workspaces: selected.map((item) => ({
          sourceWorkspacePath: item.workspacePath,
          replicationMode: 'bundle',
          writable: true,
        })),
        containerPackages: devMode
          ? buildContainerPackageManifest(
              containerPackages,
              containerPackageBaseImage,
              containerPackageManager,
            )
          : undefined,
      })
      await finishSuccess(
        launchTargetIsManaged
          ? `Added ${result.swarm.name || swarmName.trim()} as a container on ${launchTargetLabel}.`
          : `Added ${result.swarm.name || swarmName.trim()} as a local container.`,
      )
    } catch (err) {
      if (err instanceof ReplicateSwarmLaunchError) {
        const details = err.details
        const guidance = [
          details.failure.attachStatus
            ? `Attach status: ${details.failure.attachStatus}`
            : '',
          details.failure.lastAttachError
            ? `Last attach error: ${details.failure.lastAttachError}`
            : '',
          details.failure.runtime ? `Runtime: ${details.failure.runtime}` : '',
          details.failure.containerName
            ? `Container: ${details.failure.containerName}`
            : '',
          details.failure.backendHostPort > 0
            ? `Backend port: ${details.failure.backendHostPort}`
            : '',
          details.failure.desktopHostPort > 0
            ? `Desktop port: ${details.failure.desktopHostPort}`
            : '',
          details.failure.childBackendURL
            ? `Managed backend URL: ${details.failure.childBackendURL}`
            : '',
          details.failure.childDesktopURL
            ? `Managed desktop URL: ${details.failure.childDesktopURL}`
            : '',
          'Check the Swarm dashboard deployment details and the host swarmd log for this deployment.',
        ].filter(Boolean)
        setError(
          [details.error || 'Failed to add container', ...guidance].join('\n'),
        )
      } else {
        setError(err instanceof Error ? err.message : 'Failed to add container')
      }
    } finally {
      setSubmitting(false)
    }
  }

  if (!open) return null

  const panelClassName =
    'mx-auto flex max-h-[calc(100dvh_-_var(--app-safe-area-top)_-_var(--app-safe-area-bottom)_-_12px)] w-[calc(100dvw_-_12px)] max-w-[840px] min-w-0 flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:max-h-[min(860px,calc(100dvh_-_48px))] sm:w-[min(840px,calc(100vw_-_48px))]'
  const headerClassName =
    'shrink-0 border-b border-[var(--app-border)] px-4 py-3 sm:px-5 sm:py-4'
  const bodyClassName =
    'flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto overscroll-contain px-3 py-3 sm:px-5 sm:py-4'
  const sectionClassName =
    'grid min-w-0 gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-none'
  const optionClassName = (active: boolean) =>
    `min-w-0 rounded-lg border px-3 py-2 text-left transition ${active ? 'border-[var(--app-primary)] bg-transparent text-[var(--app-text)]' : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)]'}`
  const infoCardClassName =
    'min-w-0 rounded-lg border border-[var(--app-border)] bg-transparent px-3 py-2 text-left text-[var(--app-text)]'
  const launchPendingReason = (() => {
    if (loading) return 'Loading launch options…'
    if (!swarmName.trim()) return 'Please enter a container name.'
    if (!runtimeChoice)
      return runtimeStatus.warning || 'Please choose an available runtime.'
    if (selectedWorkspaceCountValue === 0) return 'Please select a workspace.'
    if (hostVaultEnabled && !syncVaultPassword.trim())
      return 'Please enter the vault password to enable sync.'
    return null
  })()
  const footerStatusText =
    launchPendingReason ||
    `${selectedWorkspaceCountValue} selected workspace${selectedWorkspaceCountValue === 1 ? '' : 's'} will be added to ${launchTargetIsManaged ? launchTargetLabel : 'a local container'} using ${runtimeChoice || 'the selected runtime'} with built-in sync and always-on enabled.`

  return (
    <Dialog className="overflow-hidden p-1.5 pt-[calc(var(--app-safe-area-top)_+_0.375rem)] pb-[calc(var(--app-safe-area-bottom)_+_0.375rem)] sm:p-6">
      <DialogBackdrop onClick={closeModal} />
      <DialogPanel data-testid="add-swarm-modal" className={panelClassName}>
        <div className={headerClassName}>
          <div className="flex min-w-0 flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0">
              <h2 className="break-words text-lg font-semibold text-[var(--app-text)] sm:text-xl">
                Add Container
              </h2>
              <p className="mt-1 break-words text-sm text-[var(--app-text-muted)]">
                Launch a container from selected workspaces on this device or a managed host.
              </p>
            </div>
            <Badge tone={runtimeChoice ? 'live' : 'warning'} className="self-start sm:shrink-0">
              {runtimeChoice ? `${runtimeChoice} ready` : 'runtime required'}
            </Badge>
          </div>
        </div>

        <div className={bodyClassName}>
          {loading ? (
            <Card className="flex items-center gap-3 p-4 text-sm text-[var(--app-text-muted)]">
              <Loader2 size={16} className="animate-spin" />
              Loading container options…
            </Card>
          ) : null}
          {error ? (
            <Card
              data-testid="add-swarm-error"
              className="whitespace-pre-wrap break-words border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]"
            >
              {error}
            </Card>
          ) : null}
          {status ? (
            <Card
              data-testid="add-swarm-status"
              className="break-words border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-4 text-sm text-[var(--app-success)]"
            >
              {status}
            </Card>
          ) : null}

          <Card className={sectionClassName}>
            <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(220px,320px)] sm:items-center">
              <div className="grid min-w-0 gap-1">
                <div className="text-sm font-semibold text-[var(--app-text)]">
                  Name this container
                </div>
                <div className="break-words text-xs text-[var(--app-text-muted)]">
                  Choose the display name used to identify this container after launch.
                </div>
              </div>
              <Input
                data-testid="add-swarm-local-name"
                value={swarmName}
                onChange={(event) => setSwarmName(event.target.value)}
                disabled={submitting}
                placeholder="Enter a container name"
              />
            </div>
          </Card>

          <Card className={sectionClassName}>
            <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(240px,340px)] sm:items-center">
              <div className="grid min-w-0 gap-1">
                <div className="text-sm font-semibold text-[var(--app-text)]">
                  Host target
                </div>
                <div className="break-words text-xs text-[var(--app-text-muted)]">
                  Choose where this container should be created. Managed hosts
                  are listed only when online and selectable.
                </div>
              </div>
              <Select
                data-testid="add-swarm-target-host"
                value={selectedTargetHostSwarmID}
                onChange={(event) =>
                  setSelectedTargetHostSwarmID(event.target.value)
                }
                disabled={submitting}
              >
                <option value={LOCAL_TARGET_SWARM_ID}>Local — {managerName}</option>
                {managedTargetHosts.map((target) => (
                  <option key={target.swarm_id} value={target.swarm_id}>
                    {target.name || target.swarm_id}
                  </option>
                ))}
              </Select>
            </div>
            <div className="break-words text-xs text-[var(--app-text-muted)]">
              {launchTargetIsManaged
                ? `This request will stay in local create mode and target managed host ${launchTargetLabel}.`
                : 'Default: create on this primary host.'}
            </div>
          </Card>

          <Card className={sectionClassName}>
            <div className="flex flex-col gap-1">
              <div className="text-sm font-semibold text-[var(--app-text)]">
                Container runtime
              </div>
              <div className="break-words text-xs text-[var(--app-text-muted)]">
                Choose which container runtime should launch the container on the selected host.
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
                    onClick={() =>
                      available && !submitting && setSelectedRuntime(runtime)
                    }
                    disabled={submitting || !available}
                  >
                    <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="text-sm font-semibold text-[var(--app-text)]">
                          {runtime}
                        </div>
                        <div className="mt-1 break-words text-xs text-[var(--app-text-muted)]">
                          {available
                            ? runtime === runtimeStatus.recommended
                              ? 'Detected and recommended on this device.'
                              : 'Detected and usable here.'
                            : installed
                              ? issue
                                ? `Installed, but unavailable here: ${issue}`
                                : 'Installed, but unavailable here.'
                              : `Install ${runtime} to launch local containers here.`}
                        </div>
                      </div>
                      {active ? (
                        <Check
                          size={16}
                          className="shrink-0 text-[var(--app-primary)]"
                        />
                      ) : null}
                    </div>
                  </button>
                )
              })}
            </div>
            {!runtimeChoice && runtimeStatus.warning ? (
              <div className="break-words rounded-2xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning-text)]">
                {runtimeStatus.warning}
              </div>
            ) : null}
          </Card>

          <Card className={sectionClassName}>
            <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
              <div className="grid min-w-0 gap-1">
                <div className="text-sm font-semibold text-[var(--app-text)]">
                  Workspaces
                </div>
                <div className="break-words text-xs text-[var(--app-text-muted)]">
                  Select the workspaces to add.
                </div>
              </div>
              <Badge
                tone={selectedWorkspaceCountValue > 0 ? 'live' : 'neutral'}
              >
                {selectedWorkspaceCountValue} selected
              </Badge>
            </div>
            {workspaceDrafts.length === 0 ? (
              <div className="break-words rounded-xl border border-dashed border-[var(--app-border)] bg-transparent px-3 py-4 text-sm text-[var(--app-text-muted)]">
                No workspaces available yet.
              </div>
            ) : (
              <div className="grid gap-2 sm:grid-cols-2">
                {workspaceDrafts.map((workspace) => {
                  const checked = workspace.selected
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
                        onChange={(event) =>
                          updateWorkspaceDraft(
                            workspace.workspacePath,
                            (item) => ({
                              ...item,
                              selected: event.target.checked,
                            }),
                          )
                        }
                      />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium text-[var(--app-text)]">
                          {workspace.workspaceName}
                        </div>
                        <div className="truncate text-xs text-[var(--app-text-muted)]">
                          {workspace.workspacePath}
                        </div>
                      </div>
                      {checked ? (
                        <Check
                          size={15}
                          className="shrink-0 text-[var(--app-primary)]"
                        />
                      ) : null}
                    </label>
                  )
                })}
              </div>
            )}
            <div className="min-w-0 rounded-lg border border-[var(--app-border)] bg-transparent p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <div className="text-sm font-semibold text-[var(--app-text)]">
                    Package platform
                  </div>
                  <button
                    type="button"
                    className="inline-flex h-6 w-6 items-center justify-center rounded-full border border-[var(--app-border)] text-[var(--app-text-muted)] transition hover:border-[var(--app-primary)] hover:text-[var(--app-text)]"
                    aria-label="Explain package platform"
                    aria-expanded={packagePlatformInfoOpen}
                    onClick={() =>
                      setPackagePlatformInfoOpen((current) => !current)
                    }
                  >
                    <HelpCircle size={14} />
                  </button>
                </div>
                <Badge tone={containerPackages.length > 0 ? 'live' : 'neutral'} className="shrink-0">
                  {containerPackages.length} apt packages
                </Badge>
              </div>
              {packagePlatformInfoOpen ? (
                <div className="mt-3 break-words rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs leading-5 text-[var(--app-text-muted)]">
                  Package platform controls the container OS image and apt
                  packages installed before your selected workspaces are copied
                  in.
                </div>
              ) : null}
              <div className="mt-2 break-words text-xs text-[var(--app-text-muted)]">
                Base image{' '}
                <span className="font-medium text-[var(--app-text)]">
                  {containerPackageBaseImage}
                </span>{' '}
                · manager{' '}
                <span className="font-medium text-[var(--app-text)]">
                  {containerPackageManager}
                </span>
              </div>
              <div className="mt-3 flex flex-wrap gap-2">
                {containerPackages.slice(0, 18).map((pkg) => (
                  <Badge
                    key={pkg.name}
                    tone={
                      pkg.source === 'user_added'
                        ? 'live'
                        : pkg.source === 'workspace_scan'
                          ? 'warning'
                          : 'neutral'
                    }
                    className="gap-2 pr-1"
                    title={describePackageSource(pkg)}
                  >
                    <span>{pkg.name}</span>
                    <button
                      type="button"
                      onClick={() => removePackage(pkg.name)}
                      className="inline-flex h-5 w-5 items-center justify-center rounded-md text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface)] hover:text-[var(--app-text)]"
                      disabled={
                        submitting || validatingPackage || suggestingPackages
                      }
                      aria-label={`Remove package ${pkg.name}`}
                    >
                      <X size={12} />
                    </button>
                  </Badge>
                ))}
                {containerPackages.length > 18 ? (
                  <Badge tone="neutral" className="shrink-0">
                    +{containerPackages.length - 18} more
                  </Badge>
                ) : null}
              </div>
              <div className="mt-3 grid gap-2 sm:flex">
                <Input
                  value={packageInput}
                  onChange={(event) => {
                    setPackageInput(event.target.value)
                    setPackageValidationError(null)
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') {
                      event.preventDefault()
                      void addPackage()
                    }
                  }}
                  placeholder={
                    suggestingPackages
                      ? 'Scanning selected workspaces…'
                      : 'Add apt package'
                  }
                  disabled={submitting || validatingPackage}
                />
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => void addPackage()}
                  disabled={submitting || validatingPackage}
                  className="w-full sm:w-auto"
                >
                  {validatingPackage ? (
                    <Loader2 size={14} className="animate-spin" />
                  ) : (
                    <Plus size={14} />
                  )}
                  Add
                </Button>
              </div>
              {packageValidationError || packageSuggestionError ? (
                <div className="mt-2 break-words text-xs text-[var(--app-danger)]">
                  {packageValidationError || packageSuggestionError}
                </div>
              ) : null}
            </div>
          </Card>

          <Card className={sectionClassName}>
            <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
              <div className="grid min-w-0 gap-1">
                <div className="text-sm font-semibold text-[var(--app-text)]">
                  Ready Check
                </div>
                <div className="break-words text-xs text-[var(--app-text-muted)]">
                  Containers include always-on restart and Swarm Sync so the main
                  swarm can keep managing them after launch.
                </div>
              </div>
              <Badge tone={runtimeChoice ? 'live' : 'warning'} className="self-start sm:shrink-0">
                {runtimeChoice ? 'ready' : 'runtime required'}
              </Badge>
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              <div className={infoCardClassName} data-testid="add-swarm-always-on">
                <div className="flex min-w-0 items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-sm font-semibold text-[var(--app-text)]">
                      Always On included
                    </div>
                    <div className="mt-1 break-words text-xs text-[var(--app-text-muted)]">
                      Required so the container restarts with its host and stays reachable from the main swarm.
                    </div>
                  </div>
                  <Check
                    size={15}
                    className="shrink-0 text-[var(--app-primary)]"
                  />
                </div>
              </div>
              <div className={infoCardClassName}>
                <div className="flex min-w-0 items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-sm font-semibold text-[var(--app-text)]">
                      Swarm Sync included
                    </div>
                    <div className="mt-1 break-words text-xs text-[var(--app-text-muted)]">
                      Required so the main swarm can sync credentials, agents, tools, and workspace management into this container.
                    </div>
                  </div>
                  <Check
                    size={15}
                    className="shrink-0 text-[var(--app-primary)]"
                  />
                </div>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={bypassPermissions}
                className={optionClassName(bypassPermissions)}
                onClick={() =>
                  !submitting && setBypassPermissions((current) => !current)
                }
                disabled={submitting}
              >
                <div className="flex min-w-0 items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="text-sm font-semibold text-[var(--app-text)]">
                      Turn permissions off for this container only
                    </div>
                    <div className="mt-1 break-words text-xs text-[var(--app-text-muted)]">
                      {bypassPermissions
                        ? 'Permission policy sync is off for this container. Swarm Sync stays on for everything else.'
                        : 'Permission policy sync is on. Turn this on only if this container should manage its own permissions.'}
                    </div>
                  </div>
                  {bypassPermissions ? (
                    <Check
                      size={15}
                      className="shrink-0 text-[var(--app-primary)]"
                    />
                  ) : null}
                </div>
              </button>
            </div>
            {hostVaultEnabled ? (
              <div className="min-w-0 rounded-lg border border-[var(--app-border)] bg-transparent p-3">
                <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">
                  Vault password
                </label>
                <Input
                  data-testid="add-swarm-sync-vault-password"
                  type="password"
                  value={syncVaultPassword}
                  onChange={(event) => setSyncVaultPassword(event.target.value)}
                  placeholder="Required to unlock synced credentials"
                  disabled={submitting}
                />
              </div>
            ) : null}
            <div className="grid min-w-0 gap-2 break-words text-sm text-[var(--app-text-muted)] sm:grid-cols-2">
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Target:
                </span>{' '}
                {launchTargetIsManaged ? `Managed host — ${launchTargetLabel}` : 'Local container'}
              </div>
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Manager:
                </span>{' '}
                {launchTargetLabel}
              </div>
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Runtime:
                </span>{' '}
                {runtimeChoice || 'Unavailable'}
              </div>
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Workspaces:
                </span>{' '}
                {selectedWorkspaceCountValue}
              </div>
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Always On:
                </span>{' '}
                Enabled
              </div>
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Swarm Sync:
                </span>{' '}
                Enabled
              </div>
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Permissions:
                </span>{' '}
                {bypassPermissions
                  ? 'Off for this container only'
                  : 'Synced from main swarm'}
              </div>
              <div className="min-w-0">
                <span className="font-medium text-[var(--app-text)]">
                  Swarm name:
                </span>{' '}
                {swarmName.trim() || 'Required'}
              </div>
            </div>
          </Card>
        </div>

        <div className="grid shrink-0 gap-3 border-t border-[var(--app-border)] px-3 py-3 pb-[max(0.75rem,var(--app-safe-area-bottom))] sm:flex sm:items-center sm:justify-between sm:px-6 sm:py-5">
          <div className="min-w-0 break-words text-sm text-[var(--app-text-muted)]">
            {footerStatusText}
          </div>
          <div className="grid gap-2 sm:flex sm:gap-3">
            <Button
              type="button"
              variant="outline"
              onClick={closeModal}
              disabled={submitting}
              className="w-full sm:w-auto"
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="primary"
              data-testid="add-swarm-launch"
              onClick={() => void handleLaunchLocal()}
              disabled={
                submitting ||
                loading ||
                !runtimeChoice ||
                !swarmName.trim() ||
                selectedWorkspaceCountValue === 0 ||
                (hostVaultEnabled && !syncVaultPassword.trim())
              }
              className="w-full sm:w-auto"
            >
              {submitting ? (
                <Loader2 size={14} className="animate-spin" />
              ) : (
                <Plus size={14} />
              )}
              {submitting ? 'Working…' : 'Launch'}
            </Button>
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
