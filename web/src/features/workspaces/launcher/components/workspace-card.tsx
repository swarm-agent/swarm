import { useState, type CSSProperties, type DragEvent } from 'react'
import { ChevronDown, Folder, ListChecks, Pencil, Trash2 } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { cn } from '../../../../lib/cn'
import type { SwarmTarget } from '../../../desktop/swarm/api/swarm-targets'
import {
  workspaceRouteDisplayPath,
  workspaceRouteHoverTitle,
  workspaceRouteTargetName,
  workspacePlacementLinks,
} from '../services/workspace-placement'
import { formatWorkspaceDirectories } from '../services/workspace-format'
import { createWorkspaceAccentStyle } from '../services/workspace-theme'
import type { WorkspaceEntry } from '../types/workspace'
import { WorkspaceWorktreeToggle } from './workspace-worktree-toggle'
import { WorkspaceDefinitionStatus } from './workspace-definition-status'

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
  availableSwarmTargets?: SwarmTarget[]
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
  availableSwarmTargets = [],
  density = 'comfortable',
}: WorkspaceCardProps) {
  const directories = formatWorkspaceDirectories(workspace.directories)
  const cardThemeStyle = createWorkspaceAccentStyle(workspace.themeId, '--workspace-card-theme') as CSSProperties
  const placementLinks = workspacePlacementLinks(workspace.topologyRoutes, availableSwarmTargets)
  const [linksExpanded, setLinksExpanded] = useState(false)
  const collapseLinks = placementLinks.length > 2 && !linksExpanded
  const visiblePlacementLinks = collapseLinks ? placementLinks.slice(0, 2) : placementLinks
  const hiddenLinkCount = Math.max(placementLinks.length - visiblePlacementLinks.length, 0)

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
    <div
      className={cn(
        'group flex flex-col rounded-lg border bg-[var(--app-surface)] shadow-sm transition-all',
        density === 'compact' ? 'gap-3 p-3.5' : 'gap-4 p-4',
        current
          ? 'border-[var(--workspace-card-theme-border-strong,var(--app-border-strong))] bg-[color-mix(in_oklab,var(--workspace-card-theme-selection,var(--app-primary))_8%,var(--app-surface))]'
          : 'border-[var(--app-border)] hover:border-[var(--workspace-card-theme-border-accent,var(--app-border-accent))]',
        dragging && 'opacity-50',
      )}
      style={cardThemeStyle}
      draggable={Boolean(onMoveToIndex)}
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDrop={handleDrop}
      onDragEnd={handleDragEnd}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="flex items-center gap-2">
            <h3 className="truncate font-medium text-[var(--app-text)]">{workspace.workspaceName}</h3>
            {current ? (
              <span className="rounded bg-[var(--app-surface-elevated)] px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
                Active
              </span>
            ) : null}
          </div>
          <span className="truncate text-xs text-[var(--app-text-subtle)]" title={directories[0] ?? workspace.path}>
            {directories[0] ?? workspace.path}
          </span>
        </div>
        {onToggleWorktree ? (
          <WorkspaceWorktreeToggle
            className="shrink-0"
            enabled={workspace.worktreeEnabled}
            busy={busy}
            onToggle={() => onToggleWorktree(workspace.path, !workspace.worktreeEnabled)}
          />
        ) : null}
      </div>

      <WorkspaceDefinitionStatus workspace={workspace} compact={density === 'compact'} />

      <div className="grid gap-2">
        <div className="flex flex-wrap gap-2">
          <div className="flex items-center gap-1.5 rounded-md border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2 py-1 text-xs text-[var(--app-text-muted)]">
            <ListChecks size={14} />
            {workspace.todoSummary?.user.taskCount ?? 0}
          </div>
          {directories.length > 1 ? (
            <div className="flex items-center gap-1.5 rounded-md border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2 py-1 text-xs text-[var(--app-text-muted)]">
              <Folder size={14} />
              {directories.length}
            </div>
          ) : null}
        </div>

        <div className="grid gap-1.5">
          <span className="text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">Available on</span>
          {placementLinks.length > 0 ? (
            <div className="grid gap-1.5">
              {visiblePlacementLinks.map(({ route, target, targetType }) => {
                const displayPath = workspaceRouteDisplayPath(route)
                return (
                  <div
                    key={route.workspaceBindingId || route.routeId}
                    className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 rounded-md border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-2 text-xs transition-colors hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)]"
                    title={workspaceRouteHoverTitle(route, target)}
                  >
                    <div className="min-w-0">
                      <div className="truncate font-medium text-[var(--app-text)]">{workspaceRouteTargetName(route, target)}</div>
                      {displayPath ? <div className="truncate text-[var(--app-text-subtle)]">{displayPath}</div> : null}
                    </div>
                    <span className="shrink-0 rounded bg-[var(--app-surface-elevated)] px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)]">
                      {targetType}
                    </span>
                  </div>
                )
              })}
              {hiddenLinkCount > 0 ? (
                <button
                  type="button"
                  className="flex min-w-0 items-center justify-center gap-1.5 rounded-md border border-dashed border-[var(--app-border)] bg-transparent px-2.5 py-1.5 text-xs font-medium text-[var(--app-text-muted)] transition-colors hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                  onClick={() => setLinksExpanded(true)}
                  aria-label={`Show ${hiddenLinkCount} more linked ${hiddenLinkCount === 1 ? 'location' : 'locations'}`}
                >
                  <ChevronDown size={14} />
                  Show {hiddenLinkCount} more
                </button>
              ) : null}
            </div>
          ) : (
            <Badge tone="neutral" className="w-fit">On this host only</Badge>
          )}
        </div>
      </div>

      <div className="mt-auto flex items-center justify-between gap-2 border-t border-[var(--app-border)] pt-3">
        <div className="flex gap-1">
          {onEdit ? (
            <button
              type="button"
              onClick={() => onEdit(workspace.path)}
              disabled={busy}
              className="rounded p-1.5 text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
            >
              <Pencil size={14} />
            </button>
          ) : null}
          {onDelete ? (
            <button
              type="button"
              onClick={() => onDelete(workspace.path)}
              disabled={busy}
              className="rounded p-1.5 text-[var(--app-text-muted)] transition-colors hover:bg-[color-mix(in_oklab,var(--app-danger)_10%,transparent)] hover:text-[var(--app-danger)]"
            >
              <Trash2 size={14} />
            </button>
          ) : null}
        </div>
        <Button size="sm" onClick={() => onOpen(workspace.path)} disabled={busy} className="rounded-md">
          {busy ? 'Opening...' : 'Open'}
        </Button>
      </div>
    </div>
  )
}
