import type { CSSProperties, ReactNode } from 'react'
import { FolderOpen } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Card } from '../../../../../components/ui/card'
import { cn } from '../../../../../lib/cn'
import { WorkspaceCard } from '../../components/workspace-card'
import { WorkspaceStatus } from '../../components/workspace-status'
import { formatWorkspacePath } from '../../services/workspace-format'
import type { WorkspaceDiscoverEntry } from '../../types/workspace'
import type { WorkspaceLauncherIterationProps } from './types'

function formatDiscoveredMeta(entry: WorkspaceDiscoverEntry): string {
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

interface SavedWorkspaceSectionProps
  extends Pick<
    WorkspaceLauncherIterationProps,
    | 'workspaces'
    | 'currentWorkspacePath'
    | 'selectingPath'
    | 'savingPath'
    | 'draggingWorkspacePath'
    | 'onOpenWorkspace'
    | 'onEditWorkspace'
    | 'onDeleteWorkspace'
    | 'onToggleWorktree'
    | 'onMoveWorkspaceToIndex'
    | 'onDraggingWorkspaceChange'
  > {
  title?: string
  description?: string
  density?: 'comfortable' | 'compact'
  columns?: number
  className?: string
  headerAside?: ReactNode
}

function SectionShell({ className, children }: { className?: string; children: ReactNode }) {
  return <Card className={cn('grid gap-5 px-5 py-5 sm:px-6', className)}>{children}</Card>
}

export function SavedWorkspaceSection({
  workspaces,
  currentWorkspacePath,
  selectingPath,
  savingPath,
  draggingWorkspacePath,
  onOpenWorkspace,
  onEditWorkspace,
  onDeleteWorkspace,
  onToggleWorktree,
  onMoveWorkspaceToIndex,
  onDraggingWorkspaceChange,
  title = 'Saved workspaces',
  description = 'Your saved workspaces. Drag to reorder.',
  density = 'comfortable',
  columns = 3,
  className,
  headerAside,
}: SavedWorkspaceSectionProps) {
  const gridStyle = { '--launcher-columns': String(columns) } as CSSProperties

  return (
    <SectionShell className={className}>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="grid gap-1.5">
          <h2 className="text-lg font-semibold text-[var(--app-text)]">{title}</h2>
          <p className="text-sm leading-6 text-[var(--app-text-muted)]">{description}</p>
        </div>
        {headerAside ? <div className="flex flex-wrap items-center gap-2">{headerAside}</div> : null}
      </div>

      {workspaces.length === 0 ? (
        <WorkspaceStatus
          kind="empty"
          title="No workspaces yet"
          message="Save a workspace first, then it will show up here."
        />
      ) : (
        <div
          className={cn(
            'grid gap-4',
            density === 'compact' ? 'md:grid-cols-2 xl:grid-cols-3' : 'md:grid-cols-2 xl:[grid-template-columns:repeat(var(--launcher-columns),minmax(0,1fr))]',
          )}
          style={gridStyle}
          aria-label="Saved workspaces"
        >
          {workspaces.map((workspace, index) => (
            <WorkspaceCard
              key={workspace.path}
              workspace={workspace}
              position={index + 1}
              current={currentWorkspacePath === workspace.path || workspace.active}
              busy={selectingPath === workspace.path || savingPath === workspace.path}
              dragging={draggingWorkspacePath === workspace.path}
              onOpen={onOpenWorkspace}
              onEdit={onEditWorkspace}
              onDelete={onDeleteWorkspace}
              onToggleWorktree={onToggleWorktree}
              onMoveToIndex={onMoveWorkspaceToIndex}
              onDraggingChange={onDraggingWorkspaceChange}
              density={density}
            />
          ))}
        </div>
      )}
    </SectionShell>
  )
}

interface DiscoveredDirectorySectionProps
  extends Pick<
    WorkspaceLauncherIterationProps,
    | 'discovered'
    | 'savingPath'
    | 'onSaveDiscovered'
    | 'onBrowsePath'
    | 'onUseFolderTemporarily'
  > {
  title?: string
  description?: string
  compact?: boolean
  className?: string
  headerAside?: ReactNode
}

export function DiscoveredDirectorySection({
  discovered,
  savingPath,
  onSaveDiscovered,
  onBrowsePath,
  onUseFolderTemporarily,
  title = 'Folders on this computer',
  description = 'Browse likely project folders and create a workspace when you find the right one.',
  compact = false,
  className,
  headerAside,
}: DiscoveredDirectorySectionProps) {
  return (
    <SectionShell className={className}>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="grid gap-1.5">
          <h2 className="text-lg font-semibold text-[var(--app-text)]">{title}</h2>
          <p className="text-sm leading-6 text-[var(--app-text-muted)]">{description}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {headerAside}
          <span className="inline-flex min-h-8 items-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-xs font-medium text-[var(--app-text-muted)]">
            {discovered.length}
          </span>
        </div>
      </div>

      {discovered.length === 0 ? (
        <WorkspaceStatus
          kind="empty"
          title="No candidate folders"
          message="No folders with AGENTS.md, CLAUDE.md, or git metadata were found in the scanned locations."
        />
      ) : (
        <div className={cn('grid gap-3', compact && 'gap-2')} aria-label="Discovered directories">
          {discovered.map((entry) => {
            const meta = formatDiscoveredMeta(entry)

            return (
              <article
                key={entry.path}
                className="flex flex-col gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] sm:flex-row sm:items-center sm:justify-between"
              >
                <button
                  type="button"
                  className="flex min-w-0 flex-1 items-start gap-3 text-left outline-none transition focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
                  onClick={() => onBrowsePath(entry.path)}
                >
                  <div className="flex size-10 shrink-0 items-center justify-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]">
                    <FolderOpen size={18} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-3">
                      <strong className="truncate text-sm font-semibold text-[var(--app-text)]">{entry.name}</strong>
                      <span className="truncate text-sm text-[var(--app-text-muted)]" title={formatWorkspacePath(entry.path)}>
                        {formatWorkspacePath(entry.path)}
                      </span>
                    </div>
                    <p className="mt-1 text-sm leading-6 text-[var(--app-text-subtle)]">{meta || 'Folder detected'}</p>
                  </div>
                </button>
                <div className="flex flex-wrap justify-end gap-2">
                  <Button type="button" variant="ghost" disabled={savingPath === entry.path} onClick={() => onUseFolderTemporarily(entry.path)}>
                    Use temporarily
                  </Button>
                  <Button type="button" disabled={savingPath === entry.path} onClick={() => onSaveDiscovered(entry)}>
                    {savingPath === entry.path ? 'Saving…' : 'Create workspace'}
                  </Button>
                </div>
              </article>
            )
          })}
        </div>
      )}
    </SectionShell>
  )
}
