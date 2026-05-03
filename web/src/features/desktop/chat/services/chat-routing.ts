import type { DesktopSessionRecord } from '../../types/realtime'
import type { WorkspaceReplicationLink } from '../../../workspaces/launcher/types/workspace'

const DESKTOP_CHAT_ROUTE_STORAGE_KEY = 'swarm.web.desktop.chat.routes.v1'

export interface DesktopChatRoute {
  id: string
  label: string
  swarmId: string | null
  hostWorkspacePath: string
  hostWorkspaceName: string
  runtimeWorkspacePath: string
}

interface StoredDesktopChatRoute {
  id?: string
  label?: string
  swarm_id?: string | null
  host_workspace_path?: string
  host_workspace_name?: string
  runtime_workspace_path?: string
}

interface StoredDesktopChatRoutes {
  sessions?: Record<string, StoredDesktopChatRoute>
  workspaces?: Record<string, StoredDesktopChatRoute>
}

function normalizeStoredRoute(route: StoredDesktopChatRoute | null | undefined): DesktopChatRoute | null {
  if (!route || typeof route !== 'object') {
    return null
  }
  const hostWorkspacePath = String(route.host_workspace_path ?? '').trim()
  const runtimeWorkspacePath = String(route.runtime_workspace_path ?? '').trim()
  if (!hostWorkspacePath || !runtimeWorkspacePath) {
    return null
  }
  const swarmId = String(route.swarm_id ?? '').trim() || null
  const label = String(route.label ?? '').trim()
  const id = String(route.id ?? '').trim()
  return {
    id: id || desktopChatRouteID(swarmId, runtimeWorkspacePath),
    label: label || (swarmId ? swarmId : 'host'),
    swarmId,
    hostWorkspacePath,
    hostWorkspaceName: String(route.host_workspace_name ?? '').trim(),
    runtimeWorkspacePath,
  }
}

function routeToStoredRecord(route: DesktopChatRoute | null | undefined): StoredDesktopChatRoute | null {
  if (!route) {
    return null
  }
  const hostWorkspacePath = route.hostWorkspacePath.trim()
  const runtimeWorkspacePath = route.runtimeWorkspacePath.trim()
  if (!hostWorkspacePath || !runtimeWorkspacePath) {
    return null
  }
  return {
    id: route.id.trim() || desktopChatRouteID(route.swarmId, runtimeWorkspacePath),
    label: route.label.trim(),
    swarm_id: route.swarmId?.trim() || null,
    host_workspace_path: hostWorkspacePath,
    host_workspace_name: route.hostWorkspaceName.trim(),
    runtime_workspace_path: runtimeWorkspacePath,
  }
}

function loadStoredRoutes(): StoredDesktopChatRoutes {
  if (typeof window === 'undefined') {
    return {}
  }
  const raw = window.sessionStorage.getItem(DESKTOP_CHAT_ROUTE_STORAGE_KEY)
  if (!raw) {
    return {}
  }
  try {
    const parsed = JSON.parse(raw) as StoredDesktopChatRoutes
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    return {}
  }
}

function saveStoredRoutes(routes: StoredDesktopChatRoutes): void {
  if (typeof window === 'undefined') {
    return
  }
  const nextSessions = routes.sessions && Object.keys(routes.sessions).length > 0 ? routes.sessions : undefined
  const nextWorkspaces = routes.workspaces && Object.keys(routes.workspaces).length > 0 ? routes.workspaces : undefined
  if (!nextSessions && !nextWorkspaces) {
    window.sessionStorage.removeItem(DESKTOP_CHAT_ROUTE_STORAGE_KEY)
    return
  }
  window.sessionStorage.setItem(DESKTOP_CHAT_ROUTE_STORAGE_KEY, JSON.stringify({
    ...(nextSessions ? { sessions: nextSessions } : {}),
    ...(nextWorkspaces ? { workspaces: nextWorkspaces } : {}),
  }))
}

function desktopChatRouteID(swarmId: string | null | undefined, runtimeWorkspacePath: string): string {
  const normalizedRuntimeWorkspacePath = runtimeWorkspacePath.trim()
  const normalizedSwarmId = swarmId?.trim() ?? ''
  if (!normalizedSwarmId) {
    return 'host'
  }
  return `swarm:${normalizedSwarmId}:${normalizedRuntimeWorkspacePath}`
}

export function buildHostDesktopChatRoute(hostSwarmName: string, workspacePath: string, workspaceName: string): DesktopChatRoute {
  const normalizedWorkspacePath = workspacePath.trim()
  return {
    id: 'host',
    label: hostSwarmName.trim() || 'host',
    swarmId: null,
    hostWorkspacePath: normalizedWorkspacePath,
    hostWorkspaceName: workspaceName.trim(),
    runtimeWorkspacePath: normalizedWorkspacePath,
  }
}

export function buildDesktopChatRouteOptions(input: {
  hostSwarmName: string
  workspacePath: string
  workspaceName: string
  replicationLinks: WorkspaceReplicationLink[]
}): DesktopChatRoute[] {
  const hostRoute = buildHostDesktopChatRoute(input.hostSwarmName, input.workspacePath, input.workspaceName)
  const options: DesktopChatRoute[] = [hostRoute]
  const seen = new Set<string>([desktopChatRouteID(hostRoute.swarmId, hostRoute.runtimeWorkspacePath)])
  for (const link of input.replicationLinks) {
    const swarmId = link.targetSwarmId.trim()
    const runtimeWorkspacePath = link.targetWorkspacePath.trim()
    if (!swarmId || !runtimeWorkspacePath) {
      continue
    }
    const id = desktopChatRouteID(swarmId, runtimeWorkspacePath)
    if (seen.has(id)) {
      continue
    }
    seen.add(id)
    options.push({
      id,
      label: link.targetSwarmName.trim() || swarmId,
      swarmId,
      hostWorkspacePath: hostRoute.hostWorkspacePath,
      hostWorkspaceName: hostRoute.hostWorkspaceName,
      runtimeWorkspacePath,
    })
  }
  return options
}

export function desktopChatRoutesEqual(left: DesktopChatRoute | null | undefined, right: DesktopChatRoute | null | undefined): boolean {
  if (!left && !right) {
    return true
  }
  if (!left || !right) {
    return false
  }
  return left.swarmId === right.swarmId
    && left.hostWorkspacePath === right.hostWorkspacePath
    && left.runtimeWorkspacePath === right.runtimeWorkspacePath
}

export function loadDesktopChatRouteForWorkspace(workspacePath: string): DesktopChatRoute | null {
  const normalizedWorkspacePath = workspacePath.trim()
  if (!normalizedWorkspacePath) {
    return null
  }
  const routes = loadStoredRoutes()
  return normalizeStoredRoute(routes.workspaces?.[normalizedWorkspacePath])
}

export function saveDesktopChatRouteForWorkspace(workspacePath: string, route: DesktopChatRoute | null | undefined): void {
  const normalizedWorkspacePath = workspacePath.trim()
  if (!normalizedWorkspacePath) {
    return
  }
  const routes = loadStoredRoutes()
  const workspaces = { ...(routes.workspaces ?? {}) }
  const nextRoute = routeToStoredRecord(route)
  if (nextRoute) {
    workspaces[normalizedWorkspacePath] = nextRoute
  } else {
    delete workspaces[normalizedWorkspacePath]
  }
  saveStoredRoutes({ ...routes, workspaces })
}

export function loadDesktopChatRouteForSession(sessionId: string): DesktopChatRoute | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  const routes = loadStoredRoutes()
  return normalizeStoredRoute(routes.sessions?.[normalizedSessionId])
}

export function saveDesktopChatRouteForSession(sessionId: string, route: DesktopChatRoute | null | undefined): void {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return
  }
  const routes = loadStoredRoutes()
  const sessions = { ...(routes.sessions ?? {}) }
  const nextRoute = routeToStoredRecord(route)
  if (nextRoute) {
    sessions[normalizedSessionId] = nextRoute
  } else {
    delete sessions[normalizedSessionId]
  }
  saveStoredRoutes({ ...routes, sessions })
}

export function withDesktopChatRoute(path: string, route: DesktopChatRoute | null | undefined): string {
  const normalizedPath = path.trim()
  if (!normalizedPath) {
    return normalizedPath
  }
  const [pathname, rawQuery = ''] = normalizedPath.split('?', 2)
  const search = new URLSearchParams(rawQuery)
  const swarmId = route?.swarmId?.trim() ?? ''
  if (swarmId) {
    search.set('swarm_id', swarmId)
  } else {
    search.delete('swarm_id')
  }
  const encoded = search.toString()
  return encoded ? `${pathname}?${encoded}` : pathname
}

export function applyDesktopChatRoute(url: URL, route: DesktopChatRoute | null | undefined): URL {
  const swarmId = route?.swarmId?.trim() ?? ''
  if (swarmId) {
    url.searchParams.set('swarm_id', swarmId)
  } else {
    url.searchParams.delete('swarm_id')
  }
  return url
}

function isFlowSessionMetadata(metadata: Record<string, unknown> | null | undefined): boolean {
  if (!metadata || typeof metadata !== 'object') {
    return false
  }
  const source = typeof metadata.source === 'string' ? metadata.source.trim().toLowerCase() : ''
  const lineageKind = typeof metadata.lineage_kind === 'string' ? metadata.lineage_kind.trim().toLowerCase() : ''
  const flowID = typeof metadata.flow_id === 'string' ? metadata.flow_id.trim() : ''
  return source === 'flow' || lineageKind === 'flow' || flowID !== ''
}

export function applyDesktopChatRouteToSession(session: DesktopSessionRecord, route: DesktopChatRoute | null | undefined): DesktopSessionRecord {
  if (!route?.swarmId) {
    return session
  }

  const runtimeWorkspacePath = session.runtimeWorkspacePath || route.runtimeWorkspacePath || session.workspacePath
  const routeIsHydratedFromRemote = Boolean(
    runtimeWorkspacePath
    && session.workspacePath
    && route.runtimeWorkspacePath.trim() === runtimeWorkspacePath.trim()
    && session.workspacePath.trim() === runtimeWorkspacePath.trim(),
  )
  if (routeIsHydratedFromRemote) {
    return {
      ...session,
      runtimeWorkspacePath,
    }
  }

  return {
    ...session,
    workspacePath: isFlowSessionMetadata(session.metadata)
      ? (session.workspacePath || route.hostWorkspacePath)
      : (route.hostWorkspacePath || session.workspacePath),
    workspaceName: isFlowSessionMetadata(session.metadata)
      ? (session.workspaceName || route.hostWorkspaceName)
      : (route.hostWorkspaceName || session.workspaceName),
    runtimeWorkspacePath,
  }
}
