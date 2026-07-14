import type { DesktopNotificationCenterRecord, DesktopNotificationSummary, DesktopPermissionRecord } from '../types/realtime'
import type { DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../chat/types/chat'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'

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
  attention?: {
    pending_permissions?: boolean
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
  current_run_state?: boolean
  session_view?: boolean
  active_plan?: boolean
  plan_revisions?: boolean
  permission_summaries?: boolean
  notifications?: boolean
  notification_summary?: boolean
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
  navigation_hidden?: boolean
  system_session?: boolean
  system_sidechat?: boolean
  lineage_kind?: string
  created_at: number
  updated_at: number
  message_count: number
  last_message_at: number
  lifecycle?: unknown
  current_execution_epoch?: V3ExecutionEpoch | null
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
  execution_epoch?: V3ExecutionEpochRef
}

export interface V3SessionProjection {
  session_id: string
  last_event_seq: number
  projection_high_watermark_seq: number
  updated_at: number
}

export interface V3ExecutionEpochRef {
  epoch_id: string
  epoch_ordinal?: number
  ordinal?: number
}

export interface V3ExecutionEpoch extends V3ExecutionEpochRef {
  session_id?: string
  status?: string
  first_root_seq?: number
  last_root_seq?: number
  started_event_seq?: number
  completed_event_seq?: number
  created_at?: number
  updated_at?: number
  sealed_at?: number
  started_at?: number
  completed_at?: number
  parent_epoch_id?: string
  previous_epoch_id?: string
  trigger?: string
}

export interface V3ExecutionEpochBoundary extends V3ExecutionEpochRef {
  kind: string
  event_seq?: number
  parent_epoch_id?: string
  previous_epoch_id?: string
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
  epoch_id?: string
  execution_epoch?: V3ExecutionEpochRef
  execution_epoch_boundary?: V3ExecutionEpochBoundary
}

export interface V3SessionRunIntent {
  session_id: string
  user_id?: string
  account_scope_id?: string
  run_id: string
  epoch_id?: string
  status: string
  blocked_reason?: string
  created_at: number
  started_at?: number
  completed_at?: number
  duration_ms?: number
  cumulative_duration_ms?: number
  updated_at: number
  event_seq: number
  execution_epoch?: V3ExecutionEpochRef
}

export interface V3SessionRunState {
  session_id: string
  user_id?: string
  account_scope_id?: string
  run_id: string
  active: boolean
  status: string
  blocked_reason?: string
  created_at: number
  started_at?: number
  completed_at?: number
  duration_ms?: number
  cumulative_duration_ms?: number
  updated_at: number
  event_seq?: number
}

export interface V3RealtimeBootstrap {
  stream_path: string
  resume: unknown
}

export interface DesktopV3AgenticSettings {
  mode: string
  agent_name: string
  resolved_agent_name: string
  runtime_mode?: string
  stored_preference?: unknown
  effective_preference?: unknown
  agent_model_policy?: unknown
  context_window?: number
  max_output_tokens?: number
  projection_seq?: number
}

export interface DesktopV3SessionView {
  agentic_settings?: DesktopV3AgenticSettings
  current_execution_epoch?: V3ExecutionEpoch | null
  pending_permissions?: unknown[]
  usage_summary?: unknown
  current_run_state?: V3SessionRunState
  has_active_plan?: boolean
  active_plan?: unknown | null
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

export interface DesktopPermissionSummary {
  pendingApprovalCount: number
  oldestPendingAt: number
  newestPendingAt: number
  updatedAt: number
}

export interface DesktopPermissionSummaryWire {
  session_id?: unknown
  sessionId?: unknown
  pending_approval_count?: unknown
  pendingApprovalCount?: unknown
  oldest_pending_at?: unknown
  oldestPendingAt?: unknown
  newest_pending_at?: unknown
  newestPendingAt?: unknown
  updated_at?: unknown
  updatedAt?: unknown
}

export interface DesktopNotificationWire {
  account_scope_id?: unknown
  accountScopeID?: unknown
  id?: unknown
  swarm_id?: unknown
  swarmID?: unknown
  origin_swarm_id?: unknown
  originSwarmID?: unknown
  session_id?: unknown
  sessionId?: unknown
  run_id?: unknown
  runId?: unknown
  category?: unknown
  severity?: unknown
  title?: unknown
  body?: unknown
  status?: unknown
  source_event_type?: unknown
  sourceEventType?: unknown
  permission_id?: unknown
  permissionId?: unknown
  tool_name?: unknown
  toolName?: unknown
  requirement?: unknown
  session_title?: unknown
  sessionTitle?: unknown
  session_label?: unknown
  sessionLabel?: unknown
  workspace_path?: unknown
  workspacePath?: unknown
  workspace_name?: unknown
  workspaceName?: unknown
  origin_label?: unknown
  originLabel?: unknown
  action_url?: unknown
  actionURL?: unknown
  read_at?: unknown
  readAt?: unknown
  acked_at?: unknown
  ackedAt?: unknown
  muted_at?: unknown
  mutedAt?: unknown
  created_at?: unknown
  createdAt?: unknown
  updated_at?: unknown
  updatedAt?: unknown
}

export interface DesktopNotificationSummaryWire {
  account_scope_id?: unknown
  accountScopeID?: unknown
  swarm_id?: unknown
  swarmID?: unknown
  total_count?: unknown
  totalCount?: unknown
  unread_count?: unknown
  unreadCount?: unknown
  active_count?: unknown
  activeCount?: unknown
  updated_at?: unknown
  updatedAt?: unknown
}

export interface SyncSnapshotResponse {
  ok: true
  rev: number
  snapshot_endpoint_cursor: string
  sessions_by_id: Record<string, SessionSnapshot>
  projections_by_session: Record<string, V3SessionProjection>
  messages_by_session?: Record<string, MessageSnapshot[]>
  events_by_session?: Record<string, V3SessionEvent[]>
  run_intents_by_session?: Record<string, V3SessionRunIntent[]>
  current_run_state_by_session?: Record<string, V3SessionRunState>
  permission_summaries_by_session?: Record<string, DesktopPermissionSummaryWire>
  notifications?: DesktopNotificationWire[]
  notification_summary?: DesktopNotificationSummaryWire
  active_session_ids?: string[]
  session_views_by_id?: Record<string, DesktopV3SessionView>
  realtime?: V3RealtimeBootstrap
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
  notification?: DesktopNotificationWire
  notification_summary?: DesktopNotificationSummaryWire
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
  current_run_state_by_session?: Record<string, V3SessionRunState>
  permission_summaries_by_session?: Record<string, DesktopPermissionSummaryWire>
  notifications?: DesktopNotificationWire[]
  notification_summary?: DesktopNotificationSummaryWire
  active_session_ids?: string[]
  session_views_by_id?: Record<string, DesktopV3SessionView>
  subscriptions: ReconnectSubscription[]
  session_order: string[]
  diagnostics_by_session: Record<string, ReconnectDiagnostic[]>
  client_id?: string
  surface?: string
  workset_id?: string
  messages_by_session?: Record<string, MessageSnapshot[]>
  events_by_session?: Record<string, V3SessionEvent[]>
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
  | 'workset.session.updated'
  | 'workset.session.removed'
  | 'auth.denied'
  | 'slow_consumer.reconnect_required'
  | 'live.patch'
  | 'notification.resource.updated'

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
  capabilities?: string[]
  live?: SessionV3RealtimeLivePatchWire
  rev?: number
  prevRev?: number
  event_type?: string
  event?: V3SessionEvent
  session?: SessionSnapshot
  current_run_state?: V3SessionRunState
  has_active_plan?: boolean
  active_plan?: unknown | null
  permission_summary?: DesktopPermissionSummaryWire
  notification?: DesktopNotificationWire
  notification_summary?: DesktopNotificationSummaryWire
  tombstone?: V3SessionTombstone
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
  epoch_id?: string
  ordinal?: number
  parent_epoch_id?: string
  execution_epoch?: V3ExecutionEpoch
  execution_epoch_boundary?: V3ExecutionEpochBoundary
  turn_usage?: unknown
  usage_summary?: unknown
  tombstone?: V3SessionTombstone
  permission?: unknown
  permission_summary?: unknown
  notification?: unknown
  notification_summary?: unknown
  summary?: unknown
  has_active_plan?: boolean
  active_plan?: unknown | null
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
  event?: V3SessionEvent
  projection?: V3SessionProjection
  [key: string]: unknown
}

export interface SessionMessageMutationResponse {
  ok: true
  session_id: string
  session?: SessionSnapshot | null
  projection?: V3SessionProjection
  message: unknown
  run_intent: V3SessionRunIntent | null
  current_run_state?: V3SessionRunState | null
  turn_usage?: unknown
  usage_summary?: unknown
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

export interface SessionSettingsMutationResponse {
  ok?: boolean
  session_id?: string
  mode?: string
  metadata?: Record<string, unknown>
  preference?: unknown
  context_window?: number
  max_output_tokens?: number
  agent?: Record<string, unknown>
  agent_model_policy?: unknown
  turn_usage?: unknown
  usage_summary?: unknown
  mutation?: SessionMutationResult | null
  realtime_outbox?: unknown
  [key: string]: unknown
}

export interface SessionArchiveMutationResponse {
  ok?: boolean
  archived?: boolean
  results?: Array<{ session_id?: string; archived?: boolean; tombstone?: unknown }>
  [key: string]: unknown
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
  streamPath?: '/v3/realtime/stream' | string
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

export type MessageListCacheSource = 'network'

export interface MessageListCache {
  items: MessageSnapshot[]
  byMessageId: Record<string, number>
  byGlobalSeq: Record<string, number>
  knownTail?: {
    limit: number
    cursor: string
  }
  knownFull?: boolean
  oldestLoadedSeq?: number
  loadedCount?: number
  sourceMessageCount?: number
  sourceLastMessageAt?: number
  sourceProjectionHighWatermarkSeq?: number
  hydratedAt?: number
  tailHydratedAt?: number
  lastAccessedAt?: number
  source?: MessageListCacheSource
}

export interface PendingUserMessage {
  clientRequestId: string
  messageId: string
  sessionId: string
  role: 'user'
  content: string
  metadata?: Record<string, unknown>
  runId?: string
  createdAt: number
  timelineSeq?: number
  status: 'pending' | 'failed'
  error?: string
}

export interface LiveTaskToolStreamState {
  pathId: string
  streamVersion: number
  status?: string
  phase?: string
  action?: string
  description?: string
  goal?: string
  parentSessionId?: string
  taskCallId?: string
  launchCount?: number
  updatedAt: number
  launchesByKey: Record<string, Record<string, unknown>>
  launchOrder: string[]
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
    streamId?: string
    streamStep?: number
    stepId?: string
    liveSeqEnd?: number
    offsetEnd?: number
    durableOffsetEnd?: number
    livePaused?: boolean
  }
  assistantSegments?: Array<{
    id: string
    content: string
    createdAt: number
    updatedAt: number
    timelineSeq?: number
    streamId?: string
    streamStep?: number
    stepId?: string
    liveSeqEnd?: number
    offsetEnd?: number
    durableOffsetEnd?: number
    livePaused?: boolean
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
      taskStream?: LiveTaskToolStreamState
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
  timelineFloor?: number
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
  source?: 'network'
}

export interface DesktopInitialHydrateState {
  status: 'idle' | 'loading' | 'cached' | 'ready' | 'error'
  requestedSessionIds: string[]
  hydratedSessionIds: string[]
  scopeId?: string
  error?: string
  stale?: boolean
  source?: 'network'
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
  sessionViewsById: Record<string, DesktopV3SessionView>
  tombstonesBySession: Record<string, V3SessionTombstone>
  messagesBySession: Record<string, MessageListCache>
  eventsBySession: Record<string, V3SessionEvent[]>
  hydrateInFlightBySession: Record<string, number>
  evictedTranscriptsBySession: Record<string, number>
  runIntentsBySession: Record<string, Record<string, V3SessionRunIntent>>
  currentRunIntentBySession: Record<string, V3SessionRunIntent | undefined>
  currentExecutionEpochBySession: Record<string, V3ExecutionEpoch | undefined>
  pendingUserByClientRequestId: Record<string, PendingUserMessage>
  liveRunsBySession: Record<string, Record<string, LiveRunOverlay>>
  subscriptionsById: Record<string, SubscriptionCache>
  worksetsById: Record<string, WorksetCache>
  plansBySession: Record<string, unknown>
  hasActivePlanBySession: Record<string, boolean>
  planRevisionsBySession: Record<string, unknown[]>
  permissionsBySession: Record<string, DesktopPermissionRecord[]>
  permissionSummaryBySessionId: Record<string, DesktopPermissionSummary>
  notificationsById: Record<string, DesktopNotificationCenterRecord>
  notificationSummary: DesktopNotificationSummary
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
  notification?: DesktopNotificationWire
  notificationSummary?: DesktopNotificationSummaryWire
}

export type DesktopV3CacheAction =
  | { type: 'desktopV3Cache.applyHydrationPlan'; reusedSessionIds: string[]; hydrateSessionIds: string[] }
  | { type: 'desktopV3Cache.markHydrateInFlight'; sessionIds: string[]; inFlight: boolean }
  | { type: 'desktopSidebarBootstrap.update'; patch: Partial<DesktopSidebarBootstrapState> }
  | { type: 'desktopInitialHydrate.update'; patch: Partial<DesktopInitialHydrateState> }
  | { type: 'session.select'; sessionId?: string }
  | { type: 'snapshot.apply'; source: 'bootstrap'; scopeId: string; snapshot: SyncSnapshotResponse }
  | { type: 'hydrate.apply'; source: 'hydrate'; scopeId: string; requestedSessionIds: string[]; snapshot: SyncSnapshotResponse }
  | { type: 'messages.prependHistoryResult'; sessionId: string; messages: MessageSnapshot[]; sourceMessageCount?: number; knownFull?: boolean }
  | { type: 'syncStream.applyBatch'; scopeId: string; endpointCursor: string; events: CacheEvent[]; hasMore: boolean; replayInstructions: SyncReplayInstructions }
  | { type: 'reconnect.applySnapshot'; snapshot: SessionsReconnectResponse }
  | { type: 'realtime.storeResume'; streamPath: '/v3/realtime/stream'; resume: RealtimeMessage }
  | { type: 'realtime.applyEvent'; event: CacheEvent; endpointCursor?: string; durabilityCommitted?: boolean }
  | { type: 'realtime.applyNotificationResource'; frame: RealtimeMessage }
  | { type: 'realtime.applyLivePatchBatch'; patches: SessionV3RealtimeLivePatchWire[] }
  | { type: 'permission.resolveResult'; sessionId: string; permissionId: string; permission: DesktopPermissionRecord | null }
  | { type: 'planSnapshot.apply'; sessionId: string; hasActivePlan: boolean; activePlan: DesktopSessionPlanRecord | null; planRevisions: DesktopSessionPlanRevisionRecord[] }
  | { type: 'liveRun.mergeRepairEvents'; sessionId: string; runId: string; events: CacheEvent[] }
  | { type: 'realtime.worksetSessionDiscovered'; frame: RealtimeMessage }
  | { type: 'realtime.worksetSessionUpdated'; frame: RealtimeMessage }
  | { type: 'realtime.worksetSessionRemoved'; frame: RealtimeMessage }
  | { type: 'realtime.cursorError'; frame: RealtimeMessage }
  | { type: 'realtime.control'; frame: RealtimeMessage }
  | { type: 'realtime.unknownFrame'; frame: RealtimeMessage }
  | { type: 'realtime.statusChanged'; status: RealtimeCache['status']; errorCode?: string; error?: string }
  | { type: 'mutation.sessionCreateResult'; raw: SessionCreateMutationResponse | SessionMutationErrorResponse; sidebarScopeId: string }
  | { type: 'mutation.messageResult'; raw: SessionMessageMutationResponse | MessageMutationConflictResponse; clientRequestId: string; messageId: string }
  | { type: 'mutation.sessionSettingsResult'; raw: SessionSettingsMutationResponse }
  | { type: 'mutation.sessionArchiveResult'; raw: SessionArchiveMutationResponse }
  | { type: 'pendingUser.upsert'; input: Omit<PendingUserMessage, 'role' | 'status'> }
