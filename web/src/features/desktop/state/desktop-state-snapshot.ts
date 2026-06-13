import { apiFetch, readErrorMessage } from '../../../app/api'
import { dedupeAndTrimMessages } from '../chat/services/message-cache'
import { normalizeDesktopSessionPlan, normalizeDesktopSessionPlanRevisions, type DesktopSessionPlanWire } from '../chat/services/session-plan-record'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import type { AgentModelPolicyRecord, ChatMessageRecord, DesktopSessionPlanRevisionRecord, ResolvedSessionPreference, SessionPreferenceRecord } from '../chat/types/chat'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import type { DesktopPermissionRecord, DesktopRunIntentRecord, DesktopSessionRecord, DesktopSessionUsageRecord } from '../types/realtime'
import type { DesktopDaemonSnapshot, DesktopSessionReadinessRecord, DesktopWorkspaceRecord } from './desktop-state'
import { applyV3RuntimeEnvelope, createV3SnapshotEnvelope } from '../v3-runtime'

export interface DesktopStateSnapshotRequest {
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
}

interface DesktopStateSnapshotRequestWire {
  session_ids?: string[]
  global?: boolean
  workspace?: { workspace_path?: string; workspace_paths?: string[] }
  recent?: { limit?: number; before_updated_at?: number | null; before_session_id?: string }
  history?: { mode?: string; max_messages_per_session?: number; max_events_per_session?: number; manifest_policy?: string; include_events?: boolean }
  resources?: { messages?: boolean; events?: boolean; run_intents?: boolean; active_plan?: boolean; plan_revisions?: boolean }
}

interface SessionWire {
  id?: string
  title?: string
  workspace_path?: string
  workspace_name?: string
  mode?: string
  metadata?: Record<string, unknown>
  session_api?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  message_count?: number
  updated_at?: number
  created_at?: number
  lifecycle?: {
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
  } | null
  run_intent?: RunIntentWire | null
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

interface MessageWire {
  id?: string
  session_id?: string
  global_seq?: number
  role?: string
  content?: string
  created_at?: number
  metadata?: Record<string, unknown>
}

interface PermissionWire {
  id?: string
  session_id?: string
  run_id?: string
  call_id?: string
  tool_name?: string
  tool_arguments?: string
  approved_arguments?: string
  status?: string
  decision?: string
  reason?: string
  requirement?: string
  mode?: string
  created_at?: number
  updated_at?: number
  resolved_at?: number
  permission_requested_at?: number
}

interface UsageWire {
  session_id?: string
  provider?: string
  model?: string
  source?: string
  context_window?: number
  total_tokens?: number
  remaining_tokens?: number
  updated_at?: number
}

interface PreferenceWire {
  provider?: string
  model?: string
  thinking?: string
  service_tier?: string
  context_mode?: string
  updated_at?: number
}

interface ResolvedPreferenceWire {
  preference?: PreferenceWire
  context_window?: number
  max_output_tokens?: number
  provider?: string
  model?: string
  thinking?: string
  service_tier?: string
  context_mode?: string
  updated_at?: number
}

interface AgentModelPolicyWire {
  agent_name?: string
  resolved_agent_name?: string
  source?: string
  locked?: boolean
  reason?: string
  preference?: PreferenceWire
  context_window?: number
  max_output_tokens?: number
}

interface RunIntentWire {
  session_id?: string
  run_id?: string
  status?: string
  blocked_reason?: string
  created_at?: number
  updated_at?: number
  event_seq?: number
}


interface DesktopStateWorksetResponseWire {
  rev?: number
  snapshot_endpoint_cursor?: string
  sessions_by_id?: Record<string, SessionWire>
  messages_by_session?: Record<string, MessageWire[]>
  permissions_by_session?: Record<string, PermissionWire[]>
  plans_by_session?: Record<string, DesktopSessionPlanWire | DesktopSessionPlanWire[] | null>
  plan_revisions_by_session?: Record<string, DesktopSessionPlanWire[]>
  usage_by_session?: Record<string, UsageWire>
  preferences_by_session?: Record<string, ResolvedPreferenceWire | PreferenceWire>
  agent_model_policy_by_session?: Record<string, AgentModelPolicyWire | null>
  run_intents_by_session?: Record<string, RunIntentWire[]>
  session_order?: string[]
}

const DEFAULT_SNAPSHOT_HISTORY = {
  mode: 'none' as const,
  maxEventsPerSession: 0,
  manifestPolicy: 'manifest' as const,
  includeEvents: false,
}

const DEFAULT_SNAPSHOT_RESOURCES = {
  runIntents: true,
}

export async function fetchDesktopStateSnapshot(input: DesktopStateSnapshotRequest, signal?: AbortSignal): Promise<DesktopDaemonSnapshot> {
  const response = await apiFetch('/v3/sessions:workset', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(toSnapshotRequestWire(input)),
    signal,
  })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  return normalizeDesktopStateSnapshot(await response.json() as DesktopStateWorksetResponseWire)
}

export async function loadDesktopStateSnapshot(input: DesktopStateSnapshotRequest, signal?: AbortSignal): Promise<DesktopDaemonSnapshot> {
  const snapshot = await fetchDesktopStateSnapshot(input, signal)
  applyV3RuntimeEnvelope(createV3SnapshotEnvelope(snapshot, { mode: 'replace', receivedAt: Date.now(), id: desktopStateSnapshotEnvelopeId('replace', snapshot, input) }))
  return snapshot
}

export async function mergeDesktopStateSnapshot(input: DesktopStateSnapshotRequest, signal?: AbortSignal): Promise<DesktopDaemonSnapshot> {
  const snapshot = await fetchDesktopStateSnapshot(input, signal)
  applyV3RuntimeEnvelope(createV3SnapshotEnvelope(snapshot, { mode: 'merge', receivedAt: Date.now(), id: desktopStateSnapshotEnvelopeId('merge', snapshot, input) }))
  return snapshot
}

function desktopStateSnapshotEnvelopeId(mode: 'replace' | 'merge', snapshot: DesktopDaemonSnapshot, input: DesktopStateSnapshotRequest): string {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean).sort()
  const workspacePath = input.workspacePath?.trim() ?? ''
  const workspacePaths = (input.workspacePaths ?? []).map((path) => path.trim()).filter(Boolean).sort()
  const recent = input.recent
    ? {
        limit: input.recent.limit ?? null,
        beforeUpdatedAt: input.recent.beforeUpdatedAt ?? null,
        beforeSessionId: input.recent.beforeSessionId?.trim() || '',
      }
    : null
  const history = input.history
    ? {
        mode: input.history.mode ?? '',
        maxMessagesPerSession: input.history.maxMessagesPerSession ?? null,
        maxEventsPerSession: input.history.maxEventsPerSession ?? null,
        manifestPolicy: input.history.manifestPolicy ?? '',
        includeEvents: input.history.includeEvents ?? null,
      }
    : null
  const snapshotScope = {
    sessionOrder: snapshot.sessionOrder ?? [],
    sessions: Object.keys(snapshot.sessionsById ?? {}).sort(),
  }
  return `snapshot:${mode}:rev:${snapshot.rev}:${JSON.stringify({ sessionIds, global: Boolean(input.global), workspacePath, workspacePaths, recent, history, snapshotScope })}`
}

export function normalizeDesktopStateSnapshot(response: DesktopStateWorksetResponseWire): DesktopDaemonSnapshot {
  assertSnapshotRevision(response.rev)
  const permissionsBySessionId = mapPermissionsBySession(response.permissions_by_session)
  const runIntentsBySessionId = latestRunIntentBySessionId(response.run_intents_by_session)
  const sessionsById = mapSessions(response.sessions_by_id, permissionsBySessionId, runIntentsBySessionId)
  const sessionOrder = normalizeSessionOrder(sessionsById, response.session_order)

  const snapshotEndpointCursor = String(response.snapshot_endpoint_cursor ?? '').trim()
  if (snapshotEndpointCursor && typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent('desktop:v3-realtime-snapshot-cursor', { detail: { endpointCursor: snapshotEndpointCursor } }))
  }

  return {
    rev: response.rev,
    snapshotEndpointCursor,
    sessionsById,
    sessionOrder,
    messagesBySessionId: mapMessagesBySession(response.messages_by_session),
    permissionsById: permissionsById(permissionsBySessionId),
    plansBySessionId: mapFirstSessionValues(response.plans_by_session, normalizeDesktopSessionPlan),
    planRevisionsBySessionId: mapPlanRevisions(response.plan_revisions_by_session),
    usageBySessionId: mapUsageBySession(response.usage_by_session),
    runIntentsBySessionId,
    workspacesByPath: workspacesByPath(Object.values(sessionsById)),
    preferencesBySessionId: mapPreferencesBySession(response.preferences_by_session),
    agentModelPolicyBySessionId: mapAgentPolicyBySession(response.agent_model_policy_by_session),
    routeReadinessBySessionId: readySessions(sessionOrder),
  }
}

function toSnapshotRequestWire(input: DesktopStateSnapshotRequest): DesktopStateSnapshotRequestWire {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const workspacePath = input.workspacePath?.trim() ?? ''
  const workspacePaths = (input.workspacePaths ?? []).map((path) => path.trim()).filter(Boolean)
  const history = { ...DEFAULT_SNAPSHOT_HISTORY, ...(input.history ?? {}) }
  const resources = { ...DEFAULT_SNAPSHOT_RESOURCES, ...(input.resources ?? {}) }
  return {
    session_ids: sessionIds.length > 0 ? sessionIds : undefined,
    global: input.global || undefined,
    workspace: workspacePath || workspacePaths.length > 0 ? {
      workspace_path: workspacePath || undefined,
      workspace_paths: workspacePaths.length > 0 ? workspacePaths : undefined,
    } : undefined,
    recent: input.recent
      ? {
          limit: input.recent.limit,
          before_updated_at: input.recent.beforeUpdatedAt ?? undefined,
          before_session_id: input.recent.beforeSessionId?.trim() || undefined,
        }
      : undefined,
    history: {
      mode: history.mode,
      max_messages_per_session: history.maxMessagesPerSession,
      max_events_per_session: history.maxEventsPerSession,
      manifest_policy: history.manifestPolicy,
      include_events: history.includeEvents,
    },
    resources: {
      messages: resources.messages || undefined,
      events: resources.events || undefined,
      run_intents: resources.runIntents || undefined,
      active_plan: resources.activePlan || undefined,
      plan_revisions: resources.planRevisions || undefined,
    },
  }
}

function assertSnapshotRevision(rev: unknown): asserts rev is number {
  if (typeof rev !== 'number' || !Number.isFinite(rev) || rev < 0) {
    throw new Error('Desktop state snapshot requires a valid daemon rev.')
  }
}

function mapSessions(
  source: Record<string, SessionWire> | undefined,
  permissionsBySessionId: Record<string, DesktopPermissionRecord[]>,
  runIntentsBySessionId: Record<string, DesktopRunIntentRecord>,
): Record<string, DesktopSessionRecord> {
  const sessions: Record<string, DesktopSessionRecord> = {}
  for (const [fallbackId, session] of Object.entries(source ?? {})) {
    const explicitId = String(session.id ?? '').trim()
    const pendingPermissions = explicitId && explicitId !== fallbackId
      ? [...(permissionsBySessionId[fallbackId] ?? []), ...(permissionsBySessionId[explicitId] ?? [])]
      : permissionsBySessionId[fallbackId] ?? []
    const topLevelRunIntent = runIntentsBySessionId[explicitId] ?? runIntentsBySessionId[fallbackId] ?? null
    const mapped = mapSession(session, fallbackId, pendingPermissions, topLevelRunIntent)
    if (mapped.id) {
      sessions[mapped.id] = mapped
    }
  }
  return sessions
}

function mapSession(session: SessionWire, fallbackId = '', pendingPermissions: DesktopPermissionRecord[] = [], topLevelRunIntent: DesktopRunIntentRecord | null = null): DesktopSessionRecord {
  const id = String(session.id ?? fallbackId).trim()
  const lifecycle = session.lifecycle && typeof session.lifecycle === 'object'
    ? {
        sessionId: String(session.lifecycle.session_id ?? id).trim(),
        runId: String(session.lifecycle.run_id ?? '').trim() || null,
        active: Boolean(session.lifecycle.active),
        phase: String(session.lifecycle.phase ?? '').trim(),
        startedAt: numberValue(session.lifecycle.started_at),
        endedAt: numberValue(session.lifecycle.ended_at),
        updatedAt: numberValue(session.lifecycle.updated_at),
        generation: numberValue(session.lifecycle.generation),
        stopReason: String(session.lifecycle.stop_reason ?? '').trim() || null,
        error: String(session.lifecycle.error ?? '').trim() || null,
        ownerTransport: String(session.lifecycle.owner_transport ?? '').trim() || null,
      }
    : null
  const mode = String(session.mode ?? 'auto').trim() || 'auto'
  const sessionPendingPermissions = pendingPermissions.filter((permission) => permission.sessionId === id && permission.status.trim().toLowerCase() === 'pending')
  const pendingPermissionCount = countApprovalRequiredPermissions(sessionPendingPermissions, mode)
  const baseLive = emptyLiveState()
  const runIntent = session.run_intent ? mapRunIntent(session.run_intent) : topLevelRunIntent
  if (!lifecycle && pendingPermissionCount > 0) {
    baseLive.status = 'blocked'
    baseLive.lastEventType = 'permission.requested'
    baseLive.lastEventAt = Math.max(...sessionPendingPermissions.map((permission) => permission.permissionRequestedAt || permission.updatedAt || permission.createdAt || 0), 0) || null
  }
  if (lifecycle?.active) {
    baseLive.runId = lifecycle.runId
    baseLive.startedAt = lifecycle.startedAt > 0 ? lifecycle.startedAt : null
    baseLive.status = lifecycle.phase === 'blocked' ? 'blocked' : lifecycle.phase === 'starting' ? 'starting' : 'running'
    baseLive.lastEventType = 'session.lifecycle.updated'
    baseLive.lastEventAt = lifecycle.updatedAt > 0 ? lifecycle.updatedAt : lifecycle.startedAt > 0 ? lifecycle.startedAt : null
    baseLive.error = lifecycle.error || lifecycle.stopReason
  }
  if (runIntent && snapshotRunIntentStatusActive(runIntent.status)) {
    baseLive.runId = runIntent.runId || baseLive.runId
    baseLive.startedAt = runIntent.createdAt > 0 ? runIntent.createdAt : baseLive.startedAt
    baseLive.status = runIntent.status.trim().toLowerCase() === 'pending_executor' ? 'starting' : 'running'
    baseLive.lastEventType = 'session.run_intent.recorded'
    baseLive.lastEventAt = runIntent.updatedAt > 0 ? runIntent.updatedAt : runIntent.createdAt > 0 ? runIntent.createdAt : baseLive.lastEventAt
    baseLive.error = null
  }
  return {
    id,
    title: String(session.title ?? '').trim(),
    workspacePath: String(session.workspace_path ?? '').trim(),
    workspaceName: String(session.workspace_name ?? '').trim(),
    mode,
    metadata: session.metadata && typeof session.metadata === 'object' ? session.metadata : undefined,
    sessionApi: String(session.session_api ?? '').trim(),
    lastEventSeq: numberValue(session.last_event_seq),
    projectionHighWatermarkSeq: numberValue(session.projection_high_watermark_seq),
    messageCount: numberValue(session.message_count),
    updatedAt: numberValue(session.updated_at),
    createdAt: numberValue(session.created_at),
    permissionsHydrated: true,
    worktreeEnabled: Boolean(session.worktree_enabled),
    worktreeRootPath: String(session.worktree_root_path ?? '').trim(),
    worktreeBaseBranch: String(session.worktree_base_branch ?? '').trim(),
    worktreeBranch: String(session.worktree_branch ?? '').trim(),
    gitBranch: String(session.git_branch ?? '').trim(),
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
    live: baseLive,
    pendingPermissions: sessionPendingPermissions,
    pendingPermissionCount,
    usage: null,
  }
}

function snapshotRunIntentStatusActive(status: string): boolean {
  const normalized = status.trim().toLowerCase()
  return normalized === 'pending_executor' || normalized === 'running'
}

function mapMessagesBySession(source: Record<string, MessageWire[]> | undefined): Record<string, ChatMessageRecord[]> {
  const messagesBySession: Record<string, ChatMessageRecord[]> = {}
  for (const [sessionId, messages] of Object.entries(source ?? {})) {
    if (!messages || messages.length === 0) {
      continue
    }
    messagesBySession[sessionId] = dedupeAndTrimMessages(messages
      .map(mapMessage)
      .filter((message) => message.id && message.sessionId))
  }
  return messagesBySession
}

function mapMessage(message: MessageWire): ChatMessageRecord {
  const content = String(message.content ?? '')
  return {
    id: String(message.id ?? '').trim(),
    sessionId: String(message.session_id ?? '').trim(),
    globalSeq: numberValue(message.global_seq),
    role: String(message.role ?? '').trim(),
    content,
    createdAt: numberValue(message.created_at),
    metadata: message.metadata,
    toolMessage: parseStructuredToolMessage(content),
  }
}

function mapPermissionsBySession(source: Record<string, PermissionWire[]> | undefined): Record<string, DesktopPermissionRecord[]> {
  const permissionsBySessionId: Record<string, DesktopPermissionRecord[]> = {}
  for (const [sessionId, records] of Object.entries(source ?? {})) {
    const mapped = (records ?? [])
      .map(mapPermission)
      .filter((permission) => permission.id && permission.sessionId && permission.status.trim().toLowerCase() === 'pending')
      .sort((left, right) =>
        (right.permissionRequestedAt - left.permissionRequestedAt)
        || (right.updatedAt - left.updatedAt)
        || left.id.localeCompare(right.id),
      )
    if (mapped.length > 0) {
      permissionsBySessionId[sessionId] = mapped
    }
  }
  return permissionsBySessionId
}

function permissionsById(source: Record<string, DesktopPermissionRecord[]> | undefined): Record<string, DesktopPermissionRecord> {
  const permissions: Record<string, DesktopPermissionRecord> = {}
  for (const records of Object.values(source ?? {})) {
    for (const permission of records ?? []) {
      if (permission.id) {
        permissions[permission.id] = permission
      }
    }
  }
  return permissions
}

function mapPermission(permission: PermissionWire): DesktopPermissionRecord {
  return {
    id: String(permission.id ?? '').trim(),
    sessionId: String(permission.session_id ?? '').trim(),
    runId: String(permission.run_id ?? '').trim(),
    callId: String(permission.call_id ?? '').trim(),
    toolName: String(permission.tool_name ?? '').trim(),
    toolArguments: String(permission.tool_arguments ?? ''),
    approvedArguments: typeof permission.approved_arguments === 'string' ? permission.approved_arguments : undefined,
    status: String(permission.status ?? '').trim(),
    decision: String(permission.decision ?? '').trim(),
    reason: String(permission.reason ?? '').trim(),
    requirement: String(permission.requirement ?? '').trim(),
    mode: String(permission.mode ?? '').trim(),
    createdAt: numberValue(permission.created_at),
    updatedAt: numberValue(permission.updated_at),
    resolvedAt: numberValue(permission.resolved_at),
    permissionRequestedAt: numberValue(permission.permission_requested_at),
  }
}

function mapUsageBySession(source: Record<string, UsageWire> | undefined): Record<string, DesktopSessionUsageRecord> {
  const usageBySession: Record<string, DesktopSessionUsageRecord> = {}
  for (const [sessionId, usage] of Object.entries(source ?? {})) {
    usageBySession[sessionId] = {
      sessionId: String(usage.session_id ?? sessionId).trim(),
      provider: String(usage.provider ?? '').trim(),
      model: String(usage.model ?? '').trim(),
      source: String(usage.source ?? '').trim(),
      contextWindow: numberValue(usage.context_window),
      totalTokens: numberValue(usage.total_tokens),
      remainingTokens: numberValue(usage.remaining_tokens),
      updatedAt: numberValue(usage.updated_at),
    }
  }
  return usageBySession
}

function mapPreference(source: PreferenceWire | undefined): SessionPreferenceRecord {
  return {
    provider: String(source?.provider ?? '').trim(),
    model: String(source?.model ?? '').trim(),
    thinking: String(source?.thinking ?? '').trim(),
    serviceTier: String(source?.service_tier ?? '').trim(),
    contextMode: String(source?.context_mode ?? '').trim(),
    updatedAt: numberValue(source?.updated_at),
  }
}

function mapPreferencesBySession(source: Record<string, ResolvedPreferenceWire | PreferenceWire> | undefined): Record<string, ResolvedSessionPreference> {
  const preferences: Record<string, ResolvedSessionPreference> = {}
  for (const [sessionId, value] of Object.entries(source ?? {})) {
    const resolved = value as ResolvedPreferenceWire
    preferences[sessionId] = {
      preference: mapPreference(resolved.preference ?? value as PreferenceWire),
      contextWindow: numberValue(resolved.context_window),
      maxOutputTokens: numberValue(resolved.max_output_tokens),
    }
  }
  return preferences
}

function mapAgentPolicyBySession(source: Record<string, AgentModelPolicyWire | null> | undefined): Record<string, AgentModelPolicyRecord | null> {
  const policies: Record<string, AgentModelPolicyRecord | null> = {}
  for (const [sessionId, policy] of Object.entries(source ?? {})) {
    policies[sessionId] = policy
      ? {
          agentName: String(policy.agent_name ?? '').trim(),
          resolvedAgentName: String(policy.resolved_agent_name ?? '').trim(),
          source: String(policy.source ?? '').trim(),
          locked: Boolean(policy.locked),
          reason: String(policy.reason ?? '').trim(),
          preference: mapPreference(policy.preference),
          contextWindow: numberValue(policy.context_window),
          maxOutputTokens: numberValue(policy.max_output_tokens),
        }
      : null
  }
  return policies
}

function mapFirstSessionValues<T, R>(source: Record<string, T | T[] | null> | undefined, mapValue: (value: T) => R): Record<string, R | null> {
  const values: Record<string, R | null> = {}
  for (const [sessionId, value] of Object.entries(source ?? {})) {
    const first = Array.isArray(value) ? value[0] : value
    values[sessionId] = first ? mapValue(first) : null
  }
  return values
}

function mapPlanRevisions(source: Record<string, DesktopSessionPlanWire[]> | undefined): Record<string, DesktopSessionPlanRevisionRecord[]> {
  const revisionsBySession: Record<string, DesktopSessionPlanRevisionRecord[]> = {}
  for (const [sessionId, revisions] of Object.entries(source ?? {})) {
    revisionsBySession[sessionId] = normalizeDesktopSessionPlanRevisions(revisions)
  }
  return revisionsBySession
}

function latestRunIntentBySessionId(source: Record<string, RunIntentWire[]> | undefined): Record<string, DesktopRunIntentRecord> {
  const bySessionId: Record<string, DesktopRunIntentRecord> = {}
  for (const [sessionId, intents] of Object.entries(source ?? {})) {
    const latest = [...(intents ?? [])].sort((left, right) => (numberValue(right.event_seq) - numberValue(left.event_seq)) || (numberValue(right.updated_at) - numberValue(left.updated_at)))[0]
    if (latest) {
      bySessionId[sessionId] = mapRunIntent({ ...latest, session_id: latest.session_id ?? sessionId })
    }
  }
  return bySessionId
}

function mapRunIntent(intent: RunIntentWire): DesktopRunIntentRecord {
  return {
    sessionId: String(intent.session_id ?? '').trim(),
    runId: String(intent.run_id ?? '').trim(),
    status: String(intent.status ?? '').trim(),
    blockedReason: String(intent.blocked_reason ?? '').trim(),
    createdAt: numberValue(intent.created_at),
    updatedAt: numberValue(intent.updated_at),
    eventSeq: numberValue(intent.event_seq),
  }
}

function workspacesByPath(sessions: DesktopSessionRecord[]): Record<string, DesktopWorkspaceRecord> {
  const sessionById = Object.fromEntries(sessions.map((session) => [session.id, session]))
  const workspaces: Record<string, DesktopWorkspaceRecord> = {}
  for (const session of sessions) {
    const workspacePath = session.workspacePath.trim()
    if (!workspacePath) {
      continue
    }
    const current = workspaces[workspacePath]
    workspaces[workspacePath] = {
      workspacePath,
      workspaceName: current?.workspaceName || session.workspaceName || workspacePath.split('/').filter(Boolean).pop() || workspacePath,
      sessionIds: [...(current?.sessionIds ?? []), session.id],
      updatedAt: Math.max(current?.updatedAt ?? 0, session.updatedAt),
    }
  }
  for (const workspace of Object.values(workspaces)) {
    workspace.sessionIds = Array.from(new Set(workspace.sessionIds))
      .sort((left, right) => ((sessionById[right]?.updatedAt ?? 0) - (sessionById[left]?.updatedAt ?? 0)) || left.localeCompare(right))
  }
  return workspaces
}

function readySessions(sessionOrder: string[]): Record<string, DesktopSessionReadinessRecord> {
  const now = Date.now()
  return Object.fromEntries(sessionOrder.map((sessionId) => [sessionId, {
    sessionId,
    status: 'ready' as const,
    ready: true,
    missingResources: [],
    omittedResources: [],
    error: null,
    updatedAt: now,
  }]))
}

function normalizeSessionOrder(sessionsById: Record<string, DesktopSessionRecord>, providedOrder: string[] | undefined): string[] {
  const remaining = new Set(Object.keys(sessionsById))
  const order: string[] = []
  for (const sessionId of providedOrder ?? []) {
    const normalized = sessionId.trim()
    if (remaining.delete(normalized)) {
      order.push(normalized)
    }
  }
  return [...order, ...Array.from(remaining).sort((left, right) => {
    const leftSession = sessionsById[left]
    const rightSession = sessionsById[right]
    return (rightSession.updatedAt - leftSession.updatedAt) || left.localeCompare(right)
  })]
}

function emptyLiveState(): DesktopSessionRecord['live'] {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: 'idle',
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
    summary: null,
    lastEventType: null,
    lastEventAt: null,
    error: null,
    seq: 0,
    assistantDraft: '',
    retainedAssistantSegments: [],
    reasoningSummary: '',
    reasoningText: '',
    reasoningState: 'idle',
    reasoningSegment: 0,
    reasoningStartedAt: null,
    awaitingAck: false,
  }
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}
