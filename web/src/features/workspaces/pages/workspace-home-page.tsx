import { useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { RefreshCw } from 'lucide-react'
import { Card } from '../../../components/ui/card'
import { Button } from '../../../components/ui/button'
import { Badge } from '../../../components/ui/badge'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { WorkspaceFolderTree } from '../launcher/components/workspace-folder-tree'
import { SavedWorkspaceSection } from '../launcher/pages/iterations/shared'
import { WorkspaceStatus } from '../launcher/components/workspace-status'
import { WorkspaceEditorModal, type WorkspaceEditorAvailableDirectory } from '../launcher/components/workspace-editor-modal'
import { useWorkspaceLauncher } from '../launcher/state/use-workspace-launcher'
import { buildWorkspaceRouteSlugMap, workspaceRouteSlugBase } from '../launcher/services/workspace-route'

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
    openWorkspace,
    useFolderTemporarily,
    deleteWorkspace,
    unlinkWorkspaceDirectory,
    setWorktreeEnabled,
    saveWorkspace,
    moveWorkspaceToIndex,
    setDraggingWorkspacePath,
    refresh,
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
  const currentWorkspaceLinkedDirectories = useMemo(
    () => currentWorkspace?.directories.filter((path) => path !== currentWorkspace.path) ?? [],
    [currentWorkspace],
  )

  const temporaryFolderActive = Boolean(currentWorkspacePath && !currentWorkspace)
  const temporaryFolderName = temporaryFolderActive && currentWorkspacePath ? fallbackWorkspaceNameFromPath(currentWorkspacePath) : ''
  const hasSavedWorkspaces = workspaces.length > 0

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
      const workspaceSlug = workspaceSlugByPath.get(resolvedPath)
        ?? workspaceRouteSlugBase({ path: resolvedPath, workspaceName: resolution.workspaceName })
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
    <main className="mx-auto flex w-full max-w-[1440px] flex-col gap-4 px-4 py-6 sm:px-6 lg:px-8">
      <div className="border-b border-[var(--app-border)] pb-3">
        <h1 className="text-xl font-semibold tracking-tight text-[var(--app-text)] sm:text-2xl">Workspace Launcher</h1>
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

      {!loading && actionError ? (
        <WorkspaceStatus kind="error" title="Workspace action failed" message={actionError} />
      ) : null}

      {!loading && !loadError ? (
        <>
          <SavedWorkspaceSection
            workspaces={workspaces}
            currentWorkspacePath={currentWorkspacePath}
            selectingPath={selectingPath}
            savingPath={savingPath}
            draggingWorkspacePath={draggingWorkspacePath}
            onOpenWorkspace={handleOpenWorkspace}
            onEditWorkspace={startEdit}
            onDeleteWorkspace={setDeleteTargetPath}
            onToggleWorktree={(path, enabled) => {
              void setWorktreeEnabled(path, enabled)
            }}
            onMoveWorkspaceToIndex={(path, index) => {
              void moveWorkspaceToIndex(path, index)
            }}
            onDraggingWorkspaceChange={setDraggingWorkspacePath}
            title={hasSavedWorkspaces ? 'Saved workspaces' : 'Welcome to Swarm'}
            description={
              hasSavedWorkspaces
                ? 'Your saved workspaces. Drag to reorder or remove Swarm metadata without touching files on disk.'
                : 'Create a saved workspace to keep sessions attached to a project, or use a folder temporarily if you just want to jump in.'
            }
            columns={3}
          />

          <WorkspaceFolderTree
            currentWorkspacePath={currentWorkspacePath}
            selectingPath={selectingPath}
            savingPath={savingPath}
            workspaces={workspaces}
            onOpenWorkspace={handleOpenWorkspace}
            onUseFolderTemporarily={handleUseFolderTemporarily}
            onCreateWorkspace={(path, name) => openCreateModal(path, [path], name)}
          />

          {currentWorkspacePath ? (
            <Card className="grid gap-3 px-5 py-5 sm:px-6">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-lg font-semibold text-[var(--app-text)]">Current folder</h2>
                {temporaryFolderActive ? <Badge tone="warning">Temporary</Badge> : <Badge tone="live">Saved workspace</Badge>}
              </div>
              <div className="break-all text-sm text-[var(--app-text)]">{currentWorkspacePath}</div>
              {temporaryFolderActive ? (
                <div>
                  <Button type="button" onClick={handlePromoteTemporaryFolder}>
                    Make workspace
                  </Button>
                </div>
              ) : currentWorkspaceLinkedDirectories.length > 0 ? (
                <div className="grid gap-2">
                  <div className="text-xs font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">
                    Linked folders · {currentWorkspaceLinkedDirectories.length}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    {currentWorkspaceLinkedDirectories.map((directory) => (
                      <Badge key={directory} tone="neutral">{directory}</Badge>
                    ))}
                  </div>
                </div>
              ) : null}
            </Card>
          ) : null}
        </>
      ) : null}

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
