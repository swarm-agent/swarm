import { createEmptyWorkspaceTodoSummary, type WorkspaceTodoSummary } from '../../workspaces/todos/types'
import type { QueryClient } from '@tanstack/react-query'
import { create } from 'zustand'
import { debugLog, createDebugTimer } from '../../../lib/debug-log'
import { queryClient } from '../../../app/query-client'
import {
  canonicalSessionWorkspaceName,
  canonicalSessionWorkspacePath,
} from '../services/session-workspace'
import {
  syncWorkspaceOverviewSession,
  syncWorkspaceOverviewThemeState,
  syncWorkspaceOverviewWorktreeState,
} from '../../workspaces/launcher/services/workspace-overview-cache'
import { applyWorkspaceTheme, setWorkspaceThemeCustomOptions } from '../../workspaces/launcher/services/workspace-theme'
import { openDesktopWebSocket } from '../realtime/client'
import { disableVault, enableVault, exportVaultBundle, fetchVaultStatus, importVaultBundle, lockVault, unlockVault } from '../vault/api'
import {
  fetchSession,
  fetchSessionPendingPermissions,
  fetchSessionUsageSummary,
} from '../chat/queries/chat-queries'
import { sessionMessagesQueryOptions, sessionPreferenceQueryKey, uiSettingsQueryKey } from '../../queries/query-options'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import { normalizeGlobalThemeSettings, normalizeSwarmSettings, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import {
  loadDesktopActiveSessionId,
  loadDesktopActiveWorkspacePath,
  saveDesktopActiveSessionId,
  saveDesktopActiveWorkspacePath,
} from './desktop-selection-storage'
import type {
  DesktopNotificationCenterRecord,
  DesktopNotificationRecord,
  DesktopNotificationSummary,
  DesktopSessionRecord,
  DesktopStoreState,
} from '../types/realtime'
import type { ChatMessageRecord } from '../chat/types/chat'
import type { VaultStatus } from '../vault/types'
import type { WorkspaceOverviewResponse } from '../../workspaces/launcher/types/workspace-overview'
import { DesktopRunStreamController, type RunStreamEventMessage } from './run-stream-controller'
import { sessionRequiresSnapshotHydration } from './session-snapshot-hydration'
import { fetchNotifications, fetchNotificationSummary, updateNotification } from '../notifications/api'
import type { DurableNotificationRecord } from '../notifications/types'

interface EventEnvelope<T = Record<string, unknown>> {
  global_seq?: number
  stream?: string
  event_type?: string
  entity_id?: string
  ts_unix_ms?: number
  payload?: T
}

interface SocketMessage {
  type?: string
  event?: EventEnvelope
}

type DraftFlushState = {
  assistantDraft: string
  reasoningSummary: string
  reasoningText: string
  reasoningState: DesktopSessionRecord['live']['reasoningState']
  reasoningSegment: number
  toolOutput?: string
}

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
const HEARTBEAT_INTERVAL_MS = 15_000
const LIVENESS_TIMEOUT_MS = 45_000
const NEW_SESSION_DRAFT_KEY_PREFIX = '__workspace__:'
const MAX_LIVE_TOOL_OUTPUT_CHARS = 4000

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
const pendingSessionSnapshotHydrations = new Set<string>()
let desktopRealtimeSocket: WebSocket | null = null
let runStreamController: DesktopRunStreamController | null = null

function requireRunStreamController(): DesktopRunStreamController {
  if (!runStreamController) {
    throw new Error('run stream controller is not initialized')
  }
  return runStreamController
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
    readAt: typeof record.read_at === 'number' && record.read_at > 0 ? record.read_at : null,
    ackedAt: typeof record.acked_at === 'number' && record.acked_at > 0 ? record.acked_at : null,
    mutedAt: typeof record.muted_at === 'number' && record.muted_at > 0 ? record.muted_at : null,
    createdAt: typeof record.created_at === 'number' ? record.created_at : 0,
    updatedAt: typeof record.updated_at === 'number' ? record.updated_at : 0,
  }
}

function mapNotificationSummary(summary: Awaited<ReturnType<typeof fetchNotificationSummary>>): DesktopNotificationSummary {
  return {
    swarmID: summary.swarm_id,
    totalCount: summary.total_count,
    unreadCount: summary.unread_count,
    activeCount: summary.active_count,
    updatedAt: summary.updated_at,
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
    bootstrapped: false,
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
  desktopRealtimeSocket = null
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

function resolveDesktopGlobalThemeId(): string {
  const settings = queryClient.getQueryData<UISettingsWire>(uiSettingsQueryKey())
  return normalizeGlobalThemeSettings(settings).activeId
}

function resolveDesktopWorkspaceThemeId(workspacePath: string | null): string {
  const normalizedWorkspacePath = workspacePath?.trim() ?? ''
  if (!normalizedWorkspacePath) {
    return ''
  }
  const queries = queryClient.getQueryCache().findAll({ queryKey: ['workspace-overview'] })
  for (const query of queries) {
    const overview = query.state.data as WorkspaceOverviewResponse | undefined
    const workspace = overview?.workspaces?.find((entry) => entry.path === normalizedWorkspacePath)
    if (workspace) {
      return workspace.themeId?.trim().toLowerCase() ?? ''
    }
  }
  return ''
}

function themeCustomOptionsSignature(settings?: UISettingsWire | null): string {
  return JSON.stringify(Array.isArray(settings?.theme?.custom_themes) ? settings.theme.custom_themes : [])
}

function themeSettingsChanged(previous?: UISettingsWire | null, next?: UISettingsWire | null): boolean {
  return normalizeGlobalThemeSettings(previous).activeId !== normalizeGlobalThemeSettings(next).activeId
    || themeCustomOptionsSignature(previous) !== themeCustomOptionsSignature(next)
}

function applyDesktopEffectiveTheme(activeWorkspacePath: string | null): void {
  const workspaceThemeId = resolveDesktopWorkspaceThemeId(activeWorkspacePath)
  const globalThemeId = resolveDesktopGlobalThemeId()
  applyWorkspaceTheme(workspaceThemeId || globalThemeId)
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

function armLivenessTimer(generation: number): void {
  const state = useDesktopStore.getState()
  clearLivenessTimer(state)
  if (state.connectionGeneration !== generation || !shouldMaintainDesktopRealtime(state)) {
    return
  }
  const timer = window.setTimeout(() => {
    const current = useDesktopStore.getState()
    if (current.connectionGeneration !== generation || current.livenessTimer !== timer) {
      return
    }
    console.warn('[desktop-store] websocket liveness timeout; forcing reconnect')
    scheduleReconnect('liveness timeout')
  }, LIVENESS_TIMEOUT_MS)
  useDesktopStore.setState({ livenessTimer: timer })
}

function startHeartbeat(socket: WebSocket, generation: number): void {
  const state = useDesktopStore.getState()
  clearHeartbeatTimer(state)
  armLivenessTimer(generation)
  const timer = window.setInterval(() => {
    const current = useDesktopStore.getState()
    if (current.connectionGeneration !== generation || desktopRealtimeSocket !== socket || !shouldMaintainDesktopRealtime(current)) {
      clearHeartbeatTimer(current)
      return
    }
    try {
      socket.send(JSON.stringify({ type: 'ping' }))
    } catch (error) {
      console.error('[desktop-store] heartbeat ping failed', error)
      scheduleReconnect('heartbeat ping failure')
    }
  }, HEARTBEAT_INTERVAL_MS)
  useDesktopStore.setState({ heartbeatTimer: timer })
}

function scheduleReconnect(reason: string): void {
  const current = useDesktopStore.getState()
  debugLog('desktop-store', 'reconnect:schedule-check', {
    reason,
    connectionState: current.connectionState,
    reconnectAttempt: current.reconnectAttempt,
    realtimeDesired: current.realtimeDesired,
    vaultEnabled: current.vault.enabled,
    vaultUnlocked: current.vault.unlocked,
  })
  if (!shouldMaintainDesktopRealtime(current)) {
    setConnectionClosed(current.connectionGeneration)
    return
  }
  clearReconnectTimer(current)
  clearHeartbeatTimer(current)
  clearLivenessTimer(current)
  desktopRealtimeSocket?.close()
  desktopRealtimeSocket = null
  const attempt = current.reconnectAttempt
  const timer = window.setTimeout(() => {
    const state = useDesktopStore.getState()
    if (state.reconnectTimer !== timer) {
      return
    }
    useDesktopStore.setState({ reconnectTimer: null, connectionState: 'closed' })
    if (!shouldMaintainDesktopRealtime(useDesktopStore.getState())) {
      return
    }
    void useDesktopStore.getState().connect()
  }, reconnectDelayMs(attempt))
  useDesktopStore.setState({
    reconnectTimer: timer,
    heartbeatTimer: null,
    livenessTimer: null,
    reconnectAttempt: attempt + 1,
    connectionState: 'closed',
  })
  debugLog('desktop-store', 'reconnect:scheduled', {
    reason,
    reconnectAttempt: attempt + 1,
  })
  console.warn(`[desktop-store] scheduled reconnect after ${reason}`)
}

function setConnectionClosed(generation: number): void {
  const state = useDesktopStore.getState()
  if (state.connectionGeneration !== generation) {
    return
  }
  clearReconnectTimer(state)
  clearHeartbeatTimer(state)
  clearLivenessTimer(state)
  useDesktopStore.setState({
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
    agentName: null,
    startedAt: null,
    status: 'idle',
    step: 0,
    toolName: null,
    toolCallId: null,
    toolArguments: null,
    toolOutput: '',
    retainedToolName: null,
    retainedToolCallId: null,
    retainedToolArguments: null,
    retainedToolOutput: '',
    retainedToolState: null,
    summary: null,
    lastEventType: null,
    lastEventAt: null,
    error: null,
    seq: 0,
    assistantDraft: '',
    reasoningSummary: '',
    reasoningText: '',
    reasoningState: 'idle',
    reasoningSegment: 0,
    reasoningStartedAt: null,
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

function resetRetainedLiveToolState(live: DesktopSessionRecord['live']): void {
  live.retainedToolName = null
  live.retainedToolCallId = null
  live.retainedToolArguments = null
  live.retainedToolOutput = ''
  live.retainedToolState = null
}

function resetLiveReasoningState(live: DesktopSessionRecord['live']): void {
  live.reasoningSummary = ''
  live.reasoningText = ''
  live.reasoningState = 'idle'
  live.reasoningStartedAt = null
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

function resolveRunStreamId(session: DesktopSessionRecord | undefined, runId?: string | null): string {
  const explicitRunId = runId?.trim() ?? ''
  if (explicitRunId) {
    return explicitRunId
  }
  const liveRunId = session?.live.runId?.trim() ?? ''
  if (liveRunId) {
    return liveRunId
  }
  if (session?.lifecycle?.active) {
    return session.lifecycle.runId?.trim() ?? ''
  }
  return ''
}

function resolveRunStreamResumeRequest(sessionId: string, fallbackRunId?: string | null): { sessionId: string; runId: string; lastSeq: number } | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  const state = useDesktopStore.getState()
  const session = state.sessions[normalizedSessionId]
  if (!session) {
    return null
  }
  if (session.live.status === 'idle' || session.live.status === 'error') {
    return null
  }
  const runId = resolveRunStreamId(session, fallbackRunId)
  if (!runId) {
    return null
  }
  if (session.lifecycle && !session.lifecycle.active) {
    const liveStatus = session.live.status
    const canResumeNewlyAcceptedRun = session.live.awaitingAck
      || liveStatus === 'starting'
      || liveStatus === 'running'
      || liveStatus === 'blocked'
    if (!canResumeNewlyAcceptedRun) {
      return null
    }
  }
  if (session.live.summary?.trim() === 'Reconnecting…' && !session.lifecycle?.active && !session.live.runId?.trim()) {
    return null
  }
  return {
    sessionId: normalizedSessionId,
    runId,
    lastSeq: session.live.seq ?? 0,
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
    phase: String(input.phase ?? '').trim(),
    startedAt: typeof input.started_at === 'number' ? input.started_at : 0,
    endedAt: typeof input.ended_at === 'number' ? input.ended_at : 0,
    updatedAt: typeof input.updated_at === 'number' ? input.updated_at : 0,
    generation: typeof input.generation === 'number' ? input.generation : 0,
    stopReason: String(input.stop_reason ?? '').trim() || null,
    error: String(input.error ?? '').trim() || null,
    ownerTransport: String(input.owner_transport ?? '').trim() || null,
  }
}

function lifecycleStatusForLive(session: DesktopSessionRecord, lifecycle: NonNullable<DesktopSessionRecord['lifecycle']>): DesktopSessionRecord['live']['status'] {
  const phase = lifecycle.phase.trim().toLowerCase()
  if (lifecycle.active) {
    switch (phase) {
      case 'blocked':
      case 'starting':
      case 'running':
        return phase as DesktopSessionRecord['live']['status']
      default:
        return 'running'
    }
  }
  if (phase === 'errored') {
    return 'error'
  }
  return session.live.awaitingAck ? session.live.status : 'idle'
}

function lifecycleTerminalSummary(lifecycle: NonNullable<DesktopSessionRecord['lifecycle']>): string | null {
  const phase = lifecycle.phase.trim().toLowerCase()
  const stopReason = lifecycle.stopReason?.trim() ?? ''
  const error = lifecycle.error?.trim() ?? ''
  switch (phase) {
    case 'cancelled':
    case 'canceled':
    case 'interrupted':
    case 'completed':
      return stopReason || null
    case 'errored':
      return error || stopReason || null
    default:
      return null
  }
}

function applyLifecycleSnapshot(
  sessionId: string,
  session: DesktopSessionRecord,
  lifecycle: NonNullable<DesktopSessionRecord['lifecycle']>,
  ts: number,
  eventType: string,
): void {
  const nextTs = lifecycle.updatedAt > 0 ? lifecycle.updatedAt : ts
  session.lifecycle = lifecycle
  session.live.lastEventType = eventType || session.live.lastEventType
  session.live.lastEventAt = nextTs
  session.live.awaitingAck = false
  session.live.runId = lifecycle.active ? lifecycle.runId : null
  session.live.startedAt = lifecycle.active && lifecycle.startedAt > 0 ? lifecycle.startedAt : null
  session.live.status = lifecycleStatusForLive(session, lifecycle)
  session.live.error = lifecycle.phase.trim().toLowerCase() === 'errored'
    ? (lifecycle.error?.trim() || lifecycle.stopReason?.trim() || null)
    : null

  if (lifecycle.active) {
    return
  }

  cancelDraftFlush(sessionId)
  retainLiveToolState(session.live, lifecycle.phase.trim().toLowerCase() === 'errored' ? 'error' : 'done')
  resetLiveToolState(session.live)
  session.live.assistantDraft = ''
  resetLiveReasoningState(session.live)
  session.live.summary = lifecycleTerminalSummary(lifecycle)
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
    window.cancelAnimationFrame(timer)
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
  useDesktopStore.setState((state) => {
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
  const raf = window.requestAnimationFrame(() => flushDraftState(sessionId))
  draftFlushTimers.set(sessionId, raf)
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
    live: emptyLiveState(),
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

function mergeSessionRecords(existing: DesktopSessionRecord | null, incoming: DesktopSessionRecord): DesktopSessionRecord {
  if (!existing) {
    return incoming
  }

  return {
    ...incoming,
    title: incoming.title || existing.title,
    workspacePath: incoming.workspacePath || existing.workspacePath,
    workspaceName: incoming.workspaceName || existing.workspaceName,
    mode: incoming.mode || existing.mode || 'auto',
    messageCount: Math.max(existing.messageCount, incoming.messageCount),
    updatedAt: Math.max(existing.updatedAt, incoming.updatedAt),
    createdAt:
      existing.createdAt > 0 && incoming.createdAt > 0
        ? Math.min(existing.createdAt, incoming.createdAt)
        : Math.max(existing.createdAt, incoming.createdAt),
    permissionsHydrated: incoming.permissionsHydrated || existing.permissionsHydrated,
    runtimeWorkspacePath: incoming.runtimeWorkspacePath || existing.runtimeWorkspacePath || incoming.workspacePath || existing.workspacePath,
    worktreeEnabled: incoming.worktreeEnabled ?? existing.worktreeEnabled ?? false,
    worktreeRootPath: incoming.worktreeRootPath || existing.worktreeRootPath || '',
    worktreeBaseBranch: incoming.worktreeBaseBranch || existing.worktreeBaseBranch || '',
    worktreeBranch: incoming.worktreeBranch || existing.worktreeBranch || '',
    gitBranch: incoming.gitBranch || existing.gitBranch || '',
    metadata: incoming.metadata ?? existing.metadata,
    gitHasGit: incoming.gitHasGit ?? existing.gitHasGit ?? false,
    gitClean: incoming.gitClean ?? existing.gitClean ?? false,
    gitDirtyCount: incoming.gitDirtyCount ?? existing.gitDirtyCount ?? 0,
    gitStagedCount: incoming.gitStagedCount ?? existing.gitStagedCount ?? 0,
    gitModifiedCount: incoming.gitModifiedCount ?? existing.gitModifiedCount ?? 0,
    gitUntrackedCount: incoming.gitUntrackedCount ?? existing.gitUntrackedCount ?? 0,
    gitConflictCount: incoming.gitConflictCount ?? existing.gitConflictCount ?? 0,
    gitAheadCount: incoming.gitAheadCount ?? existing.gitAheadCount ?? 0,
    gitBehindCount: incoming.gitBehindCount ?? existing.gitBehindCount ?? 0,
    gitCommitDetected: incoming.gitCommitDetected ?? existing.gitCommitDetected ?? false,
    gitCommitCount: incoming.gitCommitCount ?? existing.gitCommitCount ?? 0,
    gitCommittedFileCount: incoming.gitCommittedFileCount ?? existing.gitCommittedFileCount ?? 0,
    gitCommittedAdditions: incoming.gitCommittedAdditions ?? existing.gitCommittedAdditions ?? 0,
    gitCommittedDeletions: incoming.gitCommittedDeletions ?? existing.gitCommittedDeletions ?? 0,
    lifecycle: incoming.lifecycle ?? existing.lifecycle,
    live: incoming.live,
    pendingPermissions: incoming.pendingPermissions,
    pendingPermissionCount: incoming.pendingPermissionCount,
    usage: incoming.usage ?? existing.usage,
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
  window.setTimeout(() => {
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

function requestAuthoritativeSessionSnapshot(sessionId: string): void {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId || pendingSessionSnapshotHydrations.has(normalizedSessionId)) {
    return
  }

  pendingSessionSnapshotHydrations.add(normalizedSessionId)
  window.setTimeout(() => {
    void (async () => {
      try {
        const fetchedSession = await fetchSession(normalizedSessionId)
        if (!fetchedSession) {
          return
        }
        useDesktopStore.getState().upsertSession(fetchedSession)
      } catch (error) {
        console.error('[desktop-store] authoritative session hydration failed', error)
      } finally {
        pendingSessionSnapshotHydrations.delete(normalizedSessionId)
      }
    })()
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

function nextLiveStatusAfterPermissionSync(session: Pick<DesktopSessionRecord, 'lifecycle' | 'live'>): DesktopSessionRecord['live']['status'] {
  if (session.lifecycle?.active || session.live.runId || session.live.awaitingAck || session.live.startedAt !== null) {
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

  if (session.lifecycle) {
    if (summary) {
      session.live.summary = summary
    }
    if (error && session.lifecycle.phase.trim().toLowerCase() === 'errored') {
      session.live.error = error
    }
    return
  }

  session.live.error = error || null

  if (summary) {
    session.live.summary = summary
  }
  if (runId) {
    session.live.runId = runId
  }

  switch (status) {
    case 'starting':
    case 'running':
    case 'blocked':
      session.live.status = status
      if (status === 'running' && session.live.startedAt === null) {
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
      session.live.summary = null
      session.live.assistantDraft = ''
      resetLiveReasoningState(session.live)
      session.live.error = null
      break
    case 'error':
      session.live.status = 'error'
      session.live.runId = null
      session.live.startedAt = null
      retainLiveToolState(session.live, 'error')
      resetLiveToolState(session.live)
      session.live.summary = summary || null
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

function updateMessagesCache(sessionId: string, message: ChatMessageRecord): void {
  deferDesktopCacheMutation('message cache sync', () => {
    queryClient.setQueryData(sessionMessagesQueryOptions(sessionId).queryKey, (current: ChatMessageRecord[] | undefined) => {
      const next = current ?? []
      const existingIndex = next.findIndex((entry) => entry.globalSeq === message.globalSeq)
      if (existingIndex >= 0) {
        const updated = [...next]
        updated[existingIndex] = message
        return updated
      }
      return [...next, message].sort((left, right) => left.globalSeq - right.globalSeq)
    })
  })
}

function applyRunStreamFrame(state: DesktopStoreState, sessionId: string, payload: RunStreamEventMessage, ts: number): Partial<DesktopStoreState> {
  const sessions = { ...state.sessions }
  const session = { ...ensureSession(state, sessionId), live: { ...ensureSession(state, sessionId).live }, pendingPermissions: [...ensureSession(state, sessionId).pendingPermissions] }
  const type = String(payload.type ?? '').trim()
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
        if (!lifecycle.active && resolveRunStreamId(session) !== '') {
          requireRunStreamController().close(sessionId)
        }
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
      session.live.toolName = String(payload.tool_name ?? '').trim() || session.live.toolName
      if (typeof payload.summary === 'string' && payload.summary.trim() !== '') {
        session.live.summary = payload.summary.trim()
      } else if (type === 'tool.started' && session.live.toolName?.trim()) {
        session.live.summary = session.live.toolName.trim()
      }
      if (typeof payload.arguments === 'string') {
        session.live.toolArguments = payload.arguments.trim() || null
      }
      if (typeof payload.call_id === 'string' && payload.call_id.trim() !== '') {
        session.live.toolCallId = payload.call_id.trim()
      }
      if (type === 'tool.started') {
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
      } else if (type === 'tool.delta' && typeof payload.output === 'string') {
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
      } else if (type === 'tool.completed') {
        const completedToolOutput = typeof payload.raw_output === 'string'
          ? replaceLiveToolOutput(payload.raw_output)
          : typeof payload.output === 'string'
            ? replaceLiveToolOutput(payload.output)
            : session.live.toolOutput
        session.live.toolOutput = completedToolOutput
        retainLiveToolState(session.live, 'done')
        scheduleDraftFlush(sessionId, {
          assistantDraft: session.live.assistantDraft,
          reasoningSummary: session.live.reasoningSummary,
          reasoningText: session.live.reasoningText,
          reasoningState: session.live.reasoningState,
          reasoningSegment: session.live.reasoningSegment,
          toolOutput: session.live.toolOutput,
        })
      }
      if (typeof payload.step === 'number') {
        session.live.step = payload.step
      }
      break
    case 'message.stored':
    case 'message.updated': {
      const normalized = normalizeMessage(payload.message, sessionId)
      if (normalized) {
        updateMessagesCache(normalized.sessionId, normalized)
        if (normalized.role === 'assistant') {
          cancelDraftFlush(sessionId)
          session.live.assistantDraft = ''
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
        session.live.assistantDraft = ''
        resetLiveReasoningState(session.live)
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
        resetLiveReasoningState(session.live)
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

  if (existing.lifecycle && !existing.lifecycle.active) {
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
    resetLiveReasoningState(session.live)
  }

  sessions[sessionId] = mergeSessionRecords(existing, session)
  syncBlockedSessionToWorkspaceOverview(queryClient, sessions[sessionId])
  return { sessions }
}

function applyEnvelope(state: DesktopStoreState, envelope: EventEnvelope): Partial<DesktopStoreState> {
  const eventType = typeof envelope.event_type === 'string' ? envelope.event_type : ''
  const ts = typeof envelope.ts_unix_ms === 'number' ? envelope.ts_unix_ms : Date.now()
  const payload = envelope.payload && typeof envelope.payload === 'object' ? envelope.payload : {}
  const payloadRecord = payload as Record<string, unknown>
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
  if (eventType === 'ui.settings.updated') {
    const nextSettings = payload as UISettingsWire
    const previousSettings = queryClient.getQueryData<UISettingsWire>(uiSettingsQueryKey())
    queryClient.setQueryData(uiSettingsQueryKey(), nextSettings)
    queryClient.setQueryData(['ui-settings', 'swarm'], normalizeSwarmSettings(nextSettings))
    if (themeSettingsChanged(previousSettings, nextSettings)) {
      setWorkspaceThemeCustomOptions(nextSettings.theme?.custom_themes ?? [])
      applyDesktopEffectiveTheme(state.activeWorkspacePath)
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
      if (state.activeWorkspacePath === workspacePath) {
        applyWorkspaceTheme(themeId || resolveDesktopGlobalThemeId())
      }
    }
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  const sessionId = typeof payloadRecord.session_id === 'string' ? payloadRecord.session_id : typeof envelope.entity_id === 'string' ? envelope.entity_id : ''
  if (eventType === 'notification.created' || eventType === 'notification.updated') {
    deferDesktopCacheMutation('notification refresh', () => {
      void useDesktopStore.getState().refreshNotifications()
    })
    return { lastGlobalSeq: Math.max(state.lastGlobalSeq, envelope.global_seq ?? 0) }
  }
  if (eventType.startsWith('swarm.')) {
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

  switch (eventType) {
    case 'session.created': {
      const metadataRecord = payloadRecord.metadata && typeof payloadRecord.metadata === 'object'
        ? payloadRecord.metadata as Record<string, unknown>
        : null
      const hostedHostWorkspacePath = typeof metadataRecord?.swarm_routed_host_workspace_path === 'string'
        ? metadataRecord.swarm_routed_host_workspace_path.trim()
        : ''
      const hostedRuntimeWorkspacePath = typeof metadataRecord?.swarm_routed_runtime_workspace_path === 'string'
        ? metadataRecord.swarm_routed_runtime_workspace_path.trim()
        : ''
      session.title = typeof payloadRecord.title === 'string' ? payloadRecord.title : session.title
      session.metadata = metadataRecord ?? session.metadata
      const rawWorkspacePath = typeof payloadRecord.workspace_path === 'string'
        ? payloadRecord.workspace_path.trim()
        : session.workspacePath
      const worktreeRootPath = typeof payloadRecord.worktree_root_path === 'string'
        ? payloadRecord.worktree_root_path.trim()
        : session.worktreeRootPath
      const worktreeEnabled = typeof payloadRecord.worktree_enabled === 'boolean'
        ? payloadRecord.worktree_enabled
        : session.worktreeEnabled
      const nextWorkspacePath = canonicalSessionWorkspacePath({
        workspacePath: rawWorkspacePath,
        hostedHostWorkspacePath,
        worktreeEnabled,
        worktreeRootPath,
      })
      session.workspacePath = session.workspacePath || nextWorkspacePath
      const nextRuntimeWorkspacePath = hostedRuntimeWorkspacePath || (
        typeof payloadRecord.workspace_path === 'string'
          ? payloadRecord.workspace_path.trim()
          : session.runtimeWorkspacePath || session.workspacePath
      )
      session.runtimeWorkspacePath = session.runtimeWorkspacePath || nextRuntimeWorkspacePath
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
      session.createdAt = typeof payloadRecord.created_at === 'number' ? payloadRecord.created_at : session.createdAt || ts
      break
    }
    case 'session.mode.updated':
      session.mode = typeof payloadRecord.mode === 'string' ? payloadRecord.mode : session.mode
      notifications.unshift(makeNotification(sessionId, session.live.runId, eventType, 'Mode updated', session.mode, 'info', ts))
      break
    case 'session.preference.updated': {
      const preferenceSource = payloadRecord.preference && typeof payloadRecord.preference === 'object'
        ? payloadRecord.preference as Record<string, unknown>
        : payloadRecord
      deferDesktopCacheMutation('session preference sync', () => {
        queryClient.setQueryData(sessionPreferenceQueryKey(sessionId), (current: {
          preference?: {
            provider?: string
            model?: string
            thinking?: string
            serviceTier?: string
            contextMode?: string
            updatedAt?: number
          }
          contextWindow?: number
          maxOutputTokens?: number
        } | undefined) => ({
          preference: {
            provider: typeof preferenceSource.provider === 'string' ? preferenceSource.provider.trim() : '',
            model: typeof preferenceSource.model === 'string' ? preferenceSource.model.trim() : '',
            thinking: typeof preferenceSource.thinking === 'string' ? preferenceSource.thinking.trim() : '',
            serviceTier: typeof preferenceSource.service_tier === 'string' ? preferenceSource.service_tier.trim() : '',
            contextMode: typeof preferenceSource.context_mode === 'string' ? preferenceSource.context_mode.trim() : '',
            updatedAt: typeof preferenceSource.updated_at === 'number' ? preferenceSource.updated_at : ts,
          },
          contextWindow: current?.contextWindow ?? 0,
          maxOutputTokens: current?.maxOutputTokens ?? 0,
        }))
      })
      break
    }
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
    case 'session.message.appended':
      session.messageCount += 1
      break
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
        if (!lifecycle.active && resolveRunStreamId(session) !== '') {
          requireRunStreamController().close(sessionId)
        }
      }
      break
    }
    case 'session.status':
      applyAuthoritativeSessionStatus(sessionId, session, typeof payloadRecord.status === 'string' ? payloadRecord.status : '', ts, eventType, {
        runId: typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id : session.live.runId,
        summary: typeof payloadRecord.summary === 'string' ? payloadRecord.summary : session.live.summary,
        error: typeof payloadRecord.error === 'string' ? payloadRecord.error : null,
      })
      break
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
      session.live.reasoningSegment = 0
      session.live.summary = typeof payloadRecord.agent === 'string' ? payloadRecord.agent : 'Running'
      break
    case 'run.step.started':
      session.live.step = typeof payloadRecord.step === 'number' ? payloadRecord.step : session.live.step
      session.live.lastEventType = eventType
      session.live.lastEventAt = ts
      break
    case 'run.tool.started':
      session.live.runId = typeof payloadRecord.run_id === 'string' ? payloadRecord.run_id : session.live.runId
      session.live.toolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name : session.live.toolName
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
      session.live.summary = session.live.toolName ? `Tool: ${session.live.toolName}` : 'Tool started'
      break
    case 'run.tool.delta':
      session.live.toolName = typeof payloadRecord.tool_name === 'string' ? payloadRecord.tool_name : session.live.toolName
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
        updateMessagesCache(normalized.sessionId, normalized)
      }
      break
    }
    case 'message.stored':
      break
    default:
      break
  }

  const merged = mergeSessionRecords(state.sessions[sessionId] ?? null, session)
  sessions[sessionId] = merged
  syncBlockedSessionToWorkspaceOverview(queryClient, merged)
  if (sessionRequiresSnapshotHydration(merged, eventType)) {
    requestAuthoritativeSessionSnapshot(sessionId)
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

const initialActiveSessionId = loadDesktopActiveSessionId()
const initialActiveWorkspacePath = loadDesktopActiveWorkspacePath()

runStreamController = new DesktopRunStreamController({
  getResumeRequest: (sessionId, fallbackRunId) => resolveRunStreamResumeRequest(sessionId, fallbackRunId),
  onFrame: (sessionId, payload, ts) => {
    useDesktopStore.setState((state) => applyRunStreamFrame(state, sessionId, payload, ts))
  },
  onReconnectPending: (sessionId, reason, ts) => {
    useDesktopStore.setState((state) => applyRunStreamSocketFailure(state, sessionId, reason, ts))
  },
  onResumeFailure: (sessionId, message, ts) => {
    useDesktopStore.setState((state) => applyRunStreamResumeFailure(state, sessionId, message, ts))
  },
})

export const useDesktopStore = create<DesktopStoreState>((set, get) => ({
  hydrated: false,
  hydrating: false,
  connectionState: 'idle',
  onboardingFlowRequested: false,
  activeSessionId: initialActiveSessionId,
  activeWorkspacePath: initialActiveWorkspacePath,
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
    set((state) => {
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
    set((state) => {
      if (!session.id) {
        return state
      }
      const merged = mergeExternalSessionRecord(state.sessions[session.id] ?? null, session)
      syncBlockedSessionToWorkspaceOverview(queryClient, merged)
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
    try {
      const [pendingPermissions, usage] = await Promise.all([
        fetchSessionPendingPermissions(normalizedSessionId),
        fetchSessionUsageSummary(normalizedSessionId),
      ])
      set((state) => {
        const existing = state.sessions[normalizedSessionId]
        if (!existing) {
          return state
        }
        const pendingPermissionCount = countApprovalRequiredPermissions(pendingPermissions, existing.mode)
        const nextStatus = pendingPermissionCount > 0
          ? 'blocked'
          : existing.live.status === 'blocked'
            ? nextLiveStatusAfterPermissionSync(existing)
            : existing.live.status
        const nextSession = {
          ...existing,
          permissionsHydrated: true,
          live: {
            ...existing.live,
            status: nextStatus,
          },
          pendingPermissions,
          pendingPermissionCount,
          usage: usage ?? existing.usage,
        }
        syncBlockedSessionToWorkspaceOverview(queryClient, nextSession)
        return {
          sessions: {
            ...state.sessions,
            [normalizedSessionId]: nextSession,
          },
        }
      })
    } catch (error) {
      console.error('[desktop-store] pending permission refresh failed', error)
    }
  },
  refreshNotifications: async () => {
    set((state) => ({
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
      set((state) => ({
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
      set((state) => ({
        notificationCenter: {
          ...state.notificationCenter,
          loading: false,
        },
      }))
    }
  },
  updateNotificationRecord: async (id, patch) => {
    const normalizedID = id.trim()
    if (!normalizedID) {
      return
    }
    const record = await updateNotification(normalizedID, patch)
    const mappedRecord = mapDurableNotification(record)
    set((state) => {
      const existingIndex = state.notificationCenter.items.findIndex((item) => item.id === mappedRecord.id)
      const items = existingIndex >= 0
        ? state.notificationCenter.items.map((item, index) => index === existingIndex ? mappedRecord : item)
        : [mappedRecord, ...state.notificationCenter.items]
      const unreadCount = items.filter((item) => !item.readAt).length
      const activeCount = items.filter((item) => item.status === 'active').length
      const summary: DesktopNotificationSummary = {
        swarmID: mappedRecord.swarmID,
        totalCount: items.length,
        unreadCount,
        activeCount,
        updatedAt: mappedRecord.updatedAt,
      }
      updateBrowserNotificationSignals(summary)
      return {
        notificationCenter: {
          ...state.notificationCenter,
          items,
          summary,
          hydrated: true,
        },
      }
    })
  },
  setSessionDraft: (sessionId, draft) => {
    const key = sessionId.trim()
    if (!key) {
      return
    }
    set((state) => ({
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
    set((state) => ({
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
    debugLog('desktop-store', 'bootstrapVault:enter', {
      loading: current.vault.loading,
      bootstrapped: current.vault.bootstrapped,
    })
    if (current.vault.loading || current.vault.bootstrapped) {
      return
    }
    const finish = createDebugTimer('desktop-store', 'bootstrapVault')
    set((state) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await fetchVaultStatus()
      debugLog('desktop-store', 'bootstrapVault:status', {
        enabled: status.enabled,
        unlocked: status.unlocked,
        storageMode: status.storageMode,
      })
      set((state) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
      if (status.enabled && !status.unlocked) {
        set((state) => ({
          ...clearDesktopRuntimeState(state),
          vault: applyVaultStatus(state.vault, status),
        }))
      }
      finish({ ok: true })
    } catch (error) {
      debugLog('desktop-store', 'bootstrapVault:error', {
        message: error instanceof Error ? error.message : String(error),
      })
      set((state) => ({
        vault: {
          ...state.vault,
          bootstrapped: true,
          loading: false,
          error: error instanceof Error ? error.message : 'Failed to load vault status',
        },
      }))
      finish({ ok: false })
    }
  },
  refreshVaultStatus: async () => {
    set((state) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await fetchVaultStatus()
      set((state) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
      if (status.enabled && !status.unlocked) {
        set((state) => ({
          ...clearDesktopRuntimeState(state),
          vault: applyVaultStatus(state.vault, status),
        }))
      }
    } catch (error) {
      set((state) => ({
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
    set((state) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await enableVault(password)
      set((state) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
    } catch (error) {
      set((state) => ({
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
    set((state) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await unlockVault(password)
      set((state) => ({
        vault: applyVaultStatus(state.vault, status, {
          openSettingsOnUnlock: Boolean(options?.openSettingsOnUnlock),
        }),
      }))
    } catch (error) {
      set((state) => ({
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
    set((state) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await lockVault()
      set((state) => ({
        ...clearDesktopRuntimeState(state),
        vault: applyVaultStatus(state.vault, status),
      }))
    } catch (error) {
      set((state) => ({
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
    set((state) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const status = await disableVault(password)
      set((state) => ({
        vault: applyVaultStatus(state.vault, status),
      }))
    } catch (error) {
      set((state) => ({
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
    set((state) => ({
      vault: {
        ...state.vault,
        error: null,
      },
    }))
    try {
      return await exportVaultBundle(password, vaultPassword)
    } catch (error) {
      set((state) => ({
        vault: {
          ...state.vault,
          error: error instanceof Error ? error.message : 'Failed to export vault bundle',
        },
      }))
      throw error
    }
  },
  importVaultBundle: async (password, bundle, vaultPassword = '') => {
    set((state) => ({ vault: { ...state.vault, loading: true, error: null } }))
    try {
      const result = await importVaultBundle(password, bundle, vaultPassword)
      set((state) => (
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
      set((state) => ({
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
    set((state) => ({
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
    debugLog('desktop-store', 'hydrate:enter', {
      hydrating: current.hydrating,
      hydrated: current.hydrated,
      vaultBootstrapped: current.vault.bootstrapped,
      vaultEnabled: current.vault.enabled,
      vaultUnlocked: current.vault.unlocked,
      connectionState: current.connectionState,
    })
    if (current.hydrating) {
      return
    }
    const finish = createDebugTimer('desktop-store', 'hydrate')
    set({ hydrating: true, realtimeDesired: true })
    try {
      if (!get().vault.bootstrapped) {
        debugLog('desktop-store', 'hydrate:bootstrapping-vault')
        await get().bootstrapVault()
      }
      if (get().vault.enabled && !get().vault.unlocked) {
        debugLog('desktop-store', 'hydrate:stop-vault-locked')
        set({
          hydrated: true,
          hydrating: false,
        })
        finish({ ok: true, stopped: 'vault-locked' })
        return
      }
      set({
        hydrated: true,
        hydrating: false,
      })
      debugLog('desktop-store', 'hydrate:connect-dispatch')
      await get().connect()
      finish({ ok: true, connectionState: get().connectionState })
    } catch (error) {
      console.error('[desktop-store] hydrate failed', error)
      debugLog('desktop-store', 'hydrate:error', {
        message: error instanceof Error ? error.message : String(error),
      })
      set({ hydrating: false, hydrated: true })
      finish({ ok: false })
    }
  },
  connect: async () => {
    const current = get()
    debugLog('desktop-store', 'connect:enter', {
      connectionState: current.connectionState,
      hasSocket: Boolean(desktopRealtimeSocket),
      realtimeDesired: current.realtimeDesired,
      vaultEnabled: current.vault.enabled,
      vaultUnlocked: current.vault.unlocked,
    })
    if (!shouldMaintainDesktopRealtime(current)) {
      debugLog('desktop-store', 'connect:skip', { reason: 'should-not-maintain-realtime' })
      return
    }
    if (desktopRealtimeSocket || current.connectionState === 'connecting') {
      debugLog('desktop-store', 'connect:skip', {
        reason: desktopRealtimeSocket ? 'socket-present' : 'already-connecting',
      })
      return
    }
    const finish = createDebugTimer('desktop-store', 'connect')
    clearReconnectTimer(current)
    const generation = current.connectionGeneration + 1
    set({
      connectionGeneration: generation,
      connectionState: 'connecting',
      reconnectTimer: null,
    })
    try {
      const socket = await openDesktopWebSocket()
      debugLog('desktop-store', 'connect:websocket-created', { generation })
      if (get().connectionGeneration !== generation || !shouldMaintainDesktopRealtime(get())) {
        socket.close()
        finish({ ok: false, stopped: 'stale-after-socket' })
        return
      }
      socket.addEventListener('open', () => {
        const state = get()
        debugLog('desktop-store', 'socket:open', {
          generation,
          connectionState: state.connectionState,
        })
        if (state.connectionGeneration !== generation || !shouldMaintainDesktopRealtime(state)) {
          socket.close()
          return
        }
        if (desktopRealtimeSocket && desktopRealtimeSocket !== socket) {
          socket.close()
          return
        }
        clearReconnectTimer(state)
        set({ connectionState: 'open', reconnectAttempt: 0, reconnectTimer: null })
        startHeartbeat(socket, generation)
        socket.send(JSON.stringify({ type: 'subscribe', channel: 'session:*', last_seen_seq: get().lastGlobalSeq }))
        socket.send(JSON.stringify({ type: 'subscribe', channel: 'ui:*', last_seen_seq: get().lastGlobalSeq }))
        socket.send(JSON.stringify({ type: 'subscribe', channel: 'user:*', last_seen_seq: get().lastGlobalSeq }))
        socket.send(JSON.stringify({ type: 'subscribe', channel: 'workspace:*', last_seen_seq: get().lastGlobalSeq }))
        socket.send(JSON.stringify({ type: 'subscribe', channel: 'system:worktrees', last_seen_seq: get().lastGlobalSeq }))
        socket.send(JSON.stringify({ type: 'subscribe', channel: 'workspace_todo:*', last_seen_seq: get().lastGlobalSeq }))
        socket.send(JSON.stringify({ type: 'subscribe', channel: 'swarm:*', last_seen_seq: get().lastGlobalSeq }))
        deferDesktopCacheMutation('workspace overview refresh on realtime connect', () => {
          void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
        })
        finish({ ok: true, phase: 'socket-open', generation })
      })
      socket.addEventListener('message', (event) => {
        const state = get()
        if (state.connectionGeneration !== generation || desktopRealtimeSocket !== socket) {
          return
        }
        armLivenessTimer(generation)
        try {
          const message = JSON.parse(String(event.data)) as SocketMessage
          if (message.type === 'pong' || message.type === 'connected' || message.type === 'subscribed' || message.type === 'resume-complete') {
            return
          }
          if (message.type !== 'event' || !message.event) {
            return
          }
          set((state) => applyEnvelope(state, message.event ?? {}))
          const payload = message.event.payload && typeof message.event.payload === 'object' ? message.event.payload as Record<string, unknown> : null
          const sessionId = typeof payload?.session_id === 'string' ? payload.session_id : ''
          const eventType = typeof message.event.event_type === 'string' ? message.event.event_type : ''
          if (sessionId && eventType === 'permission.summary.updated') {
            void get().refreshSessionPermissions(sessionId)
          }
        } catch (error) {
          console.error('[desktop-store] socket parse failed', error)
        }
      })
      socket.addEventListener('close', () => {
        const state = get()
        debugLog('desktop-store', 'socket:close', {
          generation,
          connectionState: state.connectionState,
        })
        if (state.connectionGeneration !== generation) {
          return
        }
        if (desktopRealtimeSocket && desktopRealtimeSocket !== socket) {
          return
        }
        scheduleReconnect('socket close')
      })
      socket.addEventListener('error', () => {
        const state = get()
        debugLog('desktop-store', 'socket:error', {
          generation,
          connectionState: state.connectionState,
        })
        if (state.connectionGeneration !== generation) {
          return
        }
        if (desktopRealtimeSocket && desktopRealtimeSocket !== socket) {
          return
        }
        set({ connectionState: 'error' })
      })
      desktopRealtimeSocket = socket
      set({ heartbeatTimer: null, livenessTimer: null })
    } catch (error) {
      console.error('[desktop-store] connect failed', error)
      debugLog('desktop-store', 'connect:error', {
        generation,
        message: error instanceof Error ? error.message : String(error),
      })
      const state = get()
      if (state.connectionGeneration !== generation) {
        finish({ ok: false, stopped: 'stale-in-catch' })
        return
      }
      desktopRealtimeSocket = null
      set({ connectionState: 'error' })
      scheduleReconnect('connect failure')
      finish({ ok: false })
    }
  },
  disconnect: () => {
    const current = get()
    debugLog('desktop-store', 'disconnect:enter', {
      connectionState: current.connectionState,
      hasSocket: Boolean(desktopRealtimeSocket),
      realtimeDesired: current.realtimeDesired,
      runStreamCount: requireRunStreamController().activeSessionCount(),
    })
    clearReconnectTimer(current)
    clearHeartbeatTimer(current)
    clearLivenessTimer(current)
    const nextGeneration = current.connectionGeneration + 1
    const socket = desktopRealtimeSocket
    desktopRealtimeSocket = null
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
  },
  closeRunStream: (sessionId) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    requireRunStreamController().close(normalizedSessionId)
  },
  ensureRunStream: async (sessionId: string, runId?: string | null) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    await requireRunStreamController().ensure(normalizedSessionId, runId)
  },
  stopRun: async (sessionId) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) {
      return
    }
    const runId = get().sessions[normalizedSessionId]?.live.runId?.trim() ?? ''
    if (!runId) {
      return
    }
    set((state) => applyRunStreamFrame(state, normalizedSessionId, { type: 'run.stop.accepted', run_id: runId }, Date.now()))
    await requireRunStreamController().stop(normalizedSessionId, runId)
  },
  submitPrompt: async ({ sessionId, workspacePath, prompt, agentName, compact = false, targetKind = '', targetName = '' }: {
    sessionId: string | null
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
    const sourceDraftKey = draftKeyForSession(targetSessionId || null, workspacePath)

    if (!targetSessionId) {
      throw new Error('submitPrompt requires an attached session')
    }

    get().closeRunStream(targetSessionId)

    set((state) => {
      cancelDraftFlush(targetSessionId)
      const sessions = { ...state.sessions }
      const session = { ...ensureSession(state, targetSessionId), live: { ...ensureSession(state, targetSessionId).live } }
      session.live.status = session.lifecycle?.active ? session.live.status : 'starting'
      session.live.startedAt = session.lifecycle?.active ? session.live.startedAt : null
      session.live.agentName = targetName.trim() || agentName.trim() || session.live.agentName
      session.live.runId = null
      session.live.seq = 0
      session.live.awaitingAck = true
      session.live.summary = 'Starting…'
      session.live.error = null
      session.live.assistantDraft = ''
      resetLiveToolState(session.live)
      resetLiveReasoningState(session.live)
      session.live.reasoningSegment = 0
      sessions[targetSessionId] = mergeSessionRecords(state.sessions[targetSessionId] ?? null, session)
      syncBlockedSessionToWorkspaceOverview(queryClient, sessions[targetSessionId])
      const nextSessionDrafts = { ...state.sessionDrafts }
      nextSessionDrafts[targetSessionId] = ''
      if (sourceDraftKey !== targetSessionId) {
        delete nextSessionDrafts[sourceDraftKey]
      }
      return {
        sessions,
        sessionDrafts: nextSessionDrafts,
      }
    })

    try {
      const accepted = await requireRunStreamController().start({
        sessionId: targetSessionId,
        prompt: trimmedPrompt,
        agentName,
        compact,
        background: false,
        targetKind,
        targetName,
      })
      const acceptedRunId = accepted.run_id?.trim() ?? ''
      const acceptedStatus = accepted.status?.trim().toLowerCase() ?? ''
      set((state) => {
        const existing = state.sessions[targetSessionId]
        if (!existing) {
          return state
        }
        const sessions = { ...state.sessions }
        const session = {
          ...existing,
          live: {
            ...existing.live,
            runId: acceptedRunId || existing.live.runId,
            awaitingAck: false,
            status: acceptedStatus === 'blocked'
              ? 'blocked'
              : acceptedStatus === 'running'
                ? 'running'
                : existing.live.status,
            lastEventType: 'run.accepted',
            lastEventAt: Date.now(),
            error: null,
            summary: acceptedRunId ? 'Connecting…' : existing.live.summary,
          },
        }
        sessions[targetSessionId] = mergeSessionRecords(existing, session)
        syncBlockedSessionToWorkspaceOverview(queryClient, sessions[targetSessionId])
        return { sessions }
      })
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to start run'
      set((state) => applyRunStreamResumeFailure(state, targetSessionId, message, Date.now()))
      throw error
    }
  },
}))
