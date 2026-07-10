import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { JSX, ReactNode, ChangeEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate, Link } from '@tanstack/react-router'
import { Archive, Bell, Bot, Check, CheckCircle2, ChevronDown, ChevronLeft, ChevronRight, Download, Folder, GitBranch, Keyboard, LayoutGrid, LoaderCircle, Menu, MoreVertical, Pencil, Pin, Plus, RefreshCcw, Search, Settings, X, XCircle } from 'lucide-react'
import { requestJson } from '../../../app/api'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { cn } from '../../../lib/cn'
import { useWorkspaceLauncher } from '../../workspaces/launcher/state/use-workspace-launcher'
import { applyDesktopRouteTheme } from './desktop-theme-controller'
import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'
import { agentStateQueryOptions, draftModelQueryOptions, uiSettingsQueryKey, workspaceOverviewQueryOptions } from '../../queries/query-options'
import type { DesktopNotificationCenterRecord, DesktopSessionRecord } from '../types/realtime'
import type { DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../chat/types/chat'
import type { SettingsTabID } from '../settings/types/settings-tabs'
import { DesktopQuickSettingsModal, type QuickSettingsTabID } from '../settings/components/desktop-quick-settings-modal'
import { buildWorkspaceRouteSlugMap, resolveWorkspaceBySlug, workspaceRouteSlugBase } from '../../workspaces/launcher/services/workspace-route'
import type { WorkspaceEntry } from '../../workspaces/launcher/types/workspace'
import { WorkspaceTodoModal } from '../../workspaces/todos/components/workspace-todo-modal'
import type { WorkspaceTodoItem, WorkspaceTodoOwnerKind, WorkspaceTodoSummary } from '../../workspaces/todos/types'
import {
  createEmptyWorkspaceTodoSummary,
  createWorkspaceTodo,
  deleteAllWorkspaceTodos,
  deleteDoneWorkspaceTodos,
  deleteWorkspaceTodo,
  reorderWorkspaceTodos,
  setWorkspaceTodoInProgress,
  updateWorkspaceTodo,
} from '../../workspaces/todos/types'
import { getSwarmSettings } from '../settings/swarm/queries/get-swarm-settings'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveSwarmSettings } from '../settings/swarm/mutations/save-swarm-settings'
import { normalizeSidebarHideInactiveHours, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { saveSidebarHideInactiveHours } from '../settings/swarm/mutations/save-sidebar-hide-inactive-hours'
import { fetchSwarmTargets, type SwarmTarget } from '../swarm/api/swarm-targets'
import { DesktopV3ExistingConversationPane } from '../chat/components/desktop-v3-existing-conversation-pane'
import { DesktopV3NewSessionPane } from '../chat/components/desktop-v3-new-session-pane'
import { createDesktopV3CreateOnlySessionOperation, startDesktopV3CreateOnlySession } from '../session-v3/new-session-flow'
import { DesktopPlanModal, type DesktopPlanRecoveryInput } from '../chat/components/desktop-plan-modal'
import { buildDesktopChatRouteOptions, getDesktopSessionCreateTarget, resolveDesktopChatRouteFromSession, type DesktopChatRoute } from '../chat/services/chat-routing'
import type { DesktopSlashCommand } from '../chat/services/slash-commands'
import { fetchGitStatus, gitStatusQueryKey, startGitRealtime } from '../git/api'
import type { GitFileStatus, GitSnapshot } from '../git/types'
import { fetchDesktopUpdateJob, fetchDesktopUpdateStatus, startDesktopUpdate, type DesktopUpdateJob } from '../update/api'
import {
  sessionBackgroundInfo,
  sessionChildDescriptor,
  sessionParentSessionID,
  type SidebarSessionNodeKind,
} from './sidebar-session-lineage'
import { dispatchDesktopV3Cache, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { isDesktopV3SessionTailReady, selectDesktopSidebarRows, selectNotificationSummary, selectOrderedNotifications, selectRenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { selectSession } from '../state/desktop-v3-cache-wire'
import { selectAndHydrateDesktopV3Session } from '../state/desktop-v3-session-hydrator'
import type { DesktopV3SidebarRow, RenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { fetchAndApplyDesktopV3PlanSnapshot } from '../state/desktop-v3-session-api'
import { archiveDesktopV3Sessions, jumpDesktopPlanToRevisionCheckpoint, restartDesktopPlanFromRevision, restoreDesktopPlanRevision, startDesktopPlanAutomatic, startDesktopPlanCheckpointed } from '../session-v3/plan-execution-api'
import { DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY, updateAndApplySessionV3DesktopSidebarPinned, updateSessionV3Title } from '../session-v3/api'
import type { V3SessionRunIntent } from '../state/desktop-v3-cache-types'
import { clearNotifications, updateNotification } from '../notifications/api'
import { DesktopNotificationsModal } from '../notifications/components/desktop-notifications-modal'
import { DESKTOP_V3_RUN_TIMER_TOOLTIP } from '../chat/components/desktop-v3-run-status'
import { SearchChatsModal } from '../session-search/search-chats-modal'
import type { DesktopSessionSearchItem } from '../session-search/session-search-api'
import { DesktopQuickActionsModal, type DesktopQuickActionItem } from '../shortcuts/components/desktop-quick-actions-modal'

const DESKTOP_SIDEBAR_LAYOUT_STORAGE_KEY = 'swarm.web.desktop.sidebar.layout'
const DESKTOP_PENDING_UPDATE_TOAST_STORAGE_KEY = 'swarm.web.desktop.pending_update_toast'
const SIDEBAR_ACTIVITY_GRACE_MS = 15_000
const MOBILE_SIDEBAR_SWIPE_EDGE_PX = 28
const MOBILE_SIDEBAR_SWIPE_MIN_X_PX = 72
const MOBILE_SIDEBAR_SWIPE_MAX_Y_PX = 48
type MobileSidebarSwipeState = { startX: number; startY: number; tracking: boolean; completed: boolean; mode: 'open' | 'close' }
const UPDATE_STATUS_REFETCH_INTERVAL_MS = 5 * 60_000
const SWARM_TARGET_REFETCH_INTERVAL_MS = 10_000
const SIDEBAR_ACTION_RAIL_WIDTH_CLASS = 'w-[52px]'
const SIDEBAR_ACTION_ROW_CLASS = `grid min-w-0 grid-cols-[minmax(0,1fr)_52px] items-center gap-2.5`
const SIDEBAR_ACTION_RAIL_CLASS = `grid ${SIDEBAR_ACTION_RAIL_WIDTH_CLASS} shrink-0 grid-cols-[24px_24px] justify-end gap-1`
const SIDEBAR_ACTION_BOX_CLASS = 'grid h-6 min-h-6 w-6 min-w-6 shrink-0 place-items-center border-0 bg-transparent p-0 font-inherit'
const SIDEBAR_ACTION_BUTTON_CLASS = `${SIDEBAR_ACTION_BOX_CLASS} text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]`
const PWA_DEBUG_QUERY_PARAM = 'pwaDebug'
const UPDATE_PROGRESS_STEP_TITLES = [
  'Start update helper',
  'Check prerequisites',
  'Rebuild/apply Swarm',
  'Restart/reconnect backend',
  'Verify update',
] as const

function SidebarActionRail({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn(SIDEBAR_ACTION_RAIL_CLASS, className)}>{children}</div>
}

function PwaLayoutDebugOverlay() {
  const readSnapshot = useCallback(() => {
    if (typeof window === 'undefined') {
      return [] as Array<[string, string]>
    }
    const standalone = 'standalone' in window.navigator && Boolean((window.navigator as Navigator & { standalone?: boolean }).standalone)
    const displayStandalone = window.matchMedia?.('(display-mode: standalone)').matches ?? false
    const rootStyle = window.getComputedStyle(document.documentElement)
    return [
      ['standalone', String(standalone)],
      ['display-mode', String(displayStandalone)],
      ['innerHeight', String(window.innerHeight)],
      ['visualViewport', String(window.visualViewport?.height ?? 'n/a')],
      ['clientHeight', String(document.documentElement.clientHeight)],
      ['sat', rootStyle.getPropertyValue('--sat').trim() || 'n/a'],
      ['sab', rootStyle.getPropertyValue('--sab').trim() || 'n/a'],
    ]
  }, [])
  const [snapshot, setSnapshot] = useState(readSnapshot)

  useEffect(() => {
    const update = () => setSnapshot(readSnapshot())
    update()
    window.addEventListener('resize', update)
    window.visualViewport?.addEventListener('resize', update)
    return () => {
      window.removeEventListener('resize', update)
      window.visualViewport?.removeEventListener('resize', update)
    }
  }, [readSnapshot])

  return (
    <div className="pointer-events-none fixed bottom-2 left-2 z-[9999] rounded-lg border border-[var(--app-border)] bg-black/75 px-2 py-1 font-mono text-[10px] leading-tight text-white shadow-lg">
      {snapshot.map(([label, value]) => (
        <div key={label}>{label}: {value}</div>
      ))}
    </div>
  )
}

interface SidebarWorkspaceLayout {
  collapsed: boolean
  hidden: boolean
  ratio: number
}

interface TodoModalState {
  workspacePath: string
  workspaceName: string
}

interface DesktopUpdateProgressState {
  open: boolean
  job: DesktopUpdateJob | null
  startedAt: number | null
}

interface DesktopToastState {
  message: string
  tone: 'success' | 'error' | 'info'
}

interface StoredDesktopToastState extends DesktopToastState {
  createdAt: number
}

interface DesktopV3CompactingSessionState {
  sessionId: string
  startedAt: number
}

type DesktopSessionModeCommand = 'toggle-plan-auto'

interface PlanModalState {
  sessionId: string
}

interface GitPanelState {
  workspacePath: string
  workspaceName: string
}

interface ManagedWorktreeOption {
  path: string
  branch: string
  workspaceID: string
}

interface WorktreeSessionModalState {
  workspacePath: string
  workspaceName: string
  workspaceSlug: string
  routeOptions: DesktopChatRoute[]
  branchPrefix: string
  managedWorktrees: ManagedWorktreeOption[]
  settingsLoading: boolean
}

function desktopRunIntentFromV3(runIntent: V3SessionRunIntent | undefined) {
  if (!runIntent) return null
  return {
    sessionId: runIntent.session_id,
    runId: runIntent.run_id,
    status: runIntent.status,
    blockedReason: runIntent.blocked_reason ?? '',
    createdAt: runIntent.created_at,
    startedAt: runIntent.started_at,
    completedAt: runIntent.completed_at,
    durationMs: runIntent.duration_ms,
    cumulativeDurationMs: runIntent.cumulative_duration_ms,
    updatedAt: runIntent.updated_at,
    eventSeq: runIntent.event_seq,
  }
}

const EMPTY_DESKTOP_V3_RENDERED_MESSAGES: RenderedSessionMessages = {
  committed: [],
  pendingUser: [],
  liveRuns: [],
  runIntents: [],
}

function runIntentEqual(left: V3SessionRunIntent | undefined, right: V3SessionRunIntent | undefined): boolean {
  if (left === right) return true
  if (!left || !right) return false
  return left.session_id === right.session_id
    && left.run_id === right.run_id
    && left.status === right.status
    && left.blocked_reason === right.blocked_reason
    && left.created_at === right.created_at
    && left.started_at === right.started_at
    && left.completed_at === right.completed_at
    && left.duration_ms === right.duration_ms
    && left.cumulative_duration_ms === right.cumulative_duration_ms
    && left.updated_at === right.updated_at
    && left.event_seq === right.event_seq
}

function runIntentArrayEqual(left: V3SessionRunIntent[], right: V3SessionRunIntent[]): boolean {
  if (left === right) return true
  if (left.length !== right.length) return false
  for (let index = 0; index < left.length; index += 1) {
    if (!runIntentEqual(left[index], right[index])) return false
  }
  return true
}

function desktopV3RenderedMessagesEqual(left: RenderedSessionMessages, right: RenderedSessionMessages): boolean {
  return left.committed === right.committed
    && pendingUserMessagesEqual(left.pendingUser, right.pendingUser)
    && liveRunsEqual(left.liveRuns, right.liveRuns)
    && runIntentArrayEqual(left.runIntents, right.runIntents)
    && runIntentEqual(left.currentRunIntent, right.currentRunIntent)
    && runIntentEqual(left.latestRunIntent, right.latestRunIntent)
}

function desktopV3ConnectionStateFromRealtimeStatus(
  hydrateStatus: 'idle' | 'loading' | 'cached' | 'ready' | 'error',
  notificationCount: number,
): 'idle' | 'connecting' | 'open' | 'error' {
  if (hydrateStatus === 'loading' && notificationCount === 0) return 'connecting'
  if (hydrateStatus === 'error') return 'error'
  if (hydrateStatus === 'ready' || hydrateStatus === 'cached') return 'open'
  return 'idle'
}

function pendingUserMessagesEqual(left: RenderedSessionMessages['pendingUser'], right: RenderedSessionMessages['pendingUser']): boolean {
  if (left === right) return true
  if (left.length !== right.length) return false
  for (let index = 0; index < left.length; index += 1) {
    const a = left[index]
    const b = right[index]
    if (!a || !b) return false
    if (a.clientRequestId !== b.clientRequestId
      || a.messageId !== b.messageId
      || a.sessionId !== b.sessionId
      || a.content !== b.content
      || a.createdAt !== b.createdAt
      || a.status !== b.status
      || a.error !== b.error) return false
  }
  return true
}

function liveRunsEqual(left: RenderedSessionMessages['liveRuns'], right: RenderedSessionMessages['liveRuns']): boolean {
  if (left === right) return true
  if (left.length !== right.length) return false
  for (let index = 0; index < left.length; index += 1) {
    if (!liveRunEqual(left[index], right[index])) return false
  }
  return true
}

function liveRunEqual(left: RenderedSessionMessages['liveRuns'][number] | undefined, right: RenderedSessionMessages['liveRuns'][number] | undefined): boolean {
  if (left === right) return true
  if (!left || !right) return false
  return left.sessionId === right.sessionId
    && left.runId === right.runId
    && left.status === right.status
    && left.lastEventSeqSeen === right.lastEventSeqSeen
    && liveAssistantDraftEqual(left.assistantDraft, right.assistantDraft)
    && liveAssistantSegmentsEqual(left.assistantSegments, right.assistantSegments)
    && liveReasoningEqual(left.reasoning, right.reasoning)
    && liveReasoningByKeyEqual(left.reasoningByKey, right.reasoningByKey)
    && shallowNestedRecordEqual(left.toolCallsByCallId, right.toolCallsByCallId)
}

function liveAssistantDraftEqual(
  left: RenderedSessionMessages['liveRuns'][number]['assistantDraft'],
  right: RenderedSessionMessages['liveRuns'][number]['assistantDraft'],
): boolean {
  if (left === right) return true
  if (!left || !right) return false
  return left.content === right.content
    && left.updatedAt === right.updatedAt
    && left.timelineSeq === right.timelineSeq
}

function liveAssistantSegmentsEqual(
  left: RenderedSessionMessages['liveRuns'][number]['assistantSegments'],
  right: RenderedSessionMessages['liveRuns'][number]['assistantSegments'],
): boolean {
  if (left === right) return true
  if ((left?.length ?? 0) !== (right?.length ?? 0)) return false
  for (let index = 0; index < (left?.length ?? 0); index += 1) {
    const a = left?.[index]
    const b = right?.[index]
    if (!a || !b) return false
    if (a.id !== b.id || a.content !== b.content || a.createdAt !== b.createdAt || a.updatedAt !== b.updatedAt || a.timelineSeq !== b.timelineSeq) return false
  }
  return true
}

function liveReasoningEqual(
  left: RenderedSessionMessages['liveRuns'][number]['reasoning'],
  right: RenderedSessionMessages['liveRuns'][number]['reasoning'],
): boolean {
  if (left === right) return true
  if (!left || !right) return false
  return left.key === right.key
    && left.state === right.state
    && left.summary === right.summary
    && left.text === right.text
    && left.startedAt === right.startedAt
    && left.completedAt === right.completedAt
    && left.updatedAt === right.updatedAt
    && left.timelineSeq === right.timelineSeq
    && left.updatedSeq === right.updatedSeq
}

function desktopV3SidebarRowsEqual(left: DesktopV3SidebarRow[], right: DesktopV3SidebarRow[]): boolean {
  if (left === right) return true
  if (left.length !== right.length) return false
  for (let index = 0; index < left.length; index += 1) {
    if (!desktopV3SidebarRowEqual(left[index], right[index])) return false
  }
  return true
}

function desktopV3SidebarRowEqual(left: DesktopV3SidebarRow | undefined, right: DesktopV3SidebarRow | undefined): boolean {
  if (left === right) return true
  if (!left || !right) return false
  return left.sessionId === right.sessionId
    && sessionRecordEqual(left.record, right.record)
    && projectionEqual(left.projection, right.projection)
    && runIntentEqual(left.currentRunIntent, right.currentRunIntent)
    && runIntentRecordEqual(left.runIntents, right.runIntents)
    && left.pendingPermissionCount === right.pendingPermissionCount
    && pendingPermissionIdsEqual(left.pendingPermissions, right.pendingPermissions)
    && left.hasActivePlan === right.hasActivePlan
    && left.rowType === right.rowType
    && left.sidebarGroup === right.sidebarGroup
    && left.branchLabel === right.branchLabel
    && left.activePlan?.id === right.activePlan?.id
    && left.planExecution?.status === right.planExecution?.status
    && left.planExecution?.statusLabel === right.planExecution?.statusLabel
    && left.planExecution?.checkpointProgress.label === right.planExecution?.checkpointProgress.label
    && left.planExecution?.activeCheckpointId === right.planExecution?.activeCheckpointId
    && left.planExecution?.activeCheckpointStatus === right.planExecution?.activeCheckpointStatus
    && left.planExecution?.currentRunId === right.planExecution?.currentRunId
}

function sessionRecordEqual(left: DesktopV3SidebarRow['record'], right: DesktopV3SidebarRow['record']): boolean {
  if (left === right) return true
  if (left.kind !== right.kind) return false
  if (left.kind === 'stub' || right.kind === 'stub') {
    if (left.kind !== 'stub' || right.kind !== 'stub') return false
    return left.id === right.id
      && left.needsHydrate === right.needsHydrate
      && left.discoveredAt === right.discoveredAt
      && left.discoveredByWorksetId === right.discoveredByWorksetId
  }
  const a = left.session
  const b = right.session
  return a.id === b.id
    && a.title === b.title
    && a.workspace_path === b.workspace_path
    && a.workspace_name === b.workspace_name
    && a.mode === b.mode
    && a.created_at === b.created_at
    && a.updated_at === b.updated_at
    && a.message_count === b.message_count
    && a.last_message_at === b.last_message_at
    && a.worktree_enabled === b.worktree_enabled
    && a.worktree_root_path === b.worktree_root_path
    && a.worktree_base_branch === b.worktree_base_branch
    && a.worktree_branch === b.worktree_branch
    && shallowRecordEqual(a.metadata, b.metadata)
}

function projectionEqual(left: DesktopV3SidebarRow['projection'], right: DesktopV3SidebarRow['projection']): boolean {
  if (left === right) return true
  if (!left || !right) return false
  return left.session_id === right.session_id
    && left.last_event_seq === right.last_event_seq
    && left.projection_high_watermark_seq === right.projection_high_watermark_seq
    && left.updated_at === right.updated_at
}

function runIntentRecordEqual(left: Record<string, V3SessionRunIntent>, right: Record<string, V3SessionRunIntent>): boolean {
  const leftKeys = Object.keys(left)
  const rightKeys = Object.keys(right)
  if (leftKeys.length !== rightKeys.length) return false
  for (const key of leftKeys) {
    if (!runIntentEqual(left[key], right[key])) return false
  }
  return true
}

function pendingPermissionIdsEqual(left: DesktopV3SidebarRow['pendingPermissions'], right: DesktopV3SidebarRow['pendingPermissions']): boolean {
  if (left.length !== right.length) return false
  for (let index = 0; index < left.length; index += 1) {
    if (left[index]?.id !== right[index]?.id || left[index]?.status !== right[index]?.status) return false
  }
  return true
}

function liveReasoningByKeyEqual(
  left: RenderedSessionMessages['liveRuns'][number]['reasoningByKey'],
  right: RenderedSessionMessages['liveRuns'][number]['reasoningByKey'],
): boolean {
  const leftKeys = Object.keys(left ?? {})
  const rightKeys = Object.keys(right ?? {})
  if (leftKeys.length !== rightKeys.length) return false
  for (const key of leftKeys) {
    if (!liveReasoningEqual(left?.[key], right?.[key])) return false
  }
  return true
}

function shallowNestedRecordEqual(left: unknown, right: unknown): boolean {
  if (left === right) return true
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false
  const a = left as Record<string, unknown>
  const b = right as Record<string, unknown>
  const aKeys = Object.keys(a)
  const bKeys = Object.keys(b)
  if (aKeys.length !== bKeys.length) return false
  for (const key of aKeys) {
    if (!shallowRecordEqual(a[key], b[key])) return false
  }
  return true
}

function shallowRecordEqual(left: unknown, right: unknown): boolean {
  if (left === right) return true
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false
  const a = left as Record<string, unknown>
  const b = right as Record<string, unknown>
  const aKeys = Object.keys(a)
  const bKeys = Object.keys(b)
  if (aKeys.length !== bKeys.length) return false
  for (const key of aKeys) {
    if (!Object.is(a[key], b[key])) return false
  }
  return true
}

function metadataStringValue(metadata: Record<string, unknown> | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

export function desktopSidebarWorkspacePathForSession(
  session: Pick<DesktopSessionRecord, 'workspacePath' | 'metadata'>,
  workspacePathByBindingId?: ReadonlyMap<string, string>,
): string {
  const bindingID = metadataStringValue(session.metadata, 'local_workspace_binding_id')
    || metadataStringValue(session.metadata, 'swarm_v3_workspace_binding_id')
  if (bindingID) {
    const boundPath = workspacePathByBindingId?.get(bindingID)?.trim()
    if (boundPath) return boundPath
  }

  const sourceWorkspacePath = metadataStringValue(session.metadata, 'swarm_v3_source_workspace_path')
    || metadataStringValue(session.metadata, 'swarm_v2_source_workspace_path')
  if (sourceWorkspacePath) return sourceWorkspacePath

  return session.workspacePath.trim()
}

export function desktopRouteWorkspacePathForSession(
  session: Pick<DesktopSessionRecord, 'workspacePath' | 'metadata'>,
  workspacePathByBindingId: ReadonlyMap<string, string>,
  knownWorkspacePaths: ReadonlySet<string>,
): string {
  const bindingID = metadataStringValue(session.metadata, 'local_workspace_binding_id')
    || metadataStringValue(session.metadata, 'swarm_v3_workspace_binding_id')
  if (bindingID) {
    const boundPath = workspacePathByBindingId.get(bindingID)?.trim()
    if (boundPath) return boundPath
  }

  const candidates = [
    metadataStringValue(session.metadata, 'swarm_v3_source_workspace_path'),
    metadataStringValue(session.metadata, 'swarm_v2_source_workspace_path'),
    session.workspacePath.trim(),
  ]
  return candidates.find((path) => path && knownWorkspacePaths.has(path)) ?? ''
}

export function desktopSessionRecordFromV3SidebarRow(row: DesktopV3SidebarRow): DesktopSessionRecord {
  const record = row.record
  const session = record.kind === 'full'
    ? record.session
    : {
        id: record.id,
        title: record.id,
        workspace_path: '',
        workspace_name: 'Unknown workspace',
        mode: 'auto',
        created_at: record.discoveredAt ?? 0,
        updated_at: record.discoveredAt ?? 0,
        message_count: 0,
        last_message_at: record.discoveredAt ?? 0,
      }
  const runIntent = desktopRunIntentFromV3(row.currentRunIntent)
  const runIntentActive = runIntent ? ['pending_executor', 'running', 'dispatch_blocked'].includes(runIntent.status) : false
  const runIntentBlocked = runIntent?.status === 'dispatch_blocked'
  const pendingPermissionCount = row.pendingPermissionCount
  const liveStatus = pendingPermissionCount > 0 || runIntentBlocked ? 'blocked' : runIntentActive ? 'running' : 'idle'
  const updatedAt = row.projection?.updated_at ?? session.updated_at ?? session.last_message_at ?? session.created_at ?? 0

  return {
    id: session.id,
    title: record.kind === 'stub' ? `${session.id} · needs hydrate` : session.title || session.id,
    workspacePath: session.workspace_path || '',
    workspaceName: session.workspace_name || 'Unknown workspace',
    mode: session.mode || 'auto',
    lastEventSeq: row.projection?.last_event_seq,
    projectionHighWatermarkSeq: row.projection?.projection_high_watermark_seq,
    messageCount: session.message_count ?? 0,
    updatedAt,
    createdAt: session.created_at ?? updatedAt,
    permissionsHydrated: true,
    worktreeEnabled: session.worktree_enabled,
    worktreeRootPath: session.worktree_root_path,
    worktreeBaseBranch: session.worktree_base_branch,
    worktreeBranch: session.worktree_branch,
    lifecycle: null,
    runIntent,
    metadata: {
      ...(session.metadata ?? {}),
      swarm_v3_has_active_plan: row.hasActivePlan,
      swarm_v3_active_plan_id: row.activePlan?.id,
      swarm_v3_active_plan_title: row.activePlan?.title,
      swarm_v3_sidebar_row_type: row.rowType,
      swarm_v3_sidebar_group: row.sidebarGroup,
      swarm_v3_branch_label: row.branchLabel,
      swarm_v3_plan_execution_status: row.planExecution?.status,
      swarm_v3_plan_status_label: row.planExecution?.statusLabel,
      swarm_v3_plan_checkpoint_progress: row.planExecution?.checkpointProgress.label,
      swarm_v3_plan_checkpoint_active_index: row.planExecution?.checkpointProgress.activeIndex,
      swarm_v3_plan_checkpoint_completed_count: row.planExecution?.checkpointProgress.completedCount,
      swarm_v3_plan_checkpoint_total_count: row.planExecution?.checkpointProgress.totalCount,
      swarm_v3_active_checkpoint_id: row.planExecution?.activeCheckpointId,
      swarm_v3_active_checkpoint_title: row.planExecution?.activeCheckpointTitle,
      swarm_v3_active_checkpoint_status: row.planExecution?.activeCheckpointStatus,
      swarm_v3_plan_current_run_id: row.planExecution?.currentRunId,
      swarm_v3_plan_current_session_id: row.planExecution?.currentSessionId,
    },
    live: {
      runId: runIntentActive ? runIntent?.runId ?? null : null,
      agentName: null,
      startedAt: runIntentActive ? runIntent?.startedAt ?? null : null,
      status: liveStatus,
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
      summary: record.kind === 'stub' ? 'Session not hydrated yet' : null,
      lastEventType: null,
      lastEventAt: runIntent?.updatedAt ?? null,
      error: null,
      seq: row.projection?.last_event_seq ?? 0,
      assistantDraft: '',
      retainedAssistantSegments: [],
      reasoningSummary: '',
      reasoningText: '',
      reasoningState: 'idle',
      reasoningSegment: 0,
      reasoningStartedAt: null,
      awaitingAck: false,
    },
    pendingPermissions: row.pendingPermissions,
    pendingPermissionCount,
    usage: null,
  }
}

interface WorktreeSettingsWire {
  workspace_path?: string
  enabled?: boolean
  use_current_branch?: boolean
  base_branch?: string
  branch_name?: string
  updated_at?: number
}

interface ManagedWorktreeWire {
  path?: string
  workspace_id?: string
  branch?: string
  detached?: boolean
  exists?: boolean
  managed?: boolean
}

interface WorktreeSettingsResponseWire {
  ok?: boolean
  worktrees?: WorktreeSettingsWire
  managed?: ManagedWorktreeWire[]
}

function normalizeWorktreeBranchPrefix(value: string | undefined): string {
  const trimmed = (value ?? '').trim().replace(/^\/+|\/+$/g, '')
  if (!trimmed) return ''
  if (trimmed.toLowerCase().endsWith('/<id>')) {
    return trimmed.slice(0, -'/<id>'.length).replace(/^\/+|\/+$/g, '')
  }
  return trimmed
}

function normalizeManagedWorktreeBranch(value: string | undefined): string {
  return (value ?? '').trim().replace(/^refs\/heads\//, '')
}

async function fetchWorktreeSessionSettings(workspacePath: string): Promise<{ branchPrefix: string; managedWorktrees: ManagedWorktreeOption[] }> {
  const params = new URLSearchParams()
  const normalizedWorkspacePath = workspacePath.trim()
  if (normalizedWorkspacePath) params.set('workspace_path', normalizedWorkspacePath)
  const response = await requestJson<WorktreeSettingsResponseWire>(`/v1/worktrees?${params.toString()}`)
  const branchPrefix = normalizeWorktreeBranchPrefix(response.worktrees?.branch_name)
  if (!branchPrefix) {
    throw new Error('Worktree settings did not return a branch prefix')
  }
  const managedWorktrees = (response.managed ?? [])
    .map((item) => ({
      path: item.path?.trim() ?? '',
      branch: normalizeManagedWorktreeBranch(item.branch),
      workspaceID: item.workspace_id?.trim() ?? '',
      exists: item.exists === true,
      managed: item.managed === true,
      detached: item.detached === true,
    }))
    .filter((item) => item.path && item.branch && item.exists && item.managed && !item.detached)
    .sort((left, right) => left.branch.localeCompare(right.branch))
    .map(({ path, branch, workspaceID }) => ({ path, branch, workspaceID }))
  return { branchPrefix, managedWorktrees }
}

function normalizeWorktreeBranchSuffix(value: string): string {
  return value.trim().replace(/^\/+|\/+$/g, '')
}

function composeWorktreeBranchName(prefix: string, suffix: string): string {
  const normalizedPrefix = normalizeWorktreeBranchPrefix(prefix)
  const normalizedSuffix = normalizeWorktreeBranchSuffix(suffix)
  return normalizedSuffix ? `${normalizedPrefix}/${normalizedSuffix}` : normalizedPrefix
}

function WorktreeSessionModal({ state, title, branch, selectedExistingPath, busy, error, onTitleChange, onBranchChange, onSelectedExistingPathChange, onSubmit, onClose }: {
  state: WorktreeSessionModalState | null
  title: string
  branch: string
  selectedExistingPath: string
  busy: boolean
  error: string | null
  onTitleChange: (value: string) => void
  onBranchChange: (value: string) => void
  onSelectedExistingPathChange: (value: string) => void
  onSubmit: () => void
  onClose: () => void
}) {
  const reusingExistingWorktree = Boolean(selectedExistingPath.trim())
  if (!state) return null
  return (
    <Dialog>
      <DialogBackdrop onClick={busy ? undefined : onClose} />
      <DialogPanel className="w-[min(520px,100%)] gap-4 font-mono">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="text-sm font-semibold text-[var(--app-text)]">Worktree Session</div>
            <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{state.workspaceName || state.workspacePath}</div>
          </div>
          <button type="button" className={SIDEBAR_ACTION_BUTTON_CLASS} onClick={onClose} disabled={busy} title="Close" aria-label="Close worktree session modal">
            <X size={14} />
          </button>
        </div>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            onSubmit()
          }}
        >
          <label className="grid gap-1.5 text-xs text-[var(--app-text-muted)]">
            <span>Title:</span>
            <input
              name="title"
              className="h-10 border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 text-sm text-[var(--app-text)] outline-none focus:border-[var(--app-border-strong)]"
              value={title}
              onChange={(event) => onTitleChange(event.target.value)}
              disabled={busy}
              autoFocus
            />
          </label>
          <label className="grid gap-1.5 text-xs text-[var(--app-text-muted)]">
            <span>Reuse existing worktree:</span>
            <select
              className="h-10 border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 text-sm text-[var(--app-text)] outline-none focus:border-[var(--app-border-strong)]"
              value={selectedExistingPath}
              onChange={(event) => onSelectedExistingPathChange(event.target.value)}
              disabled={busy || state.settingsLoading || state.managedWorktrees.length === 0}
            >
              <option value="">Create a new worktree branch…</option>
              {state.managedWorktrees.map((item) => (
                <option key={item.path} value={item.path}>{item.branch}</option>
              ))}
            </select>
            <span className="text-[11px] text-[var(--app-text-subtle)]">Select a previous managed worktree to start a new conversation in it without creating or overwriting a branch.</span>
          </label>
          {!reusingExistingWorktree ? (
            <label className="grid gap-1.5 text-xs text-[var(--app-text-muted)]">
              <span>Branch suffix:</span>
              <div className="flex h-10 overflow-hidden border border-[var(--app-border)] bg-[var(--app-bg-alt)] text-sm focus-within:border-[var(--app-border-strong)]">
                <span className="flex shrink-0 items-center border-r border-[var(--app-border)] bg-[var(--app-bg)] px-3 font-mono text-[var(--app-text-muted)]">
                  {state.settingsLoading ? 'Loading…' : `${state.branchPrefix}/`}
                </span>
                <input
                  name="branch"
                  className="min-w-0 flex-1 bg-transparent px-3 text-[var(--app-text)] outline-none"
                  value={branch}
                  onChange={(event) => onBranchChange(event.target.value)}
                  disabled={busy || state.settingsLoading || !state.branchPrefix.trim()}
                  autoComplete="off"
                />
              </div>
              <span className="text-[11px] text-[var(--app-text-subtle)]">Prefix comes from Worktree settings. Change it in Settings → Worktrees.</span>
            </label>
          ) : null}
          {error ? <div className="border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning)]">{error}</div> : null}
          <div className="flex justify-end gap-3">
            <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
            <Button type="submit" disabled={busy || state.settingsLoading || !state.branchPrefix.trim() || !title.trim() || (!reusingExistingWorktree && !normalizeWorktreeBranchSuffix(branch))}>
              {busy ? 'Creating…' : 'Create session'}
            </Button>
          </div>
        </form>
      </DialogPanel>
    </Dialog>
  )
}

function GitDetailsOverlay({ state, snapshot, loading, error, onRefresh, onClose }: { state: GitPanelState | null; snapshot: GitSnapshot | null; loading: boolean; error: string | null; onRefresh: () => void; onClose: () => void }) {
  if (!state) return null
  const files = snapshot?.files ?? []
  return (
    <Dialog>
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="w-[min(760px,100%)] gap-4 font-mono">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="text-sm font-semibold text-[var(--app-text)]">Git status · {state.workspaceName}</div>
            <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{snapshot?.repo_root || state.workspacePath}</div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <button type="button" className={SIDEBAR_ACTION_BUTTON_CLASS} onClick={onRefresh} title="Refresh git status" aria-label="Refresh git status">
              <RefreshCcw size={14} className={cn(loading && 'animate-spin')} />
            </button>
            <button type="button" className={SIDEBAR_ACTION_BUTTON_CLASS} onClick={onClose} title="Close" aria-label="Close git details">
              <X size={14} />
            </button>
          </div>
        </div>
        {error ? <div className="border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning)]">{error}</div> : null}
        {snapshot?.has_git ? (
          <>
            <div className="grid gap-2 text-xs text-[var(--app-text-muted)] sm:grid-cols-4">
              <div className="border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Branch</div><div className="truncate text-[var(--app-text)]">{snapshot.branch || 'detached'}</div></div>
              <div className="border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Changes</div><div className="text-[var(--app-text)]">{snapshot.dirty_count}</div></div>
              <div className="border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Remote</div><div className="text-[var(--app-text)]">↑{snapshot.ahead_count} ↓{snapshot.behind_count}</div></div>
              <div className="border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Stash</div><div className="text-[var(--app-text)]">{snapshot.stash_count}</div></div>
            </div>
            <div className="min-h-0 overflow-hidden border border-[var(--app-border)]">
              <div className="border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 text-xs text-[var(--app-text-muted)]">Files ({files.length})</div>
              <div className="max-h-[360px] overflow-auto">
                {files.length === 0 ? <div className="px-3 py-4 text-xs text-[var(--app-text-subtle)]">Clean working tree.</div> : files.map((file) => (
                  <div key={`${file.kind}:${file.path}:${file.orig_path ?? ''}`} className="grid grid-cols-[96px_minmax(0,1fr)] gap-3 border-b border-[var(--app-border)] px-3 py-2 text-xs last:border-b-0">
                    <span className="text-[var(--app-text-subtle)]">{gitFileStatusLabel(file)}</span>
                    <span className="min-w-0 truncate text-[var(--app-text)]" title={file.orig_path ? `${file.orig_path} → ${file.path}` : file.path}>{file.orig_path ? `${file.orig_path} → ${file.path}` : file.path}</span>
                  </div>
                ))}
              </div>
            </div>
          </>
        ) : <div className="border border-[var(--app-border)] px-3 py-4 text-xs text-[var(--app-text-subtle)]">No git repository detected for this workspace.</div>}
      </DialogPanel>
    </Dialog>
  )
}

function normalizeWorkspaceTodoSummary(summary: WorkspaceTodoSummary): WorkspaceTodoSummary {
  return {
    ...summary,
    taskCount: summary.user.taskCount,
    openCount: summary.user.openCount,
    inProgressCount: summary.user.inProgressCount,
  }
}

function mergeWorkspaceTodoItemsByOwner(
  existing: WorkspaceTodoItem[],
  ownerKind: WorkspaceTodoOwnerKind,
  ownerItems: WorkspaceTodoItem[],
): WorkspaceTodoItem[] {
  return [...existing.filter((item) => item.ownerKind !== ownerKind), ...ownerItems]
}

function upsertWorkspaceTodoItem(existing: WorkspaceTodoItem[], nextItem: WorkspaceTodoItem): WorkspaceTodoItem[] {
  let found = false
  const clearOtherAgentInProgress = nextItem.ownerKind === 'agent' && nextItem.inProgress
  const updated = existing.map((item) => {
    if (item.id === nextItem.id) {
      found = true
      return nextItem
    }
    if (clearOtherAgentInProgress && item.ownerKind === 'agent' && item.inProgress) {
      return { ...item, inProgress: false }
    }
    return item
  })
  return found ? updated : [...updated, nextItem]
}

function normalizeRatio(value: number | undefined): number {
  if (typeof value !== 'number' || Number.isNaN(value) || value <= 0) {
    return 1
  }
  return value
}

function fallbackWorkspaceNameFromPath(path: string): string {
  const parts = path.trim().replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path.trim() || 'Temporary folder'
}

function gitFileStatusLabel(file: GitFileStatus): string {
  if (file.conflict) return 'conflict'
  if (file.untracked) return 'untracked'
  const labels: string[] = []
  if (file.staged) labels.push('staged')
  if (file.modified) labels.push('modified')
  return labels.join(' + ') || file.kind || 'changed'
}

function buildTemporaryWorkspaceEntry(path: string, workspaceName: string): WorkspaceEntry {
  return {
    path,
    localWorkspaceBindingId: '',
    workspaceName,
    themeId: '',
    directories: [path],
    isGitRepo: false,
    topologyRoutes: [],
    sortIndex: -1,
    addedAt: 0,
    updatedAt: 0,
    lastSelectedAt: 0,
    active: true,
    worktreeEnabled: false,
    gitBranch: '',
    gitHasGit: false,
    gitClean: false,
    gitDirtyCount: 0,
    gitStagedCount: 0,
    gitModifiedCount: 0,
    gitUntrackedCount: 0,
    gitConflictCount: 0,
    gitAheadCount: 0,
    gitBehindCount: 0,
    gitCommittedFileCount: 0,
    gitCommittedAdditions: 0,
    gitCommittedDeletions: 0,
  }
}

function updateJobMessage(job: DesktopUpdateJob | null): string {
  const message = job?.error?.trim() || job?.message?.trim()
  if (message) {
    return message
  }
  if (job?.status === 'completed') {
    return 'Update completed. Reloading desktop…'
  }
  if (job?.status === 'failed') {
    return 'Update failed.'
  }
  return 'Starting update helper…'
}

function updateCompleteToastMessage(job: DesktopUpdateJob): string {
  const message = job.message?.trim()
  if (message) {
    return message
  }
  return job.kind?.trim().toLowerCase() === 'dev'
    ? 'Dev rebuild completed.'
    : 'Swarm update completed.'
}

function loadPendingDesktopToast(): DesktopToastState | null {
  const raw = loadStoredValue(DESKTOP_PENDING_UPDATE_TOAST_STORAGE_KEY)
  if (!raw) {
    return null
  }
  saveStoredValue(DESKTOP_PENDING_UPDATE_TOAST_STORAGE_KEY, null)
  try {
    const parsed = JSON.parse(raw) as Partial<StoredDesktopToastState>
    const message = parsed.message?.trim()
    const tone = parsed.tone === 'error' || parsed.tone === 'info' ? parsed.tone : 'success'
    const createdAt = typeof parsed.createdAt === 'number' ? parsed.createdAt : 0
    if (!message || Date.now() - createdAt > 60_000) {
      return null
    }
    return { message, tone }
  } catch {
    return null
  }
}

function savePendingDesktopToast(toast: DesktopToastState): void {
  saveStoredValue(DESKTOP_PENDING_UPDATE_TOAST_STORAGE_KEY, JSON.stringify({ ...toast, createdAt: Date.now() } satisfies StoredDesktopToastState))
}

function updateProgressStepIndex(job: DesktopUpdateJob | null): number {
  const status = job?.status?.trim().toLowerCase() ?? ''
  const message = updateJobMessage(job).toLowerCase()
  if (status === 'completed') {
    return UPDATE_PROGRESS_STEP_TITLES.length
  }
  if (message.includes('container image') || message.includes('container images') || message.includes('local and remote') || message.includes('verify')) {
    return 4
  }
  if (message.includes('restart') || message.includes('reconnect')) {
    return 3
  }
  if (message.includes('rebuild') || message.includes('build') || message.includes('applying') || message.includes('installing') || message.includes('staging') || message.includes('fingerprint')) {
    return 2
  }
  return status === 'running' ? 1 : 0
}

function formatUpdateProgressTime(value: number | undefined): string {
  if (!value) {
    return '—'
  }
  return new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function loadSidebarWorkspaceLayout(): Record<string, SidebarWorkspaceLayout> {
  const raw = loadStoredValue(DESKTOP_SIDEBAR_LAYOUT_STORAGE_KEY)
  if (!raw) {
    return {}
  }

  try {
    const parsed = JSON.parse(raw) as Record<string, { hidden?: unknown; collapsed?: unknown; ratio?: unknown }>
    return Object.fromEntries(
      Object.entries(parsed).map(([path, entry]) => [
        path,
        {
          collapsed: Boolean(entry?.collapsed),
          hidden: Boolean(entry?.hidden),
          ratio: normalizeRatio(typeof entry?.ratio === 'number' ? entry.ratio : undefined),
        },
      ]),
    )
  } catch {
    return {}
  }
}

function swarmRoleLabel(target: Pick<SwarmTarget, 'role'> | null | undefined): string {
  const role = target?.role?.trim().toLowerCase() || ''
  switch (role) {
    case 'managed':
      return 'Remote Host'
    case 'child':
      return 'Child'
    case 'controller':
    case 'parent':
    case 'master':
      return 'Master'
    default:
      return role ? role.replace(/_/g, ' ') : 'Swarm'
  }
}

function sessionPendingPermissionCount(session: DesktopSessionRecord): number {
  return session.pendingPermissionCount
}

function sessionHasPendingPermission(session: DesktopSessionRecord): boolean {
  return sessionPendingPermissionCount(session) > 0
}

export function sessionActiveRunIntent(session: DesktopSessionRecord) {
  const runIntent = session.runIntent
  if (!runIntent) {
    return null
  }
  const status = runIntent.status.trim().toLowerCase()
  return status === 'pending_executor' || status === 'running' ? runIntent : null
}

function sessionHasCanonicalActiveRun(session: DesktopSessionRecord): boolean {
  return Boolean(sessionActiveRunIntent(session)?.runId.trim())
}

export function sessionStatusTone(session: DesktopSessionRecord): 'blocked' | 'running' | 'error' | 'idle' {
  if (sessionHasPendingPermission(session)) {
    return 'blocked'
  }
  if (sessionHasCanonicalActiveRun(session)) {
    return 'running'
  }
  return session.live.status === 'error' ? 'error' : 'idle'
}

function sessionMeta(session: DesktopSessionRecord): string | null {
  if (sessionHasPendingPermission(session)) {
    return 'Blocked • needs approval'
  }
  if (!sessionHasCanonicalActiveRun(session)) {
    return session.live.status === 'error' ? 'failed' : null
  }

  switch (session.live.status) {
    case 'blocked':
      return session.live.toolName ? `running ${session.live.toolName}` : 'running'
    case 'starting':
      return 'running'
    case 'running':
      return session.live.toolName ? `running ${session.live.toolName}` : 'running'
    default:
      return null
  }
}

function formatDurationCompact(durationMs: number): string {
  const safeDurationMs = Number.isFinite(durationMs) ? Math.max(0, durationMs) : 0
  if (safeDurationMs < 1000) {
    return '0s'
  }
  if (safeDurationMs < 60_000) {
    return `${Math.floor(safeDurationMs / 1000)}s`
  }
  const minutes = Math.floor(safeDurationMs / 60_000)
  const seconds = Math.floor((safeDurationMs % 60_000) / 1000)
  return `${minutes}m${String(seconds).padStart(2, '0')}s`
}

function nonNegativeDuration(value: number | null | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}

function formatRelativeTime(timestamp: number | null, now: number): string {
  if (typeof timestamp !== 'number' || timestamp <= 0) {
    return ''
  }

  const deltaMs = Math.max(0, now - timestamp)
  if (deltaMs < 60_000) {
    return 'just now'
  }

  const minutes = Math.floor(deltaMs / 60_000)
  if (minutes < 60) {
    return `${minutes} min${minutes === 1 ? '' : 's'} ago`
  }

  const hours = Math.floor(minutes / 60)
  if (hours < 24) {
    return `${hours} hr${hours === 1 ? '' : 's'} ago`
  }

  const days = Math.floor(hours / 24)
  return `${days} day${days === 1 ? '' : 's'} ago`
}

function sessionOriginLabel(session: DesktopSessionRecord, routeOptions: DesktopChatRoute[], fallbackSwarmName: string): string {
  const route = resolveDesktopChatRouteFromSession(session, routeOptions, null)
  if (route?.label.trim()) {
    return route.label.trim()
  }
  const normalizedFallback = fallbackSwarmName.trim()
  return normalizedFallback || 'host'
}

function sessionCommitSummary(session: DesktopSessionRecord): string {
  const count = Number(session.gitCommitCount ?? 0)
  if (!session.gitCommitDetected || count <= 0) {
    return ''
  }
  return count === 1 ? '1 commit' : `${count} commits`
}

function sessionCommittedFileSummary(session: DesktopSessionRecord): string {
  const count = Number(session.gitCommittedFileCount ?? 0)
  if (!session.gitCommitDetected || count <= 0) {
    return ''
  }
  return count === 1 ? '1 file' : `${count} files`
}

function sessionCommittedDeltaSummary(session: DesktopSessionRecord): string {
  if (!session.gitCommitDetected) {
    return ''
  }
  const additions = Math.max(0, Number(session.gitCommittedAdditions ?? 0))
  const deletions = Math.max(0, Number(session.gitCommittedDeletions ?? 0))
  if (additions <= 0 && deletions <= 0) {
    return ''
  }
  return `+${additions} -${deletions}`
}

function sessionStatusTooltip(session: DesktopSessionRecord): string {
  const lines: string[] = []
  if (session.worktreeEnabled) {
    lines.push('Worktree enabled')
    const branch = session.worktreeBranch?.trim() || session.gitBranch?.trim()
    const baseBranch = session.worktreeBaseBranch?.trim()
    if (branch) {
      lines.push(`Branch: ${branch}`)
    }
    if (baseBranch) {
      lines.push(`Base: ${baseBranch}`)
    }
    lines.push(`Staged: ${session.gitStagedCount ?? 0}`)
    lines.push(`Modified: ${session.gitModifiedCount ?? 0}`)
    lines.push(`Untracked: ${session.gitUntrackedCount ?? 0}`)
    lines.push(`Conflicts: ${session.gitConflictCount ?? 0}`)
    const commitSummary = sessionCommitSummary(session)
    if (commitSummary) {
      lines.push(`Committed: ${commitSummary}`)
    }
    const fileSummary = sessionCommittedFileSummary(session)
    if (fileSummary) {
      lines.push(`Committed files: ${fileSummary}`)
    }
    const deltaSummary = sessionCommittedDeltaSummary(session)
    if (deltaSummary) {
      lines.push(`Committed diff: ${deltaSummary}`)
    }
    const ahead = Number(session.gitAheadCount ?? 0)
    const behind = Number(session.gitBehindCount ?? 0)
    if (ahead > 0 || behind > 0) {
      lines.push(`Base branch: ↑${ahead} ↓${behind}`)
    }
    return lines.join('\n')
  }
  const commitSummary = sessionCommitSummary(session)
  if (commitSummary) {
    lines.push(`Session recorded ${commitSummary}`)
  }
  return lines.join('\n')
}

function sidebarSummaryLabel(session: DesktopSessionRecord): string {
  const compactLabel = sidebarCompactionLabel(session)
  if (compactLabel) {
    return compactLabel
  }

  const summary = session.live.summary?.trim() ?? ''
  if (!summary) {
    return ''
  }

  const normalized = summary.toLowerCase()
  if (
    summary.includes('\n')
    || normalized === 'starting...'
    || normalized === 'starting…'
    || normalized === 'assistant responding...'
    || normalized === 'assistant responding…'
    || normalized === 'streaming response...'
    || normalized === 'streaming response…'
    || normalized.startsWith('tool.')
    || normalized.startsWith('tool:')
    || normalized.startsWith('turn.')
    || normalized.startsWith('run.')
    || normalized.startsWith('session.')
    || normalized.startsWith('message.')
  ) {
    return ''
  }

  if (summary.length > 80) {
    return ''
  }

  if (summary === session.live.agentName?.trim()) {
    return ''
  }

  return summary
}

function sidebarCompactionLabel(session: DesktopSessionRecord): string {
  const toolName = session.live.toolName?.trim().toLowerCase() ?? ''
  const summary = session.live.summary?.trim().toLowerCase() ?? ''
  if (toolName !== 'compact' && summary !== 'manual compact' && summary !== 'overflow compact' && summary !== 'auto compact' && summary !== 'compact') {
    return ''
  }
  switch (summary) {
    case 'manual compact':
      return 'Manual compact'
    case 'overflow compact':
      return 'Overflow compact'
    case 'auto compact':
      return 'Auto compact'
    default:
      return 'Compact'
  }
}

function sidebarLiveToolLabel(session: DesktopSessionRecord): string {
  if (!['starting', 'running', 'blocked'].includes(session.live.status)) {
    return ''
  }
  return session.live.sidebarToolName?.trim() ?? ''
}

function metadataText(session: DesktopSessionRecord, key: string): string {
  const value = session.metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function sessionSidebarRowType(session: DesktopSessionRecord): 'plan_session' | 'single_chat' {
  return metadataText(session, 'swarm_v3_sidebar_row_type') === 'plan_session' ? 'plan_session' : 'single_chat'
}

type SidebarBaseSessionGroupID = 'needs_review' | 'in_progress' | 'active_chats' | 'archived'
type SidebarSessionGroupID = SidebarBaseSessionGroupID | 'pinned'

function sessionSidebarGroup(session: DesktopSessionRecord): SidebarBaseSessionGroupID {
  const group = metadataText(session, 'swarm_v3_sidebar_group')
  return group === 'needs_review' || group === 'in_progress' || group === 'archived' ? group : 'active_chats'
}

function sessionManuallyPinnedInSidebar(session: DesktopSessionRecord): boolean {
  return session.metadata?.[DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY] === true
}

function sessionAllowsManualSidebarPin(session: DesktopSessionRecord): boolean {
  return sessionSidebarGroup(session) === 'active_chats'
}

export function sessionSidebarDisplayGroup(session: DesktopSessionRecord): SidebarSessionGroupID {
  const group = sessionSidebarGroup(session)
  if (group === 'active_chats' && sessionManuallyPinnedInSidebar(session)) {
    return 'pinned'
  }
  return group
}

function sessionPlanCheckpointProgressLabel(session: DesktopSessionRecord): string {
  return metadataText(session, 'swarm_v3_plan_checkpoint_progress')
}

function metadataNumber(session: DesktopSessionRecord, key: string): number {
  const value = session.metadata?.[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function sessionPlanCheckpointCounts(session: DesktopSessionRecord): { activeIndex: number; completedCount: number; totalCount: number } {
  return {
    activeIndex: metadataNumber(session, 'swarm_v3_plan_checkpoint_active_index'),
    completedCount: metadataNumber(session, 'swarm_v3_plan_checkpoint_completed_count'),
    totalCount: metadataNumber(session, 'swarm_v3_plan_checkpoint_total_count'),
  }
}

function sessionWorkspaceLabel(session: DesktopSessionRecord): string {
  return session.workspaceName?.trim() || fallbackWorkspaceNameFromPath(session.workspacePath || '') || 'Workspace'
}

function sessionBranchLabel(session: DesktopSessionRecord): string {
  return metadataText(session, 'swarm_v3_branch_label') || session.worktreeBranch?.trim() || session.gitBranch?.trim() || ''
}

function sessionRowMetadataLabel(session: DesktopSessionRecord): string {
  const seen = new Set<string>()
  return [sessionWorkspaceLabel(session), sessionBranchLabel(session)]
    .filter((value) => {
      const normalized = value.trim().toLowerCase()
      if (!normalized || seen.has(normalized)) return false
      seen.add(normalized)
      return true
    })
    .join(' · ')
}

function sessionIsActive(session: DesktopSessionRecord): boolean {
  return sessionHasPendingPermission(session) || sessionHasCanonicalActiveRun(session)
}

function positiveTimestamp(value: number | null | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0
}

function sessionStartedSortAnchor(session: DesktopSessionRecord): number {
  const activeRun = sessionActiveRunIntent(session)
  return positiveTimestamp(activeRun?.startedAt)
    || positiveTimestamp(activeRun?.createdAt)
    || positiveTimestamp(session.live.startedAt)
    || positiveTimestamp(session.createdAt)
    || positiveTimestamp(session.updatedAt)
}

function sessionDurableActivityAt(session: DesktopSessionRecord): number {
  const updatedAt = positiveTimestamp(session.updatedAt)
  if (updatedAt > 0) {
    return updatedAt
  }
  return positiveTimestamp(session.createdAt)
}

function sessionSidebarDisplayTimestamp(session: DesktopSessionRecord): number | null {
  if (sessionIsActive(session)) {
    return session.live.lastEventAt ?? sessionDurableActivityAt(session)
  }
  const durableAt = sessionDurableActivityAt(session)
  return durableAt > 0 ? durableAt : null
}

function sessionSidebarSortAnchor(session: DesktopSessionRecord): number {
  if (sessionIsActive(session)) {
    return sessionStartedSortAnchor(session)
  }
  return sessionDurableActivityAt(session) || sessionStartedSortAnchor(session)
}

function sessionShouldPinInSidebar(session: DesktopSessionRecord, now: number): boolean {
  if (sessionIsActive(session)) {
    return true
  }
  if (sessionAllowsManualSidebarPin(session) && sessionManuallyPinnedInSidebar(session)) {
    return true
  }

  const lastActivityAt = sessionDurableActivityAt(session)
  return lastActivityAt > 0
    && now - lastActivityAt <= SIDEBAR_ACTIVITY_GRACE_MS
    && sessionSidebarSortAnchor(session) > 0
}

export function compareSidebarSessions(left: DesktopSessionRecord, right: DesktopSessionRecord, now: number): number {
  const leftPinned = sessionShouldPinInSidebar(left, now)
  const rightPinned = sessionShouldPinInSidebar(right, now)
  if (leftPinned !== rightPinned) {
    return leftPinned ? -1 : 1
  }

  const leftActive = sessionIsActive(left)
  const rightActive = sessionIsActive(right)
  if (leftPinned && rightPinned) {
    if (leftActive !== rightActive) {
      return leftActive ? -1 : 1
    }

    const leftAnchor = sessionSidebarSortAnchor(left)
    const rightAnchor = sessionSidebarSortAnchor(right)
    const anchorDelta = leftActive && rightActive
      ? leftAnchor - rightAnchor
      : rightAnchor - leftAnchor
    if (anchorDelta !== 0) {
      return anchorDelta
    }
  }

  if (!leftActive && !rightActive) {
    const activityDelta = sessionSidebarSortAnchor(right) - sessionSidebarSortAnchor(left)
    if (activityDelta !== 0) {
      return activityDelta
    }
  }

  const startedDelta = sessionStartedSortAnchor(right) - sessionStartedSortAnchor(left)
  if (startedDelta !== 0) {
    return startedDelta
  }

  const updatedDelta = positiveTimestamp(right.updatedAt) - positiveTimestamp(left.updatedAt)
  if (updatedDelta !== 0) {
    return updatedDelta
  }

  return left.id.localeCompare(right.id)
}

export function sessionStatusDetail(session: DesktopSessionRecord, now: number): string {
  return formatRelativeTime(sessionSidebarDisplayTimestamp(session), now)
}

function activeRunTimerAnchor(activeRun: NonNullable<ReturnType<typeof sessionActiveRunIntent>>): number {
  return positiveTimestamp(activeRun.startedAt)
    || positiveTimestamp(activeRun.createdAt)
    || positiveTimestamp(activeRun.updatedAt)
}

export function sessionTimerLabel(session: DesktopSessionRecord, now: number): string {
  const activeRun = sessionActiveRunIntent(session)
  if (!activeRun) return ''

  const storedRunDurationMs = nonNegativeDuration(activeRun.durationMs)
  const startedAt = activeRunTimerAnchor(activeRun)
  const loopDurationMs = startedAt > 0 ? Math.max(0, now - startedAt) : storedRunDurationMs
  if (loopDurationMs === null) return ''

  const cumulativeDurationMs = nonNegativeDuration(activeRun.cumulativeDurationMs)
  const loopTimer = formatDurationCompact(loopDurationMs)
  const overallDurationMs = cumulativeDurationMs !== null ? cumulativeDurationMs + loopDurationMs : loopDurationMs
  const overallTimer = formatDurationCompact(overallDurationMs)
  return cumulativeDurationMs !== null && overallTimer !== loopTimer ? `${loopTimer} (${overallTimer})` : loopTimer
}

function sessionTimerTooltip(session: DesktopSessionRecord): string {
  return sessionActiveRunIntent(session) ? DESKTOP_V3_RUN_TIMER_TOOLTIP : ''
}

export function sessionActivityLabel(session: DesktopSessionRecord): string {
  if (sessionHasPendingPermission(session)) {
    return 'Needs approval'
  }

  if (!sessionHasCanonicalActiveRun(session)) {
    return session.live.status === 'error' ? 'failed' : ''
  }

  const toolLabel = sidebarLiveToolLabel(session)
  return sidebarCompactionLabel(session)
    || toolLabel
    || sidebarSummaryLabel(session)
    || ''
}

interface SidebarSessionNode {
  session: DesktopSessionRecord
  children: SidebarSessionNode[]
  depth: number
  kind: SidebarSessionNodeKind
  label: string | null
  assignmentLabel: string | null
}

export function buildSidebarSessionTree(sessions: DesktopSessionRecord[], now: number, preserveInputOrder = false): SidebarSessionNode[] {
  const sortedSessions = preserveInputOrder
    ? sessions
    : sessions.length > 1
      ? [...sessions].sort((left, right) => compareSidebarSessions(left, right, now))
      : sessions
  const byID = new Map<string, SidebarSessionNode>()
  for (const session of sortedSessions) {
    const descriptor = sessionChildDescriptor(session)
    byID.set(session.id, {
      session,
      children: [],
      depth: 0,
      kind: descriptor.kind,
      label: descriptor.label,
      assignmentLabel: descriptor.assignmentLabel,
    })
  }

  const roots: SidebarSessionNode[] = []
  const attachNode = (node: SidebarSessionNode, seen: Set<string>) => {
    const parentSessionID = sessionParentSessionID(node.session)
    const parentNode = parentSessionID ? byID.get(parentSessionID) : undefined
    if (!parentNode || parentNode === node || seen.has(parentNode.session.id)) {
      node.depth = 0
      roots.push(node)
      return
    }
    if (parentNode.depth === 0 && !roots.includes(parentNode)) {
      attachNode(parentNode, new Set([...seen, node.session.id]))
    }
    node.depth = parentNode.depth + 1
    parentNode.children.push(node)
  }

  for (const session of sortedSessions) {
    const node = byID.get(session.id)
    if (!node) {
      continue
    }
    attachNode(node, new Set())
  }

  const uniqueRoots = Array.from(new Map(roots.map((node) => [node.session.id, node])).values())
  const dedupeChildren = (nodes: SidebarSessionNode[]) => {
    for (const node of nodes) {
      node.children = Array.from(new Map(node.children.map((child) => [child.session.id, child])).values())
      if (node.children.length > 0) {
        dedupeChildren(node.children)
      }
    }
  }
  dedupeChildren(uniqueRoots)

  const sortNodes = (nodes: SidebarSessionNode[]) => {
    nodes.sort((left, right) => compareSidebarSessions(left.session, right.session, now))
    for (const node of nodes) {
      if (node.children.length > 0) {
        sortNodes(node.children)
      }
    }
  }
  if (!preserveInputOrder) {
    sortNodes(uniqueRoots)
  }
  return uniqueRoots
}

interface SessionAgentSummary {
  total: number
  running: number
}

function summarizeSubagentDescendants(node: SidebarSessionNode): SessionAgentSummary {
  let total = 0
  let running = 0
  const visit = (nodes: SidebarSessionNode[]) => {
    for (const child of nodes) {
      if (child.kind === 'subagent') {
        total += 1
        if (sessionIsActive(child.session)) {
          running += 1
        }
      }
      if (child.children.length > 0) {
        visit(child.children)
      }
    }
  }
  visit(node.children)
  return { total, running }
}

function nodeHasSubagentDescendants(node: SidebarSessionNode): boolean {
  for (const child of node.children) {
    if (child.kind === 'subagent' || nodeHasSubagentDescendants(child)) {
      return true
    }
  }
  return false
}

function nodeContainsDescendantSession(node: SidebarSessionNode, sessionID: string | null | undefined): boolean {
  const normalizedID = sessionID?.trim() ?? ''
  if (!normalizedID) {
    return false
  }
  for (const child of node.children) {
    if (child.session.id === normalizedID || nodeContainsDescendantSession(child, normalizedID)) {
      return true
    }
  }
  return false
}

function sidebarNodeLastActivityAt(node: SidebarSessionNode): number {
  return Math.max(sessionDurableActivityAt(node.session), ...node.children.map(sidebarNodeLastActivityAt))
}

function sidebarNodeIsProtected(node: SidebarSessionNode, selectedSessionID: string): boolean {
  return node.session.id === selectedSessionID
    || nodeContainsDescendantSession(node, selectedSessionID)
    || sessionIsActive(node.session)
    || (sessionAllowsManualSidebarPin(node.session) && sessionManuallyPinnedInSidebar(node.session))
    || node.children.some((child) => sidebarNodeIsProtected(child, selectedSessionID))
}

export function filterInactiveSidebarSessionTrees(nodes: SidebarSessionNode[], now: number, hideAfterHours: number | null, selectedSessionID = ''): { nodes: SidebarSessionNode[]; hiddenCount: number } {
  if (hideAfterHours === null) return { nodes, hiddenCount: 0 }
  const cutoff = now - hideAfterHours * 60 * 60 * 1000
  const visible: SidebarSessionNode[] = []
  let hiddenCount = 0
  for (const node of nodes) {
    const ordinary = sessionSidebarDisplayGroup(node.session) === 'active_chats'
    if (ordinary && sidebarNodeLastActivityAt(node) < cutoff && !sidebarNodeIsProtected(node, selectedSessionID)) {
      hiddenCount += 1
    } else {
      visible.push(node)
    }
  }
  return { nodes: visible, hiddenCount }
}

function sidebarNodeSessionIDs(node: SidebarSessionNode): string[] {
  return [node.session.id, ...node.children.flatMap(sidebarNodeSessionIDs)]
}

function flattenVisibleSidebarSessionNodes(
  nodes: SidebarSessionNode[],
  expandedSessionIDs: Record<string, boolean>,
  forcedVisibleSessionID: string | null | undefined,
): SidebarSessionNode[] {
  const output: SidebarSessionNode[] = []
  const visit = (node: SidebarSessionNode) => {
    output.push(node)
    const shouldExpand = !nodeHasSubagentDescendants(node)
      || Boolean(expandedSessionIDs[node.session.id])
      || nodeContainsDescendantSession(node, forcedVisibleSessionID)
    if (!shouldExpand) {
      return
    }
    for (const child of node.children) {
      visit(child)
    }
  }
  for (const node of nodes) {
    visit(node)
  }
  return output
}

interface SessionRowProps {
  active: boolean
  now: number
  session: DesktopSessionRecord
  fallbackSwarmName: string
  routeOptions: DesktopChatRoute[]
  workspaceSlug: string | ((session: DesktopSessionRecord) => string)
  depth?: number
  childLabel?: string | null
  childAssignmentLabel?: string | null
  childKind?: SidebarSessionNode['kind']
  agentSummary: SessionAgentSummary
  agentsExpanded: boolean
  compactingStartedAt?: number | null
  pendingAction?: 'pin' | 'archive' | 'rename' | null
  selectionMode?: boolean
  selected?: boolean
  onSelect: (sessionId: string) => void | boolean
  onToggleSelected?: (sessionId: string, range: boolean) => void
  onPrefetch: (sessionId: string) => void
  onToggleAgents: (sessionId: string) => void
  onTogglePinned: (sessionId: string) => void
  onArchive: (sessionId: string) => void
  onRename: (sessionId: string, title: string) => Promise<void>
}

function SessionRow({ active, now, session: initialSession, fallbackSwarmName, routeOptions, workspaceSlug, depth = 0, childAssignmentLabel = null, agentSummary, agentsExpanded, compactingStartedAt = null, pendingAction = null, selectionMode = false, selected = false, onSelect, onToggleSelected, onPrefetch, onToggleAgents, onTogglePinned, onArchive, onRename }: SessionRowProps) {
  const session = initialSession
  const compactingActive = typeof compactingStartedAt === 'number' && compactingStartedAt > 0
  const activeSession = compactingActive || sessionIsActive(session)
  const originLabel = sessionOriginLabel(session, routeOptions, fallbackSwarmName)
  const backgroundInfo = sessionBackgroundInfo(session, originLabel)
  const rowWorkspaceSlug = typeof workspaceSlug === 'function' ? workspaceSlug(session) : workspaceSlug
  const rowType = sessionSidebarRowType(session)
  const isPlanRow = rowType === 'plan_session'
  const checkpointProgressLabel = sessionPlanCheckpointProgressLabel(session)
  const checkpointCounts = sessionPlanCheckpointCounts(session)
  const compactingTimer = compactingActive && compactingStartedAt !== null ? formatDurationCompact(now - compactingStartedAt) : ''
  const tooltip = [sessionStatusTooltip(session), sessionTimerTooltip(session)].filter(Boolean).join('\n')
  const isNestedSession = depth > 0
  const nestedAssignmentTitle = isNestedSession && childAssignmentLabel ? childAssignmentLabel : ''
  const rowTitle = nestedAssignmentTitle || session.title || 'New conversation'
  const hasAgentChildren = agentSummary.total > 0
  const metadataLabel = sessionRowMetadataLabel(session)
  const relativeActivityLabel = sessionStatusDetail(session, now)
  const hasPendingPermission = sessionHasPendingPermission(session)
  const pendingPermissionAlertActive = hasPendingPermission && !active
  const rowTimerLabel = pendingPermissionAlertActive
    ? 'Needs approval'
    : compactingActive
      ? compactingTimer
      : sessionHasCanonicalActiveRun(session)
        ? sessionTimerLabel(session, now)
        : relativeActivityLabel
  const singleStatusLabel = compactingActive
    ? 'Compacting'
    : activeSession
      ? sessionActivityLabel(session)
      : sessionMeta(session) || ''
  const rightSideLabel = hasPendingPermission || isPlanRow ? '' : singleStatusLabel
  const statusTone = sessionStatusTone(session)
  const showStatusCircle = activeSession || statusTone === 'error'
  const checkpointTotalCount = Math.max(0, checkpointCounts.totalCount)
  const checkpointCompletedCount = Math.min(Math.max(0, checkpointCounts.completedCount), checkpointTotalCount)
  const showPlanProgressBar = isPlanRow && sessionSidebarGroup(session) === 'in_progress' && checkpointTotalCount > 0
  const checkpointProgressPercent = checkpointTotalCount > 0 ? Math.min(100, (checkpointCompletedCount / checkpointTotalCount) * 100) : 0
  const checkpointProgressAriaLabel = checkpointProgressLabel || `Checkpoint progress: ${checkpointCompletedCount} of ${checkpointTotalCount} complete`
  const [actionsOpen, setActionsOpen] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const [renameDraft, setRenameDraft] = useState(rowTitle)
  const [renameError, setRenameError] = useState<string | null>(null)
  const actionMenuRef = useRef<HTMLSpanElement | null>(null)
  const actionMenuCloseTimerRef = useRef<number | null>(null)
  const clearActionMenuCloseTimer = useCallback(() => {
    if (actionMenuCloseTimerRef.current !== null) {
      window.clearTimeout(actionMenuCloseTimerRef.current)
      actionMenuCloseTimerRef.current = null
    }
  }, [])
  const closeActionMenu = useCallback(() => {
    clearActionMenuCloseTimer()
    setActionsOpen(false)
  }, [clearActionMenuCloseTimer])
  useEffect(() => clearActionMenuCloseTimer, [clearActionMenuCloseTimer])
  useEffect(() => {
    if (!actionsOpen) {
      return undefined
    }
    const handlePointerDownOutside = (event: PointerEvent) => {
      const target = event.target
      if (target instanceof Node && actionMenuRef.current?.contains(target)) {
        return
      }
      closeActionMenu()
    }
    document.addEventListener('pointerdown', handlePointerDownOutside, true)
    return () => document.removeEventListener('pointerdown', handlePointerDownOutside, true)
  }, [actionsOpen, closeActionMenu])
  const hasDetailsRowContent = Boolean(backgroundInfo)
  const showDetailsRow = !isPlanRow && hasDetailsRowContent
  const showActionMenuInMetadataRow = !showPlanProgressBar && !showDetailsRow
  const pinned = sessionAllowsManualSidebarPin(session) && sessionManuallyPinnedInSidebar(session)
  const pinDisabled = pendingAction !== null || !sessionAllowsManualSidebarPin(session)
  const archiveDisabled = pendingAction !== null
  const actionButtonBaseClass = cn(
    'inline-flex h-6 w-full shrink-0 items-center gap-2 rounded border-0 bg-transparent px-2 text-left font-inherit text-[11px] leading-6 text-[var(--app-text-subtle)] transition-[background-color,color] hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:cursor-default disabled:opacity-60 disabled:hover:bg-transparent',
  )
  const actionMenuButtonClass = cn(
    'inline-flex h-4 w-4 shrink-0 items-center justify-center rounded border-0 bg-transparent p-0 text-[var(--app-text-subtle)] opacity-0 transition-[background-color,color,opacity] hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] focus:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100',
    actionsOpen ? 'opacity-100 text-[var(--app-text)]' : null,
  )
  const pinActionControl = sessionAllowsManualSidebarPin(session) ? (
    <button
      type="button"
      className={cn(actionButtonBaseClass, pinned ? 'text-[var(--app-primary)] hover:text-[var(--app-primary-hover)]' : null)}
      disabled={pinDisabled}
      aria-label={pinned ? `Unpin ${rowTitle}` : `Pin ${rowTitle}`}
      aria-pressed={pinned}
      title={pinned ? 'Unpin from sidebar' : 'Pin to sidebar'}
      onClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
        if (!pinDisabled) {
          closeActionMenu()
          onTogglePinned(session.id)
        }
      }}
    >
      {pendingAction === 'pin' ? <LoaderCircle size={12} className="animate-spin" aria-hidden="true" /> : <Pin size={12} aria-hidden="true" />}
      <span>{pinned ? 'Unpin' : 'Pin'}</span>
    </button>
  ) : null
  const renameActionControl = (
    <button
      type="button"
      className={actionButtonBaseClass}
      disabled={pendingAction !== null}
      aria-label={`Rename ${rowTitle}`}
      onClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
        closeActionMenu()
        setRenameDraft(rowTitle)
        setRenameError(null)
        setRenaming(true)
      }}
    >
      {pendingAction === 'rename' ? <LoaderCircle size={12} className="animate-spin" aria-hidden="true" /> : <Pencil size={12} aria-hidden="true" />}
      <span>Rename</span>
    </button>
  )
  const archiveActionControl = (
    <button
      type="button"
      className={actionButtonBaseClass}
      disabled={archiveDisabled}
      aria-label={`Archive ${rowTitle}`}
      title="Archive session"
      onClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
        if (!archiveDisabled) {
          closeActionMenu()
          onArchive(session.id)
        }
      }}
    >
      {pendingAction === 'archive' ? <LoaderCircle size={12} className="animate-spin" aria-hidden="true" /> : <Archive size={12} aria-hidden="true" />}
      <span>Archive</span>
    </button>
  )
  const subagentSessionsActionControl = hasAgentChildren ? (
    <button
      type="button"
      className={cn(actionButtonBaseClass, agentsExpanded ? 'text-[var(--app-primary)] hover:text-[var(--app-primary-hover)]' : null)}
      aria-label={`${agentsExpanded ? 'Hide' : 'Show'} ${agentSummary.total} subagent session${agentSummary.total === 1 ? '' : 's'}`}
      aria-pressed={agentsExpanded}
      title={agentsExpanded ? 'Hide subagent sessions' : 'Show subagent sessions'}
      onClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
        onToggleAgents(session.id)
      }}
    >
      <Bot size={12} aria-hidden="true" />
      <span className="min-w-0 flex-1 truncate">Subagent sessions</span>
      <span className="ml-auto font-mono tabular-nums text-[10px] leading-none text-[var(--app-text-muted)]">{agentSummary.total}</span>
    </button>
  ) : null
  const actionMenu = (
    <span
      ref={actionMenuRef}
      className={cn('relative z-20 inline-flex translate-x-[5px] shrink-0 items-center', actionsOpen ? 'z-40' : null)}
      onPointerDownCapture={(event) => {
        event.preventDefault()
        event.stopPropagation()
      }}
      onMouseEnter={clearActionMenuCloseTimer}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          closeActionMenu()
        }
      }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.preventDefault()
          event.stopPropagation()
          closeActionMenu()
        }
      }}
    >
      <button
        type="button"
        className={actionMenuButtonClass}
        aria-label={`Open actions for ${rowTitle}`}
        aria-expanded={actionsOpen}
        title="Session actions"
        onClick={(event) => {
          event.preventDefault()
          event.stopPropagation()
          setActionsOpen((open) => {
            clearActionMenuCloseTimer()
            return !open
          })
        }}
      >
        <MoreVertical size={13} aria-hidden="true" />
      </button>
      {actionsOpen ? (
        <span
          className="absolute right-0 top-full z-50 mt-1 grid min-w-40 gap-0.5 rounded-md border border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] p-1 opacity-100 shadow-lg backdrop-blur-none [background-color:var(--app-surface-elevated)]"
          onMouseEnter={clearActionMenuCloseTimer}
          onClick={(event) => {
            event.preventDefault()
            event.stopPropagation()
          }}
        >
          {subagentSessionsActionControl}
          {pinActionControl}
          {renameActionControl}
          {archiveActionControl}
        </span>
      ) : null}
    </span>
  )
  return (
    <Link
      to="/$workspaceSlug/$sessionId"
      params={{ workspaceSlug: rowWorkspaceSlug, sessionId: session.id }}
      onClick={(event) => {
        if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.altKey || event.ctrlKey || event.shiftKey) {
          return
        }
        event.preventDefault()
        onSelect(session.id)
      }}
      onKeyDown={(event) => {
        if (event.key === ' ') {
          event.preventDefault()
          onSelect(session.id)
        }
      }}
      onMouseEnter={() => onPrefetch(session.id)}
      onFocus={() => onPrefetch(session.id)}
      className={cn(
        'group relative grid w-full min-w-0 rounded-md border text-left outline-none transition-[background-color,border-color,box-shadow,transform]',
        isPlanRow ? 'gap-1.5 px-2.5 py-2' : 'gap-1 px-2.5 py-1.5',
        active
          ? 'border-[var(--app-border-accent)] bg-[var(--app-surface)]/45 shadow-[0_0_0_1px_color-mix(in_oklab,var(--app-border-accent)_20%,transparent)]'
          : 'border-transparent bg-[var(--app-surface)]/45 hover:-translate-y-px hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)] hover:shadow-[0_10px_24px_rgba(0,0,0,0.10)]',
        pendingPermissionAlertActive ? 'border-transparent bg-[var(--app-warning-bg)] hover:border-transparent hover:bg-[var(--app-warning-bg)]' : null,
        isNestedSession ? 'ml-0 rounded-sm border-transparent bg-[var(--app-bg-alt)]/20 py-1 pl-1 pr-2 hover:translate-y-0 hover:border-transparent hover:bg-[var(--app-surface)]/25 hover:shadow-[0_6px_16px_rgba(0,0,0,0.06)]' : null,
        isNestedSession && active ? 'border-transparent bg-[var(--app-surface)]/30' : null,
        hasAgentChildren && agentsExpanded && !isNestedSession ? 'border-[var(--app-border-accent)]' : null,
        actionsOpen ? 'z-30' : null,
      )}
      title={tooltip || metadataLabel}
    >
      <div className="flex min-w-0 items-start justify-between gap-2">
        <div className="flex min-w-0 flex-1 items-start gap-2">
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 items-center gap-2">
              {selectionMode && depth === 0 ? (
                <input
                  type="checkbox"
                  checked={selected}
                  aria-label={`Select ${rowTitle}`}
                  onChange={() => undefined}
                  onClick={(event) => { event.preventDefault(); event.stopPropagation(); onToggleSelected?.(session.id, event.shiftKey) }}
                  className="h-4 w-4 shrink-0 accent-[var(--app-primary)]"
                />
              ) : null}
              {renaming ? (
                <form
                  className="min-w-0 flex-1"
                  onSubmit={(event) => {
                    event.preventDefault()
                    event.stopPropagation()
                    const title = renameDraft.trim()
                    if (!title) { setRenameError('Title is required.'); return }
                    void onRename(session.id, title).then(() => setRenaming(false)).catch((error) => setRenameError(error instanceof Error ? error.message : 'Rename failed'))
                  }}
                >
                  <input
                    autoFocus
                    value={renameDraft}
                    disabled={pendingAction === 'rename'}
                    aria-label={`Rename ${rowTitle}`}
                    onChange={(event) => setRenameDraft(event.target.value)}
                    onClick={(event) => event.stopPropagation()}
                    onKeyDown={(event) => {
                      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); setRenameError(null); setRenaming(false) }
                    }}
                    className="h-6 w-full rounded border border-[var(--app-border-accent)] bg-[var(--app-bg-inset)] px-1.5 text-[12px] text-[var(--app-text)] outline-none"
                  />
                  {renameError ? <span className="block truncate text-[9px] text-[var(--app-error)]">{renameError}</span> : null}
                </form>
              ) : (
                <span className={cn('min-w-0 flex-1 truncate font-medium text-[var(--app-text)]', isNestedSession ? 'text-[12px]' : 'text-[13px]')}>
                  {rowTitle}
                </span>
              )}

            </div>
          </div>
        </div>
        <span className="inline-flex shrink-0 items-center justify-end gap-1.5 text-[10px] leading-4 text-[var(--app-text-muted)]">
          {compactingActive ? <LoaderCircle size={10} className="animate-spin text-[var(--app-primary)]" aria-hidden="true" /> : null}
          {rightSideLabel ? <span className="max-w-[5.5rem] truncate text-right">{rightSideLabel}</span> : null}
          {showStatusCircle ? (
            <span
              className={cn(
                'h-1.5 w-1.5 shrink-0 rounded-full',
                compactingActive || statusTone === 'running'
                  ? 'bg-[var(--app-success)]'
                  : statusTone === 'blocked'
                    ? 'bg-[var(--app-warning)]'
                    : statusTone === 'error'
                      ? 'bg-[var(--app-danger)]'
                      : 'bg-[var(--app-border-strong)]',
              )}
            />
          ) : null}
        </span>
      </div>
      <div className="mt-0.5 flex min-w-0 items-center justify-between gap-2 text-[10px] leading-4 text-[var(--app-text-subtle)]">
        <span className="min-w-0 truncate">{metadataLabel}</span>
        <span className="ml-auto inline-flex shrink-0 items-center justify-end gap-1 text-right tabular-nums text-[var(--app-text-muted)]">
          {rowTimerLabel ? <span>{rowTimerLabel}</span> : null}
          {showActionMenuInMetadataRow ? actionMenu : null}
        </span>
      </div>

      {showPlanProgressBar ? (
        <div className="flex min-w-0 items-center gap-2 text-[10px] leading-4 text-[var(--app-text-subtle)]">
          <div
            className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-[var(--app-surface-subtle)]"
            role="progressbar"
            aria-label={checkpointProgressAriaLabel}
            aria-valuemin={0}
            aria-valuemax={checkpointTotalCount}
            aria-valuenow={checkpointCompletedCount}
            aria-valuetext={`${checkpointCompletedCount} of ${checkpointTotalCount} checkpoints complete`}
          >
            <div
              className="h-full rounded-full bg-[var(--app-primary)] transition-[width]"
              style={{ width: `${checkpointProgressPercent}%` }}
            />
          </div>
          <div className="ml-auto flex min-w-0 shrink-0 items-center gap-1.5">
            {actionMenu}
          </div>
        </div>
      ) : null}

      {showDetailsRow ? (
        <div className="flex min-w-0 items-center justify-between gap-2 text-[10px] leading-4 text-[var(--app-text-subtle)]">
          <div className="flex min-w-0 flex-1 items-center gap-1.5 truncate">
            {backgroundInfo ? (
              <span className="inline-flex h-4 shrink-0 items-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-1.5 font-medium leading-none text-[var(--app-text-muted)]">
                {backgroundInfo.badge}
              </span>
            ) : null}
            {backgroundInfo?.targetLabel ? <span className="truncate">{backgroundInfo.targetLabel}</span> : null}
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-1.5">
            {!isPlanRow ? actionMenu : null}
          </div>
        </div>
      ) : null}
    </Link>
  )
}

interface RenderSidebarSessionGroupsInput {
  nodes: SidebarSessionNode[]
  routeSessionId: string
  now: number
  fallbackSwarmName: string
  routeOptions: DesktopChatRoute[]
  workspaceSlug: string | ((session: DesktopSessionRecord) => string)
  expandedAgentSessions: Record<string, boolean>
  compactingSession: DesktopV3CompactingSessionState | null
  pendingActions: Record<string, 'pin' | 'archive' | 'rename' | undefined>
  selectionMode: boolean
  selectedRootIDs: Set<string>
  onSelect: (sessionId: string) => void | boolean
  onToggleSelected: (sessionId: string, range: boolean) => void
  onPrefetch: (sessionId: string) => void
  onToggleAgents: (sessionId: string) => void
  onTogglePinned: (sessionId: string) => void
  onArchive: (sessionId: string) => void
  onRename: (sessionId: string, title: string) => Promise<void>
}

export const SIDEBAR_SESSION_GROUPS = [
  { id: 'needs_review', label: 'Needs Review' },
  { id: 'in_progress', label: 'In Progress' },
  { id: 'pinned', label: 'Pinned' },
  { id: 'active_chats', label: 'Active Chats' },
  { id: 'archived', label: 'Archived' },
] as const satisfies ReadonlyArray<{ id: SidebarSessionGroupID; label: string }>

function renderSidebarSessionGroups(input: RenderSidebarSessionGroupsInput): JSX.Element[] | null {
  if (input.nodes.length === 0) return null
  const grouped = new Map<SidebarSessionGroupID, SidebarSessionNode[]>()
  for (const group of SIDEBAR_SESSION_GROUPS) {
    grouped.set(group.id, [])
  }
  let currentRootGroup: SidebarSessionGroupID | null = null
  for (const node of input.nodes) {
    if (node.depth === 0 || !currentRootGroup) {
      currentRootGroup = sessionSidebarDisplayGroup(node.session)
    }
    grouped.get(currentRootGroup)?.push(node)
  }
  const activeGroups = SIDEBAR_SESSION_GROUPS.filter((group) => group.id !== 'archived')
  const archivedGroup = SIDEBAR_SESSION_GROUPS.find((group) => group.id === 'archived')
  const groups = archivedGroup ? [...activeGroups, archivedGroup] : activeGroups
  return groups.flatMap((group) => {
    const nodes = grouped.get(group.id) ?? []
    if (nodes.length === 0) return []
    return [(
      <section key={group.id} className="grid content-start gap-1.5">
        <div className="px-1 pt-1 text-[9px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">
          {group.label}
        </div>
        <div className="grid gap-1">
          {nodes.map((node) => (
          <SessionRow
            key={node.session.id}
            active={input.routeSessionId === node.session.id}
            now={input.now}
            session={node.session}
            fallbackSwarmName={input.fallbackSwarmName}
            routeOptions={input.routeOptions}
            workspaceSlug={input.workspaceSlug}
            depth={node.depth}
            childLabel={node.label}
            childAssignmentLabel={node.assignmentLabel}
            childKind={node.kind}
            agentSummary={summarizeSubagentDescendants(node)}
            agentsExpanded={Boolean(input.expandedAgentSessions[node.session.id]) || nodeContainsDescendantSession(node, input.routeSessionId || undefined)}
            compactingStartedAt={input.compactingSession?.sessionId === node.session.id ? input.compactingSession.startedAt : null}
            pendingAction={input.pendingActions[node.session.id] ?? null}
            selectionMode={input.selectionMode}
            selected={input.selectedRootIDs.has(node.session.id)}
            onSelect={input.onSelect}
            onToggleSelected={input.onToggleSelected}
            onPrefetch={input.onPrefetch}
            onToggleAgents={input.onToggleAgents}
            onTogglePinned={input.onTogglePinned}
            onArchive={input.onArchive}
            onRename={input.onRename}
          />
          ))}
        </div>
      </section>
    )]
  })
}

export function DesktopAppPage() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const workspaceSessionMatch = matchRoute({ to: '/$workspaceSlug/$sessionId', fuzzy: false })
  const workspaceMatch = matchRoute({ to: '/$workspaceSlug', fuzzy: false })
  const routeWorkspaceSlug = (workspaceSessionMatch ? workspaceSessionMatch.workspaceSlug : workspaceMatch ? workspaceMatch.workspaceSlug : '').trim()
  const routeSessionId = (workspaceSessionMatch ? workspaceSessionMatch.sessionId : '').trim()
  const pwaDebugEnabled = typeof window !== 'undefined' && new URLSearchParams(window.location.search).has(PWA_DEBUG_QUERY_PARAM)
  const { workspaces, currentWorkspacePath, loading: launcherWorkspacesLoading } = useWorkspaceLauncher({ applyDocumentTheme: false, autoRefresh: false, browseDuringRefresh: false })
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [expandedAgentSessions, setExpandedAgentSessions] = useState<Record<string, boolean>>({})
  const [notificationsOpen, setNotificationsOpen] = useState(false)
  const [notificationActionError, setNotificationActionError] = useState<string | null>(null)
  const [searchModalOpen, setSearchModalOpen] = useState(false)
  const [todoModal, setTodoModal] = useState<TodoModalState | null>(null)
  const [gitPanel, setGitPanel] = useState<GitPanelState | null>(null)
  const [planModal, setPlanModal] = useState<PlanModalState | null>(null)
  const [planModalLoading, setPlanModalLoading] = useState(false)
  const [planModalSaving, setPlanModalSaving] = useState(false)
  const [planModalExecuting, setPlanModalExecuting] = useState(false)
  const [planModalError, setPlanModalError] = useState<string | null>(null)
  const [quickSettingsTab, setQuickSettingsTab] = useState<QuickSettingsTabID | null>(null)
  const [quickActionsOpen, setQuickActionsOpen] = useState(false)
  const [sessionModeCommand, setSessionModeCommand] = useState<DesktopSessionModeCommand | null>(null)
  const [gitRealtimeErrors, setGitRealtimeErrors] = useState<Record<string, string>>({})
  const [todoItems, setTodoItems] = useState<Record<string, WorkspaceTodoItem[]>>({})
  const [todoSummaries, setTodoSummaries] = useState<Record<string, WorkspaceTodoSummary>>({})
  const [editingSidebarSwarmName, setEditingSidebarSwarmName] = useState(false)
  const [sidebarSwarmNameDraft, setSidebarSwarmNameDraft] = useState('')
  const [sidebarSwarmNameSaving, setSidebarSwarmNameSaving] = useState(false)
  const [sidebarSwarmNameError, setSidebarSwarmNameError] = useState<string | null>(null)
  const [updateRunning, setUpdateRunning] = useState(false)
  const [updateError, setUpdateError] = useState<string | null>(null)
  const [updateProgress, setUpdateProgress] = useState<DesktopUpdateProgressState>({ open: false, job: null, startedAt: null })
  const [desktopToast, setDesktopToast] = useState<DesktopToastState | null>(() => loadPendingDesktopToast())
  const [worktreeSessionModal, setWorktreeSessionModal] = useState<WorktreeSessionModalState | null>(null)
  const [worktreeSessionTitle, setWorktreeSessionTitle] = useState('')
  const [worktreeSessionBranch, setWorktreeSessionBranch] = useState('')
  const [worktreeSessionExistingPath, setWorktreeSessionExistingPath] = useState('')
  const [worktreeSessionCreating, setWorktreeSessionCreating] = useState(false)
  const [worktreeSessionError, setWorktreeSessionError] = useState<string | null>(null)
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [todoSavingWorkspacePath, setTodoSavingWorkspacePath] = useState<string | null>(null)
  const [workspaceLayout, setWorkspaceLayout] = useState<Record<string, SidebarWorkspaceLayout>>(() => loadSidebarWorkspaceLayout())
  const [sidebarWorkspaceControlPath, setSidebarWorkspaceControlPath] = useState('')
  const [compactingSession, setCompactingSession] = useState<DesktopV3CompactingSessionState | null>(null)
  const [sidebarSessionActions, setSidebarSessionActions] = useState<Record<string, 'pin' | 'archive' | 'rename' | undefined>>({})
  const [sidebarSelectionMode, setSidebarSelectionMode] = useState(false)
  const [selectedSidebarRootIDs, setSelectedSidebarRootIDs] = useState<Set<string>>(() => new Set())
  const [lastSelectedSidebarRootID, setLastSelectedSidebarRootID] = useState<string | null>(null)
  const [bulkArchivePending, setBulkArchivePending] = useState(false)
  const [sidebarThresholdSaving, setSidebarThresholdSaving] = useState(false)
  const [sidebarNow, setSidebarNow] = useState(() => Date.now())
  const [previousChatSessionId, setPreviousChatSessionId] = useState<string | null>(null)
  const activeChatSessionIdRef = useRef<string | null>(null)
  const sidebarBodyRef = useRef<HTMLDivElement | null>(null)
  const mobileSidebarSwipeRef = useRef<MobileSidebarSwipeState | null>(null)
  const workspaceByPath = useMemo<Map<string, WorkspaceEntry>>(
    () => new Map(workspaces.map((workspace) => [workspace.path, workspace] as const)),
    [workspaces],
  )
  const workspacePathByBindingId = useMemo<Map<string, string>>(
    () => new Map(workspaces
      .map((workspace) => [workspace.localWorkspaceBindingId.trim(), workspace.path] as const)
      .filter(([bindingID]) => bindingID !== '')),
    [workspaces],
  )
  const knownWorkspacePaths = useMemo<Set<string>>(
    () => new Set(workspaces.map((workspace) => workspace.path)),
    [workspaces],
  )
  const routeWorkspace = useMemo(
    () => (routeWorkspaceSlug ? resolveWorkspaceBySlug(workspaces, routeWorkspaceSlug) : null),
    [routeWorkspaceSlug, workspaces],
  )
  useEffect(() => {
    if (!desktopToast) {
      return
    }
    const timer = window.setTimeout(() => setDesktopToast(null), 5_000)
    return () => window.clearTimeout(timer)
  }, [desktopToast])

  const temporaryRouteWorkspace = useMemo<WorkspaceEntry | null>(() => {
    if (!routeWorkspaceSlug || routeSessionId || routeWorkspace) {
      return null
    }
    return null
  }, [routeSessionId, routeWorkspace, routeWorkspaceSlug])
  const selectedWorkspacePath = useMemo<string | null>(() => {
    if (routeWorkspace?.path) {
      return routeWorkspace.path
    }
    if (temporaryRouteWorkspace?.path) {
      return temporaryRouteWorkspace.path
    }
    return null
  }, [routeWorkspace?.path, temporaryRouteWorkspace])
  const savedSelectedWorkspace = selectedWorkspacePath ? workspaceByPath.get(selectedWorkspacePath) ?? null : null
  const selectedWorkspace = savedSelectedWorkspace ?? (temporaryRouteWorkspace?.path === selectedWorkspacePath ? temporaryRouteWorkspace : null)
  const sidebarWorkspaceEntries = useMemo<WorkspaceEntry[]>(() => {
    if (!selectedWorkspacePath || savedSelectedWorkspace) {
      return workspaces
    }
    const temporaryWorkspace = temporaryRouteWorkspace
      ?? buildTemporaryWorkspaceEntry(selectedWorkspacePath, fallbackWorkspaceNameFromPath(selectedWorkspacePath))
    return [temporaryWorkspace, ...workspaces]
  }, [savedSelectedWorkspace, selectedWorkspacePath, temporaryRouteWorkspace, workspaces])
  const mergedSidebarWorkspaceEntries = useMemo(() => sidebarWorkspaceEntries.map((workspace) => ({
    ...workspace,
    todoSummary: todoSummaries[workspace.path] ?? workspace.todoSummary,
  })), [sidebarWorkspaceEntries, todoSummaries])
  const visibleSidebarWorkspaceEntries = useMemo(
    () => mergedSidebarWorkspaceEntries.filter((workspace) => !workspaceLayout[workspace.path]?.hidden),
    [mergedSidebarWorkspaceEntries, workspaceLayout],
  )
  const visibleWorkspacePaths = useMemo<string[]>(() => visibleSidebarWorkspaceEntries.map((workspace) => workspace.path), [visibleSidebarWorkspaceEntries])
  const selectedGitWorkspacePath = selectedWorkspacePath ?? visibleWorkspacePaths[0] ?? ''

  const gitStatusQuery = useQuery({
    queryKey: gitStatusQueryKey(selectedGitWorkspacePath),
    queryFn: () => fetchGitStatus(selectedGitWorkspacePath, 12),
    enabled: selectedGitWorkspacePath.trim() !== '',
    staleTime: 0,
    refetchOnWindowFocus: true,
  })
  const gitSnapshot = gitStatusQuery.data?.status ?? null
  const gitSnapshotByPath = useMemo(() => {
    const entries = new Map<string, GitSnapshot>()
    if (gitSnapshot?.workspace_path) entries.set(gitSnapshot.workspace_path, gitSnapshot)
    if (selectedGitWorkspacePath && gitSnapshot) entries.set(selectedGitWorkspacePath, gitSnapshot)
    return entries
  }, [gitSnapshot, selectedGitWorkspacePath])

  useEffect(() => {
    let cancelled = false
    visibleWorkspacePaths.forEach((workspacePath) => {
      void startGitRealtime(workspacePath)
        .then(() => {
          if (!cancelled) {
            setGitRealtimeErrors((current) => {
              if (!current[workspacePath]) return current
              const next = { ...current }
              delete next[workspacePath]
              return next
            })
          }
        })
        .catch((error) => {
          if (!cancelled) {
            setGitRealtimeErrors((current) => ({ ...current, [workspacePath]: error instanceof Error ? error.message : String(error) }))
          }
        })
    })
    return () => { cancelled = true }
  }, [visibleWorkspacePaths])

  const overviewQuery = useQuery({
    ...workspaceOverviewQueryOptions([], 25),
    placeholderData: (previousData) => previousData,
  })
  const workspacesLoading = launcherWorkspacesLoading || overviewQuery.isPending
  const uiSettingsQuery = useQuery({
    queryKey: uiSettingsQueryKey(),
    queryFn: () => getUISettings(),
    staleTime: 30_000,
  })
  const agentStateQuery = useQuery(agentStateQueryOptions())
  const draftPreferenceQuery = useQuery(draftModelQueryOptions())
  useEffect(() => {
    if (uiSettingsQuery.data) {
      setUISettings(uiSettingsQuery.data)
    }
  }, [uiSettingsQuery.data])
  useEffect(() => {
    applyDesktopRouteTheme(selectedWorkspacePath, workspaces, uiSettingsQuery.data ?? uiSettings, Boolean(routeWorkspaceSlug))
  }, [routeWorkspaceSlug, selectedWorkspacePath, workspaces, uiSettingsQuery.data, uiSettings])
  const swarmSettingsQuery = useQuery({
    queryKey: ['ui-settings', 'swarm'] as const,
    queryFn: () => getSwarmSettings(),
    staleTime: 30_000,
  })
  const swarmTargetsQuery = useQuery({
    queryKey: ['swarm-targets'] as const,
    queryFn: () => fetchSwarmTargets(),
    staleTime: SWARM_TARGET_REFETCH_INTERVAL_MS,
    refetchInterval: SWARM_TARGET_REFETCH_INTERVAL_MS,
    refetchIntervalInBackground: true,
  })
  const updateStatusQuery = useQuery({
    queryKey: ['desktop-update-status'] as const,
    queryFn: () => fetchDesktopUpdateStatus(),
    staleTime: UPDATE_STATUS_REFETCH_INTERVAL_MS,
    refetchInterval: UPDATE_STATUS_REFETCH_INTERVAL_MS,
    refetchIntervalInBackground: true,
  })

  const updateStatus = updateStatusQuery.data ?? null
  const updateAvailable = updateStatus?.update_available === true
  const updateDevMode = updateStatus?.dev_mode === true
  const updateActionEnabled = updateAvailable || updateDevMode
  const updateActionLabel = updateDevMode ? 'Update Dev' : 'Update Swarm'
  const updateLatestVersion = updateStatus?.latest_version?.trim() ?? ''
  const updateStatusError = updateStatusQuery.error instanceof Error ? updateStatusQuery.error.message : null
  const updateAttentionVisible = updateActionEnabled || updateRunning || Boolean(updateError)
  const updateActionTitle = updateError
    || (updateRunning
      ? updateDevMode ? 'Rebuilding Swarm dev checkout…' : 'Updating Swarm…'
      : updateDevMode
        ? 'Rebuild Swarm dev checkout'
        : updateAvailable
          ? `Update Swarm${updateLatestVersion ? ` to ${updateLatestVersion}` : ''}`
          : updateStatusQuery.isLoading
            ? 'Checking for Swarm updates…'
            : updateStatusError
              ? `Update status unavailable: ${updateStatusError}`
              : updateStatus?.suppressed
                ? 'Updates are not available for this build'
                : 'Swarm is up to date')

  const swarmTargets = swarmTargetsQuery.data?.targets ?? []
  const currentSwarmTarget = swarmTargets.find((target) => target.current) ?? null
  const swarmName = currentSwarmTarget?.name ?? swarmSettingsQuery.data?.name ?? 'Local'
  const sidebarSwarmNameDirty = sidebarSwarmNameDraft.trim() !== swarmName.trim()
  const currentSwarmRoleLabel = swarmRoleLabel(currentSwarmTarget)
  const masterWorkspaceName = selectedWorkspace?.workspaceName ?? routeWorkspace?.workspaceName ?? fallbackWorkspaceNameFromPath(selectedWorkspacePath ?? '')
  const notificationItems = useDesktopV3CacheSelector(selectOrderedNotifications)
  const notificationSummary = useDesktopV3CacheSelector(selectNotificationSummary)
  const notificationUnreadCount = Math.max(0, notificationSummary.unreadCount)
  const notificationAttentionVisible = true
  const headerActionCount = 1 + (notificationAttentionVisible ? 1 : 0) + (updateAttentionVisible ? 1 : 0)
  const headerActionRowClass = headerActionCount === 3
    ? 'grid min-w-0 grid-cols-[minmax(0,1fr)_80px] items-center gap-2.5 min-h-7 pr-4'
    : headerActionCount === 1
      ? 'grid min-w-0 grid-cols-[minmax(0,1fr)_24px] items-center gap-2.5 min-h-7 pr-4'
      : cn(SIDEBAR_ACTION_ROW_CLASS, 'min-h-7 pr-4')
  const headerActionRailClass = headerActionCount === 3
    ? '!w-[80px] !grid-cols-[24px_24px_24px]'
    : headerActionCount === 1
      ? '!w-6 !grid-cols-[24px]'
      : undefined
  const swarmTopologySignature = useMemo(
    () => swarmTargets
      .map((target) => [
        target.swarm_id.trim(),
        target.relationship.trim(),
        target.role.trim(),
        target.attach_status?.trim() ?? '',
        target.backend_url?.trim() ?? '',
        target.current ? '1' : '0',
        target.online ? '1' : '0',
      ].join(':'))
      .sort()
      .join('|'),
    [swarmTargets],
  )
  useEffect(() => {
    if (!editingSidebarSwarmName) {
      setSidebarSwarmNameDraft(swarmName)
    }
  }, [editingSidebarSwarmName, swarmName])

  useEffect(() => {
  }, [overviewQuery.data?.workspaces, overviewQuery.fetchStatus, overviewQuery.status])

  useEffect(() => {
    if (!swarmTopologySignature) {
      return
    }
    void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
  }, [queryClient, swarmTopologySignature])

  const handleOpenNotifications = useCallback(() => {
    setNotificationsOpen(true)
    setNotificationActionError(null)
  }, [])

  const mutateNotificationState = useCallback(async (action: () => Promise<void>, fallbackMessage: string): Promise<void> => {
    setNotificationActionError(null)
    try {
      await action()
    } catch (error) {
      const message = error instanceof Error ? error.message : fallbackMessage
      setNotificationActionError(message)
      setDesktopToast({ message, tone: 'error' })
    }
  }, [])

  const handleMarkNotificationRead = useCallback((record: DesktopNotificationCenterRecord) => (
    mutateNotificationState(async () => {
      await updateNotification(record.id, { read: true })
    }, 'Failed to mark notification read')
  ), [mutateNotificationState])

  const handleAcknowledgeNotification = useCallback((record: DesktopNotificationCenterRecord) => (
    mutateNotificationState(async () => {
      await updateNotification(record.id, { acked: true })
    }, 'Failed to acknowledge notification')
  ), [mutateNotificationState])

  const handleMuteNotification = useCallback((record: DesktopNotificationCenterRecord) => (
    mutateNotificationState(async () => {
      await updateNotification(record.id, { muted: true })
    }, 'Failed to mute notification')
  ), [mutateNotificationState])

  const handleClearNotifications = useCallback(() => (
    mutateNotificationState(async () => {
      await clearNotifications()
    }, 'Failed to clear notifications')
  ), [mutateNotificationState])

  const closeTodoModal = useCallback(() => {
    setTodoModal(null)
  }, [])
  const openGitPanel = useCallback((workspacePath: string, workspaceName: string) => {
    const normalizedPath = workspacePath.trim()
    if (!normalizedPath) return
    setGitPanel({ workspacePath: normalizedPath, workspaceName })
    void queryClient.invalidateQueries({ queryKey: gitStatusQueryKey(normalizedPath) })
  }, [queryClient])
  const closeGitPanel = useCallback(() => {
    setGitPanel(null)
  }, [])

  const handleStartSidebarSwarmNameEdit = useCallback(() => {
    setSidebarSwarmNameDraft(swarmName)
    setSidebarSwarmNameError(null)
    setEditingSidebarSwarmName(true)
  }, [swarmName])

  const handleCancelSidebarSwarmNameEdit = useCallback(() => {
    setSidebarSwarmNameDraft(swarmName)
    setSidebarSwarmNameError(null)
    setEditingSidebarSwarmName(false)
  }, [swarmName])

  const handleSaveSidebarSwarmName = useCallback(async () => {
    const normalized = sidebarSwarmNameDraft.trim()
    if (!normalized) {
      setSidebarSwarmNameError('Swarm name is required.')
      return
    }
    if (!sidebarSwarmNameDirty) {
      setEditingSidebarSwarmName(false)
      setSidebarSwarmNameError(null)
      return
    }
    setSidebarSwarmNameSaving(true)
    setSidebarSwarmNameError(null)
    try {
      const savedSettings = await saveSwarmSettings({ name: normalized })
      const savedName = savedSettings.name.trim() || normalized
      setUISettings(savedSettings.raw)
      setSidebarSwarmNameDraft(savedName)
      queryClient.setQueryData(uiSettingsQueryKey(), savedSettings.raw)
      queryClient.setQueryData(['ui-settings', 'swarm'], savedSettings)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['ui-settings'] }),
        queryClient.invalidateQueries({ queryKey: ['ui-settings', 'swarm'] }),
        queryClient.invalidateQueries({ queryKey: ['swarm-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['workspace-overview'] }),
      ])
      window.dispatchEvent(new CustomEvent('swarm:name-updated', { detail: { name: savedName } }))
      setEditingSidebarSwarmName(false)
    } catch (error) {
      setSidebarSwarmNameError(error instanceof Error ? error.message : 'Failed to save swarm name')
    } finally {
      setSidebarSwarmNameSaving(false)
    }
  }, [queryClient, sidebarSwarmNameDirty, sidebarSwarmNameDraft])

  const mutateTodoState = useCallback(async <T,>(workspacePath: string, action: () => Promise<T>): Promise<T> => {
    const normalizedPath = workspacePath.trim()
    setTodoSavingWorkspacePath(normalizedPath)
    try {
      return await action()
    } finally {
      setTodoSavingWorkspacePath(null)
    }
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => {
      setSidebarNow(Date.now())
    }, 1000)

    return () => {
      window.clearInterval(timer)
    }
  }, [])

  useEffect(() => {
    setWorkspaceLayout((current) => {
      let changed = false
      const next: Record<string, SidebarWorkspaceLayout> = { ...current }

      for (const workspace of mergedSidebarWorkspaceEntries) {
        const existing = current[workspace.path]
        const normalizedEntry = {
          collapsed: existing?.collapsed ?? true,
          hidden: existing?.hidden ?? false,
          ratio: normalizeRatio(existing?.ratio),
        }
        next[workspace.path] = normalizedEntry
        if (!existing || existing.collapsed !== normalizedEntry.collapsed || existing.hidden !== normalizedEntry.hidden || normalizeRatio(existing.ratio) !== normalizedEntry.ratio) {
          changed = true
        }
      }

      return changed ? next : current
    })
  }, [mergedSidebarWorkspaceEntries])

  useEffect(() => {
    saveStoredValue(DESKTOP_SIDEBAR_LAYOUT_STORAGE_KEY, JSON.stringify(workspaceLayout))
  }, [workspaceLayout])

  const desktopInitialHydrate = useDesktopV3CacheSelector((state) => state.desktopInitialHydrate)
  const selectedDesktopV3Messages = useDesktopV3CacheSelector((state) => (
    routeSessionId ? selectRenderedSessionMessages(state, routeSessionId) : EMPTY_DESKTOP_V3_RENDERED_MESSAGES
  ), desktopV3RenderedMessagesEqual)
  const selectedDesktopV3MessagesLoaded = useDesktopV3CacheSelector((state) => (
    routeSessionId ? isDesktopV3SessionTailReady(state, routeSessionId) : false
  ))
  const selectedDesktopV3LoadedMessageCount = useDesktopV3CacheSelector((state) => (
    routeSessionId ? (state.messagesBySession[routeSessionId]?.items.length ?? 0) : 0
  ))
  const desktopSidebarRows = useDesktopV3CacheSelector(selectDesktopSidebarRows, desktopV3SidebarRowsEqual)
  const desktopStateSessions = useMemo<DesktopSessionRecord[]>(
    () => desktopSidebarRows.map(desktopSessionRecordFromV3SidebarRow),
    [desktopSidebarRows],
  )
  useEffect(() => {
    const sessionId = routeSessionId.trim()
    if (!sessionId) return
    void selectAndHydrateDesktopV3Session(sessionId)
  }, [routeSessionId])

  useEffect(() => {
    if (routeSessionId.trim()) return
    if (!routeWorkspace?.path) return

    dispatchDesktopV3Cache(selectSession(undefined))
  }, [routeSessionId, routeWorkspace?.path])

  const globalSidebarSessionNodes = useMemo(
    () => buildSidebarSessionTree(desktopStateSessions, sidebarNow),
    [desktopStateSessions, sidebarNow],
  )
  const sidebarHideInactiveHours = normalizeSidebarHideInactiveHours(uiSettings?.chat?.sidebar_hide_inactive_hours)
  const filteredSidebarTrees = useMemo(
    () => filterInactiveSidebarSessionTrees(globalSidebarSessionNodes, sidebarNow, sidebarHideInactiveHours, routeSessionId),
    [globalSidebarSessionNodes, routeSessionId, sidebarHideInactiveHours, sidebarNow],
  )
  const globalFlattenedSessionNodes = useMemo(
    () => flattenVisibleSidebarSessionNodes(filteredSidebarTrees.nodes, expandedAgentSessions, routeSessionId),
    [expandedAgentSessions, filteredSidebarTrees.nodes, routeSessionId],
  )
  const visibleSidebarRootIDs = useMemo(() => filteredSidebarTrees.nodes.map((node) => node.session.id), [filteredSidebarTrees.nodes])

  const sessionById = useMemo<Map<string, DesktopSessionRecord>>(
    () => new Map(desktopStateSessions.map((session) => [session.id, session] as const)),
    [desktopStateSessions],
  )
  const workspaceSlugByPath = useMemo(() => buildWorkspaceRouteSlugMap(
    mergedSidebarWorkspaceEntries.map((workspace) => ({
      path: workspace.path,
      workspaceName: workspace.workspaceName,
    })),
  ), [mergedSidebarWorkspaceEntries])
  const selectedSidebarControlWorkspace = sidebarWorkspaceControlPath
    ? mergedSidebarWorkspaceEntries.find((workspace) => workspace.path === sidebarWorkspaceControlPath) ?? null
    : null
  const topWorkspace = selectedSidebarControlWorkspace
    ?? mergedSidebarWorkspaceEntries[0]
    ?? selectedWorkspace
    ?? routeWorkspace
    ?? visibleSidebarWorkspaceEntries[0]
    ?? null
  const topWorkspaceLabel = topWorkspace?.workspaceName?.trim() || 'Default Workspace'
  const topWorkspacePath = topWorkspace?.path || selectedWorkspacePath || ''
  const topWorkspaceSlug = topWorkspacePath
    ? workspaceSlugByPath.get(topWorkspacePath) ?? workspaceRouteSlugBase({ path: topWorkspacePath, workspaceName: topWorkspaceLabel })
    : routeWorkspaceSlug
  const topWorkspaceOptions = useMemo(() => mergedSidebarWorkspaceEntries, [mergedSidebarWorkspaceEntries])
  const defaultNewChatWorkspace = useMemo(() => {
    const defaultPath = currentWorkspacePath?.trim() || ''
    if (defaultPath) {
      return mergedSidebarWorkspaceEntries.find((workspace) => workspace.path === defaultPath)
        ?? buildTemporaryWorkspaceEntry(defaultPath, fallbackWorkspaceNameFromPath(defaultPath))
    }
    return topWorkspace
  }, [currentWorkspacePath, mergedSidebarWorkspaceEntries, topWorkspace])
  const defaultNewChatWorkspacePath = defaultNewChatWorkspace?.path || ''
  const defaultNewChatWorkspaceLabel = defaultNewChatWorkspace?.workspaceName?.trim() || 'Default Workspace'
  const globalSessionWorkspaceSlug = useCallback((session: DesktopSessionRecord): string => {
    const workspacePath = desktopRouteWorkspacePathForSession(session, workspacePathByBindingId, knownWorkspacePaths)
      || selectedWorkspacePath
      || visibleWorkspacePaths[0]
      || ''
    if (!workspacePath) return topWorkspaceSlug || routeWorkspaceSlug || 'workspace'
    return workspaceSlugByPath.get(workspacePath)
      ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: session.workspaceName || fallbackWorkspaceNameFromPath(workspacePath) })
  }, [knownWorkspacePaths, routeWorkspaceSlug, selectedWorkspacePath, topWorkspaceSlug, visibleWorkspacePaths, workspacePathByBindingId, workspaceSlugByPath])
  const globalSessionRouteOptions = useMemo(() => buildDesktopChatRouteOptions({
    hostSwarmName: swarmName,
    workspacePath: topWorkspacePath,
    workspaceName: topWorkspaceLabel,
    topologyRoutes: topWorkspace?.topologyRoutes ?? [],
    localWorkspaceBindingId: topWorkspace?.localWorkspaceBindingId ?? '',
    hostSwarmId: currentSwarmTarget?.swarm_id ?? null,
  }), [currentSwarmTarget?.swarm_id, swarmName, topWorkspace?.localWorkspaceBindingId, topWorkspace?.topologyRoutes, topWorkspaceLabel, topWorkspacePath])

  useEffect(() => {
    if (!routeSessionId) return
    if (activeChatSessionIdRef.current && activeChatSessionIdRef.current !== routeSessionId) {
      setPreviousChatSessionId(activeChatSessionIdRef.current)
    }
    activeChatSessionIdRef.current = routeSessionId
  }, [routeSessionId])

  const routeReadinessStatus = 'idle'
  const routeSessionUnavailable = false

  useEffect(() => {
    if (!selectedWorkspacePath) {
      return
    }

    if (routeSessionId || routeSessionId) {
      setWorkspaceLayout((current) => {
        const currentEntry = current[selectedWorkspacePath]
        if (currentEntry && currentEntry.collapsed === false && currentEntry.hidden !== true) {
          return current
        }
        return {
          ...current,
          [selectedWorkspacePath]: {
            collapsed: false,
            hidden: currentEntry?.hidden ?? false,
            ratio: normalizeRatio(currentEntry?.ratio),
          },
        }
      })
    }
  }, [routeSessionId, routeSessionId, selectedWorkspacePath])

  const handleOpenSearchResult = useCallback((item: DesktopSessionSearchItem) => {
    const sessionId = item.id.trim()
    if (!sessionId) return
    const workspacePath = item.workspace_path?.trim() ?? ''
    const workspaceSlug = workspacePath
      ? workspaceSlugByPath.get(workspacePath) ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: item.workspace_name || 'Workspace' })
      : ''
    if (!workspaceSlug) return
    setSearchModalOpen(false)
    setMobileSidebarOpen(false)
    void selectAndHydrateDesktopV3Session(sessionId)
    void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug, sessionId } })
  }, [navigate, workspaceSlugByPath])

  useEffect(() => {
    if (!routeWorkspaceSlug || routeSessionId || !routeWorkspace?.path) {
      return
    }
    const canonicalWorkspaceSlug = workspaceSlugByPath.get(routeWorkspace.path)
    if (!canonicalWorkspaceSlug || canonicalWorkspaceSlug === routeWorkspaceSlug) {
      return
    }
    void navigate({
      to: '/$workspaceSlug',
      params: { workspaceSlug: canonicalWorkspaceSlug },
      replace: true,
    })
  }, [navigate, routeSessionId, routeWorkspace?.path, routeWorkspaceSlug, workspaceSlugByPath])

  useEffect(() => {
    if (!routeWorkspaceSlug || !routeSessionId) {
      return
    }
    const session = sessionById.get(routeSessionId)
    if (!session) {
      return
    }
    const workspacePath = desktopRouteWorkspacePathForSession(session, workspacePathByBindingId, knownWorkspacePaths)
    if (!workspacePath) {
      return
    }
    const canonicalWorkspaceSlug = workspaceSlugByPath.get(workspacePath)
      ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: session.workspaceName })
    if (!canonicalWorkspaceSlug || canonicalWorkspaceSlug === routeWorkspaceSlug) {
      return
    }
    void navigate({
      to: '/$workspaceSlug/$sessionId',
      params: { workspaceSlug: canonicalWorkspaceSlug, sessionId: session.id },
      replace: true,
    })
  }, [knownWorkspacePaths, navigate, routeSessionId, routeWorkspaceSlug, sessionById, workspacePathByBindingId, workspaceSlugByPath])



  const handleSelectSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    void selectAndHydrateDesktopV3Session(normalizedSessionId)
    const session = sessionById.get(normalizedSessionId)
    if (!session) {
      return false
    }
    const workspacePath = desktopRouteWorkspacePathForSession(session, workspacePathByBindingId, knownWorkspacePaths)
      || selectedWorkspacePath
      || visibleWorkspacePaths[0]
      || ''
    if (!workspacePath) {
      return false
    }
    setMobileSidebarOpen(false)

    const workspaceSlug = workspaceSlugByPath.get(workspacePath)
      ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: session.workspaceName })
    void navigate({
      to: '/$workspaceSlug/$sessionId',
      params: {
        workspaceSlug,
        sessionId: session.id,
      },
    })
    return true
  }, [knownWorkspacePaths, navigate, selectedWorkspacePath, sessionById, visibleWorkspacePaths, workspacePathByBindingId, workspaceSlugByPath])



  const chatWorkspacePath = selectedWorkspace?.path || ''
  const planModalPlan = useDesktopV3CacheSelector((state) => planModal?.sessionId ? (state.plansBySession[planModal.sessionId] ?? null) : null) as DesktopSessionPlanRecord | null
  const planModalRevisions = useDesktopV3CacheSelector((state) => planModal?.sessionId ? (state.planRevisionsBySession[planModal.sessionId] ?? []) : []) as DesktopSessionPlanRevisionRecord[]

  const handleOpenWorkspace = useCallback((wsPath: string, wsName: string) => {
    setMobileSidebarOpen(false)
    const workspaceSlug = workspaceSlugByPath.get(wsPath)
      ?? workspaceRouteSlugBase({ path: wsPath, workspaceName: wsName })
    void navigate({
      to: '/$workspaceSlug',
      params: { workspaceSlug },
    })
  }, [navigate, workspaceSlugByPath])

  const handleStartNewSessionInWorkspace = useCallback((wsPath: string, wsName: string) => {
    dispatchDesktopV3Cache(selectSession(undefined))
    handleOpenWorkspace(wsPath, wsName)
  }, [handleOpenWorkspace])

  const handleArchivePlanSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    const routeSession = normalizedSessionId ? sessionById.get(normalizedSessionId) : null
    const workspacePath = routeSession
      ? desktopRouteWorkspacePathForSession(routeSession, workspacePathByBindingId, knownWorkspacePaths)
        || selectedWorkspacePath
        || routeWorkspace?.path
        || ''
      : selectedWorkspacePath || routeWorkspace?.path || ''
    const workspaceName = routeSession?.workspaceName || selectedWorkspace?.workspaceName || routeWorkspace?.workspaceName || fallbackWorkspaceNameFromPath(workspacePath)
    dispatchDesktopV3Cache(selectSession(undefined))
    if (workspacePath) {
      handleOpenWorkspace(workspacePath, workspaceName)
      return
    }
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
      return
    }
    void navigate({ to: '/' })
  }, [handleOpenWorkspace, knownWorkspacePaths, navigate, routeWorkspace?.path, routeWorkspace?.workspaceName, routeWorkspaceSlug, selectedWorkspace?.workspaceName, selectedWorkspacePath, sessionById, workspacePathByBindingId])

  const handleToggleSidebarPinned = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    const session = normalizedSessionId ? sessionById.get(normalizedSessionId) : null
    if (!session || sidebarSessionActions[normalizedSessionId]) return
    if (!sessionAllowsManualSidebarPin(session)) {
      return
    }
    const nextPinned = !sessionManuallyPinnedInSidebar(session)
    setSidebarSessionActions((current) => ({ ...current, [normalizedSessionId]: 'pin' }))
    void updateAndApplySessionV3DesktopSidebarPinned(normalizedSessionId, nextPinned, session.metadata ?? {})
      .then(() => {
        setDesktopToast({ message: nextPinned ? 'Pinned session to sidebar.' : 'Unpinned session from sidebar.', tone: 'success' })
      })
      .catch((error) => {
        setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to update sidebar pin.', tone: 'error' })
      })
      .finally(() => {
        setSidebarSessionActions((current) => {
          if (current[normalizedSessionId] !== 'pin') return current
          const next = { ...current }
          delete next[normalizedSessionId]
          return next
        })
      })
  }, [sessionById, sidebarSessionActions])

  const handleArchiveSidebarSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId || sidebarSessionActions[normalizedSessionId]) return
    setSidebarSessionActions((current) => ({ ...current, [normalizedSessionId]: 'archive' }))
    void archiveDesktopV3Sessions([normalizedSessionId])
      .then(() => {
        setDesktopToast({ message: 'Archived session.', tone: 'success' })
        if (routeSessionId === normalizedSessionId) {
          handleArchivePlanSession(normalizedSessionId)
        }
      })
      .catch((error) => {
        setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to archive session.', tone: 'error' })
      })
      .finally(() => {
        setSidebarSessionActions((current) => {
          if (current[normalizedSessionId] !== 'archive') return current
          const next = { ...current }
          delete next[normalizedSessionId]
          return next
        })
      })
  }, [handleArchivePlanSession, routeSessionId, sidebarSessionActions])

  const handleRenameSidebarSession = useCallback(async (sessionId: string, title: string): Promise<void> => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId || sidebarSessionActions[normalizedSessionId]) return
    setSidebarSessionActions((current) => ({ ...current, [normalizedSessionId]: 'rename' }))
    try {
      await updateSessionV3Title(normalizedSessionId, title, crypto.randomUUID())
      setDesktopToast({ message: 'Renamed session.', tone: 'success' })
    } finally {
      setSidebarSessionActions((current) => {
        const next = { ...current }
        delete next[normalizedSessionId]
        return next
      })
    }
  }, [sidebarSessionActions])

  const handleToggleSidebarSelected = useCallback((sessionId: string, range: boolean) => {
    const normalized = sessionId.trim()
    if (!normalized) return
    setSelectedSidebarRootIDs((current) => {
      const next = new Set(current)
      if (range && lastSelectedSidebarRootID) {
        const start = visibleSidebarRootIDs.indexOf(lastSelectedSidebarRootID)
        const end = visibleSidebarRootIDs.indexOf(normalized)
        if (start >= 0 && end >= 0) visibleSidebarRootIDs.slice(Math.min(start, end), Math.max(start, end) + 1).forEach((id) => next.add(id))
      } else if (next.has(normalized)) next.delete(normalized)
      else next.add(normalized)
      return next
    })
    setLastSelectedSidebarRootID(normalized)
  }, [lastSelectedSidebarRootID, visibleSidebarRootIDs])

  const handleBulkArchiveSidebar = useCallback(async () => {
    const roots = globalSidebarSessionNodes.filter((node) => selectedSidebarRootIDs.has(node.session.id))
    const ids = Array.from(new Set(roots.flatMap(sidebarNodeSessionIDs)))
    if (ids.length === 0) return
    setBulkArchivePending(true)
    try {
      await archiveDesktopV3Sessions(ids)
      setDesktopToast({ message: `Archived ${roots.length} conversation${roots.length === 1 ? '' : 's'} (${ids.length} sessions).`, tone: 'success' })
      const selectedRouteArchived = ids.includes(routeSessionId)
      setSelectedSidebarRootIDs(new Set())
      setSidebarSelectionMode(false)
      setMobileSidebarOpen(false)
      if (selectedRouteArchived) handleArchivePlanSession(routeSessionId)
    } catch (error) {
      setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to archive selected conversations.', tone: 'error' })
    } finally {
      setBulkArchivePending(false)
    }
  }, [globalSidebarSessionNodes, handleArchivePlanSession, routeSessionId, selectedSidebarRootIDs])

  const handleSidebarThresholdChange = useCallback(async (hours: number | null) => {
    setSidebarThresholdSaving(true)
    try {
      const saved = await saveSidebarHideInactiveHours({ current: uiSettings ?? {}, hours })
      setUISettings(saved)
      queryClient.setQueryData(uiSettingsQueryKey(), saved)
    } catch (error) {
      setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to save sidebar visibility.', tone: 'error' })
    } finally {
      setSidebarThresholdSaving(false)
    }
  }, [queryClient, uiSettings])

  const activeRouteSession = routeSessionId ? (sessionById.get(routeSessionId) ?? null) : null
  const activeRouteSessionCanPin = activeRouteSession ? sessionAllowsManualSidebarPin(activeRouteSession) : false
  const activeRouteSessionIsRegularChat = activeRouteSession ? sessionSidebarRowType(activeRouteSession) === 'single_chat' : false
  const activeRouteSessionActions = routeSessionId
    ? {
        pinned: Boolean(activeRouteSession && activeRouteSessionCanPin && sessionManuallyPinnedInSidebar(activeRouteSession)),
        canPin: Boolean(activeRouteSession && activeRouteSessionIsRegularChat && activeRouteSessionCanPin),
        pendingAction: sidebarSessionActions[routeSessionId] ?? null,
        onTogglePinned: () => {
          if (activeRouteSession && activeRouteSessionIsRegularChat && activeRouteSessionCanPin) {
            handleToggleSidebarPinned(activeRouteSession.id)
          }
        },
        onArchive: () => handleArchiveSidebarSession(routeSessionId),
      }
    : null

  const openWorktreeSessionModal = useCallback((input: {
    workspace: WorkspaceEntry
    workspaceSlug: string
    routeOptions: DesktopChatRoute[]
  }) => {
    const workspacePath = input.workspace.path
    setWorktreeSessionModal({
      workspacePath,
      workspaceName: input.workspace.workspaceName,
      workspaceSlug: input.workspaceSlug,
      routeOptions: input.routeOptions,
      branchPrefix: '',
      managedWorktrees: [],
      settingsLoading: true,
    })
    setWorktreeSessionTitle('')
    setWorktreeSessionBranch('')
    setWorktreeSessionExistingPath('')
    setWorktreeSessionError(null)
    void fetchWorktreeSessionSettings(workspacePath)
      .then(({ branchPrefix, managedWorktrees }) => {
        setWorktreeSessionModal((current) => current?.workspacePath === workspacePath
          ? { ...current, branchPrefix, managedWorktrees, settingsLoading: false }
          : current)
      })
      .catch((error) => {
        setWorktreeSessionModal((current) => current?.workspacePath === workspacePath
          ? { ...current, settingsLoading: false }
          : current)
        setWorktreeSessionError(error instanceof Error ? error.message : 'Failed to load worktree settings')
      })
  }, [])

  const closeWorktreeSessionModal = useCallback(() => {
    if (worktreeSessionCreating) return
    setWorktreeSessionModal(null)
    setWorktreeSessionError(null)
  }, [worktreeSessionCreating])

  const handleCreateWorktreeSession = useCallback(async () => {
    if (!worktreeSessionModal || worktreeSessionCreating) return
    const title = worktreeSessionTitle.trim()
    const existingPath = worktreeSessionExistingPath.trim()
    const existingWorktree = existingPath ? worktreeSessionModal.managedWorktrees.find((item) => item.path === existingPath) ?? null : null
    const branchSuffix = normalizeWorktreeBranchSuffix(worktreeSessionBranch)
    const branchPrefix = normalizeWorktreeBranchPrefix(worktreeSessionModal.branchPrefix)
    const branch = existingWorktree?.branch ?? composeWorktreeBranchName(branchPrefix, branchSuffix)
    if (worktreeSessionModal.settingsLoading) {
      setWorktreeSessionError('Worktree settings are still loading.')
      return
    }
    if (!branchPrefix) {
      setWorktreeSessionError('Worktree settings did not return a branch prefix.')
      return
    }
    if (!title) {
      setWorktreeSessionError('Title is required.')
      return
    }
    if (existingPath && !existingWorktree) {
      setWorktreeSessionError('Selected worktree is no longer available.')
      return
    }
    if (!existingWorktree && !branchSuffix) {
      setWorktreeSessionError('Branch suffix is required.')
      return
    }
    const selectedRoute = worktreeSessionModal.routeOptions.find((route) => getDesktopSessionCreateTarget(route).endpoint === '/v3/sessions') ?? null
    if (!selectedRoute) {
      setWorktreeSessionError('No writable self/host Desktop V3 route is available for this workspace.')
      return
    }
    const activeAgent = agentStateQuery.data?.activePrimary?.trim() || 'swarm'
    const preference = draftPreferenceQuery.data?.preference
    if (!preference?.provider?.trim() || !preference.model?.trim() || !preference.thinking?.trim()) {
      setWorktreeSessionError('Select a default provider, model, and thinking level before creating a worktree session.')
      return
    }
    setWorktreeSessionCreating(true)
    setWorktreeSessionError(null)
    try {
      const operation = createDesktopV3CreateOnlySessionOperation({
        workspacePath: worktreeSessionModal.workspacePath,
        workspaceName: worktreeSessionModal.workspaceName,
        route: selectedRoute,
        title,
        mode: 'auto',
        agentName: activeAgent,
        preference: {
          provider: preference.provider,
          model: preference.model,
          thinking: preference.thinking,
          serviceTier: preference.serviceTier,
          contextMode: preference.contextMode,
        },
        sessionMetadata: {
          source: 'desktop-v3',
          workspace_path: worktreeSessionModal.workspacePath,
        },
        worktree: { mode: 'on', branchName: branch, existingPath: existingWorktree?.path },
      })
      await startDesktopV3CreateOnlySession({
        operation,
        onSessionStarted: (sessionId) => {
          void navigate({
            to: '/$workspaceSlug/$sessionId',
            params: { workspaceSlug: worktreeSessionModal.workspaceSlug, sessionId },
          })
        },
      })
      setWorktreeSessionModal(null)
      setMobileSidebarOpen(false)
      setDesktopToast({ message: `Created worktree session on ${branch}`, tone: 'success' })
    } catch (error) {
      setWorktreeSessionError(error instanceof Error ? error.message : String(error))
    } finally {
      setWorktreeSessionCreating(false)
    }
  }, [agentStateQuery.data?.activePrimary, draftPreferenceQuery.data?.preference, navigate, worktreeSessionBranch, worktreeSessionCreating, worktreeSessionExistingPath, worktreeSessionModal, worktreeSessionTitle])

  const openPlanModalForSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) return
    setPlanModal({ sessionId: normalizedSessionId })
    setPlanModalLoading(true)
    setPlanModalError(null)
    void fetchAndApplyDesktopV3PlanSnapshot(normalizedSessionId)
      .catch((error) => setPlanModalError(error instanceof Error ? error.message : String(error)))
      .finally(() => setPlanModalLoading(false))
  }, [])

  const handleCopyPlanText = useCallback(async (text: string): Promise<boolean> => {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      return false
    }
  }, [])

  const handleRestorePlanRevisionModal = useCallback(async (revision: DesktopSessionPlanRevisionRecord, input: DesktopPlanRecoveryInput = {}) => {
    const sessionId = planModal?.sessionId.trim() ?? ''
    if (!sessionId || !planModalPlan?.id) return
    setPlanModalSaving(true)
    setPlanModalError(null)
    try {
      const payload = {
        planId: planModalPlan.id,
        version: revision.version,
        checkpointId: input.checkpointId,
        executionGranularity: input.executionGranularity,
        continuationPolicy: input.continuationPolicy,
        continueAutomatically: input.continueAutomatically,
        restart: input.restart,
        start: input.start,
        skipPrior: input.skipPrior,
      }
      if (input.skipPrior) {
        await jumpDesktopPlanToRevisionCheckpoint(sessionId, payload)
      } else if (input.start || input.restart || input.checkpointId) {
        await restartDesktopPlanFromRevision(sessionId, payload)
      } else {
        await restoreDesktopPlanRevision(sessionId, payload)
      }
      await fetchAndApplyDesktopV3PlanSnapshot(sessionId)
      if (input.start) setPlanModal(null)
    } catch (error) {
      setPlanModalError(error instanceof Error ? error.message : String(error))
      throw error
    } finally {
      setPlanModalSaving(false)
    }
  }, [planModal?.sessionId, planModalPlan?.id])

  const handleApproveStartPlanModal = useCallback(async (input: { checkpointId?: string; executionGranularity: 'checkpointed' | 'run_through'; continueAutomatically: boolean; continuationPolicy: 'automatic' | 'review_each_checkpoint' }) => {
    const sessionId = planModal?.sessionId.trim() ?? ''
    if (!sessionId || !planModalPlan?.id) return
    setPlanModalExecuting(true)
    setPlanModalError(null)
    try {
      if (input.executionGranularity === 'run_through') {
        await startDesktopPlanAutomatic(sessionId, planModalPlan.id, {
          checkpointId: input.checkpointId,
          executionGranularity: input.executionGranularity,
          continuationPolicy: input.continuationPolicy,
          continueAutomatically: input.continueAutomatically,
        })
      } else {
        await startDesktopPlanCheckpointed(sessionId, planModalPlan.id, {
          checkpointId: input.checkpointId,
          executionGranularity: input.executionGranularity,
          continuationPolicy: input.continuationPolicy,
          continueAutomatically: input.continueAutomatically,
        })
      }
      setPlanModal(null)
    } catch (error) {
      setPlanModalError(error instanceof Error ? error.message : String(error))
      throw error
    } finally {
      setPlanModalExecuting(false)
    }
  }, [planModal?.sessionId, planModalPlan?.id])

  const handleOpenSettingsTab = useCallback((tab: SettingsTabID) => {
    setQuickSettingsTab(null)
    setQuickActionsOpen(false)
    setMobileSidebarOpen(false)
    const search = { tab, ...(routeSessionId ? { returnSessionId: routeSessionId } : {}) }
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/settings', params: { workspaceSlug: routeWorkspaceSlug }, search })
      return
    }
    void navigate({ to: '/settings', search })
  }, [navigate, routeSessionId, routeWorkspaceSlug])

  const handleSlashCommand = useCallback((command: DesktopSlashCommand) => {
    const action = command.action
    switch (action.kind) {
      case 'open-settings':
        handleOpenSettingsTab(action.tab)
        return
      case 'open-quick-settings':
        setQuickSettingsTab(action.tab)
        setMobileSidebarOpen(false)
        return
      case 'open-permissions':
        setQuickSettingsTab('permissions')
        setMobileSidebarOpen(false)
        return
      case 'open-workspace-launcher':
        setMobileSidebarOpen(true)
        void navigate({ to: '/' })
        return
      case 'open-commit-modal': {
        const workspacePath = selectedWorkspace?.path || selectedWorkspacePath || ''
        const workspaceName = selectedWorkspace?.workspaceName || fallbackWorkspaceNameFromPath(workspacePath)
        if (workspacePath) openGitPanel(workspacePath, workspaceName)
        return
      }
      case 'open-plan-modal':
        if (routeSessionId) openPlanModalForSession(routeSessionId)
        else setDesktopToast({ message: 'Open an existing session to view its plan.', tone: 'info' })
        return
      case 'open-quick-actions':
        setQuickActionsOpen(true)
        setMobileSidebarOpen(false)
        setDesktopToast({ message: 'Desktop shortcuts differ from TUI keybindings. Open Settings → Shortcuts for the Desktop list.', tone: 'info' })
        return
      case 'new-session': {
        const session = routeSessionId ? sessionById.get(routeSessionId) : null
        const workspacePath = session?.workspacePath || selectedWorkspace?.path || selectedWorkspacePath || ''
        const workspaceName = session?.workspaceName || selectedWorkspace?.workspaceName || fallbackWorkspaceNameFromPath(workspacePath)
        if (workspacePath) handleStartNewSessionInWorkspace(workspacePath, workspaceName)
        return
      }
      case 'show-help':
        setDesktopToast({ message: 'Slash commands: use ↑/↓ to choose, Enter to run, Tab to insert.', tone: 'info' })
        return
      case 'open-model-picker':
      case 'toggle-fast':
      case 'compact-session':
        return
      default: {
        const _exhaustive: never = action
        return _exhaustive
      }
    }
  }, [handleOpenSettingsTab, handleStartNewSessionInWorkspace, openGitPanel, openPlanModalForSession, routeSessionId, selectedWorkspace?.path, selectedWorkspace?.workspaceName, selectedWorkspacePath, sessionById])

  const latestNeedsApprovalSession = useMemo(() => {
    return desktopStateSessions
      .filter((session) => sessionHasPendingPermission(session))
      .sort((left, right) => right.updatedAt - left.updatedAt)[0] ?? null
  }, [desktopStateSessions])

  const handleOpenLatestNeedsApproval = useCallback(() => {
    if (latestNeedsApprovalSession && handleSelectSession(latestNeedsApprovalSession.id)) {
      return
    }
    setDesktopToast({ message: 'No session currently needs approval.', tone: 'info' })
  }, [handleSelectSession, latestNeedsApprovalSession])

  const handleOpenPreviousChat = useCallback(() => {
    if (previousChatSessionId && handleSelectSession(previousChatSessionId)) {
      return
    }
    setDesktopToast({ message: 'No previous chat is available in this window yet.', tone: 'info' })
  }, [handleSelectSession, previousChatSessionId])

  const handleOpenSearchChats = useCallback(() => {
    setSearchModalOpen(true)
    setMobileSidebarOpen(false)
    setQuickActionsOpen(false)
  }, [])

  const handleOpenQuickActions = useCallback(() => {
    setQuickActionsOpen(true)
    setMobileSidebarOpen(false)
  }, [])

  const canStartNewSession = Boolean(topWorkspacePath)
  const canReturnToPreviousChat = Boolean(previousChatSessionId && sessionById.has(previousChatSessionId))
  useEffect(() => {
    function desktopShortcutMatches(event: KeyboardEvent, key: string): boolean {
      const normalizedKey = event.key.toLowerCase()
      return (event.metaKey || event.ctrlKey) && event.altKey && !event.shiftKey && normalizedKey === key
    }

    function shortcutTargetIsChatComposer(target: EventTarget | null): boolean {
      if (!(target instanceof HTMLElement)) return false
      return Boolean(target.closest('[data-testid="desktop-v3-agentic-composer"]'))
    }

    function shortcutTargetBlocksDesktopShortcuts(target: EventTarget | null): boolean {
      if (!(target instanceof HTMLElement)) return false
      if (target.closest('[role="dialog"]')) return true
      if (target.isContentEditable) return true
      return Boolean(target.closest('input, textarea, select, [contenteditable="true"]'))
    }

    function handleDesktopShortcut(event: KeyboardEvent) {
      if (event.defaultPrevented) return
      const target = event.target
      const element = target instanceof HTMLElement ? target : null
      const insideDialog = Boolean(element?.closest('[role="dialog"]'))
      const insideChatComposer = shortcutTargetIsChatComposer(target)
      const targetBlocksDesktopShortcuts = shortcutTargetBlocksDesktopShortcuts(target)
      if (desktopShortcutMatches(event, 'n') && (!targetBlocksDesktopShortcuts || insideChatComposer)) {
        event.preventDefault()
        if (topWorkspacePath) handleStartNewSessionInWorkspace(topWorkspacePath, topWorkspaceLabel)
        return
      }
      if (desktopShortcutMatches(event, 'f') && (!targetBlocksDesktopShortcuts || insideChatComposer)) {
        event.preventDefault()
        handleOpenSearchChats()
        return
      }
      if (desktopShortcutMatches(event, 'a') && (!targetBlocksDesktopShortcuts || insideChatComposer)) {
        event.preventDefault()
        handleOpenLatestNeedsApproval()
        return
      }
      if (desktopShortcutMatches(event, 'm') && (!targetBlocksDesktopShortcuts || insideChatComposer)) {
        event.preventDefault()
        setSessionModeCommand('toggle-plan-auto')
        return
      }
      if (desktopShortcutMatches(event, 'p') && (!targetBlocksDesktopShortcuts || insideChatComposer)) {
        event.preventDefault()
        handleOpenPreviousChat()
        return
      }
      if (targetBlocksDesktopShortcuts) {
        return
      }
      if (desktopShortcutMatches(event, 'k') && !insideDialog) {
        event.preventDefault()
        handleOpenQuickActions()
        return
      }
      if (desktopShortcutMatches(event, 's')) {
        event.preventDefault()
        handleOpenSettingsTab('shortcuts')
        return
      }
    }

    window.addEventListener('keydown', handleDesktopShortcut)
    return () => window.removeEventListener('keydown', handleDesktopShortcut)
  }, [handleOpenLatestNeedsApproval, handleOpenPreviousChat, handleOpenQuickActions, handleOpenSearchChats, handleOpenSettingsTab, handleStartNewSessionInWorkspace, topWorkspaceLabel, topWorkspacePath])

  const quickActions = useMemo<DesktopQuickActionItem[]>(() => [
    {
      id: 'quick-actions',
      label: 'Open quick actions',
      description: 'Show Desktop shortcut actions and run the supported ones from one modal.',
      keys: ['⌘/Ctrl', 'Alt', 'K'],
      availability: 'Available anywhere in Desktop unless another modal or text field owns the shortcut.',
      enabled: true,
      icon: Keyboard,
      onRun: handleOpenQuickActions,
    },
    {
      id: 'new-session',
      label: 'New session',
      description: 'Start a fresh chat in the current or top selected workspace.',
      keys: ['⌘/Ctrl', 'Alt', 'N'],
      availability: 'Requires a selected workspace.',
      enabled: canStartNewSession,
      disabledReason: 'Select a workspace before starting a new session.',
      icon: Plus,
      onRun: () => {
        if (topWorkspacePath) handleStartNewSessionInWorkspace(topWorkspacePath, topWorkspaceLabel)
        setQuickActionsOpen(false)
      },
    },
    {
      id: 'settings',
      label: 'Open settings',
      description: 'Open Desktop Settings, preserving the current workspace route when possible.',
      keys: ['⌘/Ctrl', 'Alt', 'S'],
      availability: 'Available anywhere in Desktop.',
      enabled: true,
      icon: Settings,
      onRun: () => handleOpenSettingsTab('shortcuts'),
    },
    {
      id: 'search-chats',
      label: 'Search chats',
      description: 'Open Desktop chat search.',
      keys: ['⌘/Ctrl', 'Alt', 'F'],
      availability: 'Available anywhere in Desktop.',
      enabled: true,
      icon: Search,
      onRun: handleOpenSearchChats,
    },
    {
      id: 'latest-needs-approval',
      label: 'Latest needs approval',
      description: 'Jump to the newest visible chat that has a pending permission request.',
      keys: ['⌘/Ctrl', 'Alt', 'A'],
      availability: 'Requires a session with pending permissions in the sidebar.',
      enabled: Boolean(latestNeedsApprovalSession),
      disabledReason: 'No session currently needs approval.',
      icon: Bell,
      onRun: () => {
        setQuickActionsOpen(false)
        handleOpenLatestNeedsApproval()
      },
    },
    {
      id: 'previous-chat',
      label: 'Previous chat',
      description: 'Return to the previously selected Desktop chat in this window.',
      keys: ['⌘/Ctrl', 'Alt', 'P'],
      availability: 'Available after switching between chats in the same Desktop window.',
      enabled: canReturnToPreviousChat,
      disabledReason: 'Switch chats once before using previous chat.',
      icon: ChevronLeft,
      onRun: () => {
        setQuickActionsOpen(false)
        handleOpenPreviousChat()
      },
    },
    {
      id: 'toggle-plan-auto',
      label: 'Toggle plan/auto mode',
      description: 'Switch the active chat composer between plan and auto mode.',
      keys: ['⌘/Ctrl', 'Alt', 'M'],
      availability: 'Requires an active Desktop chat route.',
      enabled: Boolean(routeSessionId || (routeWorkspaceSlug && routeWorkspace?.path)),
      disabledReason: 'Open a Desktop chat composer to toggle plan/auto mode.',
      icon: CheckCircle2,
      onRun: () => {
        if (routeSessionId || (routeWorkspaceSlug && routeWorkspace?.path)) {
          setSessionModeCommand('toggle-plan-auto')
          setQuickActionsOpen(false)
        }
      },
    },
  ], [canReturnToPreviousChat, canStartNewSession, handleOpenLatestNeedsApproval, handleOpenPreviousChat, handleOpenQuickActions, handleOpenSearchChats, handleOpenSettingsTab, handleStartNewSessionInWorkspace, latestNeedsApprovalSession, routeSessionId, routeWorkspace?.path, routeWorkspaceSlug, topWorkspaceLabel, topWorkspacePath])


  const runDesktopUpdate = useCallback(async () => {
    setUpdateRunning(true)
    setUpdateProgress({ open: true, job: null, startedAt: Date.now() })
    try {
      const initialJob = await startDesktopUpdate()
      setUpdateProgress((current) => ({ ...current, job: initialJob }))
      const startedAt = Date.now()
      let sawBackendDrop = false
      while (Date.now() - startedAt < 30 * 60_000) {
        await new Promise((resolve) => window.setTimeout(resolve, sawBackendDrop ? 1500 : 800))
        try {
          const job = await fetchDesktopUpdateJob()
          setUpdateProgress((current) => ({ ...current, job }))
          if (job.status === 'failed') {
            throw new Error(`Update failed: ${job.error || job.message || 'unknown error'}`)
          }
          if (job.status === 'running') {
            continue
          }
          const toast = { message: updateCompleteToastMessage(job), tone: 'success' } satisfies DesktopToastState
          setDesktopToast(toast)
          savePendingDesktopToast(toast)
          window.setTimeout(() => window.location.reload(), 900)
          return
        } catch (error) {
          sawBackendDrop = true
          if (error instanceof Error && /update failed/i.test(error.message)) {
            throw error
          }
        }
      }
      throw new Error('Update is still running. Leave this window open and check again shortly.')
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Update failed'
      setUpdateError(message)
      setDesktopToast({ message, tone: 'error' })
      setUpdateProgress((current) => ({
        ...current,
        open: true,
        job: current.job ? { ...current.job, status: 'failed', error: message } : {
          id: '',
          kind: updateDevMode ? 'dev' : 'release',
          status: 'failed',
          error: message,
        },
      }))
    } finally {
      setUpdateRunning(false)
    }
  }, [updateDevMode])

  const handleDesktopUpdate = useCallback(async () => {
    if (updateRunning) {
      return
    }
    setUpdateError(null)
    let status = updateStatus
    try {
      status = await updateStatusQuery.refetch().then((result) => result.data ?? status)
    } catch {
      // React Query stores the error; keep the current cached status if present.
    }
    if (!status) {
      const message = updateStatusError
        ? `Update status unavailable: ${updateStatusError}`
        : 'No Swarm update is available yet.'
      setUpdateError(message)
      return
    }
    const devRebuild = status.dev_mode === true
    if (!devRebuild && status.update_available !== true) {
      const message = status.suppressed
        ? 'Updates are not available for this build.'
        : status.error?.trim()
          ? `Update status unavailable: ${status.error}`
          : updateStatusError
            ? `Update status unavailable: ${updateStatusError}`
            : status.latest_version?.trim()
              ? `Swarm is already up to date (${status.latest_version.trim()}).`
              : 'No Swarm update is available yet.'
      setUpdateError(message)
      return
    }
    await runDesktopUpdate()
  }, [runDesktopUpdate, updateRunning, updateStatus, updateStatusError, updateStatusQuery])

  const handleCloseUpdateProgress = useCallback(() => {
    if (updateRunning) {
      return
    }
    setUpdateProgress((current) => ({ ...current, open: false }))
  }, [updateRunning])

  const handleOpenMobileSidebar = useCallback(() => {
    setSidebarCollapsed(false)
    setMobileSidebarOpen(true)
  }, [])

  const handleMobileSidebarTouchStart = useCallback((event: React.TouchEvent<HTMLDivElement>) => {
    if (event.touches.length !== 1) {
      mobileSidebarSwipeRef.current = null
      return
    }
    const touch = event.touches[0]
    if (!touch) {
      mobileSidebarSwipeRef.current = null
      return
    }
    if (!mobileSidebarOpen && touch.clientX > MOBILE_SIDEBAR_SWIPE_EDGE_PX) {
      mobileSidebarSwipeRef.current = null
      return
    }
    mobileSidebarSwipeRef.current = {
      startX: touch.clientX,
      startY: touch.clientY,
      tracking: true,
      completed: false,
      mode: mobileSidebarOpen ? 'close' : 'open',
    }
  }, [mobileSidebarOpen])

  const handleMobileSidebarTouchMove = useCallback((event: React.TouchEvent<HTMLDivElement>) => {
    const swipe = mobileSidebarSwipeRef.current
    const touch = event.touches[0]
    if (!swipe?.tracking || !touch || swipe.completed) {
      return
    }
    const deltaX = touch.clientX - swipe.startX
    const absDeltaX = Math.abs(deltaX)
    const deltaY = Math.abs(touch.clientY - swipe.startY)
    if (deltaY > MOBILE_SIDEBAR_SWIPE_MAX_Y_PX && deltaY > absDeltaX) {
      mobileSidebarSwipeRef.current = null
      return
    }
    if (swipe.mode === 'open' && deltaX >= MOBILE_SIDEBAR_SWIPE_MIN_X_PX && deltaY <= MOBILE_SIDEBAR_SWIPE_MAX_Y_PX) {
      swipe.completed = true
      handleOpenMobileSidebar()
      return
    }
    if (swipe.mode === 'close' && absDeltaX >= MOBILE_SIDEBAR_SWIPE_MIN_X_PX && deltaY <= MOBILE_SIDEBAR_SWIPE_MAX_Y_PX) {
      swipe.completed = true
      setMobileSidebarOpen(false)
    }
  }, [handleOpenMobileSidebar])

  const handleMobileSidebarTouchEnd = useCallback(() => {
    mobileSidebarSwipeRef.current = null
  }, [])

  const handlePrefetchSession = useCallback((sessionId: string) => {
    void sessionId
  }, [])

  const handleToggleAgentSessions = useCallback((sessionId: string) => {
    setExpandedAgentSessions((current) => ({
      ...current,
      [sessionId]: !current[sessionId],
    }))
  }, [])

  const mobileSessionQuickMenu = routeWorkspace?.path ? (
    <Card className="flex min-h-0 w-full flex-1 flex-col border-[var(--app-border)] bg-[var(--app-surface)] p-4 shadow-sm">
      <div className="grid min-h-0 flex-1 content-start gap-3 overflow-y-auto pr-1 [-webkit-overflow-scrolling:touch]">
        <div className="flex items-center gap-2 text-xs text-[var(--app-text-subtle)]">
          <button type="button" className="text-[var(--app-primary)]" onClick={() => { setSidebarSelectionMode((value) => !value); setSelectedSidebarRootIDs(new Set()) }}>{sidebarSelectionMode ? 'Done selecting' : 'Select chats'}</button>
          {filteredSidebarTrees.hiddenCount > 0 ? <button type="button" className="ml-auto text-[var(--app-primary)]" onClick={handleOpenSearchChats}>{filteredSidebarTrees.hiddenCount} hidden · Search</button> : null}
        </div>
        {sidebarSelectionMode ? (
          <div className="sticky top-0 z-10 flex items-center gap-2 rounded-md bg-[var(--app-surface-elevated)] px-2 py-2 text-xs shadow-sm">
            <span>{selectedSidebarRootIDs.size} selected</span>
            <button type="button" className="ml-auto" onClick={() => setSelectedSidebarRootIDs(new Set())}>Clear</button>
            <button type="button" disabled={bulkArchivePending || selectedSidebarRootIDs.size === 0} className="rounded bg-[var(--app-primary)] px-2 py-1 text-[var(--app-background)] disabled:opacity-50" onClick={() => { void handleBulkArchiveSidebar() }}>Archive selected</button>
          </div>
        ) : null}
        {renderSidebarSessionGroups({
          nodes: globalFlattenedSessionNodes,
          routeSessionId,
          now: sidebarNow,
          fallbackSwarmName: swarmName,
          routeOptions: globalSessionRouteOptions,
          workspaceSlug: globalSessionWorkspaceSlug,
          expandedAgentSessions,
          compactingSession,
          pendingActions: sidebarSessionActions,
          selectionMode: sidebarSelectionMode,
          selectedRootIDs: selectedSidebarRootIDs,
          onSelect: handleSelectSession,
          onToggleSelected: handleToggleSidebarSelected,
          onPrefetch: handlePrefetchSession,
          onToggleAgents: handleToggleAgentSessions,
          onTogglePinned: handleToggleSidebarPinned,
          onArchive: handleArchiveSidebarSession,
          onRename: handleRenameSidebarSession,
        }) ?? (
          <div className="rounded-xl border border-dashed border-[var(--app-border)] px-3 py-4 text-center text-xs text-[var(--app-text-subtle)]">
            No active chats yet.
          </div>
        )}
      </div>
    </Card>
  ) : null

  const handleWorkspaceSelect = useCallback((event: ChangeEvent<HTMLSelectElement>) => {
    const workspacePath = event.target.value.trim()
    if (!workspacePath) return
    const workspace = mergedSidebarWorkspaceEntries.find((entry) => entry.path === workspacePath)
    if (!workspace) return
    setSidebarWorkspaceControlPath(workspace.path)
  }, [mergedSidebarWorkspaceEntries])

  useEffect(() => {
    setMobileSidebarOpen(false)
  }, [routeSessionId, routeWorkspaceSlug])

  const handleCompactingSessionChange = useCallback((sessionId: string, startedAt: number | null) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) return
    setCompactingSession((current) => {
      if (startedAt === null) {
        return current?.sessionId === normalizedSessionId ? null : current
      }
      return { sessionId: normalizedSessionId, startedAt }
    })
  }, [])

  const sidebarContent = (
    <>
      {sidebarCollapsed ? (
        <div className="flex h-full flex-col items-center gap-1 py-3">
          <Button variant="ghost" className="h-12 w-12 min-w-12 p-0" onClick={() => setSidebarCollapsed(false)} aria-label="Expand sidebar">
            <ChevronRight size={28} className="shrink-0" />
          </Button>
          <Button variant="ghost" className="h-12 w-12 min-w-12 p-0" onClick={() => void navigate({ to: '/' })} aria-label="Back to launcher">
            <Folder size={24} className="shrink-0" />
          </Button>
          {notificationAttentionVisible ? (
            <Button variant="ghost" className={cn('relative h-12 w-12 min-w-12 p-0', notificationUnreadCount > 0 && 'text-[var(--app-primary)]')} onClick={handleOpenNotifications} aria-label="Open notifications" title={notificationUnreadCount > 0 ? `${notificationUnreadCount} unread notification${notificationUnreadCount === 1 ? '' : 's'}` : 'Notifications'}>
              <Bell size={24} className="shrink-0" />
              {notificationUnreadCount > 0 ? <span aria-hidden="true" className="absolute right-2 top-2 grid h-4 min-w-4 place-items-center rounded-full bg-[var(--app-primary)] px-1 text-[9px] font-semibold text-[var(--app-background)]">{notificationUnreadCount > 99 ? '99+' : notificationUnreadCount}</span> : null}
            </Button>
          ) : null}
          {updateAttentionVisible ? (
            <Button variant="ghost" className="relative h-12 w-12 min-w-12 p-0" onClick={() => { void handleDesktopUpdate() }} aria-label={updateActionLabel} title={updateActionTitle} disabled={updateRunning || !updateActionEnabled}>
              <Download size={24} className={cn('shrink-0', updateRunning && 'animate-pulse', updateActionEnabled && 'text-[var(--app-primary)]', updateError && 'text-[var(--app-error)]')} />
              {updateActionEnabled ? <span aria-hidden="true" className="absolute right-2 top-2 h-2.5 w-2.5 rounded-full bg-[var(--app-primary)] shadow-[0_0_10px_var(--app-primary)]" /> : null}
            </Button>
          ) : null}
        </div>
      ) : (
        <div className="flex h-full flex-col min-h-0">
          <div className="font-mono">
            <div className="grid h-[60px] items-center border-b border-[var(--app-border)] bg-[var(--app-surface)] pl-[13px] pr-0">
                <div className={headerActionRowClass}>
                  <div className="min-w-0">
                    {editingSidebarSwarmName ? (
                      <form
                        className="grid min-w-0 gap-1"
                        onSubmit={(event) => {
                          event.preventDefault()
                          void handleSaveSidebarSwarmName()
                        }}
                      >
                        <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_24px_24px] items-center gap-1">
                          <input
                            value={sidebarSwarmNameDraft}
                            onChange={(event) => setSidebarSwarmNameDraft(event.target.value)}
                            disabled={sidebarSwarmNameSaving}
                            autoFocus
                            aria-label="Swarm name"
                            className="h-7 min-w-0 rounded-md border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-2 text-[13px] font-semibold tracking-[-0.035em] text-[var(--app-text)] outline-none focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
                          />
                          <button
                            type="submit"
                            className={cn(SIDEBAR_ACTION_BUTTON_CLASS, 'text-[var(--app-success)]')}
                            disabled={sidebarSwarmNameSaving || !sidebarSwarmNameDirty}
                            aria-label="Save swarm name"
                            title="Save swarm name"
                          >
                            {sidebarSwarmNameSaving ? <LoaderCircle size={14} strokeWidth={1.8} className="animate-spin" /> : <Check size={14} strokeWidth={1.8} />}
                          </button>
                          <button
                            type="button"
                            className={SIDEBAR_ACTION_BUTTON_CLASS}
                            onClick={handleCancelSidebarSwarmNameEdit}
                            disabled={sidebarSwarmNameSaving}
                            aria-label="Cancel swarm name edit"
                            title="Cancel"
                          >
                            <X size={14} strokeWidth={1.8} />
                          </button>
                        </div>
                        {sidebarSwarmNameError ? (
                          <div className="truncate text-[10px] leading-[1.1] text-[var(--app-error)]" title={sidebarSwarmNameError}>{sidebarSwarmNameError}</div>
                        ) : null}
                      </form>
                    ) : (
                      <button
                        type="button"
                        className="min-w-0 truncate rounded-md text-left text-[15px] font-semibold tracking-[-0.035em] text-[var(--app-text)] hover:text-[var(--app-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
                        onClick={handleStartSidebarSwarmNameEdit}
                        aria-label="Edit swarm name"
                        title="Click to rename swarm"
                      >
                        {swarmName}
                      </button>
                    )}
                    <div className="mt-px truncate text-[10px] leading-[1.25] text-[var(--app-text-subtle)]">
                      <strong className="font-medium text-[var(--app-text-muted)]">{currentSwarmRoleLabel}</strong> · {masterWorkspaceName}
                    </div>
                  </div>
                  <SidebarActionRail className={headerActionRailClass}>
                    {notificationAttentionVisible ? (
                      <button
                        type="button"
                        className={cn(
                          SIDEBAR_ACTION_BUTTON_CLASS,
                          'relative text-[var(--app-text-subtle)]',
                          notificationUnreadCount > 0 && 'text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] hover:text-[var(--app-primary-hover)]',
                        )}
                        onClick={handleOpenNotifications}
                        aria-label="Open notifications"
                        title={notificationUnreadCount > 0 ? `${notificationUnreadCount} unread notification${notificationUnreadCount === 1 ? '' : 's'}` : 'Notifications'}
                      >
                        <Bell size={14} strokeWidth={1.8} className="shrink-0" />
                        {notificationUnreadCount > 0 ? <span aria-hidden="true" className="absolute right-0.5 top-0.5 grid h-3 min-w-3 place-items-center rounded-full bg-[var(--app-primary)] px-0.5 text-[7px] font-semibold leading-none text-[var(--app-background)]">{notificationUnreadCount > 9 ? '9+' : notificationUnreadCount}</span> : null}
                      </button>
                    ) : null}
                    {updateAttentionVisible ? (
                      <button
                        type="button"
                        className={cn(
                          SIDEBAR_ACTION_BUTTON_CLASS,
                          'relative text-[var(--app-text-subtle)]',
                          updateActionEnabled && 'text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] hover:text-[var(--app-primary-hover)]',
                          updateRunning && 'cursor-progress text-[var(--app-primary)]',
                          updateError && 'text-[var(--app-error)] hover:text-[var(--app-error)]',
                        )}
                        onClick={() => { void handleDesktopUpdate() }}
                        aria-busy={updateRunning}
                        aria-label={updateActionLabel}
                        disabled={updateRunning || !updateActionEnabled}
                        title={updateActionTitle}
                      >
                        <Download size={14} strokeWidth={1.8} className={cn('shrink-0', updateRunning && 'animate-pulse')} />
                        {updateActionEnabled ? <span aria-hidden="true" className="absolute right-0.5 top-0.5 h-1.5 w-1.5 rounded-full bg-[var(--app-primary)] shadow-[0_0_8px_var(--app-primary)]" /> : null}
                      </button>
                    ) : null}
                    <button
                      type="button"
                      className={cn(SIDEBAR_ACTION_BUTTON_CLASS, 'text-[var(--app-text-subtle)]')}
                      onClick={() => setSidebarCollapsed(true)}
                      aria-label="Collapse sidebar"
                      title="Collapse"
                    >
                      <ChevronLeft size={14} strokeWidth={1.8} className="shrink-0" />
                    </button>
                  </SidebarActionRail>
                </div>
            </div>

            <div className="border-b border-[var(--app-border)] bg-[var(--app-surface)] px-[9px] py-2">
              <div className="grid gap-0.5 text-[11px] text-[var(--app-text-subtle)]">
                  <div className="grid gap-0.5 pt-1">
                    <button
                      type="button"
                      className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)] disabled:cursor-not-allowed disabled:opacity-50"
                      onClick={() => {
                        if (defaultNewChatWorkspacePath) {
                          handleStartNewSessionInWorkspace(defaultNewChatWorkspacePath, defaultNewChatWorkspaceLabel)
                        }
                        setMobileSidebarOpen(false)
                      }}
                      disabled={!defaultNewChatWorkspacePath}
                      aria-label={`New chat in ${defaultNewChatWorkspaceLabel}`}
                      title={`New chat in ${defaultNewChatWorkspaceLabel}`}
                    >
                      <Plus size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                      <span className="min-w-0 truncate">New Chat</span>
                    </button>
                    <Link
                      to="/"
                      className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                      onClick={() => setMobileSidebarOpen(false)}
                      aria-label="Open workspaces"
                      title="Workspaces"
                    >
                      <Folder size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                      <span className="min-w-0 truncate">Workspaces</span>
                    </Link>
                    <button
                      type="button"
                      className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                      onClick={handleOpenQuickActions}
                      aria-label="Open Desktop quick actions"
                      title="Quick Actions"
                    >
                      <Keyboard size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                      <span className="min-w-0 truncate">Quick Actions</span>
                    </button>
                    <button
                      type="button"
                      className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                      onClick={handleOpenSearchChats}
                      aria-label="Open Search Chats"
                      title="Search Chats"
                    >
                      <Search size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                      <span className="min-w-0 truncate">Search Chats</span>
                    </button>
                    <Link
                      to="/tools"
                      className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                      onClick={() => setMobileSidebarOpen(false)}
                      aria-label="Open Swarm Tools"
                      title="Tools"
                    >
                      <LayoutGrid size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                      <span className="min-w-0 truncate">Tools</span>
                    </Link>
                    {routeWorkspaceSlug ? (
                      <Link
                        to="/$workspaceSlug/settings"
                        params={{ workspaceSlug: routeWorkspaceSlug }}
                        className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                        onClick={() => {
                          setQuickSettingsTab(null)
                          setMobileSidebarOpen(false)
                        }}
                        aria-label="Open settings"
                        title="Settings"
                      >
                        <Settings size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                        <span className="min-w-0 truncate">Settings</span>
                      </Link>
                    ) : (
                      <Link
                        to="/settings"
                        className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)]"
                        onClick={() => {
                          setQuickSettingsTab(null)
                          setMobileSidebarOpen(false)
                        }}
                        aria-label="Open settings"
                        title="Settings"
                      >
                        <Settings size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                        <span className="min-w-0 truncate">Settings</span>
                      </Link>
                    )}
                  </div>
              </div>
            </div>
          </div>
          <div className="flex min-h-0 flex-1 flex-col">
            <div ref={sidebarBodyRef} className="scrollbar-hidden flex min-h-0 flex-1 flex-col overflow-y-auto px-3 py-3">
              <div className="scrollbar-hidden grid min-h-0 flex-1 content-start gap-2 overflow-y-auto font-mono">
                  <div className="grid min-h-[34px] grid-cols-[minmax(0,1fr)_24px_24px] items-center gap-1 rounded-md border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2 py-1">
                    <label className="relative min-w-0" title={topWorkspacePath || 'Default Workspace'}>
                      <span className="sr-only">Workspace</span>
                      <select
                        value={topWorkspacePath}
                        onChange={handleWorkspaceSelect}
                        disabled={topWorkspaceOptions.length === 0}
                        className="h-7 w-full min-w-0 appearance-none rounded border border-transparent bg-transparent py-0 pl-0 pr-5 text-[11px] font-semibold text-[var(--app-text)] outline-none hover:text-[var(--app-primary)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] disabled:opacity-70"
                        aria-label="Workspace"
                      >
                        {topWorkspacePath && !mergedSidebarWorkspaceEntries.some((workspace) => workspace.path === topWorkspacePath) ? (
                          <option value={topWorkspacePath}>{topWorkspaceLabel}</option>
                        ) : null}
                        {topWorkspaceOptions.map((workspace) => (
                          <option key={workspace.path} value={workspace.path}>{workspace.workspaceName}</option>
                        ))}
                      </select>
                      <ChevronDown size={12} strokeWidth={1.8} className="pointer-events-none absolute right-1 top-1/2 -translate-y-1/2 text-[var(--app-text-subtle)]" />
                    </label>
                    <button
                      type="button"
                      className={SIDEBAR_ACTION_BUTTON_CLASS}
                      onClick={() => {
                        if (topWorkspacePath) handleStartNewSessionInWorkspace(topWorkspacePath, topWorkspaceLabel)
                      }}
                      disabled={!topWorkspacePath}
                      aria-label={`New chat in ${topWorkspaceLabel}`}
                      title="New Chat"
                    >
                      <Plus size={13} strokeWidth={1.8} className="shrink-0" />
                    </button>
                    <button
                      type="button"
                      className={SIDEBAR_ACTION_BUTTON_CLASS}
                      onClick={() => {
                        if (topWorkspace && topWorkspacePath) {
                          openWorktreeSessionModal({
                            workspace: topWorkspace,
                            workspaceSlug: topWorkspaceSlug || workspaceRouteSlugBase({ path: topWorkspacePath, workspaceName: topWorkspaceLabel }),
                            routeOptions: buildDesktopChatRouteOptions({
                              hostSwarmName: swarmName,
                              workspacePath: topWorkspace.path,
                              workspaceName: topWorkspace.workspaceName,
                              topologyRoutes: topWorkspace.topologyRoutes,
                              localWorkspaceBindingId: topWorkspace.localWorkspaceBindingId,
                              hostSwarmId: currentSwarmTarget?.swarm_id ?? null,
                            }),
                          })
                        }
                      }}
                      disabled={!topWorkspace}
                      aria-label={`New worktree for ${topWorkspaceLabel}`}
                      title="Worktree"
                    >
                      <GitBranch size={13} strokeWidth={1.8} className="shrink-0" />
                    </button>
                  </div>
                  <div className="sticky top-0 z-10 flex min-h-8 items-center gap-2 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-[10px] text-[var(--app-text-subtle)]">
                    <label className="flex min-w-0 items-center gap-1">
                      <span>Show</span>
                      <select
                        aria-label="Hide inactive chats after"
                        disabled={sidebarThresholdSaving}
                        value={sidebarHideInactiveHours === null ? 'never' : String(sidebarHideInactiveHours)}
                        onChange={(event) => { void handleSidebarThresholdChange(event.target.value === 'never' ? null : Number(event.target.value)) }}
                        className="h-6 rounded border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-1 text-[10px] text-[var(--app-text)]"
                      >
                        <option value="1">1h</option><option value="6">6h</option><option value="12">12h</option><option value="24">24h</option><option value="168">7d</option><option value="never">Never hide</option>
                      </select>
                    </label>
                    {filteredSidebarTrees.hiddenCount > 0 ? <button type="button" className="ml-auto text-[var(--app-primary)]" onClick={handleOpenSearchChats}>{filteredSidebarTrees.hiddenCount} hidden · Search</button> : null}
                    <button type="button" className="ml-auto text-[var(--app-primary)]" onClick={() => { setSidebarSelectionMode((value) => !value); setSelectedSidebarRootIDs(new Set()) }}>{sidebarSelectionMode ? 'Done' : 'Select'}</button>
                  </div>
                  {sidebarSelectionMode ? (
                    <div className="sticky top-9 z-10 flex items-center gap-2 rounded-md bg-[var(--app-surface-elevated)] px-2 py-1 text-[10px] shadow-sm">
                      <span>{selectedSidebarRootIDs.size} selected</span>
                      <button type="button" className="ml-auto" onClick={() => setSelectedSidebarRootIDs(new Set())}>Clear</button>
                      <button type="button" disabled={bulkArchivePending || selectedSidebarRootIDs.size === 0} className="inline-flex items-center gap-1 rounded bg-[var(--app-primary)] px-2 py-1 text-[var(--app-background)] disabled:opacity-50" onClick={() => { void handleBulkArchiveSidebar() }}>
                        {bulkArchivePending ? <LoaderCircle size={11} className="animate-spin" /> : <Archive size={11} />} Archive selected
                      </button>
                    </div>
                  ) : null}
                  {renderSidebarSessionGroups({
                    nodes: globalFlattenedSessionNodes,
                    routeSessionId,
                    now: sidebarNow,
                    fallbackSwarmName: swarmName,
                    routeOptions: globalSessionRouteOptions,
                    workspaceSlug: globalSessionWorkspaceSlug,
                    expandedAgentSessions,
                    compactingSession,
                    pendingActions: sidebarSessionActions,
                    selectionMode: sidebarSelectionMode,
                    selectedRootIDs: selectedSidebarRootIDs,
                    onSelect: handleSelectSession,
                    onToggleSelected: handleToggleSidebarSelected,
                    onPrefetch: handlePrefetchSession,
                    onToggleAgents: handleToggleAgentSessions,
                    onTogglePinned: handleToggleSidebarPinned,
                    onArchive: handleArchiveSidebarSession,
                    onRename: handleRenameSidebarSession,
                  })}
                  {globalFlattenedSessionNodes.length === 0 ? (
                    <div className="px-2 py-2 text-xs text-[var(--app-text-subtle)]">No active sessions.</div>
                  ) : null}
                </div>
            </div>

          </div>
        </div>
      )}
    </>
  )

  const updateProgressJob = updateProgress.job
  const updateProgressMessage = updateJobMessage(updateProgressJob)
  const updateProgressStep = updateProgressStepIndex(updateProgressJob)
  const updateProgressFailed = updateProgressJob?.status === 'failed'
  const updateProgressCompleted = updateProgressJob?.status === 'completed'
  return (
    <div
      className="absolute inset-0 flex h-full min-h-0 w-full overflow-hidden bg-[var(--app-surface)] p-0 text-[var(--app-text)]"
      data-v3-route-readiness={routeReadinessStatus}
      onTouchStart={handleMobileSidebarTouchStart}
      onTouchMove={handleMobileSidebarTouchMove}
      onTouchEnd={handleMobileSidebarTouchEnd}
      onTouchCancel={handleMobileSidebarTouchEnd}
    >
      <aside data-testid="desktop-workspace-sidebar" className={cn('hidden shrink-0 flex-col border-r border-[var(--app-border)] bg-[var(--app-surface)] sm:flex', sidebarCollapsed ? 'sm:w-[56px]' : 'sm:w-[320px]')}>
        {sidebarContent}
      </aside>
      {mobileSidebarOpen ? (
        <div className="absolute inset-0 z-40 flex sm:hidden pt-[var(--app-safe-area-top)] pr-[var(--app-safe-area-right)] pb-[var(--app-safe-area-bottom)] pl-[var(--app-safe-area-left)]" aria-modal="true" role="dialog">
          <button
            type="button"
            className="absolute inset-0 bg-[var(--app-backdrop)]"
            aria-label="Close sidebar"
            onClick={() => setMobileSidebarOpen(false)}
          />
          <div className="relative flex h-full w-[min(360px,92vw)] max-w-full flex-col border-r border-[var(--app-border)] bg-[var(--app-surface)] shadow-2xl">
            <div className="flex h-[60px] items-center justify-between border-b border-[var(--app-border)] px-3">
              <div className="flex min-w-0 items-center gap-2">
                <Menu size={18} className="shrink-0 text-[var(--app-text-muted)]" />
                <span className="truncate text-sm font-semibold text-[var(--app-text)]">Chats</span>
              </div>
              <Button
                variant="ghost"
                className="h-11 w-11 rounded-full bg-transparent p-0 text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                onClick={() => setMobileSidebarOpen(false)}
                aria-label="Close sidebar"
              >
                <X size={21} strokeWidth={2.5} />
              </Button>
            </div>
            <div className="min-h-0 flex-1">{sidebarContent}</div>
          </div>
        </div>
      ) : null}

      <main className="flex-1 min-w-0 min-h-0 flex flex-col h-full overflow-hidden sm:pr-[var(--app-safe-area-right)] sm:pl-[var(--app-safe-area-left)]">
        {routeSessionUnavailable ? (
          <div className="flex h-full flex-1 items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">Session not available</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                Desktop state route readiness marked this session as {routeReadinessStatus}. Refresh the workspace if this session was just created elsewhere.
              </p>
            </Card>
          </div>
        ) : routeSessionId ? (
          <DesktopV3ExistingConversationPane
            key={`existing:${routeSessionId}`}
            sessionId={routeSessionId}
            initialHydrateStatus={desktopInitialHydrate.status}
            renderedMessages={selectedDesktopV3Messages}
            messagesLoaded={selectedDesktopV3MessagesLoaded}
            loadedMessageCount={selectedDesktopV3LoadedMessageCount}
            modeCommand={sessionModeCommand}
            onModeCommandHandled={() => setSessionModeCommand(null)}
            session={sessionById.get(routeSessionId) ?? null}
            routeOptions={sessionById.get(routeSessionId) ? (() => {
              const sessionWorkspacePath = desktopRouteWorkspacePathForSession(sessionById.get(routeSessionId)!, workspacePathByBindingId, knownWorkspacePaths)
              const sessionWorkspace = mergedSidebarWorkspaceEntries.find((workspace) => workspace.path === sessionWorkspacePath) ?? null
              return buildDesktopChatRouteOptions({
                hostSwarmName: swarmName,
                workspacePath: sessionWorkspacePath,
                workspaceName: sessionById.get(routeSessionId)?.workspaceName ?? '',
                topologyRoutes: sessionWorkspace?.topologyRoutes ?? [],
                localWorkspaceBindingId: sessionWorkspace?.localWorkspaceBindingId ?? '',
                hostSwarmId: currentSwarmTarget?.swarm_id ?? null,
              })
            })() : []}
            onOpenChats={() => setMobileSidebarOpen(true)}
            sessionActions={activeRouteSessionActions}
            onCompactingChange={handleCompactingSessionChange}
            onArchivePlanSession={handleArchivePlanSession}
            onNewSession={() => {
              const routeSession = sessionById.get(routeSessionId)
              const workspacePath = routeSession
                ? desktopRouteWorkspacePathForSession(routeSession, workspacePathByBindingId, knownWorkspacePaths)
                  || selectedWorkspacePath
                  || ''
                : ''
              if (routeSession && workspacePath) handleStartNewSessionInWorkspace(workspacePath, routeSession.workspaceName)
            }}
            onSlashCommand={handleSlashCommand}
            onOpenPlan={() => openPlanModalForSession(routeSessionId)}
          />
        ) : routeWorkspaceSlug && !chatWorkspacePath && !workspacesLoading ? (
          <div className="flex h-full flex-1 items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">Workspace not found</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                We couldn’t resolve that workspace URL.
              </p>
            </Card>
          </div>
        ) : routeWorkspace?.path ? (
          <DesktopV3NewSessionPane
            key={`new:${routeWorkspace.path}`}
            modeCommand={sessionModeCommand}
            onModeCommandHandled={() => setSessionModeCommand(null)}
            workspace={routeWorkspace}
            workspaceSlug={routeWorkspaceSlug}
            routeOptions={buildDesktopChatRouteOptions({
              hostSwarmName: swarmName,
              workspacePath: routeWorkspace.path,
              workspaceName: routeWorkspace.workspaceName,
              topologyRoutes: routeWorkspace.topologyRoutes,
              localWorkspaceBindingId: routeWorkspace.localWorkspaceBindingId,
              hostSwarmId: currentSwarmTarget?.swarm_id ?? null,
            })}
            onOpenChats={() => setMobileSidebarOpen(true)}
            mobileSessionQuickMenu={mobileSessionQuickMenu}
            onSlashCommand={handleSlashCommand}
          />
        ) : (
          <div className="flex h-full flex-1 items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">No workspace selected</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                Open a workspace from the sidebar to browse cached conversations and start a new session.
              </p>
            </Card>
          </div>
        )}
      </main>

      <DesktopQuickSettingsModal
        tab={quickSettingsTab}
        onClose={() => setQuickSettingsTab(null)}
        onOpenFullSettings={handleOpenSettingsTab}
      />

      <DesktopQuickActionsModal
        open={quickActionsOpen}
        actions={quickActions}
        onClose={() => setQuickActionsOpen(false)}
        onOpenShortcutsSettings={() => handleOpenSettingsTab('shortcuts')}
      />

      <SearchChatsModal
        open={searchModalOpen}
        onOpenChange={setSearchModalOpen}
        onOpenSession={handleOpenSearchResult}
      />

      <DesktopNotificationsModal
        open={notificationsOpen}
        onOpenChange={(open) => {
          setNotificationsOpen(open)
          if (open) setNotificationActionError(null)
        }}
        notifications={notificationItems}
        summary={notificationSummary}
        loading={false}
        connectionState={desktopV3ConnectionStateFromRealtimeStatus(desktopInitialHydrate.status, notificationItems.length)}
        onMarkRead={handleMarkNotificationRead}
        onAcknowledge={handleAcknowledgeNotification}
        onMute={handleMuteNotification}
        onClearAll={handleClearNotifications}
      />
      {notificationsOpen && notificationActionError ? (
        <div className="pointer-events-none absolute left-1/2 top-6 z-[90] w-[min(520px,calc(100vw-32px))] -translate-x-1/2" role="alert">
          <Card className="border-[var(--app-error)] bg-[color-mix(in_srgb,var(--app-error)_12%,var(--app-surface))] p-3 text-sm text-[var(--app-error)] shadow-2xl">
            {notificationActionError}
          </Card>
        </div>
      ) : null}

      <DesktopPlanModal
        open={Boolean(planModal)}
        plan={planModalPlan}
        revisions={planModalRevisions}
        historyLoading={planModalLoading}
        saving={planModalSaving}
        executing={planModalExecuting}
        error={planModalError}
        onOpenChange={(open) => {
          if (!open) setPlanModal(null)
        }}
        onCopy={handleCopyPlanText}
        onRestoreRevision={handleRestorePlanRevisionModal}
        onApproveStart={handleApproveStartPlanModal}
      />

      {todoModal ? (
        <WorkspaceTodoModal
          open={Boolean(todoModal)}
          workspaceName={todoModal.workspaceName}
          userSection={{
            ownerKind: 'user',
            title: 'User Todos',
            description: 'User-requested tasks for this workspace.',
            emptyText: 'Drop user todos here',
            items: (todoItems[todoModal.workspacePath] ?? []).filter((item) => item.ownerKind === 'user'),
            summary: {
              ...(todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()),
              taskCount: (todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()).user.taskCount,
              openCount: (todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()).user.openCount,
              inProgressCount: (todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()).user.inProgressCount,
            },
          }}
          saving={todoSavingWorkspacePath === todoModal.workspacePath}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) {
              closeTodoModal()
            }
          }}
          onCreate={async (ownerKind, input) => {
            const result = await mutateTodoState(todoModal.workspacePath, () => createWorkspaceTodo({ workspacePath: todoModal.workspacePath, ownerKind, ...input }))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: upsertWorkspaceTodoItem(current[todoModal.workspacePath] ?? [], result.item),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onToggleDone={async (item, done) => {
            const result = await mutateTodoState(todoModal.workspacePath, () => updateWorkspaceTodo({ workspacePath: todoModal.workspacePath, ownerKind: item.ownerKind, id: item.id, done }))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: upsertWorkspaceTodoItem(current[todoModal.workspacePath] ?? [], result.item),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onToggleInProgress={async (item, inProgress) => {
            const result = await mutateTodoState(
              todoModal.workspacePath,
              () => (inProgress
                ? setWorkspaceTodoInProgress(todoModal.workspacePath, item.id, item.ownerKind, item.sessionId)
                : updateWorkspaceTodo({ workspacePath: todoModal.workspacePath, ownerKind: item.ownerKind, id: item.id, inProgress: false, sessionId: item.sessionId })),
            )
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: upsertWorkspaceTodoItem(current[todoModal.workspacePath] ?? [], result.item),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onUpdate={async (item, patch) => {
            const result = await mutateTodoState(todoModal.workspacePath, () => updateWorkspaceTodo({ workspacePath: todoModal.workspacePath, ownerKind: item.ownerKind, id: item.id, sessionId: item.sessionId, ...patch }))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: upsertWorkspaceTodoItem(current[todoModal.workspacePath] ?? [], result.item),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onDelete={async (item) => {
            const summary = await mutateTodoState(todoModal.workspacePath, () => deleteWorkspaceTodo(todoModal.workspacePath, item.id, item.ownerKind, item.sessionId))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: (current[todoModal.workspacePath] ?? []).filter((entry) => entry.id !== item.id),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(summary) }))
          }}
          onDeleteDone={async (ownerKind) => {
            const result = await mutateTodoState(todoModal.workspacePath, () => deleteDoneWorkspaceTodos(todoModal.workspacePath, ownerKind))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: mergeWorkspaceTodoItemsByOwner(current[todoModal.workspacePath] ?? [], ownerKind, result.items),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onDeleteAll={async (ownerKind) => {
            const result = await mutateTodoState(todoModal.workspacePath, () => deleteAllWorkspaceTodos(todoModal.workspacePath, ownerKind))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: mergeWorkspaceTodoItemsByOwner(current[todoModal.workspacePath] ?? [], ownerKind, result.items),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onReorder={async (ownerKind, orderedIDs) => {
            const result = await mutateTodoState(todoModal.workspacePath, () => reorderWorkspaceTodos(todoModal.workspacePath, orderedIDs, ownerKind))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: mergeWorkspaceTodoItemsByOwner(current[todoModal.workspacePath] ?? [], ownerKind, result.items),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
        />
      ) : null}

      {desktopToast ? (
        <div className="pointer-events-none absolute right-6 top-6 z-[70] max-w-md" role="status" aria-live="polite">
          <Card className={cn(
            'border p-4 shadow-2xl',
            desktopToast.tone === 'success'
              ? 'border-[var(--app-success)] bg-[color-mix(in_srgb,var(--app-success)_12%,var(--app-surface))] text-[var(--app-text)]'
              : desktopToast.tone === 'error'
                ? 'border-[var(--app-error)] bg-[color-mix(in_srgb,var(--app-error)_12%,var(--app-surface))] text-[var(--app-text)]'
                : 'border-[var(--app-border-strong)] bg-[var(--app-surface)] text-[var(--app-text)]',
          )}>
            <div className="flex items-start gap-3 text-sm">
              {desktopToast.tone === 'success' ? <CheckCircle2 className="mt-0.5 shrink-0 text-[var(--app-success)]" size={18} /> : desktopToast.tone === 'error' ? <XCircle className="mt-0.5 shrink-0 text-[var(--app-error)]" size={18} /> : <Bell className="mt-0.5 shrink-0 text-[var(--app-primary)]" size={18} />}
              <div className="min-w-0 font-medium">{desktopToast.message}</div>
            </div>
          </Card>
        </div>
      ) : null}

      {updateProgress.open ? (
        <div className="absolute inset-0 z-50 flex items-center justify-center bg-[var(--app-backdrop)] px-4" aria-modal="true" role="dialog" aria-label="Swarm update progress">
          <Card className="w-full max-w-xl border-[var(--app-border-strong)] bg-[var(--app-surface)] p-6 shadow-2xl">
            <div className="flex items-start justify-between gap-4">
              <div>
                <div className="text-lg font-semibold">Swarm update progress</div>
                <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                  {updateProgressJob?.kind === 'dev'
                    ? 'Running the same dev rebuild path as /update dev.'
                    : 'Running the release update path.'}
                </p>
              </div>
              {updateProgressFailed ? <XCircle className="shrink-0 text-[var(--app-error)]" size={24} /> : updateProgressCompleted ? <CheckCircle2 className="shrink-0 text-[var(--app-success)]" size={24} /> : <LoaderCircle className="shrink-0 animate-spin text-[var(--app-primary)]" size={24} />}
            </div>
            <div className={cn('mt-4 rounded-xl border p-4 text-sm', updateProgressFailed ? 'border-[var(--app-error)] bg-[color-mix(in_srgb,var(--app-error)_10%,transparent)] text-[var(--app-error)]' : 'border-[var(--app-border)] bg-[var(--app-panel)] text-[var(--app-text)]')}>
              {updateProgressMessage}
            </div>
            <ol className="mt-4 space-y-3">
              {UPDATE_PROGRESS_STEP_TITLES.map((title, index) => {
                const done = updateProgressCompleted || index < updateProgressStep
                const current = !updateProgressFailed && !updateProgressCompleted && index === Math.min(updateProgressStep, UPDATE_PROGRESS_STEP_TITLES.length - 1)
                return (
                  <li key={title} className="flex items-center gap-3 text-sm">
                    <span className={cn(
                      'grid h-6 w-6 shrink-0 place-items-center rounded-full border text-[11px]',
                      done ? 'border-[var(--app-success)] bg-[color-mix(in_srgb,var(--app-success)_18%,transparent)] text-[var(--app-success)]' : current ? 'border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_16%,transparent)] text-[var(--app-primary)]' : 'border-[var(--app-border)] text-[var(--app-text-muted)]',
                    )}>
                      {done ? <CheckCircle2 size={14} /> : current ? <LoaderCircle size={14} className="animate-spin" /> : index + 1}
                    </span>
                    <span className={cn(current && 'font-medium text-[var(--app-primary)]', done && 'text-[var(--app-text)]', !done && !current && 'text-[var(--app-text-muted)]')}>{title}</span>
                  </li>
                )
              })}
            </ol>
            <div className="mt-4 grid grid-cols-2 gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-3 text-xs text-[var(--app-text-muted)]">
              <div>Kind: {updateProgressJob?.kind || (updateDevMode ? 'dev' : 'release')}</div>
              <div>Status: {updateProgressJob?.status || (updateRunning ? 'starting' : 'idle')}</div>
              <div>Lane: {updateProgressJob?.lane || (updateProgressJob?.kind === 'dev' || updateDevMode ? 'dev' : 'main')}</div>
              <div>Helper PID: {updateProgressJob?.helper_pid || '—'}</div>
              <div className="col-span-2 break-all">Command: {updateProgressJob?.command || 'starting…'}</div>
              <div className="col-span-2 break-all">Log: {updateProgressJob?.log_path || 'not available yet'}</div>
              <div>Started: {formatUpdateProgressTime(updateProgressJob?.started_at_unix_ms ?? updateProgress.startedAt ?? undefined)}</div>
              <div>Updated: {formatUpdateProgressTime(updateProgressJob?.updated_at_unix_ms)}</div>
            </div>
            <div className="mt-5 flex justify-end gap-3">
              <Button variant="ghost" onClick={handleCloseUpdateProgress} disabled={updateRunning}>Close</Button>
            </div>
          </Card>
        </div>
      ) : null}

      <WorktreeSessionModal
        state={worktreeSessionModal}
        title={worktreeSessionTitle}
        branch={worktreeSessionBranch}
        selectedExistingPath={worktreeSessionExistingPath}
        busy={worktreeSessionCreating}
        error={worktreeSessionError}
        onTitleChange={setWorktreeSessionTitle}
        onBranchChange={setWorktreeSessionBranch}
        onSelectedExistingPathChange={setWorktreeSessionExistingPath}
        onSubmit={() => { void handleCreateWorktreeSession() }}
        onClose={closeWorktreeSessionModal}
      />
      <GitDetailsOverlay
        state={gitPanel}
        snapshot={gitPanel ? (gitSnapshotByPath.get(gitPanel.workspacePath) ?? (gitPanel.workspacePath === selectedGitWorkspacePath ? gitSnapshot : null)) : null}
        loading={Boolean(gitPanel && gitPanel.workspacePath === selectedGitWorkspacePath && gitStatusQuery.isFetching)}
        error={gitPanel ? (gitRealtimeErrors[gitPanel.workspacePath] ?? (gitPanel.workspacePath === selectedGitWorkspacePath && gitStatusQuery.error instanceof Error ? gitStatusQuery.error.message : null)) : null}
        onRefresh={() => { if (gitPanel) void queryClient.invalidateQueries({ queryKey: gitStatusQueryKey(gitPanel.workspacePath) }) }}
        onClose={closeGitPanel}
      />
      {pwaDebugEnabled ? <PwaLayoutDebugOverlay /> : null}

    </div>
  )
}
