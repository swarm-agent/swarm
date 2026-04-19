import type { CSSProperties, DragEvent } from 'react'
import { Folder, FolderOpen, ListChecks, Pencil, Trash2 } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { cn } from '../../../../lib/cn'
import { formatWorkspaceDirectories } from '../services/workspace-format'
import { createWorkspaceAccentStyle } from '../services/workspace-theme'
import type { WorkspaceEntry } from '../types/workspace'
import { WorkspaceWorktreeToggle } from './workspace-worktree-toggle'

interface WorkspaceCardProps {
  workspace: WorkspaceEntry
  position: number
  current: boolean
  busy: boolean
  dragging?: boolean
  onOpen: (path: string) => void
  onEdit?: (path: string) => void
  onDelete?: (path: string) => void
  onToggleWorktree?: (path: string, enabled: boolean) => void
  onMoveToIndex?: (path: string, index: number) => void
  onDraggingChange?: (path: string | null) => void
  density?: 'comfortable' | 'compact'
}

export function WorkspaceCard({
  workspace,
  position,
  current,
  busy,
  dragging = false,
  onOpen,
  onEdit,
  onDelete,
  onToggleWorktree,
  onMoveToIndex,
  onDraggingChange,
  density = 'comfortable',
}: WorkspaceCardProps) {
  const directories = formatWorkspaceDirectories(workspace.directories)
  const previewDirectories = directories.slice(0, density === 'compact' ? 2 : 3)
  const cardThemeStyle = createWorkspaceAccentStyle(workspace.themeId, '--workspace-card-theme') as CSSProperties

  const handleDragStart = (event: DragEvent<HTMLElement>) => {
    if (!onMoveToIndex || !onDraggingChange) {
      return
    }
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/workspace-path', workspace.path)
    onDraggingChange(workspace.path)
  }

  const handleDragOver = (event: DragEvent<HTMLElement>) => {
    if (!onMoveToIndex) {
      return
    }
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }

  const handleDrop = (event: DragEvent<HTMLElement>) => {
    if (!onMoveToIndex) {
      return
    }
    event.preventDefault()
    const sourcePath = event.dataTransfer.getData('text/workspace-path').trim()
    if (sourcePath === '') {
      return
    }
    onMoveToIndex(sourcePath, position - 1)
  }

  const handleDragEnd = () => {
    onDraggingChange?.(null)
  }

  return (
    <Card
      className={cn(
        'grid gap-4 border p-4 transition sm:p-5',
        current
          ? 'border-[var(--workspace-card-theme-border-strong,var(--app-border-strong))] bg-[color-mix(in_oklab,var(--workspace-card-theme-selection,var(--app-selection))_16%,var(--app-surface))]'
          : 'border-[color-mix(in_oklab,var(--workspace-card-theme-border-accent,var(--app-border-accent))_52%,var(--app-border))] bg-[color-mix(in_oklab,var(--workspace-card-theme-selection,var(--app-selection))_6%,var(--app-surface))] hover:border-[color-mix(in_oklab,var(--workspace-card-theme-border-strong,var(--app-border-strong))_72%,var(--app-border))] hover:bg-[color-mix(in_oklab,var(--workspace-card-theme-selection,var(--app-selection))_10%,var(--app-surface))]',
        dragging && 'scale-[1.01] opacity-80 shadow-[var(--shadow-card)]',
        density === 'compact' && 'gap-3 p-4',
      )}
      style={cardThemeStyle}
      draggable={Boolean(onMoveToIndex)}
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDrop={handleDrop}
      onDragEnd={handleDragEnd}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-1 items-start gap-3">
          <div className="flex size-10 shrink-0 items-center justify-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-elevated)] text-[var(--app-text)]">
            {position}
          </div>
          <div className="flex size-10 shrink-0 items-center justify-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]">
            <Folder size={18} />
          </div>
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-base font-semibold text-[var(--app-text)]">{workspace.workspaceName}</h2>
            <p className="truncate text-sm text-[var(--app-text-muted)]" title={previewDirectories[0] ?? workspace.path}>
              {previewDirectories[0] ?? workspace.path}
            </p>
          </div>
        </div>
        {onToggleWorktree ? (
          <WorkspaceWorktreeToggle
            className="shrink-0 self-start"
            enabled={workspace.worktreeEnabled}
            busy={busy}
            onToggle={() => onToggleWorktree(workspace.path, !workspace.worktreeEnabled)}
          />
        ) : null}
      </div>

      <div className="grid gap-2">
        <div className="flex flex-wrap gap-2">
          <div className="inline-flex items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1 text-xs font-medium text-[var(--app-text-muted)]">
            <ListChecks size={12} />
            <span>{workspace.todoSummary?.taskCount ?? 0} tasks</span>
          </div>
          {typeof workspace.todoSummary?.user?.openCount === 'number' ? (
            <div className="inline-flex items-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1 text-xs font-medium text-[var(--app-text-muted)]">
              {workspace.todoSummary.user.openCount} user open
            </div>
          ) : null}
        </div>
        {previewDirectories.map((directory) => (
          <div key={directory} className="flex items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
            <FolderOpen size={14} className="shrink-0" />
            <span className="truncate" title={directory}>{directory}</span>
          </div>
        ))}
        {directories.length > previewDirectories.length ? (
          <div className="rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 py-2 text-sm text-[var(--app-text-subtle)]">
            +{directories.length - previewDirectories.length} more
          </div>
        ) : null}
        {directories.length > 1 ? (
          <div className="inline-flex w-fit items-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1 text-xs font-medium text-[var(--app-text-muted)]">
            {directories.length - 1} linked {directories.length === 2 ? 'folder' : 'folders'}
          </div>
        ) : null}
      </div>

      <div className="flex flex-wrap gap-3">
        <Button type="button" onClick={() => onOpen(workspace.path)} disabled={busy}>
          {busy ? 'Opening…' : 'Open'}
        </Button>
        {onEdit ? (
          <Button type="button" onClick={() => onEdit(workspace.path)} disabled={busy}>
            <Pencil size={14} />
            Edit
          </Button>
        ) : null}
        {onDelete ? (
          <Button type="button" variant="ghost" onClick={() => onDelete(workspace.path)} disabled={busy}>
            <Trash2 size={14} />
            Remove from Swarm
          </Button>
        ) : null}
      </div>
    </Card>
  )
}
