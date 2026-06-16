import { parseStructuredToolMessage } from '../chat/services/tool-message'
import {
  createEmptyDesktopState,
  desktopReducer,
  type DesktopDaemonEvent,
  type DesktopDaemonSnapshot,
  type DesktopState,
  type DesktopStateStatus,
} from '../state/desktop-state'
import type { ChatMessageRecord } from '../chat/types/chat'
import type { DesktopPermissionRecord, DesktopRunIntentRecord, DesktopSessionRecord, DesktopSessionUsageRecord } from '../types/realtime'
import type {
  SessionV3HydratedSessionResponseWire,
  SessionV3MessageWire,
  SessionV3ProjectionWire,
  SessionV3RealtimeFrameWire,
  SessionV3RealtimeOutboxWire,
  SessionV3ReconnectSnapshot,
  SessionV3RunIntentWire,
  SessionV3SessionWire,
  SessionV3SnapshotResult,
} from './types'

const MAX_APPLIED_FRAME_IDS = 2048

export type SessionV3ReducerSnapshotMode = 'replace' | 'merge' | 'reconcile'
export type SessionV3ReducerStatus = 'idle' | 'ready' | 'stale' | 'error'

export interface SessionV3ReducerSubscriptionState {
  sessionId: string
  subscriptionId: string
  endpointCursor: string
  worksetIds: string[]
  autoSubscribed: boolean
  updatedAt: number
}

export interface SessionV3ReducerWorksetState {
  worksetId: string
  subscriptionId: string
  sessionIds: string[]
  removedSessionIds: string[]
  autoSubscribeSessions: boolean
  updatedAt: number
}

export interface SessionV3ReducerApplySummary {
  action: SessionV3ReducerAction['type']
  applied: boolean
  desktopChanged: boolean
  cursorAdvanced: boolean
  duplicate: boolean
  rejected: boolean
  stale: boolean
  endpointCursor: string
  sessionId: string | null
  frameKind: string | null
  reason: string | null
  mutationSeq: number
  receivedAt: number
}

export interface SessionV3ReducerState {
  desktop: DesktopState
  endpointCursor: string
  status: SessionV3ReducerStatus
  staleReason: string | null
  subscriptionsBySessionId: Record<string, SessionV3ReducerSubscriptionState>
  worksetsById: Record<string, SessionV3ReducerWorksetState>
  discoveredSessionIds: string[]
  removedSessionIds: string[]
  appliedFrameIds: Record<string, true>
  appliedFrameOrder: string[]
  mutationSeq: number
  lastApply: SessionV3ReducerApplySummary | null
}

export type SessionV3ReducerAction =
  | { type: 'snapshot'; snapshot: DesktopDaemonSnapshot; mode?: SessionV3ReducerSnapshotMode; endpointCursor?: string | null; receivedAt?: number }
  | { type: 'snapshot-result'; result: SessionV3SnapshotResult; mode?: SessionV3ReducerSnapshotMode; receivedAt?: number }
  | { type: 'reconnect'; result: SessionV3ReconnectSnapshot; mode?: SessionV3ReducerSnapshotMode; receivedAt?: number }
  | { type: 'frame'; frame: SessionV3RealtimeFrameWire; receivedAt?: number }
  | { type: 'mutation'; response: SessionV3HydratedSessionResponseWire; sessionId?: string | null; receivedAt?: number }
  | { type: 'status'; status: DesktopStateStatus; error?: string | null; receivedAt?: number }
  | { type: 'stale'; reason: string; receivedAt?: number }

export interface SessionV3ReducerResult {
  state: SessionV3ReducerState
  applied: boolean
  desktopChanged: boolean
  cursorAdvanced: boolean
  duplicate: boolean
  rejected: boolean
  stale: boolean
  reason: string | null
}

type SessionV3ReducerActionLike = {
  type: SessionV3ReducerAction['type']
  receivedAt?: number
  frame?: SessionV3RealtimeFrameWire
  sessionId?: string | null
}

interface ReducerDraft {
  desktop?: DesktopState
  endpointCursor?: string
  status?: SessionV3ReducerStatus
  staleReason?: string | null
  subscriptionsBySessionId?: Record<string, SessionV3ReducerSubscriptionState>
  worksetsById?: Record<string, SessionV3ReducerWorksetState>
  discoveredSessionIds?: string[]
  removedSessionIds?: string[]
  rememberFrameId?: string
}

export function createSessionV3ReducerInitialState(desktop: DesktopState = createEmptyDesktopState()): SessionV3ReducerState {
  return {
    desktop,
    endpointCursor: '',
    status: 'idle',
    staleReason: null,
    subscriptionsBySessionId: {},
    worksetsById: {},
    discoveredSessionIds: [],
    removedSessionIds: [],
    appliedFrameIds: {},
    appliedFrameOrder: [],
    mutationSeq: 0,
    lastApply: null,
  }
}

export function sessionV3Reducer(state: SessionV3ReducerState, action: SessionV3ReducerAction): SessionV3ReducerResult {
  switch (action.type) {
    case 'snapshot':
      return applySessionV3Snapshot(state, action.snapshot, action)
    case 'snapshot-result':
      return applySessionV3Snapshot(state, action.result.snapshot, {
        ...action,
        endpointCursor: action.result.endpointCursor,
      })
    case 'reconnect':
      return applySessionV3Reconnect(state, action.result, action)
    case 'frame':
      return applySessionV3RealtimeFrame(state, action.frame, action)
    case 'mutation':
      return applySessionV3MutationResult(state, action.response, action)
    case 'status':
      return applyReducerDraft(state, action, {
        desktop: desktopReducer(state.desktop, { type: 'connection/status', status: action.status, error: action.error }),
        status: action.status === 'stale' ? 'stale' : action.status === 'error' ? 'error' : action.status === 'ready' ? 'ready' : state.status,
        staleReason: action.status === 'stale' ? state.staleReason : null,
      })
    case 'stale':
      return staleReducerResult(state, action, action.reason)
    default:
      return unchangedResult(state)
  }
}

export function applySessionV3Snapshot(
  state: SessionV3ReducerState,
  snapshot: DesktopDaemonSnapshot,
  action: (Extract<SessionV3ReducerAction, { type: 'snapshot' | 'snapshot-result' }> | { type: 'snapshot'; mode?: SessionV3ReducerSnapshotMode; receivedAt?: number }) & { endpointCursor?: string | null },
): SessionV3ReducerResult {
  const reducerAction = action.mode === 'merge'
    ? { type: 'snapshot/merge' as const, snapshot }
    : action.mode === 'reconcile'
      ? { type: 'snapshot/reconcile' as const, snapshot }
      : { type: 'snapshot/replace' as const, snapshot }
  const desktop = desktopReducer(state.desktop, reducerAction)
  const endpointCursor = normalizeOptionalString(action.endpointCursor) || normalizeOptionalString(snapshot.snapshotEndpointCursor) || state.endpointCursor
  return applyReducerDraft(state, action, {
    desktop,
    endpointCursor,
    status: desktop.status === 'stale' ? 'stale' : 'ready',
    staleReason: desktop.status === 'stale' ? desktop.staleReason : null,
  })
}

export function applySessionV3Reconnect(
  state: SessionV3ReducerState,
  result: SessionV3ReconnectSnapshot,
  action: Extract<SessionV3ReducerAction, { type: 'reconnect' }>,
): SessionV3ReducerResult {
  const snapshotResult = applySessionV3Snapshot(state, result.snapshot, {
    type: 'snapshot',
    mode: action.mode ?? 'merge',
    endpointCursor: result.endpointCursor,
    receivedAt: action.receivedAt,
  })
  const receivedAt = normalizeReceivedAt(action.receivedAt)
  const subscriptionsBySessionId = { ...snapshotResult.state.subscriptionsBySessionId }
  for (const subscription of result.subscriptions) {
    const sessionId = normalizeOptionalString(subscription.session_id)
    const subscriptionId = normalizeOptionalString(subscription.subscription_id)
    if (!sessionId || !subscriptionId) continue
    subscriptionsBySessionId[sessionId] = {
      sessionId,
      subscriptionId,
      endpointCursor: normalizeOptionalString(subscription.endpoint_cursor) || result.endpointCursor || snapshotResult.state.endpointCursor,
      worksetIds: subscriptionsBySessionId[sessionId]?.worksetIds ?? [],
      autoSubscribed: subscriptionsBySessionId[sessionId]?.autoSubscribed ?? false,
      updatedAt: receivedAt,
    }
  }
  const worksetsById = { ...snapshotResult.state.worksetsById }
  for (const workset of result.worksets) {
    const worksetId = normalizeOptionalString(workset.workset_id)
    const subscriptionId = normalizeOptionalString(workset.subscription_id)
    if (!worksetId || !subscriptionId) continue
    worksetsById[worksetId] = {
      worksetId,
      subscriptionId,
      sessionIds: worksetsById[worksetId]?.sessionIds ?? [],
      removedSessionIds: worksetsById[worksetId]?.removedSessionIds ?? [],
      autoSubscribeSessions: Boolean(workset.auto_subscribe_sessions),
      updatedAt: receivedAt,
    }
  }
  return applyReducerDraft(snapshotResult.state, action, { subscriptionsBySessionId, worksetsById })
}

export function applySessionV3RealtimeFrame(
  state: SessionV3ReducerState,
  frame: SessionV3RealtimeFrameWire,
  action: Extract<SessionV3ReducerAction, { type: 'frame' }>,
): SessionV3ReducerResult {
  const validation = validateRealtimeFrame(frame)
  if (!validation.ok) {
    return staleReducerResult(state, action, validation.reason)
  }

  const frameId = realtimeFrameIdentity(frame)
  if (state.appliedFrameIds[frameId]) {
    return duplicateResult(state, action, frame)
  }

  const kind = frameKind(frame)
  switch (kind) {
    case 'event':
      return applyRealtimeEventFrame(state, frame, action, frameId)
    case 'workset.session.discovered':
      return applyWorksetSessionFrame(state, frame, action, frameId, 'discovered')
    case 'workset.session.removed':
      return applyWorksetSessionFrame(state, frame, action, frameId, 'removed')
    case 'subscribe.session':
      return applyReducerDraft(state, action, {
        endpointCursor: frameEndpointCursor(frame) || state.endpointCursor,
        subscriptionsBySessionId: upsertSubscription(state.subscriptionsBySessionId, frame, action.receivedAt),
        rememberFrameId: frameId,
      })
    case 'cursor.error':
    case 'auth.denied':
    case 'slow_consumer.reconnect_required':
      return staleReducerResult(state, action, frame.reason || frame.error || frame.error_code || `V3 realtime ${kind}`, frameId)
    case 'endpoint.watermark':
    case 'projection.high_watermark':
    case 'hello':
    case 'keepalive':
    case 'replay.started':
    case 'replay.complete':
    case 'resume':
    case 'unsubscribe.session':
      return applyReducerDraft(state, action, {
        endpointCursor: frameEndpointCursor(frame) || state.endpointCursor,
        rememberFrameId: frameId,
      })
    default:
      return staleReducerResult(state, action, `unsupported V3 realtime frame kind: ${kind}`, frameId)
  }
}

export function applySessionV3MutationResult(
  state: SessionV3ReducerState,
  response: SessionV3HydratedSessionResponseWire,
  action: Extract<SessionV3ReducerAction, { type: 'mutation' }>,
): SessionV3ReducerResult {
  const endpointCursor = mutationEndpointCursor(response) || state.endpointCursor
  const snapshot = mutationSnapshot(state.desktop.rev, response, action.sessionId)
  if (!snapshot) {
    return applyReducerDraft(state, action, { endpointCursor })
  }
  const desktop = desktopReducer(state.desktop, { type: 'snapshot/merge', snapshot })
  return applyReducerDraft(state, action, {
    desktop,
    endpointCursor,
    status: desktop.status === 'stale' ? 'stale' : 'ready',
    staleReason: desktop.status === 'stale' ? desktop.staleReason : null,
  })
}

function applyRealtimeEventFrame(
  state: SessionV3ReducerState,
  frame: SessionV3RealtimeFrameWire,
  action: Extract<SessionV3ReducerAction, { type: 'frame' }>,
  frameId: string,
): SessionV3ReducerResult {
  const event = materializeRealtimeEvent(frame, state.desktop.rev)
  const desktop = event.rev <= state.desktop.rev
    ? state.desktop
    : desktopReducer(state.desktop, { type: 'daemon/event', event })
  const endpointCursor = frameEndpointCursor(frame) || state.endpointCursor
  return applyReducerDraft(state, action, {
    desktop,
    endpointCursor,
    subscriptionsBySessionId: upsertSubscription(state.subscriptionsBySessionId, frame, action.receivedAt),
    status: desktop.status === 'stale' ? 'stale' : 'ready',
    staleReason: desktop.status === 'stale' ? desktop.staleReason : null,
    rememberFrameId: frameId,
  })
}

function applyWorksetSessionFrame(
  state: SessionV3ReducerState,
  frame: SessionV3RealtimeFrameWire,
  action: Extract<SessionV3ReducerAction, { type: 'frame' }>,
  frameId: string,
  mode: 'discovered' | 'removed',
): SessionV3ReducerResult {
  const receivedAt = normalizeReceivedAt(action.receivedAt)
  const sessionId = frameSessionId(frame)
  const worksetId = normalizeOptionalString(frame.workset_id)
  const worksetSubscriptionId = normalizeOptionalString(frame.workset_subscription_id)
  const endpointCursor = frameEndpointCursor(frame) || state.endpointCursor
  const current = state.worksetsById[worksetId]
  const currentSessionIds = current?.sessionIds ?? []
  const currentRemovedSessionIds = current?.removedSessionIds ?? []
  const worksetsById = worksetId
    ? {
        ...state.worksetsById,
        [worksetId]: {
          worksetId,
          subscriptionId: worksetSubscriptionId || current?.subscriptionId || '',
          sessionIds: mode === 'discovered'
            ? addUnique(currentSessionIds, sessionId)
            : currentSessionIds.filter((candidate) => candidate !== sessionId),
          removedSessionIds: mode === 'removed'
            ? addUnique(currentRemovedSessionIds, sessionId)
            : currentRemovedSessionIds.filter((candidate) => candidate !== sessionId),
          autoSubscribeSessions: current?.autoSubscribeSessions ?? Boolean(frame.auto_subscribed),
          updatedAt: receivedAt,
        },
      }
    : state.worksetsById
  return applyReducerDraft(state, action, {
    endpointCursor,
    subscriptionsBySessionId: upsertSubscription(state.subscriptionsBySessionId, frame, action.receivedAt),
    worksetsById,
    discoveredSessionIds: mode === 'discovered' ? addUnique(state.discoveredSessionIds, sessionId) : state.discoveredSessionIds.filter((candidate) => candidate !== sessionId),
    removedSessionIds: mode === 'removed' ? addUnique(state.removedSessionIds, sessionId) : state.removedSessionIds.filter((candidate) => candidate !== sessionId),
    rememberFrameId: frameId,
  })
}

function applyReducerDraft(state: SessionV3ReducerState, action: SessionV3ReducerActionLike, draft: ReducerDraft): SessionV3ReducerResult {
  const desktop = draft.desktop ?? state.desktop
  const endpointCursor = draft.endpointCursor ?? state.endpointCursor
  const status = draft.status ?? (desktop.status === 'stale' ? 'stale' : state.status)
  const staleReason = draft.staleReason ?? (status === 'stale' ? desktop.staleReason ?? state.staleReason : null)
  const remembered = draft.rememberFrameId ? rememberFrame(state, draft.rememberFrameId) : null
  const nextStateBase: SessionV3ReducerState = {
    ...state,
    desktop,
    endpointCursor,
    status,
    staleReason,
    subscriptionsBySessionId: draft.subscriptionsBySessionId ?? state.subscriptionsBySessionId,
    worksetsById: draft.worksetsById ?? state.worksetsById,
    discoveredSessionIds: draft.discoveredSessionIds ?? state.discoveredSessionIds,
    removedSessionIds: draft.removedSessionIds ?? state.removedSessionIds,
    appliedFrameIds: remembered?.appliedFrameIds ?? state.appliedFrameIds,
    appliedFrameOrder: remembered?.appliedFrameOrder ?? state.appliedFrameOrder,
  }
  const desktopChanged = !Object.is(desktop, state.desktop)
  const cursorAdvanced = endpointCursor !== state.endpointCursor
  const changed = desktopChanged
    || cursorAdvanced
    || status !== state.status
    || staleReason !== state.staleReason
    || !Object.is(nextStateBase.subscriptionsBySessionId, state.subscriptionsBySessionId)
    || !Object.is(nextStateBase.worksetsById, state.worksetsById)
    || !Object.is(nextStateBase.discoveredSessionIds, state.discoveredSessionIds)
    || !Object.is(nextStateBase.removedSessionIds, state.removedSessionIds)
    || Boolean(remembered)
  if (!changed) {
    return unchangedResult(state)
  }
  const mutationSeq = state.mutationSeq + 1
  const stale = status === 'stale' || desktop.status === 'stale'
  const summary = applySummary(action, nextStateBase, {
    applied: !stale && (desktopChanged || cursorAdvanced),
    desktopChanged,
    cursorAdvanced,
    duplicate: false,
    rejected: stale,
    stale,
    reason: stale ? staleReason ?? desktop.staleReason ?? 'V3 reducer state is stale' : null,
    mutationSeq,
  })
  const nextState = {
    ...nextStateBase,
    mutationSeq,
    lastApply: summary,
  }
  return {
    state: nextState,
    applied: summary.applied,
    desktopChanged,
    cursorAdvanced,
    duplicate: false,
    rejected: summary.rejected,
    stale,
    reason: summary.reason,
  }
}

function staleReducerResult(state: SessionV3ReducerState, action: SessionV3ReducerActionLike, reason: string, rememberFrameId?: string): SessionV3ReducerResult {
  const desktop = desktopReducer(state.desktop, { type: 'connection/stale', reason })
  return applyReducerDraft(state, action, {
    desktop,
    status: 'stale',
    staleReason: reason,
    rememberFrameId,
  })
}

function duplicateResult(state: SessionV3ReducerState, action: SessionV3ReducerActionLike, frame: SessionV3RealtimeFrameWire): SessionV3ReducerResult {
  const mutationSeq = state.mutationSeq + 1
  const summary = applySummary(action, state, {
    applied: false,
    desktopChanged: false,
    cursorAdvanced: false,
    duplicate: true,
    rejected: false,
    stale: false,
    reason: 'duplicate V3 realtime frame',
    mutationSeq,
  })
  return {
    state: {
      ...state,
      mutationSeq,
      lastApply: {
        ...summary,
        sessionId: frameSessionId(frame) || null,
        frameKind: frameKind(frame) || null,
      },
    },
    applied: false,
    desktopChanged: false,
    cursorAdvanced: false,
    duplicate: true,
    rejected: false,
    stale: false,
    reason: 'duplicate V3 realtime frame',
  }
}

function unchangedResult(state: SessionV3ReducerState): SessionV3ReducerResult {
  return {
    state,
    applied: false,
    desktopChanged: false,
    cursorAdvanced: false,
    duplicate: false,
    rejected: false,
    stale: false,
    reason: null,
  }
}

function applySummary(
  action: SessionV3ReducerActionLike,
  state: SessionV3ReducerState,
  result: Omit<SessionV3ReducerApplySummary, 'action' | 'endpointCursor' | 'sessionId' | 'frameKind' | 'receivedAt'>,
): SessionV3ReducerApplySummary {
  const frame = action.type === 'frame' ? action.frame : null
  const sessionId = frame ? frameSessionId(frame) : action.type === 'mutation' ? normalizeOptionalString(action.sessionId) : ''
  return {
    action: action.type,
    applied: result.applied,
    desktopChanged: result.desktopChanged,
    cursorAdvanced: result.cursorAdvanced,
    duplicate: result.duplicate,
    rejected: result.rejected,
    stale: result.stale,
    endpointCursor: state.endpointCursor,
    sessionId: sessionId || null,
    frameKind: frame ? frameKind(frame) || null : null,
    reason: result.reason,
    mutationSeq: result.mutationSeq,
    receivedAt: normalizeReceivedAt(action.receivedAt),
  }
}

function rememberFrame(state: SessionV3ReducerState, frameId: string): Pick<SessionV3ReducerState, 'appliedFrameIds' | 'appliedFrameOrder'> {
  const normalizedFrameId = frameId.trim()
  if (!normalizedFrameId) {
    return { appliedFrameIds: state.appliedFrameIds, appliedFrameOrder: state.appliedFrameOrder }
  }
  const order = [...state.appliedFrameOrder, normalizedFrameId]
  const ids: Record<string, true> = { ...state.appliedFrameIds, [normalizedFrameId]: true }
  while (order.length > MAX_APPLIED_FRAME_IDS) {
    const evicted = order.shift()
    if (evicted) delete ids[evicted]
  }
  return { appliedFrameIds: ids, appliedFrameOrder: order }
}

function validateRealtimeFrame(frame: SessionV3RealtimeFrameWire): { ok: true } | { ok: false; reason: string } {
  if (normalizeOptionalString(frame.protocol) && frame.protocol !== 'v3.realtime') {
    return { ok: false, reason: `unsupported V3 realtime protocol: ${String(frame.protocol)}` }
  }
  if (frame.protocol_version !== undefined && frame.protocol_version !== 1) {
    return { ok: false, reason: `unsupported V3 realtime protocol_version: ${String(frame.protocol_version)}` }
  }
  const kind = frameKind(frame)
  if (!kind) return { ok: false, reason: 'V3 realtime frame missing kind' }
  if (kind === 'event' && !frameSessionId(frame)) return { ok: false, reason: 'V3 realtime event missing session_id' }
  if ((kind === 'event' || kind === 'workset.session.discovered' || kind === 'workset.session.removed') && !frameEndpointCursor(frame)) {
    return { ok: false, reason: `V3 realtime ${kind} missing endpoint_cursor` }
  }
  return { ok: true }
}

function materializeRealtimeEvent(frame: SessionV3RealtimeFrameWire, currentRev: number): DesktopDaemonEvent {
  const event = frame.event ?? {}
  const eventType = normalizeOptionalString(frame.event_type) || normalizeOptionalString(event.event_type) || 'event'
  const sourceSeq = positiveInteger(frame.source_seq) ?? positiveInteger(event.seq)
  const globalSeq = positiveInteger(frame.global_seq) ?? sourceSeq
  const payload = normalizeRealtimeEventPayload(frame, eventType, { globalSeq, sourceSeq })
  return {
    rev: positiveInteger(frame.rev) ?? currentRev + 1,
    prevRev: nonNegativeInteger(frame.prevRev) ?? currentRev,
    type: eventType,
    payload,
    stream: normalizeOptionalString(frame.stream) || `v3/session:${frameSessionId(frame)}`,
    entityId: normalizeOptionalString(frame.entity_id) || frameSessionId(frame),
    globalSeq,
    sourceSeq,
    tsUnixMs: nonNegativeInteger(frame.ts_unix_ms) ?? nonNegativeInteger(event.ts_unix_ms),
  }
}

function normalizeRealtimeEventPayload(
  frame: SessionV3RealtimeFrameWire,
  eventType: string,
  sequence: { globalSeq?: number; sourceSeq?: number },
): Record<string, unknown> {
  const event = frame.event ?? {}
  const payload = isRecord(event.payload) ? event.payload : isRecord(frame.payload) ? frame.payload : {}
  return {
    ...payload,
    session_id: payload.session_id ?? event.session_id ?? frame.session_id,
    event_type: stringValue(payload.event_type) || eventType,
    global_seq: payload.global_seq ?? sequence.globalSeq,
    source_seq: payload.source_seq ?? sequence.sourceSeq,
    ts_unix_ms: payload.ts_unix_ms ?? event.ts_unix_ms ?? frame.ts_unix_ms,
  }
}

function mutationSnapshot(
  currentRev: number,
  response: SessionV3HydratedSessionResponseWire,
  fallbackSessionId: string | null | undefined,
): DesktopDaemonSnapshot | null {
  const normalizedSessionId = normalizeOptionalString(fallbackSessionId)
    || normalizeOptionalString(response.session?.id)
    || normalizeOptionalString(response.projection?.session_id)
    || normalizeOptionalString(response.active_run_intent?.session_id)
    || normalizeOptionalString(response.run_intent?.session_id)
  const sessionsById: Record<string, DesktopSessionRecord> = {}
  const session = response.session ? sessionFromWire(response.session, response.projection) : null
  if (session?.id) {
    sessionsById[session.id] = session
  }
  const messages = normalizeMessages(response.messages, normalizedSessionId)
  const permissions = normalizePermissions(response.pending_permissions, normalizedSessionId)
  const usage = usageFromWire(response.usage_summary, normalizedSessionId)
  const runIntent = runIntentFromWire(response.active_run_intent ?? response.run_intent, normalizedSessionId)
  const hasSnapshot = Object.keys(sessionsById).length > 0
    || messages.length > 0
    || Object.keys(permissions).length > 0
    || Boolean(usage)
    || Boolean(runIntent)
  if (!hasSnapshot) {
    return null
  }
  const sessionId = session?.id || normalizedSessionId
  return {
    rev: Math.max(0, currentRev),
    sessionsById: Object.keys(sessionsById).length > 0 ? sessionsById : undefined,
    sessionOrder: Object.keys(sessionsById).length > 0 ? Object.keys(sessionsById) : undefined,
    messagesBySessionId: sessionId && messages.length > 0 ? { [sessionId]: messages } : undefined,
    permissionsById: Object.keys(permissions).length > 0 ? permissions : undefined,
    usageBySessionId: usage ? { [usage.sessionId || sessionId]: usage } : undefined,
    runIntentsBySessionId: runIntent ? { [runIntent.sessionId]: runIntent } : undefined,
  }
}

function sessionFromWire(session: SessionV3SessionWire, projection: SessionV3ProjectionWire | null | undefined): DesktopSessionRecord {
  const mapped = mapSessionWire(session)
  return {
    ...mapped,
    lastEventSeq: numberValue(projection?.last_event_seq, mapped.lastEventSeq ?? 0),
    projectionHighWatermarkSeq: numberValue(projection?.projection_high_watermark_seq, mapped.projectionHighWatermarkSeq ?? mapped.lastEventSeq ?? 0),
  }
}

function mapSessionWire(session: SessionV3SessionWire): DesktopSessionRecord {
  const id = normalizeOptionalString(session.id)
  const runIntent = runIntentFromWire(session.run_intent, id)
  const lifecycle = session.lifecycle
    ? {
        sessionId: normalizeOptionalString(session.lifecycle.session_id) || id,
        runId: normalizeOptionalString(session.lifecycle.run_id) || null,
        active: Boolean(session.lifecycle.active),
        phase: normalizeOptionalString(session.lifecycle.phase),
        startedAt: numberValue(session.lifecycle.started_at),
        endedAt: numberValue(session.lifecycle.ended_at),
        updatedAt: numberValue(session.lifecycle.updated_at),
        generation: numberValue(session.lifecycle.generation),
        stopReason: normalizeOptionalString(session.lifecycle.stop_reason) || null,
        error: normalizeOptionalString(session.lifecycle.error) || null,
        ownerTransport: normalizeOptionalString(session.lifecycle.owner_transport) || null,
      }
    : null
  return {
    id,
    title: normalizeOptionalString(session.title),
    workspacePath: normalizeOptionalString(session.workspace_path),
    workspaceName: normalizeOptionalString(session.workspace_name),
    mode: normalizeOptionalString(session.mode) || 'auto',
    metadata: isRecord(session.metadata) ? session.metadata : undefined,
    sessionApi: normalizeOptionalString(session.session_api) || 'v3',
    lastEventSeq: numberValue(session.last_event_seq),
    projectionHighWatermarkSeq: numberValue(session.projection_high_watermark_seq),
    messageCount: numberValue(session.message_count),
    updatedAt: numberValue(session.updated_at),
    createdAt: numberValue(session.created_at),
    permissionsHydrated: false,
    worktreeEnabled: Boolean(session.worktree_enabled),
    worktreeRootPath: normalizeOptionalString(session.worktree_root_path),
    worktreeBaseBranch: normalizeOptionalString(session.worktree_base_branch),
    worktreeBranch: normalizeOptionalString(session.worktree_branch),
    gitBranch: normalizeOptionalString(session.git_branch),
    gitHasGit: Boolean(session.git_has_git),
    gitClean: Boolean(session.git_clean),
    gitDirtyCount: numberValue(session.git_dirty_count),
    gitStagedCount: numberValue(session.git_staged_count),
    gitModifiedCount: numberValue(session.git_modified_count),
    gitUntrackedCount: numberValue(session.git_untracked_count),
    gitConflictCount: numberValue(session.git_conflict_count),
    gitAheadCount: numberValue(session.git_ahead_count),
    gitBehindCount: numberValue(session.git_behind_count),
    gitCommitDetected: Boolean(session.git_commit_detected),
    gitCommitCount: numberValue(session.git_commit_count),
    gitCommittedFileCount: numberValue(session.git_committed_file_count),
    gitCommittedAdditions: numberValue(session.git_committed_additions),
    gitCommittedDeletions: numberValue(session.git_committed_deletions),
    lifecycle,
    runIntent,
    live: emptyLiveState(runIntent, lifecycle),
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

function emptyLiveState(
  runIntent: DesktopRunIntentRecord | null,
  lifecycle: DesktopSessionRecord['lifecycle'],
): DesktopSessionRecord['live'] {
  const runId = lifecycle?.runId || runIntent?.runId || null
  const active = Boolean(lifecycle?.active || runIntent)
  return {
    runId,
    terminalRunId: null,
    terminalEventSeq: 0,
    agentName: null,
    startedAt: lifecycle?.startedAt || runIntent?.createdAt || null,
    status: active ? 'running' : 'idle',
    step: 0,
    toolName: null,
    sidebarToolName: null,
    toolCallId: null,
    toolArguments: null,
    toolOutput: '',
    retainedToolName: null,
    retainedToolCallId: null,
    retainedToolArguments: null,
    retainedToolOutput: '',
    retainedToolState: null,
    toolHistory: [],
    summary: active ? 'Assistant responding…' : null,
    lastEventType: active ? 'session.lifecycle.updated' : null,
    lastEventAt: lifecycle?.updatedAt || runIntent?.updatedAt || null,
    error: null,
    seq: Math.max(runIntent?.eventSeq ?? 0, lifecycle?.generation ?? 0),
    assistantDraft: '',
    retainedAssistantSegments: [],
    reasoningSummary: '',
    reasoningText: '',
    reasoningState: 'idle',
    reasoningSegment: 0,
    reasoningStartedAt: null,
    reasoningCompletedAt: null,
    reasoningTimelineSeq: 0,
    reasoningHistory: [],
    awaitingAck: false,
  }
}

function normalizeMessages(messages: SessionV3MessageWire[] | undefined, fallbackSessionId: string): ChatMessageRecord[] {
  return (messages ?? [])
    .map((message) => messageFromWire(message, fallbackSessionId))
    .filter((message): message is ChatMessageRecord => Boolean(message && message.id && message.sessionId))
}

function messageFromWire(message: SessionV3MessageWire, fallbackSessionId: string): ChatMessageRecord | null {
  const content = String(message.content ?? '')
  const sessionId = normalizeOptionalString(message.session_id) || fallbackSessionId
  const id = normalizeOptionalString(message.id) || (sessionId && message.global_seq ? `${sessionId}:${message.global_seq}` : '')
  if (!id || !sessionId) return null
  return {
    id,
    sessionId,
    globalSeq: numberValue(message.global_seq),
    role: normalizeOptionalString(message.role),
    content,
    createdAt: numberValue(message.created_at),
    metadata: message.metadata,
    toolMessage: parseStructuredToolMessage(content),
  }
}

function usageFromWire(source: unknown, fallbackSessionId: string): DesktopSessionUsageRecord | null {
  if (!isRecord(source)) return null
  const sessionId = normalizeOptionalString(source.session_id) || fallbackSessionId
  const contextWindow = numberValue(source.context_window)
  const totalTokens = numberValue(source.total_tokens)
  const remainingTokens = numberValue(source.remaining_tokens)
  const updatedAt = numberValue(source.updated_at)
  if (!sessionId && contextWindow <= 0 && totalTokens <= 0 && remainingTokens <= 0 && updatedAt <= 0) {
    return null
  }
  return {
    sessionId,
    provider: normalizeOptionalString(source.provider),
    model: normalizeOptionalString(source.model),
    source: normalizeOptionalString(source.source),
    contextWindow,
    totalTokens,
    remainingTokens,
    updatedAt,
  }
}

function normalizePermissions(permissions: unknown[] | undefined, fallbackSessionId: string): Record<string, DesktopPermissionRecord> {
  const output: Record<string, DesktopPermissionRecord> = {}
  for (const source of permissions ?? []) {
    const permission = permissionFromWire(source)
    if (!permission.id || permission.status.trim().toLowerCase() !== 'pending') continue
    if (fallbackSessionId && permission.sessionId !== fallbackSessionId) continue
    output[permission.id] = permission
  }
  return output
}

function permissionFromWire(source: unknown): DesktopPermissionRecord {
  const permission = isRecord(source) ? source : {}
  return {
    id: normalizeOptionalString(permission.id),
    sessionId: normalizeOptionalString(permission.session_id),
    runId: normalizeOptionalString(permission.run_id),
    callId: normalizeOptionalString(permission.call_id),
    toolName: normalizeOptionalString(permission.tool_name),
    toolArguments: normalizeOptionalString(permission.tool_arguments),
    approvedArguments: normalizeOptionalString(permission.approved_arguments) || undefined,
    status: normalizeOptionalString(permission.status),
    decision: normalizeOptionalString(permission.decision),
    reason: normalizeOptionalString(permission.reason),
    requirement: normalizeOptionalString(permission.requirement),
    mode: normalizeOptionalString(permission.mode),
    createdAt: numberValue(permission.created_at),
    updatedAt: numberValue(permission.updated_at),
    resolvedAt: numberValue(permission.resolved_at),
    permissionRequestedAt: numberValue(permission.permission_requested_at),
  }
}

function runIntentFromWire(source: SessionV3RunIntentWire | null | undefined, fallbackSessionId: string): DesktopRunIntentRecord | null {
  if (!source) return null
  const sessionId = normalizeOptionalString(source.session_id) || fallbackSessionId
  const runId = normalizeOptionalString(source.run_id)
  const status = normalizeOptionalString(source.status)
  if (!sessionId || !runId || !status) return null
  return {
    sessionId,
    runId,
    status,
    blockedReason: normalizeOptionalString(source.blocked_reason),
    createdAt: numberValue(source.created_at),
    updatedAt: numberValue(source.updated_at),
    eventSeq: numberValue(source.event_seq),
  }
}

function mutationEndpointCursor(response: SessionV3HydratedSessionResponseWire): string {
  return realtimeOutboxCursor(response.realtime_outbox) || realtimeOutboxCursor(response.mutation?.realtime_outbox)
}

function realtimeOutboxCursor(outbox: SessionV3RealtimeOutboxWire | null | undefined): string {
  return normalizeOptionalString(outbox?.endpoint_cursor)
}

function upsertSubscription(
  current: Record<string, SessionV3ReducerSubscriptionState>,
  frame: SessionV3RealtimeFrameWire,
  receivedAt: number | undefined,
): Record<string, SessionV3ReducerSubscriptionState> {
  const sessionId = frameSessionId(frame)
  const subscriptionId = normalizeOptionalString(frame.subscription_id)
  if (!sessionId || !subscriptionId) {
    return current
  }
  const worksetId = normalizeOptionalString(frame.workset_id)
  const existing = current[sessionId]
  return {
    ...current,
    [sessionId]: {
      sessionId,
      subscriptionId,
      endpointCursor: frameEndpointCursor(frame) || existing?.endpointCursor || '',
      worksetIds: worksetId ? addUnique(existing?.worksetIds ?? [], worksetId) : existing?.worksetIds ?? [],
      autoSubscribed: Boolean(frame.auto_subscribed) || existing?.autoSubscribed || false,
      updatedAt: normalizeReceivedAt(receivedAt),
    },
  }
}

function realtimeFrameIdentity(frame: SessionV3RealtimeFrameWire): string {
  const eventId = normalizeOptionalString(frame.event?.id)
  const cursor = frameEndpointCursor(frame)
  const sessionId = frameSessionId(frame) || 'global'
  const sequence = positiveInteger(frame.rev)
    ?? positiveInteger(frame.event?.seq)
    ?? positiveInteger(frame.last_seq)
    ?? positiveInteger(frame.high_watermark_seq)
  if (eventId) return `event:${eventId}`
  if (cursor) return `cursor:${cursor}:${frameKind(frame)}:${sessionId}`
  return `${frameKind(frame)}:${sessionId}:${sequence ?? normalizeOptionalString(frame.subscription_id)}`
}

function frameKind(frame: SessionV3RealtimeFrameWire): string {
  return normalizeOptionalString(frame.kind ?? frame.type)
}

function frameSessionId(frame: SessionV3RealtimeFrameWire): string {
  return normalizeOptionalString(frame.session_id) || normalizeOptionalString(frame.event?.session_id)
}

function frameEndpointCursor(frame: SessionV3RealtimeFrameWire): string {
  return normalizeOptionalString(frame.endpoint_cursor)
}

function addUnique(values: string[], value: string): string[] {
  const normalized = value.trim()
  if (!normalized || values.includes(normalized)) return values
  return [...values, normalized]
}

function normalizeReceivedAt(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.floor(value) : 0
}

function normalizeOptionalString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function numberValue(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function positiveInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : undefined
}

function nonNegativeInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.floor(value) : undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}
