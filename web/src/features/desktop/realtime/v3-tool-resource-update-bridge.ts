import type { QueryClient, QueryKey } from '@tanstack/react-query'

import { queryClient as defaultQueryClient } from '../../../app/query-client'
import { agentSettingsStateQueryOptions, agentStateQueryOptions, uiSettingsQueryKey, workspaceOverviewQueryKey } from '../../queries/query-options'
import type { CacheEvent } from '../state/desktop-v3-cache-types'

type ToolResourceKind = 'agent-state' | 'ui-settings' | 'workspace-overview'

export interface DesktopV3ToolResourceUpdateResult {
  applied: boolean
  toolName: string
  resources: ToolResourceKind[]
}

interface ToolResourceUpdatePlan {
  toolName: string
  resources: Set<ToolResourceKind>
}

export function handleDesktopV3ToolResourceUpdate(
  event: CacheEvent,
  client: QueryClient = defaultQueryClient,
): DesktopV3ToolResourceUpdateResult {
  const plan = deriveDesktopV3ToolResourceUpdate(event)
  if (!plan || plan.resources.size === 0) {
    return { applied: false, toolName: '', resources: [] }
  }

  if (plan.resources.has('agent-state')) {
    refreshActiveQueries(client, agentStateQueryOptions().queryKey)
    refreshActiveQueries(client, agentSettingsStateQueryOptions().queryKey)
  }
  if (plan.resources.has('ui-settings')) {
    refreshActiveQueries(client, uiSettingsQueryKey())
  }
  if (plan.resources.has('workspace-overview')) {
    const overviewRootKey = [workspaceOverviewQueryKey()[0]] as const
    refreshActiveQueries(client, overviewRootKey)
  }

  return { applied: true, toolName: plan.toolName, resources: Array.from(plan.resources) }
}

export function deriveDesktopV3ToolResourceUpdate(event: CacheEvent): ToolResourceUpdatePlan | null {
  if (event.eventType !== 'session.tool.completed') return null

  const payload = recordValue(event.payload)
  if (!payload) return null

  const toolName = normalizeManagedToolName(firstString(payload.tool_name, payload.tool, payload.name))
  if (toolName !== 'manage-agent' && toolName !== 'manage-theme') return null

  const appliedRecords = collectToolOutputRecords(payload)
    .filter((record) => isAppliedToolResult(record))
  if (appliedRecords.length === 0) return null

  const resources = new Set<ToolResourceKind>()
  if (toolName === 'manage-agent') {
    resources.add('agent-state')
  } else if (toolName === 'manage-theme') {
    resources.add('ui-settings')
    if (appliedRecords.some((record) => includesWorkspaceThemeChange(record))) {
      resources.add('workspace-overview')
    }
  }

  return { toolName, resources }
}

function refreshActiveQueries(client: QueryClient, queryKey: QueryKey): void {
  void client.invalidateQueries({ queryKey })
  void client.refetchQueries({ queryKey, type: 'active' })
}

function collectToolOutputRecords(payload: Record<string, unknown>): Record<string, unknown>[] {
  const records: Record<string, unknown>[] = []
  const seen = new Set<unknown>()

  const visit = (value: unknown): void => {
    if (value == null || seen.has(value)) return
    seen.add(value)

    if (typeof value === 'string') {
      const parsed = parseJsonValue(value)
      if (parsed !== undefined) visit(parsed)
      return
    }

    if (Array.isArray(value)) {
      for (const item of value) visit(item)
      return
    }

    const record = recordValue(value)
    if (!record) return

    if (isToolResultRecord(record)) {
      records.push(record)
    }

    visit(record.output)
    visit(record.completed_output)
    visit(record.raw_output)
  }

  visit(payload.output)
  visit(payload.completed_output)
  visit(payload.raw_output)
  return records
}

function isToolResultRecord(record: Record<string, unknown>): boolean {
  if ('applied' in record || 'change' in record || 'approved_arguments' in record) return true
  const pathID = firstString(record.path_id).toLowerCase()
  return pathID.includes('manage-agent') || pathID.includes('manage-theme')
}

function isAppliedToolResult(record: Record<string, unknown>): boolean {
  const applied = record.applied
  if (applied === true) return true
  if (typeof applied === 'string' && applied.trim().toLowerCase() === 'true') return true
  return false
}

function includesWorkspaceThemeChange(record: Record<string, unknown>): boolean {
  if (firstString(record.apply_to).trim().toLowerCase() === 'workspace') return true
  if (recordValue(record.workspace)) return true
  if (firstString(record.workspace_path)) return true
  return hasWorkspaceThemeChange(record.change)
}

function hasWorkspaceThemeChange(value: unknown): boolean {
  const record = recordValue(value)
  if (!record) return false

  const target = firstString(record.target).trim().toLowerCase()
  if (target === 'workspace_theme') return true
  if (firstString(record.workspace_path)) return true
  if (Array.isArray(record.cleared_workspaces) && record.cleared_workspaces.length > 0) return true

  const changes = Array.isArray(record.changes) ? record.changes : []
  return changes.some((change) => hasWorkspaceThemeChange(change))
}

function normalizeManagedToolName(value: string): string {
  return value.trim().toLowerCase().replace(/_/g, '-')
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function parseJsonValue(value: string): unknown {
  const trimmed = value.trim()
  if (!trimmed || (!trimmed.startsWith('{') && !trimmed.startsWith('['))) return undefined
  try {
    return JSON.parse(trimmed) as unknown
  } catch {
    return undefined
  }
}
