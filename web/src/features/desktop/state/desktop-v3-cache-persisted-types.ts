import type { DesktopPermissionRecord } from '../types/realtime'
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

export type PersistedDesktopV3LiveRunStatusV1 =
  | 'pending_executor'
  | 'running'
  | 'dispatch_blocked'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'interrupted'
  | 'expired'

export interface PersistedDesktopV3LiveReasoningV1 {
  key?: string
  reasoningId?: string
  reasoningKey?: string
  stepId?: string
  step?: number
  state: 'running' | 'completed' | 'error'
  summary: string
  text: string
  startedAt: number | null
  completedAt?: number | null
  updatedAt: number
  timelineSeq?: number
  updatedSeq?: number
}

export interface PersistedDesktopV3LiveToolCallV1 {
  callId: string
  stepId?: string
  toolInstanceId?: string
  toolName?: string
  argumentsText?: string
  outputText?: string
  errorText?: string
  durationMs?: number
  status?: string
  createdAt?: number
  updatedAt: number
  timelineSeq?: number
}

export interface PersistedDesktopV3LiveRunOverlayV1 {
  sessionId: string
  runId: string
  status: PersistedDesktopV3LiveRunStatusV1
  assistantDraft?: {
    content: string
    updatedAt: number
    timelineSeq?: number
  }
  assistantSegments?: Array<{
    id: string
    content: string
    createdAt: number
    updatedAt: number
    timelineSeq?: number
  }>
  toolCallsByCallId: Record<string, PersistedDesktopV3LiveToolCallV1>
  reasoning?: PersistedDesktopV3LiveReasoningV1
  reasoningByKey?: Record<string, PersistedDesktopV3LiveReasoningV1>
  lastEventSeqSeen?: number
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
  permissionsBySession?: Record<string, DesktopPermissionRecord[]>

  realtimeEndpointCursor?: string
  liveRunsBySession?: Record<
    string,
    Record<string, PersistedDesktopV3LiveRunOverlayV1>
  >
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

    const liveRunsBySession =
      record.liveRunsBySession === undefined
        ? {}
        : validatePersistedDesktopV3LiveRunsBySessionV1(
            record.liveRunsBySession,
            sidebarSessionsById,
          )
    const permissionsBySession = record.permissionsBySession === undefined
      ? undefined
      : validatePersistedDesktopV3PermissionsBySessionV1(record.permissionsBySession, sidebarSessionsById)

    const realtimeEndpointCursor = optionalString(
      record.realtimeEndpointCursor,
      'realtimeEndpointCursor',
    )

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
        permissionsBySession,
        realtimeEndpointCursor,
        liveRunsBySession,
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
  for (const [rawSessionId, rawSidebar] of Object.entries(input)) {
    const sessionId = requiredMapKey(rawSessionId, 'sidebarSessionsById key')
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



function validatePersistedDesktopV3PermissionsBySessionV1(
  value: unknown,
  sidebarSessionsById: Record<string, PersistedDesktopV3SidebarSessionV1>,
): Record<string, DesktopPermissionRecord[]> {
  const input = recordValue(value, 'permissionsBySession')
  const output: Record<string, DesktopPermissionRecord[]> = {}
  for (const [rawSessionId, rawPermissions] of Object.entries(input)) {
    const sessionId = requiredMapKey(rawSessionId, 'permissionsBySession session key')
    if (!hasOwnRecord(sidebarSessionsById, sessionId)) {
      throw new Error(`permissionsBySession.${sessionId} references missing sidebar session`)
    }
    if (!Array.isArray(rawPermissions)) {
      throw new Error(`permissionsBySession.${sessionId} must be an array`)
    }
    const permissions = rawPermissions.map((rawPermission, index) => validatePersistedDesktopV3PermissionV1(rawPermission, sessionId, `permissionsBySession.${sessionId}.${index}`))
    if (permissions.length > 0) output[sessionId] = permissions
  }
  return output
}

function validatePersistedDesktopV3PermissionV1(value: unknown, sessionId: string, label: string): DesktopPermissionRecord {
  const permission = recordValue(value, label)
  const id = requiredString(permission.id, `${label}.id`)
  const permissionSessionId = requiredString(permission.sessionId, `${label}.sessionId`)
  if (permissionSessionId !== sessionId) {
    throw new Error(`${label}.sessionId must match ${sessionId}`)
  }
  const savedRule = optionalRecord(permission.savedRule, `${label}.savedRule`)
  return {
    id,
    sessionId: permissionSessionId,
    runId: requiredStringValue(permission.runId, `${label}.runId`),
    callId: requiredStringValue(permission.callId, `${label}.callId`),
    toolName: requiredStringValue(permission.toolName, `${label}.toolName`),
    toolArguments: requiredStringValue(permission.toolArguments, `${label}.toolArguments`),
    approvedArguments: optionalStringValue(permission.approvedArguments, `${label}.approvedArguments`),
    savedRule: savedRule ? {
      id: requiredStringValue(savedRule.id, `${label}.savedRule.id`),
      kind: requiredStringValue(savedRule.kind, `${label}.savedRule.kind`),
      decision: requiredStringValue(savedRule.decision, `${label}.savedRule.decision`),
      tool: optionalStringValue(savedRule.tool, `${label}.savedRule.tool`),
      pattern: optionalStringValue(savedRule.pattern, `${label}.savedRule.pattern`),
      createdAt: savedRule.createdAt === undefined ? undefined : nonNegativeSafeInteger(savedRule.createdAt, `${label}.savedRule.createdAt`),
      updatedAt: savedRule.updatedAt === undefined ? undefined : nonNegativeSafeInteger(savedRule.updatedAt, `${label}.savedRule.updatedAt`),
    } : undefined,
    status: requiredStringValue(permission.status, `${label}.status`),
    decision: requiredStringValue(permission.decision, `${label}.decision`),
    reason: requiredStringValue(permission.reason, `${label}.reason`),
    requirement: requiredStringValue(permission.requirement, `${label}.requirement`),
    mode: requiredStringValue(permission.mode, `${label}.mode`),
    createdAt: nonNegativeSafeInteger(permission.createdAt, `${label}.createdAt`),
    updatedAt: nonNegativeSafeInteger(permission.updatedAt, `${label}.updatedAt`),
    resolvedAt: nonNegativeSafeInteger(permission.resolvedAt, `${label}.resolvedAt`),
    permissionRequestedAt: nonNegativeSafeInteger(permission.permissionRequestedAt, `${label}.permissionRequestedAt`),
  }
}

function validatePersistedDesktopV3LiveRunsBySessionV1(
  value: unknown,
  sidebarSessionsById: Record<string, PersistedDesktopV3SidebarSessionV1>,
): Record<string, Record<string, PersistedDesktopV3LiveRunOverlayV1>> {
  const input = recordValue(value, 'liveRunsBySession')
  const output: Record<string, Record<string, PersistedDesktopV3LiveRunOverlayV1>> = {}

  for (const [rawSessionId, rawRunsById] of Object.entries(input)) {
    const sessionId = requiredMapKey(rawSessionId, 'liveRunsBySession session key')
    if (!hasOwnRecord(sidebarSessionsById, sessionId)) {
      throw new Error(`liveRunsBySession.${sessionId} references missing sidebar session`)
    }
    const sidebarSession = sidebarSessionsById[sessionId]
    if (sidebarSession.tombstone) {
      throw new Error(`liveRunsBySession.${sessionId} references tombstoned session`)
    }

    const runsInput = recordValue(rawRunsById, `liveRunsBySession.${sessionId}`)
    const runsOutput: Record<string, PersistedDesktopV3LiveRunOverlayV1> = {}
    for (const [rawRunId, rawOverlay] of Object.entries(runsInput)) {
      const runId = requiredMapKey(rawRunId, `liveRunsBySession.${sessionId} run key`)
      runsOutput[runId] = validatePersistedDesktopV3LiveRunOverlayV1(rawOverlay, sessionId, runId)
    }
    output[sessionId] = runsOutput
  }

  return output
}

function validatePersistedDesktopV3LiveRunOverlayV1(
  value: unknown,
  outerSessionId: string,
  outerRunId: string,
): PersistedDesktopV3LiveRunOverlayV1 {
  const record = recordValue(value, `liveRunsBySession.${outerSessionId}.${outerRunId}`)
  const sessionId = requiredString(record.sessionId, `liveRunsBySession.${outerSessionId}.${outerRunId}.sessionId`)
  const runId = requiredString(record.runId, `liveRunsBySession.${outerSessionId}.${outerRunId}.runId`)
  if (sessionId !== outerSessionId) {
    throw new Error(`liveRunsBySession.${outerSessionId}.${outerRunId}.sessionId mismatch`)
  }
  if (runId !== outerRunId) {
    throw new Error(`liveRunsBySession.${outerSessionId}.${outerRunId}.runId mismatch`)
  }

  const output: PersistedDesktopV3LiveRunOverlayV1 = {
    sessionId,
    runId,
    status: validatePersistedDesktopV3LiveRunStatusV1(record.status, `liveRunsBySession.${outerSessionId}.${outerRunId}.status`),
    toolCallsByCallId: validatePersistedDesktopV3LiveToolCallsByCallIdV1(
      record.toolCallsByCallId,
      `liveRunsBySession.${outerSessionId}.${outerRunId}.toolCallsByCallId`,
    ),
    lastEventSeqSeen: optionalNonNegativeSafeInteger(record.lastEventSeqSeen, `liveRunsBySession.${outerSessionId}.${outerRunId}.lastEventSeqSeen`),
  }

  if (record.assistantDraft !== undefined) {
    output.assistantDraft = validatePersistedDesktopV3AssistantDraftV1(
      record.assistantDraft,
      `liveRunsBySession.${outerSessionId}.${outerRunId}.assistantDraft`,
    )
  }
  if (record.assistantSegments !== undefined) {
    output.assistantSegments = validatePersistedDesktopV3AssistantSegmentsV1(
      record.assistantSegments,
      `liveRunsBySession.${outerSessionId}.${outerRunId}.assistantSegments`,
    )
  }
  if (record.reasoning !== undefined) {
    output.reasoning = validatePersistedDesktopV3LiveReasoningV1(
      record.reasoning,
      `liveRunsBySession.${outerSessionId}.${outerRunId}.reasoning`,
    )
  }
  if (record.reasoningByKey !== undefined) {
    output.reasoningByKey = validatePersistedDesktopV3LiveReasoningByKeyV1(
      record.reasoningByKey,
      `liveRunsBySession.${outerSessionId}.${outerRunId}.reasoningByKey`,
    )
  }

  return output
}

function validatePersistedDesktopV3LiveReasoningV1(
  value: unknown,
  label: string,
): PersistedDesktopV3LiveReasoningV1 {
  const record = recordValue(value, label)
  const output: PersistedDesktopV3LiveReasoningV1 = {
    state: validatePersistedDesktopV3LiveReasoningStateV1(record.state, `${label}.state`),
    summary: requiredStringValue(record.summary, `${label}.summary`),
    text: requiredStringValue(record.text, `${label}.text`),
    startedAt: nullableNonNegativeSafeInteger(record.startedAt, `${label}.startedAt`),
    updatedAt: nonNegativeSafeInteger(record.updatedAt, `${label}.updatedAt`),
  }
  assignOptional(output, 'key', optionalString(record.key, `${label}.key`))
  assignOptional(output, 'reasoningId', optionalString(record.reasoningId, `${label}.reasoningId`))
  assignOptional(output, 'reasoningKey', optionalString(record.reasoningKey, `${label}.reasoningKey`))
  assignOptional(output, 'stepId', optionalString(record.stepId, `${label}.stepId`))
  assignOptional(output, 'step', optionalNonNegativeSafeInteger(record.step, `${label}.step`))
  assignOptional(output, 'completedAt', optionalNullableNonNegativeSafeInteger(record.completedAt, `${label}.completedAt`))
  assignOptional(output, 'timelineSeq', optionalNonNegativeSafeInteger(record.timelineSeq, `${label}.timelineSeq`))
  assignOptional(output, 'updatedSeq', optionalNonNegativeSafeInteger(record.updatedSeq, `${label}.updatedSeq`))
  return output
}

function validatePersistedDesktopV3LiveToolCallV1(
  value: unknown,
  mapCallId: string,
  label: string,
): PersistedDesktopV3LiveToolCallV1 {
  const record = recordValue(value, label)
  const callId = requiredString(record.callId, `${label}.callId`)
  if (callId !== mapCallId) {
    throw new Error(`${label}.callId mismatch`)
  }
  const output: PersistedDesktopV3LiveToolCallV1 = {
    callId,
    updatedAt: nonNegativeSafeInteger(record.updatedAt, `${label}.updatedAt`),
  }
  assignOptional(output, 'stepId', optionalString(record.stepId, `${label}.stepId`))
  assignOptional(output, 'toolInstanceId', optionalString(record.toolInstanceId, `${label}.toolInstanceId`))
  assignOptional(output, 'toolName', optionalString(record.toolName, `${label}.toolName`))
  assignOptional(output, 'argumentsText', optionalStringValue(record.argumentsText, `${label}.argumentsText`))
  assignOptional(output, 'outputText', optionalStringValue(record.outputText, `${label}.outputText`))
  assignOptional(output, 'errorText', optionalStringValue(record.errorText, `${label}.errorText`))
  assignOptional(output, 'durationMs', optionalNonNegativeSafeInteger(record.durationMs, `${label}.durationMs`))
  assignOptional(output, 'status', optionalString(record.status, `${label}.status`))
  assignOptional(output, 'createdAt', optionalNonNegativeSafeInteger(record.createdAt, `${label}.createdAt`))
  assignOptional(output, 'timelineSeq', optionalNonNegativeSafeInteger(record.timelineSeq, `${label}.timelineSeq`))
  return output
}

function validatePersistedDesktopV3LiveRunStatusV1(value: unknown, label: string): PersistedDesktopV3LiveRunStatusV1 {
  switch (value) {
    case 'pending_executor':
    case 'running':
    case 'dispatch_blocked':
    case 'completed':
    case 'failed':
    case 'cancelled':
    case 'interrupted':
    case 'expired':
      return value
    default:
      throw new Error(`${label} is not a valid live run status`)
  }
}

function validatePersistedDesktopV3LiveReasoningStateV1(value: unknown, label: string): PersistedDesktopV3LiveReasoningV1['state'] {
  switch (value) {
    case 'running':
    case 'completed':
    case 'error':
      return value
    default:
      throw new Error(`${label} is not a valid live reasoning state`)
  }
}

function validatePersistedDesktopV3AssistantDraftV1(
  value: unknown,
  label: string,
): NonNullable<PersistedDesktopV3LiveRunOverlayV1['assistantDraft']> {
  const record = recordValue(value, label)
  return {
    content: requiredStringValue(record.content, `${label}.content`),
    updatedAt: nonNegativeSafeInteger(record.updatedAt, `${label}.updatedAt`),
    timelineSeq: optionalNonNegativeSafeInteger(record.timelineSeq, `${label}.timelineSeq`),
  }
}

function validatePersistedDesktopV3AssistantSegmentsV1(
  value: unknown,
  label: string,
): NonNullable<PersistedDesktopV3LiveRunOverlayV1['assistantSegments']> {
  if (!Array.isArray(value)) {
    throw new Error(`${label} must be an array`)
  }
  return value.map((entry, index) => {
    const segment = recordValue(entry, `${label}.${index}`)
    return {
      id: requiredString(segment.id, `${label}.${index}.id`),
      content: requiredStringValue(segment.content, `${label}.${index}.content`),
      createdAt: nonNegativeSafeInteger(segment.createdAt, `${label}.${index}.createdAt`),
      updatedAt: nonNegativeSafeInteger(segment.updatedAt, `${label}.${index}.updatedAt`),
      timelineSeq: optionalNonNegativeSafeInteger(segment.timelineSeq, `${label}.${index}.timelineSeq`),
    }
  })
}

function validatePersistedDesktopV3LiveToolCallsByCallIdV1(
  value: unknown,
  label: string,
): Record<string, PersistedDesktopV3LiveToolCallV1> {
  const input = recordValue(value, label)
  const output: Record<string, PersistedDesktopV3LiveToolCallV1> = {}
  for (const [rawCallId, rawTool] of Object.entries(input)) {
    const callId = requiredMapKey(rawCallId, `${label} key`)
    output[callId] = validatePersistedDesktopV3LiveToolCallV1(rawTool, callId, `${label}.${callId}`)
  }
  return output
}

function validatePersistedDesktopV3LiveReasoningByKeyV1(
  value: unknown,
  label: string,
): Record<string, PersistedDesktopV3LiveReasoningV1> {
  const input = recordValue(value, label)
  const output: Record<string, PersistedDesktopV3LiveReasoningV1> = {}
  for (const [rawKey, rawReasoning] of Object.entries(input)) {
    const mapKey = requiredMapKey(rawKey, `${label} key`)
    const reasoning = validatePersistedDesktopV3LiveReasoningV1(rawReasoning, `${label}.${mapKey}`)
    if (reasoning.key !== undefined && reasoning.key !== mapKey) {
      throw new Error(`${label}.${mapKey}.key mismatch`)
    }
    output[mapKey] = reasoning
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

function assignOptional<T extends object, K extends keyof T>(target: T, key: K, value: T[K] | undefined): void {
  if (value !== undefined) {
    target[key] = value
  }
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

function requiredMapKey(
  value: string,
  label: string,
): string {
  if (value.trim() === '') {
    throw new Error(`${label} is required`)
  }
  if (value !== value.trim()) {
    throw new Error(
      `${label} must not include leading or trailing whitespace`,
    )
  }
  if (value === '__proto__') {
    throw new Error(`${label} is not a valid map key`)
  }
  return value
}

function hasOwnRecord(
  record: Record<string, unknown>,
  key: string,
): boolean {
  return Object.prototype.hasOwnProperty.call(record, key)
}

function optionalString(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  return requiredString(value, label)
}

function requiredStringValue(value: unknown, label: string): string {
  if (typeof value !== 'string') {
    throw new Error(`${label} must be a string`)
  }
  return value
}

function optionalStringValue(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  return requiredStringValue(value, label)
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

function nullableNonNegativeSafeInteger(value: unknown, label: string): number | null {
  if (value === null) return null
  return nonNegativeSafeInteger(value, label)
}

function optionalNullableNonNegativeSafeInteger(value: unknown, label: string): number | null | undefined {
  if (value === undefined) return undefined
  return nullableNonNegativeSafeInteger(value, label)
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
