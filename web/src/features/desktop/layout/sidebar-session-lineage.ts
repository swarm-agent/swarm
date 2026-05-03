import type { DesktopSessionRecord } from '../types/realtime'

export type SidebarSessionNodeKind = 'root' | 'subagent' | 'background'

export interface SidebarSessionChildDescriptor {
  kind: SidebarSessionNodeKind
  label: string | null
}

export interface SidebarSessionBackgroundInfo {
  active: boolean
  badge: string
  targetLabel: string
}

function normalizeMetadataRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' ? value as Record<string, unknown> : null
}

function firstNonEmpty(...values: string[]): string {
  for (const value of values) {
    const trimmed = value.trim()
    if (trimmed) {
      return trimmed
    }
  }
  return ''
}

function normalizeLineageLabel(label: string): string {
  const trimmed = label.trim()
  if (!trimmed) {
    return ''
  }
  if (trimmed === 'child' || trimmed.toLowerCase() === 'background' || trimmed.toLowerCase().startsWith('bg:')) {
    return trimmed
  }
  if (trimmed.startsWith('@')) {
    return trimmed
  }
  if (trimmed.includes(' ')) {
    return ''
  }
  return `@${trimmed}`
}

function metadataString(metadata: Record<string, unknown> | null, key: string): string {
  return metadata && typeof metadata[key] === 'string' ? String(metadata[key]).trim() : ''
}

export function sessionParentSessionID(session: DesktopSessionRecord): string {
  const sessionID = session.id.trim()
  const metadata = normalizeMetadataRecord(session.metadata)
  const parentSessionID = metadataString(metadata, 'parent_session_id')
  return parentSessionID && parentSessionID !== sessionID ? parentSessionID : ''
}

function sessionLineageLabel(metadata: Record<string, unknown> | null): string {
  return normalizeLineageLabel(firstNonEmpty(
    metadataString(metadata, 'lineage_label'),
    metadataString(metadata, 'subagent'),
    metadataString(metadata, 'requested_subagent'),
    metadataString(metadata, 'background_agent'),
    metadataString(metadata, 'requested_background_agent'),
  ))
}

function sessionHasFlowIdentity(metadata: Record<string, unknown> | null): boolean {
  return metadataString(metadata, 'source').toLowerCase() === 'flow'
    || metadataString(metadata, 'lineage_kind').toLowerCase() === 'flow'
    || metadataString(metadata, 'flow_id') !== ''
}

function sessionHasBackgroundLineage(metadata: Record<string, unknown> | null): boolean {
  const background = metadata?.background === true
  const launchMode = metadataString(metadata, 'launch_mode').toLowerCase()
  const lineageKind = metadataString(metadata, 'lineage_kind').toLowerCase()
  const targetKind = metadataString(metadata, 'target_kind').toLowerCase()
  return background || launchMode === 'background' || lineageKind === 'background_agent' || sessionHasFlowIdentity(metadata) || targetKind === 'background'
}

function sessionBackgroundBadge(metadata: Record<string, unknown> | null): string {
  return sessionHasFlowIdentity(metadata) ? 'flow' : 'background'
}

export function sessionBackgroundInfo(session: DesktopSessionRecord, fallbackTargetLabel = ''): SidebarSessionBackgroundInfo | null {
  const metadata = normalizeMetadataRecord(session.metadata)
  if (!sessionHasBackgroundLineage(metadata)) {
    return null
  }
  return {
    active: session.lifecycle?.active === true || ['starting', 'running', 'blocked'].includes(session.live.status),
    badge: sessionBackgroundBadge(metadata),
    targetLabel: firstNonEmpty(
      metadataString(metadata, 'swarm_target_name'),
      metadataString(metadata, 'target_display_name'),
      fallbackTargetLabel,
      'host',
    ),
  }
}

export function sessionChildDescriptor(session: DesktopSessionRecord): SidebarSessionChildDescriptor {
  const metadata = normalizeMetadataRecord(session.metadata)
  const parentSessionID = sessionParentSessionID(session)
  if (!parentSessionID) {
    return { kind: 'root', label: null }
  }
  const requestedSubagent = metadataString(metadata, 'requested_subagent')
  const resolvedSubagent = metadataString(metadata, 'subagent')
  const lineageKind = metadataString(metadata, 'lineage_kind').toLowerCase()
  const lineageLabel = sessionLineageLabel(metadata)
  const subagent = resolvedSubagent || requestedSubagent
  if (sessionHasFlowIdentity(metadata)) {
    return { kind: 'background', label: 'flow' }
  }
  if (subagent || lineageKind === 'delegated_subagent') {
    return { kind: 'subagent', label: lineageLabel || '@subagent' }
  }
  if (sessionHasBackgroundLineage(metadata)) {
    return { kind: 'background', label: sessionBackgroundBadge(metadata) }
  }
  if (lineageLabel) {
    return { kind: lineageLabel.startsWith('@') ? 'subagent' : 'background', label: lineageLabel }
  }
  return { kind: 'background', label: 'child' }
}
