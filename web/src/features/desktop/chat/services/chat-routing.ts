import type { DesktopSessionRecord } from '../../types/realtime'
import type { WorkspaceOverviewTopologyRoute } from '../../../workspaces/launcher/types/workspace-overview'

export interface DesktopChatRoute {
  id: string
  label: string
  swarmId: string | null
  targetKind: string
  targetRelationship: string
  hostSwarmId: string
  hostSwarmName: string
  hostWorkspacePath: string
  hostWorkspaceName: string
  runtimeWorkspacePath: string
}

export function desktopChatRouteID(swarmId: string | null | undefined, runtimeWorkspacePath: string): string {
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
    targetKind: 'host',
    targetRelationship: 'self',
    hostSwarmId: '',
    hostSwarmName: hostSwarmName.trim() || 'host',
    hostWorkspacePath: normalizedWorkspacePath,
    hostWorkspaceName: workspaceName.trim(),
    runtimeWorkspacePath: normalizedWorkspacePath,
  }
}

export function buildDesktopChatRouteOptions(input: {
  hostSwarmName: string
  workspacePath: string
  workspaceName: string
  topologyRoutes: WorkspaceOverviewTopologyRoute[]
}): DesktopChatRoute[] {
  const hostRoute = buildHostDesktopChatRoute(input.hostSwarmName, input.workspacePath, input.workspaceName)
  const options: DesktopChatRoute[] = [hostRoute]
  const seen = new Set<string>([desktopChatRouteID(hostRoute.swarmId, hostRoute.runtimeWorkspacePath)])
  for (const topologyRoute of input.topologyRoutes) {
    const swarmId = topologyRoute.runtimeSwarmId.trim()
    const runtimeWorkspacePath = topologyRoute.runtimeWorkspacePath.trim()
    const id = topologyRoute.routeId.trim() || desktopChatRouteID(swarmId, runtimeWorkspacePath)
    if (!swarmId || !runtimeWorkspacePath || !id) {
      continue
    }
    if (seen.has(id)) {
      continue
    }
    seen.add(id)
    const targetRelationship = topologyRoute.runtimeRelationship.trim().toLowerCase()
    const runtimeKind = topologyRoute.runtimeKind.trim()
    const targetKind = targetRelationship === 'managed'
      ? 'host'
      : runtimeKind
    const hostSwarmName = topologyRoute.hostSwarmName.trim()
    const label = topologyRoute.runtimeSwarmName.trim() || swarmId
    options.push({
      id,
      label,
      swarmId,
      targetKind,
      targetRelationship,
      hostSwarmId: topologyRoute.hostSwarmId.trim(),
      hostSwarmName,
      hostWorkspacePath: topologyRoute.hostWorkspacePath.trim() || hostRoute.hostWorkspacePath,
      hostWorkspaceName: topologyRoute.hostWorkspaceName.trim() || hostRoute.hostWorkspaceName,
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

export function resolveDesktopChatRouteById(
  routeOptions: DesktopChatRoute[],
  routeId: string | null | undefined,
  fallback?: DesktopChatRoute | null,
): DesktopChatRoute | null {
  const normalizedRouteId = routeId?.trim() ?? ''
  if (normalizedRouteId) {
    const matched = routeOptions.find((entry) => entry.id === normalizedRouteId)
    if (matched) {
      return matched
    }
  }
  return fallback ?? routeOptions[0] ?? null
}

function sessionMetadataString(metadata: Record<string, unknown> | null | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

export function desktopChatRouteFromSessionMetadata(session: DesktopSessionRecord | null | undefined): DesktopChatRoute | null {
  const metadata = session?.metadata
  const metadataSwarmId = sessionMetadataString(metadata, 'swarm_routed_child_swarm_id')
  const runtimeWorkspacePath = session?.runtimeWorkspacePath?.trim() || sessionMetadataString(metadata, 'swarm_routed_runtime_workspace_path')
  if (!metadataSwarmId || !runtimeWorkspacePath) {
    return null
  }
  const id = sessionMetadataString(metadata, 'swarm_route_id') || desktopChatRouteID(metadataSwarmId, runtimeWorkspacePath)
  return {
    id,
    label: sessionMetadataString(metadata, 'swarm_route_label') || metadataSwarmId,
    swarmId: metadataSwarmId,
    targetKind: sessionMetadataString(metadata, 'swarm_route_target_kind') || sessionMetadataString(metadata, 'swarm_target_kind'),
    targetRelationship: sessionMetadataString(metadata, 'swarm_route_target_relationship') || sessionMetadataString(metadata, 'swarm_target_relationship'),
    hostSwarmId: sessionMetadataString(metadata, 'swarm_routed_host_swarm_id'),
    hostSwarmName: sessionMetadataString(metadata, 'swarm_routed_host_swarm_name'),
    hostWorkspacePath: sessionMetadataString(metadata, 'swarm_routed_host_workspace_path') || session?.workspacePath?.trim() || '',
    hostWorkspaceName: session?.workspaceName?.trim() || '',
    runtimeWorkspacePath,
  }
}

export function resolveDesktopChatRouteFromSession(
  session: DesktopSessionRecord | null | undefined,
  routeOptions: DesktopChatRoute[],
  fallback?: DesktopChatRoute | null,
): DesktopChatRoute | null {
  const metadataRoute = desktopChatRouteFromSessionMetadata(session)
  const matchedRoute = metadataRoute ? routeOptions.find((entry) => entry.id === metadataRoute.id) ?? null : null
  return matchedRoute ?? metadataRoute ?? fallback ?? routeOptions[0] ?? null
}

export function isManagedHostDesktopChatRoute(route: DesktopChatRoute | null | undefined): boolean {
  const swarmId = route?.swarmId?.trim() ?? ''
  if (!swarmId) {
    return false
  }
  const relationship = route?.targetRelationship?.trim().toLowerCase() ?? ''
  const kind = route?.targetKind?.trim().toLowerCase() ?? ''
  return relationship === 'managed' || kind === 'host'
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
