import { useState } from 'react'
import { Card } from '../../../../components/ui/card'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { formatWorkspacePath } from '../services/workspace-format'
import { createWorkspaceThemeStyle, WORKSPACE_THEME_OPTIONS } from '../services/workspace-theme'
import type { WorkspaceEntry } from '../types/workspace'
import { ChevronDown } from 'lucide-react'

export interface WorkspaceEditorAvailableDirectory {
  path: string
  name: string
  meta: string
}

interface WorkspaceEditorModalProps {
  open: boolean
  mode: 'create' | 'edit'
  workspacePath: string
  workspacePathEditable: boolean
  name: string
  themeId: string
  linkedDirectories: string[]
  availableDirectories: WorkspaceEditorAvailableDirectory[]
  workspaces?: WorkspaceEntry[]
  canRemoveLinkedDirectories?: boolean
  error: string | null
  saving: boolean
  onWorkspacePathChange: (value: string) => void
  onNameChange: (value: string) => void
  onThemeIdChange: (value: string) => void
  onSelectWorkspace?: (path: string) => void
  onMoveWorkspaceToIndex?: (path: string, index: number) => void
  onAddLinkedDirectory: (path: string) => void
  onRemoveLinkedDirectory: (path: string) => void
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

const fieldLabelClass = 'text-sm font-medium text-[var(--app-text)]'
const helperTextClass = 'text-sm leading-6 text-[var(--app-text-muted)]'
const listRowClass = 'flex flex-col gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] p-3 sm:flex-row sm:items-center sm:justify-between'
const subtleCardClass = 'grid gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4'
const workspaceSelectorCardClass = 'flex min-w-[148px] flex-col gap-1 rounded-2xl border px-3 py-3 text-left transition'

export function WorkspaceEditorModal({
  open,
  mode,
  workspacePath,
  workspacePathEditable,
  name,
  themeId,
  linkedDirectories,
  availableDirectories,
  workspaces = [],
  canRemoveLinkedDirectories = false,
  error,
  saving,
  onWorkspacePathChange,
  onNameChange,
  onThemeIdChange,
  onSelectWorkspace,
  onMoveWorkspaceToIndex,
  onAddLinkedDirectory,
  onRemoveLinkedDirectory,
  onClose,
  onSubmit,
}: WorkspaceEditorModalProps) {
  if (!open) {
    return null
  }

  const [draggingWorkspacePath, setDraggingWorkspacePath] = useState<string | null>(null)
  const normalizedThemeId = normalizeThemeId(themeId)
  const themePreviewStyle = createWorkspaceThemeStyle(normalizedThemeId === INHERIT_THEME_ID ? 'black' : normalizedThemeId, '--workspace-theme-preview')
  const themeOptions = [
    { id: INHERIT_THEME_ID, label: 'Inherit (global)' },
    ...WORKSPACE_THEME_OPTIONS,
  ]
  const selectedWorkspaceIndex = workspaces.findIndex((workspace) => workspace.path === workspacePath)

  return (
    <div className="fixed inset-0 z-[70] grid place-items-center p-4 sm:p-6" role="dialog" aria-modal="true" aria-label={mode === 'create' ? 'Create workspace' : 'Edit workspace'}>
      <div className="absolute inset-0 bg-[var(--app-backdrop)]" onClick={onClose} />
      <Card className="relative z-10 flex max-h-[min(920px,calc(100vh-32px))] w-full max-w-4xl flex-col overflow-hidden border-[var(--app-border)] shadow-[var(--shadow-panel)]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 sm:px-6">
          <div className="grid gap-1">
            <h2 className="text-xl font-semibold tracking-tight text-[var(--app-text)]">{mode === 'create' ? 'Create workspace' : 'Edit workspace'}</h2>
            <p className={helperTextClass}>
              {mode === 'create'
                ? 'Choose the main folder, then add other folders you want in the same workspace.'
                : 'Update the workspace name or add more folders.'}
            </p>
          </div>
          <ModalCloseButton onClick={onClose} aria-label="Close workspace editor" />
        </div>

        <div className="grid gap-5 overflow-y-auto px-5 py-5 sm:px-6">
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
                  return (
                    <button
                      key={workspace.path}
                      type="button"
                      onClick={() => onSelectWorkspace(workspace.path)}
                      draggable={Boolean(onMoveWorkspaceToIndex)}
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
                      className={[
                        workspaceSelectorCardClass,
                        selected
                          ? 'border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] text-[var(--app-text)]'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:text-[var(--app-text)]',
                        draggingWorkspacePath === workspace.path ? 'scale-[1.01] opacity-70 shadow-[var(--shadow-card)]' : '',
                      ].join(' ')}
                      aria-pressed={selected}
                    >
                      <span className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                        {index + 1}
                      </span>
                      <span className="truncate text-sm font-semibold">{workspace.workspaceName}</span>
                      <span className="truncate text-xs">{formatWorkspacePath(workspace.path)}</span>
                    </button>
                  )
                })}
              </div>
            </div>
          ) : null}

          <label className="grid gap-2">
            <span className={fieldLabelClass}>Workspace folder</span>
            <input
              value={workspacePath}
              onChange={(event) => onWorkspacePathChange(event.target.value)}
              placeholder="/path/to/folder"
              disabled={!workspacePathEditable}
              className="min-h-11 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5 text-[var(--app-text)] outline-none transition placeholder:text-[var(--app-text-subtle)] hover:border-[var(--app-border-strong)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] disabled:cursor-not-allowed disabled:bg-[var(--app-bg-inset)]"
            />
          </label>

          <label className="grid gap-2">
            <span className={fieldLabelClass}>Name</span>
            <input
              value={name}
              onChange={(event) => onNameChange(event.target.value)}
              placeholder="Workspace name"
              className="min-h-11 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5 text-[var(--app-text)] outline-none transition placeholder:text-[var(--app-text-subtle)] hover:border-[var(--app-border-strong)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
            />
          </label>

          <div className="grid gap-2">
            <span className={fieldLabelClass}>Theme</span>
            <div className="grid gap-3 rounded-2xl border border-[var(--workspace-theme-preview-border,var(--app-border))] bg-[var(--workspace-theme-preview-surface,var(--app-surface))] p-4 text-[var(--workspace-theme-preview-text,var(--app-text))]" style={themePreviewStyle}>
              <div className="flex items-center gap-3 rounded-2xl border border-[var(--workspace-theme-preview-border-strong,var(--app-border-strong))] bg-[var(--workspace-theme-preview-surface-elevated,var(--app-surface-elevated))] px-4 py-3">
                <div className="size-10 rounded-full border border-[var(--workspace-theme-preview-border-strong,var(--app-border-strong))] bg-[var(--workspace-theme-preview-accent,var(--app-primary))] shadow-[var(--shadow-soft)]" />
                <div className="grid gap-0.5 min-w-0 flex-1">
                  <strong className="text-sm font-semibold text-[var(--workspace-theme-preview-text,var(--app-text))]">{workspaceThemeLabel(normalizedThemeId)}</strong>
                  <small className="text-xs text-[var(--workspace-theme-preview-text-muted,var(--app-text-muted))]">
                    {normalizedThemeId === INHERIT_THEME_ID ? 'Uses the global web theme' : normalizedThemeId}
                  </small>
                </div>
                <div className="relative w-full max-w-56 shrink-0">
                  <select
                    value={normalizedThemeId}
                    onChange={(event) => onThemeIdChange(event.target.value)}
                    className="w-full appearance-none rounded-lg border border-[var(--workspace-theme-preview-border,var(--app-border))] bg-[var(--workspace-theme-preview-surface,var(--app-bg))] px-3 py-2 pr-8 text-sm font-medium text-[var(--workspace-theme-preview-text,var(--app-text))] outline-none transition-colors hover:border-[var(--workspace-theme-preview-border-strong,var(--app-border-strong))] focus:border-[var(--workspace-theme-preview-border-accent,var(--app-primary))] focus:ring-1 focus:ring-[var(--workspace-theme-preview-border-accent,var(--app-primary))] cursor-pointer"
                  >
                    {themeOptions.map((option) => (
                      <option key={option.id} value={option.id}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                  <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--workspace-theme-preview-text-muted,var(--app-text-muted))]" />
                </div>
              </div>
            </div>
          </div>

          <div className="grid gap-3">
            <span className={fieldLabelClass}>Linked folders</span>
            <div className={subtleCardClass}>
              <strong className="text-sm font-semibold text-[var(--app-text)]">Why add another folder?</strong>
              <p className={helperTextClass}>Add another folder when one workspace needs files from more than one place.</p>
            </div>
            {linkedDirectories.length > 0 ? (
              <div className="grid gap-3">
                {linkedDirectories.map((path) => (
                  <div key={path} className={listRowClass}>
                    <span className="min-w-0 truncate text-sm text-[var(--app-text)]" title={path}>{formatWorkspacePath(path)}</span>
                    {canRemoveLinkedDirectories ? (
                      <Button type="button" onClick={() => onRemoveLinkedDirectory(path)}>Remove</Button>
                    ) : null}
                  </div>
                ))}
              </div>
            ) : (
              <p className={helperTextClass}>No extra folders linked yet.</p>
            )}
          </div>

          <div className="grid gap-3">
            <span className={fieldLabelClass}>Available folders</span>
            {availableDirectories.length > 0 ? (
              <div className="grid gap-3">
                {availableDirectories.map((entry) => (
                  <div key={entry.path} className={listRowClass}>
                    <div className="grid min-w-0 gap-1">
                      <strong className="truncate text-sm font-semibold text-[var(--app-text)]">{entry.name}</strong>
                      <span className="truncate text-sm text-[var(--app-text-muted)]" title={entry.path}>{formatWorkspacePath(entry.path)}</span>
                      <small className="text-xs text-[var(--app-text-subtle)]">{entry.meta || 'Folder detected'}</small>
                    </div>
                    <Button type="button" onClick={() => onAddLinkedDirectory(entry.path)}>Add folder</Button>
                  </div>
                ))}
              </div>
            ) : (
              <p className={helperTextClass}>No other discovered folders are available to add right now.</p>
            )}
          </div>
        </div>

        {error ? <p className="border-t border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-5 py-3 text-sm text-[var(--app-danger)] sm:px-6">{error}</p> : null}

        <div className="flex flex-wrap justify-end gap-3 border-t border-[var(--app-border)] px-5 py-4 sm:px-6">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="button" onClick={onSubmit} disabled={saving}>
            {saving ? 'Saving…' : mode === 'create' ? 'Create workspace' : 'Save workspace'}
          </Button>
        </div>
      </Card>
    </div>
  )
}
