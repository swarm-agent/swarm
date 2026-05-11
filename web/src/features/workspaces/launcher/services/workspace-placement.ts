import type { SwarmTarget } from '../../../desktop/swarm/api/swarm-targets'
import type { WorkspaceReplicationLink } from '../types/workspace'

export interface WorkspacePlacementLink {
  link: WorkspaceReplicationLink
  target: SwarmTarget | null
  targetType: string
}

function normalizeTargetValue(value: string | undefined): string {
  return value?.trim().toLowerCase() ?? ''
}

export function resolveWorkspaceLinkTarget(link: WorkspaceReplicationLink, targets: SwarmTarget[]): SwarmTarget | null {
  const targetSwarmId = normalizeTargetValue(link.targetSwarmId)
  if (!targetSwarmId) {
    return null
  }
  return targets.find((target) => normalizeTargetValue(target.swarm_id) === targetSwarmId) ?? null
}

export function workspacePlacementLinks(links: WorkspaceReplicationLink[], targets: SwarmTarget[]): WorkspacePlacementLink[] {
  return links
    .map((link) => {
      const target = resolveWorkspaceLinkTarget(link, targets)
      const targetType = workspaceLinkTargetType(link, target)
      return targetType ? { link, target, targetType } : null
    })
    .filter((entry): entry is WorkspacePlacementLink => Boolean(entry))
}

export function workspaceLinkTargetName(link: WorkspaceReplicationLink, target: SwarmTarget | null): string {
  return target?.name?.trim() || link.targetSwarmName.trim() || link.targetWorkspacePath.trim() || link.targetSwarmId.trim() || 'target'
}

export function workspaceLinkDisplayPath(link: WorkspaceReplicationLink): string {
  return link.targetWorkspacePath.trim()
}

function workspaceLinkKind(link: WorkspaceReplicationLink, target: SwarmTarget | null): string {
  return normalizeTargetValue(target?.kind || link.targetKind)
}

export function workspaceLinkTargetType(link: WorkspaceReplicationLink, target: SwarmTarget | null): string {
  const relationship = normalizeTargetValue(target?.relationship)
  const role = normalizeTargetValue(target?.role)
  const kind = workspaceLinkKind(link, target)
  const linkKind = normalizeTargetValue(link.targetKind)
  if (relationship === 'managed' || role === 'managed' || kind === 'host' || linkKind === 'managed_host' || linkKind === 'managed') {
    return 'Managed Host'
  }
  if (target?.deployment_id?.trim() || kind === 'local' || linkKind === 'container') {
    return 'Container'
  }
  return ''
}

export function workspaceLinkPlacementLabel(link: WorkspaceReplicationLink, target: SwarmTarget | null): string {
  const type = workspaceLinkTargetType(link, target)
  return type ? `${workspaceLinkTargetName(link, target)} (${type})` : ''
}

export function workspaceLinkModeLabel(link: WorkspaceReplicationLink): string {
  const mode = link.replicationMode.trim() || link.sync.mode.trim() || 'linked'
  const sync = link.sync.enabled ? `sync ${link.sync.mode.trim() || 'enabled'}` : 'manual sync'
  return `${link.writable ? 'writable' : 'read-only'} · ${mode} · ${sync}`
}

export function workspaceLinkHoverTitle(link: WorkspaceReplicationLink, target: SwarmTarget | null): string {
  const parts = [
    workspaceLinkPlacementLabel(link, target),
    link.targetSwarmId.trim() ? `Swarm ID: ${link.targetSwarmId.trim()}` : '',
    workspaceLinkDisplayPath(link) ? `Target path: ${workspaceLinkDisplayPath(link)}` : '',
    workspaceLinkModeLabel(link),
  ].filter(Boolean)
  return parts.join('\n')
}
