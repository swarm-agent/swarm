import type {
  MessageSnapshot,
  SyncScopeCache,
  SessionSnapshot,
  V3SessionProjection,
  V3SessionRunIntent,
  V3SessionTombstone,
} from './desktop-v3-cache-types'
import { createDesktopV3CacheOwner, type DesktopV3CacheOwner } from './desktop-v3-cache-owner'

export const DESKTOP_V3_CACHE_SCHEMA_VERSION = 1

export interface PersistedDesktopV3SidebarSessionV1 {
  session: SessionSnapshot
  projection?: V3SessionProjection
  tombstone?: V3SessionTombstone
  runIntents?: V3SessionRunIntent[]
}

export interface PersistedDesktopV3OwnerV1 {
  schemaVersion: typeof DESKTOP_V3_CACHE_SCHEMA_VERSION
  ownerKey: string
  owner: DesktopV3CacheOwner
  persistedAt: number
  selectedSessionId?: string
  syncScopesById: Record<string, SyncScopeCache>
  sessionOrderByScope: Record<string, string[]>
  sidebarSessionsById: Record<string, PersistedDesktopV3SidebarSessionV1>
}

export interface PersistedDesktopV3MessageTailV1 {
  schemaVersion: typeof DESKTOP_V3_CACHE_SCHEMA_VERSION
  key: string
  ownerKey: string
  sessionId: string
  persistedAt: number
  messages: MessageSnapshot[]
  sourceMessageCount?: number
  sourceLastMessageAt?: number
  sourceProjectionHighWatermarkSeq?: number
  hydratedAt?: number
}

export type DesktopV3PersistedValidationResult<T> =
  | { ok: true; value: T }
  | { ok: false; deleteRecord: true; reason: string }

export function createPersistedDesktopV3MessageTailKey(ownerKey: string, sessionId: string): string {
  const normalizedOwnerKey = requiredString(ownerKey, 'ownerKey')
  const normalizedSessionId = requiredString(sessionId, 'sessionId')
  return `${normalizedOwnerKey}:message-tail:${encodeURIComponent(normalizedSessionId)}`
}

export function validatePersistedDesktopV3OwnerV1(
  raw: unknown,
  expectedOwnerKey?: string,
): DesktopV3PersistedValidationResult<PersistedDesktopV3OwnerV1> {
  try {
    const record = recordValue(raw, 'owner record')
    validateSchemaVersion(record.schemaVersion)
    const ownerKey = requiredString(record.ownerKey, 'ownerKey')
    if (expectedOwnerKey !== undefined && ownerKey !== expectedOwnerKey) {
      throw new Error('ownerKey mismatch')
    }

    const ownerRecord = recordValue(record.owner, 'owner')
    const owner = createDesktopV3CacheOwner({
      origin: requiredString(ownerRecord.origin, 'owner.origin'),
      accountScopeId: requiredString(ownerRecord.accountScopeId, 'owner.accountScopeId'),
      userId: requiredString(ownerRecord.userId, 'owner.userId'),
      surface: requiredString(ownerRecord.surface, 'owner.surface'),
    })
    if (owner.key !== ownerKey) {
      throw new Error('owner key does not match owner fields')
    }

    const selectedSessionId = optionalString(record.selectedSessionId, 'selectedSessionId')
    return {
      ok: true,
      value: {
        schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
        ownerKey,
        owner,
        persistedAt: finiteNumber(record.persistedAt, 'persistedAt'),
        selectedSessionId,
        syncScopesById: validateSyncScopesById(record.syncScopesById),
        sessionOrderByScope: validateSessionOrderByScope(record.sessionOrderByScope),
        sidebarSessionsById: validateSidebarSessionsById(record.sidebarSessionsById),
      },
    }
  } catch (error) {
    return coldMiss(error)
  }
}

export function parsePersistedDesktopV3OwnerV1(
  raw: unknown,
  expectedOwnerKey?: string,
): PersistedDesktopV3OwnerV1 | undefined {
  const result = validatePersistedDesktopV3OwnerV1(raw, expectedOwnerKey)
  return result.ok ? result.value : undefined
}

export function validatePersistedDesktopV3MessageTailV1(
  raw: unknown,
  expectedOwnerKey?: string,
  expectedSessionId?: string,
): DesktopV3PersistedValidationResult<PersistedDesktopV3MessageTailV1> {
  try {
    const record = recordValue(raw, 'message tail record')
    validateSchemaVersion(record.schemaVersion)
    const ownerKey = requiredString(record.ownerKey, 'ownerKey')
    const sessionId = requiredString(record.sessionId, 'sessionId')
    if (expectedOwnerKey !== undefined && ownerKey !== expectedOwnerKey) {
      throw new Error('ownerKey mismatch')
    }
    if (expectedSessionId !== undefined && sessionId !== expectedSessionId) {
      throw new Error('sessionId mismatch')
    }

    const key = requiredString(record.key, 'key')
    const expectedKey = createPersistedDesktopV3MessageTailKey(ownerKey, sessionId)
    if (key !== expectedKey) {
      throw new Error('message tail key mismatch')
    }

    return {
      ok: true,
      value: {
        schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
        key,
        ownerKey,
        sessionId,
        persistedAt: finiteNumber(record.persistedAt, 'persistedAt'),
        messages: validateMessages(record.messages, sessionId),
        sourceMessageCount: optionalFiniteNumber(record.sourceMessageCount, 'sourceMessageCount'),
        sourceLastMessageAt: optionalFiniteNumber(record.sourceLastMessageAt, 'sourceLastMessageAt'),
        sourceProjectionHighWatermarkSeq: optionalFiniteNumber(record.sourceProjectionHighWatermarkSeq, 'sourceProjectionHighWatermarkSeq'),
        hydratedAt: optionalFiniteNumber(record.hydratedAt, 'hydratedAt'),
      },
    }
  } catch (error) {
    return coldMiss(error)
  }
}

export function parsePersistedDesktopV3MessageTailV1(
  raw: unknown,
  expectedOwnerKey?: string,
  expectedSessionId?: string,
): PersistedDesktopV3MessageTailV1 | undefined {
  const result = validatePersistedDesktopV3MessageTailV1(raw, expectedOwnerKey, expectedSessionId)
  return result.ok ? result.value : undefined
}

export function buildPersistedDesktopV3MessageTailV1(input: {
  ownerKey: string
  sessionId: string
  persistedAt: number
  messages: MessageSnapshot[]
  sourceMessageCount?: number
  sourceLastMessageAt?: number
  sourceProjectionHighWatermarkSeq?: number
  hydratedAt?: number
}): PersistedDesktopV3MessageTailV1 {
  const ownerKey = requiredString(input.ownerKey, 'ownerKey')
  const sessionId = requiredString(input.sessionId, 'sessionId')
  return {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    key: createPersistedDesktopV3MessageTailKey(ownerKey, sessionId),
    ownerKey,
    sessionId,
    persistedAt: finiteNumber(input.persistedAt, 'persistedAt'),
    messages: validateMessages(input.messages, sessionId),
    sourceMessageCount: input.sourceMessageCount,
    sourceLastMessageAt: input.sourceLastMessageAt,
    sourceProjectionHighWatermarkSeq: input.sourceProjectionHighWatermarkSeq,
    hydratedAt: input.hydratedAt,
  }
}

function validateSchemaVersion(value: unknown): void {
  if (value !== DESKTOP_V3_CACHE_SCHEMA_VERSION) {
    throw new Error(`unsupported schema version ${String(value)}`)
  }
}

function validateSyncScopesById(value: unknown): Record<string, SyncScopeCache> {
  const input = recordValue(value, 'syncScopesById')
  const output: Record<string, SyncScopeCache> = {}
  for (const [scopeId, rawScope] of Object.entries(input)) {
    const scope = recordValue(rawScope, `syncScopesById.${scopeId}`)
    output[scopeId] = {
      scopeId: requiredString(scope.scopeId, `syncScopesById.${scopeId}.scopeId`),
      surface: requiredString(scope.surface, `syncScopesById.${scopeId}.surface`),
      streamKind: 'v3.sync.snapshot',
      selectorFilterHash: requiredString(scope.selectorFilterHash, `syncScopesById.${scopeId}.selectorFilterHash`),
      resourceSet: requiredString(scope.resourceSet, `syncScopesById.${scopeId}.resourceSet`),
      selector: recordValue(scope.selector, `syncScopesById.${scopeId}.selector`),
      endpointCursor: requiredString(scope.endpointCursor, `syncScopesById.${scopeId}.endpointCursor`),
      replayPath: requiredString(scope.replayPath, `syncScopesById.${scopeId}.replayPath`),
      replayTransport: requiredString(scope.replayTransport, `syncScopesById.${scopeId}.replayTransport`),
      needsBootstrap: Boolean(scope.needsBootstrap),
      lastErrorCode: optionalString(scope.lastErrorCode, `syncScopesById.${scopeId}.lastErrorCode`),
      lastError: optionalString(scope.lastError, `syncScopesById.${scopeId}.lastError`),
    }
  }
  return output
}

function validateSessionOrderByScope(value: unknown): Record<string, string[]> {
  const input = recordValue(value, 'sessionOrderByScope')
  const output: Record<string, string[]> = {}
  for (const [scopeId, order] of Object.entries(input)) {
    if (!Array.isArray(order)) {
      throw new Error(`sessionOrderByScope.${scopeId} must be an array`)
    }
    output[scopeId] = order.map((sessionId, index) => requiredString(sessionId, `sessionOrderByScope.${scopeId}.${index}`))
  }
  return output
}

function validateSidebarSessionsById(value: unknown): Record<string, PersistedDesktopV3SidebarSessionV1> {
  const input = recordValue(value, 'sidebarSessionsById')
  const output: Record<string, PersistedDesktopV3SidebarSessionV1> = {}
  for (const [sessionId, rawSidebar] of Object.entries(input)) {
    const sidebar = recordValue(rawSidebar, `sidebarSessionsById.${sessionId}`)
    const session = validateSession(sidebar.session, `sidebarSessionsById.${sessionId}.session`)
    if (session.id !== sessionId) {
      throw new Error(`sidebar session key mismatch for ${sessionId}`)
    }
    output[sessionId] = {
      session,
      projection: sidebar.projection === undefined ? undefined : validateProjection(sidebar.projection, sessionId, `sidebarSessionsById.${sessionId}.projection`),
      tombstone: sidebar.tombstone === undefined ? undefined : validateTombstone(sidebar.tombstone, sessionId, `sidebarSessionsById.${sessionId}.tombstone`),
      runIntents: sidebar.runIntents === undefined ? undefined : validateRunIntents(sidebar.runIntents, sessionId, `sidebarSessionsById.${sessionId}.runIntents`),
    }
  }
  return output
}

function validateMessages(value: unknown, sessionId: string): MessageSnapshot[] {
  if (!Array.isArray(value)) {
    throw new Error('messages must be an array')
  }
  return value.map((message, index) => validateMessage(message, sessionId, `messages.${index}`))
}

function validateSession(value: unknown, label: string): SessionSnapshot {
  const record = recordValue(value, label)
  return {
    id: requiredString(record.id, `${label}.id`),
    user_id: optionalString(record.user_id, `${label}.user_id`),
    account_scope_id: optionalString(record.account_scope_id, `${label}.account_scope_id`),
    workspace_path: requiredString(record.workspace_path, `${label}.workspace_path`),
    workspace_name: requiredString(record.workspace_name, `${label}.workspace_name`),
    temporary_workspace_roots: optionalStringArray(record.temporary_workspace_roots, `${label}.temporary_workspace_roots`),
    title: requiredString(record.title, `${label}.title`),
    mode: requiredString(record.mode, `${label}.mode`),
    preference: record.preference,
    worktree_enabled: optionalBoolean(record.worktree_enabled, `${label}.worktree_enabled`),
    worktree_root_path: optionalString(record.worktree_root_path, `${label}.worktree_root_path`),
    worktree_base_branch: optionalString(record.worktree_base_branch, `${label}.worktree_base_branch`),
    worktree_branch: optionalString(record.worktree_branch, `${label}.worktree_branch`),
    metadata: optionalRecord(record.metadata, `${label}.metadata`),
    created_at: finiteNumber(record.created_at, `${label}.created_at`),
    updated_at: finiteNumber(record.updated_at, `${label}.updated_at`),
    message_count: finiteNumber(record.message_count, `${label}.message_count`),
    last_message_at: finiteNumber(record.last_message_at, `${label}.last_message_at`),
    lifecycle: record.lifecycle,
  }
}

function validateMessage(value: unknown, sessionId: string, label: string): MessageSnapshot {
  const record = recordValue(value, label)
  const messageSessionId = requiredString(record.session_id, `${label}.session_id`)
  if (messageSessionId !== sessionId) {
    throw new Error(`${label}.session_id mismatch`)
  }
  return {
    id: requiredString(record.id, `${label}.id`),
    session_id: messageSessionId,
    user_id: optionalString(record.user_id, `${label}.user_id`),
    account_scope_id: optionalString(record.account_scope_id, `${label}.account_scope_id`),
    global_seq: finiteNumber(record.global_seq, `${label}.global_seq`),
    role: requiredString(record.role, `${label}.role`),
    content: requiredString(record.content, `${label}.content`),
    metadata: optionalRecord(record.metadata, `${label}.metadata`),
    created_at: finiteNumber(record.created_at, `${label}.created_at`),
  }
}

function validateProjection(value: unknown, sessionId: string, label: string): V3SessionProjection {
  const record = recordValue(value, label)
  const projectionSessionId = requiredString(record.session_id, `${label}.session_id`)
  if (projectionSessionId !== sessionId) {
    throw new Error(`${label}.session_id mismatch`)
  }
  return {
    session_id: projectionSessionId,
    last_event_seq: finiteNumber(record.last_event_seq, `${label}.last_event_seq`),
    projection_high_watermark_seq: finiteNumber(record.projection_high_watermark_seq, `${label}.projection_high_watermark_seq`),
    updated_at: finiteNumber(record.updated_at, `${label}.updated_at`),
  }
}

function validateTombstone(value: unknown, sessionId: string, label: string): V3SessionTombstone {
  const record = recordValue(value, label)
  const tombstoneSessionId = requiredString(record.session_id, `${label}.session_id`)
  if (tombstoneSessionId !== sessionId) {
    throw new Error(`${label}.session_id mismatch`)
  }
  return {
    session_id: tombstoneSessionId,
    user_id: optionalString(record.user_id, `${label}.user_id`),
    account_scope_id: optionalString(record.account_scope_id, `${label}.account_scope_id`),
    workspace_path: optionalString(record.workspace_path, `${label}.workspace_path`),
    kind: optionalString(record.kind, `${label}.kind`),
    deleted: optionalBoolean(record.deleted, `${label}.deleted`),
    archived: optionalBoolean(record.archived, `${label}.archived`),
    hidden: optionalBoolean(record.hidden, `${label}.hidden`),
    endpoint_seq: optionalFiniteNumber(record.endpoint_seq, `${label}.endpoint_seq`),
    event_seq: optionalFiniteNumber(record.event_seq, `${label}.event_seq`),
    updated_at: optionalFiniteNumber(record.updated_at, `${label}.updated_at`),
    session: record.session === undefined ? undefined : validateSession(record.session, `${label}.session`),
  }
}

function validateRunIntents(value: unknown, sessionId: string, label: string): V3SessionRunIntent[] {
  if (!Array.isArray(value)) {
    throw new Error(`${label} must be an array`)
  }
  return value.map((rawIntent, index) => validateRunIntent(rawIntent, sessionId, `${label}.${index}`))
}

function validateRunIntent(value: unknown, sessionId: string, label: string): V3SessionRunIntent {
  const record = recordValue(value, label)
  const intentSessionId = requiredString(record.session_id, `${label}.session_id`)
  if (intentSessionId !== sessionId) {
    throw new Error(`${label}.session_id mismatch`)
  }
  return {
    session_id: intentSessionId,
    user_id: optionalString(record.user_id, `${label}.user_id`),
    account_scope_id: optionalString(record.account_scope_id, `${label}.account_scope_id`),
    run_id: requiredString(record.run_id, `${label}.run_id`),
    status: requiredString(record.status, `${label}.status`),
    blocked_reason: optionalString(record.blocked_reason, `${label}.blocked_reason`),
    created_at: finiteNumber(record.created_at, `${label}.created_at`),
    updated_at: finiteNumber(record.updated_at, `${label}.updated_at`),
    event_seq: finiteNumber(record.event_seq, `${label}.event_seq`),
  }
}

function recordValue(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} must be an object`)
  }
  return value as Record<string, unknown>
}

function optionalRecord(value: unknown, label: string): Record<string, unknown> | undefined {
  if (value === undefined) return undefined
  return recordValue(value, label)
}

function requiredString(value: unknown, label: string): string {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`${label} is required`)
  }
  return value.trim()
}

function optionalString(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  return requiredString(value, label)
}

function optionalStringArray(value: unknown, label: string): string[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) {
    throw new Error(`${label} must be an array`)
  }
  return value.map((entry, index) => requiredString(entry, `${label}.${index}`))
}

function finiteNumber(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    throw new Error(`${label} must be finite`)
  }
  return value
}

function optionalFiniteNumber(value: unknown, label: string): number | undefined {
  if (value === undefined) return undefined
  return finiteNumber(value, label)
}

function optionalBoolean(value: unknown, label: string): boolean | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'boolean') {
    throw new Error(`${label} must be boolean`)
  }
  return value
}

function coldMiss(error: unknown): DesktopV3PersistedValidationResult<never> {
  return {
    ok: false,
    deleteRecord: true,
    reason: error instanceof Error ? error.message : String(error),
  }
}
