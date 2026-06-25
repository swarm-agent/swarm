import type { DesktopPermissionRecord } from '../../types/realtime'
import type { DesktopPermissionSummary, DesktopPermissionSummaryWire } from '../../state/desktop-v3-cache-types'

export interface DesktopPermissionWire {
  id?: unknown
  session_id?: unknown
  sessionId?: unknown
  run_id?: unknown
  runId?: unknown
  call_id?: unknown
  callId?: unknown
  tool_name?: unknown
  toolName?: unknown
  tool_arguments?: unknown
  toolArguments?: unknown
  approved_arguments?: unknown
  approvedArguments?: unknown
  saved_rule?: unknown
  savedRule?: unknown
  requirement?: unknown
  mode?: unknown
  status?: unknown
  decision?: unknown
  reason?: unknown
  created_at?: unknown
  createdAt?: unknown
  updated_at?: unknown
  updatedAt?: unknown
  resolved_at?: unknown
  resolvedAt?: unknown
  permission_requested_at?: unknown
  permissionRequestedAt?: unknown
}

export const safeString = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : ''

function rawString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function finiteNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function recordValue(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

function pickString(record: Record<string, unknown>, snake: string, camel: string): string {
  return safeString(record[snake]) || safeString(record[camel])
}

function pickRawString(record: Record<string, unknown>, snake: string, camel: string): string {
  return rawString(record[snake]) || rawString(record[camel])
}

function pickNumber(record: Record<string, unknown>, snake: string, camel: string): number {
  return finiteNumber(record[snake]) || finiteNumber(record[camel])
}

function pickNonNegativeInteger(record: Record<string, unknown>, snake: string, camel: string): number {
  const value = finiteNumber(record[snake]) || finiteNumber(record[camel])
  if (value <= 0) return 0
  return Math.floor(value)
}

function normalizeSavedRule(value: unknown): DesktopPermissionRecord['savedRule'] | undefined {
  const rule = recordValue(value)
  if (!rule) return undefined
  return {
    id: safeString(rule.id),
    kind: safeString(rule.kind),
    decision: safeString(rule.decision),
    tool: safeString(rule.tool) || undefined,
    pattern: safeString(rule.pattern) || undefined,
    createdAt: pickNumber(rule, 'created_at', 'createdAt') || undefined,
    updatedAt: pickNumber(rule, 'updated_at', 'updatedAt') || undefined,
  }
}

export function desktopPermissionIdentity(
  value: unknown,
  expectedSessionId: string,
): { id: string; sessionId: string } | null {
  const source = recordValue(value)
  const scopedSessionId = safeString(expectedSessionId)
  if (!source || !scopedSessionId) return null
  const id = safeString(source.id)
  if (!id) return null
  const explicitSessionId = pickString(source, 'session_id', 'sessionId')
  if (explicitSessionId && explicitSessionId !== scopedSessionId) return null
  return { id, sessionId: scopedSessionId }
}

export function normalizeDesktopPermission(
  value: unknown,
  expectedSessionId: string,
): DesktopPermissionRecord | null {
  const source = recordValue(value)
  const identity = desktopPermissionIdentity(value, expectedSessionId)
  if (!source || !identity) return null

  const status = pickString(source, 'status', 'status')
  if (status.toLowerCase() !== 'pending') return null

  const toolArguments = pickRawString(source, 'tool_arguments', 'toolArguments')
  const savedRule = normalizeSavedRule(source.saved_rule ?? source.savedRule)

  return {
    id: identity.id,
    sessionId: identity.sessionId,
    runId: pickString(source, 'run_id', 'runId'),
    callId: pickString(source, 'call_id', 'callId'),
    toolName: pickString(source, 'tool_name', 'toolName'),
    toolArguments: toolArguments || '{}',
    approvedArguments: pickString(source, 'approved_arguments', 'approvedArguments') || undefined,
    savedRule,
    status,
    decision: pickString(source, 'decision', 'decision'),
    reason: pickString(source, 'reason', 'reason'),
    requirement: pickString(source, 'requirement', 'requirement'),
    mode: pickString(source, 'mode', 'mode'),
    createdAt: pickNumber(source, 'created_at', 'createdAt'),
    updatedAt: pickNumber(source, 'updated_at', 'updatedAt'),
    resolvedAt: pickNumber(source, 'resolved_at', 'resolvedAt'),
    permissionRequestedAt: pickNumber(source, 'permission_requested_at', 'permissionRequestedAt'),
  }
}

export function normalizeDesktopPendingPermissions(
  values: unknown,
  expectedSessionId: string,
): DesktopPermissionRecord[] {
  if (!Array.isArray(values)) return []
  return values
    .map((value) => normalizeDesktopPermission(value, expectedSessionId))
    .filter((permission): permission is DesktopPermissionRecord => Boolean(permission))
    .sort((left, right) => (
      (left.permissionRequestedAt || left.createdAt || left.updatedAt || 0)
      - (right.permissionRequestedAt || right.createdAt || right.updatedAt || 0)
    ) || left.id.localeCompare(right.id))
}

export function normalizeDesktopPermissionSummary(
  value: unknown,
  expectedSessionId: string,
): DesktopPermissionSummary | null {
  const source = recordValue(value)
  const scopedSessionId = safeString(expectedSessionId)
  if (!source || !scopedSessionId) return null
  const explicitSessionId = pickString(source, 'session_id', 'sessionId')
  if (explicitSessionId && explicitSessionId !== scopedSessionId) return null
  const pendingApprovalCount = pickNonNegativeInteger(source, 'pending_approval_count', 'pendingApprovalCount')
  return {
    pendingApprovalCount,
    oldestPendingAt: pendingApprovalCount > 0 ? pickNonNegativeInteger(source, 'oldest_pending_at', 'oldestPendingAt') : 0,
    newestPendingAt: pendingApprovalCount > 0 ? pickNonNegativeInteger(source, 'newest_pending_at', 'newestPendingAt') : 0,
    updatedAt: pickNonNegativeInteger(source, 'updated_at', 'updatedAt'),
  }
}

export function normalizeDesktopPermissionSummaries(
  values: Record<string, DesktopPermissionSummaryWire> | undefined,
): Record<string, DesktopPermissionSummary> {
  const out: Record<string, DesktopPermissionSummary> = {}
  if (!values) return out
  for (const [sessionId, value] of Object.entries(values)) {
    const normalizedSessionId = safeString(sessionId)
    const summary = normalizeDesktopPermissionSummary(value, normalizedSessionId)
    if (normalizedSessionId && summary) out[normalizedSessionId] = summary
  }
  return out
}
