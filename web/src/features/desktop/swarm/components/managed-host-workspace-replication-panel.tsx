import { useEffect, useMemo, useState } from 'react'
import { CheckCircle2, Folder, GitBranch, HardDrive, Link2, Loader2, RefreshCw, UploadCloud, XCircle } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import {
  fetchManagedWorkspaceInventory,
  preflightManagedWorkspaces,
  replicateManagedWorkspaces,
  type ManagedWorkspaceAction,
  type ManagedWorkspaceInventoryResponse,
  type ManagedWorkspacePlan,
  type ManagedWorkspaceResult,
  type ManagedWorkspaceSelectionInput,
} from '../api/managed-workspace-replication'
import type { SwarmTarget } from '../api/swarm-targets'

interface ManagedHostWorkspaceLinkPanelProps {
  target: SwarmTarget
  busy?: boolean
  onSkip: (message: string) => Promise<void> | void
  onComplete: (message: string) => Promise<void> | void
}

interface WorkspaceDraft {
  workspacePath: string
  selected: boolean
  destinationPath: string
}

type RecommendationAction = ManagedWorkspaceAction | 'unsupported' | 'pending'

interface Recommendation {
  workspacePath: string
  action: RecommendationAction
  destinationPath: string
  reason: string
  matchSource: string
}

function workspaceName(workspace: WorkspaceEntry): string {
  return workspace.workspaceName || workspace.path.split('/').filter(Boolean).pop() || 'workspace'
}

function baseName(path: string): string {
  return path.split('/').filter(Boolean).pop() || path
}

function normalizePath(path: string): string {
  const trimmed = String(path ?? '').trim()
  if (!trimmed) return ''
  return trimmed.replace(/\/+$/g, '') || '/'
}

function joinPath(root: string, relative: string): string {
  const cleanRoot = normalizePath(root)
  const cleanRelative = String(relative ?? '').trim().replace(/^\/+|\/+$/g, '')
  if (!cleanRoot) return cleanRelative
  if (!cleanRelative) return cleanRoot
  return `${cleanRoot}/${cleanRelative}`
}

function homeRelativePath(path: string): string {
  const normalized = normalizePath(path)
  if (!normalized.startsWith('/')) return ''
  const parts = normalized.split('/').filter(Boolean)
  if (parts.length >= 3 && parts[0] === 'home') {
    return parts.slice(2).join('/')
  }
  return ''
}

function defaultDestinationPath(sourcePath: string, destinationRoot: string, inventory: ManagedWorkspaceInventoryResponse | null): string {
  const root = normalizePath(destinationRoot) === '~'
    ? normalizePath(inventory?.managedHome || '')
    : normalizePath(destinationRoot)
  const relative = homeRelativePath(sourcePath) || baseName(sourcePath)
  return joinPath(root, relative)
}

function actionLabel(action: string): string {
  switch (action) {
    case 'import_bundle':
      return 'Transfer new workspace'
    case 'link_existing':
      return 'Link existing workspace'
    case 'conflict':
      return 'Conflict'
    case 'unsupported':
      return 'Unavailable'
    default:
      return action || 'Review'
  }
}

function actionTone(action: string): 'live' | 'warning' | 'neutral' {
  if (action === 'link_existing') return 'live'
  if (action === 'import_bundle') return 'warning'
  return 'neutral'
}

function actionDescription(action: string, targetName: string, destinationPath: string): string {
  if (action === 'link_existing') {
    return `Link only: ${targetName} already has ${destinationPath}. Swarm will register/link that existing directory; it will not mkdir, transfer a git bundle, overwrite files, or delete anything.`
  }
  if (action === 'import_bundle') {
    return `Transfer: ${targetName} does not have this workspace at ${destinationPath}. Swarm will create the destination directory as needed and transfer/import a git bundle. It will not delete existing files.`
  }
  return 'This workspace cannot be linked/imported until the conflict is resolved.'
}

function buildSelections(drafts: WorkspaceDraft[], recommendations: Map<string, Recommendation>): ManagedWorkspaceSelectionInput[] {
  return drafts
    .filter((draft) => draft.selected)
    .map((draft) => ({
      sourceWorkspacePath: draft.workspacePath,
      destinationPath: draft.destinationPath.trim() || recommendations.get(draft.workspacePath)?.destinationPath || undefined,
    }))
}

function buildInventoryRecommendations(input: {
  workspaces: WorkspaceEntry[]
  drafts: WorkspaceDraft[]
  inventory: ManagedWorkspaceInventoryResponse | null
  destinationRoot: string
  preflightPlans: ManagedWorkspacePlan[]
}): Map<string, Recommendation> {
  const output = new Map<string, Recommendation>()
  const inventory = input.inventory
  const remotePaths = new Set<string>()
  const remoteNames = new Map<string, string>()
  if (inventory) {
    for (const workspace of inventory.savedWorkspaces) {
      const path = normalizePath(workspace.path)
      if (path) remotePaths.add(path)
      const name = workspaceName(workspace).toLowerCase()
      if (name && path) remoteNames.set(name, path)
    }
    for (const directory of inventory.discoveredDirectories) {
      const path = normalizePath(directory.path)
      if (path) remotePaths.add(path)
      const name = String(directory.name || baseName(directory.path)).trim().toLowerCase()
      if (name && path) remoteNames.set(name, path)
    }
    for (const cwd of inventory.activeCWDs) {
      const path = normalizePath(cwd.workspacePath || cwd.path)
      if (path) remotePaths.add(path)
      const name = String(cwd.workspaceName || baseName(path)).trim().toLowerCase()
      if (name && path) remoteNames.set(name, path)
    }
  }

  const preflightBySource = new Map(input.preflightPlans.map((plan) => [normalizePath(plan.sourceWorkspacePath), plan]))

  for (const workspace of input.workspaces) {
    const draft = input.drafts.find((item) => item.workspacePath === workspace.path)
    const fallbackDestination = draft?.destinationPath.trim() || defaultDestinationPath(workspace.path, input.destinationRoot, inventory)
    const plan = preflightBySource.get(normalizePath(workspace.path))
    if (!workspace.isGitRepo) {
      output.set(workspace.path, {
        workspacePath: workspace.path,
        action: 'unsupported',
        destinationPath: fallbackDestination,
        reason: 'Only git workspaces can be transferred to a Managed Host.',
        matchSource: '',
      })
      continue
    }
    if (plan) {
      output.set(workspace.path, {
        workspacePath: workspace.path,
        action: (plan.action || 'conflict') as RecommendationAction,
        destinationPath: plan.destinationPath || fallbackDestination,
        reason: plan.error || (plan.action === 'link_existing' ? 'Managed host preflight found an existing git workspace.' : plan.action === 'import_bundle' ? 'Managed host preflight did not find this workspace at the destination.' : ''),
        matchSource: plan.action === 'link_existing' ? 'preflight' : '',
      })
      continue
    }
    const normalizedDestination = normalizePath(fallbackDestination)
    const nameMatch = remoteNames.get(workspaceName(workspace).toLowerCase()) || ''
    const exactMatch = remotePaths.has(normalizedDestination)
    if (exactMatch || nameMatch) {
      output.set(workspace.path, {
        workspacePath: workspace.path,
        action: 'link_existing',
        destinationPath: exactMatch ? fallbackDestination : nameMatch,
        reason: exactMatch ? 'Managed host inventory has this destination path.' : 'Managed host inventory has a matching workspace/folder name.',
        matchSource: exactMatch ? 'path' : 'name',
      })
    } else {
      output.set(workspace.path, {
        workspacePath: workspace.path,
        action: 'import_bundle',
        destinationPath: fallbackDestination,
        reason: inventory ? 'Managed host inventory does not contain a matching workspace.' : 'Inventory is still loading; preflight will verify before transfer.',
        matchSource: '',
      })
    }
  }
  return output
}

export function ManagedHostWorkspaceLinkPanel({
  target,
  busy = false,
  onSkip,
  onComplete,
}: ManagedHostWorkspaceLinkPanelProps) {
  const [loading, setLoading] = useState(false)
  const [preflighting, setPreflighting] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [workspaces, setWorkspaces] = useState<WorkspaceEntry[]>([])
  const [inventory, setInventory] = useState<ManagedWorkspaceInventoryResponse | null>(null)
  const [preflightPlans, setPreflightPlans] = useState<ManagedWorkspacePlan[]>([])
  const [drafts, setDrafts] = useState<WorkspaceDraft[]>([])
  const [destinationRoot, setDestinationRoot] = useState('~')
  const [results, setResults] = useState<ManagedWorkspaceResult[]>([])
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)

  const targetName = target.name || target.swarm_id
  const recommendations = useMemo(() => buildInventoryRecommendations({ workspaces, drafts, inventory, destinationRoot, preflightPlans }), [workspaces, drafts, inventory, destinationRoot, preflightPlans])
  const selectedDrafts = drafts.filter((draft) => draft.selected)
  const selectedCount = selectedDrafts.length
  const blockedSelectedCount = selectedDrafts.filter((draft) => {
    const recommendation = recommendations.get(draft.workspacePath)
    return !recommendation || recommendation.action === 'conflict' || recommendation.action === 'unsupported'
  }).length
  const linkCount = selectedDrafts.filter((draft) => recommendations.get(draft.workspacePath)?.action === 'link_existing').length
  const transferCount = selectedDrafts.filter((draft) => recommendations.get(draft.workspacePath)?.action === 'import_bundle').length

  const refreshPreflight = async (nextDrafts = drafts, nextDestinationRoot = destinationRoot) => {
    const selections = buildSelections(nextDrafts.filter((draft) => draft.selected), recommendations)
    if (!target.swarm_id.trim() || selections.length === 0) {
      setPreflightPlans([])
      return
    }
    setPreflighting(true)
    try {
      const response = await preflightManagedWorkspaces({
        targetSwarmID: target.swarm_id,
        destinationRoot: nextDestinationRoot,
        workspaces: selections,
      })
      setPreflightPlans(response.workspaces)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to verify managed host plan')
    } finally {
      setPreflighting(false)
    }
  }

  const load = async () => {
    setLoading(true)
    setError(null)
    setStatus(null)
    try {
      const [saved, peerInventory] = await Promise.all([
        listWorkspaces(),
        fetchManagedWorkspaceInventory({ targetSwarmID: target.swarm_id, limit: 300 }),
      ])
      setWorkspaces(saved)
      setInventory(peerInventory)
      const nextDrafts = saved.map((workspace) => {
        const destinationPath = defaultDestinationPath(workspace.path, destinationRoot, peerInventory)
        return {
          workspacePath: workspace.path,
          selected: Boolean(workspace.isGitRepo),
          destinationPath,
        }
      })
      setDrafts(nextDrafts)
      setStatus(`Loaded ${peerInventory.savedWorkspaces.length} saved, ${peerInventory.discoveredDirectories.length} discovered, and ${peerInventory.activeCWDs.length} active CWD entr${peerInventory.activeCWDs.length === 1 ? 'y' : 'ies'} from ${peerInventory.target.name || targetName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load managed host inventory')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [target.swarm_id])

  useEffect(() => {
    if (!inventory || drafts.length === 0) return
    const timer = window.setTimeout(() => {
      void refreshPreflight()
    }, 150)
    return () => window.clearTimeout(timer)
  }, [inventory, destinationRoot, drafts])

  const toggleWorkspace = (path: string) => {
    setResults([])
    setDrafts((current) => current.map((draft) => draft.workspacePath === path ? { ...draft, selected: !draft.selected } : draft))
  }

  const updateDestinationPath = (path: string, destinationPath: string) => {
    setResults([])
    setDrafts((current) => current.map((draft) => draft.workspacePath === path ? { ...draft, destinationPath } : draft))
  }

  const updateDestinationRoot = (value: string) => {
    setResults([])
    setDestinationRoot(value)
    setDrafts((current) => current.map((draft) => {
      const currentDefault = defaultDestinationPath(draft.workspacePath, destinationRoot, inventory)
      const nextDefault = defaultDestinationPath(draft.workspacePath, value, inventory)
      return {
        ...draft,
        destinationPath: !draft.destinationPath.trim() || normalizePath(draft.destinationPath) === normalizePath(currentDefault) ? nextDefault : draft.destinationPath,
      }
    }))
  }

  const handleLinkOrTransfer = async () => {
    const selectedRecommendations = selectedDrafts.map((draft) => recommendations.get(draft.workspacePath))
    if (!target.swarm_id.trim()) {
      setError('Managed Host target is missing.')
      return
    }
    if (selectedCount === 0) {
      setError('Select at least one workspace.')
      return
    }
    if (blockedSelectedCount > 0 || selectedRecommendations.some((item) => !item || item.action === 'conflict' || item.action === 'unsupported')) {
      setError('Resolve conflicts or unselect unavailable workspaces before continuing.')
      return
    }
    const selections = buildSelections(drafts, recommendations)
    setSubmitting(true)
    setError(null)
    setStatus('Applying selected links/imports on the managed host…')
    setResults([])
    try {
      const preflight = await preflightManagedWorkspaces({
        targetSwarmID: target.swarm_id,
        destinationRoot,
        workspaces: selections,
      })
      setPreflightPlans(preflight.workspaces)
      if (!preflight.ready || preflight.workspaces.some((plan) => !plan.ok)) {
        setError('Managed host plan changed. Review the updated actions before continuing.')
        setStatus(null)
        return
      }
      const response = await replicateManagedWorkspaces({
        targetSwarmID: target.swarm_id,
        destinationRoot,
        workspaces: selections,
        confirmedPlans: preflight.workspaces.map((plan) => ({
          sourceWorkspacePath: plan.sourceWorkspacePath,
          destinationPath: plan.destinationPath,
          action: plan.action,
          planId: plan.planId,
        })),
      })
      setResults(response.workspaces)
      const linked = response.workspaces.filter((workspace) => workspace.action === 'link_existing').length
      const transferred = response.workspaces.filter((workspace) => workspace.action === 'import_bundle').length
      const message = `Linked ${linked} existing workspace${linked === 1 ? '' : 's'} and transferred ${transferred} new workspace${transferred === 1 ? '' : 's'} on ${response.target.name || targetName}.`
      setStatus(message)
      await onComplete(message)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Managed Host workspace linking failed')
      setStatus(null)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section data-testid="managed-host-workspace-link-panel" className="mt-4 grid gap-4 rounded-2xl border border-[var(--app-border)] bg-transparent p-4 sm:p-5">
      <Card className="flex flex-col gap-4 px-5 py-5 sm:px-6 lg:flex-row lg:items-start lg:justify-between">
        <div className="grid gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-xl font-semibold text-[var(--app-text)]">Link workspaces on {targetName}</h2>
            <Badge tone={target.online ? 'live' : 'warning'}>{target.online ? 'Online' : 'Route pending'}</Badge>
            {preflighting ? <Badge tone="neutral">verifying…</Badge> : null}
          </div>
          <p className="text-sm leading-6 text-[var(--app-text-muted)]">
            Swarm is using {targetName}'s live inventory. Existing managed-host directories are linked in place. Missing workspaces are transferred only after this review.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" onClick={() => void load()} disabled={loading || submitting || busy}>
            <RefreshCw size={14} className={loading ? 'animate-spin' : undefined} />
            Refresh inventory
          </Button>
          <span className="inline-flex min-h-9 items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text-muted)]">
            <Folder size={14} /> {inventory ? inventory.savedWorkspaces.length : '—'} saved
          </span>
          <span className="inline-flex min-h-9 items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text-muted)]">
            <HardDrive size={14} /> {inventory ? inventory.discoveredDirectories.length : '—'} discovered
          </span>
        </div>
      </Card>

      {error ? <Card className="border-[var(--app-danger-border)] bg-transparent p-3 text-sm text-[var(--app-danger)]">{error}</Card> : null}
      {status ? <Card className="border-[var(--app-border)] bg-transparent p-3 text-sm text-[var(--app-text-muted)]">{status}</Card> : null}

      <div className="grid gap-3 rounded-xl border border-[var(--app-border)] bg-transparent p-4">
        <label className="grid gap-2 text-sm">
          <span className="font-medium text-[var(--app-text)]">Destination root on {targetName}</span>
          <Input
            value={destinationRoot}
            onChange={(event) => updateDestinationRoot(event.target.value)}
            placeholder="~"
            disabled={submitting || busy}
          />
          <span className="text-xs leading-5 text-[var(--app-text-muted)]">
            Default <code className="rounded bg-[var(--app-surface-subtle)] px-1">~</code> means the managed host home{inventory?.managedHome ? ` (${inventory.managedHome})` : ''}. Link actions do not create directories or move files. Transfer actions create the destination directory as needed and import a git bundle.
          </span>
        </label>
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {loading ? (
          <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">Loading local workspaces and managed-host inventory…</div>
        ) : workspaces.length === 0 ? (
          <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No local saved workspaces found.</div>
        ) : workspaces.map((workspace, index) => {
          const draft = drafts.find((item) => item.workspacePath === workspace.path)
          const selected = Boolean(draft?.selected)
          const directories = workspace.directories.length > 0 ? workspace.directories : [workspace.path]
          const recommendation = recommendations.get(workspace.path)
          const blocked = recommendation?.action === 'conflict' || recommendation?.action === 'unsupported'
          return (
            <div key={workspace.path} className={`group flex flex-col gap-3 rounded-lg border bg-[var(--app-surface)] p-3.5 shadow-sm transition-all ${selected ? 'border-[var(--app-border-strong)]' : 'border-[var(--app-border)] hover:border-[var(--app-border-accent)]'}`}>
              <div className="flex items-start justify-between gap-3">
                <label className="flex min-w-0 flex-1 cursor-pointer items-start gap-3">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={selected}
                    disabled={submitting || busy || blocked || !workspace.isGitRepo}
                    onChange={() => toggleWorkspace(workspace.path)}
                  />
                  <span className="min-w-0">
                    <span className="block truncate font-medium text-[var(--app-text)]">{workspaceName(workspace)}</span>
                    <span className="block truncate text-xs text-[var(--app-text-subtle)]" title={directories[0]}>{directories[0]}</span>
                  </span>
                </label>
                <span className="text-xs text-[var(--app-text-muted)]">{index + 1}</span>
              </div>
              <div className="flex flex-wrap gap-2">
                <div className="flex items-center gap-1.5 rounded-md border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2 py-1 text-xs text-[var(--app-text-muted)]">
                  <GitBranch size={14} /> {workspace.isGitRepo ? 'git' : 'not git'}
                </div>
                {recommendation ? <Badge tone={actionTone(recommendation.action)}>{actionLabel(recommendation.action)}</Badge> : null}
              </div>
              <div className="rounded-lg border border-[var(--app-border)] bg-transparent px-3 py-2 text-xs leading-5 text-[var(--app-text-muted)]">
                {recommendation ? actionDescription(recommendation.action, targetName, recommendation.destinationPath) : 'Waiting for inventory.'}
                {recommendation?.reason ? <div className="mt-1 text-[var(--app-text-subtle)]">{recommendation.reason}</div> : null}
              </div>
              <label className="mt-auto grid gap-1 border-t border-[var(--app-border)] pt-3 text-xs">
                <span className="text-[var(--app-text-muted)]">Destination path on {targetName}</span>
                <Input
                  value={draft?.destinationPath ?? recommendation?.destinationPath ?? ''}
                  onChange={(event) => updateDestinationPath(workspace.path, event.target.value)}
                  placeholder="Absolute path on the managed host"
                  disabled={submitting || busy || !selected}
                />
              </label>
            </div>
          )
        })}
      </div>

      {results.length > 0 ? (
        <div className="grid gap-2 rounded-xl border border-[var(--app-border)] bg-transparent p-4">
          <div className="text-sm font-semibold text-[var(--app-text)]">Results</div>
          {results.map((result) => (
            <div key={`${result.sourceWorkspacePath}:${result.destinationPath}`} className="rounded-lg border border-[var(--app-border)] px-3 py-2 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <CheckCircle2 size={15} className="text-[var(--app-success)]" />
                <span className="font-medium text-[var(--app-text)]">{result.sourceWorkspaceName || result.sourceWorkspacePath}</span>
                <Badge tone={actionTone(result.action)}>{actionLabel(result.action)}</Badge>
              </div>
              <div className="mt-1 break-all text-xs text-[var(--app-text-muted)]">{result.managedHostName || targetName}: {result.destinationPath}</div>
            </div>
          ))}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--app-border)] pt-4">
        <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-muted)]">
          <span>{selectedCount} selected</span>
          <span className="inline-flex items-center gap-1"><Link2 size={13} /> {linkCount} link only</span>
          <span className="inline-flex items-center gap-1"><UploadCloud size={13} /> {transferCount} transfer</span>
          {blockedSelectedCount > 0 ? <span className="text-[var(--app-warning-text)]">{blockedSelectedCount} blocked</span> : null}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" onClick={() => void onSkip(`Skipped workspace linking/import for ${targetName}.`)} disabled={submitting || busy}>Skip</Button>
          <Button type="button" onClick={() => void handleLinkOrTransfer()} disabled={submitting || busy || selectedCount === 0 || blockedSelectedCount > 0}>
            {submitting ? <Loader2 size={14} className="animate-spin" /> : null}
            Link/import selected workspaces
          </Button>
        </div>
      </div>

      {!target.online ? (
        <div className="flex items-start gap-2 rounded-xl border border-[var(--app-warning-border)] px-3 py-2 text-sm text-[var(--app-warning-text)]">
          <XCircle size={16} className="mt-0.5" /> The Managed Host route is not currently marked online. The inventory and link/import action will verify reachability before making changes.
        </div>
      ) : null}
    </section>
  )
}
