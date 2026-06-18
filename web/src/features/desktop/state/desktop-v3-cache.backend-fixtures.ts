import type {
  MessageSnapshot,
  RealtimeMessage,
  SessionsReconnectResponse,
  SyncSnapshotResponse,
  SyncStreamResponse,
  V3RealtimeOutboxRecord,
  V3SessionEvent,
  V3SessionProjection,
  V3SessionRunIntent,
  V3SessionTombstone,
  SessionSnapshot,
  SessionMessageMutationResponse,
} from './desktop-v3-cache-types'

export const sessionA: SessionSnapshot = {
  id: 'session-a',
  workspace_path: '/repo',
  workspace_name: 'repo',
  title: 'Session A',
  mode: 'auto',
  created_at: 1,
  updated_at: 10,
  message_count: 2,
  last_message_at: 10,
}

export const sessionB: SessionSnapshot = {
  id: 'session-b',
  workspace_path: '/repo',
  workspace_name: 'repo',
  title: 'Session B',
  mode: 'plan',
  created_at: 2,
  updated_at: 20,
  message_count: 1,
  last_message_at: 20,
}

export const messageA1: MessageSnapshot = {
  id: 'msg-a-1',
  session_id: 'session-a',
  global_seq: 1,
  role: 'user',
  content: 'hello a',
  created_at: 3,
}

export const messageA2: MessageSnapshot = {
  id: 'msg-a-2',
  session_id: 'session-a',
  global_seq: 2,
  role: 'assistant',
  content: 'hi a',
  created_at: 4,
}

export const messageB1: MessageSnapshot = {
  id: 'msg-b-1',
  session_id: 'session-b',
  global_seq: 1,
  role: 'user',
  content: 'hello b',
  created_at: 5,
}

export const projectionA: V3SessionProjection = {
  session_id: 'session-a',
  last_event_seq: 2,
  projection_high_watermark_seq: 2,
  updated_at: 10,
}

export const projectionB: V3SessionProjection = {
  session_id: 'session-b',
  last_event_seq: 1,
  projection_high_watermark_seq: 1,
  updated_at: 20,
}

export const runIntentA: V3SessionRunIntent = {
  session_id: 'session-a',
  run_id: 'run-a',
  status: 'running',
  created_at: 10,
  updated_at: 10,
  event_seq: 2,
}

export const tombstoneB: V3SessionTombstone = {
  session_id: 'session-b',
  deleted: true,
  endpoint_seq: 9,
  event_seq: 3,
  updated_at: 21,
}

export function snapshotFixture(overrides: Partial<SyncSnapshotResponse> = {}): SyncSnapshotResponse {
  return {
    ok: true,
    rev: 1,
    snapshot_endpoint_cursor: 'cursor-bootstrap-1',
    sessions_by_id: {
      [sessionA.id]: sessionA,
      [sessionB.id]: sessionB,
    },
    projections_by_session: {
      [sessionA.id]: projectionA,
      [sessionB.id]: projectionB,
    },
    messages_by_session: {
      [sessionA.id]: [messageA1, messageA2],
    },
    events_by_session: {},
    plans_by_session: {},
    plan_revisions_by_session: {},
    permissions_by_session: {},
    usage_by_session: {},
    preferences_by_session: {},
    agent_model_policy_by_session: {},
    run_intents_by_session: {
      [sessionA.id]: [runIntentA],
    },
    history_manifests_by_session: {},
    history_chunks_by_id: {},
    omissions: [],
    pagination: null,
    watermarks: null,
    session_order: [sessionA.id, sessionB.id],
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'messages,run_intents',
    },
    scope_id: 'selector-hash:messages,run_intents',
    selector: {
      kind: 'workspace',
      workspace_path: '/repo',
      recent: { limit: 50 },
    },
    known_sessions: {},
    tombstones_by_session: {},
    replay_instructions: {
      stream_path: '/v3/sync/stream',
      transport: 'http_post',
      after_endpoint_cursor: 'cursor-bootstrap-1',
      bootstrap_required_on_cursor_error: true,
    },
    ...overrides,
  }
}

export function hydrateSnapshotFixture(overrides: Partial<SyncSnapshotResponse> = {}): SyncSnapshotResponse {
  return snapshotFixture({
    snapshot_endpoint_cursor: 'cursor-hydrate-b',
    sessions_by_id: { [sessionB.id]: sessionB },
    projections_by_session: { [sessionB.id]: projectionB },
    messages_by_session: { [sessionB.id]: [messageB1] },
    run_intents_by_session: {},
    session_order: [sessionB.id],
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-b-hash',
      resource_set: 'messages',
    },
    scope_id: 'session-b-hash:messages',
    selector: { kind: 'session_ids', session_ids: [sessionB.id] },
    ...overrides,
  })
}

export function messageStoredEvent(message: MessageSnapshot = messageB1): V3SessionEvent {
  return {
    id: `evt-${message.id}`,
    session_id: message.session_id,
    seq: message.global_seq,
    event_type: 'message.stored',
    payload: { message },
    ts_unix_ms: message.created_at,
  }
}

export function syncStreamFixture(overrides: Partial<SyncStreamResponse> = {}): SyncStreamResponse {
  const event = messageStoredEvent(messageB1)
  return {
    ok: true,
    endpoint_cursor: 'cursor-stream-2',
    events: [
      {
        session_id: sessionB.id,
        event_type: 'message.stored',
        event,
        projection: { ...projectionB, last_event_seq: 2, projection_high_watermark_seq: 2 },
      },
    ],
    has_more: false,
    selector: { kind: 'workspace', workspace_path: '/repo', recent: { limit: 50 } },
    replay_instructions: {
      stream_path: '/v3/sync/stream',
      transport: 'http_post',
      after_endpoint_cursor: 'cursor-stream-2',
      bootstrap_required_on_cursor_error: true,
    },
    ...overrides,
  }
}

export function reconnectFixture(overrides: Partial<SessionsReconnectResponse> = {}): SessionsReconnectResponse {
  return {
    ok: true,
    rev: 2,
    snapshot_endpoint_cursor: 'cursor-reconnect',
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    run_intents_by_session: { [sessionA.id]: [runIntentA] },
    current_run_intent_by_session: { [sessionA.id]: runIntentA },
    subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id, status: 'active' }],
    session_order: [sessionA.id],
    diagnostics_by_session: {},
    client_id: 'client-1',
    surface: 'desktop',
    workset_id: 'workset-1',
    realtime: {
      stream_path: '/v3/realtime/stream',
      resume: {
        protocol: 'v3.realtime',
        protocol_version: 1,
        kind: 'resume',
        endpoint_cursor: 'cursor-reconnect',
        subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
        worksets: [{ workset_id: 'workset-1', session_ids: [sessionA.id] }],
      },
    },
    ...overrides,
  }
}

export function realtimeFrameFixture(overrides: Partial<RealtimeMessage> = {}): RealtimeMessage {
  const event = messageStoredEvent(messageB1)
  return {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-rt-event',
    session_id: sessionB.id,
    event_type: 'message.stored',
    event,
    projection: { ...projectionB, last_event_seq: 2, projection_high_watermark_seq: 2 },
    ...overrides,
  }
}

export function outboxFixture(overrides: Partial<V3RealtimeOutboxRecord> = {}): V3RealtimeOutboxRecord {
  const event = messageStoredEvent(messageA1)
  return {
    endpoint_seq: 8,
    endpoint_cursor: 'cursor-outbox',
    session_id: sessionA.id,
    event,
    projection: projectionA,
    ...overrides,
  }
}

export function messageMutationFixture(overrides: Partial<SessionMessageMutationResponse> = {}): SessionMessageMutationResponse {
  return {
    ok: true,
    session_id: sessionA.id,
    message: messageA1,
    run_intent: runIntentA,
    mutation: { realtime_outbox: null },
    realtime_outbox: null,
    ...overrides,
  }
}
