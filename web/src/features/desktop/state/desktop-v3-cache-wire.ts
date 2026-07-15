import type {
  CacheEvent,
  DesktopV3CacheAction,
  RealtimeMessage,
  SessionCreateMutationResponse,
  SessionEventPayload,
  SessionMessageMutationResponse,
  SessionMutationErrorResponse,
  MessageMutationConflictResponse,
  SessionsReconnectResponse,
  SyncSnapshotResponse,
  SyncStreamEvent,
  SyncStreamResponse,
  V3RealtimeOutboxRecord,
  V3SessionEvent,
} from './desktop-v3-cache-types'

export function bootstrapResponseToAction(raw: SyncSnapshotResponse): DesktopV3CacheAction {
  return {
    type: 'snapshot.apply',
    source: 'bootstrap',
    scopeId: raw.scope_id,
    snapshot: raw,
  }
}

export function selectSession(sessionId?: string): DesktopV3CacheAction {
  return {
    type: 'session.select',
    sessionId: sessionId?.trim() || undefined,
  }
}

export function hydrateResponseToAction(
  raw: SyncSnapshotResponse,
  requestedSessionIds: string[],
): DesktopV3CacheAction {
  return {
    type: 'hydrate.apply',
    source: 'hydrate',
    scopeId: raw.scope_id,
    requestedSessionIds,
    snapshot: raw,
  }
}

export function syncStreamResponseToAction(
  raw: SyncStreamResponse,
  scopeId: string,
): DesktopV3CacheAction {
  return {
    type: 'syncStream.applyBatch',
    scopeId,
    endpointCursor: raw.endpoint_cursor,
    events: raw.events.map(normalizeSyncStreamEvent),
    hasMore: raw.has_more,
    replayInstructions: raw.replay_instructions,
  }
}

export function reconnectResponseToActions(raw: SessionsReconnectResponse): DesktopV3CacheAction[] {
  const actions: DesktopV3CacheAction[] = [
    {
      type: 'reconnect.applySnapshot',
      snapshot: raw,
    },
  ]

  if (raw.realtime?.resume) {
    actions.push({
      type: 'realtime.storeResume',
      streamPath: raw.realtime.stream_path,
      resume: raw.realtime.resume,
    })
  }

  return actions
}

export function realtimeFrameToActions(frame: RealtimeMessage): DesktopV3CacheAction[] {
  switch (frame.kind) {
    case 'event':
      return [{ type: 'realtime.applyEvent', event: normalizeRealtimeEventFrame(frame), endpointCursor: frame.endpoint_cursor }]

    case 'notification.resource.updated':
      return [{ type: 'realtime.applyNotificationResource', frame }]

    case 'workset.session.discovered':
      return worksetFrameToActions(frame, { type: 'realtime.worksetSessionDiscovered', frame })

    case 'workset.session.updated':
      return worksetFrameToActions(frame, { type: 'realtime.worksetSessionUpdated', frame })

    case 'workset.session.removed':
      return worksetFrameToActions(frame, { type: 'realtime.worksetSessionRemoved', frame })

    case 'cursor.error':
      return [{ type: 'realtime.cursorError', frame }]

    case 'hello':
    case 'keepalive':
    case 'endpoint.watermark':
    case 'projection.high_watermark':
    case 'replay.started':
    case 'replay.complete':
    case 'auth.denied':
    case 'slow_consumer.reconnect_required':
      return [{ type: 'realtime.control', frame }]

    default:
      return [{ type: 'realtime.unknownFrame', frame }]
  }
}

function worksetFrameToActions(frame: RealtimeMessage, membershipAction: DesktopV3CacheAction): DesktopV3CacheAction[] {
  const actions: DesktopV3CacheAction[] = []
  if (frame.event) {
    actions.push({
      type: 'realtime.applyEvent',
      event: normalizeRealtimeEventFrame(frame),
      endpointCursor: frame.endpoint_cursor,
    })
  }
  actions.push(membershipAction)
  return actions
}

export function sessionCreateResponseToAction(
  raw: SessionCreateMutationResponse | SessionMutationErrorResponse,
  sidebarScopeId: string,
): DesktopV3CacheAction {
  return {
    type: 'mutation.sessionCreateResult',
    raw,
    sidebarScopeId,
  }
}

export function messageMutationResponseToAction(
  raw: SessionMessageMutationResponse | MessageMutationConflictResponse,
  clientRequestId: string,
  messageId: string,
): DesktopV3CacheAction {
  return {
    type: 'mutation.messageResult',
    raw,
    clientRequestId,
    messageId,
  }
}

export function normalizeSyncStreamEvent(raw: SyncStreamEvent): CacheEvent {
  return {
    source: 'sync-stream',
    sessionId: raw.session_id,
    eventType: raw.event_type,
    sessionEvent: raw.event,
    projection: raw.projection,
    payload: decodeSessionEventPayload(raw.event),
    notification: raw.notification,
    notificationSummary: raw.notification_summary,
  }
}

export function normalizeRealtimeEventFrame(frame: RealtimeMessage): CacheEvent {
  const event = frame.event
  const sessionId = frame.session_id || event?.session_id || ''
  const eventType = frame.event_type || event?.event_type || ''
  return {
    source: 'realtime',
    sessionId,
    eventType,
    sessionEvent: event,
    projection: frame.projection,
    payload: event ? decodeSessionEventPayload(event) : {},
    notification: frame.notification,
    notificationSummary: frame.notification_summary,
  }
}

export function outboxRecordToCacheEvent(raw: V3RealtimeOutboxRecord): CacheEvent {
  return {
    source: 'outbox',
    sessionId: raw.session_id,
    eventType: raw.event.event_type,
    sessionEvent: raw.event,
    projection: raw.projection,
    payload: decodeSessionEventPayload(raw.event),
  }
}

export function decodeSessionEventPayload(event: V3SessionEvent): SessionEventPayload {
  let payload: SessionEventPayload = {}
  if (event.payload) {
    payload = typeof event.payload === 'string'
      ? JSON.parse(event.payload) as SessionEventPayload
      : event.payload as SessionEventPayload
  }

  const payloadEpochId = typeof payload.epoch_id === 'string' ? payload.epoch_id.trim() : ''
  const eventEpochId = event.epoch_id?.trim() || ''
  const epochId = event.execution_epoch?.epoch_id?.trim() || payloadEpochId || eventEpochId
  const payloadOrdinal = typeof payload.ordinal === 'number' ? payload.ordinal : undefined
  const payloadParentEpochId = typeof payload.parent_epoch_id === 'string' ? payload.parent_epoch_id : undefined
  const inferredEpoch = epochId
    ? {
        ...event.execution_epoch,
        epoch_id: epochId,
        epoch_ordinal: event.execution_epoch?.epoch_ordinal ?? payloadOrdinal,
        session_id: event.session_id,
      }
    : undefined
  const inferredBoundary = event.event_type === 'execution_epoch.began' && epochId
    ? {
        epoch_id: epochId,
        epoch_ordinal: inferredEpoch?.epoch_ordinal,
        kind: 'started',
        parent_epoch_id: payloadParentEpochId,
      }
    : undefined

  return {
    ...payload,
    execution_epoch: payload.execution_epoch ?? inferredEpoch,
    execution_epoch_boundary: payload.execution_epoch_boundary ?? event.execution_epoch_boundary ?? inferredBoundary,
  }
}
