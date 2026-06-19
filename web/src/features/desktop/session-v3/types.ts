import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type { DesktopDaemonSnapshot } from '../state/desktop-state'
import type {
  DesktopPermissionRecord,
  DesktopRunIntentRecord,
  DesktopSessionRecord,
  DesktopSessionUsageRecord,
} from '../types/realtime'

export const SESSION_V3_SURFACE = 'desktop' as const
export const SESSION_V3_REALTIME_PROTOCOL = 'v3.realtime' as const
export const SESSION_V3_REALTIME_PROTOCOL_VERSION = 1 as const
export const SESSION_V3_REALTIME_RESUME_KIND = 'resume' as const
export const SESSION_V3_REALTIME_STREAM_PATH = '/v3/realtime/stream' as const

export type SessionV3Surface = typeof SESSION_V3_SURFACE
export type SessionV3RealtimeProtocol = typeof SESSION_V3_REALTIME_PROTOCOL
export type SessionV3RealtimeProtocolVersion = typeof SESSION_V3_REALTIME_PROTOCOL_VERSION
export type SessionV3SelectorKind = 'global' | 'workspace' | 'session_ids' | 'recent'
export type SessionV3MessageRole = 'user' | 'assistant' | 'system' | 'tool' | 'reasoning'

export type SessionV3JsonRecord = Record<string, unknown>

export interface SessionV3RecentSelectorWire {
  limit?: number
  before_updated_at?: number | null
  before_session_id?: string
}

export interface SessionV3WorksetHistoryWire {
  mode?: 'none' | 'tail' | 'full' | string
  max_messages_per_session?: number
  max_events_per_session?: number
  manifest_policy?: 'error' | 'omit' | 'manifest' | string
  include_events?: boolean
}

export interface SessionV3WorksetResourcesWire {
  messages?: boolean
  events?: boolean
  run_intents?: boolean
  active_plan?: boolean
  plan_revisions?: boolean
}

export interface SessionV3WorksetWorkspaceWire {
  workspace_path?: string
  workspace_paths?: string[]
}

export interface SessionV3WorksetSelectorWire {
  kind?: SessionV3SelectorKind | string
  global?: boolean
  workspace_path?: string
  workspace_paths?: string[]
  session_ids?: string[]
  recent?: SessionV3RecentSelectorWire
}

export interface SessionV3WorksetRequestWire {
  workset_id?: string
  selector?: SessionV3WorksetSelectorWire
  session_ids?: string[]
  global?: boolean
  workspace?: SessionV3WorksetWorkspaceWire
  recent?: SessionV3RecentSelectorWire
  history?: SessionV3WorksetHistoryWire
  resources?: SessionV3WorksetResourcesWire
  include_active?: boolean
  auto_subscribe_sessions?: boolean
}

export interface SessionV3KnownStateWire {
  applied_seq?: number
  high_watermark?: number
  endpoint_cursor?: string
}

export interface SessionV3StateSnapshotRequest {
  sessionIds?: string[]
  global?: boolean
  workspacePath?: string
  workspacePaths?: string[]
  recent?: {
    limit?: number
    beforeUpdatedAt?: number | null
    beforeSessionId?: string
  }
  history?: {
    mode?: 'none' | 'tail' | 'full'
    maxMessagesPerSession?: number
    maxEventsPerSession?: number
    manifestPolicy?: 'error' | 'omit' | 'manifest'
    includeEvents?: boolean
  }
  resources?: {
    messages?: boolean
    events?: boolean
    runIntents?: boolean
    activePlan?: boolean
    planRevisions?: boolean
  }
  includeActive?: boolean
  knownSessions?: Record<string, SessionV3KnownStateWire>
}

export interface SessionV3StateSnapshotRequestWire {
  surface: SessionV3Surface
  selector_kind?: string
  selector?: SessionV3WorksetSelectorWire
  session_ids?: string[]
  history?: SessionV3WorksetHistoryWire
  resources?: SessionV3WorksetResourcesWire
  include_active?: boolean
  known_sessions?: Record<string, SessionV3KnownStateWire>
}

export interface SessionV3LifecycleWire {
  session_id?: string
  run_id?: string
  active?: boolean
  phase?: string
  started_at?: number
  ended_at?: number
  updated_at?: number
  generation?: number
  stop_reason?: string
  error?: string
  owner_transport?: string
}

export interface SessionV3SessionWire {
  id?: string
  title?: string
  workspace_path?: string
  workspace_name?: string
  mode?: string
  metadata?: SessionV3JsonRecord
  session_api?: 'v3' | string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  message_count?: number
  updated_at?: number
  created_at?: number
  lifecycle?: SessionV3LifecycleWire | null
  run_intent?: SessionV3RunIntentWire | null
  active_run?: SessionV3JsonRecord | null
  session_status?: string
  worktree_enabled?: boolean
  worktree_root_path?: string
  worktree_base_branch?: string
  worktree_branch?: string
  git_branch?: string
  git_has_git?: boolean
  git_clean?: boolean
  git_dirty_count?: number
  git_staged_count?: number
  git_modified_count?: number
  git_untracked_count?: number
  git_conflict_count?: number
  git_ahead_count?: number
  git_behind_count?: number
  git_commit_detected?: boolean
  git_commit_count?: number
  git_committed_file_count?: number
  git_committed_additions?: number
  git_committed_deletions?: number
}

export interface SessionV3MessageWire {
  id?: string
  session_id?: string
  global_seq?: number
  role?: string
  content?: string
  created_at?: number
  metadata?: SessionV3JsonRecord
}

export interface SessionV3ProjectionWire {
  session_id?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  updated_at?: number
}

export interface SessionV3RunIntentWire {
  session_id?: string
  run_id?: string
  status?: string
  blocked_reason?: string
  created_at?: number
  updated_at?: number
  event_seq?: number
}

export interface SessionV3EventWire {
  id?: string
  session_id?: string
  seq?: number
  event_type?: string
  ts_unix_ms?: number
  payload?: Record<string, unknown>
}

export interface SessionV3SyncScopeWire {
  surface?: string
  stream_kind?: string
  selector_filter_hash?: string
  resource_set?: string
}

export interface SessionV3SyncReplayInstructionsWire {
  stream_path?: string
  transport?: string
  after_endpoint_cursor?: string
  bootstrap_required_on_cursor_error?: boolean
}

export interface SessionV3StateSnapshotResponseWire {
  ok?: boolean
  rev?: number
  snapshot_endpoint_cursor?: string
  sessions_by_id?: Record<string, SessionV3SessionWire>
  projections_by_session?: Record<string, SessionV3ProjectionWire>
  messages_by_session?: Record<string, SessionV3MessageWire[]>
  events_by_session?: Record<string, SessionV3EventWire[]>
  run_intents_by_session?: Record<string, SessionV3RunIntentWire[]>
  current_run_intent_by_session?: Record<string, SessionV3RunIntentWire>
  session_order?: string[]
  plans_by_session?: Record<string, unknown>
  plan_revisions_by_session?: Record<string, unknown[]>
  permissions_by_session?: Record<string, unknown[]>
  usage_by_session?: Record<string, unknown>
  preferences_by_session?: Record<string, unknown>
  agent_model_policy_by_session?: Record<string, unknown>
  history_manifests_by_session?: Record<string, unknown>
  history_chunks_by_id?: Record<string, unknown>
  omissions?: Record<string, unknown>
  pagination?: Record<string, unknown>
  watermarks?: Record<string, unknown>
  sync_scope?: SessionV3SyncScopeWire
  scope_id?: string
  selector?: SessionV3WorksetSelectorWire
  known_sessions?: Record<string, SessionV3KnownStateWire>
  tombstones_by_session?: Record<string, unknown>
  replay_instructions?: SessionV3SyncReplayInstructionsWire
}

export interface SessionV3SyncStreamRequestWire extends SessionV3StateSnapshotRequestWire {
  endpoint_cursor: string
  limit?: number
}

export interface SessionV3SyncStreamResponseWire {
  ok?: boolean
  endpoint_cursor?: string
  after_endpoint_seq?: number
  high_watermark_seq?: number
  events?: unknown[]
  has_more?: boolean
  selector?: SessionV3WorksetSelectorWire
  replay_instructions?: SessionV3SyncReplayInstructionsWire
  error?: string
  error_code?: string
  bootstrap_required?: boolean
  oldest_available?: number
  latest?: number
}

export interface SessionV3RealtimeSubscriptionRequestWire {
  session_id: string
  subscription_id: string
  endpoint_cursor?: string
}

export interface SessionV3RealtimeWorksetSubscriptionRequestWire {
  workset_id: string
  subscription_id: string
  surface?: SessionV3Surface | string
  selector: SessionV3WorksetSelectorWire
  resources?: string[]
  auto_subscribe_sessions: boolean
}

export interface SessionV3SyncSubscriptionWire extends SessionV3RealtimeSubscriptionRequestWire {
  protocol: SessionV3RealtimeProtocol
  protocol_version: SessionV3RealtimeProtocolVersion
  kind: 'subscribe.session'
  endpoint_cursor: string
}

export interface SessionV3RealtimeResumeWire {
  protocol: SessionV3RealtimeProtocol
  protocol_version: SessionV3RealtimeProtocolVersion
  kind: typeof SESSION_V3_REALTIME_RESUME_KIND
  endpoint_cursor: string
  subscriptions?: SessionV3RealtimeSubscriptionRequestWire[]
  worksets?: SessionV3RealtimeWorksetSubscriptionRequestWire[]
}

export interface SessionV3SnapshotResult {
  snapshot: DesktopDaemonSnapshot
  endpointCursor: string
  replayInstructions: SessionV3SyncReplayInstructionsWire | null
  syncScope: SessionV3SyncScopeWire | null
  scopeId: string
  selector: SessionV3WorksetSelectorWire | null
  tombstonesBySession: Record<string, unknown>
  wire: SessionV3StateSnapshotResponseWire
}

export interface SessionV3SyncSnapshot extends SessionV3SnapshotResult {}

export interface SessionV3RealtimeFrameWire {
  protocol?: SessionV3RealtimeProtocol | string
  protocol_version?: SessionV3RealtimeProtocolVersion | number
  kind?: string
  type?: string
  session_id?: string
  subscription_id?: string
  workset_id?: string
  workset_subscription_id?: string
  auto_subscribed?: boolean
  endpoint_cursor?: string
  last_seq?: number
  high_watermark_seq?: number
  rev?: number
  prevRev?: number
  event_type?: string
  event?: {
    id?: string
    session_id?: string
    event_type?: string
    seq?: number
    ts_unix_ms?: number
    payload?: unknown
  }
  projection?: SessionV3ProjectionWire | null
  payload?: unknown
  stream?: string
  entity_id?: string
  global_seq?: number
  source_seq?: number
  ts_unix_ms?: number
  error_code?: string
  error?: string
  reason?: string
}

export interface SessionV3RealtimeOutboxWire {
  endpoint_seq?: number
  endpoint_cursor?: string
  session_id?: string
}

export interface SessionV3MutationWire {
  realtime_outbox?: SessionV3RealtimeOutboxWire | null
  message?: SessionV3MessageWire | null
  session?: never
  messages?: never
  events?: never
  workset_id?: never
  worksets?: never
  subscriptions?: never
}

export interface SessionV3MutationResponseWire {
  ok?: boolean
  session_id?: string
  session?: SessionV3SessionWire
  projection?: SessionV3ProjectionWire
  message?: SessionV3MessageWire
  active_run_intent?: SessionV3RunIntentWire | null
  run_intent?: SessionV3RunIntentWire | null
  metadata?: SessionV3JsonRecord
  realtime_outbox?: SessionV3RealtimeOutboxWire | null
  mutation?: SessionV3MutationWire | null
}

export interface SessionV3CreateSessionResponseWire extends SessionV3MutationResponseWire {}

export interface SessionV3HydratedSessionResponseWire extends SessionV3MutationResponseWire {
  messages?: SessionV3MessageWire[]
  events?: SessionV3EventWire[]
  pending_permissions?: unknown[]
  usage_summary?: unknown | null
}

export interface SessionV3MessageCommitRequestWire {
  client_request_id?: string
  role: SessionV3MessageRole
  content: string
  metadata?: SessionV3JsonRecord
}

export interface SessionV3MessageCommitResponseWire extends SessionV3MutationResponseWire {}

export interface SessionV3ModeMutationResponseWire extends SessionV3MutationResponseWire {
  mode?: string
}

export interface SessionV3AgentMutationResponseWire extends SessionV3MutationResponseWire {
  agent?: SessionV3JsonRecord
  agent_model_policy?: unknown
}

export interface SessionV3MetadataMutationResponseWire extends SessionV3MutationResponseWire {}

export interface SessionV3CompactRequestWire {
  client_request_id?: string
  note?: string
  agent_name?: string
  instructions?: string
}

export interface SessionV3CompactResponseWire extends SessionV3MutationResponseWire {
  ok?: boolean
  session_id?: string
  run_intent?: SessionV3RunIntentWire | null
  compaction?: {
    run_id?: string
    status?: string
    owner_transport?: string
  }
  mutation?: SessionV3MutationWire | null
  realtime_outbox?: SessionV3RealtimeOutboxWire | null
}

export interface SessionV3RunStopResponseWire extends SessionV3MutationResponseWire {
  session_id?: string
  run_id?: string
  status?: string
  reason?: string
}

export interface SessionV3PreferenceWire {
  provider?: string
  model?: string
  thinking?: string
  service_tier?: string
  context_mode?: string
  updated_at?: number
}

export interface SessionV3PreferenceResponseWire extends SessionV3MutationResponseWire {
  preference?: SessionV3PreferenceWire
  context_window?: number
  max_output_tokens?: number
}

export interface SessionV3UsageResponseWire {
  usage_summary?: unknown | null
}

export interface SessionV3PermissionsResponseWire {
  permissions?: unknown[]
}

export interface SessionV3PlanResponseWire extends SessionV3MutationResponseWire {
  plan?: unknown | null
  plan_revisions?: unknown[]
}

export interface SessionV3ActivePlanResponseWire {
  has_active?: boolean
  active_plan?: unknown | null
}

export interface SessionV3PlanHistoryResponseWire {
  revisions?: unknown[]
}

export interface SessionV3PermissionResolveRequestWire {
  action: 'approve' | 'deny' | 'approve_always' | 'always_allow' | 'always_deny'
  reason: string
  approved_arguments?: SessionV3JsonRecord
}

export interface SessionV3PermissionResolveResponseWire extends SessionV3MutationResponseWire {
  permission?: unknown
  saved_rule?: unknown
}

export interface SessionV3PermissionsResolveAllResponseWire extends SessionV3MutationResponseWire {
  resolved?: unknown[]
}

export interface SessionV3RunStopRequestWire {
  type: 'run.stop'
  run_id: string
  target_swarm_id?: string
}

export interface SessionV3PlanSaveRequestWire {
  id?: string
  plan_id?: string
  title?: string
  plan?: string
  document?: unknown
  document_patch?: unknown
  status?: string
  approval_state?: string
}

export interface SessionV3SessionSnapshot {
  source: 'v3'
  session: DesktopSessionRecord
  messages: ChatMessageRecord[]
  events: unknown[]
  projection: SessionV3ProjectionWire | null
  preference: ResolvedSessionPreference | null
  agentModelPolicy: AgentModelPolicyRecord | null
  pendingPermissions: DesktopPermissionRecord[]
  usage: DesktopSessionUsageRecord | null
  runIntent: DesktopRunIntentRecord | null
  hasActivePlan: boolean
  activePlan: DesktopSessionPlanRecord | null
  planRevisions: DesktopSessionPlanRevisionRecord[]
  appliedSeq: number
  highWatermark: number
  hydratedAt: number
}
