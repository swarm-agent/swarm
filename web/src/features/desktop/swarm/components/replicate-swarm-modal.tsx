import { useEffect, useMemo, useState } from 'react'
import { Check, Copy, FolderTree, Globe, Loader2, RefreshCcw, Shield } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { Select } from '../../../../components/ui/select'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { fetchSwarmLocalRuntimeStatus, type SwarmLocalRuntimeStatus } from '../../onboarding/api'
import { hydrateReplicationWorkspaces, replicateSwarm, ReplicateSwarmLaunchError } from '../api/replicate-swarm'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { DesktopOnboardingStatus } from '../../onboarding/types'
import { useDesktopStore } from '../../state/use-desktop-store'

interface ReplicateSwarmModalProps {
  open: boolean
  onboardingStatus: DesktopOnboardingStatus | null
  onOpenChange: (open: boolean) => void
  onComplete: (message: string) => Promise<void> | void
}

type ReplicateTargetMode = 'local' | 'remote'
type ReplicationMode = 'bundle' | 'copy'

interface ReplicateWorkspaceDraft {
  workspacePath: string
  selected: boolean
  writable: boolean
  replicationMode: ReplicationMode
  defaultReplicationMode: ReplicationMode
  workspaceName: string
  directories: string[]
}

const FALLBACK_RUNTIME_STATUS: SwarmLocalRuntimeStatus = {
  recommended: '',
  available: [],
  installed: [],
  issues: {},
  warning: 'Could not detect local container runtime.',
}

function suggestedReplicatedSwarmName(onboardingStatus: DesktopOnboardingStatus | null): string {
  const base = onboardingStatus?.config.swarmName?.trim() || 'Swarm'
  return `${base} replicate`
}

function inferReplicationMode(workspace: WorkspaceEntry): ReplicationMode {
  if (workspace.isGitRepo) {
    return 'bundle'
  }
  return 'copy'
}

function buildWorkspaceDrafts(workspaces: WorkspaceEntry[]): ReplicateWorkspaceDraft[] {
  return workspaces.map((workspace) => {
    const defaultReplicationMode = inferReplicationMode(workspace)
    return {
      workspacePath: workspace.path,
      selected: false,
      writable: true,
      replicationMode: defaultReplicationMode,
      defaultReplicationMode,
      workspaceName: workspace.workspaceName || workspace.path.split('/').filter(Boolean).pop() || 'workspace',
      directories: workspace.directories,
    }
  })
}

function selectedWorkspaceCount(items: ReplicateWorkspaceDraft[]): number {
  return items.filter((item) => item.selected).length
}

export function ReplicateSwarmModal({ open, onboardingStatus, onOpenChange, onComplete }: ReplicateSwarmModalProps) {
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [targetMode, setTargetMode] = useState<ReplicateTargetMode>('local')
  const [swarmName, setSwarmName] = useState('')
  const [workspaceDrafts, setWorkspaceDrafts] = useState<ReplicateWorkspaceDraft[]>([])
  const [runtimeStatus, setRuntimeStatus] = useState<SwarmLocalRuntimeStatus>(FALLBACK_RUNTIME_STATUS)
  const [selectedRuntime, setSelectedRuntime] = useState<'podman' | 'docker' | ''>('')
  const [syncEnabled, setSyncEnabled] = useState(true)
  const [syncMode, setSyncMode] = useState('managed')
  const [syncAgentsEnabled, setSyncAgentsEnabled] = useState(true)
  const [syncCustomToolsEnabled, setSyncCustomToolsEnabled] = useState(true)
  const [syncSkillsEnabled, setSyncSkillsEnabled] = useState(true)
  const [syncVaultPassword, setSyncVaultPassword] = useState('')
  const [bypassPermissions, setBypassPermissions] = useState(false)

  const vault = useDesktopStore((state) => state.vault)
  const suggestedName = useMemo(() => suggestedReplicatedSwarmName(onboardingStatus), [onboardingStatus])
  const selectedCount = useMemo(() => selectedWorkspaceCount(workspaceDrafts), [workspaceDrafts])
  const hostVaultEnabled = Boolean(vault.enabled)
  const runtimeChoice = useMemo(
    () => (selectedRuntime && runtimeStatus.available.includes(selectedRuntime) ? selectedRuntime : runtimeStatus.recommended || ''),
    [runtimeStatus, selectedRuntime],
  )

  useEffect(() => {
    if (!open) {
      return
    }
    let cancelled = false
    setLoading(true)
    setSubmitting(false)
    setError(null)
    setTargetMode('local')
    setSwarmName(suggestedName)
    setRuntimeStatus(FALLBACK_RUNTIME_STATUS)
    setSelectedRuntime('')
    setSyncEnabled(true)
    setSyncMode('managed')
    setSyncAgentsEnabled(true)
    setSyncCustomToolsEnabled(true)
    setSyncSkillsEnabled(true)
    setSyncVaultPassword('')
    setBypassPermissions(false)
    void Promise.all([
      hydrateReplicationWorkspaces(),
      fetchSwarmLocalRuntimeStatus().catch(() => FALLBACK_RUNTIME_STATUS),
    ])
      .then(([workspaces, nextRuntimeStatus]) => {
        if (cancelled) {
          return
        }
        setWorkspaceDrafts(buildWorkspaceDrafts(workspaces))
        setRuntimeStatus(nextRuntimeStatus)
        setSelectedRuntime((nextRuntimeStatus.recommended || '') as 'podman' | 'docker' | '')
      })
      .catch((err) => {
        if (cancelled) {
          return
        }
        setWorkspaceDrafts([])
        setError(err instanceof Error ? err.message : 'Failed to load workspaces')
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })
    return () => {
      cancelled = true
    }
  }, [open, suggestedName])

  if (!open) {
    return null
  }

  const closeModal = () => {
    if (submitting) {
      return
    }
    onOpenChange(false)
  }

  const handleSubmit = async () => {
    if (submitting) {
      return
    }
    if (targetMode !== 'local') {
      setError('Remote replication is not implemented yet.')
      return
    }
    if (!swarmName.trim()) {
      setError('Child swarm name is required.')
      return
    }
    if (!runtimeChoice) {
      setError(runtimeStatus.warning || 'No supported local runtime is available.')
      return
    }
    const selected = workspaceDrafts.filter((item) => item.selected)
    if (selected.length === 0) {
      setError('Select at least one workspace to replicate.')
      return
    }
    if (syncEnabled && hostVaultEnabled && !syncVaultPassword.trim()) {
      setError('Vault password is required to sync from a vaulted host.')
      return
    }

    setSubmitting(true)
    setError(null)
    try {
      const syncModules = [
        'credentials',
        ...(syncAgentsEnabled ? ['agents'] : []),
        ...(syncCustomToolsEnabled ? ['custom_tools'] : []),
        ...(syncSkillsEnabled ? ['skills'] : []),
      ]
      const result = await replicateSwarm({
        mode: 'local',
        swarmName: swarmName.trim(),
        runtime: runtimeChoice,
        bypassPermissions,
        sync: {
          enabled: syncEnabled,
          mode: syncMode,
          modules: syncEnabled ? syncModules : [],
          vaultPassword: syncEnabled && hostVaultEnabled ? syncVaultPassword.trim() : '',
        },
        workspaces: selected.map((item) => ({
          sourceWorkspacePath: item.workspacePath,
          replicationMode: item.replicationMode,
          writable: item.writable,
        })),
      })
      await onComplete(`Replicated ${result.swarm.name || swarmName.trim()} with ${result.workspaces.length} workspace${result.workspaces.length === 1 ? '' : 's'}.`)
      onOpenChange(false)
    } catch (err) {
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
        setError([details.error || 'Failed to replicate swarm', ...guidance].filter(Boolean).join('\n'))
      } else {
        setError(err instanceof Error ? err.message : 'Failed to replicate swarm')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog>
      <DialogBackdrop onClick={closeModal} />
      <DialogPanel className="mx-auto mt-[8vh] flex w-[min(880px,calc(100vw-24px))] max-w-[880px] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(880px,calc(100vw-48px))]">
        <div className="border-b border-[var(--app-border)] px-6 py-5">
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="flex items-center gap-2">
                <Badge tone="neutral">Separate flow</Badge>
                <Badge tone="live">Replicate Swarm</Badge>
              </div>
              <h2 className="mt-3 text-xl font-semibold text-[var(--app-text)]">Replicate a child swarm from existing workspaces</h2>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                This is a dedicated replication flow. It prepares a child swarm using the existing container primitives,
                then provisions selected workspaces into it with sync and access settings.
              </p>
            </div>
            <ModalCloseButton onClick={closeModal} aria-label="Close replicate swarm dialog" />
          </div>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-6 py-6">
          <Card className="border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
            <div className="font-medium text-[var(--app-text)]">Planned replication flow</div>
            <div className="mt-3 grid gap-3 md:grid-cols-3">
              <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
                <div className="flex items-center gap-2 text-sm font-medium text-[var(--app-text)]">
                  <FolderTree size={14} />
                  Select workspaces
                </div>
                <p className="mt-2 text-xs text-[var(--app-text-muted)]">Choose which host workspaces should be provisioned into the child swarm.</p>
              </div>
              <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
                <div className="flex items-center gap-2 text-sm font-medium text-[var(--app-text)]">
                  <RefreshCcw size={14} />
                  Configure sync + access
                </div>
                <p className="mt-2 text-xs text-[var(--app-text-muted)]">Replication mode, writable access, and swarm sync stay visible here.</p>
              </div>
              <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
                <div className="flex items-center gap-2 text-sm font-medium text-[var(--app-text)]">
                  <Copy size={14} />
                  Launch ready child swarm
                </div>
                <p className="mt-2 text-xs text-[var(--app-text-muted)]">The child swarm should start already provisioned and linked back to the host workspace records.</p>
              </div>
            </div>
          </Card>

          {error ? (
            <Card className="border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">
              {error}
            </Card>
          ) : null}

          <div>
            <div className="text-sm font-medium text-[var(--app-text)]">Replication target</div>
            <div className="mt-3 grid gap-3 md:grid-cols-2">
              <button
                type="button"
                onClick={() => setTargetMode('local')}
                className={`rounded-2xl border p-4 text-left transition ${targetMode === 'local' ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}`}
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2 text-sm font-medium text-[var(--app-text)]">
                    <Copy size={14} />
                    Local
                  </div>
                  <Badge tone="live">Available</Badge>
                </div>
                <p className="mt-2 text-xs text-[var(--app-text-muted)]">Reuse the existing local child swarm container path and provision workspaces on this machine.</p>
              </button>
              <button
                type="button"
                disabled
                className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-left opacity-70"
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2 text-sm font-medium text-[var(--app-text)]">
                    <Globe size={14} />
                    Remote
                  </div>
                  <Badge tone="warning">Not yet wired</Badge>
                </div>
                <p className="mt-2 text-xs text-[var(--app-text-muted)]">Remote stays visible in the product flow, but remains unavailable until the remote orchestration slice is implemented.</p>
              </button>
            </div>
          </div>

          <div className="grid gap-6 lg:grid-cols-[minmax(0,1.5fr)_minmax(320px,1fr)]">
            <div className="space-y-6">
              <div>
                <label className="text-sm font-medium text-[var(--app-text)]" htmlFor="replicate-swarm-name">
                  Child swarm name
                </label>
                <Input
                  id="replicate-swarm-name"
                  value={swarmName}
                  onChange={(event) => setSwarmName(event.target.value)}
                  className="mt-3"
                  placeholder="Replicated swarm name"
                />
              </div>

              <div>
                <div className="flex items-center justify-between gap-2">
                  <div>
                    <div className="text-sm font-medium text-[var(--app-text)]">Workspaces</div>
                    <p className="mt-1 text-xs text-[var(--app-text-muted)]">
                      Git workspaces default to bundle; non-git workspaces default to full copy. Backend remains the source of truth.
                    </p>
                  </div>
                  <Badge tone={selectedCount > 0 ? 'live' : 'neutral'}>{selectedCount} selected</Badge>
                </div>

                <div className="mt-3 grid gap-3">
                  {loading ? (
                    <Card className="border-dashed p-5 text-sm text-[var(--app-text-muted)]">Loading workspaces…</Card>
                  ) : workspaceDrafts.length === 0 ? (
                    <Card className="border-dashed p-5 text-sm text-[var(--app-text-muted)]">No workspaces available yet.</Card>
                  ) : (
                    workspaceDrafts.map((workspace) => {
                      const checked = workspace.selected
                      return (
                        <label
                          key={workspace.workspacePath}
                          className={`block rounded-2xl border p-4 transition ${checked ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}`}
                        >
                          <div className="flex items-start gap-3">
                            <input
                              type="checkbox"
                              className="mt-1 h-4 w-4 rounded border-[var(--app-border)]"
                              checked={checked}
                              onChange={(event) => {
                                const nextChecked = event.target.checked
                                setWorkspaceDrafts((current) => current.map((item) => (
                                  item.workspacePath === workspace.workspacePath
                                    ? { ...item, selected: nextChecked }
                                    : item
                                )))
                              }}
                            />
                            <div className="min-w-0 flex-1">
                              <div className="flex flex-wrap items-center gap-2">
                                <div className="truncate text-sm font-semibold text-[var(--app-text)]">{workspace.workspaceName}</div>
                                <Badge tone={workspace.defaultReplicationMode === 'bundle' ? 'live' : 'neutral'}>
                                  default {workspace.defaultReplicationMode}
                                </Badge>
                                {checked ? (
                                  <Badge tone="live">
                                    <Check size={12} />
                                    ready
                                  </Badge>
                                ) : null}
                              </div>
                              <div className="mt-1 break-all text-xs text-[var(--app-text-muted)]">{workspace.workspacePath}</div>
                              {workspace.directories.length > 1 ? (
                                <div className="mt-2 text-xs text-[var(--app-text-muted)]">
                                  Includes {workspace.directories.length} linked directories.
                                </div>
                              ) : null}

                              {checked ? (
                                <div className="mt-4 grid gap-3 md:grid-cols-2">
                                  <div>
                                    <div className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Replication mode</div>
                                    <Select
                                      value={workspace.replicationMode}
                                      onChange={(event) => {
                                        const nextMode = event.target.value as ReplicationMode
                                        setWorkspaceDrafts((current) => current.map((item) => (
                                          item.workspacePath === workspace.workspacePath
                                            ? { ...item, replicationMode: nextMode }
                                            : item
                                        )))
                                      }}
                                      className="mt-2"
                                    >
                                      <option value="bundle">Git bundle</option>
                                      <option value="copy">Full workspace copy</option>
                                    </Select>
                                  </div>
                                  <div>
                                    <div className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Workspace access</div>
                                    <button
                                      type="button"
                                      onClick={() => {
                                        setWorkspaceDrafts((current) => current.map((item) => (
                                          item.workspacePath === workspace.workspacePath
                                            ? { ...item, writable: !item.writable }
                                            : item
                                        )))
                                      }}
                                      className={`mt-2 inline-flex h-10 items-center rounded-xl border px-4 text-sm font-medium transition ${workspace.writable ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]'}`}
                                    >
                                      {workspace.writable ? 'Read / Write' : 'Read only'}
                                    </button>
                                  </div>
                                </div>
                              ) : null}
                            </div>
                          </div>
                        </label>
                      )
                    })
                  )}
                </div>
              </div>
            </div>

            <div className="space-y-6">
              <Card className="border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                <div className="text-sm font-medium text-[var(--app-text)]">Local runtime</div>
                <p className="mt-1 text-xs text-[var(--app-text-muted)]">Choose which local container runtime should launch the replicated child swarm.</p>
                <div className="mt-4 grid gap-3 sm:grid-cols-2">
                  {(['podman', 'docker'] as const).map((runtime) => {
                    const available = runtimeStatus.available.includes(runtime)
                    const installed = runtimeStatus.installed.includes(runtime)
                    const issue = runtimeStatus.issues[runtime]?.trim() || ''
                    const active = runtimeChoice === runtime
                    return (
                      <button
                        key={runtime}
                        type="button"
                        className={`rounded-2xl border p-4 text-left transition ${active ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'} ${available ? '' : 'opacity-60'}`}
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
                                  : `Install ${runtime} to use it for replicate.`}
                            </div>
                          </div>
                          {active ? <Check size={16} className="shrink-0 text-[var(--app-primary)]" /> : null}
                        </div>
                      </button>
                    )
                  })}
                </div>
                {!runtimeChoice && runtimeStatus.warning ? (
                  <div className="mt-3 rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-xs text-[var(--app-danger)]">
                    {runtimeStatus.warning}
                  </div>
                ) : null}
              </Card>

              <Card className="border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                <div className="text-sm font-medium text-[var(--app-text)]">Swarm sync</div>
                <p className="mt-1 text-xs text-[var(--app-text-muted)]">Sync stays explicit in the replication flow and defaults on.</p>
                <div className="mt-4 flex items-center justify-between gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3">
                  <div>
                    <div className="text-sm font-medium text-[var(--app-text)]">Enable sync</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">The child swarm should inherit sync configuration when supported.</div>
                  </div>
                  <button
                    type="button"
                    onClick={() => setSyncEnabled((current) => !current)}
                    className={`relative inline-flex h-7 w-12 shrink-0 items-center rounded-full border transition ${syncEnabled ? 'border-[var(--app-primary)] bg-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}`}
                    aria-checked={syncEnabled}
                    role="switch"
                  >
                    <span className={`inline-block h-5 w-5 rounded-full bg-white shadow transition ${syncEnabled ? 'translate-x-6' : 'translate-x-1'}`} />
                  </button>
                </div>
                <div className="mt-4">
                  <div className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Sync mode</div>
                  <Select value={syncMode} onChange={(event) => setSyncMode(event.target.value)} className="mt-2" disabled={!syncEnabled}>
                    <option value="managed">Managed</option>
                  </Select>
                </div>
                {syncEnabled ? (
                  <div className="mt-4 grid gap-3 md:grid-cols-3">
                    <button
                      type="button"
                      onClick={() => setSyncAgentsEnabled((current) => !current)}
                      className={`rounded-2xl border px-4 py-3 text-left text-sm transition ${syncAgentsEnabled ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]'}`}
                    >
                      <div className="font-medium">Sync agents</div>
                      <div className="mt-1 text-xs text-[var(--app-text-muted)]">Mirror saved agent profiles into the child.</div>
                    </button>
                    <button
                      type="button"
                      onClick={() => setSyncCustomToolsEnabled((current) => !current)}
                      className={`rounded-2xl border px-4 py-3 text-left text-sm transition ${syncCustomToolsEnabled ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]'}`}
                    >
                      <div className="font-medium">Sync custom tools</div>
                      <div className="mt-1 text-xs text-[var(--app-text-muted)]">Mirror custom tool definitions alongside agent state.</div>
                    </button>
                    <button
                      type="button"
                      onClick={() => setSyncSkillsEnabled((current) => !current)}
                      className={`rounded-2xl border px-4 py-3 text-left text-sm transition ${syncSkillsEnabled ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]'}`}
                    >
                      <div className="font-medium">Sync skills</div>
                      <div className="mt-1 text-xs text-[var(--app-text-muted)]">Mirror host skills into the child managed skills directory.</div>
                    </button>
                  </div>
                ) : null}
                {syncEnabled && hostVaultEnabled ? (
                  <div className="mt-4">
                    <div className="text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--app-text-muted)]">Vault password</div>
                    <p className="mt-2 text-xs text-[var(--app-text-muted)]">Use the host vault password so the child stores synced credentials in its own vault.</p>
                    <Input
                      className="mt-3"
                      type="password"
                      value={syncVaultPassword}
                      onChange={(event) => setSyncVaultPassword(event.target.value)}
                      placeholder="Vault password"
                      disabled={submitting}
                    />
                  </div>
                ) : null}
              </Card>

              <Card className="border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                <div className="text-sm font-medium text-[var(--app-text)]">Bypass permissions</div>
                <p className="mt-1 text-xs text-[var(--app-text-muted)]">Use this as the permission authority switch. ON: child bypasses prompts and host policy is not mirrored. OFF: host-managed permissions mirror host policy and route approvals through the host.</p>
                <div className="mt-4 flex items-center justify-between gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3">
                  <div>
                    <div className="flex items-center gap-2 text-sm font-medium text-[var(--app-text)]">
                      <Shield size={14} />
                      Enable bypass permissions
                    </div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">Bypass ON makes the child independent; OFF keeps host-managed policy active.</div>
                  </div>
                  <button
                    type="button"
                    onClick={() => setBypassPermissions((current) => !current)}
                    className={`relative inline-flex h-7 w-12 shrink-0 items-center rounded-full border transition ${bypassPermissions ? 'border-[var(--app-primary)] bg-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]'}`}
                    aria-checked={bypassPermissions}
                    role="switch"
                  >
                    <span className={`inline-block h-5 w-5 rounded-full bg-white shadow transition ${bypassPermissions ? 'translate-x-6' : 'translate-x-1'}`} />
                  </button>
                </div>
              </Card>

              <Card className="border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm">
                <div className="font-medium text-[var(--app-text)]">Draft summary</div>
                <div className="mt-3 grid gap-2 text-xs text-[var(--app-text-muted)]">
                  <div>Target: <span className="font-medium text-[var(--app-text)]">{targetMode}</span></div>
                  <div>Child swarm: <span className="font-medium text-[var(--app-text)]">{swarmName.trim() || suggestedName}</span></div>
                  <div>Runtime: <span className="font-medium text-[var(--app-text)]">{runtimeChoice || 'Unavailable'}</span></div>
                  <div>Selected workspaces: <span className="font-medium text-[var(--app-text)]">{selectedCount}</span></div>
                  <div>Sync: <span className="font-medium text-[var(--app-text)]">{syncEnabled ? `enabled (${syncMode})` : 'disabled'}</span></div>
                  <div>Sync modules: <span className="font-medium text-[var(--app-text)]">{syncEnabled ? ['credentials', ...(syncAgentsEnabled ? ['agents'] : []), ...(syncCustomToolsEnabled ? ['custom_tools'] : []), ...(syncSkillsEnabled ? ['skills'] : [])].join(', ') : 'none'}</span></div>
                  <div>Bypass permissions: <span className="font-medium text-[var(--app-text)]">{bypassPermissions ? 'bypass ON; host policy not mirrored' : 'host-managed; host policy mirrored'}</span></div>
                </div>
              </Card>
            </div>
          </div>
        </div>

        <div className="flex items-center justify-between gap-3 border-t border-[var(--app-border)] px-6 py-4">
          <div className="text-xs text-[var(--app-text-muted)]">
            Target: <span className="font-medium text-[var(--app-text)]">{targetMode}</span> · Selected workspaces:{' '}
            <span className="font-medium text-[var(--app-text)]">{selectedCount}</span>
          </div>
          <div className="flex items-center gap-2">
            <Button type="button" variant="outline" onClick={closeModal} disabled={submitting}>
              Close
            </Button>
            <Button type="button" onClick={() => void handleSubmit()} disabled={submitting || loading || selectedCount === 0 || targetMode !== 'local' || !runtimeChoice}>
              {submitting ? <Loader2 className="animate-spin" size={14} /> : null}
              {submitting ? 'Replicating…' : 'Replicate Swarm'}
            </Button>
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
