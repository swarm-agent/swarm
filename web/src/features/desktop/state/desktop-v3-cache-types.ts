import type { PersistedDesktopV3MessageTailV1, PersistedDesktopV3OwnerV1 } from './desktop-v3-cache-persisted-types'

export type SyncSelectorKind = '' | 'global' | 'recent' | 'session_ids' | 'workspace' | 'tui'

export interface SyncSelector {
  kind?: SyncSelectorKind | string
  global?: boolean
  workspace_path?: string
  workspace_paths?: string[]
  cwd_path?: string
  session_ids?: string[]
  recent?: {
    limit?: number
    before_updated_at?: number
    before_session_id?: string
  }
}

export interface SyncHistory {
  mode?: '' | 'none' | 'tail' | 'full' | string
  max_messages_per_session?: number
  max_events_per_session?: number
  manifest_policy?: string
  include_events?: boolean
}

export interface SyncResources {
  messages?: boolean
  events?: boolean
  run_intents?: boolean
  active_plan?: boolean
  plan_revisions?: boolean
}

export interface KnownSessionState {
  applied_seq?: number
  high_watermark?: number
  endpoint_cursor?: string
}

export interface SessionSnapshot {
  id: string
  user_id?: string
  account_scope_id?: string
  workspace_path: string
  workspace_name: string
  temporary_workspace_roots?: string[]
  title: string
  mode: string
  preference?: unknown
  worktree_enabled?: boolean
  worktree_root_path?: string
  worktree_base_branch?: string
  worktree_branch?: string
  metadata?: Record<string, unknown>
  created_at: number
  updated_at: number
  message_count: number
  last_message_at: number
  lifecycle?: unknown
}

export interface MessageSnapshot {
  id: string
  session_id: string
  user_id?: string
  account_scope_id?: string
  global_seq: number
  role: string
  content: string
  metadata?: Record<string, unknown>
  created_at: number
}

export interface V3SessionProjection {
  session_id: string
  last_event_seq: number
  projection_high_watermark_seq: number
  updated_at: number
}

export interface V3SessionEvent {
  id: string
  session_id: string
  seq: number
  event_type: string
  payload: unknown
  ts_unix_ms: number
  causation_id?: string
  correlation_id?: string
}

export interface V3SessionRunIntent {
  session_id: string
  user_id?: string
  account_scope_id?: string
  run_id: string
  status: string
  blocked_reason?: string
  created_at: number
  updated_at: number
  event_seq: number
}

export interface V3SessionTombstone {
  session_id: string
  user_id?: string
  account_scope_id?: string
  workspace_path?: string
  kind?: string
  deleted?: boolean
  archived?: boolean
  hidden?: boolean
  endpoint_seq?: number
  event_seq?: number
  updated_at?: number
  session?: SessionSnapshot
}

export interface SyncScopeWire {
  surface: string
  stream_kind: 'v3.sync.snapshot' | string
  selector_filter_hash: string
  resource_set: string
}

export interface SyncReplayInstructions {
  stream_path: '/v3/sync/stream' | string
  transport: 'http_post' | string
  after_endpoint_cursor: string
  bootstrap_required_on_cursor_error: true | boolean
}

export interface SyncSnapshotResponse {
  ok: true
  rev: number
  snapshot_endpoint_cursor: string
  sessions_by_id: Record<string, SessionSnapshot>
  projections_by_session: Record<string, V3SessionProjection>
  messages_by_session?: Record<string, MessageSnapshot[]>
  events_by_session?: Record<string, V3SessionEvent[]>
  plans_by_session?: Record<string, unknown>
  plan_revisions_by_session?: Record<string, unknown[]>
  permissions_by_session?: Record<string, unknown>
  usage_by_session?: Record<string, unknown>
  preferences_by_session?: Record<string, unknown>
  agent_model_policy_by_session?: Record<string, unknown>
  run_intents_by_session?: Record<string, V3SessionRunIntent[]>
  history_manifests_by_session?: Record<string, unknown>
  history_chunks_by_id?: Record<string, unknown>
  omissions?: unknown[]
  pagination?: unknown
  watermarks?: unknown
  session_order: string[]
  sync_scope: SyncScopeWire
  scope_id: string
  selector: SyncSelector
  known_sessions: Record<string, KnownSessionState>
  tombstones_by_session: Record<string, V3SessionTombstone>
  replay_instructions: SyncReplayInstructions
}

export interface SyncStreamEvent {
  session_id: string
  event_type: string
  event: V3SessionEvent
  projection: V3SessionProjection
}

export interface SyncStreamResponse {
  ok: true
  endpoint_cursor: string
  events: SyncStreamEvent[]
  has_more: boolean
  selector: SyncSelector
  replay_instructions: SyncReplayInstructions
}

export interface ReconnectSubscription {
  subscription_id?: string
  session_id?: string
  workset_id?: string
  status?: string
  replaying?: boolean
  caught_up?: boolean
  [key: string]: unknown
}

export interface RealtimeSubscriptionRequest {
  session_id: string
  subscription_id: string
  endpoint_cursor?: string
}

export interface RealtimeWorksetSubscriptionRequest {
  workset_id: string
  subscription_id: string
  surface?: string
  selector: SyncSelector
  resources?: string[]
  auto_subscribe_sessions?: boolean
}

export interface ReconnectDiagnostic {
  code?: string
  message?: string
  [key: string]: unknown
}

export interface SessionsReconnectResponse {
  ok: true
  rev: number
  snapshot_endpoint_cursor: string
  sessions_by_id: Record<string, SessionSnapshot>
  projections_by_session: Record<string, V3SessionProjection>
  run_intents_by_session?: Record<string, V3SessionRunIntent[]>
  current_run_intent_by_session?: Record<string, V3SessionRunIntent>
  subscriptions: ReconnectSubscription[]
  session_order: string[]
  diagnostics_by_session: Record<string, ReconnectDiagnostic[]>
  client_id?: string
  surface?: string
  workset_id?: string
  messages_by_session?: Record<string, MessageSnapshot[]>
  events_by_session?: Record<string, V3SessionEvent[]>
  plans_by_session?: Record<string, unknown>
  plan_revisions_by_session?: Record<string, unknown[]>
  permissions_by_session?: Record<string, unknown>
  usage_by_session?: Record<string, unknown>
  preferences_by_session?: Record<string, unknown>
  agent_model_policy_by_session?: Record<string, unknown>
  history_manifests_by_session?: Record<string, unknown>
  history_chunks_by_id?: Record<string, unknown>
  omissions?: unknown[]
  pagination?: unknown
  watermarks?: unknown
  worksets?: RealtimeWorksetSubscriptionRequest[]
  realtime?: {
    stream_path: '/v3/realtime/stream'
    resume: RealtimeMessage & {
      protocol: 'v3.realtime'
      protocol_version: 1
      kind: 'resume'
      endpoint_cursor: string
      subscriptions: RealtimeSubscriptionRequest[]
      worksets: RealtimeWorksetSubscriptionRequest[]
    }
  }
}

export type RealtimeKind =
  | 'hello'
  | 'event'
  | 'replay.started'
  | 'replay.complete'
  | 'cursor.error'
  | 'keepalive'
  | 'endpoint.watermark'
  | 'projection.high_watermark'
  | 'subscribe.session'
  | 'unsubscribe.session'
  | 'resume'
  | 'workset.session.discovered'
  | 'workset.session.removed'
  | 'auth.denied'
  | 'slow_consumer.reconnect_required'

export interface RealtimeMessage {
  protocol?: 'v3.realtime' | string
  protocol_version?: 1 | number
  kind: RealtimeKind | string
  session_id?: string
  workset_id?: string
  subscription_id?: string
  endpoint_cursor?: string
  subscriptions?: RealtimeSubscriptionRequest[]
  worksets?: RealtimeWorksetSubscriptionRequest[]
  rev?: number
  prevRev?: number
  event_type?: string
  event?: V3SessionEvent
  workset_subscription_id?: string
  auto_subscribed?: boolean
  projection?: V3SessionProjection
  high_watermark_seq?: number
  last_seq?: number
  next_seq?: number
  reason?: string
  error_code?: string
  error?: string
  bootstrap_required?: boolean
  oldest_available_endpoint_seq?: number
  latest_endpoint_seq?: number
  missing_endpoint_seq?: number
  [key: string]: unknown
}

export interface SessionEventPayload {
  session?: SessionSnapshot
  message?: MessageSnapshot
  lifecycle?: unknown
  run_intent?: V3SessionRunIntent
  turn_usage?: unknown
  usage_summary?: unknown
  tombstone?: V3SessionTombstone
  message_id?: string
  role?: string
  run_id?: string
  status?: string
  blocked_reason?: string
  error?: string
  [key: string]: unknown
}

export interface V3RealtimeOutboxRecord {
  endpoint_seq?: number
  endpoint_cursor?: string
  session_id: string
  event: V3SessionEvent
  projection: V3SessionProjection
  [key: string]: unknown
}

export interface MessageMutationConflictResponse {
  ok: false
  error?: string
  error_code?: string
  conflict?: unknown
  [key: string]: unknown
}

export interface SessionMutationResult {
  realtime_outbox?: V3RealtimeOutboxRecord | null
  [key: string]: unknown
}

export interface SessionMessageMutationResponse {
  ok: true
  session_id: string
  message: MessageSnapshot | null
  run_intent: V3SessionRunIntent | null
  mutation: SessionMutationResult
  realtime_outbox: V3RealtimeOutboxRecord | null
}

export interface SessionCreateMutationResponse {
  ok: true
  session_id: string
  session: SessionSnapshot
  projection: V3SessionProjection
  mutation: SessionMutationResult
  realtime_outbox: V3RealtimeOutboxRecord | null
}

export interface SessionMutationErrorResponse {
  ok: false
  error?: string
  error_code?: string
  conflict?: unknown
  [key: string]: unknown
}

export interface SyncScopeCache {
  scopeId: string
  surface: string
  streamKind: 'v3.sync.snapshot'
  selectorFilterHash: string
  resourceSet: string
  selector: SyncSelector
  endpointCursor: string
  replayPath: '/v3/sync/stream' | string
  replayTransport: 'http_post' | string
  needsBootstrap: boolean
  lastErrorCode?: string
  lastError?: string
}

export interface RealtimeCache {
  status: 'closed' | 'connecting' | 'open' | 'reconnecting' | 'auth_denied' | 'error' | 'stale'
  surface: string
  streamPath?: '/v3/realtime/stream'
  endpointCursor?: string
  resumeFrame?: RealtimeMessage
  lastHelloCursor?: string
  lastKeepaliveCursor?: string
  needsReconnect: boolean
  needsBootstrap: boolean
  errorCode?: string
  error?: string
}

export type SessionCacheRecord =
  | {
      kind: 'full'
      session: SessionSnapshot
      needsHydrate: boolean
    }
  | {
      kind: 'stub'
      id: string
      needsHydrate: true
      discoveredByWorksetId?: string
      discoveredAt?: number
    }

export type MessageListCacheSource = 'persisted' | 'network'

export interface MessageListCache {
  items: MessageSnapshot[]
  byMessageId: Record<string, number>
  byGlobalSeq: Record<string, number>
  knownTail?: {
    limit: number
    cursor: string
  }
  knownFull?: boolean
  sourceMessageCount?: number
  sourceLastMessageAt?: number
  sourceProjectionHighWatermarkSeq?: number
  hydratedAt?: number
  source?: MessageListCacheSource
}

export interface PendingUserMessage {
  clientRequestId: string
  messageId: string
  sessionId: string
  role: 'user'
  content: string
  metadata?: Record<string, unknown>
  createdAt: number
  status: 'pending' | 'failed'
  error?: string
}

export interface LiveRunOverlay {
  sessionId: string
  runId: string
  status:
    | 'pending_executor'
    | 'running'
    | 'dispatch_blocked'
    | 'completed'
    | 'failed'
    | 'cancelled'
    | 'interrupted'
    | 'expired'
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
  toolCallsByCallId: Record<
    string,
    {
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
  >
  reasoning?: LiveRunReasoningOverlay
  reasoningByKey?: Record<string, LiveRunReasoningOverlay>
  lastEventSeqSeen?: number
}

export interface LiveRunReasoningOverlay {
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

export interface SubscriptionCache extends Record<string, unknown> {
  subscription_id?: string
  subscriptionId?: string
  session_id?: string
  sessionId?: string
  workset_id?: string
  worksetId?: string
  replaying?: boolean
  caughtUp?: boolean
  status?: string
}

export interface WorksetCache extends Record<string, unknown> {
  workset_id?: string
  worksetId?: string
  sessionIds?: string[]
  inactiveSessionIds?: string[]
}

export interface DesktopSidebarBootstrapState {
  status: 'idle' | 'loading' | 'cached' | 'ready' | 'error'
  scopeId?: string
  error?: string
  stale?: boolean
  source?: 'persisted' | 'network'
}

export interface DesktopInitialHydrateState {
  status: 'idle' | 'loading' | 'cached' | 'ready' | 'error'
  requestedSessionIds: string[]
  hydratedSessionIds: string[]
  scopeId?: string
  error?: string
  stale?: boolean
  source?: 'persisted' | 'network'
}

export interface DesktopV3CacheState {
  version: 1
  syncScopesById: Record<string, SyncScopeCache>
  realtime: RealtimeCache
  desktopSidebarBootstrap: DesktopSidebarBootstrapState
  desktopInitialHydrate: DesktopInitialHydrateState
  sessionsById: Record<string, SessionCacheRecord>
  projectionsBySession: Record<string, V3SessionProjection>
  sessionOrderByScope: Record<string, string[]>
  tombstonesBySession: Record<string, V3SessionTombstone>
  messagesBySession: Record<string, MessageListCache>
  eventsBySession: Record<string, V3SessionEvent[]>
  runIntentsBySession: Record<string, Record<string, V3SessionRunIntent>>
  currentRunIntentBySession: Record<string, V3SessionRunIntent | undefined>
  pendingUserByClientRequestId: Record<string, PendingUserMessage>
  liveRunsBySession: Record<string, Record<string, LiveRunOverlay>>
  subscriptionsById: Record<string, SubscriptionCache>
  worksetsById: Record<string, WorksetCache>
  plansBySession: Record<string, unknown>
  planRevisionsBySession: Record<string, unknown[]>
  permissionsBySession: Record<string, unknown>
  usageBySession: Record<string, unknown>
  preferencesBySession: Record<string, unknown>
  agentModelPolicyBySession: Record<string, unknown>
  historyManifestsBySession: Record<string, unknown>
  historyChunksById: Record<string, unknown>
  omissionsByScope: Record<string, unknown[]>
  paginationByScope: Record<string, unknown>
  watermarksByScope: Record<string, unknown>
  selectedSessionId?: string
}

export interface CacheEvent {
  source: 'sync-stream' | 'realtime' | 'outbox'
  sessionId: string
  eventType: string
  sessionEvent?: V3SessionEvent
  projection?: V3SessionProjection
  payload: SessionEventPayload
}

export type DesktopV3CacheAction =
  | { type: 'desktopV3Cache.restore'; owner: PersistedDesktopV3OwnerV1; selectedMessageTail?: PersistedDesktopV3MessageTailV1; preferredSessionId?: string | null }
  | { type: 'desktopV3Cache.restoreMessageTails'; tails: PersistedDesktopV3MessageTailV1[] }
  | { type: 'desktopV3Cache.applyHydrationPlan'; reusedSessionIds: string[]; hydrateSessionIds: string[] }
  | { type: 'desktopSidebarBootstrap.update'; patch: Partial<DesktopSidebarBootstrapState> }
  | { type: 'desktopInitialHydrate.update'; patch: Partial<DesktopInitialHydrateState> }
  | { type: 'session.select'; sessionId?: string }
  | { type: 'snapshot.apply'; source: 'bootstrap'; scopeId: string; snapshot: SyncSnapshotResponse }
  | { type: 'hydrate.apply'; source: 'hydrate'; scopeId: string; requestedSessionIds: string[]; snapshot: SyncSnapshotResponse }
  | { type: 'syncStream.applyBatch'; scopeId: string; endpointCursor: string; events: CacheEvent[]; hasMore: boolean; replayInstructions: SyncReplayInstructions }
  | { type: 'reconnect.applySnapshot'; snapshot: SessionsReconnectResponse }
  | { type: 'realtime.storeResume'; streamPath: '/v3/realtime/stream'; resume: RealtimeMessage }
  | { type: 'realtime.applyEvent'; event: CacheEvent; endpointCursor?: string; deferLiveOverlay?: boolean }
  | { type: 'liveRun.rebuildFromEvents'; sessionId: string; runId: string; afterSeq: number }
  | { type: 'realtime.worksetSessionDiscovered'; frame: RealtimeMessage }
  | { type: 'realtime.worksetSessionRemoved'; frame: RealtimeMessage }
  | { type: 'realtime.cursorError'; frame: RealtimeMessage }
  | { type: 'realtime.control'; frame: RealtimeMessage }
  | { type: 'realtime.unknownFrame'; frame: RealtimeMessage }
  | { type: 'realtime.statusChanged'; status: RealtimeCache['status']; errorCode?: string; error?: string }
  | { type: 'mutation.sessionCreateResult'; raw: SessionCreateMutationResponse | SessionMutationErrorResponse; sidebarScopeId: string }
  | { type: 'mutation.messageResult'; raw: SessionMessageMutationResponse | MessageMutationConflictResponse; clientRequestId: string; messageId: string }
  | { type: 'pendingUser.upsert'; input: Omit<PendingUserMessage, 'role' | 'status'> }
