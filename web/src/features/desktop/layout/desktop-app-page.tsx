import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { JSX, ReactNode, ChangeEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate, Link } from '@tanstack/react-router'
import { Bell, Bot, Box, Check, CheckCircle2, ChevronDown, ChevronLeft, ChevronRight, Download, ExternalLink, GitBranch, Home, LayoutGrid, Link2, LoaderCircle, Menu, Pause, Play, Plus, RefreshCcw, Settings, Workflow, X, XCircle } from 'lucide-react'
import { requestJson } from '../../../app/api'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { cn } from '../../../lib/cn'
import { useWorkspaceLauncher } from '../../workspaces/launcher/state/use-workspace-launcher'
import { applyDesktopRouteTheme } from './desktop-theme-controller'
import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'
import { agentStateQueryOptions, draftModelQueryOptions, uiSettingsQueryKey, workspaceOverviewQueryOptions } from '../../queries/query-options'
import type { DesktopSessionRecord } from '../types/realtime'
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
import { saveLocalContainerUpdateWarningDismissal } from '../settings/swarm/mutations/save-local-container-update-warning-dismissal'
import { saveSwarmSettings } from '../settings/swarm/mutations/save-swarm-settings'
import { localContainerUpdateWarningDismissed, normalizeSwarmSettings, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { fetchSwarmTargets, selectSwarmTarget, type SwarmTarget } from '../swarm/api/swarm-targets'
import { fetchRemoteDeploySessions, type RemoteDeploySession } from '../swarm/api/deploy-container'
import { approveRemoteSwarmPairing, type RemoteSwarmPendingPairing } from '../onboarding/api'
import { ManagedHostLinkRequestModal, activePendingPairings, managedHostTargetFromPairingResult } from '../swarm/components/managed-host-link-request-modal'
import { DesktopV3ExistingConversationPane } from '../chat/components/desktop-v3-existing-conversation-pane'
import { DesktopV3NewSessionPane } from '../chat/components/desktop-v3-new-session-pane'
import { createDesktopV3CreateOnlySessionOperation, startDesktopV3CreateOnlySession } from '../session-v3/new-session-flow'
import { DesktopPlanModal } from '../chat/components/desktop-plan-modal'
import { buildDesktopChatRouteOptions, getDesktopSessionCreateTarget, resolveDesktopChatRouteFromSession, type DesktopChatRoute } from '../chat/services/chat-routing'
import type { DesktopSlashCommand } from '../chat/services/slash-commands'
import { fetchGitStatus, gitStatusQueryKey, startGitRealtime } from '../git/api'
import type { GitFileStatus, GitSnapshot } from '../git/types'
import { fetchDesktopUpdateJob, fetchDesktopUpdateStatus, fetchLocalContainerUpdatePlan, startDesktopUpdate, type DesktopUpdateJob, type LocalContainerUpdatePlan } from '../update/api'
import { fetchFlows, flowsQueryKey, setFlowEnabled, type FlowSummaryRecord } from '../settings/flows/api'
import { FlowsSettingsPage } from '../settings/flows/components/flows-settings-page'
import {
  sessionBackgroundInfo,
  sessionChildDescriptor,
  sessionParentSessionID,
  type SidebarSessionNodeKind,
} from './sidebar-session-lineage'
import { dispatchDesktopV3Cache, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { isDesktopV3SessionTailReady, selectDesktopSidebarRows, selectRenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { selectSession } from '../state/desktop-v3-cache-wire'
import { selectAndHydrateDesktopV3Session } from '../state/desktop-v3-session-hydrator'
import type { DesktopV3SidebarRow, RenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { fetchAndApplyDesktopV3PlanSnapshot, saveDesktopV3SessionPlan } from '../state/desktop-v3-session-api'
import { startDesktopPlanAutomatic, startDesktopPlanCheckpointed } from '../session-v3/plan-execution-api'
import type { V3SessionRunIntent } from '../state/desktop-v3-cache-types'

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
  'Inspect managed hosts',
  'Sync managed checkouts',
  'Rebuild/apply Swarm',
  'Restart/reconnect backends',
  'Verify/update containers',
] as const
const MANAGED_DEV_UPDATE_PHASES = ['inspect', 'sync', 'rebuild', 'reconnect', 'verify'] as const

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

interface SwarmTargetMenuState {
  open: boolean
}

interface LocalContainerUpdateConfirmState {
  plan: LocalContainerUpdatePlan
  remoteSessions: RemoteDeploySession[]
  managedHostCount: number
  pendingDismiss: boolean
}

interface DesktopV3CompactingSessionState {
  sessionId: string
  startedAt: number
}

interface PlanModalState {
  sessionId: string
}

interface GitPanelState {
  workspacePath: string
  workspaceName: string
}

interface WorktreeSessionModalState {
  workspacePath: string
  workspaceName: string
  workspaceSlug: string
  routeOptions: DesktopChatRoute[]
  branchPrefix: string
  settingsLoading: boolean
}

type SidebarFlowStatus = 'active' | 'paused' | 'draft' | 'needs_review' | 'failed'

interface SidebarFlowRow {
  id: string
  name: string
  agent: string
  enabled: boolean
  status: SidebarFlowStatus
  detail: string
  raw: FlowSummaryRecord
}

function desktopRunIntentFromV3(runIntent: V3SessionRunIntent | undefined) {
  if (!runIntent) return null
  return {
    sessionId: runIntent.session_id,
    runId: runIntent.run_id,
    status: runIntent.status,
    blockedReason: runIntent.blocked_reason ?? '',
    createdAt: runIntent.created_at,
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
      startedAt: runIntentActive ? runIntent?.createdAt ?? null : null,
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

interface WorktreeSettingsResponseWire {
  ok?: boolean
  worktrees?: WorktreeSettingsWire
}

function normalizeWorktreeBranchPrefix(value: string | undefined): string {
  const trimmed = (value ?? '').trim().replace(/^\/+|\/+$/g, '')
  if (!trimmed) return ''
  if (trimmed.toLowerCase().endsWith('/<id>')) {
    return trimmed.slice(0, -'/<id>'.length).replace(/^\/+|\/+$/g, '')
  }
  return trimmed
}

async function fetchWorktreeBranchPrefix(workspacePath: string): Promise<string> {
  const params = new URLSearchParams()
  const normalizedWorkspacePath = workspacePath.trim()
  if (normalizedWorkspacePath) params.set('workspace_path', normalizedWorkspacePath)
  const response = await requestJson<WorktreeSettingsResponseWire>(`/v1/worktrees?${params.toString()}`)
  const branchPrefix = normalizeWorktreeBranchPrefix(response.worktrees?.branch_name)
  if (!branchPrefix) {
    throw new Error('Worktree settings did not return a branch prefix')
  }
  return branchPrefix
}

function normalizeWorktreeBranchSuffix(value: string): string {
  return value.trim().replace(/^\/+|\/+$/g, '')
}

function composeWorktreeBranchName(prefix: string, suffix: string): string {
  const normalizedPrefix = normalizeWorktreeBranchPrefix(prefix)
  const normalizedSuffix = normalizeWorktreeBranchSuffix(suffix)
  return normalizedSuffix ? `${normalizedPrefix}/${normalizedSuffix}` : normalizedPrefix
}

function WorktreeSessionModal({ state, title, branch, busy, error, onTitleChange, onBranchChange, onSubmit, onClose }: {
  state: WorktreeSessionModalState | null
  title: string
  branch: string
  busy: boolean
  error: string | null
  onTitleChange: (value: string) => void
  onBranchChange: (value: string) => void
  onSubmit: () => void
  onClose: () => void
}) {
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
          {error ? <div className="border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning)]">{error}</div> : null}
          <div className="flex justify-end gap-3">
            <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
            <Button type="submit" disabled={busy || state.settingsLoading || !state.branchPrefix.trim() || !title.trim() || !normalizeWorktreeBranchSuffix(branch)}>
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

function formatLocalContainerUpdateTarget(plan: LocalContainerUpdatePlan): string {
  const target = plan.target ?? {}
  if (plan.dev_mode) {
    const postRebuildFingerprint = target.post_rebuild_fingerprint?.trim()
    const fingerprint = postRebuildFingerprint || target.fingerprint?.trim()
    return fingerprint ? `Target dev image fingerprint: ${fingerprint.slice(0, 12)}` : 'Target dev image fingerprint unavailable'
  }
  const version = target.version?.trim()
  const digest = target.digest_ref?.trim()
  if (version && digest) {
    return `Target ${version} (${digest})`
  }
  if (version) {
    return `Target ${version}`
  }
  if (digest) {
    return `Target ${digest}`
  }
  return 'Target version unavailable'
}

function localContainerUpdateAffected(plan: LocalContainerUpdatePlan): boolean {
  return (plan.summary?.affected ?? 0) > 0 || (plan.summary?.needs_update ?? 0) > 0 || (plan.summary?.unknown ?? 0) > 0 || (plan.summary?.errors ?? 0) > 0
}

function remoteDeployUpdateSessionCount(sessions: RemoteDeploySession[]): number {
  return sessions.filter((session) => session.status?.trim().toLowerCase() === 'attached' && Boolean(session.ssh_session_target?.trim())).length
}

function managedHostUpdateTargetCount(targets: SwarmTarget[]): number {
  return targets.filter((target) => target.selectable && target.relationship?.trim().toLowerCase() === 'managed' && target.kind !== 'self').length
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
    return 5
  }
  if (message.includes('restart') || message.includes('reconnect')) {
    return 4
  }
  if (message.includes('rebuild') || message.includes('build') || message.includes('applying') || message.includes('installing') || message.includes('staging') || message.includes('fingerprint')) {
    return 3
  }
  if (message.includes('syncing') || message.includes('sync')) {
    return 2
  }
  if (message.includes('inspect') || message.includes('checking managed')) {
    return 1
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

function swarmKindDotClass(kind: SwarmTarget['kind'] | undefined, online = true): string {
  if (!online) {
    return 'bg-[var(--app-warning)]'
  }
  if (kind === 'remote' || kind === 'mirrored') {
    return 'bg-[var(--app-info)]'
  }
  return 'bg-[var(--app-success)]'
}

function swarmHostDisplayName(hostSwarmID: string | undefined, targets: SwarmTarget[]): string {
  const normalizedHostSwarmID = hostSwarmID?.trim() ?? ''
  if (!normalizedHostSwarmID) {
    return ''
  }
  const host = targets.find((target) => target.swarm_id.trim() === normalizedHostSwarmID)
  return host?.name?.trim() || normalizedHostSwarmID
}

function swarmTargetPrimaryLabel(target: SwarmTarget): string {
  return target.name?.trim() || target.swarm_id?.trim() || 'Swarm'
}

function swarmTargetSecondaryLabel(target: SwarmTarget, targets: SwarmTarget[]): string {
  if (target.kind === 'mirrored') {
    const source = swarmHostDisplayName(target.host_swarm_id, targets)
    return source || 'managed host'
  }
  return `${swarmKindLabel(target)} · ${swarmTargetStatusLabel(target)}`
}

function swarmTargetTitle(target: SwarmTarget, targets: SwarmTarget[]): string {
  const secondary = swarmTargetSecondaryLabel(target, targets)
  const openURL = swarmTargetOpenURL(target)
  return `${secondary}${!target.current && target.online && openURL ? ' · open in new window' : ''}`
}

function swarmRoleLabel(target: Pick<SwarmTarget, 'role'> | null | undefined): string {
  const role = target?.role?.trim().toLowerCase() || ''
  switch (role) {
    case 'managed':
      return 'Managed Host'
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

function swarmKindLabel(target: SwarmTarget): string {
  if (target.kind === 'self') {
    return swarmRoleLabel(target)
  }
  if (target.kind === 'host' || target.relationship?.trim().toLowerCase() === 'managed') {
    return 'host'
  }
  return target.kind === 'remote' ? 'remote' : 'local'
}

function swarmTargetStatusLabel(target: SwarmTarget): string {
  if (target.current) {
    return 'active here'
  }
  if (target.online) {
    return 'online'
  }
  const status = target.attach_status?.trim()
  if (!status || status === 'attached') {
    return 'offline'
  }
  return status
}

function swarmTargetOpenURL(target: SwarmTarget): string {
  const raw = target.desktop_url?.trim() || target.backend_url?.trim() || ''
  if (!raw) {
    return ''
  }
  try {
    const parsed = new URL(raw)
    if (parsed.hostname.includes('.ts.net')) {
      parsed.port = ''
      parsed.pathname = ''
      parsed.search = ''
      parsed.hash = ''
      return parsed.toString().replace(/\/$/, '')
    }
  } catch {
    return raw
  }
  return raw
}

function flowAgentLabel(record: FlowSummaryRecord): string {
  return record.agent_detail?.name?.trim()
    || record.definition.agent.profile_name?.trim()
    || 'unassigned'
}

function sidebarFlowStatus(record: FlowSummaryRecord): SidebarFlowStatus {
  if (record.last_run?.status === 'failed') return 'failed'
  if (record.last_run?.status === 'review') return 'needs_review'
  if (!record.definition.enabled) return record.history_count > 0 ? 'paused' : 'draft'
  const statuses = record.assignment_statuses ?? []
  if (statuses.some((status) => status.pending_sync || status.status === 'target_offline' || status.status === 'target_unusable')) {
    return 'needs_review'
  }
  return 'active'
}

function sidebarFlowStatusLabel(status: SidebarFlowStatus): string {
  switch (status) {
    case 'active':
      return 'active'
    case 'paused':
      return 'paused'
    case 'draft':
      return 'draft'
    case 'needs_review':
      return 'review'
    case 'failed':
      return 'failed'
  }
}

function sidebarFlowDotClass(status: SidebarFlowStatus): string {
  switch (status) {
    case 'active':
      return 'bg-[var(--app-success)]'
    case 'needs_review':
      return 'bg-[var(--app-warning)]'
    case 'failed':
      return 'bg-[var(--app-danger)]'
    default:
      return 'bg-[var(--app-text-subtle)]'
  }
}

function sidebarFlowDetail(record: FlowSummaryRecord): string {
  const workspace = record.workspace_detail?.workspace_path?.trim()
    || record.definition.workspace.workspace_path?.trim()
    || record.definition.workspace.host_workspace_path?.trim()
    || 'workspace'
  const target = record.target_detail?.name?.trim()
    || record.definition.target.name?.trim()
    || record.definition.target.swarm_id?.trim()
    || record.definition.target.kind?.trim()
    || 'target'
  return `${fallbackWorkspaceNameFromPath(workspace)} · ${target}`
}

function sidebarFlowRow(record: FlowSummaryRecord): SidebarFlowRow {
  return {
    id: record.definition.flow_id,
    name: record.definition.name?.trim() || record.definition.flow_id,
    agent: flowAgentLabel(record),
    enabled: record.definition.enabled,
    status: sidebarFlowStatus(record),
    detail: sidebarFlowDetail(record),
    raw: record,
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

function sessionSidebarGroup(session: DesktopSessionRecord): 'needs_review' | 'in_progress' | 'active_chats' | 'archived' {
  const group = metadataText(session, 'swarm_v3_sidebar_group')
  return group === 'needs_review' || group === 'in_progress' || group === 'archived' ? group : 'active_chats'
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
  return positiveTimestamp(sessionActiveRunIntent(session)?.createdAt)
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

export function sessionTimerLabel(session: DesktopSessionRecord, now: number): string {
  const activeSince = sessionActiveRunIntent(session)?.createdAt ?? null
  return typeof activeSince === 'number' && activeSince > 0
    ? formatDurationCompact(now - activeSince)
    : 'live'
}

export function sessionActivityLabel(session: DesktopSessionRecord): string {
  if (sessionHasPendingPermission(session)) {
    return 'Needs approval'
  }

  if (!sessionHasCanonicalActiveRun(session)) {
    return session.live.status === 'error' ? 'failed' : ''
  }

  const toolLabel = sidebarLiveToolLabel(session)
  const runStatus = sessionActiveRunIntent(session)?.status.trim().toLowerCase() ?? ''
  if (runStatus === 'pending_executor') {
    return toolLabel || 'Starting'
  }
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

function agentSummaryDescriptor(summary: SessionAgentSummary): { primary: string; secondary: string; secondaryRunning: boolean } {
  const total = summary.total
  const running = summary.running
  if (running > 0) {
    return { primary: `${running} live`, secondary: `${total} agents`, secondaryRunning: false }
  }
  return { primary: `${total} agents`, secondary: '', secondaryRunning: false }
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
  onSelect: (sessionId: string) => void
  onPrefetch: (sessionId: string) => void
  onToggleAgents: (sessionId: string) => void
}

function SessionRow({ active, now, session: initialSession, fallbackSwarmName, routeOptions, workspaceSlug, depth = 0, childLabel = null, childAssignmentLabel = null, childKind = 'root', agentSummary, agentsExpanded, compactingStartedAt = null, onSelect, onPrefetch, onToggleAgents }: SessionRowProps) {
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
  const activeCheckpointTitle = metadataText(session, 'swarm_v3_active_checkpoint_title')
  const activeCheckpointStatus = metadataText(session, 'swarm_v3_active_checkpoint_status')
  const compactingTimer = compactingActive && compactingStartedAt !== null ? formatDurationCompact(now - compactingStartedAt) : ''
  const tooltip = sessionStatusTooltip(session)
  const isNestedSession = depth > 0
  const nestedAssignmentTitle = isNestedSession && childAssignmentLabel ? childAssignmentLabel : ''
  const rowTitle = nestedAssignmentTitle || session.title || 'New conversation'
  const visibleChildLabel = childLabel && childLabel !== rowTitle ? childLabel : ''
  const nestedToneClass = childKind === 'subagent' ? 'text-[var(--app-info)]' : 'text-[var(--app-text-subtle)]'
  const hasAgentChildren = agentSummary.total > 0
  const agentDescriptor = agentSummaryDescriptor(agentSummary)
  const metadataLabel = sessionRowMetadataLabel(session)
  const relativeActivityLabel = sessionStatusDetail(session, now)
  const rowTimerLabel = compactingActive
    ? compactingTimer
    : sessionHasCanonicalActiveRun(session)
      ? sessionTimerLabel(session, now)
      : relativeActivityLabel
  const singleStatusLabel = compactingActive
    ? 'Compacting'
    : activeSession
      ? sessionActivityLabel(session)
      : sessionMeta(session) || ''
  const hasPendingPermission = sessionHasPendingPermission(session)
  const rightSideLabel = isPlanRow && !hasPendingPermission ? '' : singleStatusLabel
  const statusTone = sessionStatusTone(session)
  const checkpointProgressText = checkpointProgressLabel || (checkpointCounts.totalCount > 0
    ? `${checkpointCounts.activeIndex || checkpointCounts.completedCount}/${checkpointCounts.totalCount}`
    : '')
  const checkpointStatusMeta = activeCheckpointStatus ? activeCheckpointStatus.replace(/[_-]+/g, ' ') : ''
  const checkpointMetaParts = [
    checkpointStatusMeta.toLowerCase() === 'in progress' ? '' : checkpointStatusMeta,
    activeCheckpointTitle,
  ].filter((value, index, values): value is string => Boolean(value) && values.indexOf(value) === index)
  const planCheckpointMeta = checkpointMetaParts.join(' · ') || 'No active checkpoint'
  const showDetailsRow = !isPlanRow && Boolean(backgroundInfo || visibleChildLabel || hasAgentChildren)
  const agentToggleControl = hasAgentChildren ? (
    <button
      type="button"
      className={cn(
        'inline-flex h-4 shrink-0 items-center gap-1 border-0 bg-transparent p-0 font-mono tabular-nums text-[10px] leading-4 transition-colors',
        agentsExpanded
          ? 'text-[var(--app-text)]'
          : 'text-[var(--app-text-subtle)] hover:text-[var(--app-text)]',
      )}
      onClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
        onToggleAgents(session.id)
      }}
      aria-label={`${agentSummary.running} running of ${agentSummary.total} subagents`}
      aria-pressed={agentsExpanded}
      title={`${agentSummary.total} subagent${agentSummary.total === 1 ? '' : 's'} · ${agentSummary.running} running${agentsExpanded ? ' · click to hide subagent sessions' : ' · click to show subagent sessions'}`}
    >
      {agentsExpanded ? <ChevronDown size={10} className="shrink-0 opacity-75" /> : <ChevronRight size={10} className="shrink-0 opacity-75" />}
      <Bot size={11} className={cn('shrink-0', agentSummary.running > 0 ? 'animate-pulse text-[var(--app-success)]' : null)} />
      <span className={cn('font-mono tabular-nums text-[10px] leading-none', agentSummary.running > 0 ? 'text-[var(--app-success)]' : null)}>{agentDescriptor.primary}</span>
      {agentDescriptor.secondary ? (
        <span className={cn(
          'font-mono tabular-nums text-[10px] leading-none',
          agentSummary.running > 0 ? 'text-[var(--app-text-subtle)]' : 'text-[var(--app-text)]',
        )}>{agentDescriptor.secondary}</span>
      ) : null}
    </button>
  ) : null
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
        'grid w-full min-w-0 rounded-md border text-left outline-none transition-colors',
        isPlanRow ? 'gap-1.5 px-2.5 py-2' : 'gap-1 px-2.5 py-1.5',
        active
          ? 'border-[var(--app-border-accent)] bg-[var(--app-surface)]/45'
          : 'border-transparent bg-[var(--app-surface)]/45 hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]',
        isNestedSession ? 'ml-3' : null,
        hasAgentChildren && agentsExpanded ? 'border-[var(--app-border-accent)]' : null,
      )}
      title={tooltip || metadataLabel}
    >
      <div className="flex min-w-0 items-start justify-between gap-2">
        <div className="flex min-w-0 flex-1 items-start gap-2">
          {isNestedSession ? (
            <span aria-hidden="true" className={cn('mt-0.5 grid h-4 w-4 shrink-0 place-items-center rounded-full border border-[var(--app-border)] bg-[var(--app-bg-alt)]', nestedToneClass)}>
              {childKind === 'subagent' ? <Bot size={9} /> : <span className="h-1.5 w-1.5 rounded-full bg-current opacity-70" />}
            </span>
          ) : null}
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 items-center gap-2">
              <span className={cn('min-w-0 flex-1 truncate font-medium text-[var(--app-text)]', isNestedSession ? 'text-[12px]' : 'text-[13px]')}>
                {rowTitle}
              </span>

            </div>
            <div className="mt-0.5 flex min-w-0 items-center justify-between gap-2 text-[10px] leading-4 text-[var(--app-text-subtle)]">
              <span className="min-w-0 truncate">{metadataLabel}</span>
              {rowTimerLabel ? <span className="ml-auto shrink-0 text-right tabular-nums text-[var(--app-text-muted)]">{rowTimerLabel}</span> : null}
            </div>
          </div>
        </div>
        <span className="inline-flex shrink-0 items-center gap-1.5 text-[10px] leading-4 text-[var(--app-text-muted)]">
          {compactingActive ? <LoaderCircle size={10} className="animate-spin text-[var(--app-primary)]" aria-hidden="true" /> : null}
          {rightSideLabel ? <span className="max-w-[5.5rem] truncate text-right">{rightSideLabel}</span> : null}
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
        </span>
      </div>

      {isPlanRow ? (
        <div className="flex min-w-0 items-start justify-between gap-2 text-[10px] leading-4 text-[var(--app-text-subtle)]">
          <div className="grid min-w-0 flex-1 gap-0.5 overflow-hidden">
            <span className="shrink-0 font-mono font-semibold uppercase tracking-[0.08em] text-[var(--app-text-muted)]" aria-label={checkpointProgressLabel || undefined}>
              {checkpointProgressText ? `CP ${checkpointProgressText}` : 'CP'}
            </span>
            <span className="truncate">{planCheckpointMeta}</span>
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {agentToggleControl}
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
            {visibleChildLabel ? (
              <span className={cn('truncate', childKind === 'subagent' ? 'text-[var(--app-info)]' : 'text-[var(--app-text-subtle)]')}>
                {visibleChildLabel}
              </span>
            ) : null}
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {!isPlanRow ? agentToggleControl : null}
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
  onSelect: (sessionId: string) => void
  onPrefetch: (sessionId: string) => void
  onToggleAgents: (sessionId: string) => void
}

const SIDEBAR_SESSION_GROUPS = [
  { id: 'needs_review', label: 'Needs Review' },
  { id: 'in_progress', label: 'In Progress' },
  { id: 'active_chats', label: 'Active Chats' },
  { id: 'archived', label: 'Archived' },
] as const

function renderSidebarSessionGroups(input: RenderSidebarSessionGroupsInput): JSX.Element[] | null {
  if (input.nodes.length === 0) return null
  const grouped = new Map<(typeof SIDEBAR_SESSION_GROUPS)[number]['id'], SidebarSessionNode[]>()
  for (const group of SIDEBAR_SESSION_GROUPS) {
    grouped.set(group.id, [])
  }
  for (const node of input.nodes) {
    grouped.get(sessionSidebarGroup(node.session))?.push(node)
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
            onSelect={input.onSelect}
            onPrefetch={input.onPrefetch}
            onToggleAgents={input.onToggleAgents}
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
  const workspaceFlowDetailMatch = matchRoute({ to: '/$workspaceSlug/flow/$flowId', fuzzy: false })
  const workspaceFlowMatch = matchRoute({ to: '/$workspaceSlug/flow', fuzzy: false })
  const workspaceSessionMatch = matchRoute({ to: '/$workspaceSlug/$sessionId', fuzzy: false })
  const workspaceMatch = matchRoute({ to: '/$workspaceSlug', fuzzy: false })
  const isFlowRoute = Boolean(workspaceFlowDetailMatch || workspaceFlowMatch)
  const routeWorkspaceSlug = (workspaceFlowDetailMatch ? workspaceFlowDetailMatch.workspaceSlug : workspaceFlowMatch ? workspaceFlowMatch.workspaceSlug : workspaceSessionMatch ? workspaceSessionMatch.workspaceSlug : workspaceMatch ? workspaceMatch.workspaceSlug : '').trim()
  const routeSessionId = (!isFlowRoute && workspaceSessionMatch ? workspaceSessionMatch.sessionId : '').trim()
  const pwaDebugEnabled = typeof window !== 'undefined' && new URLSearchParams(window.location.search).has(PWA_DEBUG_QUERY_PARAM)
  const { workspaces, loading: launcherWorkspacesLoading } = useWorkspaceLauncher({ applyDocumentTheme: false, autoRefresh: false, browseDuringRefresh: false })
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [expandedAgentSessions, setExpandedAgentSessions] = useState<Record<string, boolean>>({})
  const [pairingRequestsOpen, setPairingRequestsOpen] = useState(false)
  const [pendingPairingRequests, setPendingPairingRequests] = useState<RemoteSwarmPendingPairing[]>([])
  const [pairingDecisionBusyID, setPairingDecisionBusyID] = useState<string | null>(null)
  const [pairingConfirmations, setPairingConfirmations] = useState<Record<string, boolean>>({})
  const [pairingRequestError, setPairingRequestError] = useState<string | null>(null)
  const [pairingRequestStatus, setPairingRequestStatus] = useState<string | null>(null)
  const [pairingReplicationTarget, setPairingReplicationTarget] = useState<SwarmTarget | null>(null)
  const [todoModal, setTodoModal] = useState<TodoModalState | null>(null)
  const [gitPanel, setGitPanel] = useState<GitPanelState | null>(null)
  const [planModal, setPlanModal] = useState<PlanModalState | null>(null)
  const [planModalLoading, setPlanModalLoading] = useState(false)
  const [planModalSaving, setPlanModalSaving] = useState(false)
  const [planModalExecuting, setPlanModalExecuting] = useState(false)
  const [planModalError, setPlanModalError] = useState<string | null>(null)
  const [quickSettingsTab, setQuickSettingsTab] = useState<QuickSettingsTabID | null>(null)
  const [gitRealtimeErrors, setGitRealtimeErrors] = useState<Record<string, string>>({})
  const [todoItems, setTodoItems] = useState<Record<string, WorkspaceTodoItem[]>>({})
  const [todoSummaries, setTodoSummaries] = useState<Record<string, WorkspaceTodoSummary>>({})
  const [swarmMenu, setSwarmMenu] = useState<SwarmTargetMenuState>({ open: false })
  const [editingSidebarSwarmName, setEditingSidebarSwarmName] = useState(false)
  const [sidebarSwarmNameDraft, setSidebarSwarmNameDraft] = useState('')
  const [sidebarSwarmNameSaving, setSidebarSwarmNameSaving] = useState(false)
  const [sidebarSwarmNameError, setSidebarSwarmNameError] = useState<string | null>(null)
  const [flowMenuOpen, setFlowMenuOpen] = useState(false)
  const [flowBusyID, setFlowBusyID] = useState<string | null>(null)
  const [flowMenuError, setFlowMenuError] = useState<string | null>(null)
  const [updateRunning, setUpdateRunning] = useState(false)
  const [updateError, setUpdateError] = useState<string | null>(null)
  const [updateProgress, setUpdateProgress] = useState<DesktopUpdateProgressState>({ open: false, job: null, startedAt: null })
  const [desktopToast, setDesktopToast] = useState<DesktopToastState | null>(() => loadPendingDesktopToast())
  const [worktreeSessionModal, setWorktreeSessionModal] = useState<WorktreeSessionModalState | null>(null)
  const [worktreeSessionTitle, setWorktreeSessionTitle] = useState('')
  const [worktreeSessionBranch, setWorktreeSessionBranch] = useState('')
  const [worktreeSessionCreating, setWorktreeSessionCreating] = useState(false)
  const [worktreeSessionError, setWorktreeSessionError] = useState<string | null>(null)
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [localContainerUpdateConfirm, setLocalContainerUpdateConfirm] = useState<LocalContainerUpdateConfirmState | null>(null)
  const [todoSavingWorkspacePath, setTodoSavingWorkspacePath] = useState<string | null>(null)
  const [workspaceLayout, setWorkspaceLayout] = useState<Record<string, SidebarWorkspaceLayout>>(() => loadSidebarWorkspaceLayout())
  const [sidebarWorkspaceControlPath, setSidebarWorkspaceControlPath] = useState('')
  const [compactingSession, setCompactingSession] = useState<DesktopV3CompactingSessionState | null>(null)
  const [sidebarNow, setSidebarNow] = useState(() => Date.now())
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
  const flowsQuery = useQuery({
    queryKey: flowsQueryKey,
    queryFn: ({ signal }) => fetchFlows(signal),
    staleTime: 15_000,
  })
  const updateStatusQuery = useQuery({
    queryKey: ['desktop-update-status'] as const,
    queryFn: () => fetchDesktopUpdateStatus(),
    staleTime: UPDATE_STATUS_REFETCH_INTERVAL_MS,
    refetchInterval: UPDATE_STATUS_REFETCH_INTERVAL_MS,
    refetchIntervalInBackground: true,
  })

  const updateStatus = updateStatusQuery.data ?? null
  const effectiveUISettings = uiSettings ?? uiSettingsQuery.data ?? null
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
  const sortedSwarmTargets = useMemo(() => [...swarmTargets]
    .sort((left, right) => {
      if (left.current !== right.current) {
        return left.current ? -1 : 1
      }
      if (left.online !== right.online) {
        return left.online ? -1 : 1
      }
      return left.name.localeCompare(right.name)
    }), [swarmTargets])
  const selfSwarmTargets = useMemo(() => sortedSwarmTargets.filter((target) => target.kind === 'self' || target.current), [sortedSwarmTargets])
  const localSwarmTargets = useMemo(() => sortedSwarmTargets.filter((target) => target.kind === 'local' && !target.current), [sortedSwarmTargets])
  const remoteSwarmTargets = useMemo(() => sortedSwarmTargets.filter((target) => (target.kind === 'remote' || target.kind === 'host' || target.kind === 'mirrored') && !target.current), [sortedSwarmTargets])
  const swarmTargetCounts = useMemo(() => {
    const local = selfSwarmTargets.length + localSwarmTargets.length
    const remote = remoteSwarmTargets.length
    const offline = sortedSwarmTargets.filter((target) => !target.online && !target.current).length
    return { local, remote, offline }
  }, [localSwarmTargets.length, remoteSwarmTargets, selfSwarmTargets.length, sortedSwarmTargets])
  const swarmTargetSummary = `${swarmTargetCounts.local} local · ${swarmTargetCounts.remote} host/remote${swarmTargetCounts.offline > 0 ? ` · ${swarmTargetCounts.offline} offline` : ''}`
  const swarmTargetCountLabel = `${swarmTargets.length} ${swarmTargets.length === 1 ? 'swarm' : 'swarms'}`
  const activePairingRequests = useMemo(() => activePendingPairings(pendingPairingRequests), [pendingPairingRequests])
  const pairingRequestCount = activePairingRequests.length
  const pairingRequestAttentionVisible = pairingRequestCount > 0
  const headerActionCount = 2 + (pairingRequestAttentionVisible ? 1 : 0) + (updateAttentionVisible ? 1 : 0)
  const headerActionRowClass = headerActionCount === 4
    ? 'grid min-w-0 grid-cols-[minmax(0,1fr)_108px] items-center gap-2.5 min-h-7 pr-4'
    : headerActionCount === 3
      ? 'grid min-w-0 grid-cols-[minmax(0,1fr)_80px] items-center gap-2.5 min-h-7 pr-4'
      : cn(SIDEBAR_ACTION_ROW_CLASS, 'min-h-7 pr-4')
  const headerActionRailClass = headerActionCount === 4
    ? '!w-[108px] !grid-cols-[24px_24px_24px_24px]'
    : headerActionCount === 3
      ? '!w-[80px] !grid-cols-[24px_24px_24px]'
      : undefined
  const sidebarFlows = useMemo(() => (flowsQuery.data ?? []).map(sidebarFlowRow), [flowsQuery.data])
  const flowCount = sidebarFlows.length
  const activeFlowCount = sidebarFlows.filter((flow) => flow.enabled).length
  const flowSummary = flowsQuery.isLoading ? 'loading flows' : `${flowCount} flows${activeFlowCount !== flowCount ? ` · ${activeFlowCount} active` : ''}`
  const selectedWorkspaceFlowRows = useMemo(() => {
    const workspacePath = selectedWorkspacePath?.trim() ?? ''
    if (!workspacePath) return sidebarFlows
    return sidebarFlows.filter((flow) => {
      const flowWorkspace = flow.raw.workspace_detail?.workspace_path?.trim()
        || flow.raw.definition.workspace.workspace_path?.trim()
        || flow.raw.definition.workspace.host_workspace_path?.trim()
        || ''
      return !flowWorkspace || flowWorkspace === workspacePath
    })
  }, [selectedWorkspacePath, sidebarFlows])
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
  const [swarmSwitchError, setSwarmSwitchError] = useState<string | null>(null)

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

  const handleOpenPairingRequests = useCallback(() => {
    setPairingRequestsOpen(true)
    setPairingRequestStatus(null)
  }, [])

  const handlePairingDecision = useCallback(async (request: RemoteSwarmPendingPairing, approve: boolean) => {
    const requestID = request.request_id.trim()
    if (!requestID) {
      setPairingRequestError('Pairing request id is missing.')
      return
    }
    setPairingDecisionBusyID(requestID)
    setPairingRequestError(null)
    setPairingRequestStatus(null)
    try {
      const result = await approveRemoteSwarmPairing({
        requestID,
        approve,
        confirmed: approve ? pairingConfirmations[requestID] === true : undefined,
        reason: approve ? undefined : 'Rejected from Link request modal',
      })
      setPendingPairingRequests((items) => items.filter((item) => item.request_id !== requestID))
      setPairingConfirmations((current) => {
        const next = { ...current }
        delete next[requestID]
        return next
      })
      if (approve) {
        const target = managedHostTargetFromPairingResult({ request, result })
        if (target) {
          setPairingReplicationTarget(target)
          setPairingRequestsOpen(true)
        }
      } else {
        setPairingReplicationTarget(null)
      }
      setPairingRequestStatus(approve ? `Approved ${request.managed_name || request.managed_swarm_id || 'Managed Host'}. Workspace link/import review is ready.` : `Rejected link request ${requestID}.`)
      void queryClient.invalidateQueries({ queryKey: ['swarm-targets'] })
    } catch (error) {
      setPairingRequestError(error instanceof Error ? error.message : 'Failed to update link request')
    } finally {
      setPairingDecisionBusyID(null)
    }
  }, [pairingConfirmations, queryClient])

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

  const handleSelectSwarmTarget = useCallback(async (target: SwarmTarget) => {
    setSwarmSwitchError(null)
    if (target.current || target.kind === 'self') {
      try {
        await selectSwarmTarget(target.swarm_id)
        await queryClient.invalidateQueries({ queryKey: ['swarm-targets'] })
      } catch (error) {
        const message = error instanceof Error ? error.message : 'Failed to return to master'
        setSwarmSwitchError(message)
      } finally {
        setSwarmMenu({ open: false })
      }
      return
    }

    const openURL = swarmTargetOpenURL(target)
    if (!target.online) {
      setSwarmSwitchError(`${target.name || 'This swarm'} is offline.`)
      setSwarmMenu({ open: false })
      void queryClient.invalidateQueries({ queryKey: ['swarm-targets'] })
      return
    }
    if (!openURL) {
      setSwarmSwitchError(`${target.name || 'This swarm'} does not have a desktop URL yet.`)
      return
    }
    window.open(openURL, '_blank', 'noopener,noreferrer')
    setSwarmMenu({ open: false })
  }, [queryClient])

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
  const globalFlattenedSessionNodes = useMemo(
    () => flattenVisibleSidebarSessionNodes(globalSidebarSessionNodes, expandedAgentSessions, routeSessionId),
    [expandedAgentSessions, globalSidebarSessionNodes, routeSessionId],
  )

  const sessionById = useMemo<Map<string, DesktopSessionRecord>>(
    () => new Map(desktopStateSessions.map((session) => [session.id, session] as const)),
    [desktopStateSessions],
  )
  const workspaceSlugByPath = useMemo(() => {
    const routeWorkspaces: Array<Pick<WorkspaceEntry, 'path' | 'workspaceName'>> = mergedSidebarWorkspaceEntries.map((workspace) => ({
      path: workspace.path,
      workspaceName: workspace.workspaceName,
    }))
    const seenPaths = new Set(routeWorkspaces.map((workspace) => workspace.path))
    for (const session of desktopStateSessions) {
      const path = desktopSidebarWorkspacePathForSession(session, workspacePathByBindingId)
      if (!path || seenPaths.has(path)) continue
      seenPaths.add(path)
      routeWorkspaces.push({ path, workspaceName: session.workspaceName || fallbackWorkspaceNameFromPath(path) })
    }
    return buildWorkspaceRouteSlugMap(routeWorkspaces)
  }, [desktopStateSessions, mergedSidebarWorkspaceEntries, workspacePathByBindingId])
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
  const globalSessionWorkspaceSlug = useCallback((session: DesktopSessionRecord): string => {
    const workspacePath = desktopSidebarWorkspacePathForSession(session, workspacePathByBindingId)
      || selectedWorkspacePath
      || visibleWorkspacePaths[0]
      || ''
    if (!workspacePath) return topWorkspaceSlug || routeWorkspaceSlug || 'workspace'
    return workspaceSlugByPath.get(workspacePath)
      ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: session.workspaceName || fallbackWorkspaceNameFromPath(workspacePath) })
  }, [routeWorkspaceSlug, selectedWorkspacePath, topWorkspaceSlug, visibleWorkspacePaths, workspacePathByBindingId, workspaceSlugByPath])
  const globalSessionRouteOptions = useMemo(() => buildDesktopChatRouteOptions({
    hostSwarmName: swarmName,
    workspacePath: topWorkspacePath,
    workspaceName: topWorkspaceLabel,
    topologyRoutes: topWorkspace?.topologyRoutes ?? [],
  }), [swarmName, topWorkspace?.topologyRoutes, topWorkspaceLabel, topWorkspacePath])

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
    const workspacePath = desktopSidebarWorkspacePathForSession(session, workspacePathByBindingId)
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
  }, [navigate, routeSessionId, routeWorkspaceSlug, sessionById, workspacePathByBindingId, workspaceSlugByPath])



  const handleSelectSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    void selectAndHydrateDesktopV3Session(normalizedSessionId)
    const session = sessionById.get(normalizedSessionId)
    if (!session) {
      return
    }
    const workspacePath = desktopSidebarWorkspacePathForSession(session, workspacePathByBindingId)
    if (!workspacePath) {
      return
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
  }, [navigate, sessionById, workspacePathByBindingId, workspaceSlugByPath])



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
      ? desktopSidebarWorkspacePathForSession(routeSession, workspacePathByBindingId)
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
  }, [handleOpenWorkspace, navigate, routeWorkspace?.path, routeWorkspace?.workspaceName, routeWorkspaceSlug, selectedWorkspace?.workspaceName, selectedWorkspacePath, sessionById, workspacePathByBindingId])

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
      settingsLoading: true,
    })
    setWorktreeSessionTitle('')
    setWorktreeSessionBranch('')
    setWorktreeSessionError(null)
    void fetchWorktreeBranchPrefix(workspacePath)
      .then((branchPrefix) => {
        setWorktreeSessionModal((current) => current?.workspacePath === workspacePath
          ? { ...current, branchPrefix, settingsLoading: false }
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
    const branchSuffix = normalizeWorktreeBranchSuffix(worktreeSessionBranch)
    const branchPrefix = normalizeWorktreeBranchPrefix(worktreeSessionModal.branchPrefix)
    const branch = composeWorktreeBranchName(branchPrefix, branchSuffix)
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
    if (!branchSuffix) {
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
        worktree: { mode: 'on', branchName: branch },
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
  }, [agentStateQuery.data?.activePrimary, draftPreferenceQuery.data?.preference, navigate, worktreeSessionBranch, worktreeSessionCreating, worktreeSessionModal, worktreeSessionTitle])

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

  const handleSavePlanModal = useCallback(async (planText: string, document?: Record<string, unknown>) => {
    const sessionId = planModal?.sessionId.trim() ?? ''
    if (!sessionId) return
    setPlanModalSaving(true)
    setPlanModalError(null)
    try {
      await saveDesktopV3SessionPlan(sessionId, {
        id: planModalPlan?.id,
        title: planModalPlan?.title || 'Current Plan',
        plan: planText,
        document,
        status: planModalPlan?.status || undefined,
        approvalState: planModalPlan?.approvalState || undefined,
      })
    } catch (error) {
      setPlanModalError(error instanceof Error ? error.message : String(error))
      throw error
    } finally {
      setPlanModalSaving(false)
    }
  }, [planModal?.sessionId, planModalPlan?.approvalState, planModalPlan?.id, planModalPlan?.status, planModalPlan?.title])

  const handleApproveStartPlanModal = useCallback(async (input: { executionGranularity: 'checkpointed' | 'run_through'; continueAutomatically: boolean }) => {
    const sessionId = planModal?.sessionId.trim() ?? ''
    if (!sessionId || !planModalPlan?.id) return
    setPlanModalExecuting(true)
    setPlanModalError(null)
    try {
      if (input.executionGranularity === 'run_through') {
        await startDesktopPlanAutomatic(sessionId, planModalPlan.id, { executionGranularity: input.executionGranularity })
      } else {
        await startDesktopPlanCheckpointed(sessionId, planModalPlan.id, {
          executionGranularity: input.executionGranularity,
          continuationPolicy: input.continueAutomatically ? 'automatic' : 'review_each_checkpoint',
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
    setMobileSidebarOpen(false)
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/settings', params: { workspaceSlug: routeWorkspaceSlug }, search: { tab } })
      return
    }
    void navigate({ to: '/settings', search: { tab } })
  }, [navigate, routeWorkspaceSlug])

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
        setSwarmMenu({ open: false })
        setFlowMenuOpen(false)
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

  const handleOpenFlowsSettings = useCallback(() => {
    setFlowMenuOpen(false)
    setMobileSidebarOpen(false)
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/flow', params: { workspaceSlug: routeWorkspaceSlug } })
      return
    }
    const workspacePath = selectedWorkspace?.path || selectedWorkspacePath || ''
    if (workspacePath) {
      const workspaceSlug = workspaceSlugByPath.get(workspacePath)
        ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: selectedWorkspace?.workspaceName ?? '' })
      void navigate({ to: '/$workspaceSlug/flow', params: { workspaceSlug } })
      return
    }
    void navigate({ to: '/flow' })
  }, [navigate, routeWorkspaceSlug, selectedWorkspace?.path, selectedWorkspace?.workspaceName, selectedWorkspacePath, workspaceSlugByPath])

  const handleOpenFlow = useCallback((flow: SidebarFlowRow) => {
    setFlowMenuOpen(false)
    setMobileSidebarOpen(false)
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/flow/$flowId', params: { workspaceSlug: routeWorkspaceSlug, flowId: flow.id } })
      return
    }
    const workspacePath = flow.raw.workspace_detail?.workspace_path?.trim()
      || flow.raw.definition.workspace.workspace_path?.trim()
      || flow.raw.definition.workspace.host_workspace_path?.trim()
      || selectedWorkspace?.path
      || selectedWorkspacePath
      || ''
    if (workspacePath) {
      const workspaceSlug = workspaceSlugByPath.get(workspacePath)
        ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: fallbackWorkspaceNameFromPath(workspacePath) })
      void navigate({ to: '/$workspaceSlug/flow/$flowId', params: { workspaceSlug, flowId: flow.id } })
      return
    }
    void navigate({ to: '/flow/$flowId', params: { flowId: flow.id } })
  }, [navigate, routeWorkspaceSlug, selectedWorkspace?.path, selectedWorkspacePath, workspaceSlugByPath])

  const handleToggleFlowEnabled = useCallback(async (flow: SidebarFlowRow) => {
    if (flowBusyID) return
    setFlowBusyID(flow.id)
    setFlowMenuError(null)
    try {
      await setFlowEnabled(flow.id, !flow.enabled)
      await queryClient.invalidateQueries({ queryKey: flowsQueryKey })
    } catch (error) {
      setFlowMenuError(error instanceof Error ? error.message : 'Failed to update flow')
    } finally {
      setFlowBusyID(null)
    }
  }, [flowBusyID, queryClient])



  const handleOpenSwarmDashboard = useCallback(() => {
    handleOpenSettingsTab('swarm')
  }, [handleOpenSettingsTab])

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
    if (updateRunning || localContainerUpdateConfirm) {
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
    let settings = effectiveUISettings
    if (!settings) {
      try {
        settings = await uiSettingsQuery.refetch().then((result) => result.data ?? null)
      } catch {
        settings = null
      }
    }
    try {
      const remoteSessions = await fetchRemoteDeploySessions()
      const remoteUpdateCount = remoteDeployUpdateSessionCount(remoteSessions)
      const managedHostCount = status.dev_mode ? managedHostUpdateTargetCount(swarmTargetsQuery.data?.targets ?? []) : 0
      const warningDismissed = localContainerUpdateWarningDismissed(settings)
      if (!warningDismissed || remoteUpdateCount > 0 || managedHostCount > 0) {
        const plan = await fetchLocalContainerUpdatePlan({ devMode: status.dev_mode, targetVersion: status.latest_version, postRebuildCheck: status.dev_mode })
        if ((!warningDismissed && localContainerUpdateAffected(plan)) || remoteUpdateCount > 0 || managedHostCount > 0) {
          setLocalContainerUpdateConfirm({ plan, remoteSessions, managedHostCount, pendingDismiss: false })
          return
        }
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to check container images before update'
      setUpdateError(message)
      return
    }
    await runDesktopUpdate()
  }, [effectiveUISettings, localContainerUpdateConfirm, runDesktopUpdate, swarmTargetsQuery.data?.targets, uiSettingsQuery, updateRunning, updateStatus, updateStatusError, updateStatusQuery])

  const handleConfirmLocalContainerUpdate = useCallback(async () => {
    const confirmState = localContainerUpdateConfirm
    if (!confirmState || updateRunning) {
      return
    }
    if (confirmState.pendingDismiss) {
      try {
        const saved = await saveLocalContainerUpdateWarningDismissal(true)
        setUISettings(saved)
        queryClient.setQueryData(uiSettingsQueryKey(), saved)
        queryClient.setQueryData(['ui-settings', 'swarm'], normalizeSwarmSettings(saved))
      } catch (error) {
        const message = error instanceof Error ? error.message : 'Failed to save container image update warning setting'
        setUpdateError(message)
        return
      }
    }
    setLocalContainerUpdateConfirm(null)
    setUpdateError(null)
    await runDesktopUpdate()
  }, [localContainerUpdateConfirm, queryClient, runDesktopUpdate, updateRunning])

  const handleCancelLocalContainerUpdate = useCallback(() => {
    setLocalContainerUpdateConfirm(null)
    setUpdateError(null)
  }, [])

  const handleCloseUpdateProgress = useCallback(() => {
    if (updateRunning) {
      return
    }
    setUpdateProgress((current) => ({ ...current, open: false }))
  }, [updateRunning])

  const handleToggleLocalContainerUpdateDismissal = useCallback((checked: boolean) => {
    setLocalContainerUpdateConfirm((current) => current ? { ...current, pendingDismiss: checked } : current)
  }, [])

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

  const handleWorkspaceSelect = useCallback((event: ChangeEvent<HTMLSelectElement>) => {
    const workspacePath = event.target.value.trim()
    if (!workspacePath) return
    const workspace = mergedSidebarWorkspaceEntries.find((entry) => entry.path === workspacePath)
    if (!workspace) return
    setSidebarWorkspaceControlPath(workspace.path)
  }, [mergedSidebarWorkspaceEntries])

  const handleToggleFlowMenu = useCallback(() => {
    setSwarmMenu({ open: false })
    setFlowMenuOpen((open) => !open)
  }, [])

  useEffect(() => {
    setMobileSidebarOpen(false)
  }, [routeSessionId, routeWorkspaceSlug])

  const openSwarmMenu = useCallback(() => {
    setFlowMenuOpen(false)
    setSwarmMenu((current) => ({ open: !current.open }))
  }, [])

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
            <Home size={24} className="shrink-0" />
          </Button>
          <Button variant="ghost" className="h-12 w-12 min-w-12 p-0" onClick={handleOpenSwarmDashboard} aria-label="Open swarm dashboard">
            <Settings size={24} className="shrink-0" />
          </Button>
          {pairingRequestAttentionVisible ? (
            <Button variant="ghost" className="relative h-12 w-12 min-w-12 p-0 text-[var(--app-primary)]" onClick={handleOpenPairingRequests} aria-label="Open link requests" title={`${pairingRequestCount} pending link request${pairingRequestCount === 1 ? '' : 's'}`}>
              <Link2 size={24} className="shrink-0" />
              <span aria-hidden="true" className="absolute right-2 top-2 grid h-4 min-w-4 place-items-center rounded-full bg-[var(--app-warning)] px-1 text-[9px] font-semibold text-[var(--app-background)]">{pairingRequestCount}</span>
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
                    {pairingRequestAttentionVisible ? (
                      <button
                        type="button"
                        className={cn(SIDEBAR_ACTION_BUTTON_CLASS, 'relative text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] hover:text-[var(--app-primary-hover)]')}
                        onClick={handleOpenPairingRequests}
                        aria-label="Open link requests"
                        title={`${pairingRequestCount} pending link request${pairingRequestCount === 1 ? '' : 's'}`}
                      >
                        <Link2 size={14} strokeWidth={1.8} className="shrink-0" />
                        <span aria-hidden="true" className="absolute right-0.5 top-0.5 h-1.5 w-1.5 rounded-full bg-[var(--app-warning)] shadow-[0_0_8px_var(--app-warning)]" />
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
                  <div>
                    <div
                      className={cn(
                        'grid min-h-[30px] w-full grid-cols-[minmax(0,1fr)_28px] items-center rounded-md text-[var(--app-text-muted)]',
                        swarmMenu.open && 'bg-[var(--app-surface-active)] text-[var(--app-text)]',
                      )}
                    >
                      {routeWorkspaceSlug ? (
                        <Link
                          to="/$workspaceSlug/settings"
                          params={{ workspaceSlug: routeWorkspaceSlug }}
                          search={{ tab: 'swarm' }}
                          className="grid min-h-[30px] min-w-0 grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-l-md px-2 text-left font-inherit hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                          onClick={() => {
                            setQuickSettingsTab(null)
                            setMobileSidebarOpen(false)
                          }}
                          aria-label="Open swarm settings"
                          title={swarmTargetSummary}
                        >
                          <Box size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                          <span className="min-w-0 truncate">{swarmTargetCountLabel}</span>
                        </Link>
                      ) : (
                        <Link
                          to="/settings"
                          search={{ tab: 'swarm' }}
                          className="grid min-h-[30px] min-w-0 grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-l-md px-2 text-left font-inherit hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                          onClick={() => {
                            setQuickSettingsTab(null)
                            setMobileSidebarOpen(false)
                          }}
                          aria-label="Open swarm settings"
                          title={swarmTargetSummary}
                        >
                          <Box size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                          <span className="min-w-0 truncate">{swarmTargetCountLabel}</span>
                        </Link>
                      )}
                      <button
                        type="button"
                        className="grid min-h-[30px] place-items-center rounded-r-md hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                        onClick={openSwarmMenu}
                        aria-expanded={swarmMenu.open}
                        aria-label={`${swarmMenu.open ? 'Collapse' : 'Expand'} swarm list`}
                        title={`${swarmMenu.open ? 'Collapse' : 'Expand'} swarms`}
                      >
                        <ChevronDown size={13} strokeWidth={1.8} className={cn('transition-transform', swarmMenu.open && 'rotate-180')} />
                      </button>
                    </div>
                    {swarmMenu.open ? (
                      <div className="py-1 pl-5">
                        {swarmTargets.length === 0 ? (
                          <div className="px-2 py-1.5 text-[11px] text-[var(--app-text-subtle)]">No swarms.</div>
                        ) : swarmTargets.map((target) => {
                          const openURL = swarmTargetOpenURL(target)
                          const statusLabel = swarmTargetStatusLabel(target)
                          const secondaryLabel = swarmTargetSecondaryLabel(target, swarmTargets)
                          return (
                            <button
                              key={target.swarm_id}
                              type="button"
                              onClick={() => { void handleSelectSwarmTarget(target) }}
                              className={cn(
                                SIDEBAR_ACTION_ROW_CLASS,
                                'min-h-[30px] w-full px-[7px] py-[5px] text-left text-[12px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                                target.current && 'bg-[var(--app-surface-active)] text-[var(--app-text)] shadow-[inset_2px_0_0_var(--app-success)]',
                                !target.online && !target.current && 'opacity-65',
                              )}
                              title={swarmTargetTitle(target, swarmTargets)}
                            >
                              <span className="flex min-w-0 items-start gap-2">
                                <span className={cn('mt-[6px] h-[5px] w-[5px] shrink-0 rounded-full', swarmKindDotClass(target.kind, target.online))} />
                                <span className="grid min-w-0 gap-0.5">
                                  <span className="truncate">{swarmTargetPrimaryLabel(target)}</span>
                                  {target.kind === 'mirrored' ? (
                                    <span className="truncate text-[10px] leading-tight text-[var(--app-text-subtle)]">{secondaryLabel}</span>
                                  ) : null}
                                </span>
                                {!target.current && target.online && openURL ? <ExternalLink size={11} strokeWidth={1.8} className="mt-[2px] shrink-0 opacity-70" /> : null}
                              </span>
                              <span className="shrink-0 truncate text-right text-[10px] text-[var(--app-text-subtle)]">
                                {target.kind === 'mirrored' ? statusLabel : secondaryLabel}
                              </span>
                            </button>
                          )
                        })}
                        {swarmSwitchError ? <div className="mx-1 mt-1 border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-2 py-1.5 text-[10px] text-[var(--app-warning)]">{swarmSwitchError}</div> : null}
                        <button
                          type="button"
                          className="mt-1 flex min-h-[30px] w-full items-center gap-2 px-[7px] py-[5px] text-left text-[12px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                          onClick={handleOpenSwarmDashboard}
                        >
                          <Plus size={14} className="shrink-0" />
                          Add / manage swarms
                        </button>
                      </div>
                    ) : null}
                  </div>

                  <div>
                    <div
                      className={cn(
                        'grid min-h-[30px] w-full grid-cols-[minmax(0,1fr)_28px] items-center rounded-md text-[var(--app-text-muted)]',
                        flowMenuOpen && 'bg-[var(--app-surface-active)] text-[var(--app-text)]',
                      )}
                    >
                      {routeWorkspaceSlug ? (
                        <Link
                          to="/$workspaceSlug/flow"
                          params={{ workspaceSlug: routeWorkspaceSlug }}
                          className="grid min-h-[30px] min-w-0 grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-l-md px-2 text-left font-inherit hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                          onClick={() => {
                            setFlowMenuOpen(false)
                            setMobileSidebarOpen(false)
                          }}
                          aria-label="Open flow settings"
                          title={flowSummary}
                        >
                          <Workflow size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                          <span className="min-w-0 truncate">{flowCount} flows</span>
                        </Link>
                      ) : (
                        <Link
                          to="/flow"
                          className="grid min-h-[30px] min-w-0 grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-l-md px-2 text-left font-inherit hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                          onClick={() => {
                            setFlowMenuOpen(false)
                            setMobileSidebarOpen(false)
                          }}
                          aria-label="Open flow settings"
                          title={flowSummary}
                        >
                          <Workflow size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                          <span className="min-w-0 truncate">{flowCount} flows</span>
                        </Link>
                      )}
                      <button
                        type="button"
                        className="grid min-h-[30px] place-items-center rounded-r-md hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-progress disabled:opacity-70"
                        onClick={handleToggleFlowMenu}
                        disabled={flowsQuery.isFetching}
                        aria-expanded={flowMenuOpen}
                        aria-label={`${flowMenuOpen ? 'Collapse' : 'Expand'} flow list`}
                        title={`${flowMenuOpen ? 'Collapse' : 'Expand'} flows`}
                      >
                        {flowsQuery.isFetching ? <LoaderCircle size={11} strokeWidth={1.8} className="animate-spin" /> : <ChevronDown size={13} strokeWidth={1.8} className={cn('transition-transform', flowMenuOpen && 'rotate-180')} />}
                      </button>
                    </div>
                    {flowMenuOpen ? (
                      <div className="py-1 pl-5">
                        {flowsQuery.isLoading ? (
                          <div className="px-2 py-2 text-xs text-[var(--app-text-subtle)]">Loading flows…</div>
                        ) : flowsQuery.isError ? (
                          <div className="px-2 py-2 text-xs text-[var(--app-warning)]">Flows unavailable.</div>
                        ) : sidebarFlows.length === 0 ? (
                          <div className="px-2 py-2 text-xs text-[var(--app-text-subtle)]">No flows yet.</div>
                        ) : sidebarFlows.slice(0, 8).map((flow) => {
                          const busy = flowBusyID === flow.id
                          return (
                            <div key={flow.id} className="group grid min-h-[40px] grid-cols-[minmax(0,1fr)_58px] items-center gap-2 px-[7px] py-1.5 text-xs text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                              <button type="button" className="min-w-0 text-left" onClick={() => handleOpenFlow(flow)} title={flow.detail}>
                                <span className="flex min-w-0 items-center gap-1.5">
                                  <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', sidebarFlowDotClass(flow.status))} />
                                  <span className="truncate text-[var(--app-text)]">{flow.name}</span>
                                </span>
                                <span className="mt-1 block truncate text-[10px] leading-4 text-[var(--app-text-subtle)]">{sidebarFlowStatusLabel(flow.status)} · {flow.agent}</span>
                              </button>
                              <button
                                type="button"
                                className="justify-self-end rounded border border-[var(--app-border)] px-1.5 py-1 text-[10px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:cursor-progress disabled:opacity-60"
                                onClick={() => { void handleToggleFlowEnabled(flow) }}
                                disabled={Boolean(flowBusyID)}
                                aria-label={`${flow.enabled ? 'Pause' : 'Start'} ${flow.name}`}
                                title={flow.enabled ? 'Pause flow' : 'Start flow'}
                              >
                                {busy ? <LoaderCircle size={11} className="animate-spin" /> : flow.enabled ? <Pause size={11} /> : <Play size={11} />}
                              </button>
                            </div>
                          )
                        })}
                        {sidebarFlows.length > 8 ? <div className="px-2 py-1 text-[11px] text-[var(--app-text-subtle)]">+{sidebarFlows.length - 8} more on the Flow page</div> : null}
                        {flowMenuError ? <div className="mx-1 mt-1 border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-2 py-1.5 text-[11px] text-[var(--app-warning)]">{flowMenuError}</div> : null}
                        {routeWorkspaceSlug ? (
                          <Link
                            to="/$workspaceSlug/flow"
                            params={{ workspaceSlug: routeWorkspaceSlug }}
                            className="mt-1 flex min-h-[30px] w-full items-center gap-2 px-[7px] py-[5px] text-left text-[12px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                            onClick={() => {
                              setFlowMenuOpen(false)
                              setMobileSidebarOpen(false)
                            }}
                          >
                            <Workflow size={14} className="shrink-0" />
                            Add / manage flows
                          </Link>
                        ) : (
                          <Link
                            to="/flow"
                            className="mt-1 flex min-h-[30px] w-full items-center gap-2 px-[7px] py-[5px] text-left text-[12px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                            onClick={() => {
                              setFlowMenuOpen(false)
                              setMobileSidebarOpen(false)
                            }}
                          >
                            <Workflow size={14} className="shrink-0" />
                            Add / manage flows
                          </Link>
                        )}
                      </div>
                    ) : null}
                  </div>

                  <div className="grid gap-0.5 pt-1">
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
              {isFlowRoute ? (
                <div className="flex min-h-0 flex-1 flex-col gap-2 font-mono">
                  <div className="flex items-center justify-between gap-2 px-1 py-1">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">Flows</div>
                      <div className="mt-0.5 truncate text-xs text-[var(--app-text-subtle)]">{flowSummary}</div>
                    </div>
                    <button type="button" className={SIDEBAR_ACTION_BUTTON_CLASS} onClick={handleOpenFlowsSettings} aria-label="Open flow page" title="Open flow page">
                      <Workflow size={14} strokeWidth={1.8} className="shrink-0" />
                    </button>
                  </div>
                  <div className="scrollbar-hidden grid min-h-0 flex-1 content-start gap-1 overflow-y-auto">
                    {flowsQuery.isLoading ? (
                      <div className="px-2 py-2 text-xs text-[var(--app-text-subtle)]">Loading flows…</div>
                    ) : flowsQuery.isError ? (
                      <div className="px-2 py-2 text-xs text-[var(--app-warning)]">Flows unavailable.</div>
                    ) : selectedWorkspaceFlowRows.length === 0 ? (
                      <div className="px-2 py-2 text-xs text-[var(--app-text-subtle)]">No flows for this workspace yet.</div>
                    ) : selectedWorkspaceFlowRows.map((flow) => {
                      const busy = flowBusyID === flow.id
                      return (
                        <div key={flow.id} className="group grid min-h-[52px] grid-cols-[minmax(0,1fr)_28px] items-center gap-2 border border-transparent px-2 py-2 text-[13px] text-[var(--app-text-muted)] hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                          <button type="button" className="min-w-0 text-left" onClick={() => handleOpenFlow(flow)} title={flow.detail}>
                            <span className="flex min-w-0 items-center gap-1.5">
                              <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', sidebarFlowDotClass(flow.status))} />
                              <span className="truncate text-[var(--app-text)]">{flow.name}</span>
                            </span>
                            <span className="mt-1 block truncate text-[11px] leading-4 text-[var(--app-text-subtle)]">{sidebarFlowStatusLabel(flow.status)} · {flow.agent} · {flow.detail}</span>
                          </button>
                          <button
                            type="button"
                            className="grid h-7 w-7 place-items-center justify-self-end rounded border border-[var(--app-border)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:cursor-progress disabled:opacity-60"
                            onClick={() => { void handleToggleFlowEnabled(flow) }}
                            disabled={Boolean(flowBusyID)}
                            aria-label={`${flow.enabled ? 'Pause' : 'Start'} ${flow.name}`}
                            title={flow.enabled ? 'Pause flow' : 'Start flow'}
                          >
                            {busy ? <LoaderCircle size={11} className="animate-spin" /> : flow.enabled ? <Pause size={11} /> : <Play size={11} />}
                          </button>
                        </div>
                      )
                    })}
                  </div>
                  {flowMenuError ? <div className="border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-2 py-1.5 text-[11px] text-[var(--app-warning)]">{flowMenuError}</div> : null}
                </div>
              ) : (
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
                  {renderSidebarSessionGroups({
                    nodes: globalFlattenedSessionNodes,
                    routeSessionId,
                    now: sidebarNow,
                    fallbackSwarmName: swarmName,
                    routeOptions: globalSessionRouteOptions,
                    workspaceSlug: globalSessionWorkspaceSlug,
                    expandedAgentSessions,
                    compactingSession,
                    onSelect: handleSelectSession,
                    onPrefetch: handlePrefetchSession,
                    onToggleAgents: handleToggleAgentSessions,
                  })}
                  {globalFlattenedSessionNodes.length === 0 ? (
                    <div className="px-2 py-2 text-xs text-[var(--app-text-subtle)]">No active sessions.</div>
                  ) : null}
                </div>
              )}
            </div>

          </div>
        </div>
      )}
    </>
  )

  const localContainerConfirmPlan = localContainerUpdateConfirm?.plan ?? null
  const remoteContainerUpdateCount = localContainerUpdateConfirm ? remoteDeployUpdateSessionCount(localContainerUpdateConfirm.remoteSessions) : 0
  const managedHostUpdateCount = localContainerUpdateConfirm?.managedHostCount ?? 0
  const localContainerConfirmSummary = localContainerConfirmPlan?.summary ?? null
  const updateProgressJob = updateProgress.job
  const updateProgressMessage = updateJobMessage(updateProgressJob)
  const updateProgressStep = updateProgressStepIndex(updateProgressJob)
  const updateProgressFailed = updateProgressJob?.status === 'failed'
  const updateProgressCompleted = updateProgressJob?.status === 'completed'
  const localContainerAffectedCount = localContainerConfirmSummary
    ? Math.max(
      localContainerConfirmSummary.affected ?? 0,
      (localContainerConfirmSummary.needs_update ?? 0) + (localContainerConfirmSummary.unknown ?? 0) + (localContainerConfirmSummary.errors ?? 0),
    )
    : 0

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
            session={sessionById.get(routeSessionId) ?? null}
            routeOptions={sessionById.get(routeSessionId) ? buildDesktopChatRouteOptions({
              hostSwarmName: swarmName,
              workspacePath: desktopSidebarWorkspacePathForSession(sessionById.get(routeSessionId)!, workspacePathByBindingId),
              workspaceName: sessionById.get(routeSessionId)?.workspaceName ?? '',
              topologyRoutes: [],
            }) : []}
            onOpenChats={() => setMobileSidebarOpen(true)}
            onCompactingChange={handleCompactingSessionChange}
            onArchivePlanSession={handleArchivePlanSession}
            onNewSession={() => {
              const routeSession = sessionById.get(routeSessionId)
              const workspacePath = routeSession ? desktopSidebarWorkspacePathForSession(routeSession, workspacePathByBindingId) : ''
              if (routeSession && workspacePath) handleStartNewSessionInWorkspace(workspacePath, routeSession.workspaceName)
            }}
            onSlashCommand={handleSlashCommand}
            onOpenPlan={() => openPlanModalForSession(routeSessionId)}
          />
        ) : isFlowRoute ? (
          <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden bg-[var(--app-bg)] px-3 pb-[calc(var(--app-safe-area-bottom)_+_1.25rem)] pt-[calc(var(--app-safe-area-top)_+_1rem)] sm:px-6 sm:py-8">
            <div className="mx-auto min-h-full w-full max-w-6xl min-w-0">
              <FlowsSettingsPage />
            </div>
          </div>
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
            workspace={routeWorkspace}
            workspaceSlug={routeWorkspaceSlug}
            routeOptions={buildDesktopChatRouteOptions({
              hostSwarmName: swarmName,
              workspacePath: routeWorkspace.path,
              workspaceName: routeWorkspace.workspaceName,
              topologyRoutes: routeWorkspace.topologyRoutes,
            })}
            onOpenChats={() => setMobileSidebarOpen(true)}
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
        onSave={handleSavePlanModal}
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
            {updateProgressJob?.hosts?.length ? (
              <div className="mt-4 space-y-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-3 text-sm">
                <div className="font-medium">Managed host phases</div>
                {updateProgressJob.hosts.map((host) => (
                  <div key={host.host_id || host.name} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                    <div className="flex items-center justify-between gap-3">
                      <span className="font-medium">{host.name || host.host_id || 'managed host'}</span>
                      <span className={cn('text-xs uppercase tracking-wide', host.status === 'failed' ? 'text-[var(--app-error)]' : host.status === 'completed' ? 'text-[var(--app-success)]' : 'text-[var(--app-text-muted)]')}>{host.status || 'running'}</span>
                    </div>
                    <div className="mt-2 grid grid-cols-5 gap-1 text-[11px]">
                      {MANAGED_DEV_UPDATE_PHASES.map((phaseName) => {
                        const phase = host.phases?.find((entry) => entry.name === phaseName)
                        const phaseStatus = phase?.status ?? 'pending'
                        return (
                          <div key={phaseName} className={cn('rounded border px-2 py-1 text-center capitalize', phaseStatus === 'failed' ? 'border-[var(--app-error)] text-[var(--app-error)]' : phaseStatus === 'completed' ? 'border-[var(--app-success)] text-[var(--app-success)]' : phaseStatus === 'running' ? 'border-[var(--app-primary)] text-[var(--app-primary)]' : 'border-[var(--app-border)] text-[var(--app-text-muted)]')}>
                            {phaseName}
                          </div>
                        )
                      })}
                    </div>
                    {(host.error || host.message) ? <div className="mt-2 text-xs text-[var(--app-text-muted)]">{host.error || host.message}</div> : null}
                  </div>
                ))}
              </div>
            ) : null}
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

      {localContainerConfirmPlan ? (
        <div className="absolute inset-0 z-50 flex items-center justify-center bg-[var(--app-backdrop)] px-4" aria-modal="true" role="dialog">
          <Card className="w-full max-w-lg border-[var(--app-warning-border)] bg-[var(--app-surface)] p-6 shadow-2xl">
            <div className="text-lg font-semibold">Update container images too?</div>
            <p className="mt-3 text-sm text-[var(--app-text-muted)]">
              {localContainerConfirmPlan.contract?.warning_copy || 'This will also update local and remote container images.'}
            </p>
            <div className="mt-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4 text-sm">
              <div className="font-medium">
                {localContainerAffectedCount > 0
                  ? `${localContainerAffectedCount} local container${localContainerAffectedCount === 1 ? '' : 's'} may need attention.`
                  : 'No local containers need attention.'}
              </div>
              {remoteContainerUpdateCount > 0 ? (
                <div className="mt-1 text-sm text-[var(--app-text)]">{remoteContainerUpdateCount} remote SSH session{remoteContainerUpdateCount === 1 ? '' : 's'} will be checked.</div>
              ) : null}
              {managedHostUpdateCount > 0 ? (
                <div className="mt-2 rounded-lg border border-[var(--app-warning-border)] bg-[color-mix(in_srgb,var(--app-warning)_12%,transparent)] p-3 text-sm text-[var(--app-text)]">
                  Dev update will hard-reset and clean {managedHostUpdateCount} managed host dev checkout{managedHostUpdateCount === 1 ? '' : 's'} before rebuilding them.
                </div>
              ) : null}
              <div className="mt-2 text-xs text-[var(--app-text-muted)]">
                {formatLocalContainerUpdateTarget(localContainerConfirmPlan)}
              </div>
              {localContainerConfirmSummary ? (
                <div className="mt-3 grid grid-cols-2 gap-2 text-xs text-[var(--app-text-muted)]">
                  <div>Total: {localContainerConfirmSummary.total}</div>
                  <div>Needs update: {localContainerConfirmSummary.needs_update}</div>
                  <div>Already current: {localContainerConfirmSummary.already_current}</div>
                  <div>Unknown/errors: {(localContainerConfirmSummary.unknown ?? 0) + (localContainerConfirmSummary.errors ?? 0)}</div>
                </div>
              ) : null}
            </div>
            <p className="mt-3 text-xs text-[var(--app-text-muted)]">
              {localContainerConfirmPlan.contract?.failure_semantics || 'Swarm update succeeds independently; local or remote container update failures are reported as resumable follow-up work.'}
            </p>
            {remoteContainerUpdateCount === 0 && managedHostUpdateCount === 0 ? (
              <label className="mt-4 flex items-center gap-2 text-sm text-[var(--app-text-muted)]">
                <input
                  type="checkbox"
                  checked={localContainerUpdateConfirm?.pendingDismiss ?? false}
                  onChange={(event) => {
                      const dismissed = event.target.checked
                      handleToggleLocalContainerUpdateDismissal(dismissed)
                    }}
                />
                <span>Don&apos;t show this again for local-only container image warnings</span>
              </label>
            ) : null}
            <div className="mt-6 flex justify-end gap-3">
              <Button variant="ghost" onClick={handleCancelLocalContainerUpdate} disabled={updateRunning}>Cancel</Button>
              <Button onClick={() => { void handleConfirmLocalContainerUpdate() }} disabled={updateRunning}>
                Continue update
              </Button>
            </div>
          </Card>
        </div>
      ) : null}

      <WorktreeSessionModal
        state={worktreeSessionModal}
        title={worktreeSessionTitle}
        branch={worktreeSessionBranch}
        busy={worktreeSessionCreating}
        error={worktreeSessionError}
        onTitleChange={setWorktreeSessionTitle}
        onBranchChange={setWorktreeSessionBranch}
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
      <ManagedHostLinkRequestModal
        open={pairingRequestsOpen}
        requests={activePairingRequests}
        busyID={pairingDecisionBusyID}
        confirmations={pairingConfirmations}
        error={pairingRequestError}
        status={pairingRequestStatus}
        now={sidebarNow}
        linkReviewTarget={pairingReplicationTarget}
        onOpenChange={setPairingRequestsOpen}
        onConfirmationChange={(requestID, confirmed) => setPairingConfirmations((current) => ({ ...current, [requestID]: confirmed }))}
        onDecision={(request, approve) => { void handlePairingDecision(request, approve) }}
        onLinkReviewComplete={async (message: string) => {
          setPairingReplicationTarget(null)
          setPairingRequestError(null)
          setPairingRequestStatus(message)
          await queryClient.invalidateQueries({ queryKey: ['swarm-targets'] })
        }}
        onLinkReviewSkip={(message: string) => {
          setPairingReplicationTarget(null)
          setPairingRequestError(null)
          setPairingRequestStatus(message)
        }}
      />
      {pwaDebugEnabled ? <PwaLayoutDebugOverlay /> : null}

    </div>
  )
}
