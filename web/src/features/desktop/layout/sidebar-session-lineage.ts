import type { DesktopRunIntentRecord, DesktopSessionRecord } from '../types/realtime'

export type SidebarSessionNodeKind = 'root' | 'subagent' | 'background'

export interface SidebarSessionChildDescriptor {
  kind: SidebarSessionNodeKind
  label: string | null
  assignmentLabel: string | null
  taskCallId: string | null
}

export interface SidebarSessionBackgroundInfo {
  active: boolean
}

export interface SidebarTaskCallNode {
  kind: SidebarSessionNodeKind
  taskCallId: string | null
}

export function groupSidebarTaskCallSiblings<T extends SidebarTaskCallNode>(nodes: T[]): T[] {
  const output: T[] = []
  const emittedTaskCalls = new Set<string>()
  for (const node of nodes) {
    const taskCallId = node.kind === 'subagent' ? node.taskCallId?.trim() ?? '' : ''
    if (!taskCallId) {
      output.push(node)
      continue
    }
    if (emittedTaskCalls.has(taskCallId)) {
      continue
    }
    emittedTaskCalls.add(taskCallId)
    output.push(...nodes.filter((candidate) => candidate.kind === 'subagent' && candidate.taskCallId?.trim() === taskCallId))
  }
  return output
}

export function sidebarTaskCallPresentationGroups<T extends SidebarTaskCallNode>(nodes: T[]): T[][] {
  const groups: T[][] = []
  for (const node of nodes) {
    const taskCallId = node.kind === 'subagent' ? node.taskCallId?.trim() ?? '' : ''
    const previous = groups.at(-1)
    if (taskCallId && previous?.[0]?.taskCallId?.trim() === taskCallId) {
      previous.push(node)
    } else {
      groups.push([node])
    }
  }
  return groups
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
  const durableLineageLabel = metadataString(metadata, 'lineage_label')
  if (durableLineageLabel && durableLineageLabel.toLowerCase() !== '@subagent') {
    return normalizeLineageLabel(durableLineageLabel)
  }
  return normalizeLineageLabel(firstNonEmpty(
    metadataString(metadata, 'resolved_agent_name'),
    metadataString(metadata, 'agent_name'),
    metadataString(metadata, 'subagent'),
    metadataString(metadata, 'requested_subagent'),
    durableLineageLabel,
    metadataString(metadata, 'background_agent'),
    metadataString(metadata, 'requested_background_agent'),
  ))
}

function sessionHasBackgroundLineage(metadata: Record<string, unknown> | null): boolean {
  const background = metadata?.background === true
  const launchMode = metadataString(metadata, 'launch_mode').toLowerCase()
  const lineageKind = metadataString(metadata, 'lineage_kind').toLowerCase()
  const targetKind = metadataString(metadata, 'target_kind').toLowerCase()
  return background || launchMode === 'background' || lineageKind === 'background_agent' || targetKind === 'background'
}

function sessionActiveRunIntent(runIntent: DesktopRunIntentRecord | null | undefined): DesktopRunIntentRecord | null {
  const status = runIntent?.status.trim().toLowerCase() ?? ''
  return status === 'pending_executor' || status === 'running' ? runIntent ?? null : null
}

export function sessionBackgroundInfo(session: DesktopSessionRecord): SidebarSessionBackgroundInfo | null {
  const metadata = normalizeMetadataRecord(session.metadata)
  if (!sessionHasBackgroundLineage(metadata)) {
    return null
  }
  return {
    active: Boolean(sessionActiveRunIntent(session.runIntent)),
  }
}

export function sessionChildDescriptor(session: DesktopSessionRecord): SidebarSessionChildDescriptor {
  const metadata = normalizeMetadataRecord(session.metadata)
  const parentSessionID = sessionParentSessionID(session)
  const assignmentLabel = metadataString(metadata, 'assignment_label')
  const lineageKind = metadataString(metadata, 'lineage_kind').toLowerCase()
  // A managed deployment is a canonical conversation in its own right. Keep
  // parent_session_id as durable provenance, but do not present the deployed
  // session as an agent child of the conversation that launched it.
  if (!parentSessionID || lineageKind === 'session_deploy') {
    return { kind: 'root', label: null, assignmentLabel: assignmentLabel || null, taskCallId: null }
  }
  const requestedSubagent = metadataString(metadata, 'requested_subagent')
  const resolvedSubagent = metadataString(metadata, 'subagent')
  const taskCallId = metadataString(metadata, 'parent_task_call_id') || null
  const lineageLabel = sessionLineageLabel(metadata)
  const subagent = resolvedSubagent || requestedSubagent
  if (subagent || lineageKind === 'delegated_subagent') {
    return { kind: 'subagent', label: lineageLabel || '@subagent', assignmentLabel: assignmentLabel || null, taskCallId }
  }
  if (sessionHasBackgroundLineage(metadata)) {
    return { kind: 'background', label: 'background', assignmentLabel: assignmentLabel || null, taskCallId }
  }
  if (lineageLabel) {
    return { kind: lineageLabel.startsWith('@') ? 'subagent' : 'background', label: lineageLabel, assignmentLabel: assignmentLabel || null, taskCallId }
  }
  return { kind: 'background', label: 'child', assignmentLabel: assignmentLabel || null, taskCallId }
}
