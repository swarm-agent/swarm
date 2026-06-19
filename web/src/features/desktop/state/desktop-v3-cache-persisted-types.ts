import type {
  MessageSnapshot,
  SyncScopeCache,
  SessionSnapshot,
  V3SessionProjection,
  V3SessionRunIntent,
  V3SessionTombstone,
} from './desktop-v3-cache-types'
import { createDesktopV3CacheOwner, parseDesktopV3CacheOwnerKey, type DesktopV3CacheOwner } from './desktop-v3-cache-owner'

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
  sidebarScopeId: string
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
  parseDesktopV3CacheOwnerKey(normalizedOwnerKey)
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

    const parsedOwner = parseDesktopV3CacheOwnerKey(ownerKey)
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
    if (!sameOwnerIdentity(owner, parsedOwner)) {
      throw new Error('owner key does not match encoded owner identity')
    }

    const selectedSessionId = optionalString(record.selectedSessionId, 'selectedSessionId')
    const sidebarScopeId = requiredString(record.sidebarScopeId, 'sidebarScopeId')
    const syncScopesById = validateSyncScopesById(record.syncScopesById)
    const sessionOrderByScope = validateSessionOrderByScope(record.sessionOrderByScope)
    const sidebarSessionsById = validateSidebarSessionsById(record.sidebarSessionsById, owner)
    validateOwnerReferences({ selectedSessionId, sidebarScopeId, syncScopesById, sessionOrderByScope, sidebarSessionsById })

    return {
      ok: true,
      value: {
        schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
        ownerKey,
        owner,
        persistedAt: nonNegativeSafeInteger(record.persistedAt, 'persistedAt'),
        selectedSessionId,
        sidebarScopeId,
        syncScopesById,
        sessionOrderByScope,
        sidebarSessionsById,
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
    parseDesktopV3CacheOwnerKey(ownerKey)
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
        persistedAt: nonNegativeSafeInteger(record.persistedAt, 'persistedAt'),
        messages: validateMessages(record.messages, sessionId, ownerKey),
        sourceMessageCount: optionalNonNegativeSafeInteger(record.sourceMessageCount, 'sourceMessageCount'),
        sourceLastMessageAt: optionalNonNegativeSafeInteger(record.sourceLastMessageAt, 'sourceLastMessageAt'),
        sourceProjectionHighWatermarkSeq: optionalNonNegativeSafeInteger(record.sourceProjectionHighWatermarkSeq, 'sourceProjectionHighWatermarkSeq'),
        hydratedAt: optionalNonNegativeSafeInteger(record.hydratedAt, 'hydratedAt'),
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
  parseDesktopV3CacheOwnerKey(ownerKey)
  const sessionId = requiredString(input.sessionId, 'sessionId')
  return {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    key: createPersistedDesktopV3MessageTailKey(ownerKey, sessionId),
    ownerKey,
    sessionId,
    persistedAt: nonNegativeSafeInteger(input.persistedAt, 'persistedAt'),
    messages: validateMessages(input.messages, sessionId, ownerKey),
    sourceMessageCount: optionalNonNegativeSafeInteger(input.sourceMessageCount, 'sourceMessageCount'),
    sourceLastMessageAt: optionalNonNegativeSafeInteger(input.sourceLastMessageAt, 'sourceLastMessageAt'),
    sourceProjectionHighWatermarkSeq: optionalNonNegativeSafeInteger(input.sourceProjectionHighWatermarkSeq, 'sourceProjectionHighWatermarkSeq'),
    hydratedAt: optionalNonNegativeSafeInteger(input.hydratedAt, 'hydratedAt'),
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
  for (const [mapScopeId, rawScope] of Object.entries(input)) {
    const scope = recordValue(rawScope, `syncScopesById.${mapScopeId}`)
    const scopeId = requiredString(scope.scopeId, `syncScopesById.${mapScopeId}.scopeId`)
    if (scopeId !== mapScopeId) {
      throw new Error('persisted sync scope key mismatch')
    }

    output[mapScopeId] = {
      scopeId,
      surface: requiredString(scope.surface, `syncScopesById.${mapScopeId}.surface`),
      streamKind: exactString(scope.streamKind, 'v3.sync.snapshot', `syncScopesById.${mapScopeId}.streamKind`, 'unsupported persisted sync stream kind'),
      selectorFilterHash: requiredString(scope.selectorFilterHash, `syncScopesById.${mapScopeId}.selectorFilterHash`),
      resourceSet: requiredString(scope.resourceSet, `syncScopesById.${mapScopeId}.resourceSet`),
      selector: recordValue(scope.selector, `syncScopesById.${mapScopeId}.selector`),
      endpointCursor: requiredString(scope.endpointCursor, `syncScopesById.${mapScopeId}.endpointCursor`),
      replayPath: exactString(scope.replayPath, '/v3/sync/stream', `syncScopesById.${mapScopeId}.replayPath`),
      replayTransport: exactString(scope.replayTransport, 'http_post', `syncScopesById.${mapScopeId}.replayTransport`),
      needsBootstrap: requiredBoolean(scope.needsBootstrap, `syncScopesById.${mapScopeId}.needsBootstrap`),
      lastErrorCode: optionalString(scope.lastErrorCode, `syncScopesById.${mapScopeId}.lastErrorCode`),
      lastError: optionalString(scope.lastError, `syncScopesById.${mapScopeId}.lastError`),
    }
  }
  return output
}

function validateSessionOrderByScope(value: unknown): Record<string, string[]> {
  const input = recordValue(value, 'sessionOrderByScope')
  const output: Record<string, string[]> = {}
  for (const [rawScopeId, order] of Object.entries(input)) {
    const scopeId = requiredMapKey(rawScopeId, 'sessionOrderByScope key')
    if (!Array.isArray(order)) {
      throw new Error(`sessionOrderByScope.${scopeId} must be an array`)
    }
    const seen = new Set<string>()
    output[scopeId] = order.map((sessionId, index) => {
      const normalizedSessionId = requiredString(sessionId, `sessionOrderByScope.${scopeId}.${index}`)
      if (seen.has(normalizedSessionId)) {
        throw new Error(`sessionOrderByScope.${scopeId} contains duplicate session ${normalizedSessionId}`)
      }
      seen.add(normalizedSessionId)
      return normalizedSessionId
    })
  }
  return output
}

function validateSidebarSessionsById(
  value: unknown,
  owner: DesktopV3CacheOwner,
): Record<string, PersistedDesktopV3SidebarSessionV1> {
  const input = recordValue(value, 'sidebarSessionsById')
  const output: Record<string, PersistedDesktopV3SidebarSessionV1> = {}
  for (const [sessionId, rawSidebar] of Object.entries(input)) {
    const sidebar = recordValue(rawSidebar, `sidebarSessionsById.${sessionId}`)
    const session = validateSession(sidebar.session, `sidebarSessionsById.${sessionId}.session`, owner)
    if (session.id !== sessionId) {
      throw new Error(`sidebar session key mismatch for ${sessionId}`)
    }
    output[sessionId] = {
      session,
      projection: sidebar.projection === undefined ? undefined : validateProjection(sidebar.projection, sessionId, `sidebarSessionsById.${sessionId}.projection`),
      tombstone: sidebar.tombstone === undefined ? undefined : validateTombstone(sidebar.tombstone, sessionId, `sidebarSessionsById.${sessionId}.tombstone`, owner),
      runIntents: sidebar.runIntents === undefined ? undefined : validateRunIntents(sidebar.runIntents, sessionId, `sidebarSessionsById.${sessionId}.runIntents`, owner),
    }
  }
  return output
}

function validateOwnerReferences(input: {
  selectedSessionId?: string
  sidebarScopeId: string
  syncScopesById: Record<string, SyncScopeCache>
  sessionOrderByScope: Record<string, string[]>
  sidebarSessionsById: Record<string, PersistedDesktopV3SidebarSessionV1>
}): void {
  if (input.selectedSessionId !== undefined && !input.sidebarSessionsById[input.selectedSessionId]) {
    throw new Error('persisted selectedSessionId does not resolve to a sidebar session')
  }
  if (!input.syncScopesById[input.sidebarScopeId]) {
    throw new Error('persisted sidebarScopeId does not resolve to a persisted sync scope')
  }
  if (!input.sessionOrderByScope[input.sidebarScopeId]) {
    throw new Error('persisted sidebarScopeId does not resolve to a persisted session order')
  }

  for (const [scopeId, sessionOrder] of Object.entries(input.sessionOrderByScope)) {
    if (!input.syncScopesById[scopeId]) {
      throw new Error(`sessionOrderByScope.${scopeId} does not resolve to a persisted sync scope`)
    }
    for (const sessionId of sessionOrder) {
      if (!input.sidebarSessionsById[sessionId]) {
        throw new Error(`sessionOrderByScope.${scopeId} references missing sidebar session ${sessionId}`)
      }
    }
  }
}

function validateMessages(value: unknown, sessionId: string, ownerKey: string): MessageSnapshot[] {
  if (!Array.isArray(value)) {
    throw new Error('messages must be an array')
  }
  const owner = parseDesktopV3CacheOwnerKey(ownerKey)
  return value.map((message, index) => validateMessage(message, sessionId, `messages.${index}`, owner))
}

function validateSession(value: unknown, label: string, owner: DesktopV3CacheOwner): SessionSnapshot {
  const record = recordValue(value, label)
  const userId = optionalString(record.user_id, `${label}.user_id`)
  const accountScopeId = optionalString(record.account_scope_id, `${label}.account_scope_id`)
  validateOptionalOwnerIdentity(userId, accountScopeId, owner, label)
  return {
    id: requiredString(record.id, `${label}.id`),
    user_id: userId,
    account_scope_id: accountScopeId,
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
    created_at: nonNegativeSafeInteger(record.created_at, `${label}.created_at`),
    updated_at: nonNegativeSafeInteger(record.updated_at, `${label}.updated_at`),
    message_count: nonNegativeSafeInteger(record.message_count, `${label}.message_count`),
    last_message_at: nonNegativeSafeInteger(record.last_message_at, `${label}.last_message_at`),
    lifecycle: record.lifecycle,
  }
}

function validateMessage(value: unknown, sessionId: string, label: string, owner: DesktopV3CacheOwner): MessageSnapshot {
  const record = recordValue(value, label)
  const messageSessionId = requiredString(record.session_id, `${label}.session_id`)
  if (messageSessionId !== sessionId) {
    throw new Error(`${label}.session_id mismatch`)
  }
  const userId = optionalString(record.user_id, `${label}.user_id`)
  const accountScopeId = optionalString(record.account_scope_id, `${label}.account_scope_id`)
  validateOptionalOwnerIdentity(userId, accountScopeId, owner, label)
  return {
    id: requiredString(record.id, `${label}.id`),
    session_id: messageSessionId,
    user_id: userId,
    account_scope_id: accountScopeId,
    global_seq: nonNegativeSafeInteger(record.global_seq, `${label}.global_seq`),
    role: requiredString(record.role, `${label}.role`),
    content: requiredContent(record.content, `${label}.content`),
    metadata: optionalRecord(record.metadata, `${label}.metadata`),
    created_at: nonNegativeSafeInteger(record.created_at, `${label}.created_at`),
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
    last_event_seq: nonNegativeSafeInteger(record.last_event_seq, `${label}.last_event_seq`),
    projection_high_watermark_seq: nonNegativeSafeInteger(record.projection_high_watermark_seq, `${label}.projection_high_watermark_seq`),
    updated_at: nonNegativeSafeInteger(record.updated_at, `${label}.updated_at`),
  }
}

function validateTombstone(value: unknown, sessionId: string, label: string, owner: DesktopV3CacheOwner): V3SessionTombstone {
  const record = recordValue(value, label)
  const tombstoneSessionId = requiredString(record.session_id, `${label}.session_id`)
  if (tombstoneSessionId !== sessionId) {
    throw new Error(`${label}.session_id mismatch`)
  }
  const embeddedSession = record.session === undefined ? undefined : validateSession(record.session, `${label}.session`, owner)
  if (embeddedSession !== undefined && embeddedSession.id !== tombstoneSessionId) {
    throw new Error(`${label}.session.id mismatch`)
  }
  const userId = optionalString(record.user_id, `${label}.user_id`)
  const accountScopeId = optionalString(record.account_scope_id, `${label}.account_scope_id`)
  validateOptionalOwnerIdentity(userId, accountScopeId, owner, label)
  return {
    session_id: tombstoneSessionId,
    user_id: userId,
    account_scope_id: accountScopeId,
    workspace_path: optionalString(record.workspace_path, `${label}.workspace_path`),
    kind: optionalString(record.kind, `${label}.kind`),
    deleted: optionalBoolean(record.deleted, `${label}.deleted`),
    archived: optionalBoolean(record.archived, `${label}.archived`),
    hidden: optionalBoolean(record.hidden, `${label}.hidden`),
    endpoint_seq: optionalNonNegativeSafeInteger(record.endpoint_seq, `${label}.endpoint_seq`),
    event_seq: optionalNonNegativeSafeInteger(record.event_seq, `${label}.event_seq`),
    updated_at: optionalNonNegativeSafeInteger(record.updated_at, `${label}.updated_at`),
    session: embeddedSession,
  }
}

function validateRunIntents(value: unknown, sessionId: string, label: string, owner: DesktopV3CacheOwner): V3SessionRunIntent[] {
  if (!Array.isArray(value)) {
    throw new Error(`${label} must be an array`)
  }
  return value.map((rawIntent, index) => validateRunIntent(rawIntent, sessionId, `${label}.${index}`, owner))
}

function validateRunIntent(value: unknown, sessionId: string, label: string, owner: DesktopV3CacheOwner): V3SessionRunIntent {
  const record = recordValue(value, label)
  const intentSessionId = requiredString(record.session_id, `${label}.session_id`)
  if (intentSessionId !== sessionId) {
    throw new Error(`${label}.session_id mismatch`)
  }
  const userId = optionalString(record.user_id, `${label}.user_id`)
  const accountScopeId = optionalString(record.account_scope_id, `${label}.account_scope_id`)
  validateOptionalOwnerIdentity(userId, accountScopeId, owner, label)
  return {
    session_id: intentSessionId,
    user_id: userId,
    account_scope_id: accountScopeId,
    run_id: requiredString(record.run_id, `${label}.run_id`),
    status: requiredString(record.status, `${label}.status`),
    blocked_reason: optionalString(record.blocked_reason, `${label}.blocked_reason`),
    created_at: nonNegativeSafeInteger(record.created_at, `${label}.created_at`),
    updated_at: nonNegativeSafeInteger(record.updated_at, `${label}.updated_at`),
    event_seq: nonNegativeSafeInteger(record.event_seq, `${label}.event_seq`),
  }
}

function validateOptionalOwnerIdentity(
  userId: string | undefined,
  accountScopeId: string | undefined,
  owner: DesktopV3CacheOwner,
  label: string,
): void {
  if (userId !== undefined && userId !== owner.userId) {
    throw new Error(`${label}.user_id conflicts with persisted owner`)
  }
  if (accountScopeId !== undefined && accountScopeId !== owner.accountScopeId) {
    throw new Error(`${label}.account_scope_id conflicts with persisted owner`)
  }
}

function sameOwnerIdentity(left: DesktopV3CacheOwner, right: DesktopV3CacheOwner): boolean {
  return left.key === right.key
    && left.origin === right.origin
    && left.accountScopeId === right.accountScopeId
    && left.userId === right.userId
    && left.surface === right.surface
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

function requiredContent(value: unknown, label: string): string {
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(`${label} must be a non-empty string`)
  }
  return value
}

function requiredMapKey(value: string, label: string): string {
  if (value.trim() === '') {
    throw new Error(`${label} is required`)
  }
  if (value !== value.trim()) {
    throw new Error(`${label} must not include leading or trailing whitespace`)
  }
  return value
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

function exactString<T extends string>(value: unknown, expected: T, label: string, errorMessage?: string): T {
  if (typeof value !== 'string' || value !== expected) {
    throw new Error(errorMessage ?? `${label} must be ${expected}`)
  }
  return expected
}

function nonNegativeSafeInteger(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw new Error(`${label} must be a non-negative safe integer`)
  }
  return value
}

function optionalNonNegativeSafeInteger(value: unknown, label: string): number | undefined {
  if (value === undefined) return undefined
  return nonNegativeSafeInteger(value, label)
}

function requiredBoolean(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(`${label} must be boolean`)
  }
  return value
}

function optionalBoolean(value: unknown, label: string): boolean | undefined {
  if (value === undefined) return undefined
  return requiredBoolean(value, label)
}

function coldMiss(error: unknown): DesktopV3PersistedValidationResult<never> {
  return {
    ok: false,
    deleteRecord: true,
    reason: error instanceof Error ? error.message : String(error),
  }
}
