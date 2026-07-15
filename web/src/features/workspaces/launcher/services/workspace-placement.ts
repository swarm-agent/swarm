import type { SwarmTarget } from '../../../desktop/swarm/api/swarm-targets'
import type { WorkspaceOverviewTopologyRoute } from '../types/workspace-overview'

export interface WorkspacePlacementLink {
  route: WorkspaceOverviewTopologyRoute
  target: SwarmTarget | null
  targetType: string
}

function normalizeTargetValue(value: string | undefined): string {
  return value?.trim().toLowerCase() ?? ''
}

export function resolveWorkspaceRouteTarget(route: WorkspaceOverviewTopologyRoute, targets: SwarmTarget[]): SwarmTarget | null {
  const runtimeSwarmId = normalizeTargetValue(route.runtimeSwarmId)
  if (!runtimeSwarmId) {
    return null
  }
  return targets.find((target) => normalizeTargetValue(target.swarm_id) === runtimeSwarmId) ?? null
}

export function workspacePlacementLinks(routes: WorkspaceOverviewTopologyRoute[], targets: SwarmTarget[]): WorkspacePlacementLink[] {
  return routes
    .map((route) => {
      const target = resolveWorkspaceRouteTarget(route, targets)
      const targetType = workspaceRouteTargetType(route, target)
      return targetType ? { route, target, targetType } : null
    })
    .filter((entry): entry is WorkspacePlacementLink => Boolean(entry))
}

export function workspaceRouteTargetName(route: WorkspaceOverviewTopologyRoute, target: SwarmTarget | null): string {
  return target?.name?.trim() || route.runtimeSwarmName.trim() || route.runtimeWorkspacePath.trim() || route.runtimeSwarmId.trim() || 'target'
}

export function workspaceRouteDisplayPath(route: WorkspaceOverviewTopologyRoute): string {
  return route.runtimeWorkspacePath.trim()
}

function workspaceRouteKind(route: WorkspaceOverviewTopologyRoute, target: SwarmTarget | null): string {
  return normalizeTargetValue(target?.kind || route.runtimeKind)
}

export function workspaceRouteTargetType(route: WorkspaceOverviewTopologyRoute, target: SwarmTarget | null): string {
  const source = normalizeTargetValue(route.routeSource)
  if (source !== 'topology/workspace_binding') {
    return ''
  }
  const relationship = normalizeTargetValue(target?.relationship || route.runtimeRelationship)
  const role = normalizeTargetValue(target?.role)
  const kind = workspaceRouteKind(route, target)
  if (kind === 'local' || kind === 'container') {
    return 'Container'
  }
  if (relationship === 'self' || role === 'self' || kind === 'host' || kind === 'self') {
    return 'Host'
  }
  return 'Target'
}

export function workspaceRoutePlacementLabel(route: WorkspaceOverviewTopologyRoute, target: SwarmTarget | null): string {
  const type = workspaceRouteTargetType(route, target)
  return type ? `${workspaceRouteTargetName(route, target)} (${type})` : ''
}

export function workspaceRouteHoverTitle(route: WorkspaceOverviewTopologyRoute, target: SwarmTarget | null): string {
  const parts = [
    workspaceRoutePlacementLabel(route, target),
    route.workspaceBindingId.trim() ? `Binding ID: ${route.workspaceBindingId.trim()}` : '',
    route.runtimeSwarmId.trim() ? `Runtime swarm ID: ${route.runtimeSwarmId.trim()}` : '',
    workspaceRouteDisplayPath(route) ? `Runtime path: ${workspaceRouteDisplayPath(route)}` : '',
  ].filter(Boolean)
  return parts.join('\n')
}
