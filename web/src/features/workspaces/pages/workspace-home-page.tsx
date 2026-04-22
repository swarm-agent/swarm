import { useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { FolderOpen, RefreshCw } from 'lucide-react'
import { Card } from '../../../components/ui/card'
import { Button } from '../../../components/ui/button'
import { Badge } from '../../../components/ui/badge'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { WorkspaceStatus } from '../launcher/components/workspace-status'
import { WorkspaceCard } from '../launcher/components/workspace-card'
import { WorkspaceFolderTree } from '../launcher/components/workspace-folder-tree'
import { WorkspaceEditorModal, type WorkspaceEditorAvailableDirectory } from '../launcher/components/workspace-editor-modal'
import { buildWorkspaceRouteSlugMap, workspaceRouteSlugBase } from '../launcher/services/workspace-route'
import { useWorkspaceLauncher } from '../launcher/state/use-workspace-launcher'

interface WorkspaceModalState {
  open: boolean
  mode: 'create' | 'edit'
  workspacePath: string
  workspacePathEditable: boolean
  sourcePaths: string[]
  themeId: string
}

function fallbackWorkspaceNameFromPath(path: string): string {
  const parts = path.trim().replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path.trim() || 'workspace'
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

export function WorkspaceHomePage() {
  const {
    workspaces,
    discovered,
    currentWorkspacePath,
    loading,
    selectingPath,
    savingPath,
    draggingWorkspacePath,
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
    setWorktreeEnabled,
    saveWorkspace,
    moveWorkspaceToIndex,
    setDraggingWorkspacePath,
    refresh,
    browsePath,
  } = useWorkspaceLauncher()

  const navigate = useNavigate()
  const [modalState, setModalState] = useState<WorkspaceModalState | null>(null)
  const [draftName, setDraftName] = useState('')
  const [modalError, setModalError] = useState<string | null>(null)
  const [deleteTargetPath, setDeleteTargetPath] = useState<string | null>(null)

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

  const modalAvailableDirectories = useMemo<WorkspaceEditorAvailableDirectory[]>(() => {
    if (!modalState) {
      return []
    }
    const taken = new Set(modalState.sourcePaths.map((value) => value.trim()).filter((value) => value !== ''))
    taken.add(modalState.workspacePath.trim())

    const workspaceCandidates = workspaces
      .filter((workspace) => workspace.path.trim() !== '' && !taken.has(workspace.path))
      .map((workspace) => ({
        path: workspace.path,
        name: workspace.workspaceName || fallbackWorkspaceNameFromPath(workspace.path),
        meta: `Saved workspace${workspace.isGitRepo ? ' · git repo' : ''}`,
      }))

    const discoveredCandidates = discovered
      .filter((entry) => !taken.has(entry.path))
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
    })
    setDraftName(initialName)
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
    })
    setDraftName(workspace.workspaceName)
    setModalError(null)
  }

  const closeModal = () => {
    setModalState(null)
    setDraftName('')
    setModalError(null)
  }

  const addLinkedDirectory = (path: string) => {
    setModalState((current) => {
      if (!current) {
        return current
      }
      if (current.sourcePaths.includes(path) || current.workspacePath === path) {
        return current
      }
      return {
        ...current,
        sourcePaths: [...current.sourcePaths, path],
      }
    })
  }

  const removeLinkedDirectory = (path: string) => {
    if (!modalState) {
      return
    }
    if (modalState.mode === 'create') {
      setModalState({
        ...modalState,
        sourcePaths: modalState.sourcePaths.filter((value) => value !== path),
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
            sourcePaths: current.sourcePaths.filter((value) => value !== path),
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

    const normalizedSources = modalState.sourcePaths.map((value) => value.trim()).filter((value) => value !== '')
    const linkedDirectories = normalizedSources.filter((value) => value !== workspacePath)

    try {
      await saveWorkspace({
        path: workspacePath,
        name: draftName,
        themeId: modalState.themeId,
        makeCurrent: modalState.mode === 'edit' ? Boolean(editingWorkspace?.active || currentWorkspacePath === workspacePath) : false,
        linkedDirectories,
      })
      closeModal()
    } catch (err) {
      setModalError(err instanceof Error ? err.message : 'Failed to save workspace')
    }
  }

  return (
    <main className="flex min-h-screen w-full flex-col gap-4 bg-[linear-gradient(180deg,var(--app-bg),color-mix(in_oklab,var(--app-bg)_72%,var(--app-surface-subtle)))] px-4 py-6 sm:px-6 lg:px-8">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-8 p-4 sm:p-6 lg:p-8">
        <div className="flex flex-col gap-4 bg-transparent px-0 py-2 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Workspaces</h1>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">Select a project to begin working</p>
          </div>
          <div className="flex items-center gap-3">
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
          <div className="flex flex-col gap-10">
            <div className="flex flex-col gap-4">
              <div className="flex items-center justify-between">
                <h2 className="text-sm font-medium uppercase tracking-wider text-[var(--app-text)]">Saved ({workspaces.length})</h2>
              </div>
              {workspaces.length === 0 ? (
                <WorkspaceStatus kind="empty" title="No saved workspaces" message="Find a folder below to create your first workspace." />
              ) : (
                <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                  {workspaces.map((workspace, index) => (
                    <WorkspaceCard
                      key={workspace.path}
                      workspace={workspace}
                      position={index + 1}
                      current={currentWorkspacePath === workspace.path || workspace.active}
                      busy={selectingPath === workspace.path || savingPath === workspace.path}
                      dragging={draggingWorkspacePath === workspace.path}
                      onOpen={handleOpenWorkspace}
                      onEdit={startEdit}
                      onDelete={setDeleteTargetPath}
                      onToggleWorktree={(path, enabled) => {
                        void setWorktreeEnabled(path, enabled)
                      }}
                      onMoveToIndex={(path, indexToMove) => {
                        void moveWorkspaceToIndex(path, indexToMove)
                      }}
                      onDraggingChange={setDraggingWorkspacePath}
                      density="compact"
                    />
                  ))}
                </div>
              )}
            </div>

            <div className="flex flex-col gap-4 border-t border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] pt-6">
              <div className="grid gap-1">
                <h2 className="text-sm font-medium uppercase tracking-wider text-[var(--app-text)]">Local Folders ({discovered.length})</h2>
                <p className="text-sm text-[var(--app-text-muted)]">Pick a folder below to add it as a workspace.</p>
              </div>
              {discovered.length === 0 ? (
                <WorkspaceStatus kind="empty" title="No candidate folders" message="No repositories found in scanned locations." />
              ) : (
                <div className="grid gap-4 lg:grid-cols-2">
                  {discovered.map((entry) => {
                    const meta = [entry.hasSwarm ? 'AGENTS.md' : null, entry.hasClaude ? 'CLAUDE.md' : null, entry.isGitRepo ? 'git repo' : null]
                      .filter(Boolean)
                      .join(', ')

                    return (
                      <div
                        key={entry.path}
                        className="flex flex-col gap-4 rounded-lg border border-[color-mix(in_oklab,var(--app-border)_78%,var(--app-border-accent))] bg-[color-mix(in_oklab,var(--app-surface)_88%,var(--app-surface-subtle))] p-4 transition-colors hover:border-[var(--app-border-accent)] hover:bg-[color-mix(in_oklab,var(--app-surface)_82%,var(--app-surface-hover))] sm:flex-row sm:items-center sm:justify-between"
                      >
                        <div className="flex min-w-0 flex-1 items-start gap-3">
                          <button
                            type="button"
                            onClick={() => {
                              void browsePath(entry.path)
                            }}
                            className="flex size-9 shrink-0 items-center justify-center rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)] transition-colors hover:bg-[var(--app-surface-hover)]"
                          >
                            <span className="sr-only">Browse</span>
                            <FolderOpen size={16} aria-hidden="true" />
                          </button>
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <h3 className="truncate text-sm font-medium text-[var(--app-text)]">{entry.name}</h3>
                              {meta ? <span className="truncate text-xs text-[var(--app-text-subtle)]">• {meta}</span> : null}
                            </div>
                            <span className="mt-0.5 block truncate text-xs text-[var(--app-text-subtle)]" title={entry.path}>
                              {entry.path}
                            </span>
                          </div>
                        </div>
                        <div className="flex shrink-0 items-center gap-2">
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={savingPath === entry.path}
                            onClick={() => {
                              void handleUseFolderTemporarily(entry.path)
                            }}
                            className="rounded-md"
                          >
                            Use Temp
                          </Button>
                          <Button
                            size="sm"
                            disabled={savingPath === entry.path}
                            onClick={() => openCreateModal(entry.path, [entry.path], entry.name)}
                            className="rounded-md border border-[var(--app-border)] bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-sm hover:bg-[var(--app-surface-hover)]"
                          >
                            {savingPath === entry.path ? 'Saving...' : 'Add'}
                          </Button>
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}

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
              />
            </div>

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
          </div>
        ) : null}
      </div>

      <WorkspaceEditorModal
        open={Boolean(modalState?.open)}
        mode={modalState?.mode ?? 'create'}
        workspacePath={modalState?.workspacePath ?? ''}
        workspacePathEditable={modalState?.workspacePathEditable ?? true}
        name={draftName}
        themeId={modalState?.themeId ?? 'inherit'}
        linkedDirectories={modalState?.sourcePaths.filter((path) => path !== modalState.workspacePath) ?? []}
        availableDirectories={modalAvailableDirectories}
        workspaces={workspaces}
        canRemoveLinkedDirectories={Boolean(modalState)}
        error={modalError}
        saving={Boolean(savingPath && modalState?.workspacePath && savingPath === modalState.workspacePath)}
        onWorkspacePathChange={(value) => setModalState((current) => (current ? { ...current, workspacePath: value } : current))}
        onNameChange={setDraftName}
        onThemeIdChange={setThemeId}
        onSelectWorkspace={startEdit}
        onMoveWorkspaceToIndex={(path, index) => {
          void moveWorkspaceToIndex(path, index)
        }}
        onAddLinkedDirectory={addLinkedDirectory}
        onRemoveLinkedDirectory={removeLinkedDirectory}
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
    </main>
  )
}
