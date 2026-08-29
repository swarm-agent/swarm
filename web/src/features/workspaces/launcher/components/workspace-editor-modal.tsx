import { useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { ArrowUp, ChevronDown, ChevronRight, Folder, FolderPlus, Home, RefreshCw, Search, Sparkles, Trash2 } from 'lucide-react'
import { Card } from '../../../../components/ui/card'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { cn } from '../../../../lib/cn'
import { formatWorkspacePath } from '../services/workspace-format'
import { createWorkspaceThemeStyle, WORKSPACE_THEME_OPTIONS } from '../services/workspace-theme'
import type { WorkspaceBrowseResult, WorkspaceEntry } from '../types/workspace'
import { WorkspaceDefinitionStatus } from './workspace-definition-status'

export interface WorkspaceEditorAvailableDirectory {
  path: string
  name: string
  meta: string
}

type FolderPickerMode = 'workspace-folder' | null

interface WorkspaceEditorModalProps {
  open: boolean
  mode: 'create' | 'edit'
  workspacePath: string
  workspacePathEditable: boolean
  name: string
  themeId: string
  availableDirectories: WorkspaceEditorAvailableDirectory[]
  workspaces?: WorkspaceEntry[]
  browser?: WorkspaceBrowseResult | null
  browserLoading?: boolean
  browserError?: string | null
  error: string | null
  saving: boolean
  workspace?: WorkspaceEntry | null
  personalizing?: boolean
  personalizationMessage?: string | null
  onPersonalize?: () => void
  onWorkspacePathChange: (value: string) => void
  onPickWorkspaceFolder?: (path: string) => void
  onNameChange: (value: string) => void
  onThemeIdChange: (value: string) => void
  onBrowsePath?: (path: string) => void
  useExternalMobileFolderPicker?: boolean
  onRequestMobileFolderPicker?: (mode: Exclude<FolderPickerMode, null>) => void
  onCreateFolder?: (parentPath: string, name: string) => Promise<string>
  onSelectWorkspace?: (path: string) => void
  onMoveWorkspaceToIndex?: (path: string, index: number) => void
  onDeleteWorkspace?: (path: string) => void
  deleteConfirmationPath?: string | null
  deletingWorkspacePath?: string | null
  onCancelDeleteWorkspace?: () => void
  onConfirmDeleteWorkspace?: () => void
  onClose: () => void
  onSubmit: () => void
}

const INHERIT_THEME_ID = 'inherit'

function normalizeThemeId(themeId: string): string {
  const normalized = themeId.trim().toLowerCase()
  return normalized === '' ? INHERIT_THEME_ID : normalized
}

function workspaceThemeLabel(themeId: string): string {
  const normalized = normalizeThemeId(themeId)
  if (normalized === INHERIT_THEME_ID) {
    return 'Inherit (global)'
  }
  return WORKSPACE_THEME_OPTIONS.find((option) => option.id === normalized)?.label ?? themeId.trim()
}

function formatExplorerMeta(entry: Pick<WorkspaceBrowseResult['entries'][number], 'hasSwarm' | 'hasClaude' | 'isGitRepo'>) {
  const meta: string[] = []
  if (entry.hasSwarm) {
    meta.push('AGENTS.md')
  }
  if (entry.hasClaude) {
    meta.push('CLAUDE.md')
  }
  if (entry.isGitRepo) {
    meta.push('git repo')
  }
  return meta.join(' · ')
}

const fieldLabelClass = 'text-sm font-medium text-[var(--app-text)]'
const helperTextClass = 'text-sm leading-6 text-[var(--app-text-muted)]'
const inputClass = 'min-h-10 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text)] outline-none transition placeholder:text-[var(--app-text-subtle)] hover:border-[var(--app-border-strong)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] disabled:cursor-not-allowed disabled:bg-[var(--app-bg-inset)]'
const workspaceSelectorCardClass = 'flex min-w-[148px] flex-col gap-1 rounded-2xl border px-3 py-3 text-left transition'

export function WorkspaceEditorModal({
  open,
  mode,
  workspacePath,
  workspacePathEditable,
  name,
  themeId,
  workspaces = [],
  browser = null,
  browserLoading = false,
  browserError = null,
  error,
  saving,
  workspace = null,
  personalizing = false,
  personalizationMessage = null,
  onPersonalize,
  onWorkspacePathChange,
  onPickWorkspaceFolder,
  onNameChange,
  onThemeIdChange,
  onBrowsePath,
  useExternalMobileFolderPicker = false,
  onRequestMobileFolderPicker,
  onCreateFolder,
  onSelectWorkspace,
  onMoveWorkspaceToIndex,
  onDeleteWorkspace,
  deleteConfirmationPath = null,
  deletingWorkspacePath = null,
  onCancelDeleteWorkspace,
  onConfirmDeleteWorkspace,
  onClose,
  onSubmit,
}: WorkspaceEditorModalProps) {
  const [draggingWorkspacePath, setDraggingWorkspacePath] = useState<string | null>(null)
  const [folderPickerMode, setFolderPickerMode] = useState<FolderPickerMode>(null)
  const [folderPickerSearch, setFolderPickerSearch] = useState('')
  const [createdFolderName, setCreatedFolderName] = useState<string | null>(null)

  useEffect(() => {
    if (!open) {
      setFolderPickerMode(null)
      setFolderPickerSearch('')
    }
  }, [open])

  const normalizedThemeId = normalizeThemeId(themeId)
  const themePreviewStyle = createWorkspaceThemeStyle(normalizedThemeId === INHERIT_THEME_ID ? 'black' : normalizedThemeId, '--workspace-theme-preview')
  const themeOptions = [
    { id: INHERIT_THEME_ID, label: 'Inherit (global)' },
    ...WORKSPACE_THEME_OPTIONS,
  ]
  const selectedWorkspaceIndex = workspaces.findIndex((workspace) => workspace.path === workspacePath)
  const currentPath = browser?.resolvedPath ?? ''
  const currentPathLabel = currentPath ? formatWorkspacePath(currentPath) : '—'
  const visiblePickerEntries = useMemo(() => {
    const entries = browser?.entries ?? []
    const query = folderPickerSearch.trim().toLowerCase()
    if (!query) {
      return entries
    }
    return entries.filter((entry) => entry.name.toLowerCase().includes(query) || entry.path.toLowerCase().includes(query))
  }, [browser?.entries, folderPickerSearch])
  if (!open) {
    return null
  }

  const openFolderPicker = (nextMode: Exclude<FolderPickerMode, null>) => {
    if (useExternalMobileFolderPicker && onRequestMobileFolderPicker) {
      onRequestMobileFolderPicker(nextMode)
      return
    }
    setFolderPickerMode(nextMode)
    setFolderPickerSearch('')
    const startPath = workspacePath.trim() || currentPath
    if (onBrowsePath && startPath) {
      onBrowsePath(startPath)
    }
  }

  const closeFolderPicker = () => {
    setFolderPickerMode(null)
    setFolderPickerSearch('')
  }

  const createFolder = async () => {
    if (!currentPath || !onCreateFolder) {
      return
    }
    const folderName = window.prompt(`Name the new folder in ${currentPath}`)?.trim() ?? ''
    if (!folderName) {
      return
    }
    const createdPath = await onCreateFolder(currentPath, folderName)
    if (createdPath) {
      setCreatedFolderName(folderName)
      window.setTimeout(() => {
        setCreatedFolderName((value) => (value === folderName ? null : value))
      }, 3500)
    }
  }

  const useCurrentAsWorkspaceFolder = () => {
    if (!currentPath) {
      return
    }
    onPickWorkspaceFolder?.(currentPath)
    if (!onPickWorkspaceFolder) {
      onWorkspacePathChange(currentPath)
    }
    closeFolderPicker()
  }

  const renderFolderPicker = () => {
    if (!folderPickerMode) {
      return null
    }

    const title = 'Choose folder'
    const description = 'Navigate to the folder that should become the main workspace.'

    return (
      <aside className="flex min-h-[520px] min-w-0 flex-col border-t border-[var(--app-border)] bg-[color-mix(in_oklab,var(--app-surface)_58%,transparent)] md:w-[380px] md:border-l md:border-t-0 lg:w-[420px]">
        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-hidden px-4 py-4">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h3 className="text-[11px] font-semibold uppercase tracking-[0.18em] text-[var(--app-text)]">{title}</h3>
              <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">{description}</p>
            </div>
            <span className="rounded-full bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] text-[var(--app-text-subtle)]">
              {visiblePickerEntries.length}
            </span>
          </div>

          <div className="overflow-hidden rounded-lg border border-[color-mix(in_oklab,var(--app-border)_38%,transparent)] bg-[color-mix(in_oklab,var(--app-bg)_34%,transparent)] shadow-sm shadow-black/5">
            <div className="flex h-9 items-center gap-1 px-2">
              <div className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--app-text)]" title={currentPath || undefined}>
                Current: {currentPathLabel}
              </div>
              <ExplorerIconButton label="Home" disabled={!browser?.homePath || !onBrowsePath} onClick={() => onBrowsePath?.(browser?.homePath ?? '')}>
                <Home size={13} />
              </ExplorerIconButton>
              <ExplorerIconButton label="Up" disabled={!browser?.parentPath || !onBrowsePath} onClick={() => browser?.parentPath && onBrowsePath?.(browser.parentPath)}>
                <ArrowUp size={13} />
              </ExplorerIconButton>
              <ExplorerIconButton label="Refresh" disabled={browserLoading || !currentPath || !onBrowsePath} onClick={() => onBrowsePath?.(currentPath)}>
                <RefreshCw size={13} className={cn(browserLoading && 'animate-spin')} />
              </ExplorerIconButton>
            </div>
            <label className="flex h-9 items-center border-t border-[color-mix(in_oklab,var(--app-border)_28%,transparent)] px-2 text-xs transition focus-within:bg-[var(--app-surface-subtle)]">
              <Search size={13} className="mr-2 shrink-0 text-[var(--app-text-subtle)]" />
              <input
                value={folderPickerSearch}
                onChange={(event) => setFolderPickerSearch(event.target.value)}
                placeholder="Search folders…"
                className="h-full w-full border-0 bg-transparent p-0 text-xs text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)]"
              />
            </label>
          </div>

          {createdFolderName ? <div className="px-1 text-xs text-[var(--app-text-muted)]">Created “{createdFolderName}”</div> : null}
          {browserError ? <div className="rounded-lg border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{browserError}</div> : null}
          {browserLoading && !browser ? <div className="px-1 text-sm text-[var(--app-text-muted)]">Loading folders…</div> : null}

          <div className="flex min-h-0 flex-1 flex-col border-t border-[color-mix(in_oklab,var(--app-border)_34%,transparent)] pt-2">
            <button
              type="button"
              onClick={() => void createFolder()}
              disabled={browserLoading || !currentPath || !onCreateFolder}
              className="flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-xs text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50"
            >
              <FolderPlus size={14} className="shrink-0" />
              <span className="truncate">New folder</span>
            </button>

            <div className="mt-2 flex items-center justify-between px-1 text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">
              <span>Folders</span>
              <span>{visiblePickerEntries.length}</span>
            </div>

            {visiblePickerEntries.length === 0 ? (
              <div className="px-1 py-5 text-sm text-[var(--app-text-muted)]">
                {folderPickerSearch.trim() ? 'No matching folders.' : 'No folders found here.'}
              </div>
            ) : null}

            <div className="-mx-1 mt-1 min-h-0 flex-1 overflow-y-auto py-1">
              {visiblePickerEntries.map((entry) => {
                const meta = formatExplorerMeta(entry)
                return (
                  <div
                    key={entry.path}
                    className="group grid min-h-9 w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-1 rounded-md px-1 py-0.5 text-xs transition-colors hover:bg-[var(--app-surface-hover)] focus-within:bg-[var(--app-surface-hover)]"
                    title={formatWorkspacePath(entry.path)}
                  >
                    <button
                      type="button"
                      onClick={() => onBrowsePath?.(entry.path)}
                      className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-2 rounded px-1 py-1 text-left"
                    >
                      <Folder size={14} className="shrink-0 text-[var(--app-text-muted)]" />
                      <span className="min-w-0 truncate">
                        <span className="truncate font-medium text-[var(--app-text)]">{entry.name}</span>
                        {meta ? <span className="ml-2 truncate text-[11px] font-normal text-[var(--app-text-subtle)]">{meta}</span> : null}
                      </span>
                    </button>
                    <button
                      type="button"
                      className="flex size-7 items-center justify-center rounded-md text-[var(--app-text-subtle)] opacity-0 transition-colors hover:bg-[var(--app-surface-elevated)] hover:text-[var(--app-text)] group-hover:opacity-100 group-focus-within:opacity-100"
                      onClick={() => onBrowsePath?.(entry.path)}
                      aria-label={`Open ${entry.name}`}
                      title="Open folder"
                    >
                      <ChevronRight size={14} />
                    </button>
                  </div>
                )
              })}
            </div>
          </div>

        </div>
      </aside>
    )
  }

  return (
    <div className="fixed inset-0 z-[70] grid place-items-center p-3 sm:p-6" role="dialog" aria-modal="true" aria-label={mode === 'create' ? 'Create workspace' : 'Edit workspace'}>
      <div className="absolute inset-0 bg-[var(--app-backdrop)]" onClick={onClose} />
      <Card className={cn('relative z-10 flex max-h-[min(920px,calc(100vh-24px))] w-full flex-col overflow-hidden border-[var(--app-border)] shadow-[var(--shadow-panel)] max-sm:h-[calc(100dvh-16px)] max-sm:max-h-[calc(100dvh-16px)] max-sm:rounded-3xl', folderPickerMode ? 'max-w-6xl' : 'max-w-3xl')}>
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 sm:px-6">
          <div className="grid gap-1">
            <h2 className="text-xl font-semibold tracking-tight text-[var(--app-text)]">{mode === 'create' ? 'Create workspace' : 'Edit workspace'}</h2>
            <p className={helperTextClass}>
              {mode === 'create'
                ? 'Pick one folder to create a globally available workspace.'
                : 'Update this globally available workspace.'}
            </p>
          </div>
          <ModalCloseButton onClick={onClose} aria-label="Close workspace editor" />
        </div>

        <div className="flex min-h-0 flex-1 flex-col md:flex-row">
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-5 sm:px-6">
            <div className="grid gap-5">
              {mode === 'edit' && workspaces.length > 0 && onSelectWorkspace ? (
                <div className="grid gap-2">
                  <div className="flex items-center justify-between gap-3">
                    <span className={fieldLabelClass}>Workspaces</span>
                    {selectedWorkspaceIndex >= 0 ? (
                      <span className="text-xs text-[var(--app-text-muted)]">
                        {selectedWorkspaceIndex + 1} of {workspaces.length}
                      </span>
                    ) : null}
                  </div>
                  <div className="flex gap-2 overflow-x-auto pb-1" aria-label="Workspace switcher">
                    {workspaces.map((workspace, index) => {
                      const selected = workspace.path === workspacePath
                      const deleting = deletingWorkspacePath === workspace.path
                      return (
                        <div
                          key={workspace.path}
                          draggable={Boolean(onMoveWorkspaceToIndex) && !deleting}
                          onDragStart={(event) => {
                            if (!onMoveWorkspaceToIndex) {
                              return
                            }
                            event.dataTransfer.effectAllowed = 'move'
                            event.dataTransfer.setData('text/workspace-path', workspace.path)
                            setDraggingWorkspacePath(workspace.path)
                          }}
                          onDragOver={(event) => {
                            if (!onMoveWorkspaceToIndex) {
                              return
                            }
                            event.preventDefault()
                            event.dataTransfer.dropEffect = 'move'
                          }}
                          onDrop={(event) => {
                            if (!onMoveWorkspaceToIndex) {
                              return
                            }
                            event.preventDefault()
                            const sourcePath = event.dataTransfer.getData('text/workspace-path').trim()
                            if (sourcePath === '') {
                              return
                            }
                            onMoveWorkspaceToIndex(sourcePath, index)
                            setDraggingWorkspacePath(null)
                          }}
                          onDragEnd={() => {
                            setDraggingWorkspacePath(null)
                          }}
                          className={cn(
                            workspaceSelectorCardClass,
                            selected
                              ? 'border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] text-[var(--app-text)]'
                              : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:text-[var(--app-text)]',
                            draggingWorkspacePath === workspace.path && 'scale-[1.01] opacity-70 shadow-[var(--shadow-card)]',
                          )}
                        >
                          <button
                            type="button"
                            onClick={() => onSelectWorkspace(workspace.path)}
                            disabled={deleting}
                            className="grid w-full min-w-0 gap-1 text-left disabled:opacity-50"
                            aria-pressed={selected}
                          >
                            <span className="truncate text-sm font-semibold">{workspace.workspaceName}</span>
                            <span className="truncate text-xs">{formatWorkspacePath(workspace.path)}</span>
                          </button>
                        </div>
                      )
                    })}
                  </div>
                </div>
              ) : null}

              <section className="grid gap-3">
                <div>
                  <h3 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Main workspace</h3>
                </div>
                <label className="grid gap-2">
                  <span className={fieldLabelClass}>Workspace folder</span>
                  <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                    <input
                      value={workspacePath}
                      onChange={(event) => onWorkspacePathChange(event.target.value)}
                      placeholder="/path/to/folder"
                      disabled={!workspacePathEditable}
                      className={inputClass}
                    />
                    <Button type="button" variant="outline" onClick={() => openFolderPicker('workspace-folder')} disabled={!workspacePathEditable || !onBrowsePath}>
                      Browse
                    </Button>
                  </div>
                </label>

                <label className="grid gap-2">
                  <span className={fieldLabelClass}>Workspace name</span>
                  <input
                    value={name}
                    onChange={(event) => onNameChange(event.target.value)}
                    placeholder="Workspace name"
                    className={inputClass}
                  />
                </label>
              </section>

              {mode === 'edit' && workspace ? (
                <section className="grid gap-3">
                  <div className="grid gap-1 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
                    <div className="grid gap-1">
                      <h3 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Workspace definition</h3>
                      <p className={helperTextClass}>Router uses this definition to personalize how Swarm works here. Refreshing starts a fresh Router session for every saved workspace.</p>
                    </div>
                    {onPersonalize ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={onPersonalize}
                        disabled={personalizing || workspace.definitionStatus === 'pending'}
                        title="Start a fresh Router session for saved workspaces"
                      >
                        <Sparkles size={14} className={personalizing ? 'animate-pulse' : undefined} />
                        {personalizing ? 'Starting…' : workspace.definition ? 'Repersonalize' : 'Personalize'}
                      </Button>
                    ) : null}
                  </div>
                  <WorkspaceDefinitionStatus workspace={workspace} />
                  {!workspace.definitionStatus ? (
                    <p className={helperTextClass}>No workspace definition has been generated yet.</p>
                  ) : null}
                  {personalizationMessage ? (
                    <p role="status" className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-xs leading-5 text-[var(--app-text-muted)]">
                      {personalizationMessage}
                    </p>
                  ) : null}
                </section>
              ) : null}

              <section className="grid gap-2">
                <h3 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Theme</h3>
                <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(12rem,16rem)] sm:items-center" style={themePreviewStyle}>
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="size-6 rounded-full border border-[var(--workspace-theme-preview-border-strong,var(--app-border-strong))] bg-[var(--workspace-theme-preview-accent,var(--app-primary))] shadow-[var(--shadow-soft)]" />
                    <div className="min-w-0">
                      <strong className="block truncate text-sm font-medium text-[var(--app-text)]">{workspaceThemeLabel(normalizedThemeId)}</strong>
                      <small className="block truncate text-xs text-[var(--app-text-muted)]">
                        {normalizedThemeId === INHERIT_THEME_ID ? 'Uses the global web theme' : normalizedThemeId}
                      </small>
                    </div>
                  </div>
                  <div className="relative min-w-0">
                    <select
                      value={normalizedThemeId}
                      onChange={(event) => onThemeIdChange(event.target.value)}
                      className="min-h-10 w-full cursor-pointer appearance-none rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-border-accent)] focus:ring-2 focus:ring-[var(--app-focus-ring)]"
                    >
                      {themeOptions.map((option) => (
                        <option key={option.id} value={option.id}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                    <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                  </div>
                </div>
              </section>

              {mode === 'edit' && onDeleteWorkspace ? (
                deleteConfirmationPath === workspacePath ? (
                  <section className="grid gap-4 rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-4" aria-labelledby="remove-workspace-confirmation-title">
                    <div className="grid gap-2">
                      <h3 id="remove-workspace-confirmation-title" className="text-base font-semibold text-[var(--app-text)]">Remove workspace from Swarm?</h3>
                      <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                        This only removes Swarm’s saved workspace metadata. It does not delete the folder or any files on disk.
                      </p>
                    </div>
                    <div className="break-all rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text)]">
                      {deleteConfirmationPath}
                    </div>
                    <div className="flex flex-wrap justify-end gap-3">
                      <Button type="button" onClick={onCancelDeleteWorkspace} disabled={deletingWorkspacePath === workspacePath}>
                        Cancel
                      </Button>
                      <Button type="button" onClick={onConfirmDeleteWorkspace} disabled={deletingWorkspacePath === workspacePath}>
                        {deletingWorkspacePath === workspacePath ? <RefreshCw size={14} className="animate-spin" /> : <Trash2 size={14} />}
                        {deletingWorkspacePath === workspacePath ? 'Removing…' : 'Remove from Swarm'}
                      </Button>
                    </div>
                  </section>
                ) : (
                  <div>
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => onDeleteWorkspace(workspacePath)}
                      disabled={deletingWorkspacePath === workspacePath}
                      className="border-[var(--app-danger-border)] text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)]"
                    >
                      {deletingWorkspacePath === workspacePath ? <RefreshCw size={14} className="animate-spin" /> : <Trash2 size={14} />}
                      {deletingWorkspacePath === workspacePath ? 'Deleting…' : 'Delete workspace'}
                    </Button>
                  </div>
                )
              ) : null}

            </div>
          </div>

          {renderFolderPicker()}
        </div>

        {error ? <p className="border-t border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-5 py-3 text-sm text-[var(--app-danger)] sm:px-6">{error}</p> : null}

        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--app-border)] px-5 py-4 sm:px-6">
          {folderPickerMode === 'workspace-folder' ? (
            <span className="truncate text-xs text-[var(--app-text-muted)]" title={currentPath || undefined}>{currentPath ? `Current: ${currentPathLabel}` : 'Choose a folder'}</span>
          ) : (
            <span />
          )}
          <div className="flex flex-wrap justify-end gap-3">
            <Button type="button" onClick={folderPickerMode ? closeFolderPicker : onClose}>
              Cancel
            </Button>
            {folderPickerMode === 'workspace-folder' ? (
              <Button type="button" onClick={useCurrentAsWorkspaceFolder} disabled={!currentPath || browserLoading}>
                Use as workspace folder
              </Button>
            ) : (
              <Button type="button" onClick={onSubmit} disabled={saving}>
                {saving ? 'Saving…' : mode === 'create' ? 'Create workspace' : 'Save workspace'}
              </Button>
            )}
          </div>
        </div>
      </Card>
    </div>
  )
}

function ExplorerIconButton({
  label,
  disabled,
  onClick,
  children,
}: {
  label: string
  disabled?: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className="inline-flex size-7 shrink-0 items-center justify-center rounded-md text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
    >
      {children}
    </button>
  )
}
