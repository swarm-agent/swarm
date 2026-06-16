import { useSyncExternalStore } from 'react'
import { createEmptyWorkspaceTodoSummary, type WorkspaceTodoSummary } from '../../workspaces/todos/types'
import type { QueryClient } from '@tanstack/react-query'
import { queryClient } from '../../../app/query-client'
import { ensureDesktopSession } from '../../../app/api'
import {
  canonicalSessionWorkspaceName,
  canonicalSessionWorkspacePath,
  sessionWorkspaceFactsFromMetadata,
} from '../services/session-workspace'
import { applyDesktopChatRouteToSession, getDesktopSessionCreateTarget, getDesktopSessionStopTarget } from '../chat/services/chat-routing'
import {
  syncWorkspaceOverviewSession,
  syncWorkspaceOverviewThemeState,
  syncWorkspaceOverviewWorktreeState,
} from '../../workspaces/launcher/services/workspace-overview-cache'
import { setWorkspaceThemeCustomOptions } from '../../workspaces/launcher/services/workspace-theme'
import { DesktopV3RealtimeController, type DesktopV3RealtimeDiagnostics, type DesktopV3RealtimeFrame } from '../realtime/v3-realtime-controller'
import { DesktopSessionV3Runtime } from '../session-v3/runtime'
import type { SessionV3ReducerResult, SessionV3ReducerState } from '../session-v3/reducer'
import type { DesktopV3RealtimeTransportDiagnostics } from '../session-v3/transport'
import { disableVault, enableVault, exportVaultBundle, fetchVaultStatus, importVaultBundle, lockVault, unlockVault } from '../vault/api'
import {
  mapDesktopSessionUsageSummary,
} from '../chat/queries/chat-queries'
import type { DesktopChatRoute } from '../chat/services/chat-routing'
import { gitStatusQueryKey } from '../git/api'
import { agentStateQueryOptions, uiSettingsQueryKey } from '../../queries/query-options'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { applyV3RuntimeEnvelope, createV3EventEnvelope, createV3SnapshotEnvelope, getV3RuntimeDesktopSnapshot, installV3RuntimePersistence, normalizeV3RealtimeFrame, restoreV3RuntimeFromPersistence } from '../v3-runtime'
import { fetchDesktopStateSnapshot } from './desktop-state-snapshot'
import { fetchAndApplyDesktopV3SessionPermissions } from './desktop-v3-session-api'
import type { DesktopState } from './desktop-state'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import { normalizeSwarmSettings, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import type {
  DesktopNotificationCenterRecord,
  DesktopNotificationRecord,
  DesktopNotificationSummary,
  DesktopLiveToolRecord,
  DesktopRunIntentRecord,
  DesktopSessionRecord,
  DesktopStoreState,
} from '../types/realtime'
import type { ChatMessageRecord } from '../chat/types/chat'
import type { VaultStatus } from '../vault/types'
import { DesktopRunStreamController, type RunStreamEventMessage } from './run-stream-controller'
import { sessionRequiresSnapshotHydration } from './session-snapshot-hydration'
import { mergeSessionRecords } from './session-records'
import { appendLiveAssistantSegment } from './live-assistant-segments'
import { applyCanonicalReasoningEventToLiveHistory, completeLiveReasoningHistory } from './live-reasoning-history'
import { clearNotifications as clearDurableNotifications, fetchNotifications, fetchNotificationSummary, updateNotification } from '../notifications/api'
import type { DurableNotificationRecord, NotificationSummaryRecord } from '../notifications/types'

export interface EventEnvelope<T = Record<string, unknown>> {
  global_seq?: number
  source_seq?: number
  stream?: string
  event_type?: string
  entity_id?: string
  ts_unix_ms?: number
  payload?: T
}

type DraftFlushState = {
  assistantDraft: string
  reasoningSummary: string
  reasoningText: string
  reasoningState: DesktopSessionRecord['live']['reasoningState']
  reasoningSegment: number
  toolOutput?: string
}


type DesktopUiStateCreator<T extends object> = (
  set: (partial: Partial<T> | ((state: T) => Partial<T> | T)) => void,
  get: () => T,
) => T

type DesktopUiStoreHook<T extends object> = {
  <Selected>(selector: (state: T) => Selected): Selected
  getState: () => T
  setState: (partial: Partial<T> | ((state: T) => Partial<T> | T), replace?: boolean) => void
}

function createDesktopUiStore<T extends object>(initializer: DesktopUiStateCreator<T>): DesktopUiStoreHook<T> {
  let state: T
  const listeners = new Set<() => void>()
  const getState = () => state
  const setState = (partial: Partial<T> | ((state: T) => Partial<T> | T), replace = false) => {
    const nextPartial = typeof partial === 'function' ? partial(state) : partial
    if (Object.is(nextPartial, state)) {
      return
    }
    state = replace ? nextPartial as T : { ...state, ...nextPartial }
    for (const listener of listeners) {
      listener()
    }
  }
  state = initializer(setState, getState)
  const subscribe = (listener: () => void) => {
    listeners.add(listener)
    return () => listeners.delete(listener)
  }
  const useStore = (<Selected,>(selector: (state: T) => Selected): Selected => {
    return useSyncExternalStore(
      subscribe,
      () => selector(state),
      () => selector(state),
    )
  }) as DesktopUiStoreHook<T>
  useStore.getState = getState
  useStore.setState = setState
  return useStore
}


function saveDesktopActiveSessionId(_sessionId: string | null): void {}
function saveDesktopActiveWorkspacePath(_workspacePath: string | null): void {}

const MAX_NOTIFICATIONS = 200
const EMPTY_NOTIFICATION_SUMMARY: DesktopNotificationSummary = {
  swarmID: '',
  totalCount: 0,
  unreadCount: 0,
  activeCount: 0,
  updatedAt: 0,
}
const RECONNECT_BASE_DELAY_MS = 1500
const RECONNECT_MAX_DELAY_MS = 15_000
const RECONNECT_JITTER_RATIO = 0.2
const NEW_SESSION_DRAFT_KEY_PREFIX = '__workspace__:'
const MAX_LIVE_TOOL_OUTPUT_CHARS = 4000
const MAX_LIVE_TOOL_HISTORY = 20
const USER_STOP_SUMMARY = 'Run paused by user'

function userFacingRunStopReason(reason: string | null | undefined): string {
  const normalized = reason?.trim() ?? ''
  if (!normalized || normalized.toLowerCase() === 'run stopped by user') {
    return USER_STOP_SUMMARY
  }
  return normalized
}

function isTaskToolPayload(record: Record<string, unknown> | null): boolean {
  if (!record) {
    return false
  }
  const tool = typeof record.tool === 'string' ? record.tool.trim().toLowerCase() : ''
  const pathId = typeof record.path_id === 'string' ? record.path_id.trim().toLowerCase() : ''
  return tool === 'task' || pathId === 'tool.task.stream.v1' || pathId === 'tool.task.v1'
}
const draftFlushTimers = new Map<string, number>()
const pendingDraftFlush = new Map<string, DraftFlushState>()
const pendingSessionWorksetHydrations = new Set<string>()
let desktopRealtimeSocket: WebSocket | null = null
let desktopRealtimeLastActivityAt = 0
let desktopRealtimeConnectingStartedAt = 0
let desktopV3RealtimeController: DesktopV3RealtimeController | null = null
let desktopSessionV3Runtime: DesktopSessionV3Runtime | null = null
let desktopV3RealtimeEndpointCursor = ''
let desktopV3ReconnectSyncDiagnostics: Record<string, unknown> = { status: 'never-run' }
let runStreamController: DesktopRunStreamController | null = null

type DesktopV3FollowUpTraceStep = {
  name: string
  at: number
  elapsedMs: number
  details?: Record<string, unknown>
}

type DesktopV3FollowUpAssistantEvent = {
  at: number
  elapsedMs: number
  kind: string
  eventType: string
  seq: number
  deltaLength: number
  messageContentLength: number
  delta: string
  messageContent: string
}

type DesktopV3FollowUpTrace = {
  sessionId: string
  startedAt: number
  steps: DesktopV3FollowUpTraceStep[]
  assistantEvents: DesktopV3FollowUpAssistantEvent[]
  receivedAssistantText: string
  completedAssistantText: string
}

const activeDesktopV3FollowUpTraces = new Map<string, DesktopV3FollowUpTrace>()

function pushDesktopV3FollowUpTraceStep(
  trace: DesktopV3FollowUpTrace,
  name: string,
  details: Record<string, unknown> = {},
): void {
  const at = Date.now()
  trace.steps.push({
    name,
    at,
    elapsedMs: at - trace.startedAt,
    details,
  })
}

function summarizeLegacyDesktopV3RealtimeDiagnostics(diagnostics: DesktopV3RealtimeDiagnostics | null): Record<string, unknown> | null {
  if (!diagnostics) {
    return null
  }
  return {
    source: 'legacy-v3-controller',
    desired: diagnostics.desired,
    socketState: diagnostics.socketState,
    generation: diagnostics.generation,
    endpointCursorPresent: diagnostics.endpointCursorPresent,
    reconnectAttempt: diagnostics.reconnectAttempt,
    lastActivityAt: diagnostics.lastActivityAt,
    subscriptionCount: diagnostics.subscriptionCount,
    connectBlockedReason: diagnostics.connectBlockedReason,
    subscriptions: diagnostics.subscriptions.map((subscription) => ({
      sessionId: subscription.sessionId,
      subscriptionId: subscription.subscriptionId,
      endpointCursorPresent: subscription.endpointCursorPresent,
      subscribeSentAt: subscription.subscribeSentAt,
      subscribeSentCount: subscription.subscribeSentCount,
      lastFrameKind: subscription.lastFrameKind,
      lastEventType: subscription.lastEventType,
      lastFrameAt: subscription.lastFrameAt,
      lastReplayStartedAt: subscription.lastReplayStartedAt,
      lastReplayCompleteAt: subscription.lastReplayCompleteAt,
      lastEventAt: subscription.lastEventAt,
      lastEndpointCursorPresent: subscription.lastEndpointCursorPresent,
    })),
  }
}

function summarizeDesktopSessionV3RuntimeDiagnostics(diagnostics: DesktopV3RealtimeTransportDiagnostics | null): Record<string, unknown> | null {
  if (!diagnostics) {
    return null
  }
  return {
    source: 'desktop-session-v3-runtime',
    desired: diagnostics.desired,
    status: diagnostics.status,
    socketState: diagnostics.socketState,
    generation: diagnostics.generation,
    endpointCursorPresent: diagnostics.endpointCursorPresent,
    reconnectAttempt: diagnostics.reconnectAttempt,
    lastActivityAt: diagnostics.lastActivityAt,
    sessionSubscriptionCount: diagnostics.sessionSubscriptionCount,
    worksetSubscriptionCount: diagnostics.worksetSubscriptionCount,
    sessions: diagnostics.sessions.map((subscription) => ({
      sessionId: subscription.session_id,
      subscriptionId: subscription.subscription_id,
      endpointCursorPresent: Boolean(subscription.endpoint_cursor?.trim()),
      autoDiscovered: subscription.autoDiscovered,
      updatedAt: subscription.updatedAt,
    })),
    worksets: diagnostics.worksets.map((workset) => ({
      worksetId: workset.workset_id,
      subscriptionId: workset.subscription_id,
      autoSubscribeSessions: Boolean(workset.auto_subscribe_sessions),
      updatedAt: workset.updatedAt,
    })),
  }
}

function summarizeDesktopV3RealtimeDiagnostics(diagnostics: DesktopV3RealtimeDiagnostics | DesktopV3RealtimeTransportDiagnostics | null): Record<string, unknown> | null {
  if (!diagnostics) {
    return null
  }
  if ('sessionSubscriptionCount' in diagnostics) {
    return summarizeDesktopSessionV3RuntimeDiagnostics(diagnostics)
  }
  return summarizeLegacyDesktopV3RealtimeDiagnostics(diagnostics)
}

function desktopV3FollowUpConclusion(trace: DesktopV3FollowUpTrace, diagnostics: DesktopV3RealtimeDiagnostics | null): Record<string, unknown> {
  const subscription = diagnostics?.subscriptions[0]
  const connected = diagnostics?.socketState === 'open'
  const subscribed = Boolean(subscription && subscription.subscribeSentCount > 0)
  const streamFrameObserved = Boolean(subscription && (subscription.lastEventAt > 0 || subscription.lastFrameAt > 0))
  const receivedAssistantText = trace.receivedAssistantText.length > 0 || trace.completedAssistantText.length > 0
  let outcome = 'unknown'
  if (!diagnostics) {
    outcome = 'no-controller-diagnostics'
  } else if (!diagnostics.desired) {
    outcome = 'not-desired'
  } else if (diagnostics.subscriptionCount === 0) {
    outcome = 'subscription-missing-or-clobbered'
  } else if (!connected) {
    outcome = `socket-${diagnostics.socketState}`
  } else if (!subscribed) {
    outcome = 'socket-open-subscribe-not-sent'
  } else if (!streamFrameObserved) {
    outcome = 'subscribed-no-stream-frame-observed-yet'
  } else if (!receivedAssistantText) {
    outcome = 'stream-frames-arrived-but-no-assistant-text-received'
  } else {
    outcome = 'assistant-text-received-from-realtime'
  }
  return {
    outcome,
    connected,
    subscribed,
    streamFrameObserved,
    receivedAssistantText,
    assistantDeltaEventCount: trace.assistantEvents.filter((event) => event.eventType === 'session.assistant.delta').length,
    assistantCompletedEventCount: trace.assistantEvents.filter((event) => event.eventType === 'session.assistant.completed').length,
  }
}

function truncateDesktopV3FollowUpText(value: string): string {
  const limit = 4000
  return value.length > limit ? `${value.slice(0, limit)}…[truncated ${value.length - limit} chars]` : value
}

function recordDesktopV3FollowUpAssistantFrame(sessionId: string, payload: DesktopV3RealtimeFrame, ts: number): void {
  const trace = activeDesktopV3FollowUpTraces.get(sessionId)
  if (!trace) {
    return
  }
  const eventType = String(payload.event?.event_type ?? payload.event_type ?? payload.kind ?? payload.type ?? '').trim()
  if (eventType !== 'session.assistant.delta' && eventType !== 'session.assistant.completed') {
    return
  }
  const eventPayload = payload.event?.payload && typeof payload.event.payload === 'object'
    ? payload.event.payload as Record<string, unknown>
    : payload as Record<string, unknown>
  const delta = typeof eventPayload.delta === 'string' ? eventPayload.delta : ''
  const message = eventPayload.message && typeof eventPayload.message === 'object'
    ? eventPayload.message as Record<string, unknown>
    : null
  const messageContent = typeof message?.content === 'string' ? message.content : ''
  if (delta) {
    trace.receivedAssistantText += delta
  }
  if (messageContent) {
    trace.completedAssistantText = messageContent
  }
  trace.assistantEvents.push({
    at: ts,
    elapsedMs: ts - trace.startedAt,
    kind: String(payload.kind ?? payload.type ?? '').trim(),
    eventType,
    seq: typeof payload.event?.seq === 'number' ? payload.event.seq : 0,
    deltaLength: delta.length,
    messageContentLength: messageContent.length,
    delta: truncateDesktopV3FollowUpText(delta),
    messageContent: truncateDesktopV3FollowUpText(messageContent),
  })
}

function reportDesktopV3FollowUpTrace(trace: DesktopV3FollowUpTrace): void {
  activeDesktopV3FollowUpTraces.delete(trace.sessionId)
  const finalDiagnostics = desktopV3RealtimeController?.diagnostics(trace.sessionId) ?? null
  console.info('[desktop-v3-follow-up] submit/reconnect/stream report', {
    sessionId: trace.sessionId,
    durationMs: Date.now() - trace.startedAt,
    steps: trace.steps,
    assistantRealtime: {
      receivedAssistantText: trace.receivedAssistantText.length > 0 || trace.completedAssistantText.length > 0,
      deltaTextLength: trace.receivedAssistantText.length,
      completedTextLength: trace.completedAssistantText.length,
      deltaText: truncateDesktopV3FollowUpText(trace.receivedAssistantText),
      completedText: truncateDesktopV3FollowUpText(trace.completedAssistantText),
      events: trace.assistantEvents,
    },
    reconnectSync: desktopV3ReconnectSyncDiagnostics,
    finalRealtime: summarizeLegacyDesktopV3RealtimeDiagnostics(finalDiagnostics),
    conclusion: desktopV3FollowUpConclusion(trace, finalDiagnostics),
  })
}

function requireRunStreamController(): DesktopRunStreamController {
  if (!runStreamController) {
    throw new Error('run stream controller is not initialized')
  }
  return runStreamController
}

function requireV3RealtimeController(): DesktopV3RealtimeController {
  if (!desktopV3RealtimeController) {
    throw new Error('desktop v3 realtime controller is not initialized')
  }
  return desktopV3RealtimeController
}

export function getDesktopV3RealtimeDiagnostics(sessionId?: string | null): DesktopV3RealtimeTransportDiagnostics | DesktopV3RealtimeDiagnostics | null {
  const runtimeDiagnostics = desktopSessionV3Runtime?.diagnostics() ?? null
  if (runtimeDiagnostics && !sessionId?.trim()) {
    return runtimeDiagnostics
  }
  return desktopV3RealtimeController?.diagnostics(sessionId) ?? runtimeDiagnostics
}

function runtimeSessionWithActiveRunIntent(session: DesktopSessionRecord, runIntent: DesktopRunIntentRecord | null): DesktopSessionRecord {
  if (!runIntent || !v3RunIntentStatusActive(runIntent.status)) {
    return session
  }
  const next = { ...session, runIntent, live: { ...session.live } }
  next.live.runId = runIntent.runId
  next.live.status = runIntent.status.trim().toLowerCase() === 'pending_executor' ? 'starting' : 'running'
  next.live.startedAt = runIntent.createdAt > 0 ? runIntent.createdAt : next.live.startedAt
  next.live.lastEventType = 'session.run_intent.recorded'
  next.live.lastEventAt = runIntent.updatedAt > 0 ? runIntent.updatedAt : next.live.lastEventAt
  next.live.summary = runIntent.status.trim().toLowerCase() === 'pending_executor' ? 'Pending executor…' : next.live.summary
  return next
}

function desktopStoreSnapshotFromRuntimeDesktop(desktop: DesktopState): Partial<DesktopStoreState> {
  const sessions = { ...useDesktopUiStore.getState().sessions }
  for (const sessionId of desktop.sessionOrder.length > 0 ? desktop.sessionOrder : Object.keys(desktop.sessionsById)) {
    const session = desktop.sessionsById[sessionId]
    if (!session?.id) {
      continue
    }
    const runtimeSession = runtimeSessionWithActiveRunIntent(session, desktop.runIntentsBySessionId[session.id] ?? null)
    const merged = mergeExternalSessionRecord(sessions[session.id] ?? null, runtimeSession)
    sessions[session.id] = merged
    syncBlockedSessionToWorkspaceOverview(queryClient, merged)
  }
  const current = useDesktopUiStore.getState()
  const runtimeNotifications = Object.values(desktop.notificationsById)
  const notificationCenter = runtimeNotifications.length > 0 || desktop.notificationSummary.updatedAt > 0
    ? {
        ...current.notificationCenter,
        items: runtimeNotifications
          .sort((left, right) => right.updatedAt - left.updatedAt)
          .slice(0, MAX_NOTIFICATIONS),
        summary: desktop.notificationSummary,
        loading: false,
        hydrated: true,
      }
    : current.notificationCenter
  return {
    sessions,
    notificationCenter,
    lastGlobalSeq: Math.max(current.lastGlobalSeq, desktop.rev),
    activeWorkspacePath: resolveWorkspacePathForActiveSession({ ...current, sessions }, current.activeSessionId),
  }
}

function applyDesktopSessionV3RuntimeState(state: SessionV3ReducerState, result: SessionV3ReducerResult | null): void {
  const endpointCursor = state.endpointCursor.trim()
  if (endpointCursor) {
    desktopV3RealtimeEndpointCursor = endpointCursor
    desktopV3RealtimeController?.setEndpointCursor(endpointCursor)
  }
  if (!result || result.desktopChanged) {
    applyV3RuntimeEnvelope(createV3SnapshotEnvelope(state.desktop, {
      mode: 'merge',
      receivedAt: Date.now(),
      id: `desktop-session-v3-runtime:${state.mutationSeq}`,
      source: { kind: 'runtime', transport: 'memory', name: 'desktop-session-v3-runtime' },
    }))
    useDesktopUiStore.setState(desktopStoreSnapshotFromRuntimeDesktop(state.desktop))
  }
  const runtimeAction = result?.state.lastApply?.action ?? null
  if (runtimeAction === 'status') {
    if (state.status === 'ready') {
      const current = useDesktopUiStore.getState()
      clearReconnectTimer(current)
      clearHeartbeatTimer(current)
      clearLivenessTimer(current)
      useDesktopUiStore.setState({
        connectionState: 'open',
        reconnectAttempt: 0,
        reconnectTimer: null,
        heartbeatTimer: null,
        livenessTimer: null,
      })
    } else if (state.status === 'error') {
      useDesktopUiStore.setState({ connectionState: 'error' })
    }
  }
  if (result?.stale) {
    desktopV3ReconnectSyncDiagnostics = {
      ...desktopV3ReconnectSyncDiagnostics,
      status: 'stale',
      reason: result.reason,
      updatedAt: Date.now(),
    }
    useDesktopUiStore.setState({ connectionState: 'closed' })
  }
}

function desktopSocketStateName(socket: WebSocket | null): 'none' | 'connecting' | 'open' | 'closing' | 'closed' {
  switch (socket?.readyState) {
    case WebSocket.CONNECTING:
      return 'connecting'
    case WebSocket.OPEN:
      return 'open'
    case WebSocket.CLOSING:
      return 'closing'
    case WebSocket.CLOSED:
      return 'closed'
    default:
      return 'none'
  }
}

export function getDesktopRealtimeMaintenanceDiagnostics(sessionId?: string | null): Record<string, unknown> {
  const state = useDesktopUiStore.getState()
  const normalizedSessionId = sessionId?.trim() ?? ''
  const session = normalizedSessionId ? state.sessions[normalizedSessionId] ?? null : null
  const vaultAllowsRealtime = !state.vault.enabled || state.vault.unlocked
  const canFetch = canFetchRelativeDesktopAPI()
  const shouldMaintain = shouldMaintainDesktopRealtime(state)
  const v3Realtime = getDesktopV3RealtimeDiagnostics(normalizedSessionId)
  const legacyV3Realtime = desktopV3RealtimeController?.diagnostics(normalizedSessionId) ?? null
  return {
    canFetchRelativeDesktopAPI: canFetch,
    shouldMaintainDesktopRealtime: shouldMaintain,
    shouldMaintainBlocker: !state.realtimeDesired
      ? 'store.realtimeDesired=false'
      : !vaultAllowsRealtime
        ? 'store.vault-locked'
        : !canFetch
          ? 'store.relative-api-unavailable'
          : '',
    connectionState: state.connectionState,
    realtimeDesired: state.realtimeDesired,
    hydrated: state.hydrated,
    hydrating: state.hydrating,
    vaultEnabled: state.vault.enabled,
    vaultUnlocked: state.vault.unlocked,
    connectionGeneration: state.connectionGeneration,
    reconnectAttempt: state.reconnectAttempt,
    reconnectTimerActive: state.reconnectTimer !== null,
    heartbeatTimerActive: state.heartbeatTimer !== null,
    livenessTimerActive: state.livenessTimer !== null,
    globalSocketState: desktopSocketStateName(desktopRealtimeSocket),
    globalLastActivityAt: desktopRealtimeLastActivityAt,
    globalConnectingStartedAt: desktopRealtimeConnectingStartedAt,
    activeSessionId: state.activeSessionId,
    targetSessionId: normalizedSessionId,
    sessionPresent: session !== null,
    sessionApi: session?.sessionApi ?? '',
    sessionLastEventSeq: session?.lastEventSeq ?? 0,
    sessionLiveRunId: session?.live.runId ?? '',
    sessionLiveStatus: session?.live.status ?? '',
    sessionLiveAwaitingAck: session?.live.awaitingAck ?? false,
    sessionLifecyclePhase: session?.lifecycle?.phase ?? '',
    sessionLifecycleStopReason: session?.lifecycle?.stopReason ?? '',
    endpointCursorPresent: desktopV3RealtimeEndpointCursor.trim() !== '' || Boolean(desktopSessionV3Runtime?.getState().endpointCursor.trim()),
    lastV3ReconnectSync: desktopV3ReconnectSyncDiagnostics,
    v3RuntimeOwned: desktopSessionV3Runtime !== null,
    v3RuntimeState: desktopSessionV3Runtime
      ? {
          status: desktopSessionV3Runtime.getState().status,
          endpointCursorPresent: Boolean(desktopSessionV3Runtime.getState().endpointCursor.trim()),
          discoveredSessionCount: desktopSessionV3Runtime.getState().discoveredSessionIds.length,
          removedSessionCount: desktopSessionV3Runtime.getState().removedSessionIds.length,
          subscriptionCount: Object.keys(desktopSessionV3Runtime.getState().subscriptionsBySessionId).length,
          worksetCount: Object.keys(desktopSessionV3Runtime.getState().worksetsById).length,
        }
      : null,
    v3Realtime,
    legacyV3Realtime,
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('desktop:v3-realtime-snapshot-cursor', (event) => {
    const detail = (event as CustomEvent<{ endpointCursor?: string }>).detail
    const cursor = detail?.endpointCursor?.trim() ?? ''
    if (!cursor) {
      return
    }
    desktopV3RealtimeEndpointCursor = cursor
    requireV3RealtimeController().setEndpointCursor(cursor)
    if (desktopSessionV3Runtime) {
      void desktopSessionV3Runtime.refresh({ reason: 'external snapshot cursor' }).catch((error) => {
        console.error('[desktop-store] session v3 runtime cursor refresh failed', error)
      })
    }
    useDesktopUiStore.getState().syncV3RealtimeSessions()
  })
}

function mapDurableNotification(record: DurableNotificationRecord): DesktopNotificationCenterRecord {
  return {
    id: record.id,
    swarmID: record.swarm_id,
    originSwarmID: record.origin_swarm_id?.trim() || null,
    sessionId: record.session_id?.trim() || null,
    runId: record.run_id?.trim() || null,
    category: record.category,
    severity: record.severity,
    title: record.title,
    body: record.body,
    status: record.status,
    sourceEventType: record.source_event_type?.trim() || null,
    permissionId: record.permission_id?.trim() || null,
    toolName: record.tool_name?.trim() || null,
    requirement: record.requirement?.trim() || null,
    sessionTitle: record.session_title?.trim() || null,
    sessionLabel: record.session_label?.trim() || null,
    workspacePath: record.workspace_path?.trim() || null,
    workspaceName: record.workspace_name?.trim() || null,
    originLabel: record.origin_label?.trim() || null,
    actionURL: record.action_url?.trim() || null,
    readAt: typeof record.read_at === 'number' && record.read_at > 0 ? record.read_at : null,
    ackedAt: typeof record.acked_at === 'number' && record.acked_at > 0 ? record.acked_at : null,
    mutedAt: typeof record.muted_at === 'number' && record.muted_at > 0 ? record.muted_at : null,
    createdAt: typeof record.created_at === 'number' ? record.created_at : 0,
    updatedAt: typeof record.updated_at === 'number' ? record.updated_at : 0,
  }
}

function mapNotificationSummary(summary: NotificationSummaryRecord): DesktopNotificationSummary {
  return {
    swarmID: typeof summary.swarm_id === 'string' ? summary.swarm_id : '',
    totalCount: typeof summary.total_count === 'number' && Number.isFinite(summary.total_count) ? Math.max(0, summary.total_count) : 0,
    unreadCount: typeof summary.unread_count === 'number' && Number.isFinite(summary.unread_count) ? Math.max(0, summary.unread_count) : 0,
    activeCount: typeof summary.active_count === 'number' && Number.isFinite(summary.active_count) ? Math.max(0, summary.active_count) : 0,
    updatedAt: typeof summary.updated_at === 'number' && Number.isFinite(summary.updated_at) ? Math.max(0, summary.updated_at) : 0,
  }
}

function notificationCenterRecordsEqual(left: DesktopNotificationCenterRecord, right: DesktopNotificationCenterRecord): boolean {
  return left.id === right.id
    && left.swarmID === right.swarmID
    && left.originSwarmID === right.originSwarmID
    && left.sessionId === right.sessionId
    && left.runId === right.runId
    && left.category === right.category
    && left.severity === right.severity
    && left.title === right.title
    && left.body === right.body
    && left.status === right.status
    && left.sourceEventType === right.sourceEventType
    && left.permissionId === right.permissionId
    && left.toolName === right.toolName
    && left.requirement === right.requirement
    && left.sessionTitle === right.sessionTitle
    && left.sessionLabel === right.sessionLabel
    && left.workspacePath === right.workspacePath
    && left.workspaceName === right.workspaceName
    && left.originLabel === right.originLabel
    && left.actionURL === right.actionURL
    && left.readAt === right.readAt
    && left.ackedAt === right.ackedAt
    && left.mutedAt === right.mutedAt
    && left.createdAt === right.createdAt
    && left.updatedAt === right.updatedAt
}

function notificationSummariesEqual(left: DesktopNotificationSummary, right: DesktopNotificationSummary): boolean {
  return left.swarmID === right.swarmID
    && left.totalCount === right.totalCount
    && left.unreadCount === right.unreadCount
    && left.activeCount === right.activeCount
    && left.updatedAt === right.updatedAt
}

function deriveNotificationSummary(
  items: DesktopNotificationCenterRecord[],
  fallbackSwarmID: string,
  fallbackUpdatedAt: number,
): DesktopNotificationSummary {
  return {
    swarmID: fallbackSwarmID,
    totalCount: items.length,
    unreadCount: items.filter((item) => !item.readAt).length,
    activeCount: items.filter((item) => item.status === 'active').length,
    updatedAt: fallbackUpdatedAt,
  }
}

function notificationRecordFromRealtimePayload(payloadRecord: Record<string, unknown>): DurableNotificationRecord | null {
  const rawNotification = payloadRecord.notification
  if (!rawNotification || typeof rawNotification !== 'object') {
    return null
  }
  const record = rawNotification as Partial<DurableNotificationRecord>
  if (typeof record.id !== 'string' || record.id.trim() === '' || typeof record.swarm_id !== 'string' || record.swarm_id.trim() === '') {
    return null
  }
  return record as DurableNotificationRecord
}

function notificationSummaryFromRealtimePayload(payloadRecord: Record<string, unknown>): DesktopNotificationSummary | null {
  const rawSummary = payloadRecord.summary
  if (!rawSummary || typeof rawSummary !== 'object') {
    return null
  }
  return mapNotificationSummary(rawSummary as NotificationSummaryRecord)
}

function mergeNotificationCenterRecord(
  center: DesktopStoreState['notificationCenter'],
  record: DurableNotificationRecord,
  suppliedSummary: DesktopNotificationSummary | null = null,
): DesktopStoreState['notificationCenter'] {
  const mappedRecord = mapDurableNotification(record)
  const existingIndex = center.items.findIndex((item) => item.id === mappedRecord.id)
  const items = existingIndex >= 0
    ? notificationCenterRecordsEqual(center.items[existingIndex], mappedRecord)
      ? center.items
      : center.items.map((item, index) => index === existingIndex ? mappedRecord : item)
    : [mappedRecord, ...center.items].slice(0, MAX_NOTIFICATIONS)
  const incomingSummary = suppliedSummary ?? deriveNotificationSummary(
    items,
    mappedRecord.swarmID || center.summary.swarmID,
    mappedRecord.updatedAt || Date.now(),
  )
  const summary = {
    ...incomingSummary,
    updatedAt: Math.max(incomingSummary.updatedAt, center.summary.updatedAt),
  }
  const hydrated = true
  const loading = false
  if (notificationSummariesEqual(center.summary, summary) && items === center.items && center.hydrated === hydrated && center.loading === loading) {
    return center
  }
  updateBrowserNotificationSignals(summary)
  return {
    ...center,
    items,
    summary,
    loading,
    hydrated,
  }
}

function clearNotificationCenter(
  center: DesktopStoreState['notificationCenter'],
  summary: DesktopNotificationSummary,
): DesktopStoreState['notificationCenter'] {
  const stableSummary = {
    ...summary,
    updatedAt: Math.max(summary.updatedAt, center.summary.updatedAt),
  }
  const loading = false
  if (center.items.length === 0 && notificationSummariesEqual(center.summary, stableSummary) && center.hydrated && center.loading === loading) {
    return center
  }
  updateBrowserNotificationSignals(stableSummary)
  return {
    ...center,
    items: [],
    summary: stableSummary,
    loading,
    hydrated: true,
  }
}

function updateBrowserNotificationSignals(summary: DesktopNotificationSummary): void {
  if (typeof document !== 'undefined') {
    const baseTitle = document.title.replace(/^\(\d+\)\s*/, '').trim() || 'Swarm'
    document.title = summary.unreadCount > 0 ? `(${summary.unreadCount}) ${baseTitle}` : baseTitle
  }
  const navigatorWithBadge = typeof navigator !== 'undefined' ? navigator as Navigator & {
    setAppBadge?: (count?: number) => Promise<void>
    clearAppBadge?: () => Promise<void>
  } : null
  if (!navigatorWithBadge) {
    return
  }
  if (summary.unreadCount > 0 && typeof navigatorWithBadge.setAppBadge === 'function') {
    void navigatorWithBadge.setAppBadge(summary.unreadCount).catch(() => {})
    return
  }
  if (summary.unreadCount === 0 && typeof navigatorWithBadge.clearAppBadge === 'function') {
    void navigatorWithBadge.clearAppBadge().catch(() => {})
  }
}

function emptyVaultState(): DesktopStoreState['vault'] {
  return {
    bootstrapped: true,
    loading: false,
    enabled: false,
    unlocked: true,
    unlockRequired: false,
    storageMode: 'pebble/plain',
    warning: '',
    error: null,
    openSettingsOnUnlock: false,
  }
}

function applyVaultStatus(vault: DesktopStoreState['vault'], status: VaultStatus, overrides?: Partial<DesktopStoreState['vault']>): DesktopStoreState['vault'] {
  return {
    ...vault,
    bootstrapped: true,
    loading: false,
    enabled: status.enabled,
    unlocked: status.unlocked,
    unlockRequired: status.unlockRequired,
    storageMode: status.storageMode || (status.enabled ? 'pebble/vault' : 'pebble/plain'),
    warning: status.warning,
    error: null,
    ...overrides,
  }
}

function clearDesktopRuntimeState(state: DesktopStoreState): Partial<DesktopStoreState> {
  const socket = desktopRealtimeSocket
  if (state.reconnectTimer !== null) {
    window.clearTimeout(state.reconnectTimer)
  }
  clearHeartbeatTimer(state)
  clearLivenessTimer(state)
  desktopSessionV3Runtime?.stop('desktop runtime state cleared')
  desktopRealtimeSocket = null
  desktopRealtimeLastActivityAt = 0
  desktopRealtimeConnectingStartedAt = 0
  socket?.close()
  requireRunStreamController().closeAll()
  saveDesktopActiveSessionId(null)
  saveDesktopActiveWorkspacePath(null)
  return {
    sessions: {},
    notifications: [],
    notificationCenter: {
      items: [],
      summary: EMPTY_NOTIFICATION_SUMMARY,
      loading: false,
      hydrated: false,
    },
    activeSessionId: null,
    activeWorkspacePath: null,
    reconnectTimer: null,
    heartbeatTimer: null,
    livenessTimer: null,
    reconnectAttempt: 0,
    connectionGeneration: state.connectionGeneration + 1,
    realtimeDesired: false,
    connectionState: 'idle',
  }
}

function shouldMaintainDesktopRealtime(state: DesktopStoreState): boolean {
  return state.realtimeDesired && (!state.vault.enabled || state.vault.unlocked)
}

function reconnectDelayMs(attempt: number): number {
  const exponent = Math.max(0, attempt)
  const baseDelay = Math.min(RECONNECT_MAX_DELAY_MS, RECONNECT_BASE_DELAY_MS * (2 ** exponent))
  const jitterWindow = Math.max(1, Math.floor(baseDelay * RECONNECT_JITTER_RATIO))
  const jitterOffset = Math.floor((Math.random() * (jitterWindow * 2 + 1)) - jitterWindow)
  return Math.max(RECONNECT_BASE_DELAY_MS, baseDelay + jitterOffset)
}

function themeCustomOptionsSignature(settings?: UISettingsWire | null): string {
  return JSON.stringify(Array.isArray(settings?.theme?.custom_themes) ? settings.theme.custom_themes : [])
}

function themeCustomOptionsChanged(previous?: UISettingsWire | null, next?: UISettingsWire | null): boolean {
  return themeCustomOptionsSignature(previous) !== themeCustomOptionsSignature(next)
}


function clearReconnectTimer(state: DesktopStoreState): void {
  if (state.reconnectTimer !== null) {
    window.clearTimeout(state.reconnectTimer)
  }
}

function clearHeartbeatTimer(state: DesktopStoreState): void {
  if (state.heartbeatTimer !== null) {
    window.clearInterval(state.heartbeatTimer)
  }
}

function clearLivenessTimer(state: DesktopStoreState): void {
  if (state.livenessTimer !== null) {
    window.clearTimeout(state.livenessTimer)
  }
}

function scheduleReconnect(reason: string): void {
  const current = useDesktopUiStore.getState()
  if (!shouldMaintainDesktopRealtime(current)) {
    setConnectionClosed(current.connectionGeneration)
    return
  }
  clearReconnectTimer(current)
  clearHeartbeatTimer(current)
  clearLivenessTimer(current)
  desktopRealtimeSocket?.close()
  desktopRealtimeSocket = null
  desktopRealtimeLastActivityAt = 0
  desktopRealtimeConnectingStartedAt = 0
  const attempt = current.reconnectAttempt
  const timer = window.setTimeout(() => {
    const state = useDesktopUiStore.getState()
    if (state.reconnectTimer !== timer) {
      return
    }
    useDesktopUiStore.setState({ reconnectTimer: null, connectionState: 'closed' })
    if (!shouldMaintainDesktopRealtime(useDesktopUiStore.getState())) {
      return
    }
    void useDesktopUiStore.getState().connect()
  }, reconnectDelayMs(attempt))
  useDesktopUiStore.setState({
    reconnectTimer: timer,
    heartbeatTimer: null,
    livenessTimer: null,
    reconnectAttempt: attempt + 1,
    connectionState: 'closed',
  })
  console.warn(`[desktop-store] scheduled reconnect after ${reason}`)
}

function setConnectionClosed(generation: number): void {
  const state = useDesktopUiStore.getState()
  if (state.connectionGeneration !== generation) {
    return
  }
  clearReconnectTimer(state)
  clearHeartbeatTimer(state)
  clearLivenessTimer(state)
  useDesktopUiStore.setState({
    reconnectTimer: null,
    heartbeatTimer: null,
    livenessTimer: null,
    reconnectAttempt: 0,
    connectionState: state.realtimeDesired ? 'closed' : 'idle',
  })
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

function retainTail(value: string, maxChars: number): string {
  if (value.length <= maxChars) {
    return value
  }
  return '…' + value.slice(value.length - maxChars + 1)
}

function normalizeLiveToolText(value: string): string {
  return value.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
}

function resetLiveToolState(live: DesktopSessionRecord['live']): void {
  live.toolName = null
  live.toolCallId = null
  live.toolArguments = null
  live.toolOutput = ''
}

function resetSidebarLiveToolName(live: DesktopSessionRecord['live']): void {
  live.sidebarToolName = null
}

function retainLiveToolState(
  live: DesktopSessionRecord['live'],
  state: DesktopSessionRecord['live']['retainedToolState'],
): void {
  const toolName = live.toolName?.trim() ?? ''
  const toolCallId = live.toolCallId?.trim() ?? ''
  const toolArguments = live.toolArguments?.trim() ?? ''
  const toolOutput = live.toolOutput.trim()
  if (!toolName && !toolCallId && !toolArguments && !toolOutput) {
    return
  }
  live.retainedToolName = toolName || live.retainedToolName
  live.retainedToolCallId = toolCallId || live.retainedToolCallId
  live.retainedToolArguments = toolArguments || live.retainedToolArguments
  live.retainedToolOutput = toolOutput || live.retainedToolOutput
  live.retainedToolState = state
}

function liveToolKey(input: { sessionId: string; runId: string; stepId: string; callId: string; toolInstanceId: string }): string {
  return [input.sessionId, input.runId, input.stepId, input.callId, input.toolInstanceId].join('\u001f')
}

function upsertLiveToolHistory(
  live: DesktopSessionRecord['live'],
  input: {
    sessionId: string
    runId: string
    stepId: string
    callId: string
    toolInstanceId: string
    toolName: string
    toolArguments?: string | null
    output?: string | null
    rawOutput?: string | null
    completedOutput?: string | null
    state: DesktopLiveToolRecord['state']
    step?: number | null
    seq?: number | null
    ts: number
  },
): void {
  if (!input.runId || !input.stepId || !input.callId || !input.toolInstanceId) {
    return
  }
  const key = liveToolKey(input)
  const existing = (live.toolHistory ?? []).find((item) => item.key === key)
  const outputDelta = input.rawOutput ?? input.completedOutput ?? input.output ?? ''
  const existingOutput = existing?.toolOutput ?? ''
  const normalizedToolName = (input.toolName || existing?.toolName || '').trim().toLowerCase()
  const nextOutput = input.rawOutput !== undefined && input.rawOutput !== null
    ? replaceLiveToolOutput(input.rawOutput)
    : input.completedOutput !== undefined && input.completedOutput !== null
      ? replaceLiveToolOutput(input.completedOutput)
      : input.output
        ? normalizedToolName === 'task'
          ? mergedTaskToolDelta(existingOutput, input.output)
          : appendLiveToolOutput(existingOutput, input.output)
        : existingOutput
  const next: DesktopLiveToolRecord = {
    key,
    sessionId: input.sessionId,
    runId: input.runId,
    stepId: input.stepId,
    callId: input.callId,
    toolInstanceId: input.toolInstanceId,
    toolName: input.toolName || existing?.toolName || null,
    toolArguments: input.toolArguments ?? existing?.toolArguments ?? null,
    toolOutput: outputDelta ? nextOutput : existing?.toolOutput ?? '',
    state: input.state,
    step: input.step ?? existing?.step ?? null,
    seq: existing?.seq ?? input.seq ?? undefined,
    startedAt: existing?.startedAt ?? input.ts,
    updatedAt: input.ts,
    completedAt: input.state === 'done' || input.state === 'error' ? input.ts : existing?.completedAt ?? null,
  }
  const rest = (live.toolHistory ?? []).filter((item) => item.key !== key)
  live.toolHistory = [next, ...rest].slice(0, MAX_LIVE_TOOL_HISTORY)
}

function resetRetainedLiveToolState(live: DesktopSessionRecord['live']): void {
  live.retainedToolName = null
  live.retainedToolCallId = null
  live.retainedToolArguments = null
  live.retainedToolOutput = ''
  live.retainedToolState = null
}

function flushLiveAssistantDraftToSegment(live: DesktopSessionRecord['live'], createdAt: number): void {
  const draft = live.assistantDraft.trim()
  if (!draft) {
    return
  }
  live.retainedAssistantSegments = appendLiveAssistantSegment(live.retainedAssistantSegments, draft, createdAt, live.seq)
  live.assistantDraft = ''
}

function resetLiveAssistantState(live: DesktopSessionRecord['live']): void {
  live.assistantDraft = ''
  live.retainedAssistantSegments = []
}

function resetLiveReasoningState(live: DesktopSessionRecord['live']): void {
  live.reasoningSummary = ''
  live.reasoningText = ''
  live.reasoningState = 'idle'
  live.reasoningSegment = 0
  live.reasoningStartedAt = null
  live.reasoningCompletedAt = null
  live.reasoningTimelineSeq = 0
}

function completeLiveReasoningState(live: DesktopSessionRecord['live'], ts: number, seq: number, state: 'done' | 'error' = 'done'): void {
  if (live.reasoningState === 'idle') {
    return
  }
  live.reasoningState = state === 'error' ? 'error' : 'done'
  live.reasoningCompletedAt = live.reasoningCompletedAt ?? ts
  if ((live.reasoningTimelineSeq ?? 0) <= 0 && seq > 0) {
    live.reasoningTimelineSeq = seq
  }
}

function applyLiveReasoningSnapshot(session: DesktopSessionRecord, payload: Record<string, unknown>, eventType: string, ts: number, envelopeSeq: number): void {
  if (eventRevivesTerminalRun(session, payload)) {
    return
  }
  const next = applyCanonicalReasoningEventToLiveHistory(session.live, payload, eventType, ts, envelopeSeq)
  if (!next) {
    session.live.seq = Math.max(session.live.seq, envelopeSeq)
    session.live.lastEventType = eventType
    session.live.lastEventAt = ts
    return
  }
  session.live.runId = next.runId
  clearTerminalRunMarker(session.live, session.live.runId)
  if (eventType === 'session.reasoning.started') {
    flushLiveAssistantDraftToSegment(session.live, ts)
    session.live.reasoningSegment = Math.max(session.live.reasoningSegment, (session.live.reasoningHistory ?? []).length)
  }
  session.live.reasoningText = next.text
  session.live.reasoningSummary = next.summary
  session.live.reasoningState = next.state
  session.live.reasoningStartedAt = next.startedAt
  session.live.reasoningCompletedAt = next.completedAt
  session.live.reasoningTimelineSeq = next.timelineSeq
  session.live.status = 'running'
  session.live.awaitingAck = false
  session.live.startedAt = session.live.startedAt ?? ts
  session.live.summary = eventType === 'session.reasoning.completed' ? 'Thinking complete' : 'Thinking…'
  session.live.error = null
  session.live.seq = Math.max(session.live.seq, envelopeSeq)
  session.live.lastEventType = eventType
  session.live.lastEventAt = ts
}

function appendLiveToolOutput(current: string, chunk: string): string {
  const normalized = normalizeLiveToolText(chunk)
  if (normalized.trim() === '') {
    return current
  }
  return retainTail(current + normalized, MAX_LIVE_TOOL_OUTPUT_CHARS)
}

function replaceLiveToolOutput(value: string): string {
  const normalized = normalizeLiveToolText(value).trim()
  if (!normalized) {
    return ''
  }
  const parsed = parseToolDeltaOutputRecord(normalized)
  if (isTaskToolPayload(parsed)) {
    return JSON.stringify(parsed)
  }
  return retainTail(normalized, MAX_LIVE_TOOL_OUTPUT_CHARS)
}

function parseToolDeltaOutputRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value !== 'string') {
    return null
  }
  const trimmed = value.trim()
  if (!trimmed.startsWith('{') || !trimmed.endsWith('}')) {
    return null
  }
  try {
    const parsed = JSON.parse(trimmed) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : null
  } catch {
    return null
  }
}

function mergedTaskToolDelta(current: string, next: string): string {
  const nextRecord = parseToolDeltaOutputRecord(next)
  if (!nextRecord) {
    return appendLiveToolOutput(current, next)
  }
  const currentRecord = parseToolDeltaOutputRecord(current)
  const merged: Record<string, unknown> = {
    ...(currentRecord ?? {}),
    ...nextRecord,
  }
  const nextLaunches = Array.isArray(nextRecord.launches)
    ? nextRecord.launches.filter((entry): entry is Record<string, unknown> => Boolean(entry) && typeof entry === 'object' && !Array.isArray(entry))
    : []
  const currentLaunches = Array.isArray(currentRecord?.launches)
    ? currentRecord.launches.filter((entry): entry is Record<string, unknown> => Boolean(entry) && typeof entry === 'object' && !Array.isArray(entry))
    : []
  if (nextLaunches.length > 0 || currentLaunches.length > 0) {
    const launchMap = new Map<number, Record<string, unknown>>()
    for (const launch of currentLaunches) {
      const index = typeof launch.launch_index === 'number' ? launch.launch_index : launchMap.size + 1
      launchMap.set(index, launch)
    }
    for (const launch of nextLaunches) {
      const index = typeof launch.launch_index === 'number' ? launch.launch_index : launchMap.size + 1
      launchMap.set(index, {
        ...(launchMap.get(index) ?? {}),
        ...launch,
      })
    }
    merged.launches = Array.from(launchMap.entries())
      .sort((left, right) => left[0] - right[0])
      .map(([, launch]) => launch)
  }
  return JSON.stringify(merged)
}

function isDisplayableAgentLabel(value: unknown): value is string {
  if (typeof value !== 'string') {
    return false
  }
  const trimmed = value.trim()
  if (!trimmed) {
    return false
  }
  const normalized = trimmed.toLowerCase()
  return !normalized.includes('.')
}

function v3RunIntentStatusActive(status: string): boolean {
  const normalized = status.trim().toLowerCase()
  return normalized === 'pending_executor' || normalized === 'running'
}

function activeV3RunIntent(session: DesktopSessionRecord | undefined): DesktopRunIntentRecord | null {
  const intent = session?.runIntent
  return intent && v3RunIntentStatusActive(intent.status) ? intent : null
}

function resolveRunStreamId(session: DesktopSessionRecord | undefined, runId?: string | null): string {
  const explicitRunId = runId?.trim() ?? ''
  if (explicitRunId) {
    return explicitRunId
  }
  const intentRunId = activeV3RunIntent(session)?.runId.trim() ?? ''
  if (intentRunId) {
    return intentRunId
  }
  if (session && sessionUsesV3Api(session)) {
    return ''
  }
  const liveRunId = session?.live.runId?.trim() ?? ''
  if (liveRunId) {
    return liveRunId
  }
  return ''
}

function resolveStopRunId(session: DesktopSessionRecord | undefined, runId?: string | null): string {
  const explicitRunId = runId?.trim() ?? ''
  if (explicitRunId) {
    return explicitRunId
  }
  const intentRunId = activeV3RunIntent(session)?.runId.trim() ?? ''
  if (intentRunId) {
    return intentRunId
  }
  const lifecycleRunId = session?.lifecycle?.active ? session.lifecycle.runId?.trim() ?? '' : ''
  if (lifecycleRunId) {
    return lifecycleRunId
  }
  if (session && sessionUsesV3Api(session)) {
    return ''
  }
  return session?.live.runId?.trim() ?? ''
}

function resolveRunStreamResumeRequest(sessionId: string, fallbackRunId?: string | null): { sessionId: string; runId: string; lastSeq: number; sessionApi?: string | null; afterSeq?: number } | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  const state = useDesktopUiStore.getState()
  const session = state.sessions[normalizedSessionId]
  if (!session) {
    return null
  }
  const sessionApi = session.sessionApi?.trim().toLowerCase() || 'v3'
  const activeRunIntent = activeV3RunIntent(session)
  if (sessionApi === 'v3') {
    if (!activeRunIntent) {
      return null
    }
  } else if (session.live.status === 'idle' || session.live.status === 'error') {
    return null
  }
  const runId = resolveRunStreamId(session, fallbackRunId)
  if (!runId) {
    return null
  }
  if (session.live.summary?.trim() === 'Reconnecting…' && !activeRunIntent && !session.live.runId?.trim()) {
    return null
  }
  return {
    sessionId: normalizedSessionId,
    runId,
    lastSeq: sessionApi === 'v3'
      ? Math.max(0, session.lastEventSeq ?? session.live.seq ?? 0)
      : session.live.seq ?? 0,
    sessionApi: session.sessionApi,
    afterSeq: sessionApi === 'v3' ? Math.max(0, session.lastEventSeq ?? 0) : undefined,
  }
}

function normalizeLifecycle(
  input: Record<string, unknown> | null | undefined,
  fallbackSessionId: string,
): DesktopSessionRecord['lifecycle'] {
  if (!input || typeof input !== 'object') {
    return null
  }
  const sessionId = String(input.session_id ?? fallbackSessionId).trim()
  if (!sessionId) {
    return null
  }
  return {
    sessionId,
    runId: String(input.run_id ?? '').trim() || null,
    active: Boolean(input.active),
    phase: String(input.phase ?? input.status ?? '').trim(),
    startedAt: typeof input.started_at === 'number' ? input.started_at : 0,
    endedAt: typeof input.ended_at === 'number' ? input.ended_at : 0,
    updatedAt: typeof input.updated_at === 'number' ? input.updated_at : 0,
    generation: typeof input.generation === 'number' ? input.generation : 0,
    stopReason: String(input.stop_reason ?? '').trim() || null,
    error: String(input.error ?? '').trim() || null,
    ownerTransport: String(input.owner_transport ?? '').trim() || null,
  }
}

function applyLifecycleSnapshot(
  _sessionId: string,
  session: DesktopSessionRecord,
  lifecycle: NonNullable<DesktopSessionRecord['lifecycle']>,
  ts: number,
  eventType: string,
): void {
  session.lifecycle = lifecycle
  session.live.lastEventType = eventType || session.live.lastEventType
  session.live.lastEventAt = lifecycle.updatedAt > 0 ? lifecycle.updatedAt : ts
  const lifecycleRunId = lifecycle.runId?.trim() ?? ''
  if (lifecycle.active) {
    if (lifecycleRunId) {
      session.live.runId = lifecycleRunId
      clearTerminalRunMarker(session.live, lifecycleRunId)
    }
    session.live.startedAt = lifecycle.startedAt > 0 ? lifecycle.startedAt : session.live.startedAt ?? ts
    session.live.status = nextLiveStatusAfterPermissionSync(session)
    session.live.awaitingAck = false
    session.live.error = null
    return
  }
  const existingRunId = session.live.runId?.trim() ?? ''
  const existingIntentRunId = session.runIntent?.runId.trim() ?? ''
  if (!lifecycleRunId || lifecycleRunId === existingRunId || lifecycleRunId === existingIntentRunId) {
    session.runIntent = null
    markTerminalRun(session.live, lifecycleRunId || existingRunId || existingIntentRunId, 0)
    cancelDraftFlush(_sessionId)
    session.live.status = 'idle'
    session.live.runId = null
    session.live.startedAt = null
    session.live.awaitingAck = false
    session.live.summary = null
    session.live.error = null
    retainLiveToolState(session.live, 'done')
    resetLiveToolState(session.live)
  }
}

function makeNotification(sessionId: string | null, runId: string | null, eventType: string, title: string, detail: string, severity: 'info' | 'warning' | 'error', createdAt: number): DesktopNotificationRecord {
  return {
    id: `${createdAt}:${eventType}:${sessionId ?? 'global'}:${runId ?? 'none'}`,
    sessionId,
    runId,
    eventType,
    title,
    detail,
    severity,
    createdAt,
    source: 'session',
    swarmEnrollmentId: null,
    swarmChildName: null,
  }
}

function makeSwarmNotification(input: {
  eventType: string
  title: string
  detail: string
  severity: 'info' | 'warning' | 'error'
  createdAt: number
  enrollmentId?: string | null
  childName?: string | null
}): DesktopNotificationRecord {
  return {
    id: `${input.createdAt}:${input.eventType}:swarm:${input.enrollmentId ?? 'none'}`,
    sessionId: null,
    runId: null,
    eventType: input.eventType,
    title: input.title,
    detail: input.detail,
    severity: input.severity,
    createdAt: input.createdAt,
    source: 'swarm',
    swarmEnrollmentId: input.enrollmentId?.trim() || null,
    swarmChildName: input.childName?.trim() || null,
  }
}

function summarizePermission(permission: { reason: string; toolName: string; status: string }): string {
  if (permission.reason.trim() !== '') {
    return permission.reason
  }
  return `${permission.toolName} ${permission.status}`
}

function cancelDraftFlush(sessionId: string) {
  const timer = draftFlushTimers.get(sessionId)
  if (timer !== undefined) {
    if (typeof window !== 'undefined' && typeof window.cancelAnimationFrame === 'function') {
      window.cancelAnimationFrame(timer)
    }
    draftFlushTimers.delete(sessionId)
  }
  pendingDraftFlush.delete(sessionId)
}

function flushDraftState(sessionId: string) {
  const pending = pendingDraftFlush.get(sessionId)
  draftFlushTimers.delete(sessionId)
  if (!pending) {
    return
  }
  pendingDraftFlush.delete(sessionId)
  useDesktopUiStore.setState((state: DesktopStoreState) => {
    const existing = state.sessions[sessionId]
    if (!existing) {
      return state
    }
    return {
      sessions: {
        ...state.sessions,
        [sessionId]: {
          ...existing,
          live: {
            ...existing.live,
            assistantDraft: pending.assistantDraft,
            reasoningSummary: pending.reasoningSummary,
            reasoningText: pending.reasoningText,
            reasoningState: pending.reasoningState,
            reasoningSegment: Math.max(existing.live.reasoningSegment, pending.reasoningSegment),
            toolOutput: pending.toolOutput ?? existing.live.toolOutput,
          },
        },
      },
    }
  })
}

function scheduleDraftFlush(sessionId: string, draft: DraftFlushState) {
  const existing = pendingDraftFlush.get(sessionId)
  pendingDraftFlush.set(sessionId, {
    assistantDraft: draft.assistantDraft,
    reasoningSummary: draft.reasoningSummary,
    reasoningText: draft.reasoningText,
    reasoningState: draft.reasoningState,
    reasoningSegment: draft.reasoningSegment,
    toolOutput: draft.toolOutput ?? existing?.toolOutput,
  })
  if (draftFlushTimers.has(sessionId)) {
    return
  }
  const raf = typeof window !== 'undefined' && typeof window.requestAnimationFrame === 'function'
    ? window.requestAnimationFrame(() => flushDraftState(sessionId))
    : 0
  draftFlushTimers.set(sessionId, raf)
  if (raf === 0) {
    flushDraftState(sessionId)
  }
}

function normalizePermission(input: Record<string, unknown>): DesktopSessionRecord['pendingPermissions'][number] | null {
  const id = typeof input.id === 'string' ? input.id : ''
  const sessionId = typeof input.session_id === 'string' ? input.session_id : ''
  if (!id || !sessionId) {
    return null
  }
  return {
    id,
    sessionId,
    runId: typeof input.run_id === 'string' ? input.run_id : '',
    callId: typeof input.call_id === 'string' ? input.call_id : '',
    toolName: typeof input.tool_name === 'string' ? input.tool_name : '',
    toolArguments: typeof input.tool_arguments === 'string' ? input.tool_arguments : '',
    status: typeof input.status === 'string' ? input.status : '',
    decision: typeof input.decision === 'string' ? input.decision : '',
    reason: typeof input.reason === 'string' ? input.reason : '',
    requirement: typeof input.requirement === 'string' ? input.requirement : '',
    mode: typeof input.mode === 'string' ? input.mode : '',
    createdAt: typeof input.created_at === 'number' ? input.created_at : 0,
    updatedAt: typeof input.updated_at === 'number' ? input.updated_at : 0,
    resolvedAt: typeof input.resolved_at === 'number' ? input.resolved_at : 0,
    permissionRequestedAt: typeof input.permission_requested_at === 'number' ? input.permission_requested_at : 0,
  }
}

function normalizeMessage(message: RunStreamEventMessage['message'], fallbackSessionId: string): ChatMessageRecord | null {
  if (!message) {
    return null
  }
  const sessionId = String(message.session_id ?? fallbackSessionId).trim()
  const role = String(message.role ?? '').trim()
  const content = String(message.content ?? '')
  if (!sessionId || !role || content === '') {
    return null
  }
  const globalSeq = typeof message.global_seq === 'number' ? message.global_seq : 0
  return {
    id: String(message.id ?? `${sessionId}:${globalSeq}`).trim(),
    sessionId,
    globalSeq,
    role,
    content,
    createdAt: typeof message.created_at === 'number' ? message.created_at : Date.now(),
    metadata: message.metadata,
    toolMessage: parseStructuredToolMessage(content),
  }
}

function mapRunStreamUsageSummary(
  value: RunStreamEventMessage['usage_summary'],
  fallbackSessionId: string,
): DesktopSessionRecord['usage'] {
  if (!value || typeof value !== 'object') {
    return null
  }
  const sessionId = String(value.session_id ?? fallbackSessionId).trim() || fallbackSessionId
  const contextWindow = typeof value.context_window === 'number' ? value.context_window : 0
  const totalTokens = typeof value.total_tokens === 'number' ? value.total_tokens : 0
  const remainingTokens = typeof value.remaining_tokens === 'number' ? value.remaining_tokens : 0
  const updatedAt = typeof value.updated_at === 'number' ? value.updated_at : 0
  if (contextWindow <= 0 && totalTokens <= 0 && remainingTokens <= 0 && updatedAt <= 0) {
    return null
  }
  return {
    sessionId,
    provider: String(value.provider ?? '').trim(),
    model: String(value.model ?? '').trim(),
    source: String(value.source ?? '').trim(),
    contextWindow,
    totalTokens,
    remainingTokens,
    updatedAt,
  }
}

function ensureSession(state: DesktopStoreState, sessionId: string): DesktopSessionRecord {
  return state.sessions[sessionId] ?? {
    id: sessionId,
    title: 'New Session',
    workspacePath: '',
    workspaceName: '',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 0,
    createdAt: 0,
    permissionsHydrated: false,
    gitCommitDetected: false,
    gitCommitCount: 0,
    gitCommittedFileCount: 0,
    gitCommittedAdditions: 0,
    gitCommittedDeletions: 0,
    lifecycle: null,
    runIntent: null,
    live: emptyLiveState(),
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

function mergeExternalSessionRecord(existing: DesktopSessionRecord | null, incoming: DesktopSessionRecord): DesktopSessionRecord {
  const merged = mergeSessionRecords(existing, incoming)
  if (!existing) {
    return merged
  }

  const preserveHydratedPermissions = !incoming.permissionsHydrated && existing.permissionsHydrated
  const baselineLiveSnapshot =
    incoming.live.agentName === null &&
    incoming.live.lastEventAt === null &&
    incoming.live.lastEventType === null &&
    incoming.live.step === 0 &&
    incoming.live.toolName === null &&
    incoming.live.toolCallId === null &&
    incoming.live.toolArguments === null &&
    incoming.live.toolOutput === '' &&
    incoming.live.summary === null &&
    incoming.live.error === null &&
    incoming.live.seq === 0 &&
    incoming.live.assistantDraft === '' &&
    incoming.live.retainedAssistantSegments.length === 0 &&
    incoming.live.reasoningSummary === '' &&
    incoming.live.startedAt === null &&
    incoming.live.awaitingAck === false

  let next = merged
  if (preserveHydratedPermissions) {
    next = {
      ...next,
      permissionsHydrated: true,
      pendingPermissions: existing.pendingPermissions,
      pendingPermissionCount: existing.pendingPermissionCount,
    }
  }

  if (!baselineLiveSnapshot) {
    return next
  }

  return {
    ...next,
    live: preserveHydratedPermissions
      ? {
          ...existing.live,
          seq: Math.max(existing.live.seq, incoming.live.seq),
        }
      : {
          ...existing.live,
          status: incoming.live.status,
          runId: incoming.live.runId,
          retainedToolName: incoming.live.retainedToolName || existing.live.retainedToolName,
          retainedToolCallId: incoming.live.retainedToolCallId || existing.live.retainedToolCallId,
          retainedToolArguments: incoming.live.retainedToolArguments || existing.live.retainedToolArguments,
          retainedToolOutput: incoming.live.retainedToolOutput || existing.live.retainedToolOutput,
          retainedToolState: incoming.live.retainedToolState || existing.live.retainedToolState,
          seq: Math.max(existing.live.seq, incoming.live.seq),
        },
  }
}

function patchOverviewSessionStatus(session: DesktopSessionRecord): DesktopSessionRecord {
  const pendingPermissionCount = countApprovalRequiredPermissions(session.pendingPermissions, session.mode)
  return {
    ...session,
    pendingPermissionCount,
  }
}

function deferDesktopCacheMutation(label: string, mutate: () => void): void {
  const setTimeoutFn = typeof window !== 'undefined' ? window.setTimeout.bind(window) : setTimeout
  setTimeoutFn(() => {
    try {
      mutate()
    } catch (error) {
      console.error(`[desktop-store] deferred ${label} failed`, error)
    }
  }, 0)
}

function syncBlockedSessionToWorkspaceOverview(queryClient: QueryClient, session: DesktopSessionRecord): void {
  const normalizedSession = patchOverviewSessionStatus(session)
  deferDesktopCacheMutation('workspace overview sync', () => {
    syncWorkspaceOverviewSession(queryClient, normalizedSession)
  })
}

function invalidateAuthoritativeSessionSnapshot(sessionId: string): void {
  if (!sessionId.trim()) {
    return
  }
}

function canFetchRelativeDesktopAPI(): boolean {
  return typeof window !== 'undefined'
    && typeof window.location?.host === 'string'
    && window.location.host.trim() !== ''
}

function requestScopedSessionWorkset(sessionId: string, options: { force?: boolean } = {}): void {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId || !canFetchRelativeDesktopAPI() || (pendingSessionWorksetHydrations.has(normalizedSessionId) && !options.force)) {
    return
  }

  pendingSessionWorksetHydrations.add(normalizedSessionId)
  const setTimeoutFn = typeof window !== 'undefined' ? window.setTimeout.bind(window) : setTimeout
  setTimeoutFn(() => {
    void fetchDesktopStateSnapshot({
      sessionIds: [normalizedSessionId],
      history: { mode: 'none', maxEventsPerSession: 200, manifestPolicy: 'manifest', includeEvents: true },
      resources: { events: true, runIntents: true },
      includeActive: true,
    })
      .then((snapshot) => {
        applyV3RuntimeEnvelope(createV3SnapshotEnvelope(snapshot, { mode: 'merge', receivedAt: Date.now() }))
      })
      .catch((error) => {
        console.error('[desktop-store] scoped desktop v3 state hydration failed', error)
      })
      .finally(() => {
        pendingSessionWorksetHydrations.delete(normalizedSessionId)
      })
  }, 0)
}

function patchWorkspaceTodoSummary(workspacePath: string, summary: WorkspaceTodoSummary): void {
  deferDesktopCacheMutation('workspace todo summary sync', () => {
    queryClient.setQueriesData({ queryKey: ['workspace-overview'] }, (current: unknown) => {
      if (!current || typeof current !== 'object') {
        return current
      }
      const payload = current as { workspaces?: Array<Record<string, unknown>> }
      if (!Array.isArray(payload.workspaces)) {
        return current
      }
      return {
        ...payload,
        workspaces: payload.workspaces.map((workspace) => {
          if (typeof workspace.path !== 'string' || workspace.path.trim() !== workspacePath.trim()) {
            return workspace
          }
          return {
            ...workspace,
            todoSummary: summary,
          }
        }),
      }
    })
  })
}

function nextLiveStatusAfterPermissionSync(session: Pick<DesktopSessionRecord, 'runIntent' | 'lifecycle'>): DesktopSessionRecord['live']['status'] {
  if (session.runIntent && v3RunIntentStatusActive(session.runIntent.status)) {
    return session.runIntent.status.trim().toLowerCase() === 'pending_executor' ? 'starting' : 'running'
  }
  if (session.lifecycle?.active) {
    const phase = session.lifecycle.phase.trim().toLowerCase()
    if (phase === 'pending_executor' || phase === 'pending' || phase === 'queued' || phase === 'starting') {
      return 'starting'
    }
    if (phase === 'blocked' || phase === 'dispatch_blocked') {
      return 'blocked'
    }
    return 'running'
  }
  return 'idle'
}

function applyAuthoritativeSessionStatus(
  sessionId: string,
  session: DesktopSessionRecord,
  status: string,
  ts: number,
  eventType: string,
  details: {
    runId?: string | null
    summary?: string | null
    error?: string | null
  } = {},
): void {
  const runId = details.runId?.trim() ?? ''
  const summary = details.summary?.trim() ?? ''
  const error = details.error?.trim() ?? ''

  session.live.lastEventType = eventType || session.live.lastEventType
  session.live.lastEventAt = ts
  session.live.awaitingAck = false

  const runIntentTerminalStatus = eventType === 'session.run_intent.recorded' && (status === 'idle' || status === 'error' || status === 'blocked')
  if (runIntentTerminalStatus) {
    session.lifecycle = null
  }
  const hasCanonicalActiveRun = Boolean(activeV3RunIntent(session))
  if (sessionUsesV3Api(session) && (status === 'starting' || status === 'running' || status === 'blocked') && !hasCanonicalActiveRun) {
    if (summary) {
      session.live.summary = summary
    }
    if (error) {
      session.live.error = error
    }
    return
  }

  session.live.error = error || null

  if (summary) {
    session.live.summary = summary
  }
  if (runId) {
    if ((status === 'starting' || status === 'running') && eventRevivesTerminalRun(session, { run_id: runId })) {
      return
    }
    session.live.runId = runId
    clearTerminalRunMarker(session.live, runId)
  }

  switch (status) {
    case 'starting':
    case 'running':
    case 'blocked':
      session.live.status = status
      if ((status === 'starting' || status === 'running') && session.live.startedAt === null) {
        session.live.startedAt = ts
      }
      break
    case 'idle':
      cancelDraftFlush(sessionId)
      session.live.status = 'idle'
      session.live.runId = null
      session.live.startedAt = null
      retainLiveToolState(session.live, 'done')
      resetLiveToolState(session.live)
      session.live.summary = summary || null
      completeLiveReasoningState(session.live, ts, session.live.seq)
      completeLiveReasoningHistory(session.live, ts, session.live.seq)
      session.live.error = null
      break
    case 'error':
      session.live.status = 'error'
      session.live.runId = null
      session.live.startedAt = null
      retainLiveToolState(session.live, 'error')
      resetLiveToolState(session.live)
      session.live.summary = summary || null
      completeLiveReasoningState(session.live, ts, session.live.seq, 'error')
      completeLiveReasoningHistory(session.live, ts, session.live.seq, 'error')
      session.live.error = error || 'Run failed'
      break
    default:
      break
  }
}

function resolveWorkspacePathForActiveSession(state: DesktopStoreState, sessionId: string | null): string | null {
  const normalizedSessionId = sessionId?.trim() ?? ''
  if (!normalizedSessionId) {
    return state.activeWorkspacePath
  }
  return state.sessions[normalizedSessionId]?.workspacePath?.trim() || state.activeWorkspacePath
}

function draftKeyForSession(sessionId: string | null, workspacePath?: string | null): string {
  const normalizedSessionId = sessionId?.trim() ?? ''
  if (normalizedSessionId) {
    return normalizedSessionId
  }
  const normalizedWorkspacePath = workspacePath?.trim() ?? ''
  return `${NEW_SESSION_DRAFT_KEY_PREFIX}${normalizedWorkspacePath}`
}

function updateMessagesCache(sessionId: string, message: ChatMessageRecord, afterSync?: () => void): void {
  const normalizedSessionId = (message.sessionId || sessionId).trim()
  const messageId = message.id.trim()
  if (!normalizedSessionId || !messageId) {
    afterSync?.()
    return
  }

  const desktopSnapshot = getV3RuntimeDesktopSnapshot()
  if (desktopSnapshot.status === 'loading') {
    applyV3RuntimeEnvelope(createV3SnapshotEnvelope({ rev: 0 }, {
      id: 'http.message.commit.bootstrap',
      mode: 'replace',
      receivedAt: Date.now(),
      source: { kind: 'http', transport: 'http', name: 'v3-message-commit-bootstrap' },
    }))
  }
  const runtimeRev = getV3RuntimeDesktopSnapshot().rev
  applyV3RuntimeEnvelope(createV3EventEnvelope({
    type: 'desktop/message/upsert',
    payload: {
      message: {
        ...message,
        sessionId: normalizedSessionId,
      },
    },
    stream: `v3/session:${normalizedSessionId}`,
    entityId: normalizedSessionId,
    rev: runtimeRev + 1,
    prevRev: runtimeRev,
  }, {
    id: `http.message.commit:${normalizedSessionId}:${messageId}:${message.globalSeq > 0 ? message.globalSeq : 0}:${Date.now()}:${Math.random()}`,
    sessionId: normalizedSessionId,
    entityId: normalizedSessionId,
    source: { kind: 'http', transport: 'http', name: 'v3-message-commit' },
    receivedAt: Date.now(),
  }))
  afterSync?.()
}

function v3RunIntentPayload(payloadRecord: Record<string, unknown>): Record<string, unknown> | null {
  const runIntent = payloadRecord.run_intent
  return runIntent && typeof runIntent === 'object' ? runIntent as Record<string, unknown> : null
}

function v3PayloadString(payloadRecord: Record<string, unknown> | null | undefined, key: string): string {
  const value = payloadRecord?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function v3PayloadNumber(payloadRecord: Record<string, unknown> | null | undefined, key: string): number {
  const value = payloadRecord?.[key]
  return typeof value === 'number' ? value : 0
}

function v3DurableRunIntent(payloadRecord: Record<string, unknown>, eventType: string): (DesktopRunIntentRecord & { error: string }) | null {
  const nestedRunIntent = v3RunIntentPayload(payloadRecord)
  const runId = v3PayloadString(nestedRunIntent, 'run_id')
    || (eventType === 'session.run_intent.recorded' || eventType.startsWith('session.run.') ? v3PayloadString(payloadRecord, 'run_id') : '')
  const status = v3PayloadString(nestedRunIntent, 'status')
    || (eventType === 'session.run_intent.recorded' || eventType.startsWith('session.run.') ? v3PayloadString(payloadRecord, 'status') : '')
  if (!runId || !status) {
    return null
  }
  const blockedReason = v3PayloadString(nestedRunIntent, 'blocked_reason')
  return {
    sessionId: v3PayloadString(nestedRunIntent, 'session_id') || v3PayloadString(payloadRecord, 'session_id'),
    runId,
    status: status.toLowerCase(),
    blockedReason,
    createdAt: v3PayloadNumber(nestedRunIntent, 'created_at'),
    updatedAt: v3PayloadNumber(nestedRunIntent, 'updated_at') || v3PayloadNumber(payloadRecord, 'updated_at'),
    eventSeq: v3PayloadNumber(nestedRunIntent, 'event_seq'),
    error: v3PayloadString(payloadRecord, 'error') || blockedReason,
  }
}

function v3RunIntentStatusTerminal(status: string): boolean {
  switch (status.trim().toLowerCase()) {
    case 'completed':
    case 'failed':
    case 'cancelled':
    case 'expired':
    case 'interrupted':
    case 'dispatch_blocked':
      return true
    default:
      return false
  }
}

function v3TerminalRunIntent(payloadRecord: Record<string, unknown>, eventType: string): { runId: string; status: string; error: string } | null {
  const runIntent = v3DurableRunIntent(payloadRecord, eventType)
  return runIntent && v3RunIntentStatusTerminal(runIntent.status) ? runIntent : null
}

function markTerminalRun(live: DesktopSessionRecord['live'], runId: string | null | undefined, seq: number): void {
  const normalizedRunId = runId?.trim() ?? ''
  if (!normalizedRunId) {
    return
  }
  live.terminalRunId = normalizedRunId
  live.terminalEventSeq = Math.max(live.terminalEventSeq ?? 0, seq)
}

function clearTerminalRunMarker(live: DesktopSessionRecord['live'], runId: string | null | undefined): void {
  const normalizedRunId = runId?.trim() ?? ''
  if (!normalizedRunId || normalizedRunId === live.terminalRunId) {
    return
  }
  live.terminalRunId = null
  live.terminalEventSeq = 0
}

function eventRevivesTerminalRun(session: DesktopSessionRecord, payloadRecord: Record<string, unknown>): boolean {
  if (!sessionUsesV3Api(session)) {
    return false
  }
  const terminalRunId = session.live.terminalRunId?.trim() ?? ''
  if (!terminalRunId) {
    return false
  }
  const nestedRunIntent = payloadRecord.run_intent && typeof payloadRecord.run_intent === 'object'
    ? payloadRecord.run_intent as Record<string, unknown>
    : null
  const runId = typeof payloadRecord.run_id === 'string'
    ? payloadRecord.run_id.trim()
    : typeof nestedRunIntent?.run_id === 'string'
      ? nestedRunIntent.run_id.trim()
      : ''
  return runId === terminalRunId
}

function sessionUsesV3Api(session: DesktopSessionRecord): boolean {
  return session.sessionApi?.trim().toLowerCase() === 'v3'
}

function normalizeGlobalV3SessionPayload(eventType: string, payloadRecord: Record<string, unknown>): Record<string, unknown> {
  if (!eventType.startsWith('session.')) {
    return payloadRecord
  }
  const normalized: Record<string, unknown> = { ...payloadRecord }
  const nestedSession = normalized.session && typeof normalized.session === 'object'
    ? normalized.session as Record<string, unknown>
    : null
  const nestedMessage = normalized.message && typeof normalized.message === 'object'
    ? normalized.message as Record<string, unknown>
    : null
  const nestedLifecycle = normalized.lifecycle && typeof normalized.lifecycle === 'object'
    ? normalized.lifecycle as Record<string, unknown>
    : null
  const nestedRunIntent = normalized.run_intent && typeof normalized.run_intent === 'object'
    ? normalized.run_intent as Record<string, unknown>
    : null

  if (typeof normalized.session_id !== 'string') {
    if (typeof nestedSession?.id === 'string') {
      normalized.session_id = nestedSession.id
    } else if (typeof nestedMessage?.session_id === 'string') {
      normalized.session_id = nestedMessage.session_id
    } else if (typeof nestedLifecycle?.session_id === 'string') {
      normalized.session_id = nestedLifecycle.session_id
    } else if (typeof nestedRunIntent?.session_id === 'string') {
      normalized.session_id = nestedRunIntent.session_id
    }
  }

  if (eventType === 'session.created' || eventType === 'session.updated') {
    if (nestedSession) {
      return { ...nestedSession, ...normalized, session: nestedSession }
    }
    return normalized
  }
  if (eventType === 'session.title.updated' && nestedSession) {
    return {
      ...normalized,
      title: typeof normalized.title === 'string'
        ? normalized.title
        : typeof nestedSession.title === 'string'
          ? nestedSession.title
          : normalized.title,
      updated_at: typeof normalized.updated_at === 'number'
        ? normalized.updated_at
        : typeof nestedSession.updated_at === 'number'
          ? nestedSession.updated_at
          : normalized.updated_at,
    }
  }
  if (eventType === 'session.run_intent.recorded' && nestedRunIntent) {
    const status = typeof nestedRunIntent.status === 'string' ? nestedRunIntent.status.trim().toLowerCase() : ''
    if (status === 'pending_executor') {
      normalized.status = 'starting'
      normalized.summary = normalized.summary ?? 'Pending executor…'
    } else if (status === 'dispatch_blocked') {
      normalized.status = 'blocked'
      normalized.summary = normalized.summary ?? nestedRunIntent.blocked_reason ?? 'Dispatch blocked'
      normalized.error = normalized.error ?? nestedRunIntent.blocked_reason ?? null
    } else if (status === 'running') {
      normalized.status = 'running'
      normalized.summary = normalized.summary ?? 'Assistant responding…'
    } else if (status === 'completed') {
      normalized.status = 'idle'
    } else if (status === 'cancelled') {
      normalized.status = 'idle'
      normalized.summary = normalized.summary ?? userFacingRunStopReason(typeof nestedRunIntent.blocked_reason === 'string' ? nestedRunIntent.blocked_reason : '')
      normalized.error = null
    } else if (status === 'failed' || status === 'expired' || status === 'interrupted') {
      normalized.status = 'error'
      normalized.error = normalized.error ?? nestedRunIntent.blocked_reason ?? 'Run failed'
    }
    if (typeof nestedRunIntent.run_id === 'string') {
      normalized.run_id = nestedRunIntent.run_id
    }
    return normalized
  }
  return normalized
}

function deferAssistantFinalization(sessionId: string, message: ChatMessageRecord, assistantDraft: string): void {
  updateMessagesCache(message.sessionId, message, () => {
    useDesktopUiStore.setState((current: DesktopStoreState) => {
      const currentSession = current.sessions[sessionId]
      if (!currentSession || currentSession.live.assistantDraft !== assistantDraft) {
        return current
      }
      const nextSession = { ...currentSession, live: { ...currentSession.live } }
      resetLiveAssistantState(nextSession.live)
      return {
        sessions: {
          ...current.sessions,
          [sessionId]: nextSession,
        },
      }
    })
  })
}

function v3SessionStreamEventEnvelope(payload: RunStreamEventMessage): EventEnvelope | null {
  if (String(payload.type ?? '').trim() !== 'event' || !payload.event || typeof payload.event !== 'object') {
    return null
  }
  const event = payload.event
  const eventType = String(event.event_type ?? '').trim()
  const sessionId = String(event.session_id ?? payload.session_id ?? '').trim()
  const rawPayload = event.payload && typeof event.payload === 'object'
    ? { ...event.payload }
    : {}
  const nestedSession = rawPayload.session && typeof rawPayload.session === 'object'
    ? rawPayload.session as Record<string, unknown>
    : null
  const eventPayload = (eventType === 'session.created' || eventType === 'session.updated') && nestedSession
    ? { ...nestedSession }
    : rawPayload
  if (sessionId && typeof eventPayload.session_id !== 'string') {
    eventPayload.session_id = sessionId
  }
  return {
    global_seq: typeof event.seq === 'number' ? event.seq : undefined,
    source_seq: typeof event.seq === 'number' ? event.seq : undefined,
    stream: sessionId ? `v3/session:${sessionId}` : undefined,
    event_type: eventType,
    entity_id: sessionId,
    ts_unix_ms: typeof event.ts_unix_ms === 'number' ? event.ts_unix_ms : undefined,
    payload: eventPayload,
  }
}

function isV3ChildSessionStreamFrame(payload: RunStreamEventMessage): boolean {
  return String(payload.relation ?? '').trim().toLowerCase() === 'child'
}

function v3StreamTargetSessionId(parentSessionId: string, payload: RunStreamEventMessage): string {
  if (!isV3ChildSessionStreamFrame(payload)) {
    return parentSessionId
  }
  return String(payload.event?.session_id ?? payload.session_id ?? '').trim() || parentSessionId
}

function ensureChildStreamSession(
  state: DesktopStoreState,
  parentSessionId: string,
  childSessionId: string,
  payload: RunStreamEventMessage,
): DesktopSessionRecord | null {
  const normalizedChildSessionId = childSessionId.trim()
  if (!normalizedChildSessionId || normalizedChildSessionId === parentSessionId) {
    return null
  }
  const existing = state.sessions[normalizedChildSessionId]
  if (existing) {
    return existing
  }
  const parent = state.sessions[parentSessionId]
  const eventTs = typeof payload.event?.ts_unix_ms === 'number' && payload.event.ts_unix_ms > 0
    ? payload.event.ts_unix_ms
    : 1
  return {
    ...ensureSession(state, normalizedChildSessionId),
    title: 'Subagent',
    workspacePath: parent?.workspacePath ?? '',
    workspaceName: parent?.workspaceName ?? '',
    createdAt: eventTs,
    updatedAt: eventTs,
    metadata: {
      parent_session_id: parentSessionId,
      lineage_kind: String(payload.lineage_kind ?? 'delegated_subagent').trim() || 'delegated_subagent',
    },
    sessionApi: 'v3',
  }
}

function applyV3SessionStreamFrame(state: DesktopStoreState, sessionId: string, payload: RunStreamEventMessage, ts: number): Partial<DesktopStoreState> | null {
  const type = String(payload.type ?? '').trim()
  const targetSessionId = v3StreamTargetSessionId(sessionId, payload)
  if (type === 'run.stop.accepted') {
    const existing = state.sessions[targetSessionId]
    if (!existing) {
      return {}
    }
    return {
      sessions: {
        ...state.sessions,
        [targetSessionId]: mergeSessionRecords(existing, {
          ...existing,
          sessionApi: existing.sessionApi || 'v3',
          live: {
            ...existing.live,
            runId: String(payload.run_id ?? '').trim() || existing.live.runId,
            awaitingAck: false,
            error: null,
            summary: 'Stopping…',
            lastEventType: 'run.stop.accepted',
            lastEventAt: ts,
          },
        }),
      },
    }
  }
  if (type === 'session.run.cancelled') {
    const eventPayload: Record<string, unknown> = {
      session_id: targetSessionId,
      run_id: typeof payload.run_id === 'string' ? payload.run_id : undefined,
      status: typeof payload.status === 'string' ? payload.status : 'cancelled',
      error: typeof payload.error === 'string' ? payload.error : USER_STOP_SUMMARY,
      run_intent: payload.run_intent && typeof payload.run_intent === 'object'
        ? payload.run_intent
        : {
            session_id: targetSessionId,
            run_id: typeof payload.run_id === 'string' ? payload.run_id : undefined,
            status: 'cancelled',
            blocked_reason: typeof payload.error === 'string' ? payload.error : USER_STOP_SUMMARY,
          },
    }
    return applyEnvelope(state, {
      global_seq: typeof payload.seq === 'number' ? payload.seq : undefined,
      stream: `v3/session:${targetSessionId}`,
      event_type: 'session.run.cancelled',
      entity_id: targetSessionId,
      ts_unix_ms: ts,
      payload: eventPayload,
    })
  }
  if (type === 'keepalive' || type === 'endpoint.watermark') {
    const existing = state.sessions[targetSessionId]
    if (!existing) {
      return {}
    }
    const lastSeq = typeof payload.last_seq === 'number' ? Math.max(0, payload.last_seq) : existing.lastEventSeq ?? 0
    return {
      sessions: {
        ...state.sessions,
        [targetSessionId]: mergeSessionRecords(existing, {
          ...existing,
          sessionApi: existing.sessionApi || 'v3',
          lastEventSeq: Math.max(existing.lastEventSeq ?? 0, lastSeq),
          projectionHighWatermarkSeq: Math.max(existing.projectionHighWatermarkSeq ?? 0, lastSeq, typeof payload.high_watermark_seq === 'number' ? Math.max(0, payload.high_watermark_seq) : 0),
          live: {
            ...existing.live,
            lastEventType: type,
            lastEventAt: ts,
            awaitingAck: false,
            error: null,
          },
        }),
      },
    }
  }
  if (type === 'replay.started' || type === 'replay.complete') {
    const existing = state.sessions[targetSessionId]
    if (!existing) {
      return {}
    }
    const lastSeq = typeof payload.last_seq === 'number' ? Math.max(0, payload.last_seq) : existing.lastEventSeq ?? 0
    const highWatermark = typeof payload.high_watermark_seq === 'number'
      ? Math.max(0, payload.high_watermark_seq)
      : existing.projectionHighWatermarkSeq ?? lastSeq
    return {
      sessions: {
        ...state.sessions,
        [targetSessionId]: mergeSessionRecords(existing, {
          ...existing,
          sessionApi: existing.sessionApi || 'v3',
          lastEventSeq: Math.max(existing.lastEventSeq ?? 0, lastSeq),
          projectionHighWatermarkSeq: Math.max(existing.projectionHighWatermarkSeq ?? 0, highWatermark),
          live: {
            ...existing.live,
            lastEventType: type,
            lastEventAt: ts,
            awaitingAck: false,
            error: null,
          },
        }),
      },
    }
  }
  if (type === 'cursor.error') {
    const existing = state.sessions[targetSessionId]
    if (existing && !isV3ChildSessionStreamFrame(payload)) {
      requestScopedSessionWorkset(targetSessionId)
    }
    return {}
  }
  if (type === 'error') {
    const existing = state.sessions[targetSessionId]
    if (!existing) {
      return {}
    }
    return {
      sessions: {
        ...state.sessions,
        [targetSessionId]: mergeSessionRecords(existing, {
          ...existing,
          sessionApi: existing.sessionApi || 'v3',
          live: {
            ...existing.live,
            status: existing.lifecycle?.active ? existing.live.status : 'error',
            lastEventType: 'error',
            lastEventAt: ts,
            awaitingAck: false,
            error: String(payload.error ?? 'V3 session stream failed'),
            summary: existing.lifecycle?.active ? 'Stream error' : null,
          },
        }),
      },
    }
  }
  const envelope = v3SessionStreamEventEnvelope(payload)
  if (!envelope) {
    return null
  }
  const frameState = isV3ChildSessionStreamFrame(payload) && targetSessionId !== sessionId && !state.sessions[targetSessionId]
    ? {
        ...state,
        sessions: {
          ...state.sessions,
          [targetSessionId]: ensureChildStreamSession(state, sessionId, targetSessionId, payload) ?? ensureSession(state, targetSessionId),
        },
      }
    : state
  const patch = applyEnvelope(frameState, envelope)
  if (isV3ChildSessionStreamFrame(payload)) {
    patch.lastGlobalSeq = state.lastGlobalSeq
  }
  const eventSeq = typeof payload.event?.seq === 'number' ? Math.max(0, payload.event.seq) : 0
  const highWatermark = typeof payload.high_watermark_seq === 'number' ? Math.max(0, payload.high_watermark_seq) : eventSeq
  const rawV3EventType = String(payload.event?.event_type ?? type).trim()
  const v3EventType = rawV3EventType
  const patchedSessions = patch.sessions ?? frameState.sessions
  const existing = patchedSessions[targetSessionId]
  if (!existing) {
    return patch
  }
  return {
    ...patch,
    sessions: {
      ...patchedSessions,
      [targetSessionId]: mergeSessionRecords(null, {
        ...existing,
        sessionApi: existing.sessionApi || 'v3',
        lastEventSeq: Math.max(existing.lastEventSeq ?? 0, eventSeq),
        projectionHighWatermarkSeq: Math.max(existing.projectionHighWatermarkSeq ?? 0, highWatermark),
        live: {
          ...existing.live,
          lastEventType: v3EventType || type,
          lastEventAt: ts,
          awaitingAck: false,
          error: ['session.run.failed', 'session.run.cancelled', 'session.run.expired', 'session.run.interrupted', 'session.assistant.failed'].includes(v3EventType) ? existing.live.error : null,
        },
      }),
    },
  }
}

function applyRunStreamFrame(state: DesktopStoreState, sessionId: string, payload: RunStreamEventMessage, ts: number): Partial<DesktopStoreState> {
  const type = String(payload.type ?? '').trim()
  if (state.sessions[sessionId]?.sessionApi?.trim().toLowerCase() === 'v3') {
    return applyV3SessionStreamFrame(state, sessionId, payload, ts) ?? {}
  }
  const sessions = { ...state.sessions }
  const session = { ...ensureSession(state, sessionId), live: { ...ensureSession(state, sessionId).live }, pendingPermissions: [...ensureSession(state, sessionId).pendingPermissions] }
  const runID = String(payload.run_id ?? '').trim()
  const messageSessionID = String(payload.session_id ?? '').trim()

  if (messageSessionID && messageSessionID !== sessionId) {
    return {}
  }
  if (runID) {
    session.live.runId = runID
  }
  if (isDisplayableAgentLabel(payload.agent)) {
    session.live.agentName = payload.agent.trim()
  }
  if (typeof payload.seq === 'number') {
    session.live.seq = Math.max(session.live.seq, payload.seq)
  }
  session.live.lastEventType = type || session.live.lastEventType
  session.live.lastEventAt = ts
  const usage = mapRunStreamUsageSummary(payload.usage_summary, sessionId)
  if (usage) {
    session.usage = usage
  }

  switch (type) {
    case 'run.accepted':
    case 'resume.accepted':
    case 'keepalive':
      session.live.awaitingAck = false
      session.live.error = null
      break
    case 'run.stop.accepted':
      session.live.awaitingAck = false
      session.live.error = null
      session.live.summary = 'Stopping…'
      break
    case 'session.lifecycle.updated': {
      const lifecycleSource = payload.lifecycle && typeof payload.lifecycle === 'object'
        ? payload.lifecycle as Record<string, unknown>
        : payload as unknown as Record<string, unknown>
      const lifecycle = normalizeLifecycle(lifecycleSource, sessionId)
      if (lifecycle) {
        applyLifecycleSnapshot(sessionId, session, lifecycle, ts, type)
      }
      break
    }
    case 'session.status':
      applyAuthoritativeSessionStatus(sessionId, session, String(payload.status ?? '').trim(), ts, type, {
        runId: runID || session.live.runId,
        summary: typeof payload.summary === 'string' ? payload.summary : session.live.summary,
        error: typeof payload.error === 'string' ? payload.error : null,
      })
      break
    case 'assistant.delta': {
      const nextDraft = session.live.assistantDraft + String(payload.delta ?? '')
      session.live.assistantDraft = nextDraft
      scheduleDraftFlush(sessionId, {
        assistantDraft: nextDraft,
        reasoningSummary: session.live.reasoningSummary,
        reasoningText: session.live.reasoningText,
        reasoningState: session.live.reasoningState,
        reasoningSegment: session.live.reasoningSegment,
        toolOutput: session.live.toolOutput,
      })
      break
    }
    case 'tool.started':
    case 'tool.delta':
    case 'tool.completed':
    case 'run.tool.started':
    case 'run.tool.delta':
    case 'run.tool.completed': {
      const isToolStarted = type === 'tool.started' || type === 'run.tool.started'
      const isToolDelta = type === 'tool.delta' || type === 'run.tool.delta'
      const isToolCompleted = type === 'tool.completed' || type === 'run.tool.completed'
      if (isToolStarted) {
        flushLiveAssistantDraftToSegment(session.live, ts)
        cancelDraftFlush(sessionId)
      }
      session.live.toolName = String(payload.tool_name ?? '').trim() || session.live.toolName
      if (typeof payload.summary === 'string' && payload.summary.trim() !== '') {
        session.live.summary = payload.summary.trim()
      } else if (isToolStarted && session.live.toolName?.trim()) {
        session.live.summary = session.live.toolName.trim()
      }
      if (typeof payload.arguments === 'string') {
        session.live.toolArguments = payload.arguments.trim() || null
      }
      if (typeof payload.call_id === 'string' && payload.call_id.trim() !== '') {
        session.live.toolCallId = payload.call_id.trim()
      }
      if (isToolStarted) {
        resetRetainedLiveToolState(session.live)
        session.live.toolOutput = ''
        scheduleDraftFlush(sessionId, {
          assistantDraft: session.live.assistantDraft,
          reasoningSummary: session.live.reasoningSummary,
          reasoningText: session.live.reasoningText,
          reasoningState: session.live.reasoningState,
          reasoningSegment: session.live.reasoningSegment,
          toolOutput: session.live.toolOutput,
        })
      } else if (isToolDelta && typeof payload.output === 'string') {
        session.live.toolOutput = session.live.toolName === 'task'
          ? mergedTaskToolDelta(session.live.toolOutput, payload.output)
          : appendLiveToolOutput(session.live.toolOutput, payload.output)
        scheduleDraftFlush(sessionId, {
          assistantDraft: session.live.assistantDraft,
          reasoningSummary: session.live.reasoningSummary,
          reasoningText: session.live.reasoningText,
          reasoningState: session.live.reasoningState,
          reasoningSegment: session.live.reasoningSegment,
          toolOutput: session.live.toolOutput,
        })
      } else if (isToolCompleted) {
        const completedToolOutput = typeof payload.raw_output === 'string'
          ? replaceLiveToolOutput(payload.raw_output)
          : typeof payload.output === 'string'
            ? replaceLiveToolOutput(payload.output)
            : session.live.toolOutput
        session.live.toolOutput = completedToolOutput
        retainLiveToolState(session.live, 'done')
        resetLiveToolState(session.live)
      }
      if (typeof payload.step === 'number') {
        session.live.step = payload.step
      }
      break
    }
    case 'message.stored':
    case 'message.updated': {
      const normalized = normalizeMessage(payload.message, sessionId)
      if (normalized) {
        if (normalized.role === 'assistant') {
          const finalizedAssistantDraft = session.live.assistantDraft
          deferAssistantFinalization(sessionId, normalized, finalizedAssistantDraft)
          cancelDraftFlush(sessionId)
        } else {
          updateMessagesCache(normalized.sessionId, normalized)
        }
      }
      break
    }
    case 'turn.completed':
      if (!session.lifecycle) {
        cancelDraftFlush(sessionId)
        session.live.awaitingAck = false
        session.live.status = 'idle'
        session.live.startedAt = null
        retainLiveToolState(session.live, 'done')
        resetLiveToolState(session.live)
        session.live.summary = null
        completeLiveReasoningState(session.live, ts, session.live.seq)
      completeLiveReasoningHistory(session.live, ts, session.live.seq)
        session.live.error = null
        session.live.runId = null
      }
      break
    case 'turn.error':
    case 'error':
      if (!session.lifecycle) {
        cancelDraftFlush(sessionId)
        session.live.awaitingAck = false
        session.live.status = 'error'
        session.live.startedAt = null
        retainLiveToolState(session.live, 'error')
        resetLiveToolState(session.live)
        completeLiveReasoningState(session.live, ts, session.live.seq, 'error')
      completeLiveReasoningHistory(session.live, ts, session.live.seq, 'error')
        session.live.error = String(payload.error ?? 'Run failed')
        session.live.summary = null
        session.live.runId = null
      }
      break
    default:
      break
  }

  sessions[sessionId] = mergeSessionRecords(state.sessions[sessionId] ?? null, session)
  syncBlockedSessionToWorkspaceOverview(queryClient, sessions[sessionId])
  return { sessions }
}

function applyRunStreamSocketFailure(state: DesktopStoreState, sessionId: string, errorMessage: string, ts: number): Partial<DesktopStoreState> {
  const existing = state.sessions[sessionId]
  if (!existing) {
    return {}
  }

  const activeRunId = resolveRunStreamId(existing)
  if (!existing.live.awaitingAck && existing.live.status !== 'starting') {
    if (!activeRunId) {
      return {}
    }
    const sessions = { ...state.sessions }
    const session = {
      ...existing,
      live: {
        ...existing.live,
        status: 'running' as const,
        error: null,
        summary: 'Reconnecting…',
        lastEventType: 'run.reconnecting',
        lastEventAt: ts,
      },
    }
    sessions[sessionId] = mergeSessionRecords(existing, session)
    syncBlockedSessionToWorkspaceOverview(queryClient, sessions[sessionId])
    return { sessions }
  }

  const sessions = { ...state.sessions }
  const session = {
    ...existing,
    live: {
      ...existing.live,
      startedAt: null,
      awaitingAck: false,
      status: 'error' as const,
      error: errorMessage,
      summary: activeRunId ? 'Disconnected' : null,
      lastEventType: 'error',
      lastEventAt: ts,
    },
  }
  resetLiveToolState(session.live)

  sessions[sessionId] = mergeSessionRecords(existing, session)
  syncBlockedSessionToWorkspaceOverview(queryClient, sessions[sessionId])
  return { sessions }
}

function applyRunStreamResumeFailure(state: DesktopStoreState, sessionId: string, errorMessage: string, ts: number): Partial<DesktopStoreState> {
  const existing = state.sessions[sessionId]
  if (!existing) {
    return {}
  }

  const sessions = { ...state.sessions }
  const activeRunId = resolveRunStreamId(existing)
  const nextStatus: DesktopSessionRecord['live']['status'] = activeRunId ? existing.live.status : 'error'
  const session = {
    ...existing,
    live: {
      ...existing.live,
      awaitingAck: false,
      error: errorMessage,
      summary: activeRunId ? 'Stream resume failed' : null,
      lastEventType: 'resume.error',
      lastEventAt: ts,
      status: nextStatus,
    },
  }

  if (!activeRunId) {
    session.live.startedAt = null
    resetLiveToolState(session.live)
    completeLiveReasoningState(session.live, ts, session.live.seq, 'error')
      completeLiveReasoningHistory(session.live, ts, session.live.seq, 'error')
  }

  sessions[sessionId] = mergeSessionRecords(existing, session)
  syncBlockedSessionToWorkspaceOverview(queryClient, sessions[sessionId])
  return { sessions }
}

export function applyEnvelope(state: DesktopStoreState, envelope: EventEnvelope): Partial<DesktopStoreState> {
  const eventType = typeof envelope.event_type === 'string' ? envelope.event_type : ''
  const ts = typeof envelope.ts_unix_ms === 'number' ? envelope.ts_unix_ms : Date.now()
  const envelopeSeq = typeof envelope.source_seq === 'number' && envelope.source_seq > 0
    ? Math.max(0, envelope.source_seq)
    : typeof envelope.global_seq === 'number'
      ? Math.max(0, envelope.global_seq)
      : 0
  const payload = envelope.payload && typeof envelope.payload === 'object' ? envelope.payload : {}
  let payloadRecord = payload as Record<string, unknown>
  if (eventType.startsWith('workspace.todo.')) {
    const workspacePath = typeof payloadRecord.workspace_path === 'string' ? payloadRecord.workspace_path.trim() : ''
    const summaryRecord = payloadRecord.summary && typeof payloadRecord.summary === 'object' ? payloadRecord.summary as Record<string, unknown> : null
    if (workspacePath && summaryRecord) {
      const emptySummary = createEmptyWorkspaceTodoSummary()
      const userSummary = summaryRecord.user && typeof summaryRecord.user === 'object'
        ? {
            taskCount: typeof (summaryRecord.user as Record<string, unknown>).task_count === 'number' ? (summaryRecord.user as Record<string, unknown>).task_count as number : 0,
            openCount: typeof (summaryRecord.user as Record<string, unknown>).open_count === 'number' ? (summaryRecord.user as Record<string, unknown>).open_count as number : 0,
            inProgressCount: typeof (summaryRecord.user as Record<string, unknown>).in_progress_count === 'number' ? (summaryRecord.user as Record<string, unknown>).in_progress_count as number : 0,
          }
        : emptySummary.user
      const agentSummary = summaryRecord.agent && typeof summaryRecord.agent === 'object'
        ? {
            taskCount: typeof (summaryRecord.agent as Record<string, unknown>).task_count === 'number' ? (summaryRecord.agent as Record<string, unknown>).task_count as number : 0,
            openCount: typeof (summaryRecord.agent as Record<string, unknown>).open_count === 'number' ? (summaryRecord.agent as Record<string, unknown>).open_count as number : 0,
            inProgressCount: typeof (summaryRecord.agent as Record<string, unknown>).in_progress_count === 'number' ? (summaryRecord.agent as Record<string, unknown>).in_progress_count as number : 0,
          }
        : emptySummary.agent
      patchWorkspaceTodoSummary(workspacePath, {
        ...emptySummary,
        taskCount: userSummary.taskCount,
        openCount: userSummary.openCount,
        inProgressCount: userSummary.inProgressCount,
        user: userSummary,
        agent: agentSummary,
      })
      deferDesktopCacheMutation('workspace todo invalidate', () => {
        void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
      })
    }
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  if (eventType === 'workspace.git.status.updated') {
    const workspacePath = typeof payloadRecord.workspace_path === 'string'
      ? payloadRecord.workspace_path.trim()
      : typeof envelope.entity_id === 'string'
        ? envelope.entity_id.trim()
        : ''
    const status = payloadRecord.status && typeof payloadRecord.status === 'object' ? payloadRecord.status : null
    if (workspacePath && status) {
      queryClient.setQueryData(gitStatusQueryKey(workspacePath), { ok: true, status })
      deferDesktopCacheMutation('workspace git status invalidate', () => {
        void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
      })
    }
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  if (eventType === 'worktrees.config.updated') {
    const workspacePath = typeof payloadRecord.workspace_path === 'string'
      ? payloadRecord.workspace_path.trim()
      : typeof envelope.entity_id === 'string'
        ? envelope.entity_id.trim()
        : ''
    if (workspacePath !== '' && typeof payloadRecord.enabled === 'boolean') {
      const enabled = payloadRecord.enabled
      deferDesktopCacheMutation('workspace overview worktree sync', () => {
        syncWorkspaceOverviewWorktreeState(queryClient, workspacePath, enabled)
        void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
      })
    }
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  if (eventType.startsWith('agent.')) {
    deferDesktopCacheMutation('agent state invalidate', () => {
      void queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey })
    })
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  if (eventType === 'ui.settings.updated') {
    const nextSettings = payload as UISettingsWire
    const previousSettings = queryClient.getQueryData<UISettingsWire>(uiSettingsQueryKey())
    queryClient.setQueryData(uiSettingsQueryKey(), nextSettings)
    queryClient.setQueryData(['ui-settings', 'swarm'], normalizeSwarmSettings(nextSettings))
    if (themeCustomOptionsChanged(previousSettings, nextSettings)) {
      setWorkspaceThemeCustomOptions(nextSettings.theme?.custom_themes ?? [])
    }
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  if (eventType === 'workspace.theme.updated') {
    const workspacePath = typeof payloadRecord.workspace_path === 'string'
      ? payloadRecord.workspace_path.trim()
      : typeof envelope.entity_id === 'string'
        ? envelope.entity_id.trim()
        : ''
    const themeId = typeof payloadRecord.theme_id === 'string' ? payloadRecord.theme_id.trim().toLowerCase() : ''
    if (workspacePath) {
      syncWorkspaceOverviewThemeState(queryClient, workspacePath, themeId)
    }
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  payloadRecord = normalizeGlobalV3SessionPayload(eventType, payloadRecord)
  const sessionId = typeof payloadRecord.session_id === 'string'
    ? payloadRecord.session_id
    : typeof payloadRecord.id === 'string' && eventType.startsWith('session.')
      ? payloadRecord.id
      : typeof envelope.entity_id === 'string'
        ? envelope.entity_id
        : ''
  if (eventType === 'notification.created' || eventType === 'notification.updated') {
    const record = notificationRecordFromRealtimePayload(payloadRecord)
    if (record) {
      return {
        notificationCenter: mergeNotificationCenterRecord(
          state.notificationCenter,
          record,
          notificationSummaryFromRealtimePayload(payloadRecord),
        ),
        lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0),
      }
    }
    deferDesktopCacheMutation('notification refresh', () => {
      void useDesktopUiStore.getState().refreshNotifications()
    })
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  if (eventType === 'notification.cleared') {
    const summary = notificationSummaryFromRealtimePayload(payloadRecord) ?? {
      swarmID: typeof payloadRecord.swarm_id === 'string' ? payloadRecord.swarm_id : state.notificationCenter.summary.swarmID,
      totalCount: 0,
      unreadCount: 0,
      activeCount: 0,
      updatedAt: Date.now(),
    }
    return {
      notificationCenter: clearNotificationCenter(state.notificationCenter, summary),
      lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0),
    }
  }
  if (eventType.startsWith('swarm.')) {
    if (eventType === 'swarm.mirror.updated' && typeof window !== 'undefined') {
      window.dispatchEvent(new CustomEvent('swarm:mirror-updated', { detail: payloadRecord }))
    }
    const notifications = [...state.notifications]
    const enrollmentId = typeof payloadRecord.id === 'string'
      ? payloadRecord.id
      : typeof payloadRecord.enrollment_id === 'string'
        ? payloadRecord.enrollment_id
        : typeof envelope.entity_id === 'string'
          ? envelope.entity_id
          : ''
    const childName = typeof payloadRecord.child_name === 'string' ? payloadRecord.child_name.trim() : ''
    if (eventType === 'swarm.enrollment.pending') {
      notifications.unshift(makeSwarmNotification({
        eventType,
        title: 'Child wants to join',
        detail: childName || 'A child device requested pairing',
        severity: 'info',
        createdAt: ts,
        enrollmentId,
        childName,
      }))
    } else if (eventType === 'swarm.enrollment.approved') {
      notifications.unshift(makeSwarmNotification({
        eventType,
        title: 'Child approved',
        detail: childName || 'A child enrollment was approved',
        severity: 'info',
        createdAt: ts,
        enrollmentId,
        childName,
      }))
    } else if (eventType === 'swarm.enrollment.rejected') {
      notifications.unshift(makeSwarmNotification({
        eventType,
        title: 'Child rejected',
        detail: childName || 'A child enrollment was rejected',
        severity: 'warning',
        createdAt: ts,
        enrollmentId,
        childName,
      }))
    } else if (eventType === 'swarm.ceremony.requested') {
      const primaryName = typeof payloadRecord.primary_name === 'string' ? payloadRecord.primary_name.trim() : ''
      const authCode = typeof payloadRecord.auth_code === 'string' ? payloadRecord.auth_code.trim() : ''
      const detailParts = [
        primaryName ? `${primaryName} requested pairing` : 'A primary swarm requested pairing',
        authCode ? `Auth code ${authCode}` : '',
      ].filter((part) => part !== '')
      notifications.unshift(makeSwarmNotification({
        eventType,
        title: 'Pairing ceremony ready',
        detail: detailParts.join(' · '),
        severity: 'info',
        createdAt: ts,
        enrollmentId,
        childName,
      }))
    }
    return {
      notifications: notifications.slice(0, MAX_NOTIFICATIONS),
      lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0),
    }
  }
  if (!sessionId) {
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }

  const hasCanonicalV3Snapshot = Boolean(getV3RuntimeDesktopSnapshot().sessionsById[sessionId])
  if (eventType.startsWith('session.') && hasCanonicalV3Snapshot) {
    invalidateAuthoritativeSessionSnapshot(sessionId)
  }

  const sessions = { ...state.sessions }
  const ensured = ensureSession(state, sessionId)
  const session = { ...ensured, live: { ...ensured.live }, pendingPermissions: [...ensured.pendingPermissions] }
  session.permissionsHydrated = ensured.permissionsHydrated
  const notifications = [...state.notifications]

  const durableUpdatedAt = typeof payloadRecord.updated_at === 'number' && payloadRecord.updated_at > 0
    ? payloadRecord.updated_at
    : 0
  if (durableUpdatedAt > 0) {
    session.updatedAt = Math.max(session.updatedAt, durableUpdatedAt)
  }
  const usageSummary = eventType === 'run.usage.updated'
    ? mapDesktopSessionUsageSummary(payloadRecord.usage_summary)
    : null
  if (usageSummary) {
    session.usage = usageSummary
    session.updatedAt = Math.max(session.updatedAt, usageSummary.updatedAt)
  }

  switch (eventType) {
    case 'session.created':
    case 'session.updated': {
      const metadataRecord = payloadRecord.metadata && typeof payloadRecord.metadata === 'object'
        ? payloadRecord.metadata as Record<string, unknown>
        : null
      const workspaceFacts = sessionWorkspaceFactsFromMetadata(metadataRecord)
      session.title = typeof payloadRecord.title === 'string' ? payloadRecord.title : session.title
      session.metadata = metadataRecord ?? session.metadata
      const rawWorkspacePath = typeof payloadRecord.workspace_path === 'string'
        ? payloadRecord.workspace_path.trim()
        : session.workspacePath
      const worktreeRootPath = typeof payloadRecord.worktree_root_path === 'string'
        ? payloadRecord.worktree_root_path.trim()
        : workspaceFacts.worktreeRootPath || session.worktreeRootPath
      const worktreeEnabled = typeof payloadRecord.worktree_enabled === 'boolean'
        ? payloadRecord.worktree_enabled
        : workspaceFacts.worktreeEnabled ?? session.worktreeEnabled
      const nextWorkspacePath = canonicalSessionWorkspacePath({
        workspacePath: rawWorkspacePath,
        sourceWorkspacePath: workspaceFacts.sourceWorkspacePath,
        runtimeWorkspacePath: workspaceFacts.runtimeWorkspacePath,
        worktreeEnabled,
        worktreeRootPath,
      })
      session.workspacePath = nextWorkspacePath || session.workspacePath
      const nextRuntimeWorkspacePath = workspaceFacts.runtimeWorkspacePath || (
        typeof payloadRecord.workspace_path === 'string'
          ? payloadRecord.workspace_path.trim()
          : session.runtimeWorkspacePath || session.workspacePath
      )
      session.runtimeWorkspacePath = nextRuntimeWorkspacePath || session.runtimeWorkspacePath
      session.worktreeEnabled = worktreeEnabled
      session.worktreeRootPath = worktreeRootPath
      session.worktreeBaseBranch = typeof payloadRecord.worktree_base_branch === 'string'
        ? payloadRecord.worktree_base_branch.trim()
        : session.worktreeBaseBranch
      session.worktreeBranch = typeof payloadRecord.worktree_branch === 'string'
        ? payloadRecord.worktree_branch.trim()
        : session.worktreeBranch
      session.gitBranch = typeof payloadRecord.git_branch === 'string'
        ? payloadRecord.git_branch.trim()
        : session.gitBranch
      session.gitHasGit = typeof payloadRecord.git_has_git === 'boolean'
        ? payloadRecord.git_has_git
        : session.gitHasGit
      session.gitClean = typeof payloadRecord.git_clean === 'boolean'
        ? payloadRecord.git_clean
        : session.gitClean
      session.gitDirtyCount = typeof payloadRecord.git_dirty_count === 'number'
        ? payloadRecord.git_dirty_count
        : session.gitDirtyCount
      session.gitStagedCount = typeof payloadRecord.git_staged_count === 'number'
        ? payloadRecord.git_staged_count
        : session.gitStagedCount
      session.gitModifiedCount = typeof payloadRecord.git_modified_count === 'number'
        ? payloadRecord.git_modified_count
        : session.gitModifiedCount
      session.gitUntrackedCount = typeof payloadRecord.git_untracked_count === 'number'
        ? payloadRecord.git_untracked_count
        : session.gitUntrackedCount
      session.gitConflictCount = typeof payloadRecord.git_conflict_count === 'number'
        ? payloadRecord.git_conflict_count
        : session.gitConflictCount
      session.gitAheadCount = typeof payloadRecord.git_ahead_count === 'number'
        ? payloadRecord.git_ahead_count
        : session.gitAheadCount
      session.gitBehindCount = typeof payloadRecord.git_behind_count === 'number'
        ? payloadRecord.git_behind_count
        : session.gitBehindCount
      session.gitCommitDetected = typeof payloadRecord.git_commit_detected === 'boolean'
        ? payloadRecord.git_commit_detected
        : session.gitCommitDetected
      session.gitCommitCount = typeof payloadRecord.git_commit_count === 'number'
        ? payloadRecord.git_commit_count
        : session.gitCommitCount
      session.gitCommittedFileCount = typeof payloadRecord.git_committed_file_count === 'number'
        ? payloadRecord.git_committed_file_count
        : session.gitCommittedFileCount
      session.gitCommittedAdditions = typeof payloadRecord.git_committed_additions === 'number'
        ? payloadRecord.git_committed_additions
        : session.gitCommittedAdditions
      session.gitCommittedDeletions = typeof payloadRecord.git_committed_deletions === 'number'
        ? payloadRecord.git_committed_deletions
        : session.gitCommittedDeletions
      const requestedWorkspaceName = typeof payloadRecord.workspace_name === 'string' ? payloadRecord.workspace_name.trim() : session.workspaceName
      session.workspaceName = canonicalSessionWorkspaceName(requestedWorkspaceName, rawWorkspacePath, nextWorkspacePath) || session.workspaceName
      session.mode = typeof payloadRecord.mode === 'string' ? payloadRecord.mode : session.mode
      session.sessionApi = typeof payloadRecord.session_api === 'string' && payloadRecord.session_api.trim() !== ''
        ? payloadRecord.session_api.trim()
        : session.sessionApi
      session.createdAt = typeof payloadRecord.created_at === 'number' ? payloadRecord.created_at : session.createdAt || ts
      break
    }
    case 'session.mode.updated':
      session.mode = typeof payloadRecord.mode === 'string' ? payloadRecord.mode : session.mode
      requestScopedSessionWorkset(sessionId, { force: true })
      notifications.unshift(makeNotification(sessionId, session.live.runId, eventType, 'Mode updated', session.mode, 'info', ts))
      break
    case 'session.preference.updated':
      break
    case 'session.agent.updated':
    case 'session.metadata.updated': {
      const metadata = payloadRecord.metadata && typeof payloadRecord.metadata === 'object'
        ? payloadRecord.metadata as Record<string, unknown>
        : null
      if (metadata) {
        session.metadata = metadata
      }
      const gitMeta = metadata?.git && typeof metadata.git === 'object'
        ? metadata.git as Record<string, unknown>
        : null
      const gitStatus = gitMeta?.status && typeof gitMeta.status === 'object'
        ? gitMeta.status as Record<string, unknown>
        : null
      if (gitMeta) {
        session.gitCommitDetected = typeof gitMeta.commit_detected === 'boolean'
          ? gitMeta.commit_detected
          : session.gitCommitDetected
        session.gitCommitCount = typeof gitMeta.commit_count === 'number'
          ? gitMeta.commit_count
          : session.gitCommitCount
      }
      if (session.worktreeEnabled && gitStatus) {
        session.gitBranch = typeof gitStatus.branch === 'string' ? gitStatus.branch : session.gitBranch
        session.gitHasGit = typeof gitStatus.has_git === 'boolean' ? gitStatus.has_git : session.gitHasGit
        session.gitClean = typeof gitStatus.clean === 'boolean' ? gitStatus.clean : session.gitClean
        session.gitDirtyCount = typeof gitStatus.dirty_count === 'number' ? gitStatus.dirty_count : session.gitDirtyCount
        session.gitStagedCount = typeof gitStatus.staged_count === 'number' ? gitStatus.staged_count : session.gitStagedCount
        session.gitModifiedCount = typeof gitStatus.modified_count === 'number' ? gitStatus.modified_count : session.gitModifiedCount
        session.gitUntrackedCount = typeof gitStatus.untracked_count === 'number' ? gitStatus.untracked_count : session.gitUntrackedCount
        session.gitConflictCount = typeof gitStatus.conflict_count === 'number' ? gitStatus.conflict_count : session.gitConflictCount
        session.gitAheadCount = typeof gitStatus.ahead_count === 'number' ? gitStatus.ahead_count : session.gitAheadCount
        session.gitBehindCount = typeof gitStatus.behind_count === 'number' ? gitStatus.behind_count : session.gitBehindCount
        session.gitCommittedFileCount = typeof gitStatus.committed_file_count === 'number'
          ? gitStatus.committed_file_count
          : session.gitCommittedFileCount
        session.gitCommittedAdditions = typeof gitStatus.committed_additions === 'number'
          ? gitStatus.committed_additions
          : session.gitCommittedAdditions
        session.gitCommittedDeletions = typeof gitStatus.committed_deletions === 'number'
          ? gitStatus.committed_deletions
          : session.gitCommittedDeletions
      }
      break
    }
    case 'session.title.updated':
      session.title = typeof payloadRecord.title === 'string' ? payloadRecord.title : session.title
      break
    case 'session.message.appended': {
      const normalized = normalizeMessage(payloadRecord.message as RunStreamEventMessage['message'], sessionId)
      if (normalized) {
        updateMessagesCache(normalized.sessionId, normalized)
      }
      session.messageCount += 1
      break
    }
    case 'session.assistant.started': {
      const runIntent = payloadRecord.run_intent && typeof payloadRecord.run_intent === 'object'
        ? payloadRecord.run_intent as Record<string, unknown>
        : null
      const runId = typeof payloadRecord.run_id === 'string'
        ? payloadRecord.run_id.trim()
        : typeof runIntent?.run_id === 'string'
          ? runIntent.run_id.trim()
          : ''
      if (eventRevivesTerminalRun(session, payloadRecord)) {
        break
      }
      if (runId) {
        session.live.runId = runId
        clearTerminalRunMarker(session.live, runId)
      }
      session.live.status = 'running'
      session.live.awaitingAck = false
      session.live.startedAt = session.live.startedAt ?? ts
      session.live.summary = 'Assistant responding…'
      session.live.error = null
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      resetLiveToolState(session.live)
      resetLiveReasoningState(session.live)
      break
    }
    case 'session.assistant.delta': {
      const runId = typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id.trim() : ''
      if (eventRevivesTerminalRun(session, payloadRecord)) {
        break
      }
      if (runId) {
        session.live.runId = runId
        clearTerminalRunMarker(session.live, runId)
      }
      const delta = typeof payloadRecord.delta === 'string' ? payloadRecord.delta : ''
      if (delta) {
        const nextDraft = session.live.assistantDraft + delta
        session.live.assistantDraft = nextDraft
        scheduleDraftFlush(sessionId, {
          assistantDraft: nextDraft,
          reasoningSummary: session.live.reasoningSummary,
          reasoningText: session.live.reasoningText,
          reasoningState: session.live.reasoningState,
          reasoningSegment: session.live.reasoningSegment,
          toolOutput: session.live.toolOutput,
        })
      }
      session.live.status = 'running'
      session.live.awaitingAck = false
      session.live.startedAt = session.live.startedAt ?? ts
      session.live.summary = 'Streaming response…'
      session.live.error = null
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    }
    case 'session.reasoning.started':
    case 'session.reasoning.delta':
    case 'session.reasoning.completed': {
      applyLiveReasoningSnapshot(session, payloadRecord, eventType, ts, envelopeSeq)
      scheduleDraftFlush(sessionId, {
        assistantDraft: session.live.assistantDraft,
        reasoningSummary: session.live.reasoningSummary,
        reasoningText: session.live.reasoningText,
        reasoningState: session.live.reasoningState,
        reasoningSegment: session.live.reasoningSegment,
        toolOutput: session.live.toolOutput,
      })
      break
    }
    case 'session.tool.started':
    case 'session.tool.delta':
    case 'session.tool.completed':
    case 'session.tool.failed':
    case 'session.tool.cancelled':
    case 'session.tool.canceled': {
      const runId = typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id.trim() : ''
      const toolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name.trim() : ''
      const callId = typeof payloadRecord.call_id === 'string' ? payloadRecord.call_id.trim() : ''
      const stepId = typeof payloadRecord.step_id === 'string' ? payloadRecord.step_id.trim() : ''
      const toolInstanceId = typeof payloadRecord.tool_instance_id === 'string' ? payloadRecord.tool_instance_id.trim() : ''
      const isToolStarted = eventType === 'session.tool.started'
      const isToolDelta = eventType === 'session.tool.delta'
      const isToolCompleted = eventType === 'session.tool.completed'
      const isToolFailed = eventType === 'session.tool.failed'
      const isToolCancelled = eventType === 'session.tool.cancelled' || eventType === 'session.tool.canceled'
      const isToolTerminal = isToolCompleted || isToolFailed || isToolCancelled
      const afterTerminalRun = eventRevivesTerminalRun(session, payloadRecord)
      if (runId && !afterTerminalRun) {
        session.live.runId = runId
        clearTerminalRunMarker(session.live, runId)
      }
      if (!afterTerminalRun) {
        session.live.status = 'running'
        session.live.awaitingAck = false
        session.live.startedAt = session.live.startedAt ?? ts
        session.live.error = null
      }
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      if (typeof payloadRecord.step === 'number') {
        session.live.step = payloadRecord.step
      }
      if (isToolStarted && !afterTerminalRun) {
        flushLiveAssistantDraftToSegment(session.live, ts)
        cancelDraftFlush(sessionId)
        resetRetainedLiveToolState(session.live)
        session.live.toolOutput = ''
      }
      if (!afterTerminalRun) {
        session.live.sidebarToolName = toolName || null
        session.live.toolName = toolName || session.live.toolName
        session.live.toolCallId = callId || session.live.toolCallId
        if (typeof payloadRecord.arguments === 'string') {
          session.live.toolArguments = payloadRecord.arguments.trim() || null
        }
        if (typeof payloadRecord.summary === 'string' && payloadRecord.summary.trim() !== '') {
          session.live.summary = payloadRecord.summary.trim()
        } else if (session.live.toolName?.trim()) {
          session.live.summary = session.live.toolName.trim()
        }
      }
      upsertLiveToolHistory(session.live, {
        sessionId,
        runId,
        stepId,
        callId,
        toolInstanceId,
        toolName,
        toolArguments: typeof payloadRecord.arguments === 'string' ? payloadRecord.arguments.trim() || null : null,
        output: typeof payloadRecord.output === 'string' ? payloadRecord.output : null,
        rawOutput: typeof payloadRecord.raw_output === 'string' ? payloadRecord.raw_output : null,
        completedOutput: typeof payloadRecord.completed_output === 'string' ? payloadRecord.completed_output : null,
        state: isToolTerminal ? (isToolFailed ? 'error' : 'done') : 'running',
        step: typeof payloadRecord.step === 'number' ? payloadRecord.step : null,
        seq: envelopeSeq,
        ts,
      })
      if (afterTerminalRun) {
        break
      }
      if (isToolDelta && typeof payloadRecord.output === 'string') {
        session.live.toolOutput = session.live.toolName === 'task'
          ? mergedTaskToolDelta(session.live.toolOutput, payloadRecord.output)
          : appendLiveToolOutput(session.live.toolOutput, payloadRecord.output)
        scheduleDraftFlush(sessionId, {
          assistantDraft: session.live.assistantDraft,
          reasoningSummary: session.live.reasoningSummary,
          reasoningText: session.live.reasoningText,
          reasoningState: session.live.reasoningState,
          reasoningSegment: session.live.reasoningSegment,
          toolOutput: session.live.toolOutput,
        })
      } else if (isToolTerminal) {
        session.live.toolOutput = typeof payloadRecord.raw_output === 'string'
          ? replaceLiveToolOutput(payloadRecord.raw_output)
          : typeof payloadRecord.completed_output === 'string'
            ? replaceLiveToolOutput(payloadRecord.completed_output)
            : typeof payloadRecord.output === 'string'
              ? replaceLiveToolOutput(payloadRecord.output)
              : session.live.toolOutput
        retainLiveToolState(session.live, isToolFailed ? 'error' : 'done')
        resetLiveToolState(session.live)
      }
      break
    }
    case 'session.run.started':
    case 'session.run.running': {
      const runId = typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id.trim() : ''
      if (eventRevivesTerminalRun(session, payloadRecord)) {
        break
      }
      if (runId) {
        session.live.runId = runId
        clearTerminalRunMarker(session.live, runId)
      }
      session.live.status = 'running'
      session.live.awaitingAck = false
      session.live.startedAt = session.live.startedAt ?? ts
      session.live.summary = 'Assistant responding…'
      session.live.error = null
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    }
    case 'session.run.completed': {
      const terminalRunIntent = v3TerminalRunIntent(payloadRecord, eventType)
      if (!sessionUsesV3Api(session) || terminalRunIntent?.status === 'completed') {
        if (terminalRunIntent) {
          session.runIntent = null
        }
        markTerminalRun(session.live, terminalRunIntent?.runId || session.live.runId, envelopeSeq)
        cancelDraftFlush(sessionId)
        session.live.status = 'idle'
        session.live.runId = null
        session.live.startedAt = null
        session.live.awaitingAck = false
        resetSidebarLiveToolName(session.live)
        session.live.summary = null
        session.live.error = null
        retainLiveToolState(session.live, 'done')
        resetLiveToolState(session.live)
        completeLiveReasoningState(session.live, ts, envelopeSeq)
        completeLiveReasoningHistory(session.live, ts, envelopeSeq)
      }
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    }
    case 'session.assistant.completed': {
      const terminalRunIntent = v3TerminalRunIntent(payloadRecord, eventType)
      const normalized = normalizeMessage(payloadRecord.message as RunStreamEventMessage['message'], sessionId)
      if (normalized) {
        const finalizedAssistantDraft = session.live.assistantDraft
        deferAssistantFinalization(sessionId, normalized, finalizedAssistantDraft)
        cancelDraftFlush(sessionId)
        resetLiveAssistantState(session.live)
        session.messageCount += 1
      }
      if (!sessionUsesV3Api(session) || terminalRunIntent?.status === 'completed') {
        if (terminalRunIntent) {
          session.runIntent = null
        }
        markTerminalRun(session.live, terminalRunIntent?.runId || session.live.runId, envelopeSeq)
        session.live.status = 'idle'
        session.live.runId = null
        session.live.startedAt = null
        session.live.awaitingAck = false
        resetSidebarLiveToolName(session.live)
        session.live.summary = null
        session.live.error = null
        resetLiveToolState(session.live)
        completeLiveReasoningState(session.live, ts, envelopeSeq)
        completeLiveReasoningHistory(session.live, ts, envelopeSeq)
      }
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    }
    case 'session.run.failed':
    case 'session.run.cancelled':
    case 'session.run.expired':
    case 'session.run.interrupted':
    case 'session.assistant.failed': {
      const terminalRunIntent = v3TerminalRunIntent(payloadRecord, eventType)
      const runId = terminalRunIntent?.runId
        || (typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id.trim() : '')
      const rawError = terminalRunIntent?.error
        || (typeof payloadRecord.error === 'string' && payloadRecord.error.trim() !== '' ? payloadRecord.error.trim() : '')
      const isUserCancellation = eventType === 'session.run.cancelled' || terminalRunIntent?.status === 'cancelled'
      const error = isUserCancellation ? userFacingRunStopReason(rawError) : rawError || 'Run failed'
      const isTerminalError = terminalRunIntent !== null && terminalRunIntent.status !== 'completed'
      if (!sessionUsesV3Api(session) || isTerminalError || eventType === 'session.run.failed' || eventType === 'session.run.cancelled' || eventType === 'session.run.expired' || eventType === 'session.run.interrupted' || eventType === 'session.assistant.failed') {
        if (terminalRunIntent) {
          session.runIntent = null
        }
        markTerminalRun(session.live, runId || session.live.runId, envelopeSeq)
        cancelDraftFlush(sessionId)
        session.live.status = isUserCancellation ? 'idle' : 'error'
        session.live.runId = null
        session.live.startedAt = null
        session.live.awaitingAck = false
        resetSidebarLiveToolName(session.live)
        session.live.summary = error
        session.live.error = isUserCancellation ? null : error
        retainLiveToolState(session.live, isUserCancellation ? 'done' : 'error')
        resetLiveToolState(session.live)
        completeLiveReasoningState(session.live, ts, envelopeSeq, isUserCancellation ? 'done' : 'error')
        completeLiveReasoningHistory(session.live, ts, envelopeSeq, isUserCancellation ? 'done' : 'error')
        notifications.unshift(makeNotification(sessionId, runId || session.live.runId, eventType, isUserCancellation ? 'Run paused' : 'Run failed', error, isUserCancellation ? 'warning' : 'error', ts))
      }
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    }
    case 'permission.requested':
    case 'permission.updated': {
      const permissionSource = (payloadRecord.permission && typeof payloadRecord.permission === 'object' ? payloadRecord.permission : payloadRecord) as Record<string, unknown>
      const permission = normalizePermission(permissionSource)
      if (permission) {
        session.permissionsHydrated = true
        session.pendingPermissions = session.pendingPermissions.filter((item) => item.id !== permission.id)
        if (permission.status === 'pending') {
          session.pendingPermissions.unshift(permission)
        }
        const pendingPermissionCount = countApprovalRequiredPermissions(session.pendingPermissions, session.mode)
        if (!session.lifecycle && pendingPermissionCount > 0) {
          session.live.status = 'blocked'
        } else if (!session.lifecycle && session.live.status === 'blocked') {
          session.live.status = nextLiveStatusAfterPermissionSync(session)
        }
        session.pendingPermissionCount = pendingPermissionCount
        session.live.lastEventType = eventType
        session.live.lastEventAt = ts
        notifications.unshift(
          makeNotification(
            sessionId,
            permission.runId || session.live.runId,
            eventType,
            permission.status === 'pending' ? 'Permission blocked' : 'Permission updated',
            summarizePermission(permission),
            permission.status === 'denied' || permission.status === 'cancelled' ? 'warning' : 'info',
            ts,
          ),
        )
      }
      break
    }
    case 'permission.summary.updated': {
      const pendingCount = typeof payloadRecord.pending_count === 'number' ? Math.max(0, payloadRecord.pending_count) : session.pendingPermissionCount
      session.pendingPermissionCount = pendingCount === 0 ? 0 : session.pendingPermissionCount
      session.permissionsHydrated = pendingCount === 0
      if (pendingCount === 0) {
        session.pendingPermissions = []
        if (!session.lifecycle && session.live.status === 'blocked') {
          session.live.status = nextLiveStatusAfterPermissionSync(session)
        }
      }
      break
    }
    case 'session.lifecycle.updated': {
      const lifecycleSource =
        payloadRecord.lifecycle && typeof payloadRecord.lifecycle === 'object'
          ? payloadRecord.lifecycle as Record<string, unknown>
          : payloadRecord
      const lifecycle = normalizeLifecycle(lifecycleSource, sessionId)
      if (lifecycle) {
        applyLifecycleSnapshot(sessionId, session, lifecycle, ts, eventType)
      }
      break
    }
    case 'session.status':
    case 'session.run_intent.recorded': {
      const durableRunIntent = eventType === 'session.run_intent.recorded'
        ? v3DurableRunIntent(payloadRecord, eventType)
        : null
      if (durableRunIntent) {
        if (v3RunIntentStatusActive(durableRunIntent.status) && !eventRevivesTerminalRun(session, payloadRecord)) {
          session.runIntent = durableRunIntent
          session.live.runId = durableRunIntent.runId
          clearTerminalRunMarker(session.live, durableRunIntent.runId)
          session.live.startedAt = durableRunIntent.createdAt > 0 ? durableRunIntent.createdAt : session.live.startedAt
        } else if (v3RunIntentStatusTerminal(durableRunIntent.status)) {
          session.runIntent = null
          markTerminalRun(session.live, durableRunIntent.runId, envelopeSeq)
        }
      }
      applyAuthoritativeSessionStatus(sessionId, session, typeof payloadRecord.status === 'string' ? payloadRecord.status : '', ts, eventType, {
        runId: durableRunIntent?.runId || (typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id : session.live.runId),
        summary: typeof payloadRecord.summary === 'string' ? payloadRecord.summary : session.live.summary,
        error: durableRunIntent?.error || (typeof payloadRecord.error === 'string' ? payloadRecord.error : null),
      })
      if (durableRunIntent && v3RunIntentStatusActive(durableRunIntent.status) && session.live.startedAt === null) {
        session.live.startedAt = durableRunIntent.createdAt > 0 ? durableRunIntent.createdAt : ts
      }
      break
    }
    case 'run.turn.started':
      session.live.runId = typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id : session.live.runId
      if (isDisplayableAgentLabel(payloadRecord.agent)) {
        session.live.agentName = payloadRecord.agent.trim()
      }
      if (session.live.startedAt === null) {
        session.live.startedAt = ts
      }
      session.live.error = null
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      resetLiveToolState(session.live)
      resetLiveReasoningState(session.live)
      session.live.summary = typeof payloadRecord.agent === 'string' ? payloadRecord.agent : 'Running'
      break
    case 'run.step.started':
      session.live.step = typeof payloadRecord.step === 'number' ? payloadRecord.step : session.live.step
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    case 'run.tool.started':
      flushLiveAssistantDraftToSegment(session.live, ts)
      cancelDraftFlush(sessionId)
      session.live.runId = typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id : session.live.runId
      session.live.toolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name : session.live.toolName
      session.live.sidebarToolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name.trim() || null : null
      session.live.step = typeof payloadRecord.step === 'number' ? payloadRecord.step : session.live.step
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      if (typeof payloadRecord.arguments === 'string') {
        session.live.toolArguments = payloadRecord.arguments.trim() || null
      }
      if (typeof payloadRecord.call_id === 'string' && payloadRecord.call_id.trim() !== '') {
        session.live.toolCallId = payloadRecord.call_id.trim()
      }
      resetRetainedLiveToolState(session.live)
      session.live.toolOutput = ''
      session.live.summary = session.live.toolName?.trim() || 'Tool started'
      break
    case 'run.tool.delta':
      session.live.toolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name : session.live.toolName
      session.live.sidebarToolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name.trim() || null : null
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      if (typeof payloadRecord.arguments === 'string') {
        session.live.toolArguments = payloadRecord.arguments.trim() || session.live.toolArguments
      }
      if (typeof payloadRecord.call_id === 'string' && payloadRecord.call_id.trim() !== '') {
        session.live.toolCallId = payloadRecord.call_id.trim()
      }
      if (typeof payloadRecord.output === 'string') {
        session.live.toolOutput = session.live.toolName === 'task'
          ? mergedTaskToolDelta(session.live.toolOutput, payloadRecord.output)
          : appendLiveToolOutput(session.live.toolOutput, payloadRecord.output)
      }
      if (typeof payloadRecord.summary === 'string' && payloadRecord.summary.trim() !== '') {
        session.live.summary = payloadRecord.summary.trim()
      }
      break
    case 'run.tool.completed':
      session.live.toolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name : session.live.toolName
      session.live.sidebarToolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name.trim() || null : null
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      if (typeof payloadRecord.arguments === 'string') {
        session.live.toolArguments = payloadRecord.arguments.trim() || session.live.toolArguments
      }
      if (typeof payloadRecord.call_id === 'string' && payloadRecord.call_id.trim() !== '') {
        session.live.toolCallId = payloadRecord.call_id.trim()
      }
      if (typeof payloadRecord.raw_output === 'string') {
        session.live.toolOutput = replaceLiveToolOutput(payloadRecord.raw_output)
      } else if (typeof payloadRecord.output === 'string') {
        session.live.toolOutput = session.live.toolName === 'task'
          ? mergedTaskToolDelta(session.live.toolOutput, payloadRecord.output)
          : replaceLiveToolOutput(payloadRecord.output)
      }
      retainLiveToolState(session.live, 'done')
      resetLiveToolState(session.live)
      if (typeof payloadRecord.summary === 'string' && payloadRecord.summary.trim() !== '') {
        session.live.summary = payloadRecord.summary.trim()
      }
      if (typeof payloadRecord.error === 'string' && payloadRecord.error.trim() !== '') {
        notifications.unshift(makeNotification(sessionId, session.live.runId, eventType, 'Tool failed', payloadRecord.error, 'error', ts))
      }
      break
    case 'run.usage.updated':
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    case 'run.session.title.updated':
      session.title = typeof payloadRecord.title === 'string' ? payloadRecord.title : session.title
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    case 'run.session.warning':
    case 'session.title.warning': {
      const warning = typeof payloadRecord.warning === 'string' ? payloadRecord.warning : ''
      if (warning) {
        notifications.unshift(makeNotification(sessionId, session.live.runId, eventType, 'Session warning', warning, 'warning', ts))
      }
      break
    }
    case 'run.message.stored':
    case 'run.message.updated': {
      const normalized = normalizeMessage(payloadRecord.message as RunStreamEventMessage['message'], sessionId)
      if (normalized) {
        if (normalized.role === 'assistant') {
          const finalizedAssistantDraft = session.live.assistantDraft
          deferAssistantFinalization(sessionId, normalized, finalizedAssistantDraft)
          cancelDraftFlush(sessionId)
        } else {
          updateMessagesCache(normalized.sessionId, normalized)
        }
      }
      break
    }
    case 'message.stored':
      break
    default:
      break
  }

  if (envelopeSeq > 0) {
    session.live.seq = Math.max(session.live.seq, envelopeSeq)
  }
  let merged = mergeSessionRecords(state.sessions[sessionId] ?? null, session)
  if (eventType === 'session.run_intent.recorded' && v3RunIntentStatusTerminal(v3PayloadString(v3RunIntentPayload(payloadRecord), 'status'))) {
    merged = { ...merged, lifecycle: null }
  }
  sessions[sessionId] = merged
  syncBlockedSessionToWorkspaceOverview(queryClient, merged)
  if (sessionRequiresSnapshotHydration(merged, eventType)) {
    requestScopedSessionWorkset(sessionId)
  }

  const nextActiveWorkspacePath = state.activeSessionId === merged.id
    ? merged.workspacePath || state.activeWorkspacePath
    : state.activeWorkspacePath

  if (state.activeSessionId === merged.id && nextActiveWorkspacePath !== state.activeWorkspacePath) {
    saveDesktopActiveWorkspacePath(nextActiveWorkspacePath)
  }

  return {
    sessions,
    notifications: notifications.slice(0, MAX_NOTIFICATIONS),
    lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0),
    activeWorkspacePath: nextActiveWorkspacePath,
  }
}

function ensureDesktopSessionV3Runtime(): DesktopSessionV3Runtime {
  if (desktopSessionV3Runtime) {
    return desktopSessionV3Runtime
  }
  desktopSessionV3Runtime = new DesktopSessionV3Runtime({
    initialDesktopState: getV3RuntimeDesktopSnapshot(),
  })
  let initialEmission = true
  desktopSessionV3Runtime.subscribe((state, result) => {
    if (initialEmission && result === null) {
      initialEmission = false
      return
    }
    initialEmission = false
    applyDesktopSessionV3RuntimeState(state, result)
  })
  return desktopSessionV3Runtime
}

async function syncV3RuntimeSessions(options: { force?: boolean } = {}): Promise<void> {
  const startedAt = Date.now()
  const canFetch = canFetchRelativeDesktopAPI()
  if (!canFetch) {
    desktopV3ReconnectSyncDiagnostics = {
      status: 'skipped',
      owner: 'desktop-session-v3-runtime',
      reason: 'relative-api-unavailable',
      force: Boolean(options.force),
      startedAt,
      completedAt: Date.now(),
    }
    return
  }
  const runtime = ensureDesktopSessionV3Runtime()
  try {
    const shouldBoot = runtime.getState().status === 'idle' || !runtime.getState().endpointCursor.trim()
    const state = shouldBoot || options.force
      ? await runtime.boot()
      : await runtime.refresh({ reason: 'store sync' })
    desktopV3ReconnectSyncDiagnostics = {
      status: 'applied',
      owner: 'desktop-session-v3-runtime',
      force: Boolean(options.force),
      startedAt,
      completedAt: Date.now(),
      endpointCursorPresent: Boolean(state.endpointCursor.trim()),
      subscriptionCount: Object.keys(state.subscriptionsBySessionId).length,
      worksetCount: Object.keys(state.worksetsById).length,
      discoveredSessionCount: state.discoveredSessionIds.length,
      diagnostics: runtime.diagnostics(),
    }
  } catch (error) {
    desktopV3ReconnectSyncDiagnostics = {
      status: 'failed',
      owner: 'desktop-session-v3-runtime',
      force: Boolean(options.force),
      startedAt,
      completedAt: Date.now(),
      error: error instanceof Error ? error.message : String(error),
    }
    throw error
  }
}

function persistDesktopV3EndpointCursor(payload: RunStreamEventMessage): void {
  const cursor = String((payload as DesktopV3RealtimeFrame).endpoint_cursor ?? '').trim()
  if (cursor) {
    desktopV3RealtimeEndpointCursor = cursor
    requireV3RealtimeController().setEndpointCursor(cursor)
  }
}

function isSessionlessV3RealtimeControl(kind: string): boolean {
  return kind === 'keepalive'
    || kind === 'endpoint.watermark'
    || kind === 'replay.started'
    || kind === 'replay.complete'
    || kind === 'projection.high_watermark'
    || kind === 'slow_consumer.reconnect_required'
}

function applyDesktopV3RealtimeFrame(sessionId: string, payload: DesktopV3RealtimeFrame, ts: number): boolean {
  const normalizedSessionId = sessionId.trim() || String(payload.session_id ?? payload.event?.session_id ?? '').trim()
  const frameSessionId = normalizedSessionId || String(payload.event?.session_id ?? '').trim()
  const frameKind = String(payload.kind ?? payload.type ?? '').trim()
  if (!frameSessionId && !isSessionlessV3RealtimeControl(frameKind)) {
    return false
  }
  const eventType = String(payload.event?.event_type ?? payload.event_type ?? frameKind).trim()
  if (frameSessionId) {
    recordDesktopV3FollowUpAssistantFrame(frameSessionId, payload, ts)
  }
  const normalizedEnvelope = normalizeV3RealtimeFrame(payload, { receivedAt: ts, sessionId: frameSessionId || undefined })
  const runtimeOutcome = applyV3RuntimeEnvelope(normalizedEnvelope)
  if (eventType.startsWith('session.diagnostic.')) {
    if (runtimeOutcome.applied || runtimeOutcome.shouldAdvanceCursor || !runtimeOutcome.rejected) {
      persistDesktopV3EndpointCursor(payload as RunStreamEventMessage)
    }
    return runtimeOutcome.applied || runtimeOutcome.shouldAdvanceCursor || !runtimeOutcome.rejected
  }
  if (runtimeOutcome.applied) {
    useDesktopUiStore.setState((state: DesktopStoreState) => {
      const patch = applyV3SessionStreamFrame(state, frameSessionId, payload as RunStreamEventMessage, ts) ?? {}
      return patch
    })
  }
  if (runtimeOutcome.applied || runtimeOutcome.shouldAdvanceCursor) {
    persistDesktopV3EndpointCursor(payload as RunStreamEventMessage)
  }
  return runtimeOutcome.applied || runtimeOutcome.shouldAdvanceCursor
}

runStreamController = new DesktopRunStreamController({
  getResumeRequest: (sessionId, fallbackRunId) => resolveRunStreamResumeRequest(sessionId, fallbackRunId),
  onFrame: (sessionId, payload, ts) => {
    useDesktopUiStore.setState((state: DesktopStoreState) => applyRunStreamFrame(state, sessionId, payload, ts))
  },
  onReconnectPending: (sessionId, reason, ts) => {
    useDesktopUiStore.setState((state: DesktopStoreState) => applyRunStreamSocketFailure(state, sessionId, reason, ts))
  },
  onResumeFailure: (sessionId, message, ts) => {
    useDesktopUiStore.setState((state: DesktopStoreState) => applyRunStreamResumeFailure(state, sessionId, message, ts))
  },
})

desktopV3RealtimeController = new DesktopV3RealtimeController({
  getEndpointCursor: () => desktopV3RealtimeEndpointCursor,
  onFrame: (sessionId, payload, ts) => applyDesktopV3RealtimeFrame(sessionId, payload, ts),
  onReconnectPending: (_reason, _ts) => undefined,
  onCursorError: (sessionId, payload) => {
    const errorCode = String(payload.error_code ?? '').trim()
    if (sessionId && (errorCode === 'cursor_too_old' || errorCode === 'endpoint_cursor_ahead' || errorCode === 'endpoint_cursor_malformed')) {
      requestScopedSessionWorkset(sessionId, { force: true })
    }
  },
})

export const useDesktopUiStore = createDesktopUiStore<DesktopStoreState>((set, get) => ({
  hydrated: false,
  hydrating: false,
  connectionState: 'idle',
  onboardingFlowRequested: false,
  activeSessionId: null,
  activeWorkspacePath: null,
  sessions: {},
  notifications: [],
  notificationCenter: {
    items: [],
    summary: EMPTY_NOTIFICATION_SUMMARY,
    loading: false,
    hydrated: false,
  },
  reconnectTimer: null,
  heartbeatTimer: null,
  livenessTimer: null,
  reconnectAttempt: 0,
  connectionGeneration: 0,
  realtimeDesired: false,
  lastGlobalSeq: 0,
  vault: emptyVaultState(),
  sessionDrafts: {},
  sessionDraftModes: {},
  setActiveSession: (sessionId) => {
    const normalizedSessionId = sessionId?.trim() ?? ''
    set((state: DesktopStoreState) => {
      const nextActiveSessionId = normalizedSessionId || null
      const nextActiveWorkspacePath = resolveWorkspacePathForActiveSession(state, nextActiveSessionId)
      saveDesktopActiveSessionId(nextActiveSessionId)
      saveDesktopActiveWorkspacePath(nextActiveWorkspacePath)
      return {
        activeSessionId: nextActiveSessionId,
        activeWorkspacePath: nextActiveWorkspacePath,
      }
    })
  },
  setActiveWorkspacePath: (workspacePath) => {
    const normalizedWorkspacePath = workspacePath?.trim() ?? ''
    const nextActiveWorkspacePath = normalizedWorkspacePath || null
    saveDesktopActiveWorkspacePath(nextActiveWorkspacePath)
    set({ activeWorkspacePath: nextActiveWorkspacePath })
  },
  upsertSession: (session) => {
    set((state: DesktopStoreState) => {
      if (!session.id) {
        return state
      }
      const merged = mergeExternalSessionRecord(state.sessions[session.id] ?? null, session)
      syncBlockedSessionToWorkspaceOverview(queryClient, merged)
      if ((merged.sessionApi?.trim().toLowerCase() || 'v3') === 'v3') {
        ensureDesktopSessionV3Runtime().setWantedSessions([merged.id])
        void syncV3RuntimeSessions().catch((error) => {
          console.error('[desktop-store] session v3 runtime sync failed', error)
        })
      }
      const nextActiveWorkspacePath = state.activeSessionId === merged.id
        ? merged.workspacePath || state.activeWorkspacePath
        : state.activeWorkspacePath
      const nextSessionDrafts = { ...state.sessionDrafts }
      const nextSessionDraftModes = { ...state.sessionDraftModes }
      const pendingDraftKey = draftKeyForSession(null, merged.workspacePath)
      if (pendingDraftKey in nextSessionDrafts && !(merged.id in nextSessionDrafts)) {
        nextSessionDrafts[merged.id] = nextSessionDrafts[pendingDraftKey]
        delete nextSessionDrafts[pendingDraftKey]
      }
      if (pendingDraftKey in nextSessionDraftModes && !(merged.id in nextSessionDraftModes)) {
        nextSessionDraftModes[merged.id] = nextSessionDraftModes[pendingDraftKey]
        delete nextSessionDraftModes[pendingDraftKey]
      }
      if (state.activeSessionId === merged.id && nextActiveWorkspacePath !== state.activeWorkspacePath) {
        saveDesktopActiveWorkspacePath(nextActiveWorkspacePath)
      }
      return {
        sessions: {
          ...state.sessions,
          [session.id]: merged,
        },
        activeWorkspacePath: nextActiveWorkspacePath,
        sessionDrafts: nextSessionDrafts,
        sessionDraftModes: nextSessionDraftModes,
      }
    })
  },
  refreshSessionPermissions: async (sessionId) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    await fetchAndApplyDesktopV3SessionPermissions(normalizedSessionId)
  },
  refreshNotifications: async () => {
    set((state: DesktopStoreState) => ({
      notificationCenter: {
        ...state.notificationCenter,
        loading: true,
      },
    }))
    try {
      const [notifications, summary] = await Promise.all([
        fetchNotifications(),
        fetchNotificationSummary(),
      ])
      const mappedNotifications = notifications.map(mapDurableNotification)
      const mappedSummary = mapNotificationSummary(summary)
      updateBrowserNotificationSignals(mappedSummary)
      set((state: DesktopStoreState) => ({
        notificationCenter: {
          ...state.notificationCenter,
          items: mappedNotifications,
          summary: mappedSummary,
          loading: false,
          hydrated: true,
        },
      }))
    } catch (error) {
      console.error('[desktop-store] notification refresh failed', error)
      set((state: DesktopStoreState) => ({
        notificationCenter: {
          ...state.notificationCenter,
          loading: false,
        },
      }))
    }
  },
  clearNotifications: async () => {
    await clearDurableNotifications()
    await get().refreshNotifications()
  },
  updateNotificationRecord: async (id, patch) => {
    const normalizedID = id.trim()
    if (!normalizedID) {
      return
    }
    await updateNotification(normalizedID, patch)
    await get().refreshNotifications()
  },
  setSessionDraft: (sessionId, draft) => {
    const key = sessionId.trim()
    if (!key) {
      return
    }
    set((state: DesktopStoreState) => ({
      sessionDrafts: {
        ...state.sessionDrafts,
        [key]: draft,
      },
    }))
  },
  setSessionDraftMode: (sessionId, mode) => {
    const key = sessionId.trim()
    if (!key) {
      return
    }
    set((state: DesktopStoreState) => ({
      sessionDraftModes: {
        ...state.sessionDraftModes,
        [key]: mode,
      },
    }))
  },
  getSessionDraft: (sessionId, workspacePath) => {
    return get().sessionDrafts[draftKeyForSession(sessionId, workspacePath)] ?? ''
  },
  getSessionDraftMode: (sessionId, workspacePath) => {
    return get().sessionDraftModes[draftKeyForSession(sessionId, workspacePath)] ?? 'auto'
  },
  bootstrapVault: async () => {
    const current = get()
    if (current.vault.loading || current.vault.bootstrapped) {
      return
    }
    set((state: DesktopStoreState) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await fetchVaultStatus()
      set((state: DesktopStoreState) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
      if (status.enabled && !status.unlocked) {
        set((state: DesktopStoreState) => ({
          ...clearDesktopRuntimeState(state),
          vault: applyVaultStatus(state.vault, status),
        }))
      }
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          bootstrapped: true,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to load vault status',
        },
      }))
    }
  },
  refreshVaultStatus: async () => {
    set((state: DesktopStoreState) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await fetchVaultStatus()
      set((state: DesktopStoreState) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
      if (status.enabled && !status.unlocked) {
        set((state: DesktopStoreState) => ({
          ...clearDesktopRuntimeState(state),
          vault: applyVaultStatus(state.vault, status),
        }))
      }
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to refresh vault status',
        },
      }))
      throw error
    }
  },
  enableVault: async (password) => {
    set((state: DesktopStoreState) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await enableVault(password)
      set((state: DesktopStoreState) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to enable vault',
        },
      }))
      throw error
    }
  },
  unlockVault: async (password, options) => {
    set((state: DesktopStoreState) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await unlockVault(password)
      set((state: DesktopStoreState) => ({
        vault: applyVaultStatus(state.vault, status, {
          openSettingsOnUnlock: Boolean(options?.openSettingsOnUnlock),
        }),
      }))
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to unlock vault',
        },
      }))
      throw error
    }
  },
  lockVault: async () => {
    set((state: DesktopStoreState) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await lockVault()
      set((state: DesktopStoreState) => ({
        ...clearDesktopRuntimeState(state),
        vault: applyVaultStatus(state.vault, status),
      }))
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to lock vault',
        },
      }))
      throw error
    }
  },
  disableVault: async (password) => {
    set((state: DesktopStoreState) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await disableVault(password)
      set((state: DesktopStoreState) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to disable vault',
        },
      }))
      throw error
    }
  },
  exportVaultBundle: async (password, vaultPassword = '') => {
    set((state: DesktopStoreState) => ({
      vault: {
        ...state.vault,
        error: null,
      },
    }))
    try {
      return await exportVaultBundle(password, vaultPassword)
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          error: error instanceof Error ? error.message : 'Failed to export vault bundle',
        },
      }))
      throw error
    }
  },
  importVaultBundle: async (password, bundle, vaultPassword = '') => {
    set((state: DesktopStoreState) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const result = await importVaultBundle(password, bundle, vaultPassword)
      set((state: DesktopStoreState) => (
        result.vault.enabled && !result.vault.unlocked
          ? {
              ...clearDesktopRuntimeState(state),
              vault: applyVaultStatus(state.vault, result.vault),
            }
          : {
              vault: applyVaultStatus(state.vault, result.vault),
            }
      ))
      return result
    } catch (error) {
      set((state: DesktopStoreState) => ({
        vault: {
          ...state.vault,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to import vault bundle',
        },
      }))
      throw error
    }
  },
  consumeVaultSettingsRequest: () => {
    const shouldOpen = get().vault.openSettingsOnUnlock
    if (!shouldOpen) {
      return false
    }
    set((state: DesktopStoreState) => ({
      vault: { ...state.vault, openSettingsOnUnlock: false },
    }))
    return true
  },
  requestOnboardingFlow: () => {
    set({ onboardingFlowRequested: true })
  },
  clearOnboardingFlow: () => {
    set({ onboardingFlowRequested: false })
  },
  hydrate: async () => {
    const current = get()
    if (current.hydrating) {
      return
    }
    set({ hydrating: true, realtimeDesired: true })
    try {
      installV3RuntimePersistence()
      await restoreV3RuntimeFromPersistence()
      if (!get().vault.bootstrapped) {
        await get().bootstrapVault()
      }
      if (get().vault.enabled && !get().vault.unlocked) {
        set({
          hydrated: true,
          hydrating: false,
        })
        return
      }
      set({
        hydrated: true,
        hydrating: false,
      })
      await get().connect()
    } catch (error) {
      console.error('[desktop-store] hydrate failed', error)
      set({ hydrating: false, hydrated: true })
    }
  },
  connect: async () => {
    const current = get()
    if (!shouldMaintainDesktopRealtime(current)) {
      return
    }
    if (current.connectionState === 'connecting') {
      return
    }
    clearReconnectTimer(current)
    clearHeartbeatTimer(current)
    clearLivenessTimer(current)
    desktopRealtimeSocket?.close()
    desktopRealtimeSocket = null
    desktopRealtimeLastActivityAt = 0
    desktopRealtimeConnectingStartedAt = 0
    const generation = current.connectionGeneration + 1
    const startedAt = Date.now()
    set({
      connectionGeneration: generation,
      connectionState: 'connecting',
      reconnectTimer: null,
      heartbeatTimer: null,
      livenessTimer: null,
    })
    try {
      await ensureDesktopSession(true)
      if (get().connectionGeneration !== generation || !shouldMaintainDesktopRealtime(get())) {
        return
      }
      const runtime = ensureDesktopSessionV3Runtime()
      await runtime.boot()
      if (get().connectionGeneration !== generation || !shouldMaintainDesktopRealtime(get())) {
        runtime.stop('desktop connect no longer desired')
        return
      }
      desktopV3ReconnectSyncDiagnostics = {
        status: 'applied',
        owner: 'desktop-session-v3-runtime',
        operation: 'boot',
        force: true,
        startedAt,
        completedAt: Date.now(),
        endpointCursorPresent: Boolean(runtime.getState().endpointCursor.trim()),
        subscriptionCount: Object.keys(runtime.getState().subscriptionsBySessionId).length,
        worksetCount: Object.keys(runtime.getState().worksetsById).length,
        discoveredSessionCount: runtime.getState().discoveredSessionIds.length,
        diagnostics: runtime.diagnostics(),
      }
      const diagnostics = runtime.diagnostics()
      set({
        connectionState: get().connectionState === 'open' || diagnostics.status === 'open' ? 'open' : 'connecting',
        reconnectAttempt: 0,
        reconnectTimer: null,
      })
      deferDesktopCacheMutation('workspace overview refresh on V3 runtime connect', () => {
        void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
      })
    } catch (error) {
      console.error('[desktop-store] connect failed', error)
      const state = get()
      if (state.connectionGeneration !== generation) {
        return
      }
      set({ connectionState: 'error' })
      scheduleReconnect('connect failure')
    }
  },
  reconnectIfStale: async (reason) => {
    const current = get()
    const runtime = ensureDesktopSessionV3Runtime()
    const diagnostics = runtime.diagnostics()
    const staleReason = !shouldMaintainDesktopRealtime(current)
      ? null
      : diagnostics.status === 'open' && diagnostics.socketState === 'open'
        ? null
        : `V3 runtime transport ${diagnostics.status}/${diagnostics.socketState} after ${reason}`
    if (!staleReason) {
      await get().connect()
      get().syncV3RealtimeSessions()
      return
    }
    clearReconnectTimer(current)
    clearHeartbeatTimer(current)
    clearLivenessTimer(current)
    desktopRealtimeSocket?.close()
    desktopRealtimeSocket = null
    desktopRealtimeLastActivityAt = 0
    desktopRealtimeConnectingStartedAt = 0
    runtime.stop(staleReason)
    set({
      reconnectTimer: null,
      heartbeatTimer: null,
      livenessTimer: null,
      connectionGeneration: current.connectionGeneration + 1,
      connectionState: 'closed',
    })
    console.warn(`[desktop-store] forcing realtime reconnect: ${staleReason}`)
    await get().connect()
    get().syncV3RealtimeSessions()
  },
  syncV3RealtimeSessions: (options = {}) => {
    const current = get()
    const force = Boolean(options.force)
    if (!force && !shouldMaintainDesktopRealtime(current)) {
      desktopV3ReconnectSyncDiagnostics = {
        status: 'skipped',
        reason: !current.realtimeDesired
          ? 'store.realtimeDesired=false'
          : current.vault.enabled && !current.vault.unlocked
            ? 'store.vault-locked'
            : 'store.should-maintain=false',
        force,
        requestedAt: Date.now(),
        connectionState: current.connectionState,
        realtimeDesired: current.realtimeDesired,
        vaultEnabled: current.vault.enabled,
        vaultUnlocked: current.vault.unlocked,
      }
      return
    }
      if (force && !current.realtimeDesired) {
      set({ realtimeDesired: true })
    }
    void syncV3RuntimeSessions({ force }).catch((error) => {
      console.error('[desktop-store] session v3 runtime sync failed', error)
    })
  },
  disconnect: () => {
    const current = get()
    clearReconnectTimer(current)
    clearHeartbeatTimer(current)
    clearLivenessTimer(current)
    const nextGeneration = current.connectionGeneration + 1
    const socket = desktopRealtimeSocket
    desktopRealtimeSocket = null
    desktopRealtimeLastActivityAt = 0
    desktopRealtimeConnectingStartedAt = 0
    set({
      reconnectTimer: null,
      heartbeatTimer: null,
      livenessTimer: null,
      reconnectAttempt: 0,
      connectionGeneration: nextGeneration,
      realtimeDesired: false,
      connectionState: 'idle',
    })
    socket?.close()
    requireRunStreamController().closeAll()
    requireV3RealtimeController().closeAll()
    desktopSessionV3Runtime?.stop('desktop disconnected')
  },
  createSession: async (input) => {
    const target = getDesktopSessionCreateTarget(input.route)
    if (target.endpoint === null) {
      throw new Error(target.unsupportedReason)
    }
    const runtime = ensureDesktopSessionV3Runtime()
    const response = await runtime.createSession({
      title: input.title,
      workspacePath: input.workspacePath,
      workspaceName: input.workspaceName,
      mode: input.mode,
      agentName: input.agentName,
      metadata: input.metadata,
      preference: input.preference,
      workspaceBindingId: target.workspaceBindingId,
      swarmId: target.swarmId,
      targetKind: input.route?.targetKind ?? 'host',
      targetRelationship: input.route?.targetRelationship ?? 'self',
      hostWorkspacePath: input.route?.hostWorkspacePath,
      runtimeWorkspacePath: input.route?.runtimeWorkspacePath,
      worktreeMode: input.worktreeMode,
      worktreeUseCurrentBranch: input.worktreeUseCurrentBranch,
      worktreeBaseBranch: input.worktreeBaseBranch,
      worktreeBranchName: input.worktreeBranchName,
    })
    const sessionId = response.session?.id?.trim() || response.projection?.session_id?.trim() || ''
    const runtimeSession = sessionId ? runtime.getState().desktop.sessionsById[sessionId] : null
    if (!runtimeSession?.id) {
      throw new Error('Sessions API v3 create did not return a hydrated session.')
    }
    const session = applyDesktopChatRouteToSession(runtimeSession, input.route)
    get().upsertSession(session)
    set({ realtimeDesired: true })
    await get().connect()
    return session
  },
  closeRunStream: (sessionId) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    const sessionApi = get().sessions[normalizedSessionId]?.sessionApi?.trim().toLowerCase() || 'v3'
    if (sessionApi === 'v3') {
      ensureDesktopSessionV3Runtime().closeSession(normalizedSessionId)
      requireRunStreamController().close(normalizedSessionId)
      return
    }
    requireRunStreamController().close(normalizedSessionId)
  },
  ensureRunStream: async (sessionId: string, runId?: string | null) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    const sessionApi = get().sessions[normalizedSessionId]?.sessionApi?.trim().toLowerCase() || 'v3'
    if (sessionApi === 'v3') {
      set({ realtimeDesired: true })
      const runtime = ensureDesktopSessionV3Runtime()
      runtime.setWantedSessions([normalizedSessionId])
      await get().connect()
      requireRunStreamController().close(normalizedSessionId)
      return
    }
    await requireRunStreamController().ensure(normalizedSessionId, runId)
  },
  stopRun: async (sessionId, route = null, runId = null) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    const session = get().sessions[normalizedSessionId]
    const resolvedRunId = resolveStopRunId(session, runId)
    const sessionApi = session?.sessionApi?.trim().toLowerCase() || 'v3'
    try {
      if (sessionApi === 'v3') {
        const runtime = ensureDesktopSessionV3Runtime()
        const target = getDesktopSessionStopTarget(route)
        if (target.endpoint === null) {
          throw new Error(target.unsupportedReason)
        }
        runtime.setWantedSessions([normalizedSessionId])
        await runtime.stopRun({ sessionId: normalizedSessionId, runId: resolvedRunId, targetSwarmId: target.targetSwarmId })
        return
      }
      await requireRunStreamController().stop({ sessionId: normalizedSessionId, runId: resolvedRunId, route, sessionApi })
    } catch (error) {
      throw error
    }
  },
  submitPrompt: async ({ sessionId, route = null, sessionApi = null, clientRequestId: providedClientRequestId = null, workspacePath, prompt, agentName, compact = false, targetKind = '', targetName = '' }: {
    sessionId: string | null
    route?: DesktopChatRoute | null
    sessionApi?: string | null
    clientRequestId?: string | null
    workspacePath: string
    prompt: string
    agentName: string
    compact?: boolean
    targetKind?: string
    targetName?: string
  }) => {
    const trimmedPrompt = prompt.trim()
    if (!trimmedPrompt && !compact) {
      return
    }

    const targetSessionId = sessionId?.trim() ?? ''
    const submitStartedAt = Date.now()
    const sourceDraftKey = draftKeyForSession(targetSessionId || null, workspacePath)

    if (!targetSessionId) {
      throw new Error('submitPrompt requires an attached session')
    }

    const requestedSessionApi = sessionApi?.trim().toLowerCase() || get().sessions[targetSessionId]?.sessionApi?.trim().toLowerCase() || 'v3'
    if (requestedSessionApi !== 'v3') {
      throw new Error('Desktop sessions only support Sessions API v3.')
    }
    const effectiveSessionApi = 'v3'
    void route
    void targetKind
    void targetName

    get().closeRunStream(targetSessionId)
    set((state: DesktopStoreState) => {
      cancelDraftFlush(targetSessionId)
      const nextSessionDrafts = { ...state.sessionDrafts }
      nextSessionDrafts[targetSessionId] = ''
      if (sourceDraftKey !== targetSessionId) {
        delete nextSessionDrafts[sourceDraftKey]
      }
      return {
        sessionDrafts: nextSessionDrafts,
      }
    })

    try {
      if (effectiveSessionApi === 'v3' && compact) {
        const clientRequestId = providedClientRequestId?.trim() || `desktop-v3-compact:${targetSessionId}:${submitStartedAt}`
        const runtime = ensureDesktopSessionV3Runtime()
        runtime.setWantedSessions([targetSessionId])
        await runtime.compactSession({ sessionId: targetSessionId, note: trimmedPrompt, agentName, clientRequestId })
        set({ realtimeDesired: true })
        runtime.setWantedSessions([targetSessionId])
        await get().connect()
        return
      }

      if (effectiveSessionApi === 'v3') {
        const clientRequestId = providedClientRequestId?.trim() || `desktop-v3-message:${targetSessionId}:${submitStartedAt}`
        const trace: DesktopV3FollowUpTrace = {
          sessionId: targetSessionId,
          startedAt: submitStartedAt,
          steps: [],
          assistantEvents: [],
          receivedAssistantText: '',
          completedAssistantText: '',
        }
        activeDesktopV3FollowUpTraces.set(targetSessionId, trace)
        pushDesktopV3FollowUpTraceStep(trace, 'submit-start', {
          clientRequestId,
          promptLength: trimmedPrompt.length,
          beforeRealtime: summarizeDesktopV3RealtimeDiagnostics(getDesktopV3RealtimeDiagnostics(targetSessionId)),
        })
        try {
          const runtime = ensureDesktopSessionV3Runtime()
          runtime.setWantedSessions([targetSessionId])
          const result = await runtime.sendMessage({ sessionId: targetSessionId, content: trimmedPrompt, clientRequestId })
          const message = result.message ?? null
          const runIntent = result.active_run_intent ?? result.run_intent ?? null
          const realtimeOutbox = result.realtime_outbox ?? result.mutation?.realtime_outbox ?? null
          pushDesktopV3FollowUpTraceStep(trace, 'message-post-returned', {
            ok: result.ok,
            messageId: message?.id ?? '',
            runIntentPresent: Boolean(runIntent),
            realtimeOutboxPresent: Boolean(realtimeOutbox),
            realtimeOutboxSessionId: realtimeOutbox?.session_id ?? '',
            endpointSeq: realtimeOutbox?.endpoint_seq ?? 0,
            endpointCursorPresent: Boolean(realtimeOutbox?.endpoint_cursor?.trim()),
          })
          const mutationEndpointCursor = realtimeOutbox?.endpoint_cursor?.trim() ?? ''
          if (mutationEndpointCursor) {
            desktopV3RealtimeEndpointCursor = mutationEndpointCursor
          }
          pushDesktopV3FollowUpTraceStep(trace, 'message-commit-applied', {
            mutationEndpointCursorPresent: Boolean(mutationEndpointCursor),
            afterCommitRealtime: summarizeDesktopV3RealtimeDiagnostics(getDesktopV3RealtimeDiagnostics(targetSessionId)),
          })
          set({ realtimeDesired: true })
          runtime.setWantedSessions([targetSessionId])
          await get().connect()
          pushDesktopV3FollowUpTraceStep(trace, 'store-connect-finished', {
            storeConnectionState: get().connectionState,
            afterStoreConnectRealtime: summarizeDesktopV3RealtimeDiagnostics(getDesktopV3RealtimeDiagnostics(targetSessionId)),
          })
          window.setTimeout(() => reportDesktopV3FollowUpTrace(trace), 3000)
        } catch (error) {
          pushDesktopV3FollowUpTraceStep(trace, 'failed', {
            error: error instanceof Error ? error.message : String(error),
            realtime: summarizeDesktopV3RealtimeDiagnostics(getDesktopV3RealtimeDiagnostics(targetSessionId)),
          })
          reportDesktopV3FollowUpTrace(trace)
          throw error
        }
        return
      }

    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to start run'
      set((state: DesktopStoreState) => applyRunStreamResumeFailure(state, targetSessionId, message, Date.now()))
      throw error
    }
  },
  __testApplyRunStreamFrame: (sessionId, payload, ts = Date.now()) => {
    set((state: DesktopStoreState) => applyRunStreamFrame(state, sessionId, payload as RunStreamEventMessage, ts))
  },
  __testApplyV3RealtimeFrame: (sessionId, payload, ts = Date.now()) => {
    applyDesktopV3RealtimeFrame(sessionId, payload as DesktopV3RealtimeFrame, ts)
  },
}))

export const useDesktopStore = useDesktopUiStore
