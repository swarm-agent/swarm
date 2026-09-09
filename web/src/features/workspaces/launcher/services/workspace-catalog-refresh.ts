import type { QueryClient } from '@tanstack/react-query'
import { workspaceOverviewQueryKey, workspaceOverviewQueryOptions } from '../../../queries/query-options'
import type { WorkspaceOverviewResponse } from '../types/workspace-overview'

// One coordinator per shared cache, not per hook or workspace. A dirty bit retains
// mutations arriving during a request without cancelling/restarting that request.
const coordinators = new WeakMap<QueryClient, { pending: boolean; running?: Promise<void> }>()

export function refreshWorkspaceCatalog(queryClient: QueryClient): Promise<void> {
  let state = coordinators.get(queryClient)
  if (!state) {
    state = { pending: false }
    coordinators.set(queryClient, state)
  }
  state.pending = true
  if (state.running) return state.running
  const current = state
  current.running = Promise.resolve().then(async () => {
    while (current.pending) {
      current.pending = false
      // An older details request must not resurrect deleted catalog entries.
      await queryClient.cancelQueries({ queryKey: ['workspace-overview'] })
      await queryClient.invalidateQueries({ queryKey: ['workspace-overview'], refetchType: 'none' })
      const overview = await queryClient.fetchQuery({ ...workspaceOverviewQueryOptions([], 25, false), staleTime: 0 })
      if (current.pending) continue
      publishWorkspaceCatalog(queryClient, overview)
    }
  }).finally(() => { current.running = undefined })
  return current.running
}

export function publishWorkspaceCatalog(queryClient: QueryClient, catalog: WorkspaceOverviewResponse): void {
  const detailsKey = workspaceOverviewQueryKey([], 25)
  queryClient.setQueryData<WorkspaceOverviewResponse>(detailsKey, (previous) => ({
    ...catalog,
    workspaces: catalog.workspaces.map((workspace) => {
      const old = previous?.workspaces.find((entry) => entry.workspaceId === workspace.workspaceId && entry.path === workspace.path)
      if (!old) return workspace
      // Catalog owns membership, identity, names, themes and routes. Retain only
      // independently hydrated details; a lightweight update is not a Git reset.
      return {
        ...workspace,
        sessions: old.sessions,
        todoSummary: old.todoSummary,
        gitBranch: old.gitBranch,
        gitHasGit: old.gitHasGit,
        gitClean: old.gitClean,
        gitDirtyCount: old.gitDirtyCount,
        gitStagedCount: old.gitStagedCount,
        gitModifiedCount: old.gitModifiedCount,
        gitUntrackedCount: old.gitUntrackedCount,
        gitConflictCount: old.gitConflictCount,
        gitAheadCount: old.gitAheadCount,
        gitBehindCount: old.gitBehindCount,
        gitCommittedFileCount: old.gitCommittedFileCount,
        gitCommittedAdditions: old.gitCommittedAdditions,
        gitCommittedDeletions: old.gitCommittedDeletions,
      }
    }),
  }))
}
