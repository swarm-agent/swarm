import { useEffect, useMemo, useState } from 'react'
import { CheckCircle2, Folder, GitBranch, HardDrive, Loader2, RefreshCw, XCircle } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import {
  preflightManagedWorkspaces,
  replicateManagedWorkspaces,
  type ManagedWorkspacePlan,
  type ManagedWorkspaceResult,
  type ManagedWorkspaceSelectionInput,
} from '../api/managed-workspace-replication'
import type { SwarmTarget } from '../api/swarm-targets'

interface ManagedHostWorkspaceReplicationPanelProps {
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

function workspaceName(workspace: WorkspaceEntry): string {
  return workspace.workspaceName || workspace.path.split('/').filter(Boolean).pop() || 'workspace'
}

function actionLabel(action: string): string {
  switch (action) {
    case 'import_bundle':
      return 'Import'
    case 'link_existing':
      return 'Link'
    case 'conflict':
      return 'Conflict'
    default:
      return action || 'Plan'
  }
}

function actionTone(action: string, ok: boolean): 'live' | 'warning' | 'neutral' {
  if (!ok || action === 'conflict') return 'warning'
  if (action === 'import_bundle' || action === 'link_existing') return 'live'
  return 'neutral'
}

function buildSelections(drafts: WorkspaceDraft[]): ManagedWorkspaceSelectionInput[] {
  return drafts
    .filter((draft) => draft.selected)
    .map((draft) => ({
      sourceWorkspacePath: draft.workspacePath,
      destinationPath: draft.destinationPath.trim() || undefined,
    }))
}

export function ManagedHostWorkspaceReplicationPanel({
  target,
  busy = false,
  onSkip,
  onComplete,
}: ManagedHostWorkspaceReplicationPanelProps) {
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [workspaces, setWorkspaces] = useState<WorkspaceEntry[]>([])
  const [drafts, setDrafts] = useState<WorkspaceDraft[]>([])
  const [destinationRoot, setDestinationRoot] = useState('')
  const [preflightPlans, setPreflightPlans] = useState<ManagedWorkspacePlan[]>([])
  const [preflightSnapshot, setPreflightSnapshot] = useState('')
  const [results, setResults] = useState<ManagedWorkspaceResult[]>([])
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)

  const targetName = target.name || target.swarm_id
  const selectedCount = drafts.filter((draft) => draft.selected).length
  const selectedGitCount = drafts.filter((draft) => draft.selected && workspaces.some((workspace) => workspace.path === draft.workspacePath && workspace.isGitRepo)).length
  const currentSelectionKey = useMemo(() => JSON.stringify({ root: destinationRoot.trim(), selections: buildSelections(drafts) }), [destinationRoot, drafts])
  const preflightCurrent = preflightSnapshot === currentSelectionKey
  const ready = preflightCurrent && preflightPlans.length > 0 && preflightPlans.every((plan) => plan.ok && plan.action !== 'conflict')
  const planByPath = useMemo(() => {
    const map = new Map<string, ManagedWorkspacePlan>()
    for (const plan of preflightPlans) {
      map.set(plan.sourceWorkspacePath, plan)
    }
    return map
  }, [preflightPlans])

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      const saved = await listWorkspaces()
      setWorkspaces(saved)
      setDrafts((current) => {
        const currentByPath = new Map(current.map((draft) => [draft.workspacePath, draft]))
        return saved.map((workspace) => {
          const currentDraft = currentByPath.get(workspace.path)
          return {
            workspacePath: workspace.path,
            selected: currentDraft?.selected ?? Boolean(workspace.isGitRepo),
            destinationPath: currentDraft?.destinationPath ?? '',
          }
        })
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load workspaces')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  useEffect(() => {
    if (preflightPlans.length > 0 && preflightSnapshot !== currentSelectionKey) {
      setStatus('Selection changed. Run preflight again before transfer.')
    }
  }, [currentSelectionKey, preflightPlans.length, preflightSnapshot])

  const toggleWorkspace = (path: string) => {
    setResults([])
    setDrafts((current) => current.map((draft) => draft.workspacePath === path ? { ...draft, selected: !draft.selected } : draft))
  }

  const updateDestinationPath = (path: string, destinationPath: string) => {
    setResults([])
    setDrafts((current) => current.map((draft) => draft.workspacePath === path ? { ...draft, destinationPath } : draft))
  }

  const handlePreflight = async () => {
    const selections = buildSelections(drafts)
    if (!target.swarm_id.trim()) {
      setError('Managed Host target is missing.')
      return
    }
    if (!destinationRoot.trim()) {
      setError('Destination root is required.')
      return
    }
    if (selections.length === 0) {
      setError('Select at least one workspace.')
      return
    }
    if (selectedGitCount !== selectedCount) {
      setError('Only git workspaces can be transferred to a Managed Host.')
      return
    }
    setSubmitting(true)
    setError(null)
    setStatus(null)
    setResults([])
    try {
      const snapshot = JSON.stringify({ root: destinationRoot.trim(), selections })
      const response = await preflightManagedWorkspaces({
        targetSwarmID: target.swarm_id,
        destinationRoot,
        workspaces: selections,
      })
      setPreflightPlans(response.workspaces)
      setPreflightSnapshot(snapshot)
      setStatus(response.ready ? `Ready to transfer ${response.workspaces.length} workspace${response.workspaces.length === 1 ? '' : 's'} to ${response.target.name || targetName}.` : 'Preflight found conflicts. Resolve them before transfer.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Managed Host preflight failed')
    } finally {
      setSubmitting(false)
    }
  }

  const handleReplicate = async () => {
    if (!ready) {
      setError('Run a clean preflight before transfer.')
      return
    }
    const selections = buildSelections(drafts)
    setSubmitting(true)
    setError(null)
    setStatus(null)
    setResults([])
    try {
      const response = await replicateManagedWorkspaces({
        targetSwarmID: target.swarm_id,
        destinationRoot,
        workspaces: selections,
        confirmedPlans: preflightPlans.map((plan) => ({
          sourceWorkspacePath: plan.sourceWorkspacePath,
          destinationPath: plan.destinationPath,
          action: plan.action,
          planId: plan.planId,
        })),
      })
      setResults(response.workspaces)
      setStatus(`Transferred ${response.workspaces.length} workspace${response.workspaces.length === 1 ? '' : 's'} to ${response.target.name || targetName}.`)
      await onComplete(`Transferred ${response.workspaces.length} workspace${response.workspaces.length === 1 ? '' : 's'} to ${response.target.name || targetName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Managed Host transfer failed')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section data-testid="managed-host-workspace-replication-panel" className="mt-4 grid gap-4 rounded-2xl border border-[var(--app-border)] bg-transparent p-4 sm:p-5">
      <Card className="flex flex-col gap-4 px-5 py-5 sm:px-6 lg:flex-row lg:items-start lg:justify-between">
        <div className="grid gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-xl font-semibold text-[var(--app-text)]">Replicate workspaces to {targetName}</h2>
            <Badge tone={target.online ? 'live' : 'warning'}>{target.online ? 'Online' : 'Route pending'}</Badge>
          </div>
          <p className="text-sm leading-6 text-[var(--app-text-muted)]">
            Select saved git workspaces, enter the exact destination root on {targetName}, preflight the planned paths, then transfer.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" onClick={() => void load()} disabled={loading || submitting || busy}>
            <RefreshCw size={14} className={loading ? 'animate-spin' : undefined} />
            Refresh
          </Button>
          <span className="inline-flex min-h-9 items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text-muted)]">
            <Folder size={14} /> {workspaces.length} saved
          </span>
          <span className="inline-flex min-h-9 items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text-muted)]">
            <GitBranch size={14} /> {workspaces.filter((workspace) => workspace.isGitRepo).length} git
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
            onChange={(event) => {
              setResults([])
              setDestinationRoot(event.target.value)
            }}
            placeholder="Absolute path on the Managed Host"
            disabled={submitting || busy}
          />
          <span className="text-xs text-[var(--app-text-muted)]">Required. No default is assumed. Workspace destinations are planned inside this root.</span>
        </label>
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {loading ? (
          <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">Loading workspaces…</div>
        ) : workspaces.length === 0 ? (
          <div className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-sm text-[var(--app-text-muted)]">No saved workspaces found.</div>
        ) : workspaces.map((workspace, index) => {
          const draft = drafts.find((item) => item.workspacePath === workspace.path)
          const selected = Boolean(draft?.selected)
          const plan = planByPath.get(workspace.path)
          const directories = workspace.directories.length > 0 ? workspace.directories : [workspace.path]
          return (
            <div key={workspace.path} className={`group flex flex-col gap-3 rounded-lg border bg-[var(--app-surface)] p-3.5 shadow-sm transition-all ${selected ? 'border-[var(--app-border-strong)]' : 'border-[var(--app-border)] hover:border-[var(--app-border-accent)]'}`}>
              <div className="flex items-start justify-between gap-3">
                <label className="flex min-w-0 flex-1 cursor-pointer items-start gap-3">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={selected}
                    disabled={submitting || busy || !workspace.isGitRepo}
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
                  <HardDrive size={14} /> {directories.length}
                </div>
                {workspace.isGitRepo ? <Badge tone="live">git</Badge> : <Badge tone="warning">not git</Badge>}
                {plan ? <Badge tone={actionTone(plan.action, plan.ok)}>{actionLabel(plan.action)}</Badge> : null}
              </div>
              <label className="mt-auto grid gap-1 border-t border-[var(--app-border)] pt-3 text-xs">
                <span className="text-[var(--app-text-muted)]">Exact destination path (optional)</span>
                <Input
                  value={draft?.destinationPath ?? ''}
                  onChange={(event) => updateDestinationPath(workspace.path, event.target.value)}
                  placeholder="Leave blank to use root/name"
                  disabled={submitting || busy || !selected}
                />
              </label>
              {plan ? (
                <div className={`rounded-lg border px-3 py-2 text-xs ${plan.ok ? 'border-[var(--app-border)] text-[var(--app-text-muted)]' : 'border-[var(--app-warning-border)] text-[var(--app-warning-text)]'}`}>
                  <div className="break-all">{plan.destinationPath}</div>
                  {plan.error ? <div className="mt-1">{plan.error}</div> : null}
                </div>
              ) : null}
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
                <Badge tone="live">{actionLabel(result.action)}</Badge>
              </div>
              <div className="mt-1 break-all text-xs text-[var(--app-text-muted)]">{result.managedHostName || targetName}: {result.destinationPath}</div>
            </div>
          ))}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--app-border)] pt-4">
        <div className="text-xs text-[var(--app-text-muted)]">
          {selectedCount} selected{selectedCount !== selectedGitCount ? ` · ${selectedCount - selectedGitCount} unavailable because it is not git-backed` : ''}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" onClick={() => void onSkip(`Skipped workspace replication for ${targetName}.`)} disabled={submitting || busy}>Skip</Button>
          <Button type="button" variant="outline" onClick={() => void handlePreflight()} disabled={submitting || busy || selectedCount === 0}>
            {submitting && !ready ? <Loader2 size={14} className="animate-spin" /> : null}
            Preflight
          </Button>
          <Button type="button" onClick={() => void handleReplicate()} disabled={submitting || busy || !ready} title={!ready ? 'Run a clean preflight first' : undefined}>
            {submitting && ready ? <Loader2 size={14} className="animate-spin" /> : null}
            Transfer
          </Button>
        </div>
      </div>

      {!target.online ? (
        <div className="flex items-start gap-2 rounded-xl border border-[var(--app-warning-border)] px-3 py-2 text-sm text-[var(--app-warning-text)]">
          <XCircle size={16} className="mt-0.5" /> The Managed Host route is not currently marked online. Preflight will verify reachability before transfer.
        </div>
      ) : null}
    </section>
  )
}
