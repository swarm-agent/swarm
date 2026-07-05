import { useEffect, useMemo, useState, type DragEvent, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { ArrowUp, Check, ChevronRight, Eye, EyeOff, FileText, Folder, FolderPlus, GitBranch, GripVertical, Grid2X2, Home, MoreHorizontal, Plus, RefreshCw, Search, Settings, X } from 'lucide-react'
import { Card } from '../../../components/ui/card'
import { Button } from '../../../components/ui/button'
import { Badge } from '../../../components/ui/badge'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { WorkspaceStatus } from '../launcher/components/workspace-status'
import { WorkspaceFolderTree } from '../launcher/components/workspace-folder-tree'
import { WorkspaceEditorModal, type WorkspaceEditorAvailableDirectory } from '../launcher/components/workspace-editor-modal'
import { buildWorkspaceRouteSlugMap, workspaceRouteSlugBase } from '../launcher/services/workspace-route'
import { formatWorkspaceDirectories, formatWorkspacePath } from '../launcher/services/workspace-format'
import type { WorkspaceBrowseResult, WorkspaceDiscoverEntry, WorkspaceEntry } from '../launcher/types/workspace'
import { useWorkspaceLauncher } from '../launcher/state/use-workspace-launcher'
import { cn } from '../../../lib/cn'

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

function normalizeComparePath(path: string): string {
  const trimmed = path.trim()
  const withoutTrailing = trimmed.replace(/[\\/]+$/, '')
  return withoutTrailing || trimmed
}

function isSamePath(left: string, right: string): boolean {
  return normalizeComparePath(left) === normalizeComparePath(right)
}

function isPathInside(childPath: string, parentPath: string): boolean {
  const child = normalizeComparePath(childPath)
  const parent = normalizeComparePath(parentPath)
  if (!child || !parent || child === parent) {
    return false
  }
  return child.startsWith(`${parent}/`) || child.startsWith(`${parent}\\`)
}

type ExplorerDrawerMode = 'browse' | 'workspace-folder' | 'linked-folders' | null

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => (typeof window === 'undefined' ? false : window.matchMedia(query).matches))

  useEffect(() => {
    if (typeof window === 'undefined') {
      return undefined
    }
    const mediaQuery = window.matchMedia(query)
    const updateMatches = () => setMatches(mediaQuery.matches)
    updateMatches()
    mediaQuery.addEventListener('change', updateMatches)
    return () => mediaQuery.removeEventListener('change', updateMatches)
  }, [query])

  return matches
}

function formatBrowseMeta(entry: Pick<WorkspaceBrowseResult['entries'][number], 'hasSwarm' | 'hasClaude' | 'isGitRepo'>): string {
  const parts: string[] = []
  if (entry.hasSwarm) parts.push('AGENTS.md')
  if (entry.hasClaude) parts.push('CLAUDE.md')
  if (entry.isGitRepo) parts.push('git repo')
  return parts.join(' · ')
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

function workspaceLocation(workspace: WorkspaceEntry): string {
  return formatWorkspaceDirectories(workspace.directories)[0] ?? formatWorkspacePath(workspace.path)
}

function WorkspaceSignalIcons({ hasSwarm, isGitRepo, compact = false }: { hasSwarm: boolean; isGitRepo: boolean; compact?: boolean }) {
  return (
    <div className={cn('flex items-center gap-1.5', compact ? 'justify-start' : 'justify-center')}>
      <span
        className={cn(
          'flex shrink-0 items-center justify-center rounded-md border transition-colors',
          compact ? 'size-6' : 'size-7',
          hasSwarm
            ? 'border-[color-mix(in_oklab,var(--app-border-accent)_58%,var(--app-border))] bg-[color-mix(in_oklab,var(--app-primary)_12%,transparent)] text-[var(--app-text)]'
            : 'border-[color-mix(in_oklab,var(--app-border)_48%,transparent)] bg-transparent text-[var(--app-text-muted)] opacity-35',
        )}
        title={hasSwarm ? 'AGENTS.md present' : 'No AGENTS.md detected'}
        aria-label={hasSwarm ? 'AGENTS.md present' : 'No AGENTS.md detected'}
      >
        <FileText size={compact ? 12 : 14} strokeWidth={1.8} />
      </span>
      <span
        className={cn(
          'flex shrink-0 items-center justify-center rounded-md border transition-colors',
          compact ? 'size-6' : 'size-7',
          isGitRepo
            ? 'border-[color-mix(in_oklab,var(--app-success)_45%,var(--app-border))] bg-[color-mix(in_oklab,var(--app-success)_10%,transparent)] text-[var(--app-success)]'
            : 'border-[color-mix(in_oklab,var(--app-border)_48%,transparent)] bg-transparent text-[var(--app-text-muted)] opacity-35',
        )}
        title={isGitRepo ? 'Git repository' : 'No git repository detected'}
        aria-label={isGitRepo ? 'Git repository' : 'No git repository detected'}
      >
        <GitBranch size={compact ? 12 : 14} strokeWidth={1.8} />
      </span>
    </div>
  )
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
  position: number
  current: boolean
  busy: boolean
  dragging: boolean
  onOpen: (path: string) => void
  onEdit: (path: string) => void
  onDelete: (path: string) => void
  onSwapWith: (sourcePath: string, targetPath: string) => void
  onDraggingChange: (path: string | null) => void
}

function PinnedWorkspaceCard({ workspace, position, current, busy, dragging, onOpen, onEdit, onDelete, onSwapWith, onDraggingChange }: PinnedWorkspaceCardProps) {
  const handleDragStart = (event: DragEvent<HTMLButtonElement>) => {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/workspace-path', workspace.path)
    onDraggingChange(workspace.path)
  }
  const handleDragOver = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }
  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    const sourcePath = event.dataTransfer.getData('text/workspace-path').trim()
    if (sourcePath && sourcePath !== workspace.path) {
      onSwapWith(sourcePath, workspace.path)
    }
    onDraggingChange(null)
  }

  return (
    <div
      className={cn(
        'group grid min-w-0 grid-cols-[auto_auto_auto_minmax(0,1fr)_auto] items-center gap-4 rounded-2xl border px-4 py-4 text-left transition-all',
        current
          ? 'border-[color-mix(in_oklab,var(--app-border-accent)_72%,var(--app-border))] bg-[color-mix(in_oklab,var(--app-primary)_8%,var(--app-surface))] shadow-[0_0_0_1px_color-mix(in_oklab,var(--app-border-accent)_28%,transparent)]'
          : 'border-[color-mix(in_oklab,var(--app-border)_62%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_74%,transparent)] hover:border-[color-mix(in_oklab,var(--app-border-accent)_55%,var(--app-border))] hover:bg-[var(--app-surface-hover)]',
        dragging && 'scale-[0.99] opacity-55',
      )}
      onDragOver={handleDragOver}
      onDrop={handleDrop}
      onDragEnd={() => onDraggingChange(null)}
    >
      <div className="flex size-9 shrink-0 items-center justify-center rounded-xl border border-[color-mix(in_oklab,var(--app-border)_58%,transparent)] bg-[var(--app-surface-subtle)] font-mono text-[11px] font-semibold tabular-nums text-[var(--app-text-muted)]" title={`Pinned position ${position}`}>
        {String(position).padStart(2, '0')}
      </div>
      <button
        type="button"
        draggable={!busy}
        onDragStart={handleDragStart}
        onDragEnd={() => onDraggingChange(null)}
        disabled={busy}
        className="flex size-9 shrink-0 cursor-grab items-center justify-center rounded-xl border border-dashed border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] text-[var(--app-text-subtle)] transition-colors hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] active:cursor-grabbing disabled:cursor-not-allowed disabled:opacity-50"
        aria-label={`Drag ${workspace.workspaceName} onto another workspace to swap positions`}
        title="Drag onto another workspace to swap positions"
      >
        <GripVertical size={15} />
      </button>
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

          </div>
          <div className="mt-0.5 truncate text-xs text-[var(--app-text-subtle)]" title={workspace.path}>
            {workspaceLocation(workspace)}
          </div>

        </div>
      </button>
      <div className="flex items-center gap-1">
        <button
          type="button"
          className="rounded-md p-1.5 text-[var(--app-text-subtle)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-50"
          onClick={() => onEdit(workspace.path)}
          disabled={busy}
          aria-label={`Edit ${workspace.workspaceName}`}
          title="Edit workspace"
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
  return (
    <div
      className={cn(
        'grid min-w-0 items-center gap-3 rounded-lg border border-[color-mix(in_oklab,var(--app-border)_48%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_52%,transparent)] px-3 text-sm transition-colors hover:bg-[color-mix(in_oklab,var(--app-surface-hover)_70%,transparent)] max-md:min-h-14 max-md:grid-cols-[minmax(0,1fr)_auto] max-md:px-2.5 max-md:py-2',
        compact ? 'grid-cols-[minmax(0,1fr)_4.25rem_5.5rem] py-1.5' : 'grid-cols-[minmax(0,1fr)_4.25rem_5.5rem] py-2.5',
        compact && 'max-md:grid-cols-[minmax(0,1fr)_auto]',
      )}
    >
      <button type="button" className="flex min-w-0 items-center gap-3 text-left" onClick={() => onBrowse(entry.path)}>
        <div
          className={cn(
            'flex shrink-0 items-center justify-center rounded-lg bg-[color-mix(in_oklab,var(--app-surface-elevated)_76%,transparent)] text-[var(--app-text-muted)]',
            compact ? 'size-6 max-md:size-8' : 'size-8',
          )}
        >
          <Folder size={compact ? 13 : 15} />
        </div>
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate font-medium text-[var(--app-text)]">{savedWorkspace?.workspaceName || entry.name}</span>
          </div>
          <div className="mt-1 hidden max-md:block">
            <WorkspaceSignalIcons hasSwarm={entry.hasSwarm} isGitRepo={entry.isGitRepo} compact />
          </div>
          {!compact ? (
            <div className="mt-0.5 truncate text-xs text-[var(--app-text-subtle)] max-md:text-[11px]" title={entry.path}>
              {formatWorkspacePath(entry.path)}
            </div>
          ) : null}
        </div>
      </button>
      <div className="max-md:hidden">
        <WorkspaceSignalIcons hasSwarm={entry.hasSwarm} isGitRepo={entry.isGitRepo} compact={compact} />
      </div>
      <div className="flex items-center justify-start gap-1">
        <Button
          size="sm"
          variant={savedWorkspace ? 'outline' : 'ghost'}
          disabled={busy}
          onClick={() => (savedWorkspace ? onOpen(savedWorkspace.path) : onCreate(entry))}
          className="h-7 rounded-md px-2.5 text-xs max-md:h-8 max-md:min-w-12"
        >
          {busy ? 'Working…' : savedWorkspace ? 'Open' : 'Add'}
        </Button>
        <ChevronRight size={15} className="text-[var(--app-text-subtle)] max-md:hidden" />
      </div>
    </div>
  )
}

interface MobileExplorerDrawerProps {
  open: boolean
  mode: ExplorerDrawerMode
  browser: WorkspaceBrowseResult | null
  browserLoading: boolean
  browserError: string | null
  workspaces: WorkspaceEntry[]
  workspacePath: string
  linkedDirectories: string[]
  selectingPath: string | null
  savingPath: string | null
  onClose: () => void
  onBrowsePath: (path: string) => void
  onOpenWorkspace: (path: string) => void
  onCreateWorkspace: (entry: WorkspaceDiscoverEntry) => void
  onUseFolderTemporarily: (path: string) => void
  onPickWorkspaceFolder: (path: string) => void
  onAddLinkedDirectories: (paths: string[]) => void
  onCreateFolder: (parentPath: string, name: string) => Promise<string>
}

function MobileExplorerDrawer({
  open,
  mode,
  browser,
  browserLoading,
  browserError,
  workspaces,
  workspacePath,
  linkedDirectories,
  selectingPath,
  savingPath,
  onClose,
  onBrowsePath,
  onOpenWorkspace,
  onCreateWorkspace,
  onUseFolderTemporarily,
  onPickWorkspaceFolder,
  onAddLinkedDirectories,
  onCreateFolder,
}: MobileExplorerDrawerProps) {
  const [search, setSearch] = useState('')
  const [selectedLinkedFolders, setSelectedLinkedFolders] = useState<Set<string>>(() => new Set())
  const [fullHeight, setFullHeight] = useState(false)
  const [createdFolderName, setCreatedFolderName] = useState<string | null>(null)

  useEffect(() => {
    if (!open) {
      setSearch('')
      setSelectedLinkedFolders(new Set())
      setFullHeight(false)
    }
  }, [open])

  const currentPath = browser?.resolvedPath ?? ''
  const currentPathLabel = currentPath ? formatWorkspacePath(currentPath) : '—'
  const savedPaths = useMemo(() => new Set(workspaces.map((workspace) => normalizeComparePath(workspace.path))), [workspaces])
  const linkedPathSet = useMemo(() => new Set(linkedDirectories.map(normalizeComparePath)), [linkedDirectories])
  const searchValue = search.trim().toLowerCase()
  const visibleEntries = useMemo(() => {
    const entries = browser?.entries ?? []
    if (!searchValue) return entries
    return entries.filter((entry) => entry.name.toLowerCase().includes(searchValue) || entry.path.toLowerCase().includes(searchValue))
  }, [browser?.entries, searchValue])
  const selectedAddableFolders = useMemo(() => Array.from(selectedLinkedFolders).filter((path) => {
    const normalized = normalizeComparePath(path)
    return normalized !== '' && !isSamePath(path, workspacePath) && !linkedPathSet.has(normalized)
  }), [linkedPathSet, selectedLinkedFolders, workspacePath])

  if (!open || !mode) {
    return null
  }

  const title = mode === 'linked-folders' ? 'Select linked folders' : mode === 'workspace-folder' ? 'Choose workspace folder' : 'Explorer'
  const description = mode === 'linked-folders'
    ? 'Select folders agents may access from this workspace.'
    : mode === 'workspace-folder'
      ? 'Choose the main folder for this workspace.'
      : 'Navigate folders and add any folder as a workspace.'
  const currentSaved = currentPath ? savedPaths.has(normalizeComparePath(currentPath)) : false
  const currentBusy = Boolean(currentPath && (savingPath === currentPath || selectingPath === currentPath))

  const createFolder = async () => {
    if (!currentPath) return
    const name = window.prompt(`Name the new folder in ${currentPath}`)?.trim() ?? ''
    if (!name) return
    const createdPath = await onCreateFolder(currentPath, name)
    if (createdPath) {
      setCreatedFolderName(name)
      window.setTimeout(() => setCreatedFolderName((value) => (value === name ? null : value)), 3500)
    }
  }

  const addCurrentFolder = () => {
    if (!currentPath) return
    onCreateWorkspace({
      path: currentPath,
      name: fallbackWorkspaceNameFromPath(currentPath),
      isGitRepo: false,
      hasClaude: false,
      hasSwarm: false,
      lastModified: 0,
    })
    onClose()
  }

  const toggleLinkedFolder = (path: string) => {
    setSelectedLinkedFolders((current) => {
      const next = new Set(current)
      const existing = Array.from(next).find((value) => isSamePath(value, path))
      if (existing) {
        next.delete(existing)
      } else {
        next.add(path)
      }
      return next
    })
  }

  const confirmLinkedFolders = () => {
    if (selectedAddableFolders.length === 0) return
    onAddLinkedDirectories(selectedAddableFolders)
    onClose()
  }

  return (
    <div className="fixed inset-0 z-[80] lg:hidden" role="dialog" aria-modal="true" aria-label={title}>
      <div className="absolute inset-0 bg-black/55 backdrop-blur-[1px]" onClick={onClose} />
      <div
        className={cn(
          'absolute inset-x-0 bottom-0 flex flex-col overflow-hidden rounded-t-3xl border border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_96%,var(--app-bg))] shadow-[0_-24px_60px_rgba(0,0,0,0.45)] transition-[height] duration-200',
          fullHeight ? 'h-[calc(100dvh-10px)]' : 'h-[78dvh]',
        )}
      >
        <button type="button" className="mx-auto mt-2 h-6 w-24 touch-none" aria-label="Expand Explorer drawer" onClick={() => setFullHeight((value) => !value)}>
          <span className="mx-auto mt-2 block h-1.5 w-12 rounded-full bg-[color-mix(in_oklab,var(--app-text-muted)_48%,transparent)]" />
        </button>
        <div className="flex items-start justify-between gap-3 px-4 pb-3 pt-1">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="text-base font-semibold text-[var(--app-text)]">{title}</h2>
              {mode === 'linked-folders' ? <span className="text-xs text-[var(--app-text-muted)]">{selectedAddableFolders.length} selected</span> : null}
            </div>
            <p className="mt-0.5 text-xs leading-5 text-[var(--app-text-muted)]">{description}</p>
          </div>
          <button type="button" className="flex size-9 shrink-0 items-center justify-center rounded-full text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" onClick={onClose} aria-label="Close Explorer">
            <X size={17} />
          </button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-hidden px-4">
          <div className="overflow-hidden rounded-xl border border-[color-mix(in_oklab,var(--app-border)_44%,transparent)] bg-[color-mix(in_oklab,var(--app-bg)_34%,transparent)]">
            <div className="flex h-10 items-center gap-1 px-3">
              <div className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--app-text)]" title={currentPath || undefined}>
                {currentPathLabel}
              </div>
              <ExplorerDrawerIconButton label="Home" disabled={!browser?.homePath} onClick={() => onBrowsePath(browser?.homePath ?? '')}><Home size={14} /></ExplorerDrawerIconButton>
              <ExplorerDrawerIconButton label="Up" disabled={!browser?.parentPath} onClick={() => browser?.parentPath && onBrowsePath(browser.parentPath)}><ArrowUp size={14} /></ExplorerDrawerIconButton>
              <ExplorerDrawerIconButton label="Refresh" disabled={browserLoading || !currentPath} onClick={() => onBrowsePath(currentPath)}><RefreshCw size={14} className={cn(browserLoading && 'animate-spin')} /></ExplorerDrawerIconButton>
            </div>
            <label className="flex h-10 items-center border-t border-[color-mix(in_oklab,var(--app-border)_30%,transparent)] px-3 text-xs focus-within:bg-[var(--app-surface-subtle)]">
              <Search size={14} className="mr-2 shrink-0 text-[var(--app-text-subtle)]" />
              <input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search folders…" className="h-full w-full border-0 bg-transparent p-0 text-sm text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)]" />
            </label>
          </div>

          {createdFolderName ? <div className="px-1 text-xs text-[var(--app-text-muted)]">Created “{createdFolderName}”</div> : null}
          {browserError ? <div className="rounded-lg border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{browserError}</div> : null}
          {browserLoading && !browser ? <div className="px-1 text-sm text-[var(--app-text-muted)]">Loading folders…</div> : null}

          <div className="flex min-h-0 flex-1 flex-col border-t border-[color-mix(in_oklab,var(--app-border)_34%,transparent)] pt-2">
            <button type="button" className="flex min-h-11 w-full items-center gap-2 rounded-lg px-2 text-left text-sm text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-50" disabled={browserLoading || !currentPath} onClick={() => void createFolder()}>
              <FolderPlus size={16} />
              <span>New folder</span>
            </button>
            <div className="mt-2 flex items-center justify-between px-1 text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">
              <span>Folders</span>
              <span>{visibleEntries.length}</span>
            </div>
            {visibleEntries.length === 0 ? <div className="px-1 py-5 text-sm text-[var(--app-text-muted)]">{searchValue ? 'No matching folders.' : 'No folders found here.'}</div> : null}
            <div className="-mx-1 mt-1 min-h-0 flex-1 overflow-y-auto pb-2">
              {visibleEntries.map((entry) => {
                const meta = formatBrowseMeta(entry)
                const checked = Array.from(selectedLinkedFolders).some((path) => isSamePath(path, entry.path))
                const alreadyLinked = linkedPathSet.has(normalizeComparePath(entry.path))
                const isMainFolder = isSamePath(entry.path, workspacePath)
                const nestedInMain = isPathInside(entry.path, workspacePath)
                const disabledSelection = mode === 'linked-folders' && (alreadyLinked || isMainFolder || nestedInMain)
                const isSaved = savedPaths.has(normalizeComparePath(entry.path))
                const entryBusy = savingPath === entry.path || selectingPath === entry.path

                return (
                  <div key={entry.path} className={cn('grid min-h-12 grid-cols-[minmax(0,1fr)_auto] items-center gap-1 rounded-lg px-1 text-sm hover:bg-[var(--app-surface-hover)]', checked && 'bg-[color-mix(in_oklab,var(--app-primary)_10%,transparent)]', disabledSelection && 'opacity-55')} title={formatWorkspacePath(entry.path)}>
                    <button type="button" className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-3 rounded-md px-1 py-2 text-left" disabled={disabledSelection} onClick={() => (mode === 'linked-folders' ? toggleLinkedFolder(entry.path) : onBrowsePath(entry.path))}>
                      {mode === 'linked-folders' ? (
                        <span className={cn('flex size-5 shrink-0 items-center justify-center rounded border', checked ? 'border-[var(--app-border-accent)] bg-[var(--app-primary)] text-[var(--app-bg)]' : 'border-[var(--app-border)] text-transparent')}><Check size={13} /></span>
                      ) : (
                        <Folder size={16} className="shrink-0 text-[var(--app-text-muted)]" />
                      )}
                      <span className="min-w-0">
                        <span className="block truncate font-medium text-[var(--app-text)]">{entry.name}</span>
                        <span className="block truncate text-xs text-[var(--app-text-subtle)]">{[meta || null, alreadyLinked ? 'linked' : null, isMainFolder ? 'main folder' : null, nestedInMain ? 'inside main folder' : null].filter(Boolean).join(' · ')}</span>
                      </span>
                    </button>
                    {mode === 'browse' && !isSaved ? (
                      <button
                        type="button"
                        className="flex size-9 items-center justify-center rounded-md text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-elevated)] hover:text-[var(--app-text)]"
                        onClick={() => {
                          onCreateWorkspace({ path: entry.path, name: entry.name, isGitRepo: entry.isGitRepo, hasClaude: entry.hasClaude, hasSwarm: entry.hasSwarm, lastModified: 0 })
                          onClose()
                        }}
                        aria-label={`Add ${entry.name} as workspace`}
                      >
                        <Plus size={16} />
                      </button>
                    ) : (
                      <button type="button" className="flex size-9 items-center justify-center rounded-md text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-elevated)] hover:text-[var(--app-text)]" onClick={() => onBrowsePath(entry.path)} aria-label={`Open ${entry.name}`}>
                        {entryBusy ? <RefreshCw size={15} className="animate-spin" /> : <ChevronRight size={16} />}
                      </button>
                    )}
                  </div>
                )
              })}
            </div>
          </div>
        </div>

        <div className="shrink-0 border-t border-[color-mix(in_oklab,var(--app-border)_48%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_92%,var(--app-bg))] px-4 pb-[calc(env(safe-area-inset-bottom)+14px)] pt-3">
          {mode === 'linked-folders' ? (
            <div className="grid grid-cols-[1fr_1.4fr] gap-3">
              <Button type="button" onClick={onClose}>Cancel</Button>
              <Button type="button" disabled={selectedAddableFolders.length === 0} onClick={confirmLinkedFolders}>Add {selectedAddableFolders.length} folder{selectedAddableFolders.length === 1 ? '' : 's'}</Button>
            </div>
          ) : mode === 'workspace-folder' ? (
            <div className="grid gap-2">
              <div className="truncate text-[11px] text-[var(--app-text-subtle)]" title={currentPath || undefined}>Current: {currentPathLabel}</div>
              <Button type="button" disabled={!currentPath || browserLoading} onClick={() => { onPickWorkspaceFolder(currentPath); onClose() }}>Use this folder</Button>
            </div>
          ) : (
            <div className="grid gap-2">
              <div className="truncate text-[11px] text-[var(--app-text-subtle)]" title={currentPath || undefined}>Current: {currentPathLabel}</div>
              <Button type="button" className="w-full" disabled={!currentPath || currentBusy} onClick={() => (currentSaved ? onOpenWorkspace(currentPath) : addCurrentFolder())}>
                {currentBusy ? <RefreshCw size={14} className="animate-spin" /> : currentSaved ? <Folder size={15} /> : <Plus size={15} />}
                {currentBusy ? 'Working…' : currentSaved ? 'Open workspace' : 'Add current folder as workspace'}
              </Button>
              <button type="button" className="h-8 rounded-md text-xs text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-50" disabled={!currentPath || selectingPath === currentPath} onClick={() => onUseFolderTemporarily(currentPath)}>Use current folder as temp</button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function ExplorerDrawerIconButton({ label, disabled, onClick, children }: { label: string; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-label={label} title={label} disabled={disabled} onClick={onClick} className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-40">
      {children}
    </button>
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
    draggingWorkspacePath,
    setDraggingWorkspacePath,
    swapWorkspacePositions,
    openWorkspace,
    useFolderTemporarily,
    deleteWorkspace,
    unlinkWorkspaceDirectory,
    saveWorkspace,
    createFolder,
    moveWorkspaceToIndex,
    refresh,
    browsePath,
  } = useWorkspaceLauncher()

  const navigate = useNavigate()
  const [modalState, setModalState] = useState<WorkspaceModalState | null>(null)
  const [draftName, setDraftName] = useState('')
  const [workspaceNameTouched, setWorkspaceNameTouched] = useState(false)
  const [modalError, setModalError] = useState<string | null>(null)
  const [deleteTargetPath, setDeleteTargetPath] = useState<string | null>(null)
  const [workspaceSearch, setWorkspaceSearch] = useState('')
  const [allWorkspacesVisible, setAllWorkspacesVisible] = useState(true)
  const [explorerDrawerMode, setExplorerDrawerMode] = useState<ExplorerDrawerMode>(null)
  const isDesktopExplorer = useMediaQuery('(min-width: 1024px)')

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
  const sidebarDefaultWorkspace = currentWorkspace ?? workspaces[0] ?? null

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

  const openMobileExplorer = (mode: Exclude<ExplorerDrawerMode, null>) => {
    setExplorerDrawerMode(mode)
    const startPath = mode === 'browse' ? (browser?.resolvedPath || browser?.homePath || '') : (modalState?.workspacePath || browser?.resolvedPath || browser?.homePath || '')
    if (startPath) {
      void browsePath(startPath)
    }
  }

  const closeMobileExplorer = () => {
    setExplorerDrawerMode(null)
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
    closeMobileExplorer()
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
      closeModal()
    } catch (err) {
      setModalError(err instanceof Error ? err.message : 'Failed to save workspace')
    }
  }

  return (
    <>
      <main className="flex h-full min-h-0 w-full overflow-hidden bg-[linear-gradient(180deg,var(--app-bg),color-mix(in_oklab,var(--app-bg)_76%,var(--app-surface-subtle)))]">
        {isDesktopExplorer ? (
          <aside className="hidden min-h-0 w-[320px] shrink-0 flex-col border-r border-[var(--app-border)] bg-[var(--app-surface)] lg:flex">
            <div className="border-b border-[var(--app-border)] px-3 py-3 font-mono">
              <div className="px-2 py-1">
                <div className="truncate text-[15px] font-semibold tracking-[-0.035em] text-[var(--app-text)]">Swarm</div>
                <div className="mt-px truncate text-[10px] leading-[1.25] text-[var(--app-text-subtle)]">Workspace command center</div>
              </div>
              <div className="mt-3 grid gap-0.5 text-[11px] text-[var(--app-text-subtle)]">
                <button
                  type="button"
                  className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)] disabled:cursor-not-allowed disabled:opacity-50"
                  onClick={() => {
                    if (sidebarDefaultWorkspace) handleOpenWorkspace(sidebarDefaultWorkspace.path)
                  }}
                  disabled={!sidebarDefaultWorkspace}
                  aria-label={sidebarDefaultWorkspace ? `New chat in ${sidebarDefaultWorkspace.workspaceName}` : 'New chat'}
                  title={sidebarDefaultWorkspace ? `New chat in ${sidebarDefaultWorkspace.workspaceName}` : 'New Chat'}
                >
                  <Plus size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                  <span className="min-w-0 truncate">New Chat</span>
                </button>
                <Link
                  to="/"
                  className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md bg-[var(--app-surface-hover)] px-2 text-left font-inherit text-[11px] text-[var(--app-text)]"
                  aria-label="Open workspaces"
                  title="Workspaces"
                >
                  <Folder size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                  <span className="min-w-0 truncate">Workspaces</span>
                </Link>
                <Link
                  to="/tools"
                  className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                  aria-label="Open tools"
                  title="Tools"
                >
                  <Grid2X2 size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                  <span className="min-w-0 truncate">Tools</span>
                </Link>
                <Link
                  to="/settings"
                  className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                  aria-label="Open settings"
                  title="Settings"
                >
                  <Settings size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                  <span className="min-w-0 truncate">Settings</span>
                </Link>
              </div>
            </div>
            <div className="min-h-0 flex-1">
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
            </div>
          </aside>
        ) : null}
        <section className="min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain px-4 py-6 pb-28 [-webkit-overflow-scrolling:touch] sm:px-8 lg:px-12 lg:py-8 lg:pb-8 xl:px-16">
          <div className="mx-auto flex min-h-full w-full max-w-5xl flex-col gap-8 lg:gap-10">
            <div className="flex items-start justify-between gap-3 bg-transparent px-0 py-1 sm:items-center">
              <div className="flex min-w-0 items-start gap-3">
                <div className="min-w-0">
                  <h1 className="text-xl font-semibold tracking-tight text-[var(--app-text)] sm:text-2xl">Workspaces</h1>
                  <p className="mt-1 text-sm text-[var(--app-text-muted)]">Select a project to begin working</p>
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-2 sm:gap-3">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void navigate({ to: '/settings' })}
                  className="size-9 rounded-md bg-[var(--app-surface)] p-0 shadow-sm sm:w-auto sm:px-3"
                  aria-label="Settings"
                >
                  <Settings size={14} />
                  <span className="hidden sm:inline">Settings</span>
                </Button>
                <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={refreshing} className="size-9 rounded-md bg-[var(--app-surface)] p-0 shadow-sm sm:w-auto sm:px-3" aria-label="Refresh workspaces">
                  <RefreshCw size={14} className={refreshing ? 'animate-spin' : undefined} />
                  <span className="hidden sm:inline">Refresh</span>
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
              <div className="flex flex-col gap-12">
                <section className="flex flex-col gap-5">
                  <div className="flex items-end justify-between gap-3">
                    <div className="flex items-center gap-2">
                      <h2 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Pinned workspaces</h2>
                      <span className="text-xs text-[var(--app-text-subtle)]">{workspaces.length}</span>
                    </div>
                    <p className="hidden text-sm text-[var(--app-text-muted)] sm:block">Ordered list — drag a handle onto another workspace to swap them.</p>
                  </div>
                  {workspaces.length === 0 ? (
                    <WorkspaceStatus kind="empty" title="No saved workspaces" message="Browse a folder in Explorer and add it as your first workspace." />
                  ) : (
                    <div className="grid gap-3">
                      {workspaces.map((workspace, index) => (
                        <PinnedWorkspaceCard
                          key={workspace.path}
                          workspace={workspace}
                          position={index + 1}
                          current={currentWorkspacePath === workspace.path || workspace.active}
                          busy={selectingPath === workspace.path || savingPath === workspace.path}
                          dragging={draggingWorkspacePath === workspace.path}
                          onOpen={handleOpenWorkspace}
                          onEdit={startEdit}
                          onDelete={setDeleteTargetPath}
                          onSwapWith={(sourcePath, targetPath) => void swapWorkspacePositions(sourcePath, targetPath)}
                          onDraggingChange={setDraggingWorkspacePath}
                        />
                      ))}
                    </div>
                  )}
                </section>

                <section className="flex flex-col gap-5">
                  <div className="flex flex-col gap-3 border-t border-[color-mix(in_oklab,var(--app-border)_56%,transparent)] pt-8 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                      <div className="flex items-center gap-2">
                        <h2 className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">All workspaces</h2>
                        <span className="rounded-full bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] text-[var(--app-text-muted)]">{discovered.length}</span>
                      </div>
                      <p className="mt-1 hidden text-sm text-[var(--app-text-muted)] sm:block">Folders with AGENTS.md or git repos</p>
                    </div>
                    <div className="flex flex-wrap items-center gap-2 max-sm:w-full">
                      {allWorkspacesVisible ? (
                        <div className="flex h-8 min-w-[220px] items-center rounded-lg border border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] bg-transparent px-2.5 text-sm focus-within:border-[var(--app-border-accent)] focus-within:ring-2 focus-within:ring-[var(--app-focus-ring)] max-sm:min-w-0 max-sm:flex-1">
                          <Search size={14} className="mr-2 shrink-0 text-[var(--app-text-subtle)]" />
                          <input
                            value={workspaceSearch}
                            onChange={(event) => setWorkspaceSearch(event.target.value)}
                            placeholder="Search workspaces…"
                            className="h-full w-full border-0 bg-transparent p-0 text-xs text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)]"
                          />
                        </div>
                      ) : null}
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
                      <div className="grid gap-3">
                        {discoveredRows.length === 0 ? (
                          <div className="rounded-xl border border-[color-mix(in_oklab,var(--app-border)_58%,transparent)] px-4 py-6 text-sm text-[var(--app-text-muted)]">No folders match your search.</div>
                        ) : (
                          discoveredRows.map(({ entry, savedWorkspace }) => (
                            <AllWorkspaceRow
                              key={entry.path}
                              entry={entry}
                              savedWorkspace={savedWorkspace}
                              busy={savingPath === entry.path || selectingPath === entry.path}
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

      </main>

      {!isDesktopExplorer ? (
        <div className="fixed inset-x-0 bottom-0 z-40 px-4 pb-[calc(env(safe-area-inset-bottom)+12px)] pt-3 lg:hidden pointer-events-none">
          <button
            type="button"
            className="pointer-events-auto flex min-h-12 w-full items-center justify-center rounded-2xl border border-[color-mix(in_oklab,var(--app-border-accent)_58%,transparent)] bg-[color-mix(in_oklab,var(--app-primary)_88%,#6d5dfc)] px-4 text-sm font-semibold text-white shadow-[0_14px_36px_rgba(0,0,0,0.38)]"
            onClick={() => openMobileExplorer('browse')}
          >
            <span className="flex items-center gap-2"><Plus size={17} /> Add from Explorer</span>
          </button>
        </div>
      ) : null}

      <WorkspaceEditorModal
        open={Boolean(modalState?.open)}
        mode={modalState?.mode ?? 'create'}
        workspacePath={modalState?.workspacePath ?? ''}
        workspacePathEditable={modalState?.workspacePathEditable ?? true}
        name={draftName}
        themeId={modalState?.themeId ?? 'inherit'}
        linkedDirectories={modalState?.sourcePaths.filter((path) => !isSamePath(path, modalState.workspacePath)) ?? []}
        availableDirectories={modalAvailableDirectories}
        workspaces={workspaces}
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
        useExternalMobileFolderPicker={!isDesktopExplorer}
        onRequestMobileFolderPicker={(mode) => openMobileExplorer(mode)}
        onCreateFolder={createFolder}
        onSelectWorkspace={startEdit}
        onMoveWorkspaceToIndex={(path, index) => {
          void moveWorkspaceToIndex(path, index)
        }}
        onAddLinkedDirectory={addLinkedDirectory}
        onAddLinkedDirectories={addLinkedDirectories}
        onRemoveLinkedDirectory={removeLinkedDirectory}
        onClose={closeModal}
        onSubmit={() => {
          void submitModal()
        }}
      />

      <MobileExplorerDrawer
        open={Boolean(explorerDrawerMode)}
        mode={explorerDrawerMode}
        browser={browser}
        browserLoading={browserLoading}
        browserError={browserError}
        workspaces={workspaces}
        workspacePath={modalState?.workspacePath ?? ''}
        linkedDirectories={modalState?.sourcePaths.filter((path) => !isSamePath(path, modalState.workspacePath)) ?? []}
        selectingPath={selectingPath}
        savingPath={savingPath}
        onClose={closeMobileExplorer}
        onBrowsePath={(path) => void browsePath(path)}
        onOpenWorkspace={handleOpenWorkspace}
        onCreateWorkspace={(entry) => openCreateModal(entry.path, [entry.path], entry.name)}
        onUseFolderTemporarily={handleUseFolderTemporarily}
        onPickWorkspaceFolder={pickWorkspaceFolder}
        onAddLinkedDirectories={addLinkedDirectories}
        onCreateFolder={createFolder}
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
