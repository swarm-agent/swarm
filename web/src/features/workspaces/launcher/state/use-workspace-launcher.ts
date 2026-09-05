import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { applyWorkspaceTheme, setWorkspaceThemeCatalog, workspaceThemeDefaultId } from '../services/workspace-theme'
import { normalizeGlobalThemeSettings, type UISettingsWire } from '../../../desktop/settings/swarm/types/swarm-settings'
import { moveWorkspace } from '../mutations/move-workspace'
import { saveWorkspace as saveWorkspaceAPI } from '../mutations/save-workspace'
import { setupWorkspaceRepository as setupWorkspaceRepositoryAPI } from '../mutations/setup-workspace-repository'
import { createWorkspaceFolder as createWorkspaceFolderAPI } from '../mutations/create-workspace-folder'
import { deleteWorkspace as deleteWorkspaceAPI } from '../mutations/delete-workspace'
import { selectWorkspace } from '../mutations/select-workspace'
import { setWorkspaceTheme as setWorkspaceThemeAPI } from '../mutations/set-workspace-theme'
import { setWorkspaceIcon as setWorkspaceIconAPI } from '../mutations/set-workspace-icon'
import { setWorkspaceWorktrees } from '../mutations/set-workspace-worktrees'
import { refreshWorkspaceDefinitions as refreshWorkspaceDefinitionsAPI } from '../mutations/refresh-workspace-definitions'
import { sortDiscoveredWorkspaces, dedupeDiscoveredAgainstWorkspaces } from '../services/discovery-ordering'
import { loadLauncherCatalogFirst } from '../services/load-launcher-catalog-first'
import { syncWorkspaceOverviewWorktreeState } from '../services/workspace-overview-cache'
import { browseWorkspacePath } from '../queries/browse-workspace-path'
import { listWorkspaces } from '../queries/list-workspaces'
import { discoverWorkspaces } from '../queries/discover-workspaces'
import { uiSettingsQueryKey, uiSettingsQueryOptions, workspaceOverviewQueryKey, workspaceOverviewQueryOptions } from '../../../queries/query-options'
import type {
  WorkspaceBrowseResult,
  WorkspaceDiscoverEntry,
  WorkspaceEntry,
  WorkspaceResolution,
} from '../types/workspace'
import type { WorkspaceOverviewResponse, WorkspaceOverviewTopologyRoute } from '../types/workspace-overview'
import type { WorkspaceRepositoryState } from '../services/workspace-repository'

interface SaveWorkspaceInput {
  path: string
  name: string
  themeId: string
  makeCurrent: boolean
}

interface UseWorkspaceLauncherOptions {
  applyDocumentTheme?: boolean
  autoRefresh?: boolean
  browseDuringRefresh?: boolean
}

interface UseWorkspaceLauncherState {
  workspaces: WorkspaceEntry[]
  discovered: WorkspaceDiscoverEntry[]
  currentWorkspacePath: string | null
  loading: boolean
  refreshing: boolean
  personalizing: boolean
  personalizationMessage: string | null
  selectingPath: string | null
  savingPath: string | null
  draggingWorkspacePath: string | null
  browser: WorkspaceBrowseResult | null
  browserLoading: boolean
  browserError: string | null
  loadError: string | null
  actionError: string | null
  openWorkspace: (path: string) => Promise<WorkspaceResolution>
  deleteWorkspace: (path: string) => Promise<void>
  setWorktreeEnabled: (path: string, enabled: boolean) => Promise<void>
  saveWorkspace: (input: SaveWorkspaceInput) => Promise<WorkspaceResolution>
  setupWorkspaceRepository: (path: string, expectedResolvedPath: string) => Promise<WorkspaceRepositoryState>
  createFolder: (parentPath: string, name: string) => Promise<string>
  setWorkspaceTheme: (path: string, themeId: string) => Promise<void>
  setWorkspaceIcon: (path: string, iconPNGDataURL: string) => Promise<void>
  moveWorkspaceToIndex: (path: string, targetIndex: number) => Promise<void>
  swapWorkspacePositions: (sourcePath: string, targetPath: string) => Promise<void>
  setDraggingWorkspacePath: (path: string | null) => void
  refresh: (roots?: string[]) => Promise<void>
  refreshWorkspaceDefinitions: () => Promise<void>
  browsePath: (path: string) => Promise<void>
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

function topologyRouteArraysEqual(left: WorkspaceOverviewTopologyRoute[], right: WorkspaceOverviewTopologyRoute[]): boolean {
  if (left === right) {
    return true
  }
  if (left.length !== right.length) {
    return false
  }
  for (let index = 0; index < left.length; index += 1) {
    const leftRoute = left[index]
    const rightRoute = right[index]
    if (
      leftRoute.routeId !== rightRoute.routeId
      || leftRoute.routeSource !== rightRoute.routeSource
      || leftRoute.workspaceBindingId !== rightRoute.workspaceBindingId
      || leftRoute.runtimeSwarmId !== rightRoute.runtimeSwarmId
      || leftRoute.runtimeSwarmName !== rightRoute.runtimeSwarmName
      || leftRoute.runtimeKind !== rightRoute.runtimeKind
      || leftRoute.runtimeRelationship !== rightRoute.runtimeRelationship
      || leftRoute.authorityHostSwarmId !== rightRoute.authorityHostSwarmId
      || leftRoute.hostSwarmId !== rightRoute.hostSwarmId
      || leftRoute.hostWorkspacePath !== rightRoute.hostWorkspacePath
      || leftRoute.hostWorkspaceName !== rightRoute.hostWorkspaceName
      || leftRoute.runtimeWorkspacePath !== rightRoute.runtimeWorkspacePath
      || leftRoute.writable !== rightRoute.writable
      || leftRoute.createdAt !== rightRoute.createdAt
      || leftRoute.updatedAt !== rightRoute.updatedAt
    ) {
      return false
    }
  }
  return true
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
      || leftWorkspace.iconPNGDataURL !== rightWorkspace.iconPNGDataURL
      || leftWorkspace.definitionStatus !== rightWorkspace.definitionStatus
      || leftWorkspace.definition !== rightWorkspace.definition
      || leftWorkspace.definitionError !== rightWorkspace.definitionError
      || leftWorkspace.definitionSuggestion !== rightWorkspace.definitionSuggestion
      || leftWorkspace.definitionAttempts !== rightWorkspace.definitionAttempts
      || leftWorkspace.definitionGeneration !== rightWorkspace.definitionGeneration
      || leftWorkspace.definitionUpdatedAt !== rightWorkspace.definitionUpdatedAt
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
      || !topologyRouteArraysEqual(leftWorkspace.topologyRoutes, rightWorkspace.topologyRoutes)
    ) {
      return false
    }
    for (let i = 0; i < leftWorkspace.directories.length; i += 1) {
      if (leftWorkspace.directories[i] !== rightWorkspace.directories[i]) {
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
  return Array.isArray(roots) && roots.length === 0 && sessionLimit === 25 && Reflect.get(params, 'includeDetails') !== false
}

export function useWorkspaceLauncher(options: UseWorkspaceLauncherOptions = {}): UseWorkspaceLauncherState {
  const queryClient = useQueryClient()
  const applyDocumentTheme = options.applyDocumentTheme ?? true
  const autoRefresh = options.autoRefresh ?? true
  const browseDuringRefresh = options.browseDuringRefresh ?? true
  const cachedDetails = queryClient.getQueryState<WorkspaceOverviewResponse>(workspaceOverviewQueryKey([], 25))
  const cachedCatalog = queryClient.getQueryState<WorkspaceOverviewResponse>(workspaceOverviewQueryKey([], 25, false))
  const cachedOverview = cachedCatalog?.data && cachedCatalog.dataUpdatedAt > (cachedDetails?.dataUpdatedAt ?? 0)
    ? cachedCatalog.data
    : cachedDetails?.data ?? cachedCatalog?.data
  const [workspaces, setWorkspaces] = useState<WorkspaceEntry[]>(() => sortWorkspaces(cachedOverview?.workspaces ?? []))
  const [discovered, setDiscovered] = useState<WorkspaceDiscoverEntry[]>([])
  const [currentWorkspacePath, setCurrentWorkspacePath] = useState<string | null>(() => cachedOverview?.currentWorkspace?.resolvedPath?.trim() || null)
  const [loading, setLoading] = useState(autoRefresh && !cachedOverview)
  const refreshGeneration = useRef(0)
  const browserRef = useRef<WorkspaceBrowseResult | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [personalizing, setPersonalizing] = useState(false)
  const [personalizationMessage, setPersonalizationMessage] = useState<string | null>(null)
  const [selectingPath, setSelectingPath] = useState<string | null>(null)
  const [savingPath, setSavingPath] = useState<string | null>(null)
  const [draggingWorkspacePath, setDraggingWorkspacePath] = useState<string | null>(null)
  const [browser, setBrowser] = useState<WorkspaceBrowseResult | null>(null)
  const [browserLoading, setBrowserLoading] = useState(false)
  const [browserError, setBrowserError] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [globalThemeId, setGlobalThemeId] = useState(workspaceThemeDefaultId())

  const applyCurrentResolution = useCallback((resolution: WorkspaceResolution | null) => {
    const nextPath = resolution?.resolvedPath?.trim() || null
    const nextThemeId = resolution?.themeId?.trim() || ''
    setCurrentWorkspacePath(nextPath)
    if (applyDocumentTheme) {
      applyWorkspaceTheme(nextThemeId || globalThemeId)
    }
  }, [applyDocumentTheme, globalThemeId])

  const browsePath = useCallback(async (path: string) => {
    setBrowserLoading(true)
    setBrowserError(null)
    try {
      const nextBrowser = await browseWorkspacePath(path)
      browserRef.current = nextBrowser
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
        setWorkspaceThemeCatalog(settings.theme)
        setGlobalThemeId(normalizeGlobalThemeSettings(settings).activeId)
      })
      .catch(() => {
        if (!cancelled) {
          setGlobalThemeId(workspaceThemeDefaultId())
        }
      })

    return () => {
      cancelled = true
    }
  }, [queryClient])

  const refresh = useCallback(async (roots: string[] = []) => {
    const generation = ++refreshGeneration.current
    const isCurrent = () => generation === refreshGeneration.current
    setRefreshing(true)
    setLoadError(null)

    try {
      // Retire an older enrichment before publishing a newer catalog, otherwise
      // its shared-cache completion could resurrect deleted or reordered rows.
      await Promise.all([
        queryClient.cancelQueries({ queryKey: workspaceOverviewQueryKey(roots, 25), exact: true }),
        queryClient.cancelQueries({ queryKey: workspaceOverviewQueryKey(roots, 25, false), exact: true }),
      ])
      if (!isCurrent()) return
      await loadLauncherCatalogFirst({
        loadCatalog: () => queryClient.fetchQuery({
          ...workspaceOverviewQueryOptions(roots, 25, false),
          staleTime: 0,
        }),
        publishCatalog: (overview) => {
          const sorted = sortWorkspaces(overview.workspaces)
          const nextPath = overview.currentWorkspace?.resolvedPath?.trim() || null
          setWorkspaces((current) => (workspacesEqual(current, sorted) ? current : sorted))
          setCurrentWorkspacePath((current) => current === nextPath ? current : nextPath)
          setLoading(false)
        },
        loadDetails: () => queryClient.fetchQuery({ ...workspaceOverviewQueryOptions(roots, 25), staleTime: 0 }),
        publishDetails: (overview) => {
          const sorted = sortWorkspaces(overview.workspaces)
          setWorkspaces((current) => (workspacesEqual(current, sorted) ? current : sorted))
        },
        discover: () => discoverWorkspaces(1000, roots),
        publishDiscovery: (entries, overview) => {
          setDiscovered(sortDiscoveredWorkspaces(dedupeDiscoveredAgainstWorkspaces(entries, overview.workspaces.map((workspace) => workspace.path))))
        },
        reportBackgroundError: (error) => setActionError(error instanceof Error ? error.message : 'Failed to refresh workspace details'),
        isCurrent,
      })
      if (isCurrent() && browseDuringRefresh) {
        if (roots.length > 0) {
          void browsePath(roots[0])
        } else if (!browserRef.current) {
          void browsePath('')
        }
      }
    } catch (err) {
      if (isCurrent()) setLoadError(err instanceof Error ? err.message : 'Failed to load workspaces')
    } finally {
      if (isCurrent()) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [browseDuringRefresh, browsePath, queryClient])

  useEffect(() => {
    if (!autoRefresh) {
      setLoading(false)
      return
    }
    void refresh()
    return () => { refreshGeneration.current += 1 }
  }, [autoRefresh, refresh])

  useEffect(() => {
    if (loading) {
      return
    }

    if (applyDocumentTheme) {
      applyWorkspaceTheme(resolveEffectiveThemeId(currentWorkspacePath, workspaces, globalThemeId))
    }
  }, [applyDocumentTheme, currentWorkspacePath, globalThemeId, loading, workspaces])

  const hasPendingWorkspaceDefinition = workspaces.some((workspace) => workspace.definitionStatus === 'pending')

  useEffect(() => {
    if (!hasPendingWorkspaceDefinition) {
      return
    }
    const timer = window.setInterval(() => {
      void listWorkspaces()
        .then((latest) => {
          const latestByPath = new Map(latest.map((workspace) => [workspace.path, workspace]))
          setWorkspaces((current) => current.map((workspace) => {
            const updated = latestByPath.get(workspace.path)
            return updated
              ? {
                  ...workspace,
                  definitionStatus: updated.definitionStatus,
                  definition: updated.definition,
                  definitionError: updated.definitionError,
                  definitionSuggestion: updated.definitionSuggestion,
                  definitionAttempts: updated.definitionAttempts,
                  definitionGeneration: updated.definitionGeneration,
                  definitionUpdatedAt: updated.definitionUpdatedAt,
                }
              : workspace
          }))
        })
        .catch(() => {})
    }, 2_000)
    return () => window.clearInterval(timer)
  }, [hasPendingWorkspaceDefinition])

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
      setWorkspaceThemeCatalog(settings.theme)
      setGlobalThemeId(normalizeGlobalThemeSettings(settings).activeId)
    }

    let disposed = false
    const scheduleCacheSync = (sync: () => void) => {
      const setTimeoutFn = typeof window !== 'undefined' ? window.setTimeout.bind(window) : setTimeout
      setTimeoutFn(() => { if (!disposed) sync() }, 0)
    }

    // Initial state already uses the freshest catalog/details cache above.
    syncFromUISettingsCache()
    const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
      // React Query also emits observer option/result notifications while hooks are
      // rendering. Updating launcher state from those notifications can recurse
      // through React's setOptions path and trigger error #185 in production.
      if (event.type !== 'updated' || event.action.type !== 'success') {
        return
      }
      const queryKey = event.query.queryKey
      if (!Array.isArray(queryKey)) {
        return
      }
      if (isDefaultWorkspaceOverviewKey(queryKey)) {
        scheduleCacheSync(syncFromOverviewCache)
        return
      }
      if (queryKey.length === 1 && queryKey[0] === settingsKey[0]) {
        scheduleCacheSync(syncFromUISettingsCache)
      }
    })
    return () => { disposed = true; unsubscribe() }
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

  const persistWorkspace = useCallback(async (input: SaveWorkspaceInput) => {
    const targetPath = input.path.trim()
    setSavingPath(targetPath)
    setActionError(null)

    try {
      const resolution = await saveWorkspaceAPI(targetPath, input.name.trim(), input.themeId.trim() === 'inherit' ? '' : input.themeId.trim(), input.makeCurrent)
      setWorkspaces((current) => {
        const resolvedPath = resolution.resolvedPath.trim() || targetPath
        const next = current.map((workspace) => workspace.path === resolvedPath
          ? {
              ...workspace,
              definitionStatus: resolution.definitionStatus,
              definition: resolution.definition,
              definitionError: resolution.definitionError,
              definitionSuggestion: resolution.definitionSuggestion,
              definitionAttempts: resolution.definitionAttempts,
              definitionGeneration: resolution.definitionGeneration,
              definitionUpdatedAt: resolution.definitionUpdatedAt,
            }
          : workspace)
        return next
      })
      await refresh()
      await browsePath(resolution.resolvedPath)
      return resolution
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to save workspace')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [browsePath, refresh])

  const setupWorkspaceRepository = useCallback(async (path: string, expectedResolvedPath: string): Promise<WorkspaceRepositoryState> => {
    const targetPath = path.trim()
    setSavingPath(targetPath)
    setActionError(null)
    try {
      const repository = await setupWorkspaceRepositoryAPI(targetPath, expectedResolvedPath.trim())
      await refresh()
      await browsePath(repository.path || targetPath)
      return repository
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to initialize Git repository')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [browsePath, refresh])

  const createFolder = useCallback(async (parentPath: string, name: string): Promise<string> => {
    const trimmedParentPath = parentPath.trim()
    const trimmedName = name.trim()
    if (trimmedParentPath === '' || trimmedName === '') {
      return ''
    }
    setSavingPath(trimmedParentPath)
    setActionError(null)
    try {
      const folder = await createWorkspaceFolderAPI(trimmedParentPath, trimmedName)
      await browsePath(folder.parentPath || trimmedParentPath)
      return folder.path
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to create folder')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [browsePath])

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
        if (applyDocumentTheme) {
          applyWorkspaceTheme(globalThemeId)
        }
      }
      await refresh()
      await browsePath(deleted.resolvedPath)
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to delete workspace')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [applyDocumentTheme, globalThemeId, browsePath, currentWorkspacePath, refresh])

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
      if (applyDocumentTheme && currentWorkspacePath === trimmedPath) {
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
  }, [applyDocumentTheme, currentWorkspacePath, globalThemeId, queryClient, workspaces])

  const updateWorkspaceIcon = useCallback(async (path: string, iconPNGDataURL: string) => {
    const trimmedPath = path.trim()
    if (trimmedPath === '') return
    setSavingPath(trimmedPath)
    setActionError(null)
    try {
      const resolution = await setWorkspaceIconAPI(trimmedPath, iconPNGDataURL)
      setWorkspaces((current) => current.map((workspace) => (
        workspace.path === trimmedPath
          ? { ...workspace, iconPNGDataURL: resolution.iconPNGDataURL }
          : workspace
      )))
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to update workspace icon')
      throw err
    } finally {
      setSavingPath(null)
    }
  }, [])

  const refreshWorkspaceDefinitions = useCallback(async () => {
    setPersonalizing(true)
    setPersonalizationMessage(null)
    setActionError(null)
    try {
      const result = await refreshWorkspaceDefinitionsAPI()
      setWorkspaces((current) => {
        const pendingByPath = new Map(result.workspaces.map((workspace) => [workspace.path, workspace]))
        return current.map((workspace) => {
          const pending = pendingByPath.get(workspace.path)
          return pending ? { ...workspace, ...pending } : workspace
        })
      })
      const launchedLabel = `${result.launchedCount} workspace${result.launchedCount === 1 ? '' : 's'}`
      setPersonalizationMessage(result.failedCount > 0
        ? `Started Router personalization for ${launchedLabel}; ${result.failedCount} could not start.`
        : `Started Router personalization for ${launchedLabel}.`)
      if (result.failedCount > 0) {
        const firstFailure = result.failures[0]
        setActionError(firstFailure?.error || 'Some workspace personalization sessions could not start')
      }
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to personalize workspaces')
      throw err
    } finally {
      setPersonalizing(false)
    }
  }, [])

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

  const swapWorkspacePositions = useCallback(async (sourcePath: string, targetPath: string) => {
    const trimmedSourcePath = sourcePath.trim()
    const trimmedTargetPath = targetPath.trim()
    if (trimmedSourcePath === '' || trimmedTargetPath === '' || trimmedSourcePath === trimmedTargetPath) {
      return
    }

    const sourceIndex = workspaces.findIndex((workspace) => workspace.path === trimmedSourcePath)
    const targetIndex = workspaces.findIndex((workspace) => workspace.path === trimmedTargetPath)
    if (sourceIndex < 0 || targetIndex < 0 || sourceIndex === targetIndex) {
      return
    }

    setSavingPath(trimmedSourcePath)
    setActionError(null)
    try {
      await moveWorkspace(trimmedSourcePath, targetIndex - sourceIndex)
      if (sourceIndex < targetIndex) {
        const targetDelta = sourceIndex - (targetIndex - 1)
        if (targetDelta !== 0) {
          await moveWorkspace(trimmedTargetPath, targetDelta)
        }
      } else {
        const targetDelta = sourceIndex - (targetIndex + 1)
        if (targetDelta !== 0) {
          await moveWorkspace(trimmedTargetPath, targetDelta)
        }
      }
      await refresh()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : 'Failed to swap workspaces')
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
    personalizing,
    personalizationMessage,
    selectingPath,
    savingPath,
    draggingWorkspacePath,
    browser,
    browserLoading,
    browserError,
    loadError,
    actionError,
    openWorkspace,
    deleteWorkspace,
    setWorktreeEnabled: updateWorkspaceWorktreeEnabled,
    saveWorkspace: persistWorkspace,
    setupWorkspaceRepository,
    createFolder,
    setWorkspaceTheme: updateWorkspaceTheme,
    setWorkspaceIcon: updateWorkspaceIcon,
    moveWorkspaceToIndex,
    swapWorkspacePositions,
    setDraggingWorkspacePath,
    refresh,
    refreshWorkspaceDefinitions,
    browsePath,
  }
}
