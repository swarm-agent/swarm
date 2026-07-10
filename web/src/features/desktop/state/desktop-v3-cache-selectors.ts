import type { DesktopSessionPlanCheckpoint, DesktopSessionPlanDocument, DesktopSessionPlanRecord } from '../chat/types/chat'
import type { DesktopNotificationCenterRecord, DesktopNotificationSummary, DesktopPermissionRecord } from '../types/realtime'
import { safeString } from '../permissions/services/desktop-permission-normalization'
import type { DesktopPermissionSummary, DesktopV3CacheState, LiveRunOverlay, MessageListCache, MessageSnapshot, PendingUserMessage, SessionCacheRecord, V3SessionProjection, V3SessionRunIntent, V3SessionTombstone } from './desktop-v3-cache-types'

export type DesktopV3SidebarRowType = 'plan_session' | 'single_chat'
export type DesktopV3SidebarPlanStatusLabel = 'RUNNING' | 'REVIEW' | 'BLOCKED' | 'QUEUED'
export type DesktopV3SidebarGroupId = 'needs_review' | 'in_progress' | 'active_chats' | 'archived'

export interface DesktopV3SidebarCheckpointProgress {
  activeCheckpointId: string
  activeIndex: number
  completedCount: number
  totalCount: number
  label: string
}

export interface DesktopV3SidebarRow {
  sessionId: string
  record: SessionCacheRecord
  projection?: V3SessionProjection
  tombstone?: V3SessionTombstone
  runIntents: Record<string, V3SessionRunIntent>
  currentRunIntent?: V3SessionRunIntent
  pendingPermissions: DesktopPermissionRecord[]
  permissionSummary?: DesktopPermissionSummary
  pendingPermissionCount: number
  hasActivePlan: boolean
  activePlan?: DesktopSessionPlanRecord
  planExecution?: DesktopV3SidebarPlanExecution
  rowType: DesktopV3SidebarRowType
  sidebarGroup: DesktopV3SidebarGroupId
  branchLabel: string
}

export interface DesktopV3SidebarPlanExecution {
  planId: string
  title: string
  status: string
  statusLabel: DesktopV3SidebarPlanStatusLabel
  checkpointProgress: DesktopV3SidebarCheckpointProgress
  activeCheckpointId: string
  activeCheckpointTitle: string
  activeCheckpointStatus: string
  activeSubtaskId: string
  activeSubtaskTitle: string
  activeSubtaskStatus: string
  currentRunId: string
  currentSessionId: string
  reviewRequired: boolean
  blocked: boolean
  failed: boolean
  completed: boolean
}

export interface RenderedSessionMessages {
  committed: MessageSnapshot[]
  pendingUser: PendingUserMessage[]
  liveRuns: LiveRunOverlay[]
  runIntents: V3SessionRunIntent[]
  currentRunIntent?: V3SessionRunIntent
  latestRunIntent?: V3SessionRunIntent
}

export interface DesktopPlanExecutionView {
  plan: DesktopSessionPlanRecord
  activeCheckpoint?: DesktopSessionPlanCheckpoint
  activeCheckpointId: string
  status: string
  policyMode: string
  policyShape: string
  currentRunId: string
  currentSessionId: string
  freshContext: boolean
  reviewRequired: boolean
  blocked: boolean
  failed: boolean
  completed: boolean
  attemptCount: number
}

export interface DesktopV3HydratedTranscriptDiagnostics {
  hydratedSessionCount: number
  hydratedMessageCount: number
  retainedBackgroundHydratedSessionCount: number
  inFlightHydrateSessionCount: number
  evictedTranscriptCount: number
}

export function selectOrderedNotifications(state: DesktopV3CacheState): DesktopNotificationCenterRecord[] {
  return Object.values(state.notificationsById).map(cloneNotification).sort((left, right) => {
    if (left.updatedAt !== right.updatedAt) return right.updatedAt - left.updatedAt
    if (left.createdAt !== right.createdAt) return right.createdAt - left.createdAt
    return left.id.localeCompare(right.id)
  })
}

export function selectNotificationSummary(state: DesktopV3CacheState): DesktopNotificationSummary {
  return { ...state.notificationSummary }
}

export function selectUnreadNotificationCount(state: DesktopV3CacheState): number {
  return Math.max(0, state.notificationSummary.unreadCount)
}

export function selectActiveNotificationCount(state: DesktopV3CacheState): number {
  return Math.max(0, state.notificationSummary.activeCount)
}

export function selectScopeEndpointCursor(state: DesktopV3CacheState, scopeId: string): string | undefined {
  return state.syncScopesById[scopeId]?.endpointCursor
}

export function selectSessionOrder(state: DesktopV3CacheState, scopeId: string): string[] {
  return state.sessionOrderByScope[scopeId] ?? []
}

export function selectDesktopSidebarScopeId(state: DesktopV3CacheState): string | undefined {
  return state.desktopSidebarBootstrap.scopeId
}

export function selectDesktopSidebarRows(state: DesktopV3CacheState, scopeId = state.desktopSidebarBootstrap.scopeId): DesktopV3SidebarRow[] {
  const resolvedScopeId = scopeId ?? Object.keys(state.sessionOrderByScope)[0]
  if (!resolvedScopeId) return []
  const rows: DesktopV3SidebarRow[] = []
  for (const sessionId of selectSessionOrder(state, resolvedScopeId)) {
    if (state.tombstonesBySession[sessionId]) continue
    const record = state.sessionsById[sessionId]
    if (!record) continue
    const planState = buildDesktopSidebarPlanState(state, sessionId)
    rows.push({
      sessionId,
      record: cloneSessionCacheRecord(record),
      projection: state.projectionsBySession[sessionId] ? { ...state.projectionsBySession[sessionId] } : undefined,
      runIntents: cloneRunIntentRecord(state.runIntentsBySession[sessionId]),
      currentRunIntent: cloneRunIntent(state.currentRunIntentBySession[sessionId]),
      pendingPermissions: clonePendingPermissions(state.permissionsBySession[sessionId]),
      permissionSummary: clonePermissionSummary(state.permissionSummaryBySessionId[sessionId]),
      pendingPermissionCount: state.permissionSummaryBySessionId[sessionId]?.pendingApprovalCount ?? 0,
      ...planState,
      rowType: planState.planExecution ? 'plan_session' : 'single_chat',
      sidebarGroup: desktopSidebarGroupForRow({
        hasActivePlan: planState.hasActivePlan,
        planExecution: planState.planExecution,
        hasActiveRun: hasActiveRunIntent(state.currentRunIntentBySession[sessionId]),
        pendingPermissionCount: state.permissionSummaryBySessionId[sessionId]?.pendingApprovalCount ?? 0,
        tombstoned: Boolean(state.tombstonesBySession[sessionId]),
      }),
      branchLabel: desktopSidebarBranchLabel(record),
    })
  }
  rows.push(...selectArchivedDesktopSidebarRows(state))
  return rows
}

export function selectDesktopSidebarGroupedRows(state: DesktopV3CacheState, scopeId = state.desktopSidebarBootstrap.scopeId): Record<DesktopV3SidebarGroupId, DesktopV3SidebarRow[]> {
  const grouped: Record<DesktopV3SidebarGroupId, DesktopV3SidebarRow[]> = {
    needs_review: [],
    in_progress: [],
    active_chats: [],
    archived: [],
  }
  for (const row of selectDesktopSidebarRows(state, scopeId)) {
    grouped[row.sidebarGroup].push(row)
  }
  return grouped
}

function selectArchivedDesktopSidebarRows(state: DesktopV3CacheState): DesktopV3SidebarRow[] {
  const rows: DesktopV3SidebarRow[] = []
  for (const tombstone of Object.values(state.tombstonesBySession)) {
    if (!isArchivedTombstone(tombstone) || !tombstone.session) continue
    const sessionId = tombstone.session_id || tombstone.session.id
    if (!sessionId) continue
    const record: SessionCacheRecord = { kind: 'full', session: tombstone.session, needsHydrate: false }
    rows.push({
      sessionId,
      record: cloneSessionCacheRecord(record),
      projection: state.projectionsBySession[sessionId] ? { ...state.projectionsBySession[sessionId] } : undefined,
      tombstone: { ...tombstone },
      runIntents: {},
      currentRunIntent: undefined,
      pendingPermissions: [],
      permissionSummary: undefined,
      pendingPermissionCount: 0,
      hasActivePlan: false,
      rowType: 'single_chat',
      sidebarGroup: 'archived',
      branchLabel: desktopSidebarBranchLabel(record),
    })
  }
  return rows.sort((left, right) => {
    const leftUpdated = left.tombstone?.updated_at ?? (left.record.kind === 'full' ? left.record.session.updated_at : 0)
    const rightUpdated = right.tombstone?.updated_at ?? (right.record.kind === 'full' ? right.record.session.updated_at : 0)
    if (leftUpdated !== rightUpdated) return rightUpdated - leftUpdated
    return left.sessionId.localeCompare(right.sessionId)
  })
}

function isArchivedTombstone(tombstone: V3SessionTombstone | undefined): boolean {
  return Boolean(tombstone && tombstone.kind === 'archived' && tombstone.archived === true && tombstone.deleted !== true)
}

function buildDesktopSidebarPlanState(state: DesktopV3CacheState, sessionId: string): Pick<DesktopV3SidebarRow, 'hasActivePlan' | 'activePlan' | 'planExecution'> {
  if (state.hasActivePlanBySession[sessionId] !== true) {
    return { hasActivePlan: false }
  }
  const candidate = state.plansBySession[sessionId] as DesktopSessionPlanRecord | null | undefined
  if (!candidate?.document) {
    return { hasActivePlan: true }
  }
  return {
    hasActivePlan: true,
    activePlan: candidate,
    planExecution: buildDesktopSidebarPlanExecution(candidate),
  }
}

function buildDesktopSidebarPlanExecution(plan: DesktopSessionPlanRecord): DesktopV3SidebarPlanExecution | undefined {
  const document = plan.document
  if (!document) return undefined
  const orderedCheckpoints = orderedPlanCheckpoints(document)
  const activeCheckpointId = document.activeCheckpointId || document.executionState?.lastCheckpointId || orderedCheckpoints.find((checkpoint) => checkpoint.status.toLowerCase() === 'in_progress')?.id || ''
  const activeCheckpoint = activeCheckpointId
    ? orderedCheckpoints.find((checkpoint) => checkpoint.id === activeCheckpointId)
    : undefined
  const status = document.executionState?.status || document.status || plan.status || ''
  const normalizedStatus = status.toLowerCase()
  const checkpointStatus = (activeCheckpoint?.status || document.executionState?.lastOutcome || '').toLowerCase()
  const reviewRequired = normalizedStatus === 'waiting_review' || checkpointStatus === 'needs_review' || activeCheckpoint?.review?.status === 'pending'
  const blocked = normalizedStatus === 'blocked' || checkpointStatus === 'blocked'
  const failed = normalizedStatus === 'failed' || checkpointStatus === 'failed'
  const completed = normalizedStatus === 'completed' || allCheckpointsCompleted(document)
  return {
    planId: plan.id,
    title: document.title || plan.title,
    status,
    statusLabel: desktopPlanStatusLabel({ normalizedStatus, checkpointStatus, reviewRequired, blocked, failed, completed }),
    checkpointProgress: desktopSidebarCheckpointProgress(document, activeCheckpointId),
    activeCheckpointId,
    activeCheckpointTitle: activeCheckpoint?.title || '',
    activeCheckpointStatus: activeCheckpoint?.status || document.executionState?.lastOutcome || '',
    activeSubtaskId: activeCheckpoint?.activeSubtaskId || '',
    activeSubtaskTitle: activeCheckpoint?.subtasks?.find((subtask) => subtask.id === activeCheckpoint.activeSubtaskId)?.title || '',
    activeSubtaskStatus: activeCheckpoint?.subtasks?.find((subtask) => subtask.id === activeCheckpoint.activeSubtaskId)?.status || '',
    currentRunId: document.executionState?.currentRunId || activeCheckpoint?.runId || '',
    currentSessionId: document.executionState?.currentSessionId || activeCheckpoint?.sessionId || '',
    reviewRequired,
    blocked,
    failed,
    completed,
  }
}

function orderedPlanCheckpoints(document: DesktopSessionPlanDocument): DesktopSessionPlanCheckpoint[] {
  return [...document.checkpoints].sort((left, right) => {
    const leftOrder = Number.isFinite(left.order) && left.order > 0 ? left.order : Number.MAX_SAFE_INTEGER
    const rightOrder = Number.isFinite(right.order) && right.order > 0 ? right.order : Number.MAX_SAFE_INTEGER
    if (leftOrder !== rightOrder) return leftOrder - rightOrder
    return left.id.localeCompare(right.id)
  })
}

function desktopSidebarCheckpointProgress(document: DesktopSessionPlanDocument, activeCheckpointId: string): DesktopV3SidebarCheckpointProgress {
  const checkpoints = orderedPlanCheckpoints(document)
  const totalCount = checkpoints.length
  const completedCount = checkpoints.filter((checkpoint) => checkpoint.status.toLowerCase() === 'completed').length
  const rawActiveIndex = activeCheckpointId ? checkpoints.findIndex((checkpoint) => checkpoint.id === activeCheckpointId) : -1
  const activeIndex = rawActiveIndex >= 0 ? rawActiveIndex + 1 : 0
  return {
    activeCheckpointId,
    activeIndex,
    completedCount,
    totalCount,
    label: totalCount > 0 ? `${activeIndex || completedCount}/${totalCount}` : '',
  }
}

function desktopPlanStatusLabel(input: { normalizedStatus: string; checkpointStatus: string; reviewRequired: boolean; blocked: boolean; failed: boolean; completed: boolean }): DesktopV3SidebarPlanStatusLabel {
  if (input.reviewRequired) return 'REVIEW'
  if (input.blocked || input.failed) return 'BLOCKED'
  if (input.completed) return 'QUEUED'
  if (input.normalizedStatus === 'queued' || input.normalizedStatus === 'pending' || input.checkpointStatus === 'pending') return 'QUEUED'
  return 'RUNNING'
}

function desktopSidebarGroupForRow(input: { hasActivePlan: boolean; planExecution?: DesktopV3SidebarPlanExecution; hasActiveRun: boolean; pendingPermissionCount: number; tombstoned: boolean }): DesktopV3SidebarGroupId {
  if (input.tombstoned) return 'archived'
  if (input.planExecution?.reviewRequired) return 'needs_review'
  if (input.planExecution && !input.planExecution.completed) return 'in_progress'
  if (input.pendingPermissionCount > 0 || input.hasActiveRun || input.hasActivePlan) return 'active_chats'
  return 'active_chats'
}

function hasActiveRunIntent(runIntent: V3SessionRunIntent | undefined): boolean {
  return runIntent ? ['pending_executor', 'running', 'dispatch_blocked'].includes(runIntent.status) : false
}

function desktopSidebarBranchLabel(record: SessionCacheRecord): string {
  if (record.kind !== 'full') return ''
  return record.session.worktree_branch?.trim() || metadataString(record.session.metadata, 'git_branch') || metadataString(record.session.metadata, 'branch') || ''
}

function metadataString(metadata: Record<string, unknown> | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function allCheckpointsCompleted(document: DesktopSessionPlanDocument): boolean {
  return document.checkpoints.length > 0 && document.checkpoints.every((checkpoint) => checkpoint.status.toLowerCase() === 'completed')
}

export function selectCommittedMessages(state: DesktopV3CacheState, sessionId: string): MessageSnapshot[] {
  return state.messagesBySession[sessionId]?.items ?? []
}

export function selectPendingUserMessages(state: DesktopV3CacheState, sessionId: string): PendingUserMessage[] {
  return Object.values(state.pendingUserByClientRequestId)
    .filter((pending) => pending.sessionId === sessionId)
    .map((pending) => ({ ...pending, metadata: pending.metadata ? { ...pending.metadata } : undefined }))
    .sort((left, right) => left.createdAt - right.createdAt || left.messageId.localeCompare(right.messageId))
}

export function selectLiveRuns(state: DesktopV3CacheState, sessionId: string): LiveRunOverlay[] {
  return Object.values(state.liveRunsBySession[sessionId] ?? {}).map(cloneLiveRun).sort((a, b) => {
    const aSeq = a.lastEventSeqSeen ?? 0
    const bSeq = b.lastEventSeqSeen ?? 0

    if (aSeq !== bSeq) {
      return aSeq - bSeq
    }

    const aUpdated = a.assistantDraft?.updatedAt ?? 0
    const bUpdated = b.assistantDraft?.updatedAt ?? 0

    if (aUpdated !== bUpdated) {
      return aUpdated - bUpdated
    }

    return a.runId.localeCompare(b.runId)
  })
}

export function selectSessionRunIntents(state: DesktopV3CacheState, sessionId: string): V3SessionRunIntent[] {
  return Object.values(state.runIntentsBySession[sessionId] ?? {}).sort((left, right) => {
    const leftSeq = typeof left.event_seq === 'number' ? left.event_seq : 0
    const rightSeq = typeof right.event_seq === 'number' ? right.event_seq : 0
    if (leftSeq !== rightSeq) return leftSeq - rightSeq

    const leftUpdated = typeof left.updated_at === 'number' ? left.updated_at : 0
    const rightUpdated = typeof right.updated_at === 'number' ? right.updated_at : 0
    if (leftUpdated !== rightUpdated) return leftUpdated - rightUpdated

    return left.run_id.localeCompare(right.run_id)
  })
}

export function selectDesktopV3HydratedTranscriptDiagnostics(state: DesktopV3CacheState): DesktopV3HydratedTranscriptDiagnostics {
  const selectedSessionId = state.selectedSessionId?.trim()
  let hydratedSessionCount = 0
  let hydratedMessageCount = 0
  let retainedBackgroundHydratedSessionCount = 0

  for (const [sessionId, list] of Object.entries(state.messagesBySession)) {
    if (!isHydratedTranscript(list)) continue
    hydratedSessionCount += 1
    hydratedMessageCount += list.items.length
    if (sessionId !== selectedSessionId && (state.hydrateInFlightBySession ?? {})[sessionId] === undefined) {
      retainedBackgroundHydratedSessionCount += 1
    }
  }

  return {
    hydratedSessionCount,
    hydratedMessageCount,
    retainedBackgroundHydratedSessionCount,
    inFlightHydrateSessionCount: Object.keys(state.hydrateInFlightBySession ?? {}).length,
    evictedTranscriptCount: Object.keys(state.evictedTranscriptsBySession ?? {}).length,
  }
}

export function selectRenderedSessionMessages(state: DesktopV3CacheState, sessionId: string): RenderedSessionMessages {
  const runIntents = selectSessionRunIntents(state, sessionId)
  return {
    committed: selectCommittedMessages(state, sessionId),
    pendingUser: selectPendingUserMessages(state, sessionId),
    liveRuns: selectLiveRuns(state, sessionId),
    runIntents,
    currentRunIntent: state.currentRunIntentBySession[sessionId],
    latestRunIntent: runIntents[runIntents.length - 1],
  }
}

export function selectDesktopPlanExecutionView(state: DesktopV3CacheState, sessionId: string): DesktopPlanExecutionView | null {
  if (state.hasActivePlanBySession[sessionId] !== true) return null
  const candidate = state.plansBySession[sessionId] as DesktopSessionPlanRecord | null | undefined
  const plan = candidate?.document ? candidate : null
  const document = plan?.document
  if (!plan || !document) return null

  const activeCheckpointId = document.activeCheckpointId || document.executionState?.lastCheckpointId || ''
  const activeCheckpoint = activeCheckpointId
    ? document.checkpoints.find((checkpoint) => checkpoint.id === activeCheckpointId)
    : undefined
  const status = document.executionState?.status || document.status || plan.status || ''
  const normalizedStatus = status.toLowerCase()
  const checkpointStatus = (activeCheckpoint?.status || document.executionState?.lastOutcome || '').toLowerCase()

  return {
    plan,
    activeCheckpoint,
    activeCheckpointId,
    status,
    policyMode: document.executionPolicy?.mode || '',
    policyShape: document.executionPolicy?.shape || '',
    currentRunId: document.executionState?.currentRunId || activeCheckpoint?.runId || '',
    currentSessionId: document.executionState?.currentSessionId || activeCheckpoint?.sessionId || '',
    freshContext: Boolean(document.executionState?.activeAttemptId || activeCheckpoint?.attemptId || document.executionState?.currentRunId || activeCheckpoint?.runId),
    reviewRequired: normalizedStatus === 'waiting_review' || checkpointStatus === 'needs_review' || activeCheckpoint?.review?.status === 'pending',
    blocked: normalizedStatus === 'blocked' || checkpointStatus === 'blocked',
    failed: normalizedStatus === 'failed' || checkpointStatus === 'failed',
    completed: normalizedStatus === 'completed' || document.checkpoints.length > 0 && document.checkpoints.every((checkpoint) => checkpoint.status.toLowerCase() === 'completed'),
    attemptCount: document.checkpoints.reduce((total, checkpoint) => total + checkpoint.attempts.length, 0),
  }
}

export function selectSessionNeedsHydrate(state: DesktopV3CacheState, sessionId: string): boolean {
  return state.sessionsById[sessionId]?.needsHydrate ?? true
}

export function isDesktopV3SessionViewReady(state: DesktopV3CacheState, sessionId: string): boolean {
  const normalized = sessionId.trim()
  if (!normalized) return false
  return Boolean(state.sessionViewsById[normalized])
}

export function isDesktopV3SessionTailReady(
  state: DesktopV3CacheState,
  sessionId: string,
): boolean {
  const normalized = sessionId.trim()
  if (!normalized) return false
  const tombstone = state.tombstonesBySession[normalized]
  if (tombstone && !isArchivedTombstone(tombstone)) return false

  const record = state.sessionsById[normalized]
  const messages = state.messagesBySession[normalized]
  const session = record?.kind === 'full' ? record.session : undefined

  if (!session || !messages) return false

  const hasAuthoritativeTail = messages.knownFull === true
    || Boolean(messages.knownTail)
    || Number.isSafeInteger(messages.tailHydratedAt)

  return hasAuthoritativeTail
    && Number.isSafeInteger(messages.sourceMessageCount)
    && Number.isSafeInteger(messages.sourceLastMessageAt)
    && (messages.sourceMessageCount ?? -1) >= session.message_count
    && (messages.sourceLastMessageAt ?? -1) >= session.last_message_at
}

function isHydratedTranscript(list: MessageListCache | undefined): boolean {
  return Boolean(list?.knownFull)
    || Boolean(list?.knownTail)
    || Number.isSafeInteger(list?.tailHydratedAt)
}

function cloneSessionCacheRecord(record: SessionCacheRecord): SessionCacheRecord {
  if (record.kind === 'stub') return { ...record }
  return {
    kind: 'full',
    session: {
      ...record.session,
      metadata: record.session.metadata ? { ...record.session.metadata } : undefined,
      temporary_workspace_roots: record.session.temporary_workspace_roots ? [...record.session.temporary_workspace_roots] : undefined,
    },
    needsHydrate: record.needsHydrate,
  }
}

function cloneRunIntent(runIntent: V3SessionRunIntent | undefined): V3SessionRunIntent | undefined {
  return runIntent ? { ...runIntent } : undefined
}

function cloneRunIntentRecord(runIntents: Record<string, V3SessionRunIntent> | undefined): Record<string, V3SessionRunIntent> {
  if (!runIntents) return {}
  const output: Record<string, V3SessionRunIntent> = {}
  for (const [runId, runIntent] of Object.entries(runIntents)) {
    output[runId] = { ...runIntent }
  }
  return output
}

function clonePendingPermissions(permissions: DesktopPermissionRecord[] | undefined): DesktopPermissionRecord[] {
  return (permissions ?? [])
    .filter((permission) => safeString(permission.status).toLowerCase() === 'pending')
    .map((permission) => ({ ...permission, savedRule: permission.savedRule ? { ...permission.savedRule } : undefined }))
}

function clonePermissionSummary(summary: DesktopPermissionSummary | undefined): DesktopPermissionSummary | undefined {
  return summary ? { ...summary } : undefined
}

function cloneNotification(notification: DesktopNotificationCenterRecord): DesktopNotificationCenterRecord {
  return { ...notification }
}

function cloneLiveRun(run: LiveRunOverlay): LiveRunOverlay {
  return {
    ...run,
    assistantDraft: run.assistantDraft ? { ...run.assistantDraft } : undefined,
    assistantSegments: run.assistantSegments?.map((segment) => ({ ...segment })),
    toolCallsByCallId: Object.fromEntries(
      Object.entries(run.toolCallsByCallId).map(([callId, tool]) => [callId, {
        ...tool,
        taskStream: tool.taskStream ? {
          ...tool.taskStream,
          launchesByKey: Object.fromEntries(
            Object.entries(tool.taskStream.launchesByKey).map(([launchKey, launch]) => [launchKey, { ...launch }]),
          ),
          launchOrder: [...tool.taskStream.launchOrder],
        } : undefined,
      }]),
    ),
    reasoning: run.reasoning ? { ...run.reasoning } : undefined,
    reasoningByKey: run.reasoningByKey ? Object.fromEntries(
      Object.entries(run.reasoningByKey).map(([key, reasoning]) => [key, { ...reasoning }]),
    ) : undefined,
  }
}
