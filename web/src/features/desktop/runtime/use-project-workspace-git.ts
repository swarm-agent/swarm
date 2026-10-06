import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import { backgroundRead } from '../../../app/background-read'
import { fetchGitStatus, gitStatusQueryKey } from '../git/api'
import { subscribeGit } from '../git/subscriptions'
import type { GitSnapshot } from '../git/types'
import type { ProjectSummary, RunningTask } from '../orchestrate/orchestrate-types'

export interface ProjectWorkspace {
  key: string
  id?: string
  path: string
  name: string
}
export interface ProjectWorkspaceGit extends ProjectWorkspace {
  status?: GitSnapshot
  loading: boolean
  error?: string
}

export function projectWorkspaces(project?: ProjectSummary): ProjectWorkspace[] {
  const catalog: NonNullable<ProjectSummary['workspaces']> = project?.workspaces ?? project?.linkedWorkspaces.map(path => ({ path })) ?? []
  return catalog.map(workspace => ({
    key: workspace.path,
    id: workspace.workspace_id,
    path: workspace.path,
    name: workspace.label || workspace.path.split(/[\\/]/).filter(Boolean).slice(-1)[0] || 'Workspace',
  }))
}

// Prefer the explicit source identity: workspacePath can be an isolated task
// worktree. Never infer ownership from labels or a prefix shared by nested repos.
export function taskMatchesWorkspace(task: RunningTask, workspace: ProjectWorkspace): boolean {
  if (task.sourceWorkspaceId && workspace.id) return task.sourceWorkspaceId === workspace.id
  if (task.sourceWorkspacePath) return task.sourceWorkspacePath === workspace.path
  return task.workspacePath === workspace.path || Boolean(task.workspacesInvolved?.includes(workspace.path))
}

// Push bursts join the current read and cause at most one trailing refresh per
// burst on completion. No retry timer, polling, or cancellation of loaded data.
export function coalescedGitRefresh(refresh: () => Promise<unknown>) {
  let running = false
  let pending = false
  let disposed = false
  return {
    request() {
      if (disposed) return
      pending = true
      if (running) return
      running = true
      void (async () => {
        try {
          while (pending && !disposed) {
            pending = false
            await refresh()
          }
        } catch {
          // Query state exposes read failures; only a new notice/user retry
          // requests another read. Never spin on an unavailable repository.
        } finally { running = false }
      })()
    },
    dispose() { disposed = true; pending = false },
  }
}

export function useProjectWorkspaceGit(workspaces: ProjectWorkspace[]) {
  const queryClient = useQueryClient()
  const [watchErrors, setWatchErrors] = useState<Record<string, string | undefined>>({})
  const [watchRevision, setWatchRevision] = useState(0)
  const selectorsKey = JSON.stringify(workspaces.map(workspace => workspace.path))
  const paths: string[] = useMemo(() => JSON.parse(selectorsKey), [selectorsKey])
  const queries = useQueries({ queries: workspaces.map(workspace => ({
    queryKey: gitStatusQueryKey(workspace.path),
    queryFn: ({ signal }: { signal: AbortSignal }) => backgroundRead(async () => {
      const response = await fetchGitStatus(workspace.path, 0, '', signal)
      if (!response.ok) throw new Error('Git status unavailable')
      return response
    }, signal),
    enabled: Boolean(workspace.path && workspace.path !== '.'),
    retry: false,
    refetchOnWindowFocus: false,
    refetchInterval: false as const,
  })) })
  const branchesKey = JSON.stringify(queries.map(query => query.data?.status.branch || ''))
  const branches: string[] = useMemo(() => JSON.parse(branchesKey), [branchesKey])
  useEffect(() => {
    setWatchErrors({})
    const lanes = paths.map(path => coalescedGitRefresh(async () => {
      const queryKey = gitStatusQueryKey(path)
      const alreadyReading = queryClient.getQueryState(queryKey)?.fetchStatus === 'fetching'
      await queryClient.invalidateQueries({ queryKey, exact: true }, { cancelRefetch: false })
      // An initial/shared read may have captured its snapshot before this
      // notice. Join it first, then repair the gap without cancelling it.
      if (alreadyReading) await queryClient.invalidateQueries({ queryKey, exact: true }, { cancelRefetch: false })
    }))
    const unsubscribe = subscribeGit(paths.map((path, index) => ({ workspace_path: path, branch: branches[index] })), notice => {
      const path = paths[notice.index]
      if (!path) return
      if (notice.kind === 'lost') {
        setWatchErrors(previous => ({ ...previous, [path]: notice.error || 'Live Git status unavailable' }))
      } else {
        setWatchErrors(previous => previous[path] ? { ...previous, [path]: undefined } : previous)
        lanes[notice.index].request() // ready also repairs a missed transport gap
      }
    })
    return () => { unsubscribe(); lanes.forEach(lane => lane.dispose()) }
  }, [paths, branches, queryClient, watchRevision])
  const refresh = useCallback(() => {
    setWatchRevision(previous => previous + 1)
    paths.forEach(path => { void queryClient.invalidateQueries({ queryKey: gitStatusQueryKey(path), exact: true }) })
  }, [paths, queryClient])
  const rows: ProjectWorkspaceGit[] = workspaces.map((workspace, index) => ({
    ...workspace,
    status: queries[index].data?.status,
    loading: queries[index].isPending && queries[index].fetchStatus !== 'idle',
    error: queries[index].error ? queries[index].error.message : watchErrors[workspace.path]
      || (!workspace.path || workspace.path === '.' ? 'Workspace path unavailable' : undefined),
  }))
  return { workspaces: rows, refresh }
}
