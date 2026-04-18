import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, Folder, Home, Plus, RefreshCw } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import { cn } from '../../../../lib/cn'
import { browseWorkspacePath } from '../../../workspaces/launcher/queries/browse-workspace-path'
import type { WorkspaceBrowseEntry, WorkspaceBrowseResult, WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { ContainerProfileMount, ContainerMountMode } from '../types/container-profiles'

interface ContainerMountPickerProps {
  mounts: ContainerProfileMount[]
  workspaces: WorkspaceEntry[]
  disabled?: boolean
  onChange: (mounts: ContainerProfileMount[]) => void
}

function defaultMountTarget(path: string): string {
  const trimmed = path.trim().replace(/[\\/]+$/, '')
  const segments = trimmed.split(/[\\/]/).filter(Boolean)
  const lastSegment = (segments[segments.length - 1] || 'workspace').toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
  return `/workspace/${lastSegment || 'workspace'}`
}

function upsertMounts(current: ContainerProfileMount[], additions: ContainerProfileMount[]): ContainerProfileMount[] {
  const next = [...current]
  const seen = new Set(next.map((mount) => `${mount.sourcePath.toLowerCase()}|${mount.targetPath.toLowerCase()}`))
  for (const mount of additions) {
    const normalized: ContainerProfileMount = {
      sourcePath: mount.sourcePath.trim(),
      targetPath: mount.targetPath.trim() || defaultMountTarget(mount.sourcePath),
      mode: mount.mode === 'ro' ? 'ro' : 'rw',
      workspacePath: mount.workspacePath.trim(),
      workspaceName: mount.workspaceName.trim(),
    }
    if (!normalized.sourcePath) {
      continue
    }
    const key = `${normalized.sourcePath.toLowerCase()}|${normalized.targetPath.toLowerCase()}`
    if (seen.has(key)) {
      continue
    }
    seen.add(key)
    next.push(normalized)
  }
  return next
}

export function ContainerMountPicker({ mounts, workspaces, disabled = false, onChange }: ContainerMountPickerProps) {
  const [browserByPath, setBrowserByPath] = useState<Record<string, WorkspaceBrowseResult>>({})
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({})
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set())
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [rootState, setRootState] = useState<WorkspaceBrowseResult | null>(null)
  const [browserError, setBrowserError] = useState<string | null>(null)

  const loadPath = useCallback(async (path: string) => {
    setLoadingPaths((current) => ({ ...current, [path]: true }))
    try {
      const result = await browseWorkspacePath(path)
      setBrowserByPath((current) => ({ ...current, [result.resolvedPath]: result }))
      setBrowserError(null)
      if (path.trim() === '') {
        setRootState(result)
        setExpandedPaths(new Set([result.homePath]))
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
      setBrowserError(error instanceof Error ? error.message : 'Failed to load folders')
    })
  }, [loadPath])

  const workspaceEntries = useMemo(() => workspaces.slice().sort((left, right) => left.workspaceName.localeCompare(right.workspaceName)), [workspaces])

  const handleAddWorkspace = useCallback((workspace: WorkspaceEntry) => {
    const paths = Array.from(new Set([workspace.path, ...workspace.directories].map((value) => value.trim()).filter(Boolean)))
    onChange(upsertMounts(mounts, paths.map((path) => ({
      sourcePath: path,
      targetPath: defaultMountTarget(path),
      mode: 'rw',
      workspacePath: workspace.path,
      workspaceName: workspace.workspaceName,
    }))))
  }, [mounts, onChange])

  const handleAddSelectedPath = useCallback(() => {
    if (!selectedPath?.trim()) {
      return
    }
    const sourcePath = selectedPath.trim()
    onChange(upsertMounts(mounts, [{
      sourcePath,
      targetPath: defaultMountTarget(sourcePath),
      mode: 'rw',
      workspacePath: '',
      workspaceName: '',
    }]))
  }, [mounts, onChange, selectedPath])

  const toggleExpanded = useCallback(async (path: string) => {
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
        setBrowserError(error instanceof Error ? error.message : 'Failed to load folders')
        return
      }
    }
    setExpandedPaths((current) => {
      const next = new Set(current)
      next.add(normalizedPath)
      return next
    })
  }, [browserByPath, expandedPaths, loadPath])

  const updateMount = useCallback((index: number, patch: Partial<ContainerProfileMount>) => {
    onChange(mounts.map((mount, currentIndex) => currentIndex === index ? { ...mount, ...patch } : mount))
  }, [mounts, onChange])

  const removeMount = useCallback((index: number) => {
    onChange(mounts.filter((_, currentIndex) => currentIndex !== index))
  }, [mounts, onChange])

  const renderEntry = useCallback((entry: WorkspaceBrowseEntry, depth: number) => {
    const isExpanded = expandedPaths.has(entry.path)
    const childBrowser = browserByPath[entry.path]
    const isLoading = loadingPaths[entry.path] === true
    const isSelected = selectedPath === entry.path
    const meta = [entry.isGitRepo ? 'git' : null, entry.hasSwarm ? 'AGENTS.md' : null, entry.hasClaude ? 'CLAUDE.md' : null].filter(Boolean).join(' · ')

    return (
      <li key={entry.path} className="grid gap-1">
        <div className={cn('relative', depth > 0 && 'pl-4')}>
          {depth > 0 ? <span className="absolute left-0 top-4 h-px w-3 bg-[var(--app-border)]" aria-hidden="true" /> : null}
          <div
            className={cn(
              'flex min-h-8 items-center gap-1 rounded-md border px-1.5 py-0.5 text-sm transition',
              isSelected
                ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] text-[var(--app-text)]'
                : 'border-transparent text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
            )}
          >
            <button
              type="button"
              className="inline-flex size-5 shrink-0 items-center justify-center rounded text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)]"
              onClick={() => {
                void toggleExpanded(entry.path)
              }}
              aria-label={isExpanded ? `Collapse ${entry.name}` : `Expand ${entry.name}`}
              disabled={disabled}
            >
              {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            </button>
            <button
              type="button"
              className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden text-left"
              onClick={() => setSelectedPath(entry.path)}
              title={entry.path}
              disabled={disabled}
            >
              <Folder size={15} className="shrink-0 text-[var(--app-text-muted)]" />
              <span className="truncate font-medium">{entry.name}</span>
              {meta ? <span className="truncate text-[11px] text-[var(--app-text-subtle)]">{meta}</span> : null}
            </button>
          </div>
        </div>
        {isExpanded ? (
          isLoading ? (
            <div className="ml-7 text-xs text-[var(--app-text-muted)]">Loading…</div>
          ) : childBrowser && childBrowser.entries.length > 0 ? (
            <ul className="ml-3 grid gap-1 border-l border-[var(--app-border)] pl-3">
              {childBrowser.entries.map((childEntry) => renderEntry(childEntry, depth + 1))}
            </ul>
          ) : (
            <div className="ml-7 text-xs text-[var(--app-text-subtle)]">empty</div>
          )
        ) : null}
      </li>
    )
  }, [browserByPath, disabled, expandedPaths, loadingPaths, selectedPath, toggleExpanded])

  return (
    <div className="grid gap-4">
      <Card className="grid gap-4 p-5">
        <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
          <div>
            <h3 className="text-base font-semibold text-[var(--app-text)]">Start from your workspaces</h3>
            <p className="text-sm text-[var(--app-text-muted)]">Use the same directories you already work with, then adjust any mount target or mode below.</p>
          </div>
          <Badge tone="neutral">{workspaceEntries.length} saved workspaces</Badge>
        </div>
        {workspaceEntries.length === 0 ? (
          <div className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
            No saved workspaces yet. You can still browse folders manually below.
          </div>
        ) : (
          <div className="grid gap-3 xl:grid-cols-2">
            {workspaceEntries.map((workspace) => {
              const directoryCount = Array.from(new Set([workspace.path, ...workspace.directories].filter(Boolean))).length
              return (
                <div key={workspace.path} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-semibold text-[var(--app-text)]">{workspace.workspaceName}</div>
                      <div className="mt-1 truncate text-xs text-[var(--app-text-muted)]">{workspace.path}</div>
                    </div>
                    <Badge tone="neutral">{directoryCount} dirs</Badge>
                  </div>
                  <div className="mt-3 flex flex-wrap gap-2">
                    <Button type="button" variant="outline" onClick={() => handleAddWorkspace(workspace)} disabled={disabled}>
                      <Plus size={14} />
                      Use workspace folders
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </Card>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,0.95fr)_minmax(320px,1.05fr)]">
        <Card className="grid gap-4 p-5">
          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
            <div>
              <h3 className="text-base font-semibold text-[var(--app-text)]">Browse folders</h3>
              <p className="text-sm text-[var(--app-text-muted)]">Same compact explorer pattern as the workspace launcher, but for container mounts.</p>
            </div>
            <div className="flex items-center gap-2">
              <Button type="button" variant="outline" onClick={() => handleAddSelectedPath()} disabled={disabled || !selectedPath}>
                <Plus size={14} />
                Add selected folder
              </Button>
              <Button
                type="button"
                variant="ghost"
                onClick={() => {
                  void loadPath('').catch((error) => {
                    setBrowserError(error instanceof Error ? error.message : 'Failed to load folders')
                  })
                }}
                disabled={disabled || loadingPaths[''] === true}
              >
                <RefreshCw size={14} className={loadingPaths[''] === true ? 'animate-spin' : undefined} />
                Refresh
              </Button>
            </div>
          </div>

          {browserError ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{browserError}</div> : null}

          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] p-3">
            <div className="mb-3 flex items-center gap-2 text-xs uppercase tracking-[0.14em] text-[var(--app-text-muted)]">
              <Home size={14} />
              Folder browser
            </div>
            {rootState ? (
              <ul className="grid gap-1">
                {rootState.entries.map((entry) => renderEntry(entry, 0))}
              </ul>
            ) : (
              <div className="text-sm text-[var(--app-text-muted)]">Loading folders…</div>
            )}
          </div>

          {selectedPath ? (
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
              <div className="text-xs uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Selected folder</div>
              <div className="mt-2 break-all text-[var(--app-text)]">{selectedPath}</div>
              <div className="mt-2 text-xs">Default target: {defaultMountTarget(selectedPath)}</div>
            </div>
          ) : null}
        </Card>

        <Card className="grid gap-4 p-5">
          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
            <div>
              <h3 className="text-base font-semibold text-[var(--app-text)]">Chosen mounts</h3>
              <p className="text-sm text-[var(--app-text-muted)]">Read/write is the obvious default. Switch to read-only only when you mean it.</p>
            </div>
            <Badge tone={mounts.length > 0 ? 'live' : 'neutral'}>{mounts.length} mounts</Badge>
          </div>

          {mounts.length === 0 ? (
            <div className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-5 text-sm text-[var(--app-text-muted)]">
              Add folders from a saved workspace or the browser and they will become part of this reusable container shape.
            </div>
          ) : (
            <div className="grid gap-3">
              {mounts.map((mount, index) => (
                <div key={`${mount.sourcePath}:${mount.targetPath}:${index}`} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                  <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
                    <div className="min-w-0">
                      <div className="break-all text-sm font-medium text-[var(--app-text)]">{mount.sourcePath}</div>
                      {mount.workspaceName ? <div className="mt-1 text-xs text-[var(--app-text-muted)]">From workspace {mount.workspaceName}</div> : null}
                    </div>
                    <Button type="button" variant="ghost" onClick={() => removeMount(index)} disabled={disabled}>
                      Remove
                    </Button>
                  </div>
                  <div className="mt-4 grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
                    <div className="grid gap-2">
                      <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Mount target inside container</label>
                      <Input
                        value={mount.targetPath}
                        onChange={(event) => updateMount(index, { targetPath: event.target.value })}
                        disabled={disabled}
                        placeholder="/workspace/project"
                      />
                    </div>
                    <div className="grid gap-2">
                      <label className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--app-text-muted)]">Access</label>
                      <div className="flex rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1">
                        {(['rw', 'ro'] as ContainerMountMode[]).map((mode) => (
                          <button
                            key={mode}
                            type="button"
                            onClick={() => updateMount(index, { mode })}
                            disabled={disabled}
                            className={cn(
                              'min-w-[88px] rounded-xl px-3 py-2 text-sm font-medium transition',
                              mount.mode === mode
                                ? 'bg-[var(--app-primary)] text-white shadow-sm'
                                : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                            )}
                          >
                            {mode === 'rw' ? 'Read/write' : 'Read-only'}
                          </button>
                        ))}
                      </div>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}
