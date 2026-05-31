import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { ArrowUp, ChevronRight, Folder, FolderPlus, Home, Plus, RefreshCw, Search } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { cn } from '../../../../lib/cn'
import { formatWorkspacePath } from '../services/workspace-format'
import { WorkspaceStatus } from './workspace-status'
import type { WorkspaceBrowseResult, WorkspaceEntry, WorkspaceDiscoverEntry } from '../types/workspace'

interface WorkspaceFolderTreeProps {
  browser: WorkspaceBrowseResult | null
  browserLoading: boolean
  browserError: string | null
  workspaces: WorkspaceEntry[]
  selectingPath: string | null
  savingPath: string | null
  onBrowsePath: (path: string) => void
  onOpenWorkspace: (path: string) => void
  onUseFolderTemporarily: (path: string) => void
  onCreateWorkspace: (entry: WorkspaceDiscoverEntry) => void
  onCreateFolder: (parentPath: string, name: string) => Promise<string>
}

function formatExplorerMeta(entry: WorkspaceBrowseResult['entries'][number]) {
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

function fallbackFolderName(path: string): string {
  const parts = path.trim().replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path.trim() || 'folder'
}

export function WorkspaceFolderTree({
  browser,
  browserLoading,
  browserError,
  workspaces,
  selectingPath,
  savingPath,
  onBrowsePath,
  onOpenWorkspace,
  onUseFolderTemporarily,
  onCreateWorkspace,
  onCreateFolder,
}: WorkspaceFolderTreeProps) {
  const [search, setSearch] = useState('')
  const [createdFolderPath, setCreatedFolderPath] = useState<string | null>(null)
  const [createMessage, setCreateMessage] = useState<string | null>(null)

  const savedPaths = useMemo(() => new Set(workspaces.map((workspace) => workspace.path)), [workspaces])
  const searchValue = search.trim().toLowerCase()
  const visibleEntries = useMemo(() => {
    const entries = browser?.entries ?? []
    if (!searchValue) {
      return entries
    }
    return entries.filter((entry) => entry.name.toLowerCase().includes(searchValue) || entry.path.toLowerCase().includes(searchValue))
  }, [browser?.entries, searchValue])
  const currentPath = browser?.resolvedPath ?? ''
  const currentPathLabel = currentPath ? formatWorkspacePath(currentPath) : '—'
  const currentSaved = currentPath ? savedPaths.has(currentPath) : false
  const createdMessageText = createMessage ? `Created “${createMessage}”` : null
  const currentBusy = Boolean(currentPath && (savingPath === currentPath || selectingPath === currentPath))

  const createFolder = async () => {
    if (!currentPath) {
      return
    }
    const name = window.prompt(`Name the new folder in ${currentPath}`)?.trim() ?? ''
    if (!name) {
      return
    }
    const createdPath = await onCreateFolder(currentPath, name)
    if (createdPath) {
      setCreatedFolderPath(createdPath)
      setCreateMessage(name)
      window.setTimeout(() => {
        setCreateMessage((current) => (current === name ? null : current))
      }, 3500)
    }
  }

  const addCurrentFolder = () => {
    if (!currentPath) {
      return
    }
    onCreateWorkspace({
      path: currentPath,
      name: fallbackFolderName(currentPath),
      isGitRepo: false,
      hasClaude: false,
      hasSwarm: false,
      lastModified: 0,
    })
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-hidden px-3 py-4 sm:px-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-[11px] font-semibold uppercase tracking-[0.18em] text-[var(--app-text)]">Explorer</h2>
            <p className="mt-1 truncate text-xs text-[var(--app-text-muted)]">Navigate folders and add workspaces.</p>
          </div>
          <span className="rounded-full bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] text-[var(--app-text-subtle)]">{browser?.entries.length ?? 0}</span>
        </div>

        <div className="overflow-hidden rounded-lg border border-[color-mix(in_oklab,var(--app-border)_38%,transparent)] bg-[color-mix(in_oklab,var(--app-bg)_34%,transparent)] shadow-sm shadow-black/5">
          <div className="flex h-9 items-center gap-1 px-2">
            <div className="min-w-0 flex-1 truncate font-mono text-xs text-[var(--app-text)]" title={currentPath || undefined}>
              {currentPathLabel}
            </div>
            <ExplorerIconButton label="Home" disabled={!browser?.homePath} onClick={() => onBrowsePath(browser?.homePath ?? '')}>
              <Home size={13} />
            </ExplorerIconButton>
            <ExplorerIconButton
              label="Up"
              disabled={!browser?.parentPath}
              onClick={() => {
                if (browser?.parentPath) {
                  onBrowsePath(browser.parentPath)
                }
              }}
            >
              <ArrowUp size={13} />
            </ExplorerIconButton>
            <ExplorerIconButton label="Refresh" disabled={browserLoading || !currentPath} onClick={() => onBrowsePath(currentPath)}>
              <RefreshCw size={13} className={cn(browserLoading && 'animate-spin')} />
            </ExplorerIconButton>
          </div>
          <label className="flex h-9 items-center border-t border-[color-mix(in_oklab,var(--app-border)_28%,transparent)] px-2 text-xs transition focus-within:bg-[var(--app-surface-subtle)]">
            <Search size={13} className="mr-2 shrink-0 text-[var(--app-text-subtle)]" />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Search folders…"
              className="h-full w-full border-0 bg-transparent p-0 text-xs text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)]"
            />
          </label>
        </div>

        {createdMessageText ? (
          <div className="px-1 text-xs text-[var(--app-text-muted)]" role="status">
            {createdMessageText}
          </div>
        ) : null}

        {browserError ? (
          <WorkspaceStatus kind="error" title="Could not load explorer" message={browserError} />
        ) : browserLoading && !browser ? (
          <div className="px-1 text-sm text-[var(--app-text-muted)]">Loading explorer…</div>
        ) : !browser ? null : (
          <div className="flex min-h-0 flex-1 flex-col border-t border-[color-mix(in_oklab,var(--app-border)_34%,transparent)] pt-2">
            <button
              type="button"
              onClick={() => void createFolder()}
              disabled={browserLoading || !currentPath}
              className="flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-xs text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50"
              title="Create a folder here"
            >
              <FolderPlus size={14} className="shrink-0" />
              <span className="truncate">New folder</span>
            </button>

            <div className="mt-2 flex items-center justify-between px-1 text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">
              <span>Folders</span>
              <span>{visibleEntries.length}</span>
            </div>

            {visibleEntries.length === 0 ? (
              <div className="px-1 py-5">
                <WorkspaceStatus
                  kind="empty"
                  title={searchValue ? 'No matching folders' : 'No folders found'}
                  message={searchValue ? 'Try a broader filter or browse to another location.' : 'Create a folder here or browse to another location.'}
                />
              </div>
            ) : null}

            <div className="-mx-1 mt-1 min-h-0 flex-1 overflow-y-auto py-1">
              {visibleEntries.map((entry) => {
                const isSaved = savedPaths.has(entry.path)
                const entryBusy = savingPath === entry.path || selectingPath === entry.path
                const entryMeta = formatExplorerMeta(entry)
                const meta = [isSaved ? 'saved' : null, entryMeta || null].filter(Boolean).join(' · ')
                const selected = createdFolderPath === entry.path

                return (
                  <div
                    key={entry.path}
                    className={cn(
                      'group grid min-h-9 w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-1 rounded-md px-1 py-0.5 text-xs transition-colors hover:bg-[var(--app-surface-hover)] focus-within:bg-[var(--app-surface-hover)]',
                      selected && 'bg-[color-mix(in_oklab,var(--app-primary)_10%,transparent)]',
                    )}
                    title={formatWorkspacePath(entry.path)}
                  >
                    <button
                      type="button"
                      onClick={() => onBrowsePath(entry.path)}
                      className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-2 rounded px-1 py-1 text-left"
                    >
                      <Folder size={14} className="shrink-0 text-[var(--app-text-muted)]" />
                      <span className="min-w-0 truncate">
                        <span className="truncate font-medium text-[var(--app-text)]">{entry.name}</span>
                        {meta ? <span className="ml-2 truncate text-[11px] font-normal text-[var(--app-text-subtle)]">{meta}</span> : null}
                      </span>
                    </button>
                    <div className="flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
                      <button
                        type="button"
                        className="flex size-7 items-center justify-center rounded-md text-[var(--app-text-subtle)] transition-colors hover:bg-[var(--app-surface-elevated)] hover:text-[var(--app-text)]"
                        onClick={() => onBrowsePath(entry.path)}
                        aria-label={`Open ${entry.name}`}
                        title="Open folder"
                      >
                        <ChevronRight size={14} />
                      </button>
                      <button
                        type="button"
                        className="flex size-7 items-center justify-center rounded-md text-[var(--app-text-subtle)] transition-colors hover:bg-[var(--app-surface-elevated)] hover:text-[var(--app-text)] disabled:cursor-wait disabled:opacity-50"
                        disabled={entryBusy}
                        onClick={() => {
                          if (isSaved) {
                            onOpenWorkspace(entry.path)
                            return
                          }
                          onCreateWorkspace({
                            path: entry.path,
                            name: entry.name,
                            isGitRepo: entry.isGitRepo,
                            hasClaude: entry.hasClaude,
                            hasSwarm: entry.hasSwarm,
                            lastModified: 0,
                          })
                        }}
                        aria-label={isSaved ? `Open workspace ${entry.name}` : `Add ${entry.name} as workspace`}
                        title={isSaved ? 'Open workspace' : 'Add as workspace'}
                      >
                        {entryBusy ? <RefreshCw size={13} className="animate-spin" /> : isSaved ? <Folder size={13} /> : <Plus size={14} />}
                      </button>
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>

      <div className="sticky bottom-0 shrink-0 border-t border-[color-mix(in_oklab,var(--app-border)_48%,transparent)] bg-[color-mix(in_oklab,var(--app-surface)_88%,var(--app-bg))] px-3 py-3 sm:px-4">
        <div className="mb-2 truncate text-[11px] text-[var(--app-text-subtle)]" title={currentPath || undefined}>
          {currentPath ? `Current: ${currentPathLabel}` : 'Choose a folder to enable workspace actions'}
        </div>
        <Button
          type="button"
          className="h-9 min-h-0 w-full rounded-md text-sm"
          disabled={!currentPath || currentBusy}
          onClick={() => (currentSaved ? onOpenWorkspace(currentPath) : addCurrentFolder())}
        >
          {currentBusy ? <RefreshCw size={14} className="animate-spin" /> : currentSaved ? <Folder size={15} /> : <Plus size={15} />}
          {currentBusy ? 'Working…' : currentSaved ? 'Open workspace' : 'Add as workspace'}
        </Button>
        <button
          type="button"
          className="mt-2 flex h-7 w-full items-center justify-center rounded-md px-3 text-xs text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50"
          disabled={!currentPath || selectingPath === currentPath}
          onClick={() => onUseFolderTemporarily(currentPath)}
        >
          Use as temp
        </button>
      </div>
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
