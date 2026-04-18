import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, Folder, HardDrive, Home, RefreshCw } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Badge } from '../../../../components/ui/badge'
import { Card } from '../../../../components/ui/card'
import { cn } from '../../../../lib/cn'
import { WorkspaceStatus } from './workspace-status'
import { browseWorkspacePath } from '../queries/browse-workspace-path'
import type { WorkspaceBrowseEntry, WorkspaceBrowseResult, WorkspaceEntry } from '../types/workspace'

interface WorkspaceFolderTreeProps {
  currentWorkspacePath: string | null
  selectingPath: string | null
  savingPath: string | null
  workspaces: WorkspaceEntry[]
  onOpenWorkspace: (path: string) => void
  onUseFolderTemporarily: (path: string) => void
  onCreateWorkspace: (path: string, name: string) => void
}

interface TreeRoot {
  id: 'home' | 'root'
  label: string
  path: string
}

function fallbackWorkspaceNameFromPath(path: string): string {
  const parts = path.trim().replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path.trim() || 'workspace'
}

function normalizeExpandedPaths(paths: Set<string>, homePath: string): Set<string> {
  const next = new Set(paths)
  next.add(homePath)
  return next
}

function signalBadges(entry: WorkspaceBrowseEntry): string[] {
  const badges: string[] = []
  if (entry.hasSwarm) {
    badges.push('AGENTS.md')
  }
  if (entry.hasClaude) {
    badges.push('CLAUDE.md')
  }
  if (entry.isGitRepo) {
    badges.push('git')
  }
  return badges
}

export function WorkspaceFolderTree({
  currentWorkspacePath,
  selectingPath,
  savingPath,
  workspaces,
  onOpenWorkspace,
  onUseFolderTemporarily,
  onCreateWorkspace,
}: WorkspaceFolderTreeProps) {
  const [browserByPath, setBrowserByPath] = useState<Record<string, WorkspaceBrowseResult>>({})
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({})
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set())
  const [selectedPath, setSelectedPath] = useState<string | null>(currentWorkspacePath)
  const [rootState, setRootState] = useState<WorkspaceBrowseResult | null>(null)
  const [treeError, setTreeError] = useState<string | null>(null)

  const loadPath = useCallback(async (path: string) => {
    setLoadingPaths((current) => ({ ...current, [path]: true }))
    try {
      const result = await browseWorkspacePath(path)
      setBrowserByPath((current) => ({ ...current, [result.resolvedPath]: result }))
      setTreeError(null)
      if (path.trim() === '') {
        setRootState(result)
        setExpandedPaths((current) => normalizeExpandedPaths(current, result.homePath))
        setSelectedPath((current) => current ?? result.homePath)
      }
      return result
    } finally {
      setLoadingPaths((current) => {
        const next = { ...current }
        delete next[path]
        return next
      })
    }
  }, [])

  useEffect(() => {
    void loadPath('').catch((error) => {
      setTreeError(error instanceof Error ? error.message : 'Failed to load folder tree')
    })
  }, [loadPath])

  useEffect(() => {
    if (!currentWorkspacePath?.trim()) {
      return
    }
    setSelectedPath(currentWorkspacePath)
  }, [currentWorkspacePath])

  const roots = useMemo(() => {
    if (!rootState) {
      return []
    }
    const next: TreeRoot[] = [{ id: 'home', label: 'Home', path: rootState.homePath }]
    if (rootState.rootPath !== rootState.homePath) {
      next.push({ id: 'root', label: 'Computer', path: rootState.rootPath })
    }
    return next
  }, [rootState])

  const savedWorkspaceByPath = useMemo(() => new Map(workspaces.map((workspace) => [workspace.path, workspace] as const)), [workspaces])

  const selectedWorkspace = selectedPath ? savedWorkspaceByPath.get(selectedPath) ?? null : null
  const selectedName = selectedWorkspace?.workspaceName ?? (selectedPath ? fallbackWorkspaceNameFromPath(selectedPath) : '')
  const selectedRoot = selectedPath ? roots.find((root) => root.path === selectedPath) ?? null : null
  const selectedSummary = !selectedPath
    ? null
    : currentWorkspacePath === selectedPath
      ? 'Currently open.'
      : selectedWorkspace
        ? `Saved as ${selectedWorkspace.workspaceName}.`
        : selectedRoot
          ? `Browse inside ${selectedRoot.label.toLowerCase()} to pick a project folder.`
          : 'Open it temporarily or save it as a workspace.'

  const toggleExpanded = useCallback(
    async (path: string) => {
      const normalizedPath = path.trim()
      if (!normalizedPath) {
        return
      }
      if (expandedPaths.has(normalizedPath)) {
        setExpandedPaths((current) => {
          const next = new Set(current)
          next.delete(normalizedPath)
          return next
        })
        return
      }
      if (!browserByPath[normalizedPath]) {
        try {
          await loadPath(normalizedPath)
        } catch (error) {
          setTreeError(error instanceof Error ? error.message : 'Failed to load folder tree')
          return
        }
      }
      setExpandedPaths((current) => {
        const next = new Set(current)
        next.add(normalizedPath)
        return next
      })
    },
    [browserByPath, expandedPaths, loadPath],
  )

  const renderNode = useCallback(
    (entry: WorkspaceBrowseEntry, depth: number) => {
      const isExpanded = expandedPaths.has(entry.path)
      const childBrowser = browserByPath[entry.path]
      const isLoading = loadingPaths[entry.path] === true
      const isCurrent = currentWorkspacePath === entry.path
      const isSaved = savedWorkspaceByPath.has(entry.path)
      const isSelected = selectedPath === entry.path
      const entrySignals = signalBadges(entry)
      const meta = [isCurrent ? 'current' : null, isSaved ? 'saved' : null, ...entrySignals].filter(Boolean).join(' · ')

      return (
        <li key={entry.path} className="min-w-0 grid gap-1">
          <div className={cn('relative', depth > 0 && 'pl-4')}>
            {depth > 0 ? <span className="absolute left-0 top-5 h-px w-3 bg-[var(--app-border)]" aria-hidden="true" /> : null}
            <div
              className={cn(
                'flex min-h-11 items-start gap-2 rounded-xl border px-2 py-2 text-sm transition sm:min-h-8 sm:items-center sm:gap-1 sm:rounded-md sm:px-1.5 sm:py-1',
                isSelected
                  ? 'border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-[var(--shadow-soft)]'
                  : 'border-transparent text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
              )}
            >
              <button
                type="button"
                className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] sm:size-5 sm:rounded"
                onClick={() => {
                  void toggleExpanded(entry.path)
                }}
                aria-label={isExpanded ? `Collapse ${entry.name}` : `Expand ${entry.name}`}
              >
                {isExpanded ? <ChevronDown size={16} className="sm:size-3.5" /> : <ChevronRight size={16} className="sm:size-3.5" />}
              </button>
              <button
                type="button"
                className="min-w-0 max-w-full flex flex-1 flex-col items-start gap-1 overflow-hidden text-left sm:flex-row sm:items-center sm:gap-2"
                onClick={() => setSelectedPath(entry.path)}
                title={entry.path}
              >
                <div className="flex min-w-0 max-w-full items-center gap-2 overflow-hidden">
                  <Folder size={16} className="shrink-0 text-[var(--app-text-muted)]" />
                  <span className="min-w-0 truncate font-medium text-[var(--app-text)]">{entry.name}</span>
                </div>
                {meta ? <span className="block w-full overflow-hidden break-all text-xs text-[var(--app-text-subtle)] sm:w-auto sm:max-w-[14rem] sm:truncate sm:text-[11px]">{meta}</span> : null}
              </button>
            </div>
          </div>
          {isExpanded ? (
            isLoading ? (
              <div className="ml-7 text-xs text-[var(--app-text-muted)]">Loading…</div>
            ) : childBrowser && childBrowser.entries.length > 0 ? (
              <ul className="ml-3 grid gap-1 border-l border-[var(--app-border)] pl-3">
                {childBrowser.entries.map((childEntry) => renderNode(childEntry, depth + 1))}
              </ul>
            ) : (
              <div className="ml-7 text-xs text-[var(--app-text-subtle)]">empty</div>
            )
          ) : null}
        </li>
      )
    },
    [browserByPath, currentWorkspacePath, expandedPaths, loadingPaths, savedWorkspaceByPath, selectedPath, toggleExpanded],
  )

  return (
    <Card className="min-w-0 overflow-hidden grid gap-4 px-4 py-4 sm:px-6 sm:py-5">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="grid gap-1">
          <h2 className="text-lg font-semibold text-[var(--app-text)]">Folders on this computer</h2>
          <p className="text-sm text-[var(--app-text-muted)]">Expand folders, select one, then open it or save it as a workspace.</p>
        </div>

        <div className="min-w-0 flex w-full flex-wrap items-center gap-2 lg:w-auto lg:justify-end">
          {selectedPath ? (
            selectedWorkspace ? (
              <Button className="w-full sm:w-auto" type="button" onClick={() => onOpenWorkspace(selectedPath)} disabled={selectingPath === selectedPath}>
                {selectingPath === selectedPath ? 'Opening…' : 'Open workspace'}
              </Button>
            ) : selectedRoot ? null : (
              <>
                <Button className="flex-1 sm:flex-none" type="button" variant="ghost" onClick={() => onUseFolderTemporarily(selectedPath)} disabled={selectingPath === selectedPath}>
                  {selectingPath === selectedPath ? 'Opening…' : 'Use temporarily'}
                </Button>
                <Button className="flex-1 sm:flex-none" type="button" onClick={() => onCreateWorkspace(selectedPath, selectedName)} disabled={savingPath === selectedPath}>
                  {savingPath === selectedPath ? 'Saving…' : 'Create workspace'}
                </Button>
              </>
            )
          ) : null}
          <Button
            className="w-full sm:w-auto"
            type="button"
            onClick={() => {
              void loadPath('').catch((error) => {
                setTreeError(error instanceof Error ? error.message : 'Failed to load folder tree')
              })
            }}
            disabled={loadingPaths[''] === true}
          >
            <RefreshCw size={14} className={loadingPaths[''] === true ? 'animate-spin' : undefined} />
            Refresh
          </Button>
        </div>
      </div>

      {selectedPath ? (
        <div className="min-w-0 grid gap-3 overflow-hidden rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-3 sm:p-4 shadow-[var(--shadow-soft)] lg:grid-cols-[minmax(0,1fr)_auto] lg:items-center">
          <div className="min-w-0 grid gap-1">
            <div className="text-xs uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">Selected folder</div>
            <div className="min-w-0 overflow-hidden break-words text-sm font-medium text-[var(--app-text)]">{selectedPath}</div>
            <div className="text-sm text-[var(--app-text-muted)]">{selectedSummary}</div>
          </div>
          <div className="flex flex-wrap gap-2">
            {selectedWorkspace ? <Badge tone="live">Saved workspace</Badge> : null}
            {currentWorkspacePath === selectedPath ? <Badge tone="warning">Current</Badge> : null}
          </div>
        </div>
      ) : null}

      {treeError ? <WorkspaceStatus kind="error" title="Could not load folder tree" message={treeError} /> : null}

      {rootState ? (
        <div className="min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] p-2 sm:p-3">
          <div className="mb-2 px-1 text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">Explorer</div>
          <div className="min-w-0 max-w-full max-h-[70vh] overflow-x-hidden overflow-y-auto overscroll-contain pr-1 sm:max-h-[60vh]">
            <ul className="grid gap-1 font-mono text-[13px]">
              {roots.map((root) => {
                const isExpanded = expandedPaths.has(root.path)
                const isSelected = selectedPath === root.path
                const rootBrowser = browserByPath[root.path]
                const rootLoading = loadingPaths[root.path] === true
                return (
                  <li key={root.id} className="min-w-0 grid gap-1">
                    <div
                      className={cn(
                        'min-w-0 max-w-full flex min-h-11 items-start gap-2 rounded-xl border px-2 py-2 text-sm transition sm:min-h-8 sm:items-center sm:gap-1 sm:rounded-md sm:px-1.5 sm:py-1',
                        isSelected
                          ? 'border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-[var(--shadow-soft)]'
                          : 'border-transparent text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                      )}
                    >
                      <button
                        type="button"
                        className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] sm:size-5 sm:rounded"
                        onClick={() => {
                          void toggleExpanded(root.path)
                        }}
                        aria-label={isExpanded ? `Collapse ${root.label}` : `Expand ${root.label}`}
                      >
                        {isExpanded ? <ChevronDown size={16} className="sm:size-3.5" /> : <ChevronRight size={16} className="sm:size-3.5" />}
                      </button>
                      <button
                        type="button"
                        className="min-w-0 max-w-full flex flex-1 flex-col items-start gap-1 overflow-hidden text-left sm:flex-row sm:items-center sm:gap-2"
                        onClick={() => setSelectedPath(root.path)}
                        title={root.path}
                      >
                        <div className="flex min-w-0 max-w-full items-center gap-2 overflow-hidden">
                          {root.id === 'home' ? (
                            <Home size={16} className="shrink-0 text-[var(--app-text-muted)]" />
                          ) : (
                            <HardDrive size={16} className="shrink-0 text-[var(--app-text-muted)]" />
                          )}
                          <span className="min-w-0 truncate font-medium text-[var(--app-text)]">{root.label}</span>
                        </div>
                        <span className="block w-full overflow-hidden break-all text-xs text-[var(--app-text-subtle)] sm:w-auto sm:max-w-[18rem] sm:truncate sm:text-[11px]">{root.path}</span>
                      </button>
                    </div>
                    {isExpanded ? (
                      rootLoading ? (
                        <div className="ml-7 text-xs text-[var(--app-text-muted)]">Loading…</div>
                      ) : rootBrowser && rootBrowser.entries.length > 0 ? (
                        <ul className="ml-3 grid gap-1 border-l border-[var(--app-border)] pl-3">
                          {rootBrowser.entries.map((entry) => renderNode(entry, 1))}
                        </ul>
                      ) : (
                        <div className="ml-7 text-xs text-[var(--app-text-subtle)]">empty</div>
                      )
                    ) : null}
                  </li>
                )
              })}
            </ul>
          </div>
        </div>
      ) : loadingPaths[''] === true ? (
        <div className="text-sm text-[var(--app-text-muted)]">Loading folder tree…</div>
      ) : null}
    </Card>
  )
}
