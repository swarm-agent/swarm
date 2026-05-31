import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { ChevronRight, Eye, EyeOff, Folder, FolderOpen, Grid2X2, List, MoreHorizontal, RefreshCw, Search, Settings } from 'lucide-react'
import { Card } from '../../../components/ui/card'
import { Button } from '../../../components/ui/button'
import { Badge } from '../../../components/ui/badge'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { WorkspaceStatus } from '../launcher/components/workspace-status'
import { WorkspaceFolderTree } from '../launcher/components/workspace-folder-tree'
import { WorkspaceEditorModal, type WorkspaceEditorAvailableDirectory, type WorkspaceEditorManagedLinkDraft } from '../launcher/components/workspace-editor-modal'
import { fetchSwarmTargets } from '../../desktop/swarm/api/swarm-targets'
import { buildWorkspaceRouteSlugMap, workspaceRouteSlugBase } from '../launcher/services/workspace-route'
import { formatWorkspaceDirectories, formatWorkspacePath } from '../launcher/services/workspace-format'
import type { WorkspaceDiscoverEntry, WorkspaceEntry } from '../launcher/types/workspace'
import { useWorkspaceLauncher } from '../launcher/state/use-workspace-launcher'
import { cn } from '../../../lib/cn'

interface WorkspaceModalState {
  open: boolean
  mode: 'create' | 'edit'
  workspacePath: string
  workspacePathEditable: boolean
  sourcePaths: string[]
  themeId: string
  pendingManagedLinks: WorkspaceEditorManagedLinkDraft[]
}

function fallbackWorkspaceNameFromPath(path: string): string {
  const parts = path.trim().replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path.trim() || 'workspace'
}

function normalizeComparePath(path: string): string {
  const trimmed = path.trim()
  const withoutTrailing = trimmed.replace(/[\\/]+$/, '')
  return withoutTrailing || trimmed
}

function isSamePath(left: string, right: string): boolean {
  return normalizeComparePath(left) === normalizeComparePath(right)
}

function formatDiscoveredMeta(entry: { hasSwarm: boolean; hasClaude: boolean; isGitRepo: boolean; lastModified: number }): string {
  const parts: string[] = []
  if (entry.hasSwarm) {
    parts.push('AGENTS.md')
  }
  if (entry.hasClaude) {
    parts.push('CLAUDE.md')
  }
  if (entry.isGitRepo) {
    parts.push('git repo')
  }
  if (entry.lastModified > 0) {
    parts.push(`updated ${new Date(entry.lastModified).toLocaleDateString()}`)
  }
  return parts.join(' · ')
}

function workspaceMeta(workspace: WorkspaceEntry): string {
  const sessions = workspace.todoSummary?.user.taskCount ?? 0
  const folders = Math.max(workspace.directories.length, 1)
  const availability = workspace.topologyRoutes.length > 0 ? `${workspace.topologyRoutes.length + 1} locations` : 'this host only'
  return `${sessions} session${sessions === 1 ? '' : 's'} · ${folders} folder${folders === 1 ? '' : 's'} · ${availability}`
}

function workspaceLocation(workspace: WorkspaceEntry): string {
  return formatWorkspaceDirectories(workspace.directories)[0] ?? formatWorkspacePath(workspace.path)
}

function WorkspaceGlyph({ active = false }: { active?: boolean }) {
  return (
    <div
      className={cn(
        'flex size-10 shrink-0 items-center justify-center rounded-xl border text-[var(--app-text-muted)] shadow-sm',
        active
          ? 'border-[color-mix(in_oklab,var(--app-border-accent)_70%,transparent)] bg-[color-mix(in_oklab,var(--app-primary)_14%,var(--app-surface))] text-[var(--app-text)]'
          : 'border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_86%,transparent)]',
      )}
    >
      <Folder size={18} strokeWidth={1.7} />
    </div>
  )
}

interface PinnedWorkspaceCardProps {
  workspace: WorkspaceEntry
  current: boolean
  busy: boolean
  onOpen: (path: string) => void
  onEdit: (path: string) => void
  onDelete: (path: string) => void
}

function PinnedWorkspaceCard({ workspace, current, busy, onOpen, onEdit, onDelete }: PinnedWorkspaceCardProps) {
  return (
    <div
      className={cn(
        'group grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-xl border px-3 py-3 text-left transition-colors',
        current
          ? 'border-[color-mix(in_oklab,var(--app-border-accent)_72%,var(--app-border))] bg-[color-mix(in_oklab,var(--app-primary)_8%,var(--app-surface))] shadow-[0_0_0_1px_color-mix(in_oklab,var(--app-border-accent)_28%,transparent)]'
          : 'border-[color-mix(in_oklab,var(--app-border)_62%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_74%,transparent)] hover:border-[color-mix(in_oklab,var(--app-border-accent)_55%,var(--app-border))] hover:bg-[var(--app-surface-hover)]',
      )}
    >
      <button
        type="button"
        className="contents text-left disabled:cursor-wait"
        disabled={busy}
        onClick={() => onOpen(workspace.path)}
        aria-label={`Open ${workspace.workspaceName}`}
      >
        <WorkspaceGlyph active={current} />
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <h3 className="truncate text-sm font-semibold text-[var(--app-text)]">{workspace.workspaceName}</h3>
            {current ? (
              <span className="shrink-0 rounded-full bg-[color-mix(in_oklab,var(--app-primary)_18%,var(--app-surface-elevated))] px-2 py-0.5 text-[9px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text)]">
                Active
              </span>
            ) : null}
          </div>
          <div className="mt-0.5 truncate text-xs text-[var(--app-text-subtle)]" title={workspace.path}>
            {workspaceLocation(workspace)}
          </div>
          <div className="mt-1 truncate text-[11px] text-[var(--app-text-muted)]">{workspaceMeta(workspace)}</div>
        </div>
      </button>
      <div className="flex items-center gap-1">
        <button
          type="button"
          className="rounded-md p-1.5 text-[var(--app-text-subtle)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-50"
          onClick={() => onEdit(workspace.path)}
          disabled={busy}
          aria-label={`Edit ${workspace.workspaceName}`}
        >
          <MoreHorizontal size={16} />
        </button>
        <button
          type="button"
          className="rounded-md p-1.5 text-[var(--app-text-subtle)] transition-colors hover:bg-[color-mix(in_oklab,var(--app-danger)_10%,transparent)] hover:text-[var(--app-danger)] disabled:opacity-50"
          onClick={() => onDelete(workspace.path)}
          disabled={busy}
          aria-label={`Remove ${workspace.workspaceName}`}
        >
          <span className="text-sm leading-none">×</span>
        </button>
        <ChevronRight size={16} className="text-[var(--app-text-subtle)] transition-transform group-hover:translate-x-0.5" />
      </div>
    </div>
  )
}

interface AllWorkspaceRowProps {
  entry: WorkspaceDiscoverEntry
  savedWorkspace?: WorkspaceEntry
  busy: boolean
  onBrowse: (path: string) => void
  onOpen: (path: string) => void
  onCreate: (entry: WorkspaceDiscoverEntry) => void
  compact?: boolean
}

function AllWorkspaceRow({ entry, savedWorkspace, busy, onBrowse, onOpen, onCreate, compact = false }: AllWorkspaceRowProps) {
  const metaItems = compact ? [] : [entry.hasSwarm ? 'AGENTS.md' : null, entry.hasClaude ? 'CLAUDE.md' : null, entry.isGitRepo ? 'git repo' : null].filter(Boolean)

  return (
    <div
      className={cn(
        'grid min-w-0 items-center gap-3 border-b border-[color-mix(in_oklab,var(--app-border)_48%,transparent)] px-3 text-sm transition-colors last:border-b-0 hover:bg-[color-mix(in_oklab,var(--app-surface-hover)_70%,transparent)] max-md:grid-cols-[minmax(0,1fr)_auto] max-md:items-start',
        compact ? 'grid-cols-[minmax(0,1fr)_5.5rem] py-1.5' : 'grid-cols-[minmax(0,1.05fr)_minmax(0,1fr)_5.5rem] py-2.5',
      )}
    >
      <button type="button" className="flex min-w-0 items-center gap-3 text-left" onClick={() => onBrowse(entry.path)}>
        <div
          className={cn(
            'flex shrink-0 items-center justify-center rounded-lg bg-[color-mix(in_oklab,var(--app-surface-elevated)_76%,transparent)] text-[var(--app-text-muted)]',
            compact ? 'size-6' : 'size-8',
          )}
        >
          <FolderOpen size={compact ? 13 : 15} />
        </div>
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate font-medium text-[var(--app-text)]">{savedWorkspace?.workspaceName || entry.name}</span>
            {metaItems.map((item) => (
              <span key={item} className="rounded bg-[var(--app-surface-subtle)] px-1.5 py-0.5 text-[10px] text-[var(--app-text-subtle)] max-lg:hidden">
                {item}
              </span>
            ))}
          </div>
          {!compact ? (
            <div className="mt-0.5 truncate text-xs text-[var(--app-text-subtle)] md:hidden" title={entry.path}>
              {formatWorkspacePath(entry.path)}
            </div>
          ) : null}
        </div>
      </button>
      {!compact ? (
        <button type="button" className="min-w-0 text-left max-md:hidden" onClick={() => onBrowse(entry.path)}>
          <span className="block truncate text-xs text-[var(--app-text-muted)]" title={entry.path}>
            {formatWorkspacePath(entry.path)}
          </span>
        </button>
      ) : null}
      <div className="flex items-center justify-start gap-1">
        <Button
          size="sm"
          variant={savedWorkspace ? 'outline' : 'ghost'}
          disabled={busy}
          onClick={() => (savedWorkspace ? onOpen(savedWorkspace.path) : onCreate(entry))}
          className="h-7 rounded-md px-2.5 text-xs"
        >
          {busy ? 'Working…' : savedWorkspace ? 'Open' : 'Add'}
        </Button>
        <ChevronRight size={15} className="text-[var(--app-text-subtle)]" />
      </div>
    </div>
  )
}

export function WorkspaceHomePage() {
  const {
    workspaces,
    discovered,
    currentWorkspacePath,
    loading,
    selectingPath,
    savingPath,
    loadError,
    actionError,
    browser,
    browserLoading,
    browserError,
    refreshing,
    openWorkspace,
    useFolderTemporarily,
    deleteWorkspace,
    unlinkWorkspaceDirectory,
    upsertWorkspaceManagedLink,
    removeWorkspaceManagedLink,
    saveWorkspace,
    createFolder,
    moveWorkspaceToIndex,
    refresh,
    browsePath,
  } = useWorkspaceLauncher()

  const navigate = useNavigate()
  const swarmTargetsQuery = useQuery({
    queryKey: ['workspace-launcher-swarm-targets'],
    queryFn: fetchSwarmTargets,
    staleTime: 30_000,
  })
  const availableSwarmTargets = swarmTargetsQuery.data?.targets ?? []
  const [modalState, setModalState] = useState<WorkspaceModalState | null>(null)
  const [draftName, setDraftName] = useState('')
  const [workspaceNameTouched, setWorkspaceNameTouched] = useState(false)
  const [modalError, setModalError] = useState<string | null>(null)
  const [deleteTargetPath, setDeleteTargetPath] = useState<string | null>(null)
  const [workspaceSearch, setWorkspaceSearch] = useState('')
  const [allWorkspacesCompact, setAllWorkspacesCompact] = useState(false)
  const [allWorkspacesVisible, setAllWorkspacesVisible] = useState(true)

  const workspaceSlugByPath = useMemo(() => buildWorkspaceRouteSlugMap(workspaces), [workspaces])

  const editingWorkspace = useMemo(
    () => (modalState?.mode === 'edit' ? workspaces.find((workspace) => workspace.path === modalState.workspacePath) ?? null : null),
    [modalState, workspaces],
  )

  const currentWorkspace = useMemo(
    () => (currentWorkspacePath ? workspaces.find((workspace) => workspace.path === currentWorkspacePath) ?? null : null),
    [currentWorkspacePath, workspaces],
  )

  const temporaryFolderActive = Boolean(currentWorkspacePath && !currentWorkspace)
  const temporaryFolderName = temporaryFolderActive && currentWorkspacePath ? fallbackWorkspaceNameFromPath(currentWorkspacePath) : ''

  const savedWorkspaceByPath = useMemo(() => new Map(workspaces.map((workspace) => [workspace.path, workspace])), [workspaces])
  const discoveredRows = useMemo(() => {
    const rows = discovered.map((entry) => ({ entry, savedWorkspace: savedWorkspaceByPath.get(entry.path) }))
    const query = workspaceSearch.trim().toLowerCase()
    if (!query) {
      return rows
    }
    return rows.filter(({ entry, savedWorkspace }) => {
      const haystack = [entry.name, entry.path, savedWorkspace?.workspaceName ?? '', formatDiscoveredMeta(entry)].join(' ').toLowerCase()
      return haystack.includes(query)
    })
  }, [discovered, savedWorkspaceByPath, workspaceSearch])

  const modalAvailableDirectories = useMemo<WorkspaceEditorAvailableDirectory[]>(() => {
    if (!modalState) {
      return []
    }
    const taken = new Set(modalState.sourcePaths.map(normalizeComparePath).filter((value) => value !== ''))
    taken.add(normalizeComparePath(modalState.workspacePath))

    const workspaceCandidates = workspaces
      .filter((workspace) => workspace.path.trim() !== '' && !taken.has(normalizeComparePath(workspace.path)))
      .map((workspace) => ({
        path: workspace.path,
        name: workspace.workspaceName || fallbackWorkspaceNameFromPath(workspace.path),
        meta: `Saved workspace${workspace.isGitRepo ? ' · git repo' : ''}`,
      }))

    const discoveredCandidates = discovered
      .filter((entry) => !taken.has(normalizeComparePath(entry.path)))
      .map((entry) => ({
        path: entry.path,
        name: entry.name,
        meta: formatDiscoveredMeta(entry) || 'Folder detected',
      }))

    const merged = new Map<string, WorkspaceEditorAvailableDirectory>()
    for (const entry of workspaceCandidates) {
      merged.set(entry.path, entry)
    }
    for (const entry of discoveredCandidates) {
      if (!merged.has(entry.path)) {
        merged.set(entry.path, entry)
      }
    }
    return Array.from(merged.values())
  }, [discovered, modalState, workspaces])

  const deleteTargetWorkspace = useMemo(
    () => (deleteTargetPath ? workspaces.find((workspace) => workspace.path === deleteTargetPath) ?? null : null),
    [deleteTargetPath, workspaces],
  )

  const openCreateModal = (workspacePath: string, sourcePaths: string[], initialName: string) => {
    setModalState({
      open: true,
      mode: 'create',
      workspacePath,
      workspacePathEditable: true,
      sourcePaths,
      themeId: 'inherit',
      pendingManagedLinks: [],
    })
    setDraftName(initialName)
    setWorkspaceNameTouched(false)
    setModalError(null)
  }

  const startEdit = (path: string) => {
    const workspace = workspaces.find((item) => item.path === path)
    if (!workspace) {
      return
    }
    setModalState({
      open: true,
      mode: 'edit',
      workspacePath: path,
      workspacePathEditable: false,
      sourcePaths: workspace.directories,
      themeId: workspace.themeId || 'inherit',
      pendingManagedLinks: [],
    })
    setDraftName(workspace.workspaceName)
    setWorkspaceNameTouched(false)
    setModalError(null)
  }

  const closeModal = () => {
    setModalState(null)
    setDraftName('')
    setWorkspaceNameTouched(false)
    setModalError(null)
  }

  const addLinkedDirectories = (paths: string[]) => {
    setModalState((current) => {
      if (!current) {
        return current
      }
      const workspaceComparePath = normalizeComparePath(current.workspacePath)
      const existing = new Set(current.sourcePaths.map(normalizeComparePath))
      const next = paths.filter((path) => {
        const comparePath = normalizeComparePath(path)
        return comparePath !== '' && comparePath !== workspaceComparePath && !existing.has(comparePath)
      })
      if (next.length === 0) {
        return current
      }
      return {
        ...current,
        sourcePaths: [...current.sourcePaths, ...next],
      }
    })
  }

  const addLinkedDirectory = (path: string) => {
    addLinkedDirectories([path])
  }

  const pickWorkspaceFolder = (path: string) => {
    setModalState((current) => {
      if (!current) {
        return current
      }
      return {
        ...current,
        workspacePath: path,
        sourcePaths: current.sourcePaths.filter((value) => !isSamePath(value, current.workspacePath) && !isSamePath(value, path)),
      }
    })
    if (!workspaceNameTouched) {
      setDraftName(fallbackWorkspaceNameFromPath(path))
    }
  }

  const setDraftNameTouched = (value: string) => {
    setDraftName(value)
    setWorkspaceNameTouched(true)
  }

  const removeLinkedDirectory = (path: string) => {
    if (!modalState) {
      return
    }
    if (modalState.mode === 'create') {
      setModalState({
        ...modalState,
        sourcePaths: modalState.sourcePaths.filter((value) => !isSamePath(value, path)),
      })
      return
    }
    void (async () => {
      try {
        await unlinkWorkspaceDirectory(modalState.workspacePath, path)
        setModalState((current) => {
          if (!current || current.workspacePath !== modalState.workspacePath) {
            return current
          }
          return {
            ...current,
            sourcePaths: current.sourcePaths.filter((value) => !isSamePath(value, path)),
          }
        })
      } catch (err) {
        setModalError(err instanceof Error ? err.message : 'Failed to remove linked folder')
      }
    })()
  }

  const setThemeId = (nextThemeId: string) => {
    setModalState((current) => (current ? { ...current, themeId: nextThemeId } : current))
  }

  const addManagedLink = (targetSwarmID: string, destinationPath: string) => {
    if (!modalState) {
      return
    }
    if (modalState.mode === 'create') {
      setModalState((current) => {
        if (!current) return current
        const next = { targetSwarmID, destinationPath }
        const existing = current.pendingManagedLinks.filter((link) => link.targetSwarmID !== targetSwarmID)
        return { ...current, pendingManagedLinks: [...existing, next] }
      })
      return
    }
    void (async () => {
      try {
        await upsertWorkspaceManagedLink({
          workspacePath: modalState.workspacePath,
          targetSwarmID,
          destinationPath,
          workspaceName: draftName,
          provision: true,
        })
      } catch (err) {
        setModalError(err instanceof Error ? err.message : 'Failed to add managed host link')
      }
    })()
  }

  const removePendingManagedLink = (targetSwarmID: string, destinationPath: string) => {
    setModalState((current) => current ? {
      ...current,
      pendingManagedLinks: current.pendingManagedLinks.filter((link) => link.targetSwarmID !== targetSwarmID || link.destinationPath !== destinationPath),
    } : current)
  }

  const removeManagedLink = (linkID: string) => {
    if (!modalState) {
      return
    }
    void (async () => {
      try {
        await removeWorkspaceManagedLink(modalState.workspacePath, linkID)
      } catch (err) {
        setModalError(err instanceof Error ? err.message : 'Failed to remove managed host link')
      }
    })()
  }

  const handleOpenWorkspace = (path: string) => {
    void (async () => {
      const resolution = await openWorkspace(path)
      const resolvedPath = resolution.resolvedPath.trim() || path
      const workspaceSlug = workspaceSlugByPath.get(resolvedPath) ?? workspaceRouteSlugBase({ path: resolvedPath, workspaceName: resolution.workspaceName })
      await navigate({
        to: '/$workspaceSlug',
        params: { workspaceSlug },
      })
    })()
  }

  const handleUseFolderTemporarily = (path: string) => {
    void (async () => {
      const resolution = await useFolderTemporarily(path)
      const workspaceSlug = workspaceRouteSlugBase({
        path: resolution.resolvedPath,
        workspaceName: resolution.workspaceName,
      })
      await navigate({
        to: '/$workspaceSlug',
        params: { workspaceSlug },
      })
    })()
  }

  const handleConfirmDelete = () => {
    if (!deleteTargetPath) {
      return
    }
    void (async () => {
      await deleteWorkspace(deleteTargetPath)
      setDeleteTargetPath(null)
    })()
  }

  const handlePromoteTemporaryFolder = () => {
    if (!currentWorkspacePath) {
      return
    }
    openCreateModal(currentWorkspacePath, [currentWorkspacePath], temporaryFolderName)
  }

  const submitModal = async () => {
    if (!modalState) {
      return
    }

    const workspacePath = modalState.workspacePath.trim()
    if (!workspacePath) {
      setModalError('Workspace path is required.')
      return
    }

    const seenLinkedDirectories = new Set<string>()
    const linkedDirectories = modalState.sourcePaths
      .map((value) => value.trim())
      .filter((value) => {
        const comparePath = normalizeComparePath(value)
        if (comparePath === '' || comparePath === normalizeComparePath(workspacePath) || seenLinkedDirectories.has(comparePath)) {
          return false
        }
        seenLinkedDirectories.add(comparePath)
        return true
      })

    try {
      await saveWorkspace({
        path: workspacePath,
        name: draftName,
        themeId: modalState.themeId,
        makeCurrent: modalState.mode === 'edit' ? Boolean(editingWorkspace?.active || currentWorkspacePath === workspacePath) : false,
        linkedDirectories,
      })
      for (const link of modalState.pendingManagedLinks) {
        await upsertWorkspaceManagedLink({
          workspacePath,
          targetSwarmID: link.targetSwarmID,
          destinationPath: link.destinationPath,
          workspaceName: draftName,
          provision: true,
        })
      }
      closeModal()
    } catch (err) {
      setModalError(err instanceof Error ? err.message : 'Failed to save workspace')
    }
  }

  return (
    <>
      <main className="flex h-full min-h-0 w-full overflow-hidden bg-[linear-gradient(180deg,var(--app-bg),color-mix(in_oklab,var(--app-bg)_76%,var(--app-surface-subtle)))]">
        <section className="min-h-0 min-w-0 flex-[1_1_68%] overflow-y-auto overscroll-contain px-4 py-6 [-webkit-overflow-scrolling:touch] sm:px-6 lg:px-8">
          <div className="flex min-h-full flex-col gap-8">
            <div className="flex flex-col gap-4 bg-transparent px-0 py-2 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <h1 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Workspaces</h1>
                <p className="mt-1 text-sm text-[var(--app-text-muted)]">Select a project to begin working</p>
              </div>
              <div className="flex items-center gap-3">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void navigate({ to: '/settings' })}
                  className="rounded-md bg-[var(--app-surface)] shadow-sm"
                >
                  <Settings size={14} />
                  Settings
                </Button>
                <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={refreshing} className="rounded-md bg-[var(--app-surface)] shadow-sm">
                  <RefreshCw size={14} className={refreshing ? 'animate-spin' : undefined} />
                  Refresh
                </Button>
              </div>
            </div>

            {loading ? (
              <Card className="flex items-center gap-3 px-5 py-4 text-sm text-[var(--app-text-muted)] sm:px-6">
                <RefreshCw size={18} className="animate-spin" />
                <span>Loading workspaces…</span>
              </Card>
            ) : null}

            {!loading && loadError ? (
              <WorkspaceStatus
                kind="error"
                title="Could not load workspaces"
                message={loadError}
                actionLabel="Try again"
                onAction={() => void refresh()}
              />
            ) : null}

            {!loading && actionError ? <WorkspaceStatus kind="error" title="Workspace action failed" message={actionError} /> : null}

            {!loading && !loadError ? (
              <div className="flex flex-col gap-9">
                <section className="flex flex-col gap-4">
                  <div className="flex items-end justify-between gap-3">
                    <div>
                      <h2 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Pinned workspaces</h2>
                      <p className="mt-1 text-sm text-[var(--app-text-muted)]">Open a saved workspace or manage its local links.</p>
                    </div>
                    <span className="text-xs text-[var(--app-text-subtle)]">{workspaces.length}</span>
                  </div>
                  {workspaces.length === 0 ? (
                    <WorkspaceStatus kind="empty" title="No saved workspaces" message="Browse a folder in Explorer and add it as your first workspace." />
                  ) : (
                    <div className="grid gap-3 xl:grid-cols-2">
                      {workspaces.map((workspace) => (
                        <PinnedWorkspaceCard
                          key={workspace.path}
                          workspace={workspace}
                          current={currentWorkspacePath === workspace.path || workspace.active}
                          busy={selectingPath === workspace.path || savingPath === workspace.path}
                          onOpen={handleOpenWorkspace}
                          onEdit={startEdit}
                          onDelete={setDeleteTargetPath}
                        />
                      ))}
                    </div>
                  )}
                </section>

                <section className="flex flex-col gap-3">
                  <div className="flex flex-col gap-3 border-t border-[color-mix(in_oklab,var(--app-border)_56%,transparent)] pt-6 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                      <div className="flex items-center gap-2">
                        <h2 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">All workspaces</h2>
                        <span className="rounded-full bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] text-[var(--app-text-muted)]">{discovered.length}</span>
                      </div>
                      <p className="mt-1 text-sm text-[var(--app-text-muted)]">Folders with AGENTS.md or git repos</p>
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      {allWorkspacesVisible ? (
                        <div className="flex h-8 min-w-[220px] items-center rounded-lg border border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] bg-transparent px-2.5 text-sm focus-within:border-[var(--app-border-accent)] focus-within:ring-2 focus-within:ring-[var(--app-focus-ring)]">
                          <Search size={14} className="mr-2 shrink-0 text-[var(--app-text-subtle)]" />
                          <input
                            value={workspaceSearch}
                            onChange={(event) => setWorkspaceSearch(event.target.value)}
                            placeholder="Search workspaces…"
                            className="h-full w-full border-0 bg-transparent p-0 text-xs text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)]"
                          />
                        </div>
                      ) : null}
                      <div className="flex rounded-lg border border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] bg-[var(--app-surface)] p-0.5 text-[var(--app-text-subtle)]">
                        <button
                          type="button"
                          className={cn('rounded-md p-1.5 transition-colors', !allWorkspacesCompact && 'bg-[var(--app-surface-elevated)] text-[var(--app-text)]')}
                          aria-label="Detailed folder list"
                          aria-pressed={!allWorkspacesCompact}
                          onClick={() => setAllWorkspacesCompact(false)}
                        >
                          <List size={14} />
                        </button>
                        <button
                          type="button"
                          className={cn('rounded-md p-1.5 transition-colors', allWorkspacesCompact && 'bg-[var(--app-surface-elevated)] text-[var(--app-text)]')}
                          aria-label="Compact folder names"
                          aria-pressed={allWorkspacesCompact}
                          onClick={() => setAllWorkspacesCompact(true)}
                        >
                          <Grid2X2 size={14} />
                        </button>
                      </div>
                      <button
                        type="button"
                        className="flex size-8 items-center justify-center rounded-lg border border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] bg-[var(--app-surface)] text-[var(--app-text-subtle)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                        aria-label={allWorkspacesVisible ? 'Hide all workspaces' : 'Show all workspaces'}
                        aria-pressed={!allWorkspacesVisible}
                        onClick={() => setAllWorkspacesVisible((visible) => !visible)}
                      >
                        {allWorkspacesVisible ? <EyeOff size={14} /> : <Eye size={14} />}
                      </button>
                    </div>
                  </div>

                  {allWorkspacesVisible ? (
                    discovered.length === 0 ? (
                      <WorkspaceStatus kind="empty" title="No candidate folders" message="No repositories found in scanned locations. Use Explorer to browse to a folder." />
                    ) : (
                      <div className="overflow-hidden rounded-xl border border-[color-mix(in_oklab,var(--app-border)_58%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_52%,transparent)]">
                        {!allWorkspacesCompact ? (
                          <div className="grid grid-cols-[minmax(0,1.05fr)_minmax(0,1fr)_5.5rem] gap-3 border-b border-[color-mix(in_oklab,var(--app-border)_56%,transparent)] px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)] max-md:hidden">
                            <span>Name</span>
                            <span>Location</span>
                            <span>Action</span>
                          </div>
                        ) : null}
                        {discoveredRows.length === 0 ? (
                          <div className="px-4 py-6 text-sm text-[var(--app-text-muted)]">No folders match your search.</div>
                        ) : (
                          discoveredRows.map(({ entry, savedWorkspace }) => (
                            <AllWorkspaceRow
                              key={entry.path}
                              entry={entry}
                              savedWorkspace={savedWorkspace}
                              busy={savingPath === entry.path || selectingPath === entry.path}
                              compact={allWorkspacesCompact}
                              onBrowse={(path) => void browsePath(path)}
                              onOpen={handleOpenWorkspace}
                              onCreate={(row) => openCreateModal(row.path, [row.path], row.name)}
                            />
                          ))
                        )}
                      </div>
                    )
                  ) : null}

                  {temporaryFolderActive ? (
                    <Card className="grid gap-3 px-5 py-5 sm:px-6">
                      <div className="flex flex-wrap items-center gap-2">
                        <h2 className="text-lg font-semibold text-[var(--app-text)]">Temporary folder</h2>
                        <Badge tone="warning">Temporary</Badge>
                      </div>
                      <div className="break-all text-sm text-[var(--app-text)]">{currentWorkspacePath}</div>
                      <div>
                        <Button type="button" onClick={handlePromoteTemporaryFolder}>
                          Make workspace
                        </Button>
                      </div>
                    </Card>
                  ) : null}
                </section>
              </div>
            ) : null}
          </div>
        </section>

        <aside className="min-h-0 w-[34%] min-w-[340px] max-w-[430px] shrink-0 border-l border-[color-mix(in_oklab,var(--app-border)_64%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_52%,transparent)]">
          <WorkspaceFolderTree
            browser={browser}
            browserLoading={browserLoading}
            browserError={browserError}
            workspaces={workspaces}
            selectingPath={selectingPath}
            savingPath={savingPath}
            onBrowsePath={(path) => {
              void browsePath(path)
            }}
            onOpenWorkspace={handleOpenWorkspace}
            onUseFolderTemporarily={handleUseFolderTemporarily}
            onCreateWorkspace={(entry) => openCreateModal(entry.path, [entry.path], entry.name)}
            onCreateFolder={createFolder}
          />
        </aside>
      </main>

      <WorkspaceEditorModal
        open={Boolean(modalState?.open)}
        mode={modalState?.mode ?? 'create'}
        workspacePath={modalState?.workspacePath ?? ''}
        workspacePathEditable={modalState?.workspacePathEditable ?? true}
        name={draftName}
        themeId={modalState?.themeId ?? 'inherit'}
        linkedDirectories={modalState?.sourcePaths.filter((path) => !isSamePath(path, modalState.workspacePath)) ?? []}
        availableDirectories={modalAvailableDirectories}
        pendingManagedLinks={modalState?.pendingManagedLinks ?? []}
        workspaces={workspaces}
        availableSwarmTargets={availableSwarmTargets}
        browser={browser}
        browserLoading={browserLoading}
        browserError={browserError}
        canRemoveLinkedDirectories={Boolean(modalState)}
        error={modalError}
        saving={Boolean(savingPath && modalState?.workspacePath && savingPath === modalState.workspacePath)}
        onWorkspacePathChange={(value) => setModalState((current) => (current ? { ...current, workspacePath: value } : current))}
        onPickWorkspaceFolder={pickWorkspaceFolder}
        onNameChange={setDraftNameTouched}
        onThemeIdChange={setThemeId}
        onBrowsePath={(path) => {
          void browsePath(path)
        }}
        onCreateFolder={createFolder}
        onSelectWorkspace={startEdit}
        onMoveWorkspaceToIndex={(path, index) => {
          void moveWorkspaceToIndex(path, index)
        }}
        onAddLinkedDirectory={addLinkedDirectory}
        onAddLinkedDirectories={addLinkedDirectories}
        onRemoveLinkedDirectory={removeLinkedDirectory}
        onAddManagedLink={addManagedLink}
        onRemoveManagedLink={removeManagedLink}
        onRemovePendingManagedLink={removePendingManagedLink}
        onClose={closeModal}
        onSubmit={() => {
          void submitModal()
        }}
      />

      <Dialog className={deleteTargetWorkspace ? undefined : 'hidden'} aria-hidden={!deleteTargetWorkspace}>
        <DialogBackdrop onClick={() => setDeleteTargetPath(null)} />
        <DialogPanel className="max-w-xl gap-4">
          <div className="grid gap-2">
            <div className="flex items-center gap-2">
              <Badge tone="warning">!</Badge>
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Remove workspace from Swarm?</h2>
            </div>
            <p className="text-sm leading-6 text-[var(--app-text-muted)]">
              This only removes Swarm’s saved workspace metadata. It does not delete the folder or any files on disk.
            </p>
          </div>
          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text)]">
            {deleteTargetWorkspace ? deleteTargetWorkspace.path : ''}
          </div>
          <div className="flex justify-end gap-3">
            <Button type="button" onClick={() => setDeleteTargetPath(null)}>
              Cancel
            </Button>
            <Button type="button" onClick={handleConfirmDelete} disabled={!deleteTargetWorkspace || savingPath === deleteTargetWorkspace.path}>
              {deleteTargetWorkspace && savingPath === deleteTargetWorkspace.path ? 'Removing…' : 'Remove from Swarm'}
            </Button>
          </div>
        </DialogPanel>
      </Dialog>
    </>
  )
}
