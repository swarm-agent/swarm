import { useEffect, useMemo, useState } from 'react'
import { Check, HelpCircle, Loader2, Plus, X } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { Select } from '../../../../components/ui/select'
import { fetchDesktopOnboardingStatus, fetchSwarmLocalRuntimeStatus, saveDesktopOnboarding, type SwarmLocalRuntimeStatus } from '../../onboarding/api'
import { getUISettings, saveRemoteSSHTarget } from '../../settings/swarm/queries/get-ui-settings'
import type { DesktopOnboardingStatus } from '../../onboarding/types'
import {
  approveRemoteDeploySession,
  createRemoteDeploySession,
  fetchDeployContainerPackageDefaults,
  fetchRemoteDeploySession,
  fetchRemoteDeploySessions,
  startRemoteDeploySession,
  suggestDeployContainerPackages,
  validateDeployContainerPackage,
  type DeployContainerPackageSelection,
  type RemoteDeployPreflightError,
  type RemoteDeploySession,
  type RemoteDeployStartError,
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
  onOpenChange: (open: boolean) => void
  onComplete: (message: string) => Promise<void> | void
}

type LaunchTarget = 'local' | 'remote'
type RemoteDeployMethod = 'lan' | 'tailscale'
type RemoteTailscaleAuthMode = 'manual' | 'key'
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
const REMOTE_IMAGE_DELIVERY_MODE_RELEASE: 'registry' = 'registry'
const REMOTE_IMAGE_DELIVERY_MODE_DEV: 'archive' = 'archive'
const REMOTE_LAUNCH_POLL_INTERVAL_MS = 2000

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

function formatBytes(bytes?: number): string {
  const safeBytes = typeof bytes === 'number' && Number.isFinite(bytes) && bytes > 0 ? bytes : 0
  if (safeBytes <= 0) {
    return 'unknown'
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = safeBytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return unit === 0 ? `${safeBytes} ${units[unit]}` : `${value.toFixed(1)} ${units[unit]}`
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

function preferredChildSwarmName(
  onboardingStatus: DesktopOnboardingStatus | null,
  groupNames: string[],
): string {
  const baseName = preferredChildSwarmBaseName(onboardingStatus)
  const usedNames = new Set(
    groupNames
      .map((value) => value.trim().toLowerCase())
      .filter(Boolean),
  )
  if (!usedNames.has(baseName.toLowerCase())) {
    return baseName
  }
  let suffix = 2
  while (usedNames.has(`${baseName} ${suffix}`.toLowerCase())) {
    suffix += 1
  }
  return `${baseName} ${suffix}`
}

function preferredChildSwarmBaseName(onboardingStatus: DesktopOnboardingStatus | null): string {
  const swarmName = onboardingStatus?.config.swarmName.trim()
  if (swarmName) {
    return `${swarmName} child`
  }
  const dnsLabel = firstHostnameLabel(onboardingStatus?.network.tailscale.dnsName)
  if (dnsLabel) {
    return `${dnsLabel} child`
  }
  return 'New child swarm'
}

function firstHostnameLabel(value: string | null | undefined): string {
  const trimmed = String(value ?? '').trim()
  if (!trimmed) {
    return ''
  }
  const withoutProtocol = trimmed.replace(/^[a-z]+:\/\//i, '')
  const hostname = withoutProtocol.split('/')[0].trim().replace(/\.+$/, '')
  if (!hostname) {
    return ''
  }
  return hostname.split('.')[0]?.trim() ?? ''
}

function remoteReachableHostCandidate(target: string): string {
  const trimmed = String(target ?? '').trim()
  if (!trimmed) {
    return ''
  }
  let value = trimmed
  const atIndex = value.lastIndexOf('@')
  if (atIndex >= 0) {
    value = value.slice(atIndex + 1).trim()
  }
  if (value.startsWith('[')) {
    const end = value.indexOf(']')
    if (end > 0) {
      return value.slice(1, end).trim()
    }
  }
  const colonIndex = value.lastIndexOf(':')
  if (colonIndex > 0 && value.indexOf(':') === colonIndex) {
    value = value.slice(0, colonIndex).trim()
  }
  const candidate = value.trim()
  if (!candidate) {
    return ''
  }
  if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(candidate)) {
    return candidate
  }
  if (candidate.includes(':')) {
    return candidate
  }
  if (candidate.includes('.')) {
    return candidate
  }
  return ''
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

const REMOTE_WORKSPACE_ROOT = '/workspaces'

function fallbackWorkspaceName(workspacePath: string, index: number): string {
  const segments = String(workspacePath ?? '').trim().split(/[\\/]/).filter(Boolean)
  return segments[segments.length - 1] || `workspace-${index + 1}`
}

function sanitizeRemoteWorkspaceTargetSegment(value: string): string {
  return String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '')
}

function nextRemoteWorkspaceTargetPath(baseName: string, usedTargets: Map<string, number>): string {
  const normalizedBase = sanitizeRemoteWorkspaceTargetSegment(baseName) || 'workspace'
  const basePath = `${REMOTE_WORKSPACE_ROOT}/${normalizedBase}`
  const count = (usedTargets.get(basePath) ?? 0) + 1
  usedTargets.set(basePath, count)
  return count === 1 ? basePath : `${basePath}-${count}`
}

function buildRemoteWorkspacePayloads(workspaces: ReplicateWorkspaceDraft[]) {
  const usedTargets = new Map<string, number>()
  return workspaces
    .filter((workspace) => workspace.selected)
    .map((workspace, index) => {
      const sourcePath = workspace.workspacePath.trim()
      const workspaceName = workspace.workspaceName.trim() || fallbackWorkspaceName(sourcePath, index)
      const baseName = sanitizeRemoteWorkspaceTargetSegment(workspaceName)
        || sanitizeRemoteWorkspaceTargetSegment(fallbackWorkspaceName(sourcePath, index))
        || `workspace-${index + 1}`
      const targetPath = nextRemoteWorkspaceTargetPath(baseName, usedTargets)
      const linkedDirectories = workspace.directories
        .map((directory) => directory.trim())
        .filter((directory) => directory.length > 0 && directory !== sourcePath)
        .map((directory, directoryIndex) => {
          const directoryBaseName = sanitizeRemoteWorkspaceTargetSegment(
            `${baseName}-dir-${fallbackWorkspaceName(directory, directoryIndex)}`,
          ) || `${baseName}-dir-${directoryIndex + 1}`
          return {
            sourcePath: directory,
            targetPath: nextRemoteWorkspaceTargetPath(directoryBaseName, usedTargets),
          }
        })
      return {
        sourcePath,
        workspacePath: sourcePath,
        workspaceName,
        targetPath,
        mode: 'rw' as const,
        directories: linkedDirectories,
      }
    })
}

function countRemotePayloadArchives(payloads: Array<{ directories?: Array<unknown> }>): number {
  return payloads.reduce((total, payload) => total + 1 + (payload.directories?.length ?? 0), 0)
}

type RemotePreflightGuidance = {
  title: string
  details: string[]
  commands: string[]
}

type MasterLANCallbackGuidance = {
  blocking: boolean
  title: string
  details: string[]
  suggestedHost: string
  suggestedPort: number
  canAutofill: boolean
}

function formatEndpointLabel(host: string, port: number): string {
  const normalizedHost = String(host ?? '').trim() || 'unset'
  return port > 0 ? `${normalizedHost}:${port}` : normalizedHost
}

function usableLANCallbackHost(value: string): string {
  const normalized = String(value ?? '').trim()
  if (!normalized) {
    return ''
  }
  const lower = normalized.toLowerCase()
  if (
    lower === 'localhost'
    || lower === '0.0.0.0'
    || lower === '::'
    || lower === '::1'
    || lower === '[::1]'
    || lower.startsWith('127.')
  ) {
    return ''
  }
  return normalized
}

function isLocalOnlyBindHost(value: string): boolean {
  const normalized = String(value ?? '').trim().toLowerCase()
  if (!normalized) {
    return false
  }
  return normalized === 'localhost'
    || normalized === '::1'
    || normalized === '[::1]'
    || normalized.startsWith('127.')
}

function preferredLANCallbackCandidate(onboardingStatus: DesktopOnboardingStatus | null): string {
  const config = onboardingStatus?.config
  const configHost = usableLANCallbackHost(config?.host ?? '')
  const tailscaleIPs = new Set((onboardingStatus?.network.tailscale.ips ?? []).map((value) => String(value).trim()))
  if (configHost && !tailscaleIPs.has(configHost)) {
    return configHost
  }
  const lanCandidates = onboardingStatus?.network.lanAddresses ?? []
  for (const candidate of lanCandidates) {
    const host = usableLANCallbackHost(candidate)
    if (!host || tailscaleIPs.has(host)) {
      continue
    }
    return host
  }
  return ''
}

function buildMasterLANCallbackGuidance(onboardingStatus: DesktopOnboardingStatus | null): MasterLANCallbackGuidance {
  const config = onboardingStatus?.config
  const bindHost = String(config?.host ?? '').trim()
  const bindPort = typeof config?.port === 'number' ? config.port : 0
  const advertiseHost = String(config?.advertiseHost ?? '').trim()
  const advertisePort = typeof config?.advertisePort === 'number' ? config.advertisePort : bindPort
  const effectiveHost = advertiseHost || usableLANCallbackHost(bindHost)
  const effectivePort = advertisePort > 0 ? advertisePort : bindPort
  const tailscaleURL = String(onboardingStatus?.network.tailscale.tailnetURL || config?.tailscaleURL || '').trim()
  const suggestedHost = preferredLANCallbackCandidate(onboardingStatus)
  const suggestedPort = effectivePort
  const bindIsLocalOnly = isLocalOnlyBindHost(bindHost)

  if (bindIsLocalOnly) {
    const details = [
      `This machine is still listening only on ${formatEndpointLabel(bindHost || '127.0.0.1', bindPort)}.`,
      advertiseHost
        ? `Advertise host is set to ${formatEndpointLabel(advertiseHost, effectivePort)}, but that only changes what children try to call. It does not move the master listener.`
        : 'No reachable LAN / VPN bind host is active on this machine yet.',
      'LAN / WireGuard remote deploy requires the master backend itself to listen on a reachable LAN, WireGuard, or tunnel address.',
      'Update host in the master swarm.conf to a reachable address, keep advertise_host aligned unless you have a separate forwarded endpoint, then restart Swarm.',
    ]
    if (suggestedHost) {
      details.push(`Detected LAN / VPN candidate on this machine: ${formatEndpointLabel(suggestedHost, suggestedPort)}.`)
    }
    if (tailscaleURL) {
      details.push(`If both machines are already on Tailscale, switch this deploy method to Tailscale and use ${tailscaleURL}.`)
    }
    return {
      blocking: true,
      title: 'Restart this machine with a LAN / VPN bind address first',
      details,
      suggestedHost,
      suggestedPort,
      canAutofill: false,
    }
  }

  if (effectiveHost) {
    return {
      blocking: false,
      title: 'This machine will accept LAN / WireGuard callbacks',
      details: advertiseHost
        ? [
            `Remote children will call back to ${formatEndpointLabel(effectiveHost, effectivePort)} over your LAN, WireGuard, or tunnel path.`,
            'Source: Advertise host on this machine.',
          ]
        : [
            `Remote children will call back to ${formatEndpointLabel(effectiveHost, effectivePort)} over your LAN, WireGuard, or tunnel path.`,
            'Source: Advertise host is blank, so Swarm will reuse this machine’s current host bind.',
          ],
      suggestedHost: '',
      suggestedPort,
      canAutofill: false,
    }
  }

  const details = [
    'LAN / WireGuard uses SSH only to install the child.',
    'After launch, the child must connect back to this machine over your LAN, WireGuard, or another VPN/tunnel path.',
    `Right now this machine is bound to ${formatEndpointLabel(bindHost, bindPort)}, which is still local-only for this flow.`,
    'Open Settings -> Swarm, then set Advertise host to the LAN or VPN address other machines should use to reach this machine.',
    'Examples: 10.0.0.12, 192.168.1.40, wg-box.internal.',
    'Leave Advertise port as the API port unless the child should call back on a different port.',
  ]
  if (suggestedHost) {
    details.push(`Swarm can fill this in now with ${formatEndpointLabel(suggestedHost, suggestedPort)}.`)
  }
  if (tailscaleURL) {
    details.push(`If both machines are already on Tailscale, switch this deploy method to Tailscale and use ${tailscaleURL}.`)
  }
  return {
    blocking: true,
    title: 'Set a LAN / VPN address for this machine first',
    details,
    suggestedHost,
    suggestedPort,
    canAutofill: suggestedHost !== '',
  }
}

function parseRemotePreflightGuidance(message: string): RemotePreflightGuidance | null {
  const text = typeof message === 'string' ? message.trim() : ''
  if (!text) {
    return null
  }
  const lines = text.split(/\r?\n/).map((line) => line.trim())
  const details: string[] = []
  const commands: string[] = []
  let title = 'Remote preflight failed'
  let section: 'details' | 'commands' | '' = ''
  for (const line of lines) {
    if (!line) {
      continue
    }
    if (title === 'Remote preflight failed' && line.toLowerCase().startsWith('remote preflight failed')) {
      title = line
      continue
    }
    const lower = line.toLowerCase()
    if (lower === 'suggested commands' || lower.startsWith('suggested commands')) {
      section = 'commands'
      continue
    }
    if (lower === 'what failed' || lower === 'what to do' || lower === 'alternative') {
      section = 'details'
      continue
    }
    if (line.startsWith('- ')) {
      const value = line.slice(2).trim()
      if (!value) {
        continue
      }
      if (section === 'commands') {
        commands.push(value)
      } else {
        details.push(value)
      }
      continue
    }
    if (section === 'commands') {
      commands.push(line)
    } else {
      details.push(line)
    }
  }
  return {
    title,
    details,
    commands,
  }
}

function remoteDeployStartError(error: unknown): RemoteDeployStartError | undefined {
  if (!error || typeof error !== 'object' || !('remoteStart' in error)) {
    return undefined
  }
  return (error as Error & { remoteStart?: RemoteDeployStartError }).remoteStart
}

function remoteLaunchStatusMessage(session: RemoteDeploySession, fallbackMethod: RemoteDeployMethod): string {
  const progress = String(session.last_progress ?? '').trim()
  const target = String(session.ssh_session_target ?? '').trim() || 'remote host'
  const status = String(session.status ?? '').trim()
  if (status === 'failed') {
    return String(session.last_error ?? '').trim() || progress || `Remote deploy failed on ${target}.`
  }
  if (session.remote_auth_url) {
    return `Remote child started. Approve Tailscale login: ${session.remote_auth_url}`
  }
  if (session.remote_endpoint) {
    return `Remote child started. Waiting for the child to enroll back at ${session.remote_endpoint}…`
  }
  if (progress) {
    return progress
  }
  if (status === 'starting') {
    return `Remote deploy is running on ${target}.`
  }
  if (status === 'waiting_for_approval' || status === 'waiting_for_child') {
    return `Remote child started. Waiting for the child to enroll back over ${fallbackMethod === 'tailscale' ? 'Tailscale' : 'LAN / WireGuard'}…`
  }
  return ''
}

function remoteLaunchFailureMessage(error: unknown, session: RemoteDeploySession | null): string {
  const base = error instanceof Error ? error.message : 'Failed to launch remote swarm'
  const details = [
    String(session?.last_progress ?? '').trim(),
    String(session?.last_error ?? '').trim(),
  ].filter((value, index, values) => value && value !== base && values.indexOf(value) === index)
  return [base, ...details].join('\n')
}

export function AddSwarmModal({ open, onboardingStatus, onOpenChange, onComplete }: AddSwarmModalProps) {
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [currentOnboardingStatus, setCurrentOnboardingStatus] = useState<DesktopOnboardingStatus | null>(onboardingStatus)
  const [workspaceDrafts, setWorkspaceDrafts] = useState<ReplicateWorkspaceDraft[]>([])
  const [runtimeStatus, setRuntimeStatus] = useState<SwarmLocalRuntimeStatus>(FALLBACK_RUNTIME_STATUS)
  const [, setRemoteSessions] = useState<RemoteDeploySession[]>([])
  const [savedRemoteSSHTargets, setSavedRemoteSSHTargets] = useState<string[]>([])
  const [remoteSSHTarget, setRemoteSSHTarget] = useState('')
  const [remoteRuntimeChoice, setRemoteRuntimeChoice] = useState<'docker' | 'podman'>('docker')
  const [remoteDeployMethod, setRemoteDeployMethod] = useState<RemoteDeployMethod>('tailscale')
  const [remoteReachableHost, setRemoteReachableHost] = useState('')
  const [remotePreflightSession, setRemotePreflightSession] = useState<RemoteDeploySession | null>(null)
  const [remotePreflightError, setRemotePreflightError] = useState<string | null>(null)
  const [remotePreflightLoading, setRemotePreflightLoading] = useState(false)
  const [remotePreflightGuidance, setRemotePreflightGuidance] = useState<RemotePreflightGuidance | null>(null)
  const [configuringMasterLANCallback, setConfiguringMasterLANCallback] = useState(false)
  const [remoteTailscaleAuthMode, setRemoteTailscaleAuthMode] = useState<RemoteTailscaleAuthMode>('manual')
  const [remoteTailscaleAuthKey, setRemoteTailscaleAuthKey] = useState('')
  const [launchTarget, setLaunchTarget] = useState<LaunchTarget>('local')
  const [selectedRuntime, setSelectedRuntime] = useState<'podman' | 'docker' | ''>('')
  const [swarmName, setSwarmName] = useState('')
  const [syncEnabled, setSyncEnabled] = useState(true)
  const [syncAgentsEnabled, setSyncAgentsEnabled] = useState(true)
  const [syncCustomToolsEnabled, setSyncCustomToolsEnabled] = useState(true)
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
  const activeRuntimeLabel = launchTarget === 'remote' ? remoteRuntimeChoice : runtimeChoice
  const devMode = Boolean(currentOnboardingStatus?.config.devMode)
  const remoteImageDeliveryMode = devMode ? REMOTE_IMAGE_DELIVERY_MODE_DEV : REMOTE_IMAGE_DELIVERY_MODE_RELEASE

  const group = useMemo(() => currentGroup(currentOnboardingStatus), [currentOnboardingStatus])
  const hostSwarmID = group?.group.hostSwarmID || ''
  const hostVaultEnabled = Boolean(vault.enabled)
  const masterLANCallbackGuidance = useMemo(
    () => (remoteDeployMethod === 'lan' ? buildMasterLANCallbackGuidance(currentOnboardingStatus) : null),
    [currentOnboardingStatus, remoteDeployMethod],
  )
  const remotePreflightBlocked = remoteDeployMethod === 'lan' && Boolean(masterLANCallbackGuidance?.blocking)
  const remotePreflightCanAutofill = remoteDeployMethod === 'lan' && Boolean(masterLANCallbackGuidance?.canAutofill)
  const remoteReachableHostSuggestions = useMemo(() => {
    if (remoteDeployMethod !== 'lan') {
      return []
    }
    const values = remotePreflightSession?.preflight.remote_network_candidates ?? []
    const seen = new Set<string>()
    const out: string[] = []
    for (const value of values) {
      const trimmed = String(value ?? '').trim()
      if (!trimmed) {
        continue
      }
      const key = trimmed.toLowerCase()
      if (seen.has(key)) {
        continue
      }
      seen.add(key)
      out.push(trimmed)
    }
    return out
  }, [remoteDeployMethod, remotePreflightSession])
  const masterName = useMemo(
    () => group?.members.find((member) => member.swarmID === hostSwarmID)?.name || 'Current master',
    [group, hostSwarmID],
  )

  const suggestedSwarmName = useMemo(
    () => preferredChildSwarmName(
      currentOnboardingStatus,
      currentOnboardingStatus?.groups.flatMap((entry) => entry.members.map((member) => member.name)) ?? [],
    ),
    [currentOnboardingStatus],
  )
  const selectedWorkspaceCountValue = useMemo(() => selectedWorkspaceCount(workspaceDrafts), [workspaceDrafts])
  const selectedWorkspacePaths = useMemo(
    () => workspaceDrafts.filter((item) => item.selected).map((item) => item.workspacePath),
    [workspaceDrafts],
  )
  const selectedRemotePayloads = useMemo(() => buildRemoteWorkspacePayloads(workspaceDrafts), [workspaceDrafts])
  const selectedRemoteArchiveCount = useMemo(() => countRemotePayloadArchives(selectedRemotePayloads), [selectedRemotePayloads])
  const remotePreflightArchiveCount = useMemo(
    () => countRemotePayloadArchives(remotePreflightSession?.preflight.payloads ?? []),
    [remotePreflightSession],
  )

  const invalidateRemotePreflight = () => {
    setRemotePreflightSession(null)
    setRemotePreflightError(null)
    setRemotePreflightGuidance(null)
  }

  const updateWorkspaceDraft = (
    workspacePath: string,
    transform: (draft: ReplicateWorkspaceDraft) => ReplicateWorkspaceDraft,
  ) => {
    invalidateRemotePreflight()
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
      invalidateRemotePreflight()
      setContainerPackages((current) => mergeContainerPackages([...current, { name: normalized, source: 'user_added' }]))
      setPackageInput('')
    } catch (err) {
      setPackageValidationError(err instanceof Error ? err.message : 'Failed to validate package')
    } finally {
      setValidatingPackage(false)
    }
  }

  const removePackage = (name: string) => {
    invalidateRemotePreflight()
    setContainerPackages((current) => current.filter((pkg) => pkg.name !== name))
    setPackageValidationError(null)
  }

  useEffect(() => {
    let cancelled = false
    if (selectedWorkspacePaths.length === 0) {
      invalidateRemotePreflight()
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
        invalidateRemotePreflight()
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
        invalidateRemotePreflight()
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
    setLaunchTarget('local')
    setSelectedRuntime('')
    setSyncEnabled(true)
    setSyncAgentsEnabled(true)
    setSyncCustomToolsEnabled(true)
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
      getUISettings().catch(() => null),
      fetchDeployContainerPackageDefaults().catch(() => ({
        baseImage: FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE,
        packageManager: FALLBACK_CONTAINER_PACKAGE_MANAGER,
      })),
      onboardingStatus ? Promise.resolve(onboardingStatus) : fetchDesktopOnboardingStatus().catch(() => null),
    ])
      .then(([nextWorkspaces, nextRuntimeStatus, nextUISettings, nextPackageDefaults, nextOnboardingStatus]) => {
        if (cancelled) {
          return
        }
        setWorkspaceDrafts(buildWorkspaceDrafts(nextWorkspaces))
        setRuntimeStatus(nextRuntimeStatus)
        setContainerPackageBaseImage(nextPackageDefaults.baseImage || FALLBACK_CONTAINER_PACKAGE_BASE_IMAGE)
        setContainerPackageManager(nextPackageDefaults.packageManager || FALLBACK_CONTAINER_PACKAGE_MANAGER)
        setRemoteSessions([])
        setCurrentOnboardingStatus(nextOnboardingStatus)
        const nextSavedTargets = Array.isArray(nextUISettings?.swarm?.remote_ssh_targets)
          ? nextUISettings.swarm.remote_ssh_targets.filter((value): value is string => typeof value === 'string' && value.trim().length > 0)
          : []
        setSavedRemoteSSHTargets(nextSavedTargets)
        setRemoteSSHTarget((current) => current || nextSavedTargets[0] || '')
        setRemotePreflightSession(null)
        setRemotePreflightError(null)
        setRemotePreflightGuidance(null)
        setRemoteRuntimeChoice('docker')
        setRemoteDeployMethod('tailscale')
        setRemoteReachableHost('')
        setRemoteTailscaleAuthMode('manual')
        setRemoteTailscaleAuthKey('')
        const nextSuggestedSwarmName = preferredChildSwarmName(
          nextOnboardingStatus,
          nextOnboardingStatus?.groups.flatMap((entry) => entry.members.map((member) => member.name)) ?? [],
        )
        setSwarmName(nextSuggestedSwarmName)
        setSelectedRuntime((nextRuntimeStatus.recommended || '') as 'podman' | 'docker' | '')
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
          setError(err instanceof Error ? err.message : 'Failed to load Add Swarm options')
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
  }, [onboardingStatus, open])

  const closeModal = () => {
    if (submitting) {
      return
    }
    onOpenChange(false)
  }

  useEffect(() => {
    if (remoteDeployMethod !== 'lan' || remoteReachableHost.trim()) {
      return
    }
    const candidate = remoteReachableHostCandidate(remoteSSHTarget)
    if (candidate) {
      setRemoteReachableHost(candidate)
    }
  }, [remoteDeployMethod, remoteReachableHost, remoteSSHTarget])

  const refreshRemoteSessions = async (options?: { refresh?: boolean; sessionID?: string }): Promise<RemoteDeploySession[]> => {
    if (options?.sessionID?.trim()) {
      const nextSession = await fetchRemoteDeploySession(options.sessionID.trim(), { refresh: options.refresh ?? true })
      setRemoteSessions((current) => [nextSession, ...current.filter((item) => item.id !== nextSession.id)])
      logAddSwarm('refreshed remote session', { session_id: nextSession.id, status: nextSession.status })
      return [nextSession]
    }
    const nextSessions = await fetchRemoteDeploySessions({ refresh: options?.refresh ?? true })
    setRemoteSessions(nextSessions)
    logAddSwarm('refreshed remote session list', { sessions: nextSessions.length })
    return nextSessions
  }

  const ensureMasterLANCallbackConfigured = async (): Promise<DesktopOnboardingStatus | null> => {
    if (remoteDeployMethod !== 'lan') {
      return currentOnboardingStatus
    }
    if (!masterLANCallbackGuidance?.blocking) {
      return currentOnboardingStatus
    }
    const suggestedHost = masterLANCallbackGuidance?.suggestedHost.trim() || ''
    if (!suggestedHost) {
      throw new Error('Swarm could not detect a LAN / VPN address for this machine. Open Settings -> Swarm and set Advertise host manually.')
    }
    const suggestedPort = masterLANCallbackGuidance.suggestedPort > 0 ? masterLANCallbackGuidance.suggestedPort : (currentOnboardingStatus?.config.port || 0)
    setConfiguringMasterLANCallback(true)
    setStatus(`Using ${formatEndpointLabel(suggestedHost, suggestedPort)} for this machine’s LAN / WireGuard callback…`)
    try {
      const nextOnboarding = await saveDesktopOnboarding({
        advertiseHost: suggestedHost,
        advertisePort: suggestedPort,
      })
      setCurrentOnboardingStatus(nextOnboarding)
      if (nextOnboarding.config.restartRequired) {
        throw new Error(`Saved this machine’s LAN / VPN address, but Swarm must be restarted before remote preflight can continue: ${nextOnboarding.config.restartReason || 'transport settings changed'}.`)
      }
      return nextOnboarding
    } finally {
      setConfiguringMasterLANCallback(false)
    }
  }

  const runRemotePreflight = async (): Promise<RemoteDeploySession> => {
    if (!group?.group.id.trim()) {
      throw new Error('Create or select a swarm group on the master before adding a remote swarm.')
    }
    if (!swarmName.trim()) {
      throw new Error('Swarm name is required.')
    }
    if (!remoteSSHTarget.trim()) {
      throw new Error('SSH alias or target is required.')
    }
    if (syncEnabled && hostVaultEnabled && !syncVaultPassword.trim()) {
      throw new Error('Vault password is required to sync from a vaulted host.')
    }
    setRemotePreflightLoading(true)
    setRemotePreflightError(null)
    setRemotePreflightGuidance(null)
    try {
      await ensureMasterLANCallbackConfigured()
      const session = await createRemoteDeploySession({
        name: swarmName.trim(),
        sshSessionTarget: remoteSSHTarget.trim(),
        transportMode: remoteDeployMethod,
        remoteAdvertiseHost: remoteDeployMethod === 'lan' ? remoteReachableHost.trim() : '',
        groupID: group.group.id,
        groupName: group.group.name,
        remoteRuntime: remoteRuntimeChoice,
        imageDeliveryMode: remoteImageDeliveryMode,
        syncEnabled,
        bypassPermissions,
        alwaysOn,
        containerPackages: buildContainerPackageManifest(containerPackages, containerPackageBaseImage, containerPackageManager),
        payloads: selectedRemotePayloads,
      })
      setRemotePreflightSession(session)
      if (remoteDeployMethod === 'lan' && !remoteReachableHost.trim() && session.remote_advertise_host?.trim()) {
        setRemoteReachableHost(session.remote_advertise_host.trim())
      }
      setRemoteSessions((current) => [session, ...current.filter((item) => item.id !== session.id)])
      const currentSettings = await getUISettings().catch(() => null)
      if (currentSettings) {
        const saved = await saveRemoteSSHTarget({ current: currentSettings, target: remoteSSHTarget.trim() })
        const nextSavedTargets = Array.isArray(saved.swarm?.remote_ssh_targets)
          ? saved.swarm.remote_ssh_targets.filter((value): value is string => typeof value === 'string' && value.trim().length > 0)
          : []
        setSavedRemoteSSHTargets(nextSavedTargets)
      }
      return session
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Remote preflight failed'
      const remotePreflight = err && typeof err === 'object' && 'remotePreflight' in err
        ? (err as Error & { remotePreflight?: RemoteDeployPreflightError }).remotePreflight
        : undefined
      setRemotePreflightGuidance(parseRemotePreflightGuidance(remotePreflight?.error || message))
      setRemotePreflightError(message)
      setRemotePreflightSession(null)
      throw err instanceof Error ? err : new Error(message)
    } finally {
      setRemotePreflightLoading(false)
    }
  }

  const handleLaunchLocal = async () => {
    if (!group?.group.id.trim()) {
      setError('Create or select a swarm group on the master before adding a local swarm.')
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
      const syncModules = [
        'credentials',
        ...(syncAgentsEnabled ? ['agents'] : []),
        ...(syncCustomToolsEnabled ? ['custom_tools'] : []),
      ]
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
          guidance.push(`Child backend URL: ${details.failure.childBackendURL}`)
        }
        if (details.failure.childDesktopURL) {
          guidance.push(`Child desktop URL: ${details.failure.childDesktopURL}`)
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
      setError('Create or select a swarm group on the master before adding a remote swarm.')
      return
    }
    if (!swarmName.trim()) {
      setError('Swarm name is required.')
      return
    }
    if (!remoteSSHTarget.trim()) {
      setError('SSH alias or target is required.')
      return
    }
    if (remoteDeployMethod === 'lan' && !remoteReachableHost.trim()) {
      setError('Remote reachable host is required for LAN / WireGuard deploy.')
      return
    }
    let preflightSession = remotePreflightSession
    if (!preflightSession
      || preflightSession.ssh_session_target?.trim() !== remoteSSHTarget.trim()
      || preflightSession.name?.trim() !== swarmName.trim()) {
      setStatus('Running remote preflight…')
      try {
        preflightSession = await runRemotePreflight()
      } catch (err) {
        setStatus(null)
        setError(err instanceof Error ? err.message : 'Remote preflight failed')
        return
      }
    }
    if (syncEnabled && hostVaultEnabled && !syncVaultPassword.trim()) {
      setError('Vault password is required to sync from a vaulted host.')
      return
    }
    if (remoteDeployMethod === 'tailscale' && remoteTailscaleAuthMode === 'key' && !remoteTailscaleAuthKey.trim()) {
      setError('Tailscale auth key is required for auth-key launch mode.')
      return
    }

    setSubmitting(true)
    setError(null)
    let latestRemoteSession: RemoteDeploySession | null = preflightSession
    let pollLaunchProgress = false
    let launchProgressPoll: Promise<void> | null = null
    const stopLaunchProgressPoll = async () => {
      pollLaunchProgress = false
      if (launchProgressPoll) {
        await launchProgressPoll
      }
    }
    try {
      const session = preflightSession
      setStatus(`Preflight ready: ${session.preflight.summary || `copying ${session.preflight.files_to_copy?.length || 0} files to ${remoteSSHTarget.trim()}`}`)
      const confirmed = window.confirm([
        `Remote host: ${remoteSSHTarget.trim()}`,
        `Builder runtime: ${session.preflight.builder_runtime || 'unknown'}`,
        `Remote runtime: ${session.preflight.remote_runtime || 'unknown'}`,
        `Deploy method: ${remoteDeployMethod === 'tailscale' ? 'Tailscale' : `LAN / WireGuard via ${remoteReachableHost.trim()}`}`,
        `Always On: ${alwaysOn ? 'enabled (restart attached child on host startup)' : 'disabled (manual start)'}`,
        `Files copied: ${(session.preflight.files_to_copy || []).join(', ') || 'none'}`,
        `Payloads: ${(session.preflight.payloads || []).map((payload) => `${payload.workspace_name || payload.source_path} (${payload.included_files} tracked files)`).join('; ') || 'none'}`,
        ...(remoteDeployMethod === 'tailscale'
          ? [`Tailscale login: ${remoteTailscaleAuthMode === 'key' ? 'Auth key (launch only, not saved)' : 'Manual browser approval'}`]
          : []),
        '',
        selectedRemotePayloads.length > 0
          ? 'This will send only Git-tracked files from the selected workspace roots and any linked directories to the remote server.'
          : 'No workspace payloads will be staged; the remote child will start empty and connect back to this master.',
        devMode
          ? `This will deliver generated config/install/start content over SSH stdin, copy a locally built dev Swarm image archive${selectedRemotePayloads.length > 0 ? ' and selected payload archives' : ''} over SSH, then load and launch it on the remote host.`
          : `This will deliver generated config/install/start content over SSH stdin${selectedRemotePayloads.length > 0 ? ' and copy selected payload archives over SSH' : ''}, then have the remote host download the published ${remoteDeployMethod === 'tailscale' ? 'SSH + Tailscale' : 'SSH + LAN / WireGuard'} remote image when it is not already present there.`,
        `The remote child will be launched there and configured to connect back to this master over the master's ${remoteDeployMethod === 'tailscale' ? 'Tailscale' : 'LAN / WireGuard'} endpoint.`,
        alwaysOn
          ? 'Always On is recorded for this child; the host will restart attached local children on Swarm startup. Remote machine boot persistence still depends on the remote owner.'
          : 'Always On is off; reboot/startup recovery will not automatically restart this child.',
        '',
        'Continue with SSH launch?'
      ].join('\n'))
      if (!confirmed) {
        setStatus('Remote deploy cancelled after preflight review.')
        return
      }

      setStatus('Preparing payloads locally and shipping the minimum needed over SSH…')
      pollLaunchProgress = true
      launchProgressPoll = (async () => {
        while (pollLaunchProgress) {
          await new Promise((resolve) => window.setTimeout(resolve, REMOTE_LAUNCH_POLL_INTERVAL_MS))
          if (!pollLaunchProgress) {
            break
          }
          try {
            const sessions = await refreshRemoteSessions({ sessionID: session.id, refresh: false })
            const nextSession = sessions.find((item) => item.id === session.id)
            if (!nextSession) {
              continue
            }
            latestRemoteSession = nextSession
            const nextStatus = remoteLaunchStatusMessage(nextSession, remoteDeployMethod)
            if (nextStatus) {
              setStatus(nextStatus)
            }
          } catch (pollErr) {
            logAddSwarmError('remote launch progress poll failed', pollErr, {
              session_id: session.id,
              ssh_target: remoteSSHTarget.trim(),
            })
          }
        }
      })()
      let currentSession = await startRemoteDeploySession(session.id, {
        tailscaleAuthKey: remoteDeployMethod === 'tailscale' && remoteTailscaleAuthMode === 'key' ? remoteTailscaleAuthKey.trim() : '',
        syncVaultPassword: syncEnabled && hostVaultEnabled ? syncVaultPassword.trim() : '',
      })
      latestRemoteSession = currentSession
      await stopLaunchProgressPoll()
      await refreshRemoteSessions({ sessionID: session.id })
      setStatus(remoteLaunchStatusMessage(currentSession, remoteDeployMethod)
        || `Remote child started. Waiting for the child to enroll back over ${remoteDeployMethod === 'tailscale' ? 'Tailscale' : 'LAN / WireGuard'}…`)

      const startedAt = Date.now()
      const timeoutMs = 5 * 60 * 1000
      while (Date.now() - startedAt < timeoutMs) {
        const sessions = await refreshRemoteSessions({ sessionID: session.id })
        currentSession = sessions.find((item) => item.id === session.id) ?? currentSession
        latestRemoteSession = currentSession
        const nextStatus = remoteLaunchStatusMessage(currentSession, remoteDeployMethod)
        if (nextStatus) {
          setStatus(nextStatus)
        }
        if (currentSession.enrollment_id || currentSession.child_swarm_id) {
          break
        }
        await new Promise((resolve) => window.setTimeout(resolve, REMOTE_LAUNCH_POLL_INTERVAL_MS))
      }
      if (!currentSession.enrollment_id && !currentSession.child_swarm_id) {
        throw new Error(currentSession.last_error || 'Remote child did not enroll before timeout')
      }

      setStatus(`Remote child ${currentSession.name} is waiting for approval on the main swarm…`)
      currentSession = await approveRemoteDeploySession(currentSession.id)
      await refreshRemoteSessions({ sessionID: currentSession.id })
      await finishSuccess(`Added remote child ${currentSession.name} to the swarm.`)
    } catch (err) {
      await stopLaunchProgressPoll()
      const startDetails = remoteDeployStartError(err)
      latestRemoteSession = startDetails?.session ?? latestRemoteSession
      logAddSwarmError('remote launch flow failed', err, {
        swarm_name: swarmName.trim(),
        ssh_target: remoteSSHTarget.trim(),
        group_id: group.group.id,
      })
      setError(remoteLaunchFailureMessage(err, latestRemoteSession))
    } finally {
      await stopLaunchProgressPoll()
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
  const availabilityChipClassName = (active: boolean) => (
    `inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-xs font-medium transition ${active ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_9%,var(--app-surface))] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-transparent text-[var(--app-text-muted)]'}`
  )

  const swarmNameCard = (
    <Card className={sectionClassName}>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(220px,320px)] sm:items-end">
        <div className="grid gap-1">
          <div className="text-sm font-semibold text-[var(--app-text)]">Name this swarm</div>
          <div className="text-xs text-[var(--app-text-muted)]">First decision: the child swarm name shown in this group after launch.</div>
        </div>
        <div className="grid gap-2">
          <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Child swarm name</label>
          <Input
            data-testid="add-swarm-child-name"
            value={swarmName}
            onChange={(event) => {
              invalidateRemotePreflight()
              setSwarmName(event.target.value)
            }}
            disabled={submitting}
            placeholder={suggestedSwarmName}
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
              disabled={submitting || validatingPackage || suggestingPackages || remotePreflightLoading}
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
          disabled={submitting || validatingPackage || remotePreflightLoading}
        />
        <Button type="button" variant="secondary" onClick={() => void addPackage()} disabled={submitting || validatingPackage || remotePreflightLoading}>
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
              <h2 className="text-xl font-semibold text-[var(--app-text)]">Add swarm</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                Configure a child swarm in one compact flow.
              </p>
            </div>
            <Badge tone={activeRuntimeLabel ? 'live' : 'warning'}>
              {activeRuntimeLabel ? `${activeRuntimeLabel} ready` : 'runtime required'}
            </Badge>
          </div>
        </div>

        <div className={bodyClassName}>
          {loading ? (
            <Card className="flex items-center gap-3 p-4 text-sm text-[var(--app-text-muted)]">
              <Loader2 size={16} className="animate-spin" />
              <span>Loading Add Swarm options…</span>
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

          {swarmNameCard}

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
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">Launch here and replicate selected workspaces into a child swarm.</div>
                  </div>
                  {launchTarget === 'local' ? <Check size={16} className="shrink-0 text-[var(--app-primary)]" /> : null}
                </div>
              </button>

              <button
                type="button"
                data-testid="add-swarm-target-remote"
                className={optionClassName(launchTarget === 'remote', true)}
                onClick={() => setLaunchTarget('remote')}
                disabled={submitting}
              >
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <div className="text-sm font-semibold text-[var(--app-text)]">Remote over SSH</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">Ship selected workspaces and config over SSH, then attach back through the selected transport.</div>
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
                    Choose which local container runtime should launch the replicated child swarm.
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
                  <div className="text-sm font-semibold text-[var(--app-text)]">Remote deploy method</div>
                  <div className="text-xs text-[var(--app-text-muted)]">
                    Choose how the remote child connects back. Preflight runs from the footer when you are ready.
                  </div>
                </div>

                <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_180px] sm:items-end">
                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">SSH alias or target</label>
                    <Input
                      data-testid="add-swarm-ssh-target"
                      value={remoteSSHTarget}
                      onChange={(event) => {
                        setRemoteSSHTarget(event.target.value)
                        setRemotePreflightSession(null)
                        setRemotePreflightError(null)
                        setRemotePreflightGuidance(null)
                      }}
                      disabled={submitting || remotePreflightLoading}
                      placeholder="user@host or ssh-config alias"
                    />
                  </div>
                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Remote runtime</label>
                    <Select
                      data-testid="add-swarm-remote-runtime"
                      value={remoteRuntimeChoice}
                      onChange={(event) => {
                        setRemoteRuntimeChoice((event.target.value === 'podman' ? 'podman' : 'docker'))
                        setRemotePreflightSession(null)
                        setRemotePreflightError(null)
                        setRemotePreflightGuidance(null)
                      }}
                      disabled={submitting || remotePreflightLoading}
                    >
                      <option value="docker">Docker</option>
                      <option value="podman">Podman</option>
                    </Select>
                  </div>
                </div>

                <div className="grid gap-3 sm:grid-cols-2">
                  {[
                    {
                      id: 'tailscale' as const,
                      title: 'SSH + Tailscale',
                      text: 'Prepare the SSH/Tailscale remote image here, then the child calls back over the master tailnet URL.',
                    },
                    {
                      id: 'lan' as const,
                      title: 'SSH + LAN / WireGuard',
                      text: 'Prepare the SSH/LAN remote image here, then the child calls back over a reachable host or private IP.',
                    },
                  ].map((option) => (
                    <button
                      key={option.id}
                      type="button"
                      data-testid={`add-swarm-method-${option.id}`}
                      onClick={() => {
                        setRemoteDeployMethod(option.id)
                        setRemotePreflightSession(null)
                        setRemotePreflightError(null)
                        setRemotePreflightGuidance(null)
                      }}
                      className={optionClassName(remoteDeployMethod === option.id)}
                      disabled={submitting || remotePreflightLoading}
                    >
                      <div className="flex items-start justify-between gap-3">
                        <div>
                          <div className="text-sm font-semibold text-[var(--app-text)]">{option.title}</div>
                          <div className="mt-1 text-xs text-[var(--app-text-muted)]">{option.text}</div>
                        </div>
                        {remoteDeployMethod === option.id ? <Check size={16} className="shrink-0 text-[var(--app-primary)]" /> : null}
                      </div>
                    </button>
                  ))}
                </div>

                {savedRemoteSSHTargets.length > 0 ? (
                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Saved targets</label>
                    <Select
                      value={savedRemoteSSHTargets.includes(remoteSSHTarget.trim()) ? remoteSSHTarget.trim() : ''}
                      onChange={(event) => {
                        setRemoteSSHTarget(event.target.value)
                        setRemotePreflightSession(null)
                        setRemotePreflightError(null)
                        setRemotePreflightGuidance(null)
                      }}
                      disabled={submitting || remotePreflightLoading}
                    >
                      <option value="">Choose a saved SSH target</option>
                      {savedRemoteSSHTargets.map((target) => (
                        <option key={target} value={target}>{target}</option>
                      ))}
                    </Select>
                  </div>
                ) : null}

                {remoteDeployMethod === 'tailscale' ? (
                  <div className={`grid gap-3 ${subtlePanelClassName}`}>
                    <div className="grid gap-1">
                      <div className="text-sm font-semibold text-[var(--app-text)]">Tailscale login</div>
                      <div className="text-xs text-[var(--app-text-muted)]">
                        Choose manual browser approval or a launch-only auth key. The raw key is used only for this launch and is not saved by Swarm.
                      </div>
                    </div>
                    <div className="grid gap-2 sm:max-w-xs">
                      <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Login mode</label>
                      <Select
                        data-testid="add-swarm-tailscale-login-mode"
                        value={remoteTailscaleAuthMode}
                        onChange={(event) => setRemoteTailscaleAuthMode(event.target.value === 'key' ? 'key' : 'manual')}
                        disabled={submitting || remotePreflightLoading}
                      >
                        <option value="manual">Manual URL</option>
                        <option value="key">Tailscale auth key</option>
                      </Select>
                    </div>
                    {remoteTailscaleAuthMode === 'key' ? (
                      <div className="grid gap-2">
                        <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Launch-only auth key</label>
                        <Input
                          data-testid="add-swarm-tailscale-auth-key"
                          type="password"
                          value={remoteTailscaleAuthKey}
                          onChange={(event) => setRemoteTailscaleAuthKey(event.target.value)}
                          disabled={submitting || remotePreflightLoading}
                          placeholder="tskey-..."
                        />
                        <div className="text-xs text-[var(--app-text-muted)]">
                          Use a reusable short-lived Tailscale key for multi-launch testing. Swarm does not store the raw key in Pebble, startup config, or artifacts.
                        </div>
                      </div>
                    ) : (
                      <div className="text-xs text-[var(--app-text-muted)]">
                        Manual mode will show the child&apos;s Tailscale login URL after launch so you can approve it in the browser.
                      </div>
                    )}
                  </div>
                ) : (
                  <div className={`grid gap-3 ${subtlePanelClassName}`}>
                    <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Remote reachable host</label>
                    <Input
                      data-testid="add-swarm-remote-reachable-host"
                      value={remoteReachableHost}
                      onChange={(event) => {
                        setRemoteReachableHost(event.target.value)
                        setRemotePreflightSession(null)
                        setRemotePreflightError(null)
                        setRemotePreflightGuidance(null)
                      }}
                      disabled={submitting || remotePreflightLoading}
                      placeholder={remoteReachableHostCandidate(remoteSSHTarget) || '10.0.0.12'}
                    />
                    <div className="text-xs text-[var(--app-text-muted)]">
                      Enter the remote machine&apos;s LAN, WireGuard, or tunnel host/IP that this master should use after SSH install. Do not use an SSH alias unless other machines can resolve it too.
                    </div>
                    {remoteReachableHostSuggestions.length > 0 ? (
                      <div className="grid gap-2">
                        <div className="text-xs text-[var(--app-text-muted)]">Detected on the remote host during preflight:</div>
                        <div className="flex flex-wrap gap-2">
                          {remoteReachableHostSuggestions.map((candidate) => {
                            const selected = candidate === remoteReachableHost.trim()
                            return (
                              <button
                                key={candidate}
                                type="button"
                                className={`rounded-md border px-2 py-1 text-xs transition ${
                                  selected
                                    ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]'
                                    : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                                }`}
                                onClick={() => {
                                  setRemoteReachableHost(candidate)
                                  if (remotePreflightSession?.remote_advertise_host?.trim() !== candidate) {
                                    setRemotePreflightSession(null)
                                  }
                                  setRemotePreflightError(null)
                                  setRemotePreflightGuidance(null)
                                }}
                                disabled={submitting || remotePreflightLoading}
                              >
                                {candidate}
                              </button>
                            )
                          })}
                        </div>
                      </div>
                    ) : null}
                    {masterLANCallbackGuidance ? (
                      <div
                        className={`rounded-2xl border p-4 text-sm ${
                          masterLANCallbackGuidance.blocking
                            ? 'border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] text-[var(--app-warning-text)]'
                            : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]'
                        }`}
                      >
                        <div className="font-medium text-[var(--app-text)]">{masterLANCallbackGuidance.title}</div>
                        <ul className="mt-3 list-disc space-y-2 pl-5">
                          {masterLANCallbackGuidance.details.map((detail) => (
                            <li key={detail}>{detail}</li>
                          ))}
                        </ul>
                      </div>
                    ) : null}
                  </div>
                )}

                <div className={`${subtlePanelClassName} text-sm text-[var(--app-text-muted)]`}>
                  <div className="font-medium text-[var(--app-text)]">Preflight summary shown before execution</div>
                  <ul className="mt-3 list-disc space-y-2 pl-5">
                    <li>This will send only Git-tracked files from the selected workspace roots and any linked directories to the remote server as payload archives.</li>
                    <li>This will also have the remote host pull the published Swarm remote image for the selected SSH transport when that image is not already present there.</li>
                    <li>The remote install path copies: rendered <code>swarm.conf</code>, installer script, and Git-tracked workspace payload archives.</li>
                    <li>The launched remote child is configured to call back to this master over the selected transport endpoint.</li>
                    <li>Persistence is not installed by Swarm in this path. Reboot survival is up to the remote machine owner.</li>
                    <li>Attach flow: remote child must come back over the selected transport, then you explicitly approve it here.</li>
                  </ul>
                </div>
              </Card>

              {workspaceSelectionCard}

              {remotePreflightError ? (
                <div className="rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">
                  <div className="font-medium">{remotePreflightGuidance?.title || 'Remote preflight failed'}</div>
                  {remotePreflightGuidance?.details?.length ? (
                    <ul className="mt-3 list-disc space-y-2 pl-5 text-[var(--app-text)]">
                      {remotePreflightGuidance.details.map((detail) => (
                        <li key={detail}>{detail}</li>
                      ))}
                    </ul>
                  ) : (
                    <div className="mt-2">{remotePreflightError}</div>
                  )}
                  {remotePreflightGuidance?.commands?.length ? (
                    <div className="mt-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs text-[var(--app-text)]">
                      <div className="font-medium text-[var(--app-text)]">Suggested commands</div>
                      <pre className="mt-2 overflow-x-auto whitespace-pre-wrap">{remotePreflightGuidance.commands.join('\n')}</pre>
                    </div>
                  ) : null}
                </div>
              ) : null}

            </>
          ) : null}

          <Card className={sectionClassName}>
            <div className="flex items-start justify-between gap-3">
              <div className="grid gap-1">
                <div className="text-sm font-semibold text-[var(--app-text)]">Ready Check</div>
                <div className="text-xs text-[var(--app-text-muted)]">
                  Smart defaults are selected. Adjust only what you need before launch.
                </div>
              </div>
              <Badge tone={launchTarget === 'remote' && !remotePreflightSession ? 'warning' : 'live'}>
                {launchTarget === 'remote'
                  ? (remotePreflightSession ? 'preflight passed' : 'preflight next')
                  : (runtimeChoice ? 'ready' : 'runtime required')}
              </Badge>
            </div>

            <div className="grid gap-2 sm:grid-cols-3">
              <button
                type="button"
                role="switch"
                aria-checked={alwaysOn}
                data-testid="add-swarm-always-on"
                className={optionClassName(alwaysOn)}
                onClick={() => {
                  if (!submitting && !remotePreflightLoading) {
                    invalidateRemotePreflight()
                    setAlwaysOn((current) => !current)
                  }
                }}
                disabled={submitting || remotePreflightLoading}
              >
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="text-sm font-semibold text-[var(--app-text)]">Always On</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                      {alwaysOn ? 'Restart attached children on host startup.' : 'Manual start after host restart.'}
                    </div>
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
                  if (!submitting && !remotePreflightLoading) {
                    invalidateRemotePreflight()
                    setSyncEnabled((current) => !current)
                  }
                }}
                disabled={submitting || remotePreflightLoading}
              >
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="text-sm font-semibold text-[var(--app-text)]">Swarm Sync</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                      {syncEnabled ? 'Credentials, agents, and tools sync.' : 'Standalone child auth.'}
                    </div>
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
                  if (!submitting && !remotePreflightLoading) {
                    invalidateRemotePreflight()
                    setBypassPermissions((current) => !current)
                  }
                }}
                disabled={submitting || remotePreflightLoading}
              >
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="text-sm font-semibold text-[var(--app-text)]">Permissions</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">
                      {bypassPermissions ? 'Bypass enabled at launch.' : 'Prompt when needed.'}
                    </div>
                  </div>
                  {bypassPermissions ? <Check size={15} className="shrink-0 text-[var(--app-primary)]" /> : null}
                </div>
              </button>
            </div>

            {syncEnabled ? (
              <div className="flex flex-wrap items-center gap-2">
                <button type="button" className={availabilityChipClassName(true)} disabled={submitting || remotePreflightLoading}>Credentials</button>
                <button
                  type="button"
                  className={availabilityChipClassName(syncAgentsEnabled)}
                  onClick={() => {
                    if (!submitting && !remotePreflightLoading) {
                      invalidateRemotePreflight()
                      setSyncAgentsEnabled((current) => !current)
                    }
                  }}
                  disabled={submitting || remotePreflightLoading}
                >
                  Agents
                </button>
                <button
                  type="button"
                  className={availabilityChipClassName(syncCustomToolsEnabled)}
                  onClick={() => {
                    if (!submitting && !remotePreflightLoading) {
                      invalidateRemotePreflight()
                      setSyncCustomToolsEnabled((current) => !current)
                    }
                  }}
                  disabled={submitting || remotePreflightLoading}
                >
                  Custom tools
                </button>
                {hostVaultEnabled ? (
                  <Input
                    data-testid="add-swarm-sync-vault-password"
                    className="min-w-[220px] flex-1"
                    type="password"
                    value={syncVaultPassword}
                    onChange={(event) => setSyncVaultPassword(event.target.value)}
                    placeholder="Vault password required for sync"
                    disabled={submitting || remotePreflightLoading}
                  />
                ) : null}
              </div>
            ) : null}

            {launchTarget === 'remote' && remotePreflightSession ? (
              <div data-testid="add-swarm-preflight-success" className="rounded-lg border border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-3 text-sm text-[var(--app-success)]">
                <div className="font-medium">Preflight passed</div>
                <div className="mt-1 text-[var(--app-text)]">{remotePreflightSession.preflight.summary || 'Remote host is ready for SSH launch.'}</div>
              </div>
            ) : null}

            <div className="grid gap-2 text-sm text-[var(--app-text-muted)] sm:grid-cols-2">
              {launchTarget === 'remote' ? (
                <>
                  <div><span className="font-medium text-[var(--app-text)]">Target:</span> Remote over SSH</div>
                  <div><span className="font-medium text-[var(--app-text)]">SSH target:</span> {remoteSSHTarget.trim() || 'Required'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Deploy method:</span> {remoteDeployMethod === 'tailscale' ? 'Tailscale' : `LAN / WireGuard via ${remoteReachableHost.trim() || 'Required'}`}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Preflight:</span> {remotePreflightSession ? 'Passed' : (remotePreflightLoading ? 'Running…' : 'Required before launch')}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Runtime:</span> {remotePreflightSession?.preflight.remote_runtime || remoteRuntimeChoice}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Disk:</span> {remotePreflightSession ? `${formatBytes(remotePreflightSession.preflight.remote_disk?.available_bytes)} available / ${formatBytes(remotePreflightSession.preflight.remote_disk?.required_bytes)} required` : 'Unknown until preflight'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Workspaces:</span> {selectedWorkspaceCountValue}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Payload archives:</span> {remotePreflightSession ? remotePreflightArchiveCount : selectedRemoteArchiveCount}</div>
                </>
              ) : (
                <>
                  <div><span className="font-medium text-[var(--app-text)]">Target:</span> Local container</div>
                  <div><span className="font-medium text-[var(--app-text)]">Master:</span> {masterName}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Runtime:</span> {runtimeChoice || 'Unavailable'}</div>
                  <div><span className="font-medium text-[var(--app-text)]">Workspaces:</span> {selectedWorkspaceCountValue}</div>
                </>
              )}
              <div><span className="font-medium text-[var(--app-text)]">Always On:</span> {alwaysOn ? 'Enabled' : 'Disabled'}</div>
              <div><span className="font-medium text-[var(--app-text)]">Swarm Sync:</span> {syncEnabled ? 'Enabled' : 'Disabled'}</div>
              <div><span className="font-medium text-[var(--app-text)]">Bypass permissions:</span> {bypassPermissions ? 'Enabled' : 'Disabled'}</div>
              <div><span className="font-medium text-[var(--app-text)]">Swarm name:</span> {swarmName.trim() || 'Required'}</div>
            </div>
          </Card>
        </div>

        <div className="flex flex-col gap-3 border-t border-[var(--app-border)] px-6 py-5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-sm text-[var(--app-text-muted)]">
            {launchTarget === 'remote'
              ? `${selectedWorkspaceCountValue} selected workspace${selectedWorkspaceCountValue === 1 ? '' : 's'} and ${containerPackages.length} package${containerPackages.length === 1 ? '' : 's'} configured for the remote deploy.`
              : `${selectedWorkspaceCountValue} selected workspace${selectedWorkspaceCountValue === 1 ? '' : 's'} will be replicated using ${runtimeChoice || 'the selected runtime'} with Swarm Sync ${syncEnabled ? 'enabled' : 'disabled'}.`}
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
                  : !swarmName.trim()
                    || !remoteSSHTarget.trim()
                    || remotePreflightLoading
                    || configuringMasterLANCallback
                    || (remotePreflightBlocked && !remotePreflightCanAutofill)
                    || (remoteDeployMethod === 'lan' && !remoteReachableHost.trim()))
              }
            >
              {(submitting || remotePreflightLoading || configuringMasterLANCallback) ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
              {submitting
                ? 'Working…'
                : launchTarget === 'remote'
                  ? (configuringMasterLANCallback
                      ? 'Saving this machine’s address…'
                      : remotePreflightLoading
                        ? 'Checking…'
                        : remotePreflightSession
                          ? 'Launch and add'
                          : 'Preflight when Ready')
                  : 'Launch and add'}
            </Button>
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
