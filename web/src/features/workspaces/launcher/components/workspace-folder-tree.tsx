import { useMemo, useState } from 'react'
import { FolderOpen, Home, RefreshCw, Search } from 'lucide-react'
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
}: WorkspaceFolderTreeProps) {
  const [search, setSearch] = useState('')

  const savedPaths = useMemo(() => new Set(workspaces.map((workspace) => workspace.path)), [workspaces])
  const searchValue = search.trim().toLowerCase()
  const visibleEntries = useMemo(() => {
    const entries = browser?.entries ?? []
    if (!searchValue) {
      return entries
    }
    return entries.filter((entry) => entry.name.toLowerCase().includes(searchValue) || entry.path.toLowerCase().includes(searchValue))
  }, [browser?.entries, searchValue])

  return (
    <div className="flex flex-col gap-4 border-t border-[var(--app-border)] pt-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 className="text-sm font-medium uppercase tracking-wider text-[var(--app-text)]">Explorer</h2>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Browse into folders below, or filter the current location.</p>
        </div>
        <div className="text-xs text-[var(--app-text-subtle)]">{browser?.entries.length ?? 0}</div>
      </div>

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-end">
        <div className="grid gap-2">
          <span className="text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">Search current location</span>
          <div className="flex h-9 items-center rounded-xl border border-[var(--app-border)] bg-transparent px-3 text-sm transition hover:border-[var(--app-border-strong)] focus-within:border-[var(--app-border-accent)] focus-within:bg-[var(--app-surface-subtle)] focus-within:ring-2 focus-within:ring-[var(--app-focus-ring)] focus-within:ring-offset-2 focus-within:ring-offset-[var(--app-bg)]">
            <Search size={14} className="mr-2 shrink-0 text-[var(--app-text-subtle)]" />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Filter folders by name or path"
              className="h-full w-full border-0 bg-transparent p-0 text-sm text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)]"
            />
          </div>
        </div>

        <div className="flex flex-wrap items-end gap-2 lg:justify-end">
          <Button variant="outline" size="sm" className="rounded-md" onClick={() => onBrowsePath('')}>
            <Home size={14} />
            Home
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="rounded-md"
            disabled={!browser?.parentPath}
            onClick={() => {
              if (browser?.parentPath) {
                onBrowsePath(browser.parentPath)
              }
            }}
          >
            Up
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="rounded-md"
            onClick={() => onBrowsePath(browser?.resolvedPath ?? '')}
            disabled={browserLoading}
          >
            <RefreshCw size={14} className={cn(browserLoading && 'animate-spin')} />
            Refresh
          </Button>
        </div>
      </div>

      {browser ? (
        <div className="text-xs text-[var(--app-text-subtle)]">
          <span className="text-[var(--app-text-muted)]">Current:</span> {browser.resolvedPath}
        </div>
      ) : null}

      {browserError ? (
        <WorkspaceStatus kind="error" title="Could not load explorer" message={browserError} />
      ) : browserLoading && !browser ? (
        <div className="text-sm text-[var(--app-text-muted)]">Loading explorer…</div>
      ) : visibleEntries.length === 0 ? (
        <WorkspaceStatus
          kind="empty"
          title={searchValue ? 'No matching folders' : 'No folders found'}
          message={searchValue ? 'Try a broader search term or browse to another location.' : 'Browse a location to inspect folders here.'}
        />
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {visibleEntries.map((entry) => {
            const isSaved = savedPaths.has(entry.path)
            const meta = formatExplorerMeta(entry)

            return (
              <div
                key={entry.path}
                className="flex flex-col gap-4 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 transition-colors hover:border-[var(--app-border-strong)] sm:flex-row sm:items-center sm:justify-between"
              >
                <div className="flex min-w-0 flex-1 items-start gap-3">
                  <button
                    type="button"
                    onClick={() => onBrowsePath(entry.path)}
                    className="flex size-9 shrink-0 items-center justify-center rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)] transition-colors hover:bg-[var(--app-surface-hover)]"
                  >
                    <FolderOpen size={16} />
                  </button>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <h3 className="truncate text-sm font-medium text-[var(--app-text)]">{entry.name}</h3>
                      {isSaved ? <span className="truncate text-xs text-[var(--app-text-subtle)]">• saved workspace</span> : null}
                    </div>
                    <span className="mt-0.5 block truncate text-xs text-[var(--app-text-subtle)]" title={formatWorkspacePath(entry.path)}>
                      {formatWorkspacePath(entry.path)}
                    </span>
                    {meta ? <span className="mt-1 block text-xs text-[var(--app-text-subtle)]">{meta}</span> : null}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Button variant="ghost" size="sm" className="rounded-md" onClick={() => onBrowsePath(entry.path)}>
                    Browse
                  </Button>
                  {isSaved ? (
                    <Button size="sm" className="rounded-md" onClick={() => onOpenWorkspace(entry.path)} disabled={selectingPath === entry.path}>
                      {selectingPath === entry.path ? 'Opening...' : 'Open'}
                    </Button>
                  ) : (
                    <>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="rounded-md"
                        onClick={() => onUseFolderTemporarily(entry.path)}
                        disabled={selectingPath === entry.path}
                      >
                        {selectingPath === entry.path ? 'Opening...' : 'Use Temp'}
                      </Button>
                      <Button
                        size="sm"
                        className="rounded-md"
                        onClick={() =>
                          onCreateWorkspace({
                            path: entry.path,
                            name: entry.name,
                            isGitRepo: entry.isGitRepo,
                            hasClaude: entry.hasClaude,
                            hasSwarm: entry.hasSwarm,
                            lastModified: 0,
                          })
                        }
                        disabled={savingPath === entry.path}
                      >
                        {savingPath === entry.path ? 'Saving...' : 'Add'}
                      </Button>
                    </>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
