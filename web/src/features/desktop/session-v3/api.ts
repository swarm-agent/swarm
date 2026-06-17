import { apiFetch, readErrorMessage, requestJson } from '../../../app/api'
import { normalizeDesktopSessionPlan, normalizeDesktopSessionPlanRevisions, type DesktopSessionPlanWire } from '../chat/services/session-plan-record'
import { mapDesktopSessionPermission, mapDesktopSessionUsageSummary } from '../chat/queries/chat-queries'
import { normalizeDesktopStateSnapshot } from '../state/desktop-state-snapshot'
import type { DesktopDaemonSnapshot } from '../state/desktop-state'
import type { DesktopPermissionRecord, DesktopSessionUsageRecord } from '../types/realtime'
import type { ResolvedSessionPreference } from '../chat/types/chat'
import type {
  SessionV3ActivePlanResponseWire,
  SessionV3AgentMutationResponseWire,
  SessionV3CompactRequestWire,
  SessionV3CompactResponseWire,
  SessionV3CreateSessionResponseWire,
  SessionV3JsonRecord,
  SessionV3MessageCommitRequestWire,
  SessionV3MessageCommitResponseWire,
  SessionV3MessageRole,
  SessionV3ModeMutationResponseWire,
  SessionV3PermissionResolveRequestWire,
  SessionV3PermissionResolveResponseWire,
  SessionV3PermissionsResolveAllResponseWire,
  SessionV3PermissionsResponseWire,
  SessionV3PlanHistoryResponseWire,
  SessionV3PlanResponseWire,
  SessionV3PlanSaveRequestWire,
  SessionV3PreferenceResponseWire,
  SessionV3RunStopRequestWire,
  SessionV3RunStopResponseWire,
  SessionV3SessionSnapshot,
  SessionV3SnapshotResult,
  SessionV3SyncStreamRequestWire,
  SessionV3SyncStreamResponseWire,
  SessionV3StateSnapshotRequest,
  SessionV3StateSnapshotRequestWire,
  SessionV3StateSnapshotResponseWire,
  SessionV3UsageResponseWire,
  SessionV3WorksetSelectorWire,
} from './types'
import { SESSION_V3_SURFACE } from './types'

const DEFAULT_SNAPSHOT_HISTORY = {
  mode: 'none' as const,
  maxEventsPerSession: 0,
  manifestPolicy: 'manifest' as const,
  includeEvents: false,
}

export interface SessionV3RequestOptions {
  signal?: AbortSignal
}

export interface SessionV3MessageOptions extends SessionV3RequestOptions {
  clientRequestId?: string | null
  metadata?: SessionV3JsonRecord
}

export interface SessionV3CompactOptions extends SessionV3RequestOptions {
  note?: string | null
  agentName?: string | null
  instructions?: string | null
  clientRequestId?: string | null
}

export interface SessionV3PlanSaveInput {
  id?: string
  title?: string
  plan?: string
  document?: unknown
  documentPatch?: unknown
  status?: string
  approvalState?: string
}

export interface SessionV3StopRunInput {
  runId: string
  targetSwarmId?: string | null
}

export interface SessionV3CreateSessionInput extends SessionV3RequestOptions {
  sessionId?: string | null
  clientRequestId?: string | null
  title?: string | null
  workspacePath: string
  workspaceName?: string | null
  workspaceBindingId?: string | null
  swarmId?: string | null
  targetKind?: string | null
  targetRelationship?: string | null
  hostWorkspacePath?: string | null
  runtimeWorkspacePath?: string | null
  mode?: string | null
  agentName?: string | null
  metadata?: SessionV3JsonRecord
  preference?: Partial<ResolvedSessionPreference['preference']>
  worktreeMode?: string | null
  worktreeUseCurrentBranch?: boolean
  worktreeBaseBranch?: string | null
  worktreeBranchName?: string | null
}

export function assertSessionV3SessionId(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    throw new Error('Sessions API v3 requires a canonical session id.')
  }
  return normalizedSessionId
}

export async function bootstrapSessionV3Sync(
  input: SessionV3StateSnapshotRequest = {},
  options: SessionV3RequestOptions = {},
): Promise<SessionV3SnapshotResult> {
  const wire = await requestJson<SessionV3StateSnapshotResponseWire>('/v3/sync/bootstrap', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(toSessionV3SnapshotRequestWire(input)),
    signal: options.signal,
  })
  return normalizeSessionV3SnapshotResult(wire)
}

export async function hydrateSessionV3Sync(
  input: SessionV3StateSnapshotRequest,
  options: SessionV3RequestOptions = {},
): Promise<SessionV3SnapshotResult> {
  const body = toSessionV3SnapshotRequestWire(input)
  if (!body.session_ids?.length) {
    throw new Error('Sessions API v3 sync hydrate requires at least one session id.')
  }
  const wire = await requestJson<SessionV3StateSnapshotResponseWire>('/v3/sync/hydrate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: options.signal,
  })
  return normalizeSessionV3SnapshotResult(wire)
}

export async function streamSessionV3Sync(
  input: SessionV3StateSnapshotRequest & { endpointCursor: string; limit?: number },
  options: SessionV3RequestOptions = {},
): Promise<SessionV3SyncStreamResponseWire> {
  const body = toSessionV3StreamRequestWire(input)
  return requestJson<SessionV3SyncStreamResponseWire>('/v3/sync/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: options.signal,
  })
}

export async function fetchSessionV3StateSnapshot(
  input: SessionV3StateSnapshotRequest,
  options: SessionV3RequestOptions = {},
): Promise<SessionV3SnapshotResult> {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  return sessionIds.length > 0
    ? hydrateSessionV3Sync({ ...input, sessionIds }, options)
    : bootstrapSessionV3Sync({ ...input, sessionIds: [] }, options)
}

export async function fetchSessionV3SessionSnapshot(
  sessionId: string,
  options: SessionV3RequestOptions & Pick<SessionV3StateSnapshotRequest, 'history' | 'resources'> = {},
): Promise<SessionV3SessionSnapshot | null> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const { snapshot } = await fetchSessionV3StateSnapshot({
    sessionIds: [normalizedSessionId],
    history: options.history ?? { mode: 'none', maxEventsPerSession: 200, manifestPolicy: 'manifest', includeEvents: true },
    resources: options.resources ?? { events: true },
  }, options)
  return sessionV3SessionSnapshotFromDaemonSnapshot(snapshot, normalizedSessionId)
}

export async function createSessionV3(input: SessionV3CreateSessionInput): Promise<SessionV3CreateSessionResponseWire> {
  const body = toSessionV3CreateRequestWire(input)
  return requestJson<SessionV3CreateSessionResponseWire>('/v3/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: input.signal,
  })
}

export async function sendSessionV3Message(
  sessionId: string,
  role: SessionV3MessageRole,
  content: string,
  options: SessionV3MessageOptions = {},
): Promise<SessionV3MessageCommitResponseWire> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const body: SessionV3MessageCommitRequestWire = {
    client_request_id: options.clientRequestId?.trim() || `desktop-v3-message:${normalizedSessionId}:${crypto.randomUUID()}`,
    role,
    content,
    metadata: options.metadata,
  }
  return requestJson<SessionV3MessageCommitResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: options.signal,
  })
}

export async function compactSessionV3(
  sessionId: string,
  options: SessionV3CompactOptions = {},
): Promise<SessionV3CompactResponseWire> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const body: SessionV3CompactRequestWire = {
    client_request_id: options.clientRequestId?.trim() || `desktop-v3-compact:${normalizedSessionId}:${crypto.randomUUID()}`,
    note: options.note?.trim() ?? '',
    agent_name: options.agentName?.trim() ?? '',
    instructions: options.instructions?.trim() ?? '',
  }
  return requestJson<SessionV3CompactResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/compact`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: options.signal,
  })
}

export async function updateSessionV3Mode(
  sessionId: string,
  mode: string,
  options: SessionV3RequestOptions = {},
): Promise<SessionV3ModeMutationResponseWire> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  return requestJson<SessionV3ModeMutationResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/mode`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mode }),
    signal: options.signal,
  })
}

export async function updateSessionV3Agent(
  sessionId: string,
  agentName: string,
  options: SessionV3RequestOptions = {},
): Promise<SessionV3AgentMutationResponseWire> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  return requestJson<SessionV3AgentMutationResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/agent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_name: agentName.trim() }),
    signal: options.signal,
  })
}

export async function updateSessionV3Metadata(
  sessionId: string,
  metadata: SessionV3JsonRecord,
  options: SessionV3RequestOptions = {},
): Promise<SessionV3SessionSnapshot | null> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  await requestJson<unknown>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/metadata`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ metadata }),
    signal: options.signal,
  })
  return fetchSessionV3SessionSnapshot(normalizedSessionId, options)
}

export async function fetchSessionV3Preference(
  sessionId: string,
  options: SessionV3RequestOptions = {},
): Promise<ResolvedSessionPreference> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const response = await requestJson<SessionV3PreferenceResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/preference`, {
    signal: options.signal,
  })
  const preference = response.preference ?? {}
  return {
    preference: {
      provider: String(preference.provider ?? '').trim(),
      model: String(preference.model ?? '').trim(),
      thinking: String(preference.thinking ?? '').trim(),
      serviceTier: String(preference.service_tier ?? '').trim(),
      contextMode: String(preference.context_mode ?? '').trim(),
      updatedAt: numberValue(preference.updated_at),
    },
    contextWindow: numberValue(response.context_window),
    maxOutputTokens: numberValue(response.max_output_tokens),
  }
}

export async function updateSessionV3Preference(
  sessionId: string,
  input: Partial<ResolvedSessionPreference['preference']>,
  options: SessionV3RequestOptions = {},
): Promise<ResolvedSessionPreference> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const response = await requestJson<SessionV3PreferenceResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/preference`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      provider: input.provider,
      model: input.model,
      thinking: input.thinking,
      service_tier: input.serviceTier,
      context_mode: input.contextMode,
    }),
    signal: options.signal,
  })
  return normalizeSessionV3PreferenceResponse(response)
}

export async function fetchSessionV3Usage(
  sessionId: string,
  options: SessionV3RequestOptions = {},
): Promise<DesktopSessionUsageRecord | null> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const response = await requestJson<SessionV3UsageResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/usage`, {
    signal: options.signal,
  })
  return mapDesktopSessionUsageSummary(response.usage_summary)
}

export async function fetchSessionV3Permissions(
  sessionId: string,
  options: SessionV3RequestOptions & { status?: string; limit?: number } = {},
): Promise<DesktopPermissionRecord[]> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const search = new URLSearchParams()
  search.set('status', options.status?.trim() || 'pending')
  search.set('limit', String(options.limit && options.limit > 0 ? Math.floor(options.limit) : 200))
  const response = await requestJson<SessionV3PermissionsResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/permissions?${search.toString()}`, {
    signal: options.signal,
  })
  return (response.permissions ?? [])
    .map((permission) => mapDesktopSessionPermission(permission))
    .filter((permission) => permission.id && permission.sessionId === normalizedSessionId)
}

export async function resolveSessionV3Permission(
  sessionId: string,
  permissionId: string,
  input: SessionV3PermissionResolveRequestWire,
  options: SessionV3RequestOptions = {},
): Promise<DesktopPermissionRecord> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const normalizedPermissionId = permissionId.trim()
  if (!normalizedPermissionId) {
    throw new Error('Sessions API v3 permission resolution requires a permission id.')
  }
  const response = await requestJson<SessionV3PermissionResolveResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/permissions/${encodeURIComponent(normalizedPermissionId)}/resolve`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal: options.signal,
  })
  return mapDesktopSessionPermission(response.permission)
}

export async function resolveAllSessionV3Permissions(
  sessionId: string,
  input: Omit<SessionV3PermissionResolveRequestWire, 'approved_arguments'> & { limit?: number },
  options: SessionV3RequestOptions = {},
): Promise<DesktopPermissionRecord[]> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const response = await requestJson<SessionV3PermissionsResolveAllResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/permissions/resolve_all`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
    signal: options.signal,
  })
  return (response.resolved ?? []).map((permission) => mapDesktopSessionPermission(permission))
}

export async function fetchSessionV3ActivePlan(
  sessionId: string,
  options: SessionV3RequestOptions & { includeHistory?: boolean } = {},
): Promise<Pick<SessionV3SessionSnapshot, 'hasActivePlan' | 'activePlan' | 'planRevisions'>> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const active = await requestJson<SessionV3ActivePlanResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans/active`, {
    signal: options.signal,
  })
  const activePlan = active.active_plan ? normalizeDesktopSessionPlan(active.active_plan as DesktopSessionPlanWire) : null
  let planRevisions: SessionV3SessionSnapshot['planRevisions'] = []
  if (options.includeHistory !== false && activePlan?.id) {
    const history = await requestJson<SessionV3PlanHistoryResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans/${encodeURIComponent(activePlan.id)}/history?limit=100`, {
      signal: options.signal,
    })
    planRevisions = normalizeDesktopSessionPlanRevisions(history.revisions as DesktopSessionPlanWire[] | undefined)
  }
  return {
    hasActivePlan: Boolean(active.has_active || activePlan?.id || activePlan?.plan || activePlan?.title),
    activePlan,
    planRevisions,
  }
}

export async function saveSessionV3Plan(
  sessionId: string,
  input: SessionV3PlanSaveInput,
  options: SessionV3RequestOptions = {},
): Promise<SessionV3SessionSnapshot['activePlan']> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const body: SessionV3PlanSaveRequestWire = {
    id: input.id?.trim() || undefined,
    plan_id: input.id?.trim() || undefined,
    title: input.title?.trim() || undefined,
    plan: input.plan,
    document: input.document ?? undefined,
    document_patch: input.documentPatch ?? undefined,
    status: input.status?.trim() || undefined,
    approval_state: input.approvalState?.trim() || undefined,
  }
  const response = await requestJson<SessionV3PlanResponseWire>(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: options.signal,
  })
  return response.plan ? normalizeDesktopSessionPlan(response.plan as DesktopSessionPlanWire) : null
}

export async function stopSessionV3Run(
  sessionId: string,
  input: SessionV3StopRunInput,
  options: SessionV3RequestOptions = {},
): Promise<SessionV3RunStopResponseWire | null> {
  const normalizedSessionId = assertSessionV3SessionId(sessionId)
  const normalizedRunId = input.runId.trim()
  const body: SessionV3RunStopRequestWire = {
    type: 'run.stop',
    run_id: normalizedRunId,
    target_swarm_id: input.targetSwarmId?.trim() || undefined,
  }
  const response = await apiFetch(`/v3/sessions/${encodeURIComponent(normalizedSessionId)}/run/stop`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: options.signal,
  })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  if (response.status === 204) {
    return null
  }
  const text = (await response.text()).trim()
  if (!text) {
    return null
  }
  return JSON.parse(text) as SessionV3RunStopResponseWire
}

function normalizeSessionV3SnapshotResult(wire: SessionV3StateSnapshotResponseWire): SessionV3SnapshotResult {
  const snapshot = normalizeDesktopStateSnapshot(wire)
  return {
    snapshot,
    endpointCursor: snapshot.snapshotEndpointCursor ?? '',
    replayInstructions: wire.replay_instructions ?? null,
    syncScope: wire.sync_scope ?? null,
    scopeId: String(wire.scope_id ?? '').trim(),
    selector: wire.selector ?? null,
    tombstonesBySession: wire.tombstones_by_session ?? {},
    wire,
  }
}

export function sessionV3SessionSnapshotFromDaemonSnapshot(
  snapshot: DesktopDaemonSnapshot,
  sessionId: string,
): SessionV3SessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  const session = snapshot.sessionsById?.[normalizedSessionId]
  if (!normalizedSessionId || !session) {
    return null
  }
  const activePlan = snapshot.plansBySessionId?.[normalizedSessionId] ?? null
  const messages = snapshot.messagesBySessionId?.[normalizedSessionId] ?? []
  const appliedSeq = Math.max(0, session.lastEventSeq ?? 0)
  const highWatermark = Math.max(0, session.projectionHighWatermarkSeq ?? appliedSeq)
  const pendingPermissions = Object.values(snapshot.permissionsById ?? {})
    .filter((permission) => permission.sessionId === normalizedSessionId && permission.status.trim().toLowerCase() === 'pending')
  return {
    source: 'v3',
    session,
    messages,
    events: [],
    projection: null,
    preference: snapshot.preferencesBySessionId?.[normalizedSessionId] ?? null,
    agentModelPolicy: snapshot.agentModelPolicyBySessionId?.[normalizedSessionId] ?? null,
    pendingPermissions,
    usage: snapshot.usageBySessionId?.[normalizedSessionId] ?? null,
    runIntent: snapshot.runIntentsBySessionId?.[normalizedSessionId] ?? session.runIntent ?? null,
    hasActivePlan: Boolean(activePlan?.id || activePlan?.plan || activePlan?.title),
    activePlan,
    planRevisions: snapshot.planRevisionsBySessionId?.[normalizedSessionId] ?? [],
    appliedSeq,
    highWatermark,
    hydratedAt: Date.now(),
  }
}

function toSessionV3CreateRequestWire(input: SessionV3CreateSessionInput): SessionV3JsonRecord {
  const workspacePath = input.workspacePath.trim()
  if (!workspacePath) {
    throw new Error('Sessions API v3 create requires workspacePath.')
  }
  const preference = input.preference ?? {}
  return stripUndefinedFields({
    session_id: input.sessionId?.trim() || undefined,
    client_request_id: input.clientRequestId?.trim() || `desktop-v3-create:${crypto.randomUUID()}`,
    title: input.title?.trim() || undefined,
    workspace_path: workspacePath,
    workspace_name: input.workspaceName?.trim() || undefined,
    workspace_binding_id: input.workspaceBindingId?.trim() || undefined,
    swarm_id: input.swarmId?.trim() || undefined,
    target_kind: input.targetKind?.trim() || undefined,
    target_relationship: input.targetRelationship?.trim() || undefined,
    host_workspace_path: input.hostWorkspacePath?.trim() || undefined,
    runtime_workspace_path: input.runtimeWorkspacePath?.trim() || undefined,
    mode: input.mode?.trim() || undefined,
    agent_name: input.agentName?.trim() || undefined,
    metadata: input.metadata,
    preference: stripUndefinedFields({
      provider: preference.provider?.trim() || undefined,
      model: preference.model?.trim() || undefined,
      thinking: preference.thinking?.trim() || undefined,
      service_tier: preference.serviceTier?.trim() || undefined,
      context_mode: preference.contextMode?.trim() || undefined,
    }),
    worktree_mode: input.worktreeMode?.trim() || undefined,
    worktree_use_current_branch: typeof input.worktreeUseCurrentBranch === 'boolean' ? input.worktreeUseCurrentBranch : undefined,
    worktree_base_branch: input.worktreeBaseBranch?.trim() || undefined,
    worktree_branch_name: input.worktreeBranchName?.trim() || undefined,
  }) ?? { workspace_path: workspacePath }
}

function stripUndefinedFields<T extends SessionV3JsonRecord>(value: T): T | undefined {
  for (const key of Object.keys(value)) {
    if (value[key] === undefined) {
      delete value[key]
    }
  }
  return Object.keys(value).length > 0 ? value : undefined
}

function toSessionV3SnapshotRequestWire(input: SessionV3StateSnapshotRequest): SessionV3StateSnapshotRequestWire {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const history = { ...DEFAULT_SNAPSHOT_HISTORY, ...(input.history ?? {}) }
  const resources = input.resources ?? {}
  return {
    surface: SESSION_V3_SURFACE,
    selector_kind: sessionV3SelectorKind(input),
    selector: sessionV3SelectorWire(input),
    session_ids: sessionIds.length > 0 ? sessionIds : undefined,
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
    include_active: input.includeActive || undefined,
    known_sessions: input.knownSessions,
  }
}

function toSessionV3StreamRequestWire(input: SessionV3StateSnapshotRequest & { endpointCursor: string; limit?: number }): SessionV3SyncStreamRequestWire {
  const endpointCursor = input.endpointCursor.trim()
  if (!endpointCursor) {
    throw new Error('Sessions API v3 sync stream requires endpointCursor.')
  }
  const body = toSessionV3SnapshotRequestWire(input) as SessionV3SyncStreamRequestWire
  body.endpoint_cursor = endpointCursor
  if (typeof input.limit === 'number' && Number.isFinite(input.limit) && input.limit > 0) {
    body.limit = Math.floor(input.limit)
  }
  return body
}


function sessionV3SelectorKind(input: SessionV3StateSnapshotRequest): string {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const workspacePath = input.workspacePath?.trim() ?? ''
  const workspacePaths = (input.workspacePaths ?? []).map((path) => path.trim()).filter(Boolean)
  if (sessionIds.length > 0) return 'session_ids'
  if (input.global) return 'global'
  if (workspacePath || workspacePaths.length > 0) return 'workspace'
  if (input.recent?.limit) return 'recent'
  return 'global'
}

function sessionV3SelectorWire(input: SessionV3StateSnapshotRequest): SessionV3WorksetSelectorWire {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const workspacePath = input.workspacePath?.trim() ?? ''
  const workspacePaths = (input.workspacePaths ?? []).map((path) => path.trim()).filter(Boolean)
  return {
    kind: sessionV3SelectorKind(input),
    global: input.global || undefined,
    session_ids: sessionIds.length > 0 ? sessionIds : undefined,
    workspace_path: workspacePath || undefined,
    workspace_paths: workspacePaths.length > 0 ? workspacePaths : undefined,
    recent: input.recent
      ? {
          limit: input.recent.limit,
          before_updated_at: input.recent.beforeUpdatedAt ?? null,
          before_session_id: input.recent.beforeSessionId?.trim() || undefined,
        }
      : undefined,
  }
}

function normalizeSessionV3PreferenceResponse(response: SessionV3PreferenceResponseWire): ResolvedSessionPreference {
  const preference = response.preference ?? {}
  return {
    preference: {
      provider: String(preference.provider ?? '').trim(),
      model: String(preference.model ?? '').trim(),
      thinking: String(preference.thinking ?? '').trim(),
      serviceTier: String(preference.service_tier ?? '').trim(),
      contextMode: String(preference.context_mode ?? '').trim(),
      updatedAt: numberValue(preference.updated_at),
    },
    contextWindow: numberValue(response.context_window),
    maxOutputTokens: numberValue(response.max_output_tokens),
  }
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

