import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { debugLog, createDebugTimer } from '../../../../lib/debug-log'
import { applyWorkspaceTheme, setWorkspaceThemeCustomOptions } from '../services/workspace-theme'
import { normalizeGlobalThemeSettings, type UISettingsWire } from '../../../desktop/settings/swarm/types/swarm-settings'
import { linkWorkspaceDirectory } from '../mutations/link-workspace-directory'
import { unlinkWorkspaceDirectory } from '../mutations/unlink-workspace-directory'
import { moveWorkspace } from '../mutations/move-workspace'
import { saveWorkspace as saveWorkspaceAPI } from '../mutations/save-workspace'
import { deleteWorkspace as deleteWorkspaceAPI } from '../mutations/delete-workspace'
import { selectWorkspace } from '../mutations/select-workspace'
import { setWorkspaceTheme as setWorkspaceThemeAPI } from '../mutations/set-workspace-theme'
import { setWorkspaceWorktrees } from '../mutations/set-workspace-worktrees'
import { sortDiscoveredWorkspaces, dedupeDiscoveredAgainstWorkspaces } from '../services/discovery-ordering'
import { syncWorkspaceOverviewWorktreeState } from '../services/workspace-overview-cache'
import { browseWorkspacePath } from '../queries/browse-workspace-path'
import { uiSettingsQueryKey, uiSettingsQueryOptions, workspaceOverviewQueryKey, workspaceOverviewQueryOptions } from '../../../queries/query-options'
import { useDesktopStore } from '../../../desktop/state/use-desktop-store'
import type {
  WorkspaceBrowseResult,
  WorkspaceDiscoverEntry,
  WorkspaceEntry,
  WorkspaceResolution,
} from '../types/workspace'
import type { WorkspaceOverviewResponse } from '../types/workspace-overview'

interface SaveWorkspaceInput {
  path: string
  name: string
  themeId: string
  makeCurrent: boolean
  linkedDirectory?: string
  linkedDirectories?: string[]
}

interface UseWorkspaceLauncherState {
  workspaces: WorkspaceEntry[]
  discovered: WorkspaceDiscoverEntry[]
  currentWorkspacePath: string | null
  loading: boolean
  refreshing: boolean
  selectingPath: string | null
  savingPath: string | null
  draggingWorkspacePath: string | null
  browser: WorkspaceBrowseResult | null
  browserLoading: boolean
  browserError: string | null
  loadError: string | null
  actionError: string | null
  openWorkspace: (path: string) => Promise<WorkspaceResolution>
  useFolderTemporarily: (path: string) => Promise<WorkspaceResolution>
  deleteWorkspace: (path: string) => Promise<void>
  unlinkWorkspaceDirectory: (workspacePath: string, directoryPath: string) => Promise<void>
  setWorktreeEnabled: (path: string, enabled: boolean) => Promise<void>
  saveWorkspace: (input: SaveWorkspaceInput) => Promise<void>
  setWorkspaceTheme: (path: string, themeId: string) => Promise<void>
  moveWorkspaceToIndex: (path: string, targetIndex: number) => Promise<void>
  setDraggingWorkspacePath: (path: string | null) => void
  refresh: (roots?: string[]) => Promise<void>
  browsePath: (path: string) => Promise<void>
}

const VAULT_LOCKED_ERROR_PREFIX = 'vault is locked'

function isVaultLockedError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : typeof error === 'string' ? error : ''
  return message.trim().toLowerCase().includes(VAULT_LOCKED_ERROR_PREFIX)
}

function sortWorkspaces<T extends WorkspaceEntry>(workspaces: T[]): T[] {
  return [...workspaces].sort((left, right) => {
    if (left.sortIndex !== right.sortIndex) {
      return left.sortIndex - right.sortIndex
    }

    if (left.lastSelectedAt !== right.lastSelectedAt) {
      return right.lastSelectedAt - left.lastSelectedAt
    }

    return left.workspaceName.localeCompare(right.workspaceName)
  })
}

function resolveEffectiveThemeId(
  currentWorkspacePath: string | null,
  workspaces: WorkspaceEntry[],
  globalThemeId: string,
): string {
  if (currentWorkspacePath) {
    const activeWorkspace = workspaces.find((workspace) => workspace.path === currentWorkspacePath)
    const workspaceThemeId = activeWorkspace?.themeId?.trim().toLowerCase() || ''
    if (workspaceThemeId) {
      return workspaceThemeId
    }
  }

  return globalThemeId.trim().toLowerCase() || ''
}

function patchWorkspaceWorktreeEnabled(workspaces: WorkspaceEntry[], path: string, enabled: boolean): WorkspaceEntry[] {
  let changed = false
  const next = workspaces.map((workspace) => {
    if (workspace.path !== path || workspace.worktreeEnabled === enabled) {
      return workspace
    }
    changed = true
    return {
      ...workspace,
      worktreeEnabled: enabled,
    }
  })
  return changed ? next : workspaces
}

function workspacesEqual(left: WorkspaceEntry[], right: WorkspaceEntry[]): boolean {
  if (left === right) {
    return true
  }
  if (left.length !== right.length) {
    return false
  }
  for (let index = 0; index < left.length; index += 1) {
    const leftWorkspace = left[index]
    const rightWorkspace = right[index]
    if (
      leftWorkspace.path !== rightWorkspace.path
      || leftWorkspace.workspaceName !== rightWorkspace.workspaceName
      || leftWorkspace.themeId !== rightWorkspace.themeId
      || leftWorkspace.isGitRepo !== rightWorkspace.isGitRepo
      || leftWorkspace.sortIndex !== rightWorkspace.sortIndex
      || leftWorkspace.addedAt !== rightWorkspace.addedAt
      || leftWorkspace.updatedAt !== rightWorkspace.updatedAt
      || leftWorkspace.lastSelectedAt !== rightWorkspace.lastSelectedAt
      || leftWorkspace.active !== rightWorkspace.active
      || leftWorkspace.worktreeEnabled !== rightWorkspace.worktreeEnabled
      || leftWorkspace.gitBranch !== rightWorkspace.gitBranch
      || leftWorkspace.gitHasGit !== rightWorkspace.gitHasGit
      || leftWorkspace.gitClean !== rightWorkspace.gitClean
      || leftWorkspace.gitDirtyCount !== rightWorkspace.gitDirtyCount
      || leftWorkspace.gitStagedCount !== rightWorkspace.gitStagedCount
      || leftWorkspace.gitModifiedCount !== rightWorkspace.gitModifiedCount
      || leftWorkspace.gitUntrackedCount !== rightWorkspace.gitUntrackedCount
      || leftWorkspace.gitConflictCount !== rightWorkspace.gitConflictCount
      || leftWorkspace.gitAheadCount !== rightWorkspace.gitAheadCount
      || leftWorkspace.gitBehindCount !== rightWorkspace.gitBehindCount
      || leftWorkspace.gitCommittedFileCount !== rightWorkspace.gitCommittedFileCount
      || leftWorkspace.gitCommittedAdditions !== rightWorkspace.gitCommittedAdditions
      || leftWorkspace.gitCommittedDeletions !== rightWorkspace.gitCommittedDeletions
      || leftWorkspace.todoSummary?.taskCount !== rightWorkspace.todoSummary?.taskCount
      || leftWorkspace.todoSummary?.openCount !== rightWorkspace.todoSummary?.openCount
      || leftWorkspace.todoSummary?.inProgressCount !== rightWorkspace.todoSummary?.inProgressCount
      || leftWorkspace.todoSummary?.user?.taskCount !== rightWorkspace.todoSummary?.user?.taskCount
      || leftWorkspace.todoSummary?.user?.openCount !== rightWorkspace.todoSummary?.user?.openCount
      || leftWorkspace.todoSummary?.user?.inProgressCount !== rightWorkspace.todoSummary?.user?.inProgressCount
      || leftWorkspace.todoSummary?.agent?.taskCount !== rightWorkspace.todoSummary?.agent?.taskCount
      || leftWorkspace.todoSummary?.agent?.openCount !== rightWorkspace.todoSummary?.agent?.openCount
      || leftWorkspace.todoSummary?.agent?.inProgressCount !== rightWorkspace.todoSummary?.agent?.inProgressCount
      || leftWorkspace.directories.length !== rightWorkspace.directories.length
      || leftWorkspace.replicationLinks.length !== rightWorkspace.replicationLinks.length
    ) {
      return false
    }
    for (let i = 0; i < leftWorkspace.directories.length; i += 1) {
      if (leftWorkspace.directories[i] !== rightWorkspace.directories[i]) {
        return false
      }
    }
    for (let i = 0; i < leftWorkspace.replicationLinks.length; i += 1) {
      const leftLink = leftWorkspace.replicationLinks[i]
      const rightLink = rightWorkspace.replicationLinks[i]
      if (
        leftLink.id !== rightLink.id
        || leftLink.targetKind !== rightLink.targetKind
        || leftLink.targetSwarmId !== rightLink.targetSwarmId
        || leftLink.targetSwarmName !== rightLink.targetSwarmName
        || leftLink.targetWorkspacePath !== rightLink.targetWorkspacePath
        || leftLink.replicationMode !== rightLink.replicationMode
        || leftLink.writable !== rightLink.writable
        || leftLink.createdAt !== rightLink.createdAt
        || leftLink.updatedAt !== rightLink.updatedAt
        || leftLink.sync.enabled !== rightLink.sync.enabled
        || leftLink.sync.mode !== rightLink.sync.mode
      ) {
        return false
      }
    }
  }
  return true
}

function isDefaultWorkspaceOverviewKey(queryKey: readonly unknown[]): boolean {
  if (queryKey[0] !== 'workspace-overview') {
    return false
  }
  const params = queryKey[1]
  if (!params || typeof params !== 'object') {
    return false
  }
  const roots = Reflect.get(params, 'roots')
  const sessionLimit = Reflect.get(params, 'sessionLimit')
  return Array.isArray(roots) && roots.length === 0 && sessionLimit === 25
}

export function useWorkspaceLauncher(): UseWorkspaceLauncherState {
  const queryClient = useQueryClient()
  const refreshVaultStatus = useDesktopStore((state) => state.refreshVaultStatus)
  const [workspaces, setWorkspaces] = useState<WorkspaceEntry[]>([])
  const [discovered, setDiscovered] = useState<WorkspaceDiscoverEntry[]>([])
  const [currentWorkspacePath, setCurrentWorkspacePath] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [selectingPath, setSelectingPath] = useState<string | null>(null)
  const [savingPath, setSavingPath] = useState<string | null>(null)
  const [draggingWorkspacePath, setDraggingWorkspacePath] = useState<string | null>(null)
  const [browser, setBrowser] = useState<WorkspaceBrowseResult | null>(null)
  const [browserLoading, setBrowserLoading] = useState(false)
  const [browserError, setBrowserError] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [globalThemeId, setGlobalThemeId] = useState('nord')

  const applyCurrentResolution = useCallback((resolution: WorkspaceResolution | null) => {
    const nextPath = resolution?.resolvedPath?.trim() || null
    const nextThemeId = resolution?.themeId?.trim() || ''
    setCurrentWorkspacePath(nextPath)
    applyWorkspaceTheme(nextThemeId || globalThemeId)
  }, [globalThemeId])

  const browsePath = useCallback(async (path: string) => {
    setBrowserLoading(true)
    setBrowserError(null)
    try {
      const nextBrowser = await browseWorkspacePath(path)
      setBrowser(nextBrowser)
    } catch (err) {
      setBrowserError(err instanceof Error ? err.message : 'Failed to browse folder')
    } finally {
      setBrowserLoading(false)
    }
  }, [])

  useEffect(() => {
    let cancelled = false

    void queryClient.fetchQuery(uiSettingsQueryOptions())
      .then((settings) => {
        if (cancelled) {
          return
        }
        setWorkspaceThemeCustomOptions(settings.theme?.custom_themes ?? [])
        setGlobalThemeId(normalizeGlobalThemeSettings(settings).activeId)
      })
      .catch(() => {
        if (!cancelled) {
          setGlobalThemeId('nord')
        }
      })

    return () => {
      cancelled = true
    }
  }, [queryClient])

  const refresh = useCallback(async (roots: string[] = []) => {
    const finish = createDebugTimer('workspace-launcher', 'refresh', {
      roots,
      browserPresent: Boolean(browser),
    })
    debugLog('workspace-launcher', 'refresh:enter', {
      roots,
      browserPresent: Boolean(browser),
      globalThemeId,
    })
    setRefreshing(true)
    setLoadError(null)

    try {
      const overview = await queryClient.fetchQuery({
        ...workspaceOverviewQueryOptions(roots, 25),
        staleTime: 0,
      })
      debugLog('workspace-launcher', 'refresh:overview-ready', {
        workspaceCount: overview.workspaces.length,
        discoveredCount: overview.discovered.length,
        currentWorkspacePath: overview.currentWorkspace?.resolvedPath?.trim() || null,
      })
      const sorted = sortWorkspaces(overview.workspaces)
      const knownPaths = new Set(sorted.map((workspace) => workspace.path))
      const nextCurrentWorkspacePath = overview.currentWorkspace?.resolvedPath?.trim() || null
      setWorkspaces((current) => (workspacesEqual(current, sorted) ? current : sorted))
      setDiscovered(sortDiscoveredWorkspaces(dedupeDiscoveredAgainstWorkspaces(overview.discovered, Array.from(knownPaths))))
      setCurrentWorkspacePath((current) => (current === nextCurrentWorkspacePath ? current : nextCurrentWorkspacePath))
      applyWorkspaceTheme(resolveEffectiveThemeId(nextCurrentWorkspacePath, sorted, globalThemeId))
      if (roots.length > 0) {
        debugLog('workspace-launcher', 'refresh:browse-root', { root: roots[0] })
        await browsePath(roots[0])
      } else if (!browser) {
        debugLog('workspace-launcher', 'refresh:browse-empty-root')
        await browsePath('')
      }
      finish({ ok: true, workspaceCount: sorted.length })
    } catch (err) {
      debugLog('workspace-launcher', 'refresh:error', {
        message: err instanceof Error ? err.message : String(err),
      })
      if (isVaultLockedError(err)) {
        try {
          await refreshVaultStatus()
          const { vault } = useDesktopStore.getState()
          if (vault.enabled && !vault.unlocked) {
            finish({ ok: false, stopped: 'vault-locked' })
            return
          }
        } catch {
          // Fall back to the launcher error below when vault status refresh fails.
        }
      }
      setLoadError(err instanceof Error ? err.message : 'Failed to load workspaces')
      finish({ ok: false })
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [browser, browsePath, globalThemeId, queryClient, refreshVaultStatus])

  useEffect(() => {
    debugLog('workspace-launcher', 'effect:initial-refresh')
    void refresh()
  }, [refresh])

  useEffect(() => {
    if (loading) {
      return
    }

    applyWorkspaceTheme(resolveEffectiveThemeId(currentWorkspacePath, workspaces, globalThemeId))
  }, [currentWorkspacePath, globalThemeId, loading, workspaces])

  useEffect(() => {
    const defaultOverviewKey = workspaceOverviewQueryKey([], 25)
    const settingsKey = uiSettingsQueryKey()
    const syncFromOverviewCache = () => {
      const overview = queryClient.getQueryData<WorkspaceOverviewResponse>(defaultOverviewKey)
      if (!overview) {
        return
      }
      const sorted = sortWorkspaces(overview.workspaces)
      setWorkspaces((current) => (workspacesEqual(current, sorted) ? current : sorted))
      const nextCurrentWorkspacePath = overview.currentWorkspace?.resolvedPath?.trim() || null
      setCurrentWorkspacePath((current) => (current === nextCurrentWorkspacePath ? current : nextCurrentWorkspacePath))
    }
    const syncFromUISettingsCache = () => {
      const settings = queryClient.getQueryData<UISettingsWire>(settingsKey)
      if (!settings) {
        return
      }
      setWorkspaceThemeCustomOptions(settings.theme?.custom_themes ?? [])
      setGlobalThemeId(normalizeGlobalThemeSettings(settings).activeId)
    }

    syncFromOverviewCache()
    syncFromUISettingsCache()
    return queryClient.getQueryCache().subscribe((event) => {
      const queryKey = event?.query?.queryKey
      if (!Array.isArray(queryKey)) {
        return
      }
      if (isDefaultWorkspaceOverviewKey(queryKey)) {
        syncFromOverviewCache()
        return
      }
      if (queryKey.length === 1 && queryKey[0] === settingsKey[0]) {
        syncFromUISettingsCache()
      }
    })
  }, [queryClient])

  const openWorkspace = useCallback(async (path: string) => {
    setSelectingPath(path)
    setActionError(null)

    try {
      const resolution = await selectWorkspace(path)
      const resolvedPath = resolution.resolvedPath.trim() || path
      applyCurrentResolution(resolution)
      setWorkspaces((current) =>
        current.map((workspace) => ({
          ...workspace,
          active: workspace.path === resolvedPath,
        })),
      )
      return resolution
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to open workspace')
      throw err
    } finally {
      setSelectingPath(null)
    }
  }, [applyCurrentResolution])

  const useFolderTemporarily = useCallback(async (path: string) => {
    const trimmedPath = path.trim()
    setSelectingPath(trimmedPath)
    setActionError(null)
    try {
      const browserResult = await browseWorkspacePath(trimmedPath)
      const resolution = {
        requestedPath: trimmedPath,
        resolvedPath: browserResult.resolvedPath,
        workspaceName: browserResult.resolvedPath.split(/[\\/]/).filter(Boolean).pop() || browserResult.resolvedPath,
        themeId: '',
      }
      applyCurrentResolution(resolution)
      setCurrentWorkspacePath(browserResult.resolvedPath)
      useDesktopStore.getState().setActiveWorkspacePath(browserResult.resolvedPath)
      return resolution
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to open folder')
      throw err
    } finally {
      setSelectingPath(null)
    }
  }, [applyCurrentResolution])

  const persistWorkspace = useCallback(async (input: SaveWorkspaceInput) => {
    const targetPath = input.path.trim()
    setSavingPath(targetPath)
    setActionError(null)

    try {
      const resolution = await saveWorkspaceAPI(targetPath, input.name.trim(), input.themeId.trim() === 'inherit' ? '' : input.themeId.trim(), input.makeCurrent)
      const linkedDirectories = [
        ...(input.linkedDirectory && input.linkedDirectory.trim() !== '' ? [input.linkedDirectory.trim()] : []),
        ...((input.linkedDirectories ?? []).map((value) => value.trim()).filter((value) => value !== '')),
      ]

      for (const directory of linkedDirectories) {
        await linkWorkspaceDirectory(resolution.resolvedPath, directory)
      }
      await refresh()
      await browsePath(resolution.resolvedPath)
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to save workspace')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [browsePath, refresh])

  const deleteWorkspace = useCallback(async (path: string) => {
    const trimmedPath = path.trim()
    if (trimmedPath === '') {
      return
    }
    setSavingPath(trimmedPath)
    setActionError(null)
    try {
      const deleted = await deleteWorkspaceAPI(trimmedPath)
      setWorkspaces((current) => current.filter((workspace) => workspace.path !== deleted.resolvedPath))
      if (currentWorkspacePath === deleted.resolvedPath) {
        setCurrentWorkspacePath(null)
        applyWorkspaceTheme(globalThemeId)
        useDesktopStore.getState().setActiveWorkspacePath(null)
      }
      await refresh()
      await browsePath(deleted.resolvedPath)
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to delete workspace')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [globalThemeId, browsePath, currentWorkspacePath, refresh])

  const removeWorkspaceDirectory = useCallback(async (workspacePath: string, directoryPath: string) => {
    const trimmedWorkspacePath = workspacePath.trim()
    const trimmedDirectoryPath = directoryPath.trim()
    if (trimmedWorkspacePath === '' || trimmedDirectoryPath === '') {
      return
    }
    setSavingPath(trimmedWorkspacePath)
    setActionError(null)
    try {
      await unlinkWorkspaceDirectory(trimmedWorkspacePath, trimmedDirectoryPath)
      await refresh()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to remove linked folder')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [refresh])

  const updateWorkspaceWorktreeEnabled = useCallback(async (path: string, enabled: boolean) => {
    const trimmedPath = path.trim()
    if (trimmedPath === '') {
      return
    }

    const previousEnabled = workspaces.find((workspace) => workspace.path === trimmedPath)?.worktreeEnabled
    const applyOptimisticState = (nextEnabled: boolean) => {
      setWorkspaces((current) => patchWorkspaceWorktreeEnabled(current, trimmedPath, nextEnabled))
      syncWorkspaceOverviewWorktreeState(queryClient, trimmedPath, nextEnabled)
    }

    setSavingPath(trimmedPath)
    setActionError(null)
    try {
      applyOptimisticState(enabled)
      const resolvedEnabled = await setWorkspaceWorktrees(trimmedPath, enabled)
      applyOptimisticState(resolvedEnabled)
      void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
    } catch (err) {
      if (typeof previousEnabled === 'boolean') {
        applyOptimisticState(previousEnabled)
      }
      setActionError(err instanceof Error ? err.message : 'Failed to update worktree setting')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [queryClient, workspaces])

  const updateWorkspaceTheme = useCallback(async (path: string, themeId: string) => {
    const trimmedPath = path.trim()
    const normalizedThemeId = themeId.trim().toLowerCase() === 'inherit' ? '' : themeId.trim().toLowerCase()
    if (trimmedPath === '') {
      return
    }

    const previousWorkspace = workspaces.find((workspace) => workspace.path === trimmedPath) ?? null
    setSavingPath(trimmedPath)
    setActionError(null)

    const applyOptimisticTheme = (nextThemeId: string) => {
      setWorkspaces((current) => current.map((workspace) => (
        workspace.path === trimmedPath
          ? { ...workspace, themeId: nextThemeId }
          : workspace
      )))
      if (currentWorkspacePath === trimmedPath) {
        applyWorkspaceTheme(nextThemeId || globalThemeId)
      }
    }

    try {
      const resolution = await setWorkspaceThemeAPI(trimmedPath, normalizedThemeId)
      applyOptimisticTheme(resolution.themeId?.trim().toLowerCase() || '')
      void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
    } catch (err) {
      if (previousWorkspace) {
        applyOptimisticTheme(previousWorkspace.themeId)
      }
      setActionError(err instanceof Error ? err.message : 'Failed to update workspace theme')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [currentWorkspacePath, globalThemeId, queryClient, workspaces])

  const moveWorkspaceToIndex = useCallback(async (path: string, targetIndex: number) => {
    const trimmedPath = path.trim()
    if (trimmedPath === '') {
      return
    }

    const currentIndex = workspaces.findIndex((workspace) => workspace.path === trimmedPath)
    if (currentIndex < 0) {
      return
    }
    const boundedTarget = Math.max(0, Math.min(targetIndex, workspaces.length - 1))
    const delta = boundedTarget - currentIndex
    if (delta === 0) {
      return
    }

    setSavingPath(trimmedPath)
    setActionError(null)
    try {
      await moveWorkspace(trimmedPath, delta)
      await refresh()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to reorder workspace')
      throw err
    } finally {
      setSavingPath(null)
      setDraggingWorkspacePath(null)
    }
  }, [refresh, workspaces])

  const decoratedWorkspaces = useMemo(() => workspaces, [workspaces])

  return {
    workspaces: decoratedWorkspaces,
    discovered,
    currentWorkspacePath,
    loading,
    refreshing,
    selectingPath,
    savingPath,
    draggingWorkspacePath,
    browser,
    browserLoading,
    browserError,
    loadError,
    actionError,
    openWorkspace,
    useFolderTemporarily,
    deleteWorkspace,
    unlinkWorkspaceDirectory: removeWorkspaceDirectory,
    setWorktreeEnabled: updateWorkspaceWorktreeEnabled,
    saveWorkspace: persistWorkspace,
    setWorkspaceTheme: updateWorkspaceTheme,
    moveWorkspaceToIndex,
    setDraggingWorkspacePath,
    refresh,
    browsePath,
  }
}
