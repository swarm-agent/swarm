import { apiFetch, readErrorMessage } from '../../../app/api'
import { dedupeAndTrimMessages } from '../chat/services/message-cache'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { applyCanonicalReasoningEventToLiveHistory } from './live-reasoning-history'
import type { ChatMessageRecord } from '../chat/types/chat'
import type { DesktopRunIntentRecord, DesktopSessionRecord } from '../types/realtime'
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
  }
  includeActive?: boolean
}

interface DesktopStateSnapshotRequestWire {
  session_ids?: string[]
  global?: boolean
  workspace?: { workspace_path?: string; workspace_paths?: string[] }
  recent?: { limit?: number; before_updated_at?: number | null; before_session_id?: string }
  history?: { mode?: string; max_messages_per_session?: number; max_events_per_session?: number; manifest_policy?: string; include_events?: boolean }
  resources?: { messages?: boolean; events?: boolean; run_intents?: boolean }
  include_active?: boolean
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

export interface RunIntentWire {
  session_id?: string
  run_id?: string
  status?: string
  blocked_reason?: string
  created_at?: number
  updated_at?: number
  event_seq?: number
}


interface EventWire {
  id?: string
  session_id?: string
  seq?: number
  event_type?: string
  ts_unix_ms?: number
  payload?: Record<string, unknown>
}

export interface DesktopStateWorksetResponseWire {
  rev?: number
  snapshot_endpoint_cursor?: string
  sessions_by_id?: Record<string, SessionWire>
  messages_by_session?: Record<string, MessageWire[]>
  events_by_session?: Record<string, EventWire[]>
  run_intents_by_session?: Record<string, RunIntentWire[]>
  current_run_intent_by_session?: Record<string, RunIntentWire>
  session_order?: string[]
}

const DEFAULT_SNAPSHOT_HISTORY = {
  mode: 'none' as const,
  maxEventsPerSession: 0,
  manifestPolicy: 'manifest' as const,
  includeEvents: false,
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
  const rawRunIntentsBySessionId = currentRunIntentBySessionId(response.current_run_intent_by_session)
  const sessionsById = mapSessions(response.sessions_by_id, rawRunIntentsBySessionId)
  const runIntentsBySessionId = activeRunIntentsForNonTerminalSessions(rawRunIntentsBySessionId, sessionsById)
  applyReasoningEventsBySession(sessionsById, response.events_by_session)
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
    permissionsById: {},
    plansBySessionId: {},
    planRevisionsBySessionId: {},
    usageBySessionId: {},
    runIntentsBySessionId,
    workspacesByPath: workspacesByPath(Object.values(sessionsById)),
    routeReadinessBySessionId: readySessions(sessionOrder),
  }
}

function toSnapshotRequestWire(input: DesktopStateSnapshotRequest): DesktopStateSnapshotRequestWire {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const workspacePath = input.workspacePath?.trim() ?? ''
  const workspacePaths = (input.workspacePaths ?? []).map((path) => path.trim()).filter(Boolean)
  const history = { ...DEFAULT_SNAPSHOT_HISTORY, ...(input.history ?? {}) }
  const resources = input.resources ?? {}
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
    },
    include_active: input.includeActive || undefined,
  }
}

function assertSnapshotRevision(rev: unknown): asserts rev is number {
  if (typeof rev !== 'number' || !Number.isFinite(rev) || rev < 0) {
    throw new Error('Desktop state snapshot requires a valid daemon rev.')
  }
}

function applyReasoningEventsBySession(
  sessionsById: Record<string, DesktopSessionRecord>,
  eventsBySession: Record<string, EventWire[]> | undefined,
): void {
  for (const [sessionId, events] of Object.entries(eventsBySession ?? {})) {
    const session = sessionsById[sessionId]
    if (!session) {
      continue
    }
    for (const event of events.slice().sort((left, right) => numberValue(left.seq) - numberValue(right.seq))) {
      const eventType = String(event.event_type ?? '').trim()
      if (eventType !== 'session.reasoning.started' && eventType !== 'session.reasoning.delta' && eventType !== 'session.reasoning.completed') {
        continue
      }
      const payload = event.payload && typeof event.payload === 'object' ? event.payload : null
      if (!payload) {
        continue
      }
      const seq = numberValue(event.seq)
      const ts = numberValue(event.ts_unix_ms)
      applyCanonicalReasoningEventToLiveHistory(session.live, payload, eventType, ts, seq)
    }
  }
}

function mapSessions(
  source: Record<string, SessionWire> | undefined,
  runIntentsBySessionId: Record<string, DesktopRunIntentRecord>,
): Record<string, DesktopSessionRecord> {
  const sessions: Record<string, DesktopSessionRecord> = {}
  for (const [fallbackId, session] of Object.entries(source ?? {})) {
    const explicitId = String(session.id ?? '').trim()
    const topLevelRunIntent = runIntentsBySessionId[explicitId] ?? runIntentsBySessionId[fallbackId] ?? null
    const mapped = mapSession(session, fallbackId, topLevelRunIntent)
    if (mapped.id) {
      sessions[mapped.id] = mapped
    }
  }
  return sessions
}

function mapSession(session: SessionWire, fallbackId = '', topLevelRunIntent: DesktopRunIntentRecord | null = null): DesktopSessionRecord {
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
  const baseLive = emptyLiveState()
  const mappedRunIntent = session.run_intent ? mapRunIntent(session.run_intent) : topLevelRunIntent
  const runIntent = mappedRunIntent
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
    permissionsHydrated: false,
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
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

export function snapshotRunIntentStatusActive(status: string): boolean {
  const normalized = status.trim().toLowerCase()
  return normalized === 'pending_executor' || normalized === 'running'
}

function activeRunIntentsForNonTerminalSessions(
  source: Record<string, DesktopRunIntentRecord>,
  _sessionsById: Record<string, DesktopSessionRecord>,
): Record<string, DesktopRunIntentRecord> {
  const next: Record<string, DesktopRunIntentRecord> = {}
  for (const [sessionId, runIntent] of Object.entries(source)) {
    if (snapshotRunIntentStatusActive(runIntent.status)) {
      next[sessionId] = runIntent
    }
  }
  return next
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

function currentRunIntentBySessionId(source: Record<string, RunIntentWire> | undefined): Record<string, DesktopRunIntentRecord> {
  const bySessionId: Record<string, DesktopRunIntentRecord> = {}
  for (const [sessionId, intent] of Object.entries(source ?? {})) {
    if (intent) {
      bySessionId[sessionId] = mapRunIntent({ ...intent, session_id: intent.session_id ?? sessionId })
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
    terminalRunId: null,
    terminalEventSeq: 0,
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
    reasoningCompletedAt: null,
    reasoningTimelineSeq: 0,
    reasoningHistory: [],
    awaitingAck: false,
  }
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}
