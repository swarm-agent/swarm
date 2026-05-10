import { useEffect, useState } from 'react'
import { CheckCircle2, Folder, GitBranch, HardDrive, Loader2, RefreshCw, XCircle } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import {
  replicateManagedWorkspaces,
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
  const [destinationRoot, setDestinationRoot] = useState('~')
  const [results, setResults] = useState<ManagedWorkspaceResult[]>([])
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)

  const targetName = target.name || target.swarm_id
  const selectedCount = drafts.filter((draft) => draft.selected).length
  const selectedGitCount = drafts.filter((draft) => draft.selected && workspaces.some((workspace) => workspace.path === draft.workspacePath && workspace.isGitRepo)).length

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

  const toggleWorkspace = (path: string) => {
    setResults([])
    setDrafts((current) => current.map((draft) => draft.workspacePath === path ? { ...draft, selected: !draft.selected } : draft))
  }

  const updateDestinationPath = (path: string, destinationPath: string) => {
    setResults([])
    setDrafts((current) => current.map((draft) => draft.workspacePath === path ? { ...draft, destinationPath } : draft))
  }

  const handleReplicate = async () => {
    const selections = buildSelections(drafts)
    if (!target.swarm_id.trim()) {
      setError('Managed Host target is missing.')
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
    setStatus('Checking the managed host and transferring workspaces…')
    setResults([])
    try {
      const response = await replicateManagedWorkspaces({
        targetSwarmID: target.swarm_id,
        destinationRoot,
        workspaces: selections,
      })
      setResults(response.workspaces)
      setStatus(`Replicated ${response.workspaces.length} workspace${response.workspaces.length === 1 ? '' : 's'} to ${response.target.name || targetName}.`)
      await onComplete(`Replicated ${response.workspaces.length} workspace${response.workspaces.length === 1 ? '' : 's'} to ${response.target.name || targetName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Managed Host replication failed')
      setStatus(null)
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
            Select saved git workspaces, then replicate. Swarm checks {targetName}, detects existing git directories, registers them when present, and imports missing workspaces with git bundles.
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
          <span className="font-medium text-[var(--app-text)]">Change the destination of the linked workspaces</span>
          <Input
            value={destinationRoot}
            onChange={(event) => {
              setResults([])
              setDestinationRoot(event.target.value)
            }}
            placeholder="~"
            disabled={submitting || busy}
          />
          <span className="text-xs leading-5 text-[var(--app-text-muted)]">
            Default is <code className="rounded bg-[var(--app-surface-subtle)] px-1">~</code> on {targetName}; paths under your home keep the part after <code className="rounded bg-[var(--app-surface-subtle)] px-1">/home/user/</code>. To store them in the workspaces folder, use <code className="rounded bg-[var(--app-surface-subtle)] px-1">workspaces</code> (typically <code className="rounded bg-[var(--app-surface-subtle)] px-1">/home/user/workspaces</code>).
          </span>
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
              </div>
              <label className="mt-auto grid gap-1 border-t border-[var(--app-border)] pt-3 text-xs">
                <span className="text-[var(--app-text-muted)]">Exact destination path (optional)</span>
                <Input
                  value={draft?.destinationPath ?? ''}
                  onChange={(event) => updateDestinationPath(workspace.path, event.target.value)}
                  placeholder="Leave blank to use the planned home-relative path"
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
          <Button type="button" onClick={() => void handleReplicate()} disabled={submitting || busy || selectedCount === 0}>
            {submitting ? <Loader2 size={14} className="animate-spin" /> : null}
            Replicate workspaces
          </Button>
        </div>
      </div>

      {!target.online ? (
        <div className="flex items-start gap-2 rounded-xl border border-[var(--app-warning-border)] px-3 py-2 text-sm text-[var(--app-warning-text)]">
          <XCircle size={16} className="mt-0.5" /> The Managed Host route is not currently marked online. Replication will verify reachability before transfer.
        </div>
      ) : null}
    </section>
  )
}
