import type { QueryClient } from '@tanstack/react-query'
import type { DesktopSessionRecord } from '../../../desktop/types/realtime'
import type { WorkspaceOverviewResponse, WorkspaceOverviewWorkspace } from '../types/workspace-overview'

const WORKSPACE_OVERVIEW_QUERY_KEY_PREFIX = ['workspace-overview'] as const

function normalizeWorkspacePath(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) {
    return ''
  }
  return trimmed.replace(/[\\/]+$/, '')
}

function pathInWorkspaceScope(sessionPath: string, workspacePath: string): boolean {
  const normalizedSessionPath = normalizeWorkspacePath(sessionPath)
  const normalizedWorkspacePath = normalizeWorkspacePath(workspacePath)
  if (!normalizedSessionPath || !normalizedWorkspacePath) {
    return false
  }
  if (normalizedSessionPath === normalizedWorkspacePath) {
    return true
  }
  return normalizedSessionPath.startsWith(`${normalizedWorkspacePath}/`)
    || normalizedSessionPath.startsWith(`${normalizedWorkspacePath}\\`)
}

function nearestWorkspaceScopePath(workspaces: WorkspaceOverviewWorkspace[], sessionPath: string): string {
  let matchedWorkspacePath = ''
  for (const workspace of workspaces) {
    const normalizedWorkspacePath = normalizeWorkspacePath(workspace.path)
    if (!pathInWorkspaceScope(sessionPath, normalizedWorkspacePath)) {
      continue
    }
    if (normalizedWorkspacePath.length > matchedWorkspacePath.length) {
      matchedWorkspacePath = normalizedWorkspacePath
    }
  }
  return matchedWorkspacePath
}

function sortOverviewSessions(sessions: DesktopSessionRecord[]): DesktopSessionRecord[] {
  return [...sessions].sort((left, right) => {
    const liveDelta = Number(Boolean(right.live.lastEventAt)) - Number(Boolean(left.live.lastEventAt))
    if (liveDelta !== 0) {
      return liveDelta
    }
    return right.updatedAt - left.updatedAt
  })
}

function mergeOverviewSession(existing: DesktopSessionRecord | null, incoming: DesktopSessionRecord): DesktopSessionRecord {
  if (!existing) {
    return incoming
  }

  return {
    ...existing,
    ...incoming,
    live: incoming.live,
    pendingPermissions: incoming.pendingPermissions,
    pendingPermissionCount: incoming.pendingPermissionCount,
  }
}

function patchWorkspace(
  workspace: WorkspaceOverviewWorkspace,
  session: DesktopSessionRecord,
  matchedWorkspacePath: string,
): { changed: boolean; workspace: WorkspaceOverviewWorkspace } {
  if (normalizeWorkspacePath(workspace.path) !== matchedWorkspacePath) {
    return { changed: false, workspace }
  }

  const existingIndex = workspace.sessions.findIndex((entry) => entry.id === session.id)
  const nextSessions = existingIndex >= 0
    ? workspace.sessions.map((entry, index) => (index === existingIndex ? mergeOverviewSession(entry, session) : entry))
    : [session, ...workspace.sessions]

  return {
    changed: true,
    workspace: {
      ...workspace,
      sessions: sortOverviewSessions(nextSessions),
    },
  }
}

export function syncWorkspaceOverviewWorktreeState(queryClient: QueryClient, workspacePath: string, enabled: boolean): void {
  const normalizedWorkspacePath = normalizeWorkspacePath(workspacePath)
  if (!normalizedWorkspacePath) {
    return
  }

  const queries = queryClient.getQueryCache().findAll({ queryKey: WORKSPACE_OVERVIEW_QUERY_KEY_PREFIX })
  for (const query of queries) {
    queryClient.setQueryData<WorkspaceOverviewResponse | undefined>(query.queryKey, (current) => {
      if (!current) {
        return current
      }

      let changed = false
      const workspaces = current.workspaces.map((workspace) => {
        if (normalizeWorkspacePath(workspace.path) !== normalizedWorkspacePath || workspace.worktreeEnabled === enabled) {
          return workspace
        }
        changed = true
        return {
          ...workspace,
          worktreeEnabled: enabled,
        }
      })

      if (!changed) {
        return current
      }

      return {
        ...current,
        workspaces,
      }
    })
  }
}

export function syncWorkspaceOverviewThemeState(queryClient: QueryClient, workspacePath: string, themeId: string): void {
  const normalizedWorkspacePath = normalizeWorkspacePath(workspacePath)
  const normalizedThemeId = themeId.trim().toLowerCase()
  if (!normalizedWorkspacePath) {
    return
  }

  const queries = queryClient.getQueryCache().findAll({ queryKey: WORKSPACE_OVERVIEW_QUERY_KEY_PREFIX })
  for (const query of queries) {
    queryClient.setQueryData<WorkspaceOverviewResponse | undefined>(query.queryKey, (current) => {
      if (!current) {
        return current
      }

      let changed = false
      const workspaces = current.workspaces.map((workspace) => {
        if (normalizeWorkspacePath(workspace.path) !== normalizedWorkspacePath || workspace.themeId === normalizedThemeId) {
          return workspace
        }
        changed = true
        return {
          ...workspace,
          themeId: normalizedThemeId,
        }
      })

      if (!changed) {
        return current
      }

      return {
        ...current,
        workspaces,
      }
    })
  }
}

export function syncWorkspaceOverviewSession(queryClient: QueryClient, session: DesktopSessionRecord): void {
  if (!session.id.trim() || !session.workspacePath.trim()) {
    return
  }

  const queries = queryClient.getQueryCache().findAll({ queryKey: WORKSPACE_OVERVIEW_QUERY_KEY_PREFIX })
  for (const query of queries) {
    queryClient.setQueryData<WorkspaceOverviewResponse | undefined>(query.queryKey, (current) => {
      if (!current) {
        return current
      }

      const matchedWorkspacePath = nearestWorkspaceScopePath(current.workspaces, session.workspacePath)
      if (!matchedWorkspacePath) {
        return current
      }

      let changed = false
      const workspaces = current.workspaces.map((workspace) => {
        const result = patchWorkspace(workspace, session, matchedWorkspacePath)
        changed = changed || result.changed
        return result.workspace
      })

      if (!changed) {
        return current
      }

      return {
        ...current,
        // Keep client-side live updates grouped the same way as server-side overview refreshes.
        workspaces,
      }
    })
  }
}
