import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, JSX, ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate, useSearch, Link } from '@tanstack/react-router'
import { Archive, Bell, Bot, Check, CheckCircle2, ChevronDown, ChevronLeft, ChevronRight, Download, Film, Folder, GitBranch, GitCommitHorizontal, GitMerge, Keyboard, ListChecks, ListTodo, LoaderCircle, Menu, MessageSquare, Mic, MoreVertical, NotepadText, Pencil, Pin, Plus, RefreshCcw, Save, Search, Settings, X, XCircle } from 'lucide-react'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { cn } from '../../../lib/cn'
import { useWorkspaceLauncher } from '../../workspaces/launcher/state/use-workspace-launcher'
import { applyDesktopRouteTheme } from './desktop-theme-controller'
import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'
import { agentStateQueryOptions, draftModelQueryOptions, modelProfilesQueryOptions, uiSettingsQueryKey, workspaceOverviewQueryOptions } from '../../queries/query-options'
import type { DesktopNotificationCenterRecord, DesktopSessionRecord } from '../types/realtime'
import type { DesktopSessionPlanRecord } from '../chat/types/chat'
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
import { mergeWorkspaceAITaskMonotonic } from '../../workspaces/todos/ai-task-reconciliation'
import { getSwarmSettings } from '../settings/swarm/queries/get-swarm-settings'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveSwarmSettings } from '../settings/swarm/mutations/save-swarm-settings'
import { normalizeShowTipsEnabled, normalizeSidebarHideInactiveHours, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { saveSidebarHideInactiveHours } from '../settings/swarm/mutations/save-sidebar-hide-inactive-hours'
import { saveShowTipsSetting } from '../settings/swarm/mutations/save-show-tips-setting'
import { fetchSwarmTargets } from '../swarm/api/swarm-targets'
import { DesktopV3ExistingConversationPane } from '../chat/components/desktop-v3-existing-conversation-pane'
import { DesktopV3NewSessionPane } from '../chat/components/desktop-v3-new-session-pane'
import { DesktopV3ChatHeader } from '../chat/components/desktop-v3-chat-header'
import { DesktopV3AgenticComposer } from '../chat/components/desktop-v3-agentic-composer'
import { clearDesktopV3RoutedStartOperation, createDesktopV3NewSessionOperation, desktopV3RoutedWorkspaceAuthority, startNewDesktopV3Session, type DesktopV3RoutedStartResult, type DesktopV3RoutedWorkspaceAuthority } from '../session-v3/new-session-flow'
import { DesktopPlanModal } from '../chat/components/desktop-plan-modal'
import { buildDesktopChatRouteOptions, getDesktopSessionCreateTarget, type DesktopChatRoute } from '../chat/services/chat-routing'
import { resolveDesktopV3AgentModelLock } from '../chat/services/agent-model-preferences'
import { preferenceFromModelProfile } from '../chat/services/model-profiles'
import { parseDesktopNewSessionCommand, parseDesktopTaskCommand, type DesktopNewSessionCommandRequest, type DesktopSlashCommand } from '../chat/services/slash-commands'
import { executeDesktopTipsCommand } from '../chat/services/home-tips'
import { commitWorkspaceChanges, fetchGitStatus, gitStatusQueryKey, startGitRealtime, suggestWorkspaceCommitMessage } from '../git/api'
import type { GitFileStatus, GitSnapshot } from '../git/types'
import { AICommitButton } from '../git/ai-commit-control'
import { DesktopWorkspaceActionPanel } from '../chat/components/desktop-workspace-action-panel'
import { startWorkspaceAction, type WorkspaceAction, type WorkspaceActionRun } from '../../workspaces/actions/types'
import { WorkspaceActionsSidebarSection } from '../settings/actions/components/workspace-actions-sidebar-section'
import { fetchDesktopUpdateJob, fetchDesktopUpdateStatus, startDesktopUpdate, type DesktopUpdateJob } from '../update/api'
import {
  sessionBackgroundInfo,
  sessionChildDescriptor,
  sessionParentSessionID,
  type SidebarSessionNodeKind,
} from './sidebar-session-lineage'
import { commitDesktopV3CacheSnapshot, dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { isDesktopV3SessionTailReady, selectDesktopSidebarRows, selectDesktopVideoStudioRows, selectNotificationSummary, selectOrderedNotifications, selectRenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { messageMutationResponseToAction, selectSession, sessionCreateResponseToAction } from '../state/desktop-v3-cache-wire'
import { selectAndHydrateDesktopV3Session } from '../state/desktop-v3-session-hydrator'
import type { DesktopV3SidebarRow, RenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { fetchAndApplyDesktopV3PlanSnapshot } from '../state/desktop-v3-session-api'
import { archiveDesktopV3Sessions } from '../session-v3/plan-execution-api'
import { DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY, updateAndApplySessionV3DesktopSidebarPinned, updateSessionV3Title } from '../session-v3/api'
import { sessionWorkspaceBindingId } from '../services/session-workspace'
import type { DesktopV3SessionView, SessionCreateMutationResponse, SessionMessageMutationResponse, V3SessionRunIntent, V3SessionRunState } from '../state/desktop-v3-cache-types'
import { desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { bootstrapDesktopV3SidebarMetadataOnly } from '../state/desktop-v3-bootstrap-controller'
import { normalizeDesktopV3RoutedSessionStartResponse, postDesktopV3BackgroundRouterSessionStart, type DesktopV3RoutedSessionStartResponse } from '../session-v3/write-api'
import { isDesktopV3NavigationHiddenRecord, isDesktopV3VideoStudioRecord } from '../state/desktop-v3-session-visibility'
import { clearNotifications, updateNotification } from '../notifications/api'
import { DesktopNotificationsModal } from '../notifications/components/desktop-notifications-modal'
import { DESKTOP_V3_RUN_TIMER_TOOLTIP } from '../chat/components/desktop-v3-run-status'
import { SearchChatsModal } from '../session-search/search-chats-modal'
import type { DesktopSessionSearchItem } from '../session-search/session-search-api'
import { DesktopQuickActionsModal, type DesktopQuickActionItem } from '../shortcuts/components/desktop-quick-actions-modal'
import { DesktopWorkspacePicker } from '../shortcuts/components/desktop-workspace-picker'
import { DesktopCodexUsageModal } from '../codex/desktop-codex-usage-modal'
import { buildReviewWorktreeFixPrompt, ReviewWorktreesModal, type ReviewWorktreeIntegrationFailure } from './review-worktrees-modal'
import { reviewDesktopV3Worktrees } from '../session-v3/review-worktrees-api'
import { IntegrationConfirmation } from './integration-confirmation'
import {
  loadDesktopMainSidebarMode,
  saveDesktopMainSidebarMode,
  type DesktopMainSidebarMode,
} from './main-sidebar-focus-state'

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
const SIDEBAR_SESSION_ICON_CLASS = {
  worktree: 'text-[var(--app-primary)]',
  task: 'text-[var(--app-warning)]',
  plan: 'text-[var(--app-info)]',
} as const
const PWA_DEBUG_QUERY_PARAM = 'pwaDebug'
const DESKTOP_REPAIR_AGENT_NAME = 'swarm'
const MISSING_GIT_REV_PARSE_ERROR = 'git rev-parse --show-toplevel: exit status 128'
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

interface PlanModalState {
  sessionId: string
}

interface GitCommitModalState {
  workspacePath: string
  sessionId: string
  files: GitFileStatus[]
  worktree?: boolean
  targetWorkspacePath?: string
  targetBranch?: string
  canIntegrate?: boolean
}

interface GitIntegrateModalState {
  sessionId: string
  workspacePath: string
  worktreeBranch: string
  targetBranch: string
  integrationComplete?: boolean
  presentation?: 'sidebar-popout'
}

export function buildGitSidebarIntegrationHelpPrompt(input: GitIntegrateModalState, integrationError: string): string {
  return [
    'Fix this worktree integration error and integrate the worktree.',
    'Inspect the existing session and worktree context, diagnose and resolve the failure, and integrate the source worktree into the target branch. Take the safe recovery steps needed to complete integration rather than only explaining the error or returning a blocked message. Do not archive the session unless I explicitly ask in a later message.',
    '',
    `Session ID: ${input.sessionId}`,
    `Source branch: ${input.worktreeBranch || 'unknown'}`,
    `Target branch: ${input.targetBranch || 'unknown'}`,
    `Target workspace: ${input.workspacePath || 'unknown'}`,
    '',
    'Integration error:',
    integrationError,
  ].join('\n')
}

export function isMissingGitSidebarError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : typeof error === 'string' ? error : ''
  return message.includes(MISSING_GIT_REV_PARSE_ERROR)
}

export function buildInstallGitPrompt(gitError: string): string {
  return [
    'Please install Git on this machine so Swarm can use Git features.',
    '',
    'The sidebar Git check failed with:',
    gitError,
  ].join('\n')
}

interface GitPanelState {
  workspacePath: string
  workspaceName: string
}

interface DesktopRepairSessionLaunchInput {
  owningWorkspacePath: string
  sourceSessionId: string
  prompt: string
  title: string
  source: string
  sessionMetadata?: Record<string, unknown>
  messageMetadata?: Record<string, unknown>
}

interface DesktopRepairSessionAuthority {
  workspace: WorkspaceEntry
  route: DesktopChatRoute
  workspaceSlug: string
}

interface DesktopV3RoutedActivationDeps {
  getSnapshot: typeof getDesktopV3CacheSnapshot
  commitSnapshot: typeof commitDesktopV3CacheSnapshot
  requireRealtimeController: typeof requireDesktopV3RealtimeControllerReady
  ensureSidebarBootstrap: typeof bootstrapDesktopV3SidebarMetadataOnly
  currentURL: () => string
  replaceURL: (url: string) => void
}

function currentDesktopURL(): string {
  if (typeof window === 'undefined') return ''
  return `${window.location.pathname}${window.location.search}${window.location.hash}`
}

const desktopV3RoutedActivationDeps: DesktopV3RoutedActivationDeps = {
  getSnapshot: getDesktopV3CacheSnapshot,
  commitSnapshot: commitDesktopV3CacheSnapshot,
  requireRealtimeController: requireDesktopV3RealtimeControllerReady,
  ensureSidebarBootstrap: bootstrapDesktopV3SidebarMetadataOnly,
  currentURL: currentDesktopURL,
  replaceURL: (url) => {
    if (typeof window === 'undefined' || !url) return
    window.history.replaceState(window.history.state, '', url)
    window.dispatchEvent(new PopStateEvent('popstate', { state: window.history.state }))
  },
}

function desktopV3RoutedResultResponse(result: DesktopV3RoutedStartResult): DesktopV3RoutedSessionStartResponse {
  return normalizeDesktopV3RoutedSessionStartResponse(result)
}

function desktopV3RoutedSessionView(response: DesktopV3RoutedSessionStartResponse): DesktopV3SessionView {
  return response.session_view as unknown as DesktopV3SessionView
}

function desktopV3RoutedRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null
}

function desktopV3RoutedRequiredString(value: unknown, field: string): string {
  const normalized = typeof value === 'string' ? value.trim() : ''
  if (!normalized) throw new Error(`Routed Desktop activation requires canonical ${field}`)
  return normalized
}

function validateDesktopV3RoutedActivationResponse(response: DesktopV3RoutedSessionStartResponse): void {
  const identity = response.session_view.identity
  const settings = desktopV3RoutedRecord(response.session_view.agentic_settings)
  const mediaCapability = desktopV3RoutedRecord(response.session_view.media_capability)
  const sessionId = response.session_id.trim()
  const title = response.title.trim()
  desktopV3RoutedRequiredString(identity.source_workspace_name, 'source workspace name')
  desktopV3RoutedRequiredString(identity.source_workspace_path, 'source workspace')
  const runtimeWorkspacePath = desktopV3RoutedRequiredString(identity.runtime_workspace_path, 'runtime workspace')
  if (desktopV3RoutedRequiredString(identity.session_id, 'session identity') !== sessionId
    || desktopV3RoutedRequiredString(identity.title, 'session title') !== title) {
    throw new Error('Routed Desktop activation received inconsistent canonical identity')
  }
  desktopV3RoutedRequiredString(response.first_message.content, 'first message')
  if (desktopV3RoutedRequiredString(response.session.workspace_path, 'runtime workspace') !== runtimeWorkspacePath
    || desktopV3RoutedRequiredString(response.session.title, 'session title') !== title) {
    throw new Error('Routed Desktop activation received inconsistent canonical session authority')
  }
  if (!settings || response.session.mode !== response.starting_mode || settings.mode !== response.starting_mode) {
    throw new Error('Routed Desktop activation received inconsistent canonical mode authority')
  }
  desktopV3RoutedRequiredString(settings.agent_name, 'agent')
  desktopV3RoutedRequiredString(settings.resolved_agent_name, 'resolved agent')
  const effectivePreference = desktopV3RoutedRecord(settings.effective_preference)
  desktopV3RoutedRequiredString(effectivePreference?.provider, 'model provider')
  desktopV3RoutedRequiredString(effectivePreference?.model, 'model')
  if (!mediaCapability || !Array.isArray(mediaCapability.capabilities)) {
    throw new Error('Routed Desktop activation requires canonical media capability')
  }
  if (typeof identity.worktree_enabled !== 'boolean') {
    throw new Error('Routed Desktop activation requires canonical worktree intent')
  }
  if (identity.requested_worktree_name !== undefined && typeof identity.requested_worktree_name !== 'string') {
    throw new Error('Routed Desktop activation received invalid requested worktree authority')
  }
  if (identity.worktree_enabled) {
    const worktreeRootPath = desktopV3RoutedRequiredString(identity.worktree_root_path, 'worktree root')
    const worktreeBranch = desktopV3RoutedRequiredString(identity.worktree_branch, 'worktree branch')
    if (response.session.worktree_enabled !== true
      || response.session.worktree_root_path?.trim() !== worktreeRootPath
      || response.session.worktree_branch?.trim() !== worktreeBranch) {
      throw new Error('Routed Desktop activation received inconsistent canonical worktree authority')
    }
  } else if (response.session.worktree_enabled === true) {
    throw new Error('Routed Desktop activation received inconsistent disabled worktree authority')
  }
}

/**
 * Publishes one already-durable routed result only after validation and realtime
 * connection succeed. Any later publish/navigation failure restores the exact
 * prior cache selection/sidebar snapshot and URL while the activation still owns
 * the state it published.
 */
export async function activateDesktopV3RoutedSession(
  result: DesktopV3RoutedStartResult,
  deps: DesktopV3RoutedActivationDeps,
  shouldActivate: () => boolean,
  onActivated?: (response: DesktopV3RoutedSessionStartResponse) => Promise<void> | void,
): Promise<DesktopV3RoutedSessionStartResponse> {
  const response = desktopV3RoutedResultResponse(result)
  validateDesktopV3RoutedActivationResponse(response)
  const sessionId = response.session_id
  if (!shouldActivate()) throw new Error('Routed Desktop activation is stale')

  let previousState = deps.getSnapshot()
  let sidebarScopeId = previousState.desktopSidebarBootstrap.scopeId?.trim()
  if (!sidebarScopeId) {
    await deps.ensureSidebarBootstrap({ preferredSessionId: sessionId })
    if (!shouldActivate()) throw new Error('Routed Desktop activation is stale')
    previousState = deps.getSnapshot()
    sidebarScopeId = previousState.desktopSidebarBootstrap.scopeId?.trim()
  }
  if (!sidebarScopeId) throw new Error('Routed Desktop activation could not bootstrap the canonical sidebar scope')

  const previousURL = deps.currentURL()
  const previousSelectedSessionId = previousState.selectedSessionId
  const previousSidebarOrderByScope = structuredClone(previousState.sessionOrderByScope)

  const createResponse: SessionCreateMutationResponse = {
    ok: true,
    session_id: sessionId,
    session: response.session,
    projection: response.projection,
    mutation: response.mutation,
    realtime_outbox: response.mutation.realtime_outbox ?? null,
  }
  const mutationResources = response.mutation as typeof response.mutation & {
    run_intent?: V3SessionRunIntent | null
    usage_summary?: unknown
  }
  const messageResponse: SessionMessageMutationResponse = {
    ok: true,
    session_id: sessionId,
    session: response.session,
    projection: response.projection,
    message: response.first_message,
    run_intent: mutationResources.run_intent ?? null,
    current_run_state: response.session_view.current_run_state as V3SessionRunState | undefined,
    usage_summary: mutationResources.usage_summary ?? response.session_view.usage_summary,
    mutation: response.mutation,
    realtime_outbox: response.mutation.realtime_outbox ?? null,
  }
  const actions = [
    sessionCreateResponseToAction(createResponse, sidebarScopeId),
    messageMutationResponseToAction(messageResponse, `desktop-v3-routed:activation:${sessionId}`, response.first_message.id),
    selectSession(sessionId),
  ] as const

  const controller = await deps.requireRealtimeController()
  if (!shouldActivate()) throw new Error('Routed Desktop activation is stale')
  await controller.ensureSessionConnected(sessionId)
  if (!shouldActivate()) throw new Error('Routed Desktop activation is stale')

  let nextState = structuredClone(previousState)
  for (const action of actions) nextState = desktopV3CacheReducer(nextState, action)
  const routedView = desktopV3RoutedSessionView(response)
  nextState.sessionViewsById[sessionId] = routedView
  const routedSettings = routedView.agentic_settings
  if (!routedSettings) throw new Error('Routed Desktop activation requires canonical agentic settings')
  nextState.preferencesBySession[sessionId] = routedSettings.effective_preference
    ?? routedSettings.stored_preference
  nextState.agentModelPolicyBySession[sessionId] = routedSettings.agent_model_policy
  if (routedView.has_active_plan !== undefined) nextState.hasActivePlanBySession[sessionId] = routedView.has_active_plan
  if (routedView.active_plan !== undefined) nextState.plansBySession[sessionId] = routedView.active_plan

  let published = false
  try {
    if (!shouldActivate()) throw new Error('Routed Desktop activation is stale')
    published = true
    deps.commitSnapshot(previousState, nextState, [...actions])
    await onActivated?.(response)
    return response
  } catch (error) {
    const ownsPublishedState = published && deps.getSnapshot() === nextState
      && previousState.selectedSessionId === previousSelectedSessionId
      && JSON.stringify(previousState.sessionOrderByScope) === JSON.stringify(previousSidebarOrderByScope)
    const currentURL = deps.currentURL()
    const routedSessionSuffix = `/${encodeURIComponent(sessionId)}`
    const ownsRoute = shouldActivate() || (ownsPublishedState && currentURL.split(/[?#]/, 1)[0]?.endsWith(routedSessionSuffix) === true)
    if (ownsPublishedState) deps.commitSnapshot(nextState, previousState, [])
    if (ownsRoute && currentURL !== previousURL) deps.replaceURL(previousURL)
    throw error
  }
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
      || a.timelineSeq !== b.timelineSeq
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
  const bindingID = sessionWorkspaceBindingId(session.metadata)
  if (bindingID) {
    const boundPath = workspacePathByBindingId?.get(bindingID)?.trim()
    if (boundPath) return boundPath
  }

  const sourceWorkspacePath = metadataStringValue(session.metadata, 'swarm_v3_source_workspace_path')
  if (sourceWorkspacePath) return sourceWorkspacePath

  return session.workspacePath.trim()
}

export function desktopRouteWorkspacePathForSession(
  session: Pick<DesktopSessionRecord, 'workspacePath' | 'metadata'>,
  workspacePathByBindingId: ReadonlyMap<string, string>,
  knownWorkspacePaths: ReadonlySet<string>,
): string {
  const bindingID = sessionWorkspaceBindingId(session.metadata)
  if (bindingID) {
    const boundPath = workspacePathByBindingId.get(bindingID)?.trim()
    if (boundPath) return boundPath
  }

  const candidates = [
    metadataStringValue(session.metadata, 'swarm_v3_source_workspace_path'),
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

type SpeechRecognitionResultEventLike = Event & {
  results: ArrayLike<{ 0?: { transcript?: string }; isFinal?: boolean }>
}

type SpeechRecognitionErrorEventLike = Event & {
  error?: string
  message?: string
}

type SpeechRecognitionLike = {
  continuous: boolean
  interimResults: boolean
  lang: string
  maxAlternatives: number
  onstart: ((event: Event) => void) | null
  onend: ((event: Event) => void) | null
  onresult: ((event: SpeechRecognitionResultEventLike) => void) | null
  onerror: ((event: SpeechRecognitionErrorEventLike) => void) | null
  start: () => void
  stop: () => void
  abort: () => void
}

type SpeechRecognitionConstructor = new () => SpeechRecognitionLike

type SpeechRecognitionWindow = Window & typeof globalThis & {
  SpeechRecognition?: SpeechRecognitionConstructor
  webkitSpeechRecognition?: SpeechRecognitionConstructor
}

function browserSpeechRecognitionConstructor(): SpeechRecognitionConstructor | null {
  if (typeof window === 'undefined') return null
  const speechWindow = window as SpeechRecognitionWindow
  return speechWindow.SpeechRecognition ?? speechWindow.webkitSpeechRecognition ?? null
}

function appendBrowserDictation(base: string, transcript: string): string {
  const addition = transcript.replace(/\s+/g, ' ').trim()
  if (!addition) return base
  const trimmedBase = base.replace(/[ \t]+$/g, '')
  if (!trimmedBase) return addition
  const separator = /[\s\n]$/.test(trimmedBase) || /^[,.;:!?]/.test(addition) ? '' : ' '
  return `${trimmedBase}${separator}${addition}`
}

function browserDictationError(error: string, message = ''): string {
  if (error === 'not-allowed' || error === 'service-not-allowed') return 'Microphone permission was denied or blocked.'
  if (error === 'audio-capture') return 'No microphone was found.'
  if (error === 'no-speech') return 'No speech was detected. Try again.'
  return message.trim() || 'Browser dictation could not start.'
}

function useBrowserDictation(value: string, onValueChange: (value: string) => void, disabled: boolean) {
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const valueRef = useRef(value)
  const [supported, setSupported] = useState(false)
  const [listening, setListening] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    valueRef.current = value
  }, [value])

  useEffect(() => {
    const Recognition = browserSpeechRecognitionConstructor()
    setSupported(Boolean(Recognition))
    if (!Recognition) return
    const recognition = new Recognition()
    recognition.continuous = false
    recognition.interimResults = false
    recognition.lang = typeof navigator === 'undefined' ? 'en-US' : navigator.language || 'en-US'
    recognition.maxAlternatives = 1
    recognition.onstart = () => {
      setListening(true)
      setError(null)
    }
    recognition.onend = () => setListening(false)
    recognition.onerror = (event) => {
      setListening(false)
      setError(browserDictationError(event.error ?? '', event.message))
    }
    recognition.onresult = (event) => {
      let transcript = ''
      for (let index = 0; index < event.results.length; index += 1) {
        transcript += ` ${event.results[index]?.[0]?.transcript ?? ''}`
      }
      const nextValue = appendBrowserDictation(valueRef.current, transcript)
      valueRef.current = nextValue
      onValueChange(nextValue)
    }
    recognitionRef.current = recognition
    return () => {
      recognitionRef.current = null
      try {
        recognition.abort()
      } catch {
        // Ignore browser recognition teardown races.
      }
    }
  }, [onValueChange])

  useEffect(() => {
    if (!disabled || !listening) return
    try {
      recognitionRef.current?.stop()
    } catch {
      recognitionRef.current?.abort()
    }
  }, [disabled, listening])

  const toggle = useCallback(() => {
    const recognition = recognitionRef.current
    if (!recognition || disabled) return
    if (listening) {
      recognition.stop()
      return
    }
    setError(null)
    try {
      recognition.start()
    } catch (startError) {
      setError(startError instanceof Error ? startError.message : 'Browser dictation could not start.')
    }
  }, [disabled, listening])

  return { supported, listening, error, toggle }
}

function BackgroundTaskForm({ presentation, workspaceName, request, busy, error, onRequestChange, onSubmit, onClose }: {
  presentation: 'dialog' | 'page'
  workspaceName: string
  request: string
  busy: boolean
  error: string | null
  onRequestChange: (value: string) => void
  onSubmit: (request: string) => void
  onClose: () => void
}) {
  const dictation = useBrowserDictation(request, onRequestChange, busy)
  if (presentation === 'page') {
    return (
      <section data-testid="mobile-task-page" className="flex h-full min-h-0 flex-col bg-[var(--app-surface)] pt-[var(--app-safe-area-top)] font-mono">
        <div className="flex shrink-0 items-start justify-between gap-4 border-b border-[var(--app-border)] px-4 py-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]">
              <ListChecks size={16} aria-hidden="true" />
              <span>Background task</span>
            </div>
            <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{workspaceName}</div>
          </div>
          <button type="button" className="grid size-11 shrink-0 touch-manipulation place-items-center rounded-xl text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]" onClick={onClose} disabled={busy} aria-label="Back to workspace">
            <X size={18} />
          </button>
        </div>
        <div className="flex min-h-0 flex-1 flex-col justify-end">
          <p className="px-4 pb-1 pt-5 text-sm leading-6 text-[var(--app-text-muted)]">
            Send Swarm a background task. Sessions start automatically.
          </p>
          <DesktopV3AgenticComposer
            draft={request}
            onDraftChange={onRequestChange}
            placeholder="What should Swarm do?"
            inputLabel="Send Swarm a background task"
            disabled={busy}
            busy={busy}
            canSubmit={Boolean(request.trim()) && !busy}
            error={error}
            onSubmit={(draft) => onSubmit(draft)}
            mode="auto"
            showModePicker={false}
            executionLabel="background"
            currentAgent="swarm"
            selectedPrimaryAgent="swarm"
            agents={[]}
            modelOptions={[]}
            selectedModelKey=""
            thinking="off"
          />
        </div>
      </section>
    )
  }
  const content = (
    <>
        <div className="flex shrink-0 items-start justify-between gap-4 border-b border-[var(--app-border)] px-4 py-3 sm:px-5 sm:py-4">
          <div className="min-w-0">
            <div className="flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]">
              <ListChecks size={16} aria-hidden="true" />
              <span>Background task</span>
            </div>
            <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{workspaceName}</div>
          </div>
          <button type="button" className="grid size-11 shrink-0 touch-manipulation place-items-center rounded-xl text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]" onClick={onClose} disabled={busy} aria-label="Close background task dialog">
            <X size={18} />
          </button>
        </div>
        <form
          className="flex min-h-0 flex-1 flex-col"
          onSubmit={(event) => {
            event.preventDefault()
            onSubmit(request)
          }}
        >
          <div data-creation-form-scroll className="grid min-h-0 flex-1 gap-4 overflow-y-auto px-4 py-4 [-webkit-overflow-scrolling:touch] sm:px-5">
            <p className="text-xs leading-5 text-[var(--app-text-muted)]">
              Describe the work once. Swarm will run it in the background in a managed session, so you can close this window and follow progress from the active session list.
            </p>
          <label className="grid min-h-0 gap-1.5 text-xs text-[var(--app-text-muted)]">
            <span>What should Swarm do?</span>
            <div className="relative">
              <textarea
                autoFocus={presentation === 'dialog'}
                value={request}
                onChange={(event) => onRequestChange(event.target.value)}
                disabled={busy}
                rows={6}
                className="min-h-32 w-full resize-y rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-3 pr-14 text-[16px] leading-6 text-[var(--app-text)] outline-none focus:border-[var(--app-border-strong)] max-sm:max-h-[34dvh]"
                placeholder="Describe a complete task for this workspace…"
              />
              {dictation.supported ? (
                <button type="button" className={cn('absolute bottom-2 right-2 grid size-11 touch-manipulation place-items-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]', dictation.listening && 'border-[var(--app-primary)] text-[var(--app-primary)]')} onClick={dictation.toggle} disabled={busy} aria-pressed={dictation.listening} aria-label={dictation.listening ? 'Stop microphone dictation' : 'Start microphone dictation'} title={dictation.listening ? 'Stop dictation' : 'Dictate task'}>
                  <Mic size={18} className={dictation.listening ? 'animate-pulse' : undefined} aria-hidden="true" />
                </button>
              ) : null}
            </div>
          </label>
            {dictation.error ? <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning)]" role="alert">{dictation.error}</div> : null}
            {error ? <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-xs text-[var(--app-warning)]" role="alert">{error}</div> : null}
          </div>
          <div className="grid shrink-0 grid-cols-2 gap-3 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 max-sm:pb-[calc(0.75rem+var(--app-safe-area-bottom))] sm:flex sm:justify-end sm:px-5">
            <Button className="min-h-11" type="button" variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
            <Button className="min-h-11" type="submit" disabled={busy || !request.trim()}>{busy ? 'Starting…' : 'Start task'}</Button>
          </div>
        </form>
    </>
  )
  return (
    <Dialog>
      <DialogBackdrop onClick={busy ? undefined : onClose} />
      <DialogPanel className="w-[min(560px,100%)] gap-0 overflow-hidden p-0 font-mono">{content}</DialogPanel>
    </Dialog>
  )
}

function GitDetailsOverlay({ state, snapshot, loading, error, onRefresh, onCommit, aiCommitControl, onClose }: { state: GitPanelState | null; snapshot: GitSnapshot | null; loading: boolean; error: string | null; onRefresh: () => void; onCommit: (files: GitFileStatus[]) => void; aiCommitControl?: ReactNode; onClose: () => void }) {
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
        {loading && !snapshot ? <div className="border border-[var(--app-border)] px-3 py-4 text-xs text-[var(--app-text-subtle)]">Loading Git status…</div> : snapshot?.has_git ? (
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
            {files.length > 0 ? <div className="flex flex-wrap justify-end gap-2"><Button type="button" onClick={() => onCommit(files)}>Commit changes…</Button>{aiCommitControl}</div> : null}
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
      return item.aiState || nextItem.aiState ? mergeWorkspaceAITaskMonotonic(item, nextItem) : nextItem
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

export function sidebarWorkspaceContextLabel(workspaceName: string, branch: string | null | undefined): string {
  const normalizedWorkspaceName = workspaceName.trim()
  const normalizedBranch = branch?.trim() ?? ''
  if (!normalizedBranch) return normalizedWorkspaceName
  return normalizedWorkspaceName ? `${normalizedBranch} · ${normalizedWorkspaceName}` : normalizedBranch
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
    return `${minutes} min${minutes === 1 ? '' : 's'}`
  }

  const hours = Math.floor(minutes / 60)
  if (hours < 24) {
    return `${hours} hr${hours === 1 ? '' : 's'}`
  }

  const days = Math.floor(hours / 24)
  return `${days} day${days === 1 ? '' : 's'}`
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
  const workspaceName = session.workspaceName?.trim()
  if (workspaceName) return workspaceName
  const workspacePath = session.workspacePath?.trim().replace(/[\\/]+$/, '') ?? ''
  const pathParts = workspacePath.split(/[\\/]/).filter(Boolean)
  return pathParts[pathParts.length - 1] || workspacePath || 'Workspace'
}

function sessionBranchLabel(session: DesktopSessionRecord): string {
  return metadataText(session, 'swarm_v3_branch_label') || session.worktreeBranch?.trim() || session.gitBranch?.trim() || ''
}

function sessionIsActive(session: DesktopSessionRecord): boolean {
  return sessionHasPendingPermission(session) || sessionHasCanonicalActiveRun(session)
}

export function sessionIsMobileActive(session: DesktopSessionRecord): boolean {
  const group = sessionSidebarDisplayGroup(session)
  return sessionIsActive(session) || group === 'needs_review' || group === 'in_progress'
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
    const parentSessionID = node.kind === 'root' ? '' : sessionParentSessionID(node.session)
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

const EMPTY_SESSION_AGENT_SUMMARY: SessionAgentSummary = { total: 0, running: 0 }

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
  selectionGroup?: SidebarSessionGroupID
  selected?: boolean
  onSelect: (sessionId: string) => void | boolean
  onEnterSelectionMode?: (group: SidebarSessionGroupID) => void
  onToggleSelected?: (sessionId: string, range: boolean) => void
  onPrefetch: (sessionId: string) => void
  onToggleAgents: (sessionId: string) => void
  onTogglePinned: (sessionId: string) => void
  onArchive: (sessionId: string) => void
  onRename: (sessionId: string, title: string) => Promise<void>
}

const SessionRow = memo(function SessionRow({ active, now, session: initialSession, workspaceSlug, depth = 0, childAssignmentLabel = null, agentSummary, agentsExpanded, compactingStartedAt = null, pendingAction = null, selectionMode = false, selectionGroup, selected = false, onSelect, onEnterSelectionMode, onToggleSelected, onPrefetch, onToggleAgents, onTogglePinned, onArchive, onRename }: SessionRowProps) {
  const session = initialSession
  const compactingActive = typeof compactingStartedAt === 'number' && compactingStartedAt > 0
  const activeSession = compactingActive || sessionIsActive(session)
  const backgroundInfo = sessionBackgroundInfo(session)
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
  const workspaceLabel = sessionWorkspaceLabel(session)
  const branchLabel = sessionBranchLabel(session)
  const showWorktreeChip = Boolean(session.worktreeEnabled)
  const showBranchLabel = !session.worktreeEnabled && Boolean(branchLabel)
  const showTaskChip = Boolean(backgroundInfo)
  const showActivePlan = session.mode === 'plan' && sessionHasCanonicalActiveRun(session)
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
  const pinned = sessionAllowsManualSidebarPin(session) && sessionManuallyPinnedInSidebar(session)
  const pinDisabled = pendingAction !== null || !sessionAllowsManualSidebarPin(session)
  const archiveDisabled = pendingAction !== null
  const actionButtonBaseClass = cn(
    'inline-flex h-6 w-full shrink-0 items-center gap-2 rounded border-0 bg-transparent px-2 text-left font-inherit text-[11px] leading-6 text-[var(--app-text-subtle)] transition-[background-color,color] hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:cursor-default disabled:opacity-60 disabled:hover:bg-transparent',
  )
  const hoverActionButtonClass = 'inline-flex h-4 w-4 shrink-0 items-center justify-center rounded border-0 bg-transparent p-0 text-[var(--app-text-subtle)] opacity-0 transition-[background-color,color,opacity] hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] focus:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100 disabled:cursor-default disabled:opacity-40'
  const actionMenuButtonClass = cn(
    hoverActionButtonClass,
    actionsOpen ? 'opacity-100 text-[var(--app-text)]' : null,
  )
  const pinActionControl = sessionAllowsManualSidebarPin(session) ? (
    <button
      type="button"
      className={cn(hoverActionButtonClass, pinned ? 'text-[var(--app-primary)] hover:text-[var(--app-primary-hover)]' : null)}
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
        setRenameDraft('')
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
      className={hoverActionButtonClass}
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
    </button>
  )
  const selectActionControl = depth === 0 && selectionGroup && onEnterSelectionMode ? (
    <button
      type="button"
      className={actionButtonBaseClass}
      disabled={selectionMode}
      aria-label="Select sessions to archive"
      onClick={(event) => {
        event.preventDefault()
        event.stopPropagation()
        closeActionMenu()
        onEnterSelectionMode(selectionGroup)
      }}
    >
      <ListChecks size={12} aria-hidden="true" />
      <span>Select</span>
    </button>
  ) : null
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
      <span className="min-w-0 flex-1 truncate">Subagents</span>
      <span className="ml-auto font-mono tabular-nums text-[10px] leading-none text-[var(--app-text-muted)]">{agentSummary.total}</span>
    </button>
  ) : null
  const actionMenu = (
    <span
      ref={actionMenuRef}
      className={cn('relative z-20 inline-flex h-4 w-4 shrink-0 items-center', actionsOpen ? 'z-40' : null)}
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
        <MoreVertical size={12} aria-hidden="true" />
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
          {renameActionControl}
          {selectActionControl}
        </span>
      ) : null}
    </span>
  )
  return (
    <Link
      to="/$workspaceSlug/$sessionId"
      params={{ workspaceSlug: rowWorkspaceSlug, sessionId: session.id }}
      onClick={(event) => {
        if (event.defaultPrevented || event.button !== 0) return
        if (selectionMode && depth === 0) {
          event.preventDefault()
          onToggleSelected?.(session.id, event.shiftKey)
          return
        }
        if (event.metaKey || event.altKey || event.ctrlKey || event.shiftKey) return
        event.preventDefault()
        onSelect(session.id)
      }}
      onKeyDown={(event) => {
        if (event.key === ' ') {
          event.preventDefault()
          if (selectionMode && depth === 0) {
            onToggleSelected?.(session.id, event.shiftKey)
            return
          }
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
      title={tooltip || [workspaceLabel, showBranchLabel ? branchLabel : ''].filter(Boolean).join(' · ')}
    >
      <div className="flex min-w-0 items-start justify-between gap-2">
        <div className="flex min-w-0 flex-1 items-start gap-2">
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 items-center">
              {selectionMode && depth === 0 ? (
                <input
                  type="checkbox"
                  checked={selected}
                  aria-label={`Select ${rowTitle}`}
                  onChange={() => undefined}
                  onClick={(event) => {
                    event.preventDefault()
                    event.stopPropagation()
                    onToggleSelected?.(session.id, event.shiftKey)
                  }}
                  className="mr-2 h-4 w-4 shrink-0 accent-[var(--app-primary)]"
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
                      event.stopPropagation()
                      if (event.key === 'Escape') { event.preventDefault(); setRenameError(null); setRenaming(false) }
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
          <span className="relative inline-flex h-4 w-14 shrink-0 items-center justify-end" data-sidebar-session-corner-controls>
            <span
              className={cn(
                'absolute right-0 inline-flex items-center justify-end gap-1 transition-opacity group-hover:opacity-0 group-focus-within:opacity-0',
                actionsOpen ? 'opacity-0' : 'opacity-100',
              )}
              data-sidebar-session-metadata-icons
            >
              {showWorktreeChip ? (
                <span className="inline-flex h-4 w-4 items-center justify-center" title="Worktree session">
                  <GitBranch size={12} className={cn(SIDEBAR_SESSION_ICON_CLASS.worktree, 'opacity-80')} aria-label="Worktree session" />
                </span>
              ) : null}
              {showTaskChip ? (
                <span className="inline-flex h-4 w-4 items-center justify-center" title="Task session">
                  <ListTodo size={12} className={cn(SIDEBAR_SESSION_ICON_CLASS.task, 'opacity-80')} aria-label="Task session" />
                </span>
              ) : null}
              {showActivePlan ? (
                <span className="inline-flex h-4 w-4 items-center justify-center" title="Active plan">
                  <NotepadText size={12} className={cn(SIDEBAR_SESSION_ICON_CLASS.plan, 'opacity-80')} aria-label="Active plan" />
                </span>
              ) : null}
            </span>
            <span className="absolute right-0 inline-flex items-center justify-end gap-1" data-sidebar-session-action-icons>
              {pinActionControl}
              {archiveActionControl}
              {actionMenu}
            </span>
          </span>
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
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="min-w-0 truncate">{workspaceLabel}</span>
          {showBranchLabel ? (
            <>
              <span aria-hidden="true">·</span>
              <span className="min-w-0 truncate">{branchLabel}</span>
            </>
          ) : null}
        </span>
        <span className="ml-auto inline-flex shrink-0 items-center justify-end gap-1 text-right tabular-nums text-[var(--app-text-muted)]">
          {rowTimerLabel ? <span>{rowTimerLabel}</span> : null}
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
        </div>
      ) : null}

    </Link>
  )
})

interface RenderSidebarSessionGroupsInput {
  nodes: SidebarSessionNode[]
  presentation?: 'desktop' | 'mobile'
  routeSessionId: string
  now: number
  workspaceSlug: string | ((session: DesktopSessionRecord) => string)
  expandedAgentSessions: Record<string, boolean>
  agentSummaries: Map<string, SessionAgentSummary>
  compactingSession: DesktopV3CompactingSessionState | null
  pendingActions: Record<string, 'pin' | 'archive' | 'rename' | undefined>
  selectionMode: boolean
  selectedRootIDs: Set<string>
  hideInactiveHours: number | null
  thresholdSaving: boolean
  bulkArchivePending: boolean
  masterSelectionGroup: SidebarSessionGroupID | null
  reviewCleanupOpen: boolean
  gitHasGit: boolean
  gitAheadCount: number
  gitBehindCount: number
  gitDirtyCount: number
  onOpenGit: () => void
  onToggleReviewCleanup: () => void
  onEnterSelectionMode: (group: SidebarSessionGroupID) => void
  onClearSelection: () => void
  onBulkArchive: () => void
  onThresholdChange: (hours: number | null) => void
  onSelect: (sessionId: string) => void | boolean
  onToggleSelected: (sessionId: string, range: boolean) => void
  onPrefetch: (sessionId: string) => void
  onToggleAgents: (sessionId: string) => void
  onTogglePinned: (sessionId: string) => void
  onArchive: (sessionId: string) => void
  onRename: (sessionId: string, title: string) => Promise<void>
}

export function sidebarRootIDsForSelectionGroup(nodes: SidebarSessionNode[], group: SidebarSessionGroupID | null): string[] {
  return nodes
    .filter((node) => !group || sessionSidebarDisplayGroup(node.session) === group)
    .map((node) => node.session.id)
}

export function sidebarShouldRenderSelectionToolbar(
  selectionMode: boolean,
  masterGroup: SidebarSessionGroupID | null,
  group: SidebarSessionGroupID,
): boolean {
  return selectionMode && masterGroup === group
}

export function sidebarShouldShowReviewAction(group: SidebarSessionGroupID, selectionMode: boolean): boolean {
  return group === 'needs_review' && !selectionMode
}

export const SIDEBAR_SESSION_GROUPS = [
  { id: 'needs_review', label: 'Needs Review', showInactiveThreshold: false },
  { id: 'in_progress', label: 'In Progress', showInactiveThreshold: false },
  { id: 'pinned', label: 'Pinned', showInactiveThreshold: false },
  { id: 'active_chats', label: 'Active Chats', showInactiveThreshold: true },
] as const satisfies ReadonlyArray<{ id: SidebarSessionGroupID; label: string; showInactiveThreshold: boolean }>

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
  return SIDEBAR_SESSION_GROUPS.flatMap((group) => {
    const nodes = grouped.get(group.id) ?? []
    if (nodes.length === 0) return []
    const groupControls = (
      <div className={`ml-auto flex items-center gap-1 normal-case tracking-normal transition-opacity ${group.id === 'needs_review' || input.selectionMode ? 'opacity-100' : 'opacity-0 group-hover/section:opacity-100 group-focus-within/section:opacity-100'}`}>
            {sidebarShouldShowReviewAction(group.id, input.selectionMode) ? (
              <>
                <button
                  type="button"
                  className={input.presentation === 'mobile'
                    ? 'inline-flex min-h-11 touch-manipulation items-center gap-1 rounded-xl border border-[var(--app-border)] px-3 text-xs font-semibold text-[var(--app-text-muted)] active:bg-[var(--app-surface-hover)] active:text-[var(--app-text)]'
                    : 'inline-flex h-5 items-center gap-1 rounded border border-[var(--app-border)] px-1.5 text-[9px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}
                  aria-label="Review worktrees"
                  title="Review worktrees"
                  aria-expanded={input.reviewCleanupOpen}
                  onClick={input.onToggleReviewCleanup}
                >
                  <span>Manage</span>
                </button>
              </>
            ) : null}
            {group.showInactiveThreshold ? (
              <label className="flex items-center gap-1 font-normal">
                <span>Show last</span>
                <select
                  aria-label="Show Active Chats from the last"
                  disabled={input.thresholdSaving}
                  value={input.hideInactiveHours === null ? 'never' : String(input.hideInactiveHours)}
                  onChange={(event) => input.onThresholdChange(event.target.value === 'never' ? null : Number(event.target.value))}
                  className="h-5 rounded border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-1 text-[9px] text-[var(--app-text)]"
                >
                  <option value="1">1h</option><option value="6">6h</option><option value="12">12h</option><option value="24">24h</option><option value="168">7d</option><option value="never">All</option>
                </select>
              </label>
            ) : null}
            {sidebarShouldRenderSelectionToolbar(input.selectionMode, input.masterSelectionGroup, group.id) ? (
              <>
                <span>{input.selectedRootIDs.size} selected</span>
                <button type="button" onClick={input.onClearSelection}>Clear</button>
                <button type="button" disabled={input.bulkArchivePending || input.selectedRootIDs.size === 0} className="rounded bg-[var(--app-primary)] px-1.5 py-0.5 text-[var(--app-primary-text)] disabled:opacity-50" onClick={input.onBulkArchive}>Archive</button>
              </>
            ) : null}
      </div>
    )
    return [(
      <section key={group.id} className="group/section grid content-start gap-1.5">
        {group.id === 'needs_review' ? (
          <>
            <div data-sidebar-review-toolbar className="flex min-h-6 items-center gap-1 px-1 pt-1 text-[9px] font-semibold text-[var(--app-text-subtle)]">
              {input.gitHasGit ? (
                <button
                  type="button"
                  data-sidebar-dirty-git-entry
                  className={`flex h-5 items-center gap-1 rounded px-1 text-[9px] font-medium hover:bg-[var(--app-surface-hover)] ${input.gitDirtyCount > 0 ? 'text-[var(--app-warning)]' : 'text-[var(--app-text-muted)]'}`}
                  onClick={input.onOpenGit}
                  aria-label={`Open Git details: ${input.gitAheadCount} ahead, ${input.gitBehindCount} behind, ${input.gitDirtyCount} uncommitted changes`}
                  title="Open Git details"
                >
                  <span aria-hidden="true">↑{input.gitAheadCount} ↓{input.gitBehindCount}</span>
                  <span>· {input.gitDirtyCount > 0 ? `${input.gitDirtyCount} uncommitted` : 'clean'}</span>
                </button>
              ) : null}
              {groupControls}
            </div>
            <div data-sidebar-needs-review-heading className="flex min-h-6 items-center px-1 pt-1 text-[9px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">
              <span>{group.label}</span>
            </div>
          </>
        ) : (
          <div className="flex min-h-6 items-center gap-1 px-1 pt-1 text-[9px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">
            <span>{group.label}</span>
            {groupControls}
          </div>
        )}
        <div className="grid gap-1">
          {nodes.map((node) => (
          <SessionRow
            key={node.session.id}
            active={input.routeSessionId === node.session.id}
            now={input.now}
            session={node.session}
            workspaceSlug={input.workspaceSlug}
            depth={node.depth}
            childLabel={node.label}
            childAssignmentLabel={node.assignmentLabel}
            childKind={node.kind}
            agentSummary={input.agentSummaries.get(node.session.id) ?? EMPTY_SESSION_AGENT_SUMMARY}
            agentsExpanded={Boolean(input.expandedAgentSessions[node.session.id]) || nodeContainsDescendantSession(node, input.routeSessionId || undefined)}
            compactingStartedAt={input.compactingSession?.sessionId === node.session.id ? input.compactingSession.startedAt : null}
            pendingAction={input.pendingActions[node.session.id] ?? null}
            selectionMode={input.selectionMode}
            selectionGroup={group.id}
            selected={input.selectedRootIDs.has(node.session.id)}
            onSelect={input.onSelect}
            onEnterSelectionMode={input.onEnterSelectionMode}
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
  const search = useSearch({ strict: false }) as { agentSetup?: unknown; agent?: unknown; newWorktree?: unknown; newPlan?: unknown }
  const requestedAgentSetup = search.agentSetup === '1'
  const requestedNewWorktree = search.newWorktree === '1'
  const requestedNewPlan = search.newPlan === '1'
  const requestedAgentName = typeof search.agent === 'string' ? search.agent.trim() : 'swarm'
  const agentSettingsOpenSignal = requestedAgentSetup ? 1 : 0
  const matchRoute = useMatchRoute()
  const workspaceTaskMatch = matchRoute({ to: '/$workspaceSlug/task', fuzzy: false })
  const workspaceWorktreeMatch = matchRoute({ to: '/$workspaceSlug/worktree', fuzzy: false })
  const workspaceVideoSessionMatch = matchRoute({ to: '/$workspaceSlug/video/$videoSessionId', fuzzy: false })
  const workspaceSessionMatch = matchRoute({ to: '/$workspaceSlug/$sessionId', fuzzy: false })
  const workspaceMatch = matchRoute({ to: '/$workspaceSlug', fuzzy: false })
  const routeWorkspaceSlug = (workspaceTaskMatch
    ? workspaceTaskMatch.workspaceSlug
    : workspaceWorktreeMatch
      ? workspaceWorktreeMatch.workspaceSlug
      : workspaceVideoSessionMatch
        ? workspaceVideoSessionMatch.workspaceSlug
        : workspaceSessionMatch
          ? workspaceSessionMatch.workspaceSlug
          : workspaceMatch
          ? workspaceMatch.workspaceSlug
          : '').trim()
  const mobileCreationPage = workspaceTaskMatch ? 'task' : workspaceWorktreeMatch ? 'worktree' : null
  const videoStudioRoute = Boolean(workspaceVideoSessionMatch)
  const routeSessionId = mobileCreationPage
    ? ''
    : (workspaceVideoSessionMatch ? workspaceVideoSessionMatch.videoSessionId : workspaceSessionMatch ? workspaceSessionMatch.sessionId : '').trim()
  const pwaDebugEnabled = typeof window !== 'undefined' && new URLSearchParams(window.location.search).has(PWA_DEBUG_QUERY_PARAM)
  const { workspaces, loading: launcherWorkspacesLoading, setWorkspaceIcon } = useWorkspaceLauncher({ applyDocumentTheme: false, autoRefresh: false, browseDuringRefresh: false })
  const [sidebarDisplayMode, setSidebarDisplayModeState] = useState<DesktopMainSidebarMode>(() => loadDesktopMainSidebarMode())
  const focusMode = sidebarDisplayMode === 'focus'
  const setSidebarDisplayMode = useCallback((mode: DesktopMainSidebarMode) => {
    setSidebarDisplayModeState(mode)
    saveDesktopMainSidebarMode(mode)
  }, [])
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [mobilePreviousSessionsOpen, setMobilePreviousSessionsOpen] = useState(false)
  const [backgroundTaskOpen, setBackgroundTaskOpen] = useState(false)
  const [backgroundTaskRequest, setBackgroundTaskRequest] = useState('')
  const [backgroundTaskError, setBackgroundTaskError] = useState<string | null>(null)
  const [expandedAgentSessions, setExpandedAgentSessions] = useState<Record<string, boolean>>({})
  const [notificationsOpen, setNotificationsOpen] = useState(false)
  const [codexUsageOpen, setCodexUsageOpen] = useState(false)
  const [notificationActionError, setNotificationActionError] = useState<string | null>(null)
  const [searchModalOpen, setSearchModalOpen] = useState(false)
  const [todoModal, setTodoModal] = useState<TodoModalState | null>(null)
  const [gitPanel, setGitPanel] = useState<GitPanelState | null>(null)
  const [gitCommitModal, setGitCommitModal] = useState<GitCommitModalState | null>(null)
  const [gitCommitMessage, setGitCommitMessage] = useState('')
  const gitCommitMessageInputRef = useRef<HTMLInputElement | null>(null)
  const [gitCommitBusy, setGitCommitBusy] = useState(false)
  const [gitAICommitPhase, setGitAICommitPhase] = useState<'generating' | 'committing' | null>(null)
  const gitAICommitRunningRef = useRef(false)
  const [gitCommitError, setGitCommitError] = useState<string | null>(null)
  const [workspaceActionPresentation, setWorkspaceActionPresentation] = useState<{ action: WorkspaceAction; mode: 'standalone' | 'post-commit'; workspacePath: string; sessionId: string; initialRun?: WorkspaceActionRun } | null>(null)
  const [gitCommitIntegrate, setGitCommitIntegrate] = useState(false)
  const [gitCommitArchive, setGitCommitArchive] = useState(false)
  const [gitIntegrateModal, setGitIntegrateModal] = useState<GitIntegrateModalState | null>(null)
  const [gitIntegrateBusy, setGitIntegrateBusy] = useState(false)
  const [gitIntegrateHelpBusy, setGitIntegrateHelpBusy] = useState(false)
  const [gitInstallHelpBusy, setGitInstallHelpBusy] = useState(false)
  const [gitIntegrateArchive, setGitIntegrateArchive] = useState(false)
  const [gitIntegrateError, setGitIntegrateError] = useState<string | null>(null)
  const gitIntegrateAnchorRef = useRef<HTMLDivElement | null>(null)
  const gitIntegratePopoutRef = useRef<HTMLDivElement | null>(null)
  const [gitIntegratePopoutStyle, setGitIntegratePopoutStyle] = useState<CSSProperties>({ visibility: 'hidden' })
  const [planModal, setPlanModal] = useState<PlanModalState | null>(null)
  const [planModalError, setPlanModalError] = useState<string | null>(null)
  const [quickSettingsTab, setQuickSettingsTab] = useState<QuickSettingsTabID | null>(null)
  const [quickActionsOpen, setQuickActionsOpen] = useState(false)
  const [workspacePickerOpen, setWorkspacePickerOpen] = useState(false)
  const [composerFocusSignal, setComposerFocusSignal] = useState(0)
  const [newSessionEpoch, setNewSessionEpoch] = useState(0)
  const [newSessionIntent, setNewSessionIntent] = useState<(DesktopNewSessionCommandRequest & { workspacePath: string }) | null>(null)
  const [workspaceDropdownOpen, setWorkspaceDropdownOpen] = useState(false)
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
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [todoSavingWorkspacePath, setTodoSavingWorkspacePath] = useState<string | null>(null)
  const [workspaceLayout, setWorkspaceLayout] = useState<Record<string, SidebarWorkspaceLayout>>(() => loadSidebarWorkspaceLayout())
  const [compactingSession, setCompactingSession] = useState<DesktopV3CompactingSessionState | null>(null)
  const [sidebarSessionActions, setSidebarSessionActions] = useState<Record<string, 'pin' | 'archive' | 'rename' | undefined>>({})
  const [sidebarSelectionMode, setSidebarSelectionMode] = useState(false)
  const [sidebarMasterSelectionGroup, setSidebarMasterSelectionGroup] = useState<SidebarSessionGroupID | null>(null)
  const [selectedSidebarRootIDs, setSelectedSidebarRootIDs] = useState<Set<string>>(() => new Set())
  const lastSelectedSidebarRootIDRef = useRef<string | null>(null)
  const [bulkArchivePending, setBulkArchivePending] = useState(false)
  const [needsReviewCleanupOpen, setNeedsReviewCleanupOpen] = useState(false)
  const [sidebarThresholdSaving, setSidebarThresholdSaving] = useState(false)
  const [sidebarNow, setSidebarNow] = useState(() => Date.now())
  const [previousChatSessionId, setPreviousChatSessionId] = useState<string | null>(null)
  const activeChatSessionIdRef = useRef<string | null>(null)
  const aiTaskLifecycleByID = useDesktopV3CacheSelector((state) => state.aiTasksById)
  const aiTaskObservedStateRef = useRef(new Map<string, WorkspaceTodoItem['aiState']>())
  const aiTaskTerminalToastRef = useRef(new Set<string>())
  const routedActivationGenerationRef = useRef(0)
  const routedActivationWorkspaceRef = useRef('')
  const sidebarBodyRef = useRef<HTMLDivElement | null>(null)
  const workspaceDropdownRef = useRef<HTMLDivElement | null>(null)
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
  routedActivationWorkspaceRef.current = routeSessionId ? '' : routeWorkspace?.path.trim() ?? ''
  useEffect(() => {
    if (!workspaceDropdownOpen) return
    const dismiss = (event: MouseEvent) => {
      if (!workspaceDropdownRef.current?.contains(event.target as Node)) setWorkspaceDropdownOpen(false)
    }
    const dismissOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setWorkspaceDropdownOpen(false)
    }
    document.addEventListener('mousedown', dismiss)
    window.addEventListener('keydown', dismissOnEscape)
    return () => {
      document.removeEventListener('mousedown', dismiss)
      window.removeEventListener('keydown', dismissOnEscape)
    }
  }, [workspaceDropdownOpen])

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
  const modelProfilesQuery = useQuery(modelProfilesQueryOptions())
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
  const updateAttentionVisible = !updateDevMode && (updateActionEnabled || updateRunning || Boolean(updateError))
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
        (target.swarm_id ?? '').trim(),
        (target.relationship ?? '').trim(),
        (target.role ?? '').trim(),
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

  useEffect(() => {
    const lifecycleItems = Object.values(aiTaskLifecycleByID)
    if (lifecycleItems.length === 0) return
    setTodoItems((current) => {
      const next = { ...current }
      for (const lifecycle of lifecycleItems) {
        next[lifecycle.workspacePath] = upsertWorkspaceTodoItem(next[lifecycle.workspacePath] ?? [], lifecycle)
      }
      return next
    })
    for (const lifecycle of lifecycleItems) {
      const previousState = aiTaskObservedStateRef.current.get(lifecycle.id)
      aiTaskObservedStateRef.current.set(lifecycle.id, lifecycle.aiState)
      if (!previousState || previousState === lifecycle.aiState || aiTaskTerminalToastRef.current.has(lifecycle.id)) continue
      const title = lifecycle.aiDisplayTitle || lifecycle.text || 'Task'
      if (lifecycle.aiState === 'in_progress') {
        setDesktopToast({ message: `${title} started.`, tone: 'info' })
      } else if (lifecycle.aiState === 'completed') {
        aiTaskTerminalToastRef.current.add(lifecycle.id)
        setDesktopToast({ message: `${title} completed.`, tone: 'success' })
      } else if (lifecycle.aiState === 'failed') {
        aiTaskTerminalToastRef.current.add(lifecycle.id)
        setDesktopToast({ message: lifecycle.aiError || `${title} failed.`, tone: 'error' })
      } else if (lifecycle.aiState === 'cancelled') {
        aiTaskTerminalToastRef.current.add(lifecycle.id)
        setDesktopToast({ message: `${title} was cancelled.`, tone: 'info' })
      }
    }
  }, [aiTaskLifecycleByID])

  const closeTodoModal = useCallback(() => {
    setTodoModal(null)
  }, [])
  const openMainWorktreeGitPanel = useCallback((workspacePath: string, workspaceName: string) => {
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
  const routeSessionNavigationHidden = useDesktopV3CacheSelector((state) => (
    routeSessionId ? isDesktopV3NavigationHiddenRecord(state.sessionsById[routeSessionId]) : false
  ))
  const routeSessionIsVideoStudio = useDesktopV3CacheSelector((state) => (
    routeSessionId ? isDesktopV3VideoStudioRecord(state.sessionsById[routeSessionId]) : false
  ))
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
  const desktopVideoStudioRows = useDesktopV3CacheSelector(selectDesktopVideoStudioRows, desktopV3SidebarRowsEqual)
  const desktopStateSessions = useMemo<DesktopSessionRecord[]>(
    () => [...desktopSidebarRows, ...desktopVideoStudioRows].map(desktopSessionRecordFromV3SidebarRow),
    [desktopSidebarRows, desktopVideoStudioRows],
  )
  const videoStudioSessions = useMemo<DesktopSessionRecord[]>(
    () => desktopVideoStudioRows.map(desktopSessionRecordFromV3SidebarRow),
    [desktopVideoStudioRows],
  )
  useEffect(() => {
    const sessionId = routeSessionId.trim()
    if (!sessionId) return
    void selectAndHydrateDesktopV3Session(sessionId)
  }, [routeSessionId])

  useEffect(() => {
    if (!routeSessionNavigationHidden || !routeWorkspaceSlug) return
    dispatchDesktopV3Cache(selectSession(undefined))
    void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
  }, [navigate, routeSessionNavigationHidden, routeWorkspaceSlug])

  useEffect(() => {
    if (!routeSessionId || !routeWorkspaceSlug || !routeSessionIsVideoStudio || videoStudioRoute) return
    void navigate({
      to: '/$workspaceSlug/video/$videoSessionId',
      params: { workspaceSlug: routeWorkspaceSlug, videoSessionId: routeSessionId },
      replace: true,
    })
  }, [navigate, routeSessionId, routeSessionIsVideoStudio, routeWorkspaceSlug, videoStudioRoute])

  useEffect(() => {
    if (routeSessionId.trim()) return
    if (!routeWorkspace?.path) return

    dispatchDesktopV3Cache(selectSession(undefined))
  }, [routeSessionId, routeWorkspace?.path])

  const ordinaryDesktopStateSessions = useMemo(
    () => desktopSidebarRows.map(desktopSessionRecordFromV3SidebarRow),
    [desktopSidebarRows],
  )
  const globalSidebarSessionNodes = useMemo(
    () => buildSidebarSessionTree(ordinaryDesktopStateSessions, sidebarNow),
    [ordinaryDesktopStateSessions, sidebarNow],
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
  const mobileActiveSessionNodes = useMemo(
    () => globalFlattenedSessionNodes.filter((node) => sessionIsMobileActive(node.session)),
    [globalFlattenedSessionNodes],
  )
  const mobilePreviousSessionNodes = useMemo(
    () => globalFlattenedSessionNodes.filter((node) => !sessionIsMobileActive(node.session)),
    [globalFlattenedSessionNodes],
  )
  const visibleSidebarRootIDs = useMemo(
    () => sidebarRootIDsForSelectionGroup(filteredSidebarTrees.nodes, null),
    [filteredSidebarTrees.nodes],
  )
  const sidebarAgentSummaries = useMemo(
    () => new Map(globalFlattenedSessionNodes.map((node) => [node.session.id, summarizeSubagentDescendants(node)] as const)),
    [globalFlattenedSessionNodes],
  )

  const sessionById = useMemo<Map<string, DesktopSessionRecord>>(
    () => new Map(desktopStateSessions.map((session) => [session.id, session] as const)),
    [desktopStateSessions],
  )
  const activeGitSession = routeSessionId ? sessionById.get(routeSessionId) ?? null : null
  const selectedGitSessionId = activeGitSession?.id ?? ''
  const selectedGitWorkspacePath = activeGitSession?.worktreeEnabled
    ? activeGitSession.worktreeRootPath?.trim() || ''
    : activeGitSession
      ? desktopRouteWorkspacePathForSession(activeGitSession, workspacePathByBindingId, knownWorkspacePaths)
      : ''
  const gitStatusQuery = useQuery({
    queryKey: gitStatusQueryKey(selectedGitWorkspacePath, selectedGitSessionId),
    queryFn: () => fetchGitStatus(selectedGitWorkspacePath, 12, selectedGitSessionId),
    enabled: selectedGitSessionId !== '' && selectedGitWorkspacePath !== '',
    staleTime: 0,
    refetchOnWindowFocus: true,
  })
  const gitSnapshot = gitStatusQuery.data?.status ?? null
  const activeSessionWorktree = Boolean(activeGitSession?.worktreeEnabled && selectedGitWorkspacePath)
  const activeSessionCommits = activeSessionWorktree ? gitSnapshot?.session_commits ?? [] : []
  const activeSessionTargetBranch = activeGitSession?.worktreeBaseBranch?.trim() || 'target branch'
  const activeSessionTargetWorkspacePath = activeGitSession ? desktopSidebarWorkspacePathForSession(activeGitSession, workspacePathByBindingId) : ''
  const gitReviewQuery = useQuery({
    queryKey: ['session-worktree-review', selectedGitSessionId, activeSessionTargetWorkspacePath, gitSnapshot?.head_oid ?? '', gitSnapshot?.clean ?? false],
    queryFn: () => reviewDesktopV3Worktrees({ workspacePath: activeSessionTargetWorkspacePath, graceHours: 1 }),
    enabled: activeSessionWorktree && activeSessionTargetWorkspacePath !== '',
    staleTime: 2_000,
    refetchOnWindowFocus: true,
  })
  const activeSessionReviewCandidate = activeSessionWorktree
    ? [...(gitReviewQuery.data?.retained ?? []), ...(gitReviewQuery.data?.done ?? [])].find((item) => item.session_id === selectedGitSessionId) ?? null
    : null
  const activeSessionIntegrateEligible = Boolean(activeSessionReviewCandidate?.integrate_eligible)

  useEffect(() => {
    if (!selectedGitWorkspacePath || document.visibilityState === 'hidden') return
    let cancelled = false
    let token = ''
    const refresh = async () => {
      const startedAt = Date.now()
      const requestedToken = token
      try {
        const response = await startGitRealtime(selectedGitWorkspacePath, selectedGitSessionId, requestedToken)
        if (cancelled) return
        if (response.watch_token !== requestedToken) {
          token = response.watch_token
          queryClient.setQueryData(gitStatusQueryKey(selectedGitWorkspacePath, selectedGitSessionId), { ok: true, status: response.status })
        } else {
          // A current daemon holds this request for the long-poll window. Keep a
          // defensive floor for stale/nonconforming daemons that ignore the token
          // so an immediate unchanged response cannot create a hot request loop.
          const remaining = 1_000 - (Date.now() - startedAt)
          if (remaining > 0) await new Promise((resolve) => window.setTimeout(resolve, remaining))
        }
        setGitRealtimeErrors((current) => {
          if (!current[selectedGitWorkspacePath]) return current
          const next = { ...current }; delete next[selectedGitWorkspacePath]; return next
        })
        return true
      } catch (error) {
        if (!cancelled) setGitRealtimeErrors((current) => ({ ...current, [selectedGitWorkspacePath]: error instanceof Error ? error.message : String(error) }))
        return false
      }
    }
    const poll = async () => {
      while (!cancelled) {
        const ok = document.visibilityState === 'visible' ? await refresh() : true
        if (!cancelled) await new Promise((resolve) => window.setTimeout(resolve, document.visibilityState !== 'visible' ? 1_000 : ok ? 250 : 5_000))
      }
    }
    void poll()
    return () => { cancelled = true }
  }, [queryClient, selectedGitSessionId, selectedGitWorkspacePath])
  const workspaceSlugByPath = useMemo(() => buildWorkspaceRouteSlugMap(
    mergedSidebarWorkspaceEntries.map((workspace) => ({
      path: workspace.path,
      workspaceName: workspace.workspaceName,
    })),
  ), [mergedSidebarWorkspaceEntries])
  const topWorkspace = selectedWorkspace
    ?? routeWorkspace
    ?? mergedSidebarWorkspaceEntries[0]
    ?? visibleSidebarWorkspaceEntries[0]
    ?? null
  const topWorkspaceLabel = topWorkspace?.workspaceName?.trim() || 'Default Workspace'
  const topWorkspacePath = topWorkspace?.path || selectedWorkspacePath || ''
  const topWorkspaceSlug = topWorkspacePath
    ? workspaceSlugByPath.get(topWorkspacePath) ?? workspaceRouteSlugBase({ path: topWorkspacePath, workspaceName: topWorkspaceLabel })
    : routeWorkspaceSlug
  const topWorkspaceGitStatusQuery = useQuery({
    queryKey: gitStatusQueryKey(topWorkspacePath),
    queryFn: () => fetchGitStatus(topWorkspacePath),
    enabled: Boolean(topWorkspacePath),
    staleTime: 5_000,
    refetchOnWindowFocus: true,
  })
  const topWorkspaceGitSnapshot = topWorkspaceGitStatusQuery.data?.status ?? null
  const topWorkspaceGitAheadCount = topWorkspaceGitSnapshot?.ahead_count ?? topWorkspace?.gitAheadCount ?? 0
  const topWorkspaceGitBehindCount = topWorkspaceGitSnapshot?.behind_count ?? topWorkspace?.gitBehindCount ?? 0
  const topWorkspaceGitDirtyCount = topWorkspaceGitSnapshot?.dirty_count ?? topWorkspace?.gitDirtyCount ?? 0
  const topWorkspaceHasGit = topWorkspaceGitSnapshot?.has_git ?? topWorkspace?.gitHasGit ?? false
  const sidebarWorkspaceBranch = activeGitSession?.worktreeEnabled
    ? gitSnapshot?.branch
    : gitSnapshot?.branch || selectedWorkspace?.gitBranch || routeWorkspace?.gitBranch || topWorkspace?.gitBranch
  const sidebarWorkspaceContext = sidebarWorkspaceContextLabel(masterWorkspaceName || topWorkspaceLabel, sidebarWorkspaceBranch)
  const defaultNewChatWorkspacePath = topWorkspacePath
  const defaultNewChatWorkspaceLabel = topWorkspaceLabel
  const activeWorkspaceAuthority = useMemo<DesktopV3RoutedWorkspaceAuthority | null>(() => {
    if (!topWorkspace) return null
    const route = buildDesktopChatRouteOptions({
      hostSwarmName: swarmName,
      workspacePath: topWorkspace.path,
      workspaceName: topWorkspace.workspaceName,
      topologyRoutes: topWorkspace.topologyRoutes,
      localWorkspaceBindingId: topWorkspace.localWorkspaceBindingId,
      hostSwarmId: currentSwarmTarget?.swarm_id ?? null,
    }).find((option) => getDesktopSessionCreateTarget(option).endpoint === '/v3/sessions')
    return route ? desktopV3RoutedWorkspaceAuthority(topWorkspace.path, route) : null
  }, [currentSwarmTarget?.swarm_id, swarmName, topWorkspace])
  const globalSessionWorkspaceSlug = useCallback((session: DesktopSessionRecord): string => {
    const workspacePath = desktopRouteWorkspacePathForSession(session, workspacePathByBindingId, knownWorkspacePaths)
      || selectedWorkspacePath
      || visibleWorkspacePaths[0]
      || ''
    if (!workspacePath) return topWorkspaceSlug || routeWorkspaceSlug || 'workspace'
    return workspaceSlugByPath.get(workspacePath)
      ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: session.workspaceName || fallbackWorkspaceNameFromPath(workspacePath) })
  }, [knownWorkspacePaths, routeWorkspaceSlug, selectedWorkspacePath, topWorkspaceSlug, visibleWorkspacePaths, workspacePathByBindingId, workspaceSlugByPath])
  const resolveDesktopRepairSessionAuthority = useCallback((owningWorkspacePath: string, sourceSessionId: string): DesktopRepairSessionAuthority | null => {
    const normalizedOwningPath = owningWorkspacePath.trim()
    const sourceSession = sessionById.get(sourceSessionId.trim()) ?? null
    const sourceBindingId = sessionWorkspaceBindingId(sourceSession?.metadata)
    const sourceWorkspacePath = sourceSession
      ? desktopSidebarWorkspacePathForSession(sourceSession, workspacePathByBindingId)
      : ''
    const workspace = workspaceByPath.get(normalizedOwningPath)
      ?? (sourceBindingId ? workspaceByPath.get(workspacePathByBindingId.get(sourceBindingId) ?? '') : null)
      ?? workspaceByPath.get(sourceWorkspacePath)
      ?? mergedSidebarWorkspaceEntries.find((candidate) => candidate.path === normalizedOwningPath || candidate.path === sourceWorkspacePath)
      ?? null
    if (!workspace) return null

    const bindingId = workspace.localWorkspaceBindingId.trim() || sourceBindingId
    const swarmId = currentSwarmTarget?.swarm_id?.trim()
      || metadataStringValue(sourceSession?.metadata, 'swarm_v3_runtime_swarm_id')
    const route = buildDesktopChatRouteOptions({
      hostSwarmName: swarmName,
      workspacePath: workspace.path,
      workspaceName: workspace.workspaceName,
      topologyRoutes: workspace.topologyRoutes,
      localWorkspaceBindingId: bindingId,
      hostSwarmId: swarmId || null,
    }).find((option) => getDesktopSessionCreateTarget(option).endpoint === '/v3/sessions') ?? null
    if (!route) return null

    return {
      workspace,
      route,
      workspaceSlug: workspaceSlugByPath.get(workspace.path)
        ?? workspaceRouteSlugBase({ path: workspace.path, workspaceName: workspace.workspaceName }),
    }
  }, [currentSwarmTarget?.swarm_id, mergedSidebarWorkspaceEntries, sessionById, swarmName, workspaceByPath, workspacePathByBindingId, workspaceSlugByPath])

  const launchDesktopRepairSession = useCallback(async (input: DesktopRepairSessionLaunchInput): Promise<void> => {
    const authority = resolveDesktopRepairSessionAuthority(input.owningWorkspacePath, input.sourceSessionId)
    if (!authority) throw new Error('The owning workspace binding or primary runtime is unavailable')

    const draftPreference = draftPreferenceQuery.data?.preference
    const modelProfileState = modelProfilesQuery.data
    const defaultModelProfile = modelProfileState?.profiles.find((candidate) => candidate.profileId === modelProfileState.defaultProfileId) ?? null
    const defaultModelProfilePreference = defaultModelProfile
      ? preferenceFromModelProfile(defaultModelProfile, 'auto', defaultModelProfile.updatedAt)
      : null
    const agentModel = resolveDesktopV3AgentModelLock(agentStateQuery.data?.profiles ?? [], DESKTOP_REPAIR_AGENT_NAME)
    const preference = defaultModelProfilePreference ?? (agentModel.locked
      ? {
          provider: agentModel.provider,
          model: agentModel.model,
          thinking: agentModel.thinking || draftPreference?.thinking || '',
          serviceTier: agentModel.serviceTier,
          contextMode: draftPreference?.contextMode || '',
        }
      : draftPreference)
    if (!preference?.provider?.trim() || !preference.model?.trim() || !preference.thinking?.trim()) {
      throw new Error('The Desktop V3 model preference is unavailable')
    }

    const operation = createDesktopV3NewSessionOperation({
      workspacePath: authority.workspace.path,
      workspaceName: authority.workspace.workspaceName,
      route: authority.route,
      prompt: input.prompt,
      title: input.title,
      mode: 'auto',
      agentName: DESKTOP_REPAIR_AGENT_NAME,
      modelProfileChoice: defaultModelProfilePreference ? { kind: 'account-default' as const } : undefined,
      worktree: { mode: 'off' },
      preference: {
        provider: preference.provider,
        model: preference.model,
        thinking: preference.thinking,
        serviceTier: preference.serviceTier,
        contextMode: preference.contextMode,
      },
      sessionMetadata: {
        ...input.sessionMetadata,
        source: input.source,
        source_session_id: input.sourceSessionId,
        workspace_path: authority.workspace.path,
      },
      messageMetadata: {
        ...input.messageMetadata,
        source: input.source,
        source_session_id: input.sourceSessionId,
      },
    })
    await startNewDesktopV3Session({
      operation,
      onSessionStarted: (sessionId) => {
        void navigate({
          to: '/$workspaceSlug/$sessionId',
          params: { workspaceSlug: authority.workspaceSlug, sessionId },
        })
      },
    })
  }, [agentStateQuery.data?.profiles, draftPreferenceQuery.data?.preference, modelProfilesQuery.data, navigate, resolveDesktopRepairSessionAuthority])

  const reviewFixAvailable = Boolean(topWorkspacePath)
  const handleAskSwarmToFixReviewIntegration = useCallback(async (failure: ReviewWorktreeIntegrationFailure) => {
    setNeedsReviewCleanupOpen(false)
    try {
      await launchDesktopRepairSession({
        owningWorkspacePath: topWorkspacePath,
        sourceSessionId: failure.candidate.session_id,
        prompt: buildReviewWorktreeFixPrompt(failure, topWorkspacePath),
        title: `${failure.operation === 'commit_and_integrate' ? 'Fix commit and integration' : 'Fix integration'}: ${failure.candidate.title || failure.candidate.worktree_branch || failure.candidate.session_id}`,
        source: 'desktop-v3-review-worktrees-recovery',
        messageMetadata: {
          failed_session_id: failure.candidate.session_id,
          worktree_branch: failure.candidate.worktree_branch,
          target_branch: failure.candidate.target_branch,
          target_workspace_path: topWorkspacePath,
          integration_error: failure.error,
        },
      })
    } catch (cause) {
      setDesktopToast({ message: cause instanceof Error ? cause.message : 'Could not start a Swarm repair session.', tone: 'error' })
    }
  }, [launchDesktopRepairSession, topWorkspacePath])

  useEffect(() => {
    if (!routeSessionId) return
    if (activeChatSessionIdRef.current && activeChatSessionIdRef.current !== routeSessionId) {
      setPreviousChatSessionId(activeChatSessionIdRef.current)
    }
    activeChatSessionIdRef.current = routeSessionId
  }, [routeSessionId])

  const routeReadinessStatus = routeSessionNavigationHidden ? 'navigation_hidden' : 'idle'
  const routeSessionUnavailable = routeSessionNavigationHidden

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

  const handleClearSidebarSelection = useCallback(() => {
    setSelectedSidebarRootIDs(new Set())
    setSidebarSelectionMode(false)
    setSidebarMasterSelectionGroup(null)
    lastSelectedSidebarRootIDRef.current = null
  }, [])

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
    handleClearSidebarSelection()
    void selectAndHydrateDesktopV3Session(sessionId)
    void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug, sessionId } })
  }, [handleClearSidebarSelection, navigate, workspaceSlugByPath])

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
    handleClearSidebarSelection()
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
  }, [handleClearSidebarSelection, knownWorkspacePaths, navigate, selectedWorkspacePath, sessionById, visibleWorkspacePaths, workspacePathByBindingId, workspaceSlugByPath])



  const chatWorkspacePath = selectedWorkspace?.path || ''
  const planModalPlan = useDesktopV3CacheSelector((state) => planModal?.sessionId ? (state.plansBySession[planModal.sessionId] ?? null) : null) as DesktopSessionPlanRecord | null

  const handleOpenWorkspace = useCallback((wsPath: string, wsName: string) => {
    setMobileSidebarOpen(false)
    const workspaceSlug = workspaceSlugByPath.get(wsPath)
      ?? workspaceRouteSlugBase({ path: wsPath, workspaceName: wsName })
    void navigate({
      to: '/$workspaceSlug',
      params: { workspaceSlug },
    })
  }, [navigate, workspaceSlugByPath])

  const handleStartNewSessionInWorkspace = useCallback((
    wsPath: string,
    wsName: string,
    options: { prompt?: string; worktreeRequested?: boolean; planModeRequested?: boolean } = {},
  ) => {
    const nextIntent = {
      workspacePath: wsPath,
      prompt: options.prompt?.trim() ?? '',
      worktreeRequested: options.worktreeRequested === true,
      planModeRequested: options.planModeRequested === true,
    }
    // An explicit New Session gesture is an abandonment boundary, not an
    // interrupted-start retry. Drop persisted retry identity and force a fresh
    // pane even when navigation targets the workspace URL already on screen.
    clearDesktopV3RoutedStartOperation()
    routedActivationGenerationRef.current += 1
    setNewSessionEpoch((current) => current + 1)
    setNewSessionIntent(nextIntent)
    dispatchDesktopV3Cache(selectSession(undefined))
    setMobileSidebarOpen(false)
    const workspaceSlug = workspaceSlugByPath.get(wsPath)
      ?? workspaceRouteSlugBase({ path: wsPath, workspaceName: wsName })
    const search = {
      ...(nextIntent.worktreeRequested ? { newWorktree: '1' } : {}),
      ...(nextIntent.planModeRequested ? { newPlan: '1' } : {}),
    }
    void navigate({ to: '/$workspaceSlug', params: { workspaceSlug }, search })
    setComposerFocusSignal((current) => current + 1)
  }, [navigate, workspaceSlugByPath])

  const handleRoutedSessionResolved = useCallback(async (result: DesktopV3RoutedStartResult, authority: DesktopV3RoutedWorkspaceAuthority): Promise<void> => {
    const expectedWorkspacePath = authority.workspace_path.trim()
    if (!expectedWorkspacePath || routedActivationWorkspaceRef.current !== expectedWorkspacePath) {
      throw new Error('Routed Desktop activation is stale')
    }
    const activationGeneration = ++routedActivationGenerationRef.current
    let canonicalWorkspace: WorkspaceEntry
    try {
      const returnedAuthority = desktopV3RoutedResultResponse(result).session_view.identity
      const sourceWorkspacePath = returnedAuthority.source_workspace_path.trim()
      const runtimeWorkspacePath = returnedAuthority.runtime_workspace_path.trim()
      const expectedRuntimeWorkspacePath = authority.runtime_workspace_path.trim()
      const runtimeAuthorityMatches = returnedAuthority.worktree_enabled
        ? runtimeWorkspacePath === returnedAuthority.worktree_root_path?.trim()
        : runtimeWorkspacePath === expectedRuntimeWorkspacePath
      if (sourceWorkspacePath !== expectedWorkspacePath
        || returnedAuthority.workspace_binding_id?.trim() !== authority.workspace_binding_id
        || returnedAuthority.runtime_swarm_id?.trim() !== authority.swarm_id
        || !runtimeAuthorityMatches) {
        throw new Error('Routed Desktop start returned authority for a different workspace')
      }
      canonicalWorkspace = workspaceByPath.get(sourceWorkspacePath)
        ?? workspaces.find((workspace) => workspace.workspaceId && workspace.workspaceId === returnedAuthority.source_workspace_id)
        ?? (() => { throw new Error('Routed Desktop start returned an unknown source workspace') })()
    } catch (error) {
      setDesktopToast({ message: error instanceof Error ? error.message : 'Routed session authority is invalid.', tone: 'error' })
      throw error
    }
    const activationStillCurrent = () => activationGeneration === routedActivationGenerationRef.current
      && routedActivationWorkspaceRef.current === expectedWorkspacePath
    await activateDesktopV3RoutedSession(
      result,
      desktopV3RoutedActivationDeps,
      activationStillCurrent,
      async (response) => {
        if (!activationStillCurrent()) throw new Error('Routed Desktop activation is stale')
        const identity = response.session_view.identity
        const workspaceSlug = workspaceSlugByPath.get(canonicalWorkspace.path)
          ?? workspaceRouteSlugBase({ path: canonicalWorkspace.path, workspaceName: identity.source_workspace_name })
        await navigate({
          to: '/$workspaceSlug/$sessionId',
          params: { workspaceSlug, sessionId: response.session_id },
          replace: true,
        })
        setMobileSidebarOpen(false)
      },
    ).catch((error) => {
      if (activationStillCurrent()) {
        setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to activate routed session.', tone: 'error' })
      }
      throw error
    })
  }, [navigate, workspaceByPath, workspaces, workspaceSlugByPath])

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

  const handleEnterSidebarSelectionMode = useCallback((group: SidebarSessionGroupID) => {
    setSidebarMasterSelectionGroup((current) => current ?? group)
    setSidebarSelectionMode(true)
  }, [])

  const handleToggleSidebarSelected = useCallback((sessionId: string, range: boolean) => {
    const normalized = sessionId.trim()
    if (!normalized) return
    setSelectedSidebarRootIDs((current) => {
      const next = new Set(current)
      const lastSelectedSidebarRootID = lastSelectedSidebarRootIDRef.current
      if (range && lastSelectedSidebarRootID) {
        const start = visibleSidebarRootIDs.indexOf(lastSelectedSidebarRootID)
        const end = visibleSidebarRootIDs.indexOf(normalized)
        if (start >= 0 && end >= 0) visibleSidebarRootIDs.slice(Math.min(start, end), Math.max(start, end) + 1).forEach((id) => next.add(id))
      } else if (next.has(normalized)) next.delete(normalized)
      else next.add(normalized)
      return next
    })
    lastSelectedSidebarRootIDRef.current = normalized
  }, [visibleSidebarRootIDs])

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
      setSidebarMasterSelectionGroup(null)
      lastSelectedSidebarRootIDRef.current = null
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
        pendingAction: sidebarSessionActions[routeSessionId] === 'pin' || sidebarSessionActions[routeSessionId] === 'archive' || sidebarSessionActions[routeSessionId] === 'rename'
          ? sidebarSessionActions[routeSessionId]
          : null,
        onTogglePinned: () => {
          if (activeRouteSession && activeRouteSessionIsRegularChat && activeRouteSessionCanPin) {
            handleToggleSidebarPinned(activeRouteSession.id)
          }
        },
        onArchive: () => handleArchiveSidebarSession(routeSessionId),
        onRename: (title: string) => handleRenameSidebarSession(routeSessionId, title),
      }
    : null

  const openPlanModalForSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) return
    setPlanModal({ sessionId: normalizedSessionId })
    setPlanModalError(null)
    void fetchAndApplyDesktopV3PlanSnapshot(normalizedSessionId)
      .catch((error) => setPlanModalError(error instanceof Error ? error.message : String(error)))
  }, [])

  const handleCopyPlanText = useCallback(async (text: string): Promise<boolean> => {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      return false
    }
  }, [])

  const handleOpenSettingsTab = useCallback((tab: SettingsTabID | 'agents') => {
    if (tab === 'agents') {
      const search = { agentSetup: '1', agent: 'swarm' }
      if (routeSessionId && routeWorkspaceSlug) {
        void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug: routeWorkspaceSlug, sessionId: routeSessionId }, search })
      } else if (routeWorkspaceSlug) {
        void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug }, search })
      }
      return
    }
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

  const handleAICommit = useCallback(async (
    input: Pick<GitCommitModalState, 'workspacePath' | 'sessionId'>,
  ) => {
    if (gitCommitBusy || gitAICommitRunningRef.current) return

    gitAICommitRunningRef.current = true
    setGitAICommitPhase('generating')
    setDesktopToast({ message: 'AI Commit is generating a commit message. Please wait…', tone: 'info' })
    try {
      const suggestion = await suggestWorkspaceCommitMessage({
        workspacePath: input.workspacePath,
        sessionId: input.sessionId,
      })
      setGitAICommitPhase('committing')
      setDesktopToast({ message: `AI Commit is committing “${suggestion.message}”. Please wait…`, tone: 'info' })
      await commitWorkspaceChanges({
        workspacePath: input.workspacePath,
        sessionId: input.sessionId,
        message: suggestion.message,
        all: true,
      })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['workspace-git-status'] }),
        queryClient.invalidateQueries({ queryKey: ['session-worktree-review'] }),
      ])

      setDesktopToast({ message: `Changes committed with “${suggestion.message}”.`, tone: 'success' })
    } catch (error) {
      setDesktopToast({ message: `AI Commit failed: ${error instanceof Error ? error.message : String(error)}`, tone: 'error' })
    } finally {
      gitAICommitRunningRef.current = false
      setGitAICommitPhase(null)
    }
  }, [gitCommitBusy, queryClient])

  const handleSlashCommand = useCallback(async (command: DesktopSlashCommand, draft = '') => {
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
      case 'open-codex-usage':
        setCodexUsageOpen(true)
        setMobileSidebarOpen(false)
        return
      case 'open-commit-modal': {
        const workspacePath = selectedWorkspace?.path || selectedWorkspacePath || ''
        const workspaceName = selectedWorkspace?.workspaceName || fallbackWorkspaceNameFromPath(workspacePath)
        if (workspacePath) openMainWorktreeGitPanel(workspacePath, workspaceName)
        return
      }
      case 'ai-commit': {
        const workspacePath = selectedGitWorkspacePath || selectedWorkspace?.path || selectedWorkspacePath || ''
        if (!workspacePath) {
          setDesktopToast({ message: 'Open a workspace before running AI Commit.', tone: 'error' })
          return
        }
        await handleAICommit({
          workspacePath,
          sessionId: selectedGitWorkspacePath ? selectedGitSessionId : '',
        })
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
        const parsed = parseDesktopNewSessionCommand(draft) ?? {
          prompt: '',
          worktreeRequested: action.worktreeRequested,
          planModeRequested: action.planModeRequested,
        }
        if (!routeSessionId) {
          setDesktopToast({ message: 'You’re already starting a new session—just type your request in the chat.', tone: 'info' })
          return
        }
        const session = sessionById.get(routeSessionId)
        const workspacePath = session?.workspacePath || selectedWorkspace?.path || selectedWorkspacePath || ''
        const workspaceName = session?.workspaceName || selectedWorkspace?.workspaceName || fallbackWorkspaceNameFromPath(workspacePath)
        if (workspacePath) handleStartNewSessionInWorkspace(workspacePath, workspaceName, parsed)
        return
      }
      case 'start-background-router-session': {
        const { request, mode } = parseDesktopTaskCommand(draft)
        if (!request) {
          const error = new Error('Enter a task request after /task.')
          setDesktopToast({ message: error.message, tone: 'error' })
          throw error
        }
        const clientRequestId = `desktop-v3-background-router:${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`
        let launch: ReturnType<typeof postDesktopV3BackgroundRouterSessionStart>
        try {
          if (!activeWorkspaceAuthority) throw new Error('Background Router session requires the active workspace authority')
          launch = postDesktopV3BackgroundRouterSessionStart({
            ...activeWorkspaceAuthority,
            input: request,
            client_request_id: clientRequestId,
            agent_name: 'swarm',
            metadata: { source: 'desktop-v3-task-command' },
            plan_mode_requested: mode === 'plan',
          })
        } catch (error) {
          setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to start background Router session', tone: 'error' })
          throw error
        }
        setDesktopToast({ message: 'Background Router task sent.', tone: 'success' })
        void launch.catch((error) => {
          setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to start background Router session', tone: 'error' })
        })
        return
      }
      case 'show-help':
        setDesktopToast({ message: 'Slash commands: use ↑/↓ to choose, Enter to run, Tab to insert.', tone: 'info' })
        return
      case 'toggle-tips': {
        try {
          const result = await executeDesktopTipsCommand(
            draft,
            normalizeShowTipsEnabled(uiSettingsQuery.data ?? uiSettings),
            saveShowTipsSetting,
          )
          if (!result) {
            setDesktopToast({ message: 'Use /tips, /tips on, /tips off, /tips toggle, or /tips status.', tone: 'error' })
            return
          }
          if (result.saved) {
            setUISettings(result.saved)
            queryClient.setQueryData(uiSettingsQueryKey(), result.saved)
          }
          setDesktopToast({
            message: result.mode === 'status'
              ? `Home tips are ${result.enabled ? 'on' : 'off'}.`
              : `Home tips turned ${result.enabled ? 'on' : 'off'}.`,
            tone: result.mode === 'status' ? 'info' : 'success',
          })
        } catch (error) {
          setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to update home tips.', tone: 'error' })
        }
        return
      }
      case 'open-artifact-viewer':
      case 'open-action-chooser':
      case 'open-model-picker':
      case 'toggle-thinking':
      case 'compact-session':
      case 'enable-new-session-worktree':
        return
      default: {
        const _exhaustive: never = action
        return _exhaustive
      }
    }
  }, [activeWorkspaceAuthority, handleAICommit, handleOpenSettingsTab, handleStartNewSessionInWorkspace, openMainWorktreeGitPanel, openPlanModalForSession, queryClient, routeSessionId, selectedGitSessionId, selectedGitWorkspacePath, selectedWorkspace?.path, selectedWorkspace?.workspaceName, selectedWorkspacePath, sessionById, topWorkspacePath, uiSettings, uiSettingsQuery.data])

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

  const handleOpenWorkspacePicker = useCallback(() => {
    setWorkspacePickerOpen(true)
    setMobileSidebarOpen(false)
    setWorkspaceDropdownOpen(false)
  }, [])

  const handleSelectWorkspaceFromPicker = useCallback((workspace: WorkspaceEntry) => {
    setWorkspacePickerOpen(false)
    handleOpenWorkspace(workspace.path, workspace.workspaceName)
  }, [handleOpenWorkspace])

  const canStartNewSession = Boolean(topWorkspacePath)
  const canReturnToPreviousChat = Boolean(previousChatSessionId && sessionById.has(previousChatSessionId))
  useEffect(() => {
    function desktopShortcutMatches(event: globalThis.KeyboardEvent, key: string): boolean {
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

    function handleDesktopShortcut(event: globalThis.KeyboardEvent) {
      if (event.defaultPrevented) return
      const target = event.target
      const element = target instanceof HTMLElement ? target : null
      const insideDialog = Boolean(element?.closest('[role="dialog"]'))
      const insideChatComposer = shortcutTargetIsChatComposer(target)
      const targetBlocksDesktopShortcuts = shortcutTargetBlocksDesktopShortcuts(target)
      const normalizedKey = event.key.toLowerCase()
      const normalizedCode = event.code.toLowerCase()
      if (event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey && (normalizedKey === 'w' || normalizedCode === 'keyw') && !insideDialog) {
        event.preventDefault()
        handleOpenWorkspacePicker()
        return
      }
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
  }, [handleOpenLatestNeedsApproval, handleOpenPreviousChat, handleOpenQuickActions, handleOpenSearchChats, handleOpenSettingsTab, handleOpenWorkspacePicker, handleStartNewSessionInWorkspace, topWorkspaceLabel, topWorkspacePath])

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
      id: 'workspace-picker',
      label: 'Switch workspace',
      description: 'Open the numbered workspace picker and press 1–9 or 0 to switch.',
      keys: ['Alt', 'W'],
      availability: 'Available anywhere in Desktop unless another modal owns the shortcut.',
      enabled: mergedSidebarWorkspaceEntries.length > 0,
      disabledReason: 'No workspaces are available.',
      icon: Folder,
      onRun: () => {
        setQuickActionsOpen(false)
        handleOpenWorkspacePicker()
      },
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
      id: 'enable-new-session-plan',
      label: 'Enable plan mode',
      description: 'Enable plan mode for the new chat composer.',
      keys: ['Shift', 'Tab'],
      availability: 'Available only before a new chat is started.',
      enabled: Boolean(topWorkspacePath && !routeSessionId),
      disabledReason: 'Open a new chat before enabling plan mode.',
      icon: ListChecks,
      onRun: () => {
        setQuickActionsOpen(false)
        if (!topWorkspacePath || routeSessionId) return
        handleStartNewSessionInWorkspace(topWorkspacePath, topWorkspaceLabel, { planModeRequested: true })
      },
    },
  ], [canReturnToPreviousChat, canStartNewSession, handleOpenLatestNeedsApproval, handleOpenPreviousChat, handleOpenQuickActions, handleOpenSearchChats, handleOpenSettingsTab, handleOpenWorkspacePicker, handleStartNewSessionInWorkspace, latestNeedsApprovalSession, mergedSidebarWorkspaceEntries.length, routeSessionId, topWorkspaceLabel, topWorkspacePath])


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

  const openBackgroundTaskModal = useCallback(() => {
    setBackgroundTaskRequest('')
    setBackgroundTaskError(null)
    if (typeof window !== 'undefined' && window.matchMedia('(max-width: 639px)').matches && routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/task', params: { workspaceSlug: routeWorkspaceSlug } })
      return
    }
    setBackgroundTaskOpen(true)
  }, [navigate, routeWorkspaceSlug])

  const closeBackgroundTaskModal = useCallback(() => {
    setBackgroundTaskOpen(false)
    setBackgroundTaskError(null)
    if (mobileCreationPage === 'task' && routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
    }
  }, [mobileCreationPage, navigate, routeWorkspaceSlug])

  const handleStartBackgroundRouterSession = useCallback((submittedRequest = backgroundTaskRequest) => {
    const request = submittedRequest.trim()
    if (!request) return
    setBackgroundTaskError(null)
    const clientRequestId = `desktop-v3-background-router:${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`
    let launch: ReturnType<typeof postDesktopV3BackgroundRouterSessionStart>
    try {
      if (!activeWorkspaceAuthority) throw new Error('Background Router session requires the active workspace authority')
      launch = postDesktopV3BackgroundRouterSessionStart({
        ...activeWorkspaceAuthority,
        input: request,
        client_request_id: clientRequestId,
        agent_name: 'swarm',
        metadata: { source: 'desktop-v3-background-task-form' },
        plan_mode_requested: false,
      })
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to start background Router session'
      setBackgroundTaskError(message)
      setDesktopToast({ message, tone: 'error' })
      return
    }
    setBackgroundTaskOpen(false)
    setBackgroundTaskRequest('')
    setDesktopToast({ message: 'Background Router task sent.', tone: 'success' })
    if (mobileCreationPage === 'task' && routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
    }
    void launch.catch((error) => {
      setDesktopToast({ message: error instanceof Error ? error.message : 'Failed to start background Router session', tone: 'error' })
    })
  }, [activeWorkspaceAuthority, backgroundTaskRequest, mobileCreationPage, navigate, routeWorkspaceSlug])

  const openRouteWorkspaceWorktree = useCallback((workspace: WorkspaceEntry | null = routeWorkspace) => {
    if (!workspace?.path) return
    handleStartNewSessionInWorkspace(workspace.path, workspace.workspaceName, { worktreeRequested: true })
  }, [handleStartNewSessionInWorkspace, routeWorkspace])

  const renderMobileSessions = (nodes: SidebarSessionNode[]) => renderSidebarSessionGroups({
    nodes,
    presentation: 'mobile',
    routeSessionId,
    now: sidebarNow,
    workspaceSlug: globalSessionWorkspaceSlug,
    expandedAgentSessions,
    agentSummaries: sidebarAgentSummaries,
    compactingSession,
    pendingActions: sidebarSessionActions,
    selectionMode: sidebarSelectionMode,
    selectedRootIDs: selectedSidebarRootIDs,
    hideInactiveHours: sidebarHideInactiveHours,
    thresholdSaving: sidebarThresholdSaving,
    bulkArchivePending,
    masterSelectionGroup: sidebarMasterSelectionGroup,
    reviewCleanupOpen: needsReviewCleanupOpen,
    gitHasGit: topWorkspaceHasGit,
    gitAheadCount: topWorkspaceGitAheadCount,
    gitBehindCount: topWorkspaceGitBehindCount,
    gitDirtyCount: topWorkspaceGitDirtyCount,
    onOpenGit: () => openMainWorktreeGitPanel(topWorkspacePath, topWorkspaceLabel),
    onToggleReviewCleanup: () => setNeedsReviewCleanupOpen((open) => !open),
    onEnterSelectionMode: handleEnterSidebarSelectionMode,
    onClearSelection: handleClearSidebarSelection,
    onBulkArchive: () => { void handleBulkArchiveSidebar() },
    onThresholdChange: (hours) => { void handleSidebarThresholdChange(hours) },
    onSelect: handleSelectSession,
    onToggleSelected: handleToggleSidebarSelected,
    onPrefetch: handlePrefetchSession,
    onToggleAgents: handleToggleAgentSessions,
    onTogglePinned: handleToggleSidebarPinned,
    onArchive: handleArchiveSidebarSession,
    onRename: handleRenameSidebarSession,
  })

  const mobileSessionQuickMenu = routeWorkspace?.path ? (
    <div className="flex min-h-0 w-full flex-1 flex-col bg-[var(--app-bg)]" data-testid="mobile-workspace-home">
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--app-border)] px-4 py-3">
        <div className="min-w-0 flex-1">
          <p className="text-[11px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Workspace</p>
          <div className="relative mt-0.5 max-w-full">
            <select
              aria-label="Change workspace"
              className="min-h-8 w-full appearance-none truncate rounded-lg border border-transparent bg-transparent py-1 pl-0 pr-7 text-base font-semibold text-[var(--app-text)] outline-none transition focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
              value={routeWorkspace.path}
              onChange={(event) => {
                const workspace = mergedSidebarWorkspaceEntries.find((candidate) => candidate.path === event.target.value)
                if (workspace) handleOpenWorkspace(workspace.path, workspace.workspaceName)
              }}
            >
              {mergedSidebarWorkspaceEntries.map((workspace) => (
                <option key={workspace.path} value={workspace.path}>{workspace.workspaceName}</option>
              ))}
            </select>
            <ChevronDown size={16} className="pointer-events-none absolute right-1 top-1/2 -translate-y-1/2 text-[var(--app-text-subtle)]" aria-hidden="true" />
          </div>
        </div>
        <button
          type="button"
          className="inline-flex min-h-11 shrink-0 touch-manipulation items-center gap-2 rounded-xl border border-[var(--app-primary)] bg-[var(--app-surface)] px-4 text-sm font-semibold text-[var(--app-primary)] shadow-sm transition active:bg-[var(--app-surface-hover)]"
          onClick={openBackgroundTaskModal}
        >
          <ListChecks size={17} aria-hidden="true" />
          <span>Task</span>
        </button>
      </div>

      <section className="flex min-h-0 flex-1 flex-col" aria-labelledby="mobile-workspace-sessions-heading">
        <div data-mobile-active-sessions-header className="flex min-h-11 shrink-0 items-center justify-between px-4 pt-1">
          <h2 id="mobile-workspace-sessions-heading" className="text-xs font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Sessions</h2>
          {mobileActiveSessionNodes.length > 0 ? <span className="text-xs text-[var(--app-text-subtle)]">{mobileActiveSessionNodes.length} active</span> : null}
        </div>
        <div className="grid min-h-0 content-start gap-2 overflow-y-auto px-3 pb-3 [-webkit-overflow-scrolling:touch]" data-testid="mobile-workspace-session-scroll">
          {renderMobileSessions(mobileActiveSessionNodes) ?? (
            <div className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-6 text-center text-sm text-[var(--app-text-subtle)]">No active sessions yet.</div>
          )}
          {mobilePreviousSessionNodes.length > 0 ? (
            <div className="mt-1 border-t border-[var(--app-border)] pt-2">
              <button type="button" className="flex min-h-11 w-full touch-manipulation items-center justify-between rounded-xl px-3 text-left text-xs text-[var(--app-text-muted)] active:bg-[var(--app-surface-hover)]" onClick={() => setMobilePreviousSessionsOpen((open) => !open)} aria-expanded={mobilePreviousSessionsOpen}>
                <span>{mobilePreviousSessionNodes.length} previous session{mobilePreviousSessionNodes.length === 1 ? '' : 's'}</span>
                <ChevronDown size={16} className={cn('transition-transform', mobilePreviousSessionsOpen && 'rotate-180')} aria-hidden="true" />
              </button>
              {mobilePreviousSessionsOpen ? <div className="mt-2 grid gap-2">{renderMobileSessions(mobilePreviousSessionNodes)}</div> : null}
            </div>
          ) : null}
        </div>
      </section>
    </div>
  ) : null

  useEffect(() => {
    setMobileSidebarOpen(false)
  }, [routeSessionId, routeWorkspaceSlug])

  useEffect(() => {
    routedActivationGenerationRef.current += 1
  }, [routeSessionId, routeWorkspace?.path])

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

  const integrateSessionWorktree = async (input: GitIntegrateModalState) => {
    await reviewDesktopV3Worktrees({
      workspacePath: input.workspacePath,
      integrateSessionIds: [input.sessionId],
      graceHours: 1,
    })
  }

  const archiveIntegratedSession = async (input: GitIntegrateModalState) => {
    await reviewDesktopV3Worktrees({
      workspacePath: input.workspacePath,
      archiveSessionIds: [input.sessionId],
      graceHours: 1,
    })
    handleArchivePlanSession(input.sessionId)
  }

  const openWorkspaceAction = (action: WorkspaceAction) => {
    setWorkspaceActionPresentation({ action, mode: 'standalone', workspacePath: selectedGitWorkspacePath || action.workspacePath, sessionId: selectedGitSessionId })
  }

  const runAICommitWorkspaceAction = async (action: WorkspaceAction, workspacePath: string, sessionId: string) => {
    if (gitCommitBusy || gitAICommitRunningRef.current) return

    gitAICommitRunningRef.current = true
    setGitAICommitPhase('generating')
    setWorkspaceActionPresentation(null)
    setDesktopToast({ message: `AI Commit is preparing changes before “${action.name}”.`, tone: 'info' })
    try {
      const suggestion = await suggestWorkspaceCommitMessage({ workspacePath, sessionId })
      setGitAICommitPhase('committing')
      await commitWorkspaceChanges({ workspacePath, sessionId, message: suggestion.message, all: true })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['workspace-git-status'] }),
        queryClient.invalidateQueries({ queryKey: ['session-worktree-review'] }),
      ])
      const inputs = Object.fromEntries(action.inputs.map((input) => [input.id, input.defaultValue]))
      const run = await startWorkspaceAction(workspacePath, action.id, inputs, sessionId)
      setWorkspaceActionPresentation({ action, mode: 'post-commit', workspacePath, sessionId, initialRun: run })
      setDesktopToast({ message: `Committed “${suggestion.message}”; ${action.name} is running.`, tone: 'success' })
    } catch (error) {
      setDesktopToast({ message: `AI Commit + Action failed: ${error instanceof Error ? error.message : String(error)}`, tone: 'error' })
    } finally {
      gitAICommitRunningRef.current = false
      setGitAICommitPhase(null)
    }
  }

  const openGitCommitReview = (modal: GitCommitModalState) => {
    if (gitAICommitRunningRef.current) return
    setGitCommitMessage('')
    setGitCommitError(null)
    setGitCommitIntegrate(false)
    setGitCommitArchive(false)
    setGitCommitModal(modal)
  }

  const handleGitCommit = async () => {
    const modal = gitCommitModal
    const message = gitCommitMessage.trim()
    if (!modal || gitCommitBusy || !message) return

    setGitCommitBusy(true)
    setGitCommitError(null)
    let commitSucceeded = false
    const archiveAfterCommit = !modal.worktree && Boolean(modal.sessionId) && gitCommitArchive
    const integration = modal.worktree && gitCommitIntegrate && modal.canIntegrate && modal.targetWorkspacePath
      ? {
          sessionId: modal.sessionId,
          workspacePath: modal.targetWorkspacePath,
          worktreeBranch: activeGitSession?.worktreeBranch?.trim() || gitSnapshot?.branch || 'worktree',
          targetBranch: modal.targetBranch || activeSessionTargetBranch,
        }
      : null
    try {
      await commitWorkspaceChanges({
        workspacePath: modal.workspacePath,
        sessionId: modal.sessionId,
        message,
        all: true,
      })
      commitSucceeded = true
      setGitCommitModal(null)
      setGitCommitMessage('')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['workspace-git-status'] }),
        queryClient.invalidateQueries({ queryKey: ['session-worktree-review'] }),
      ])

      let completionMessage = 'Changes committed successfully.'

      if (integration) {
        setGitIntegrateModal(integration)
        setGitIntegrateArchive(gitCommitArchive)
        setGitIntegrateError(null)
        setGitIntegrateBusy(true)
        await integrateSessionWorktree(integration)
        const integrated = { ...integration, integrationComplete: true }
        setGitIntegrateModal(integrated)
        if (gitCommitArchive) await archiveIntegratedSession(integrated)
        setGitIntegrateModal(null)
        completionMessage = gitCommitArchive ? 'Changes committed, integrated, and session archived.' : 'Changes committed and integrated successfully.'
      } else if (archiveAfterCommit) {
        await archiveDesktopV3Sessions([modal.sessionId])
        handleArchivePlanSession(modal.sessionId)
        completionMessage = 'Changes committed and session archived.'
      }

      if (integration || archiveAfterCommit) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ['workspace-git-status'] }),
          queryClient.invalidateQueries({ queryKey: ['session-worktree-review'] }),
        ])
      }

      setDesktopToast({ message: completionMessage, tone: 'success' })
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      if (commitSucceeded && integration) {
        setGitIntegrateError(message)
      } else if (commitSucceeded && archiveAfterCommit) {
        setDesktopToast({ message: `Changes committed, but the session could not be archived: ${message}`, tone: 'error' })
      } else {
        setGitCommitError(message)
      }
    } finally {
      setGitCommitBusy(false)
      setGitIntegrateBusy(false)
    }
  }

  const handleGitIntegrate = async (archiveAfterIntegration = gitIntegrateArchive) => {
    const modal = gitIntegrateModal
    if (!modal || gitIntegrateBusy) return
    setGitIntegrateArchive(archiveAfterIntegration)
    setGitIntegrateBusy(true)
    setGitIntegrateError(null)
    try {
      const integrated = modal.integrationComplete ? modal : { ...modal, integrationComplete: true }
      if (!modal.integrationComplete) {
        await integrateSessionWorktree(modal)
        setGitIntegrateModal(integrated)
      }
      if (archiveAfterIntegration) await archiveIntegratedSession(integrated)
      setGitIntegrateModal(null)
      setDesktopToast({ message: archiveAfterIntegration ? 'Worktree integrated and session archived.' : 'Worktree integrated successfully.', tone: 'success' })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['workspace-git-status'] }),
        queryClient.invalidateQueries({ queryKey: ['session-worktree-review'] }),
      ])
    } catch (error) {
      setGitIntegrateError(error instanceof Error ? error.message : String(error))
    } finally {
      setGitIntegrateBusy(false)
    }
  }

  const handleAskSwarmForGitIntegrationHelp = async () => {
    const modal = gitIntegrateModal
    const integrationError = gitIntegrateError
    if (!modal || modal.presentation !== 'sidebar-popout' || modal.integrationComplete || !integrationError || gitIntegrateBusy || gitIntegrateHelpBusy) return
    setGitIntegrateHelpBusy(true)
    try {
      await launchDesktopRepairSession({
        owningWorkspacePath: modal.workspacePath,
        sourceSessionId: modal.sessionId,
        prompt: buildGitSidebarIntegrationHelpPrompt(modal, integrationError),
        title: `Review integration failure: ${modal.worktreeBranch || modal.sessionId}`,
        source: 'desktop-v3-git-sidebar-integration-help',
        messageMetadata: {
          worktree_branch: modal.worktreeBranch,
          target_branch: modal.targetBranch,
          target_workspace_path: modal.workspacePath,
          integration_error: integrationError,
        },
      })
      setGitIntegrateModal(null)
      setGitIntegrateArchive(false)
      setDesktopToast({ message: 'Started a new Swarm session for this integration error.', tone: 'success' })
    } catch (error) {
      setDesktopToast({ message: `Could not ask Swarm for integration help: ${error instanceof Error ? error.message : String(error)}`, tone: 'error' })
    } finally {
      setGitIntegrateHelpBusy(false)
    }
  }

  const gitSidebarError = gitRealtimeErrors[selectedGitWorkspacePath]
    || (gitStatusQuery.error instanceof Error ? gitStatusQuery.error.message : '')
  const gitSidebarMissingGit = isMissingGitSidebarError(gitSidebarError)

  const handleAskSwarmToInstallGit = async () => {
    if (!gitSidebarMissingGit || !selectedGitWorkspacePath || !selectedGitSessionId || gitInstallHelpBusy) return
    setGitInstallHelpBusy(true)
    try {
      await launchDesktopRepairSession({
        owningWorkspacePath: activeSessionTargetWorkspacePath || selectedGitWorkspacePath,
        sourceSessionId: selectedGitSessionId,
        prompt: buildInstallGitPrompt(gitSidebarError),
        title: 'Install Git on this machine',
        source: 'desktop-v3-git-sidebar-install-help',
        messageMetadata: {
          workspace_path: selectedGitWorkspacePath,
          git_error: gitSidebarError,
          requested_action: 'install_git',
        },
      })
      setDesktopToast({ message: 'Started a new Swarm session to install Git.', tone: 'success' })
    } catch (error) {
      setDesktopToast({ message: `Could not ask Swarm to install Git: ${error instanceof Error ? error.message : String(error)}`, tone: 'error' })
    } finally {
      setGitInstallHelpBusy(false)
    }
  }

  const closeGitSidebarIntegratePopout = useCallback(() => {
    if (gitIntegrateBusy || gitIntegrateHelpBusy) return
    setGitIntegrateModal((current) => current?.presentation === 'sidebar-popout' ? null : current)
    setGitIntegrateArchive(false)
    setGitIntegrateError(null)
  }, [gitIntegrateBusy, gitIntegrateHelpBusy])

  const positionGitSidebarIntegratePopout = useCallback(() => {
    const anchor = gitIntegrateAnchorRef.current
    const popout = gitIntegratePopoutRef.current
    if (!anchor || !popout) return
    const viewportPadding = 8
    const gap = 4
    const anchorRect = anchor.getBoundingClientRect()
    const width = Math.min(416, Math.max(anchorRect.width, 280), window.innerWidth - viewportPadding * 2)
    const availableAbove = Math.max(0, anchorRect.top - viewportPadding - gap)
    const availableBelow = Math.max(0, window.innerHeight - anchorRect.bottom - viewportPadding - gap)
    const popoutHeight = popout.scrollHeight
    const placeAbove = popoutHeight <= availableAbove || availableAbove >= availableBelow
    const maxHeight = placeAbove ? availableAbove : availableBelow
    const visibleHeight = Math.min(popoutHeight, maxHeight)
    const left = Math.min(Math.max(viewportPadding, anchorRect.left), window.innerWidth - width - viewportPadding)
    const top = placeAbove
      ? Math.max(viewportPadding, anchorRect.top - gap - visibleHeight)
      : anchorRect.bottom + gap
    setGitIntegratePopoutStyle({ left, top, width, maxHeight, visibility: 'visible' })
  }, [])

  useLayoutEffect(() => {
    if (gitIntegrateModal?.presentation !== 'sidebar-popout') return
    positionGitSidebarIntegratePopout()
    const frame = window.requestAnimationFrame(positionGitSidebarIntegratePopout)
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(positionGitSidebarIntegratePopout)
    if (gitIntegratePopoutRef.current) observer?.observe(gitIntegratePopoutRef.current)
    window.addEventListener('resize', positionGitSidebarIntegratePopout)
    window.addEventListener('scroll', positionGitSidebarIntegratePopout, true)
    return () => {
      window.cancelAnimationFrame(frame)
      observer?.disconnect()
      window.removeEventListener('resize', positionGitSidebarIntegratePopout)
      window.removeEventListener('scroll', positionGitSidebarIntegratePopout, true)
    }
  }, [gitIntegrateError, gitIntegrateModal, positionGitSidebarIntegratePopout])

  useEffect(() => {
    if (gitIntegrateModal?.presentation !== 'sidebar-popout') return
    const dismissOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target as Node | null
      if (target && (gitIntegrateAnchorRef.current?.contains(target) || gitIntegratePopoutRef.current?.contains(target))) return
      closeGitSidebarIntegratePopout()
    }
    const dismissOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeGitSidebarIntegratePopout()
    }
    document.addEventListener('pointerdown', dismissOnOutsidePointer)
    document.addEventListener('keydown', dismissOnEscape)
    return () => {
      document.removeEventListener('pointerdown', dismissOnOutsidePointer)
      document.removeEventListener('keydown', dismissOnEscape)
    }
  }, [closeGitSidebarIntegratePopout, gitIntegrateModal?.presentation])

  const planSidebarGitPanel = selectedGitSessionId && selectedGitWorkspacePath ? (
    <>
    <section data-testid="desktop-plan-git-sidebar" className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden" data-plan-git-layout="inset-card" data-plan-section-treatment="inset-card">
      <div className="flex shrink-0 items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]" data-plan-git-header>
        <GitBranch size={13} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate">{gitSnapshot?.branch || 'Git changes'}</span>
        {gitSnapshot?.has_git ? <span className="shrink-0">{gitSnapshot.dirty_count}</span> : null}
      </div>
      {gitSnapshot?.has_git ? (
        <div className="mt-2 flex min-w-0 shrink-0 items-center justify-end gap-1 normal-case tracking-normal" data-plan-git-action-row data-plan-git-commit>
          {gitSnapshot.files.length > 0 ? <>
            <button type="button" className="grid min-h-9 w-9 shrink-0 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-60" disabled={gitCommitBusy || gitAICommitPhase !== null} onClick={() => openGitCommitReview({ workspacePath: selectedGitWorkspacePath, sessionId: selectedGitSessionId, files: gitSnapshot.files, worktree: activeSessionWorktree, targetWorkspacePath: activeSessionTargetWorkspacePath, targetBranch: activeSessionTargetBranch, canIntegrate: Boolean(activeSessionReviewCandidate?.commit_eligible && activeSessionTargetWorkspacePath) })} aria-label="Commit changes" title="Commit changes"><Save size={14} aria-hidden="true" /></button>
            <AICommitButton compact phase={gitAICommitPhase} disabled={gitCommitBusy} onGenerate={() => { void handleAICommit({ workspacePath: selectedGitWorkspacePath, sessionId: selectedGitSessionId }) }} />
          </> : null}
        </div>
      ) : null}
      {activeSessionWorktree ? (
        <details className="group mt-2 shrink-0 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)]" data-plan-git-session-commits>
          <summary className="flex min-h-8 cursor-pointer list-none items-center gap-1.5 px-2 text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)] [&::-webkit-details-marker]:hidden">
            <GitCommitHorizontal size={11} className="shrink-0" />
            <span className="min-w-0 flex-1 truncate">Session commits</span>
            <span className="shrink-0 normal-case tracking-normal">{activeSessionCommits.length}</span>
            <ChevronDown size={12} className="shrink-0 transition-transform group-open:rotate-180" aria-hidden="true" />
          </summary>
          {activeSessionCommits.length > 0 ? <div className="max-h-28 overflow-y-auto border-t border-[var(--app-border)] [scrollbar-gutter:stable]" data-plan-git-session-commit-list>{activeSessionCommits.map((commit) => <div key={commit.hash} className="flex min-w-0 items-start gap-2 border-b border-[var(--app-border)] px-2 py-1.5 text-[10px] last:border-0"><span className="shrink-0 font-mono text-[var(--app-primary)]">{commit.short_hash}</span><span className="min-w-0 flex-1 truncate text-[var(--app-text-muted)]" title={commit.subject}>{commit.subject}</span></div>)}</div> : <div className="border-t border-[var(--app-border)] px-2 py-1.5 text-[10px] text-[var(--app-text-subtle)]">No commits yet.</div>}
        </details>
      ) : null}
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden" data-plan-git-scroll-region>
        {gitSidebarMissingGit ? <button type="button" className="mt-2 inline-flex min-h-9 w-full items-center justify-center gap-1.5 rounded-lg border border-[var(--app-warning)] px-2 py-1.5 text-xs font-semibold text-[var(--app-warning)] hover:bg-[var(--app-warning-bg)] disabled:cursor-not-allowed disabled:opacity-60" disabled={gitInstallHelpBusy} onClick={() => { void handleAskSwarmToInstallGit() }}>{gitInstallHelpBusy ? <LoaderCircle size={13} className="animate-spin" aria-hidden="true" /> : <Bot size={13} aria-hidden="true" />}{gitInstallHelpBusy ? 'Asking Swarm…' : "Git isn't installed, Ask Swarm to install Git?"}</button>
          : gitSidebarError ? <div className="mt-2 text-xs text-[var(--app-warning)]">{gitSidebarError}</div>
          : gitStatusQuery.isPending ? <div className="mt-2 text-xs text-[var(--app-text-subtle)]">Loading scoped changes…</div>
          : !gitSnapshot?.has_git ? <div className="mt-2 text-xs text-[var(--app-text-subtle)]">No Git repository for this session.</div>
          : gitSnapshot.files.length === 0 ? <div className="mt-2 text-xs text-[var(--app-text-subtle)]">Clean working tree.</div>
          : <details className="group mt-2 min-h-0 shrink overflow-hidden rounded-xl bg-[var(--app-bg-alt)]" data-plan-git-file-details>
              <summary className="flex min-h-8 cursor-pointer list-none items-center gap-2 px-2 text-[10px] font-semibold text-[var(--app-text-muted)] [&::-webkit-details-marker]:hidden">
                <span className="min-w-0 flex-1 truncate">{gitSnapshot.files.length} file{gitSnapshot.files.length === 1 ? '' : 's'} changed</span>
                <ChevronDown size={12} className="shrink-0 transition-transform group-open:rotate-180" aria-hidden="true" />
              </summary>
              <div className="max-h-40 overflow-y-auto border-t border-[var(--app-border)] p-1 [scrollbar-gutter:stable]" data-plan-git-file-list data-plan-git-scroll="inside-disclosure">{gitSnapshot.files.map((file) => <div key={`${file.kind}:${file.path}:${file.orig_path ?? ''}`} className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-[10px] hover:bg-[var(--app-surface-hover)]"><span className={cn('shrink-0 rounded px-1 py-0.5', file.untracked ? 'bg-[var(--app-warning-bg)] text-[var(--app-warning)]' : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-subtle)]')}>{gitFileStatusLabel(file)}</span><span className="min-w-0 flex-1 truncate" title={file.path}>{file.path}</span></div>)}</div>
            </details>}
      </div>
      {activeSessionIntegrateEligible && activeSessionReviewCandidate ? <div ref={gitIntegrateAnchorRef} className="relative mt-2 shrink-0" data-plan-git-integrate-anchor>
        {gitIntegrateModal?.presentation === 'sidebar-popout' && typeof document !== 'undefined' ? createPortal(
          <div ref={gitIntegratePopoutRef} className="fixed z-[90] grid min-w-0 gap-1 overflow-y-auto overscroll-contain rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] p-1 text-xs shadow-xl" style={gitIntegratePopoutStyle} role="dialog" aria-label="Confirm Git sidebar integration" data-plan-git-integrate-popout>
            <div className="flex min-h-8 items-center justify-end px-1"><button type="button" className="inline-flex h-8 w-8 items-center justify-center rounded-full text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)] disabled:opacity-50" aria-label="Close Git integration options" disabled={gitIntegrateBusy || gitIntegrateHelpBusy} onClick={closeGitSidebarIntegratePopout}><X size={15} /></button></div>
            {gitIntegrateError ? <div className="m-1 min-w-0 rounded-md border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-2 text-[var(--app-danger)]" role="alert"><p className="break-words">{gitIntegrateError}</p>{gitIntegrateModal.integrationComplete ? <p className="mt-1 text-[var(--app-text-subtle)]">The worktree is integrated. Retry only the remaining archive step.</p> : null}</div> : null}
            {gitIntegrateError && !gitIntegrateModal.integrationComplete ? <button type="button" className="inline-flex min-h-11 w-full items-center justify-center gap-1.5 rounded-md px-3 py-1.5 font-semibold text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] disabled:opacity-50" disabled={gitIntegrateBusy || gitIntegrateHelpBusy} onClick={() => void handleAskSwarmForGitIntegrationHelp()}>{gitIntegrateHelpBusy ? <LoaderCircle size={13} className="animate-spin" /> : <Bot size={13} />}{gitIntegrateHelpBusy ? 'Asking Swarm…' : 'Ask Swarm for Help'}</button> : null}
            <IntegrationConfirmation
              targetBranch={gitIntegrateModal.targetBranch}
              worktreeBranch={gitIntegrateModal.worktreeBranch}
              archiveAfter={gitIntegrateArchive}
              busy={gitIntegrateBusy || gitIntegrateHelpBusy}
              integrationComplete={gitIntegrateModal.integrationComplete}
              retrying={Boolean(gitIntegrateError)}
              onArchiveAfterChange={setGitIntegrateArchive}
              onConfirm={() => void handleGitIntegrate()}
              onCancel={closeGitSidebarIntegratePopout}
            />
          </div>,
          document.body,
        ) : null}
        <button type="button" className="inline-flex w-full items-center justify-center gap-1.5 rounded-lg border border-[var(--app-primary)] px-2 py-1.5 text-xs font-semibold text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] disabled:opacity-50" data-plan-git-integrate aria-expanded={gitIntegrateModal?.presentation === 'sidebar-popout'} aria-haspopup="dialog" disabled={gitIntegrateBusy || gitIntegrateHelpBusy} onClick={() => {
          if (gitIntegrateModal?.presentation === 'sidebar-popout') {
            positionGitSidebarIntegratePopout()
            return
          }
          setGitIntegrateArchive(false)
          setGitIntegrateError(null)
          setGitIntegratePopoutStyle({ visibility: 'hidden' })
          setGitIntegrateModal({ sessionId: selectedGitSessionId, workspacePath: activeSessionTargetWorkspacePath, worktreeBranch: activeSessionReviewCandidate.worktree_branch || gitSnapshot?.branch || 'worktree', targetBranch: activeSessionReviewCandidate.target_branch || activeSessionTargetBranch, presentation: 'sidebar-popout' })
        }}>{gitIntegrateBusy ? <LoaderCircle size={12} className="animate-spin" /> : gitIntegrateModal?.integrationComplete ? <Archive size={12} /> : <GitMerge size={12} />}{gitIntegrateModal?.presentation === 'sidebar-popout' ? gitIntegrateModal.integrationComplete ? 'Archive session' : gitIntegrateError ? 'Review integration error' : `Confirm integration into ${activeSessionReviewCandidate.target_branch || activeSessionTargetBranch}` : `Integrate into ${activeSessionReviewCandidate.target_branch || activeSessionTargetBranch}`}</button>
      </div> : null}
    </section>
    <WorkspaceActionsSidebarSection workspacePath={selectedGitWorkspacePath} sessionId={selectedGitSessionId} workspaceName={routeWorkspace?.workspaceName || ''} canAICommit={Boolean(gitSnapshot?.files.length) && gitAICommitPhase === null && !gitCommitBusy} onRun={openWorkspaceAction} onAICommitRun={(action) => { void runAICommitWorkspaceAction(action, selectedGitWorkspacePath, selectedGitSessionId) }} />
    </>
  ) : null

  const focusedSidebarContent = (
    <div className="flex h-full flex-col items-center gap-1 py-3" data-testid="desktop-focus-sidebar-controls">
      <Button variant="ghost" className="h-12 w-12 min-w-12 p-0" onClick={() => setSidebarDisplayMode('full')} aria-label="Expand sidebar to full width" title="Full-width sidebar">
        <ChevronRight size={28} className="shrink-0" />
      </Button>
      <Button variant="ghost" className="h-12 w-12 min-w-12 p-0" onClick={() => void navigate({ to: '/' })} aria-label="Back to launcher">
        <Folder size={24} className="shrink-0" />
      </Button>
      {notificationAttentionVisible ? (
        <Button variant="ghost" className={cn('relative h-12 w-12 min-w-12 p-0', notificationUnreadCount > 0 && 'text-[var(--app-primary)]')} onClick={handleOpenNotifications} aria-label="Open notifications" title={notificationUnreadCount > 0 ? `${notificationUnreadCount} unread notification${notificationUnreadCount === 1 ? '' : 's'}` : 'Notifications'}>
          <Bell size={24} className="shrink-0" />
          {notificationUnreadCount > 0 ? <span aria-hidden="true" className="absolute right-2 top-2 grid h-4 min-w-4 place-items-center rounded-full bg-[var(--app-primary)] px-1 text-[9px] font-semibold text-[var(--app-primary-text)]">{notificationUnreadCount > 99 ? '99+' : notificationUnreadCount}</span> : null}
        </Button>
      ) : null}
      {updateAttentionVisible ? (
        <Button variant="ghost" className="relative h-12 w-12 min-w-12 p-0" onClick={() => { void handleDesktopUpdate() }} aria-label={updateActionLabel} title={updateActionTitle} disabled={updateRunning || !updateActionEnabled}>
          <Download size={24} className={cn('shrink-0', updateRunning && 'animate-pulse', updateActionEnabled && 'text-[var(--app-primary)]', updateError && 'text-[var(--app-error)]')} />
          {updateActionEnabled ? <span aria-hidden="true" className="absolute right-2 top-2 h-2.5 w-2.5 rounded-full bg-[var(--app-primary)] shadow-[0_0_10px_var(--app-primary)]" /> : null}
        </Button>
      ) : null}
      <Button variant="ghost" className="mt-auto h-12 w-12 min-w-12 p-0" onClick={() => handleOpenSettingsTab('account')} aria-label="Open settings" title="Settings">
        <Settings size={24} className="shrink-0" />
      </Button>
    </div>
  )

  const sidebarContent = (
    <>
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
                    <div
                      className="mt-px truncate text-[10px] font-medium leading-[1.25] text-[var(--app-text-muted)]"
                      title={sidebarWorkspaceContext}
                    >
                      {sidebarWorkspaceContext}
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
                        {notificationUnreadCount > 0 ? <span aria-hidden="true" className="absolute right-0.5 top-0.5 grid h-3 min-w-3 place-items-center rounded-full bg-[var(--app-primary)] px-0.5 text-[7px] font-semibold leading-none text-[var(--app-primary-text)]">{notificationUnreadCount > 9 ? '9+' : notificationUnreadCount}</span> : null}
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
                      onClick={() => setSidebarDisplayMode('focus')}
                      aria-label="Enter focus mode"
                      title="Focus mode"
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
                      <MessageSquare size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
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
                      className="grid min-h-[28px] w-full grid-cols-[18px_minmax(0,1fr)] items-center gap-2 rounded-md px-2 text-left font-inherit text-[11px] text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text-muted)] disabled:cursor-not-allowed disabled:opacity-50"
                      onClick={() => {
                        if (!topWorkspaceSlug) return
                        setMobileSidebarOpen(false)
                        void navigate({ to: '/$workspaceSlug/studio', params: { workspaceSlug: topWorkspaceSlug } })
                      }}
                      disabled={!topWorkspaceSlug}
                      aria-label="Open Studio"
                      title="Studio"
                    >
                      <Film size={13} strokeWidth={1.8} className="text-[var(--app-text-subtle)]" />
                      <span className="min-w-0 truncate">Studio</span>
                    </button>
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
                    <div ref={workspaceDropdownRef} className="relative min-w-0">
                      <button
                        type="button"
                        className="flex h-7 w-full min-w-0 items-center gap-1 rounded px-1 text-left text-[11px] font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]"
                        aria-label={`Current workspace: ${topWorkspaceLabel}`}
                        aria-haspopup="menu"
                        aria-expanded={workspaceDropdownOpen}
                        title={topWorkspacePath || 'Default Workspace'}
                        onClick={() => setWorkspaceDropdownOpen((open) => !open)}
                      >
                        <span className="min-w-0 flex-1 truncate">{topWorkspaceLabel}</span>
                        <ChevronDown size={12} className="shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />
                      </button>
                      {workspaceDropdownOpen ? (
                        <div
                          role="menu"
                          aria-label="Select workspace"
                          className="absolute left-0 top-8 z-30 max-h-64 min-w-full overflow-y-auto rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] p-1 shadow-xl"
                        >
                          {mergedSidebarWorkspaceEntries.map((workspace) => (
                            <button
                              key={workspace.path}
                              type="button"
                              role="menuitemradio"
                              aria-checked={workspace.path === topWorkspacePath}
                              className="flex min-h-8 w-full min-w-[220px] items-center gap-2 rounded px-2 text-left text-[11px] hover:bg-[var(--app-surface-hover)]"
                              onClick={() => {
                                setWorkspaceDropdownOpen(false)
                                handleOpenWorkspace(workspace.path, workspace.workspaceName)
                              }}
                            >
                              <span className="min-w-0 flex-1 truncate">{workspace.workspaceName}</span>
                              {workspace.path === topWorkspacePath ? <Check size={12} aria-hidden="true" /> : null}
                            </button>
                          ))}
                        </div>
                      ) : null}
                    </div>
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
                      <MessageSquare size={13} strokeWidth={1.8} className="shrink-0" />
                    </button>
                    <button
                      type="button"
                      className={SIDEBAR_ACTION_BUTTON_CLASS}
                      onClick={() => {
                        if (topWorkspace && topWorkspacePath) {
                          openRouteWorkspaceWorktree(topWorkspace)
                        }
                      }}
                      disabled={!topWorkspace}
                      aria-label={`New worktree for ${topWorkspaceLabel}`}
                      title="Worktree"
                    >
                      <GitBranch size={13} strokeWidth={1.8} className="shrink-0" />
                    </button>
                  </div>
                  {videoStudioSessions.length > 0 ? (
                    <section className="grid content-start gap-1.5" aria-labelledby="desktop-video-sessions-heading">
                      <div className="flex min-h-6 items-center gap-1 px-1 pt-1 text-[9px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">
                        <Film size={11} aria-hidden="true" />
                        <span id="desktop-video-sessions-heading">Video</span>
                      </div>
                      <div className="grid gap-1">
                        {videoStudioSessions.map((session) => {
                          const workspaceSlug = globalSessionWorkspaceSlug(session)
                          return (
                            <Link
                              key={session.id}
                              to="/$workspaceSlug/video/$videoSessionId"
                              params={{ workspaceSlug, videoSessionId: session.id }}
                              onClick={() => {
                                setMobileSidebarOpen(false)
                                void selectAndHydrateDesktopV3Session(session.id)
                              }}
                              className={cn(
                                'flex min-h-8 items-center gap-2 rounded-md border px-2.5 py-1.5 text-[12px] text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                                videoStudioRoute && routeSessionId === session.id
                                  ? 'border-[var(--app-border-accent)] bg-[var(--app-surface)] text-[var(--app-text)]'
                                  : 'border-transparent bg-[var(--app-surface)]/45',
                              )}
                            >
                              <Film size={12} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                              <span className="min-w-0 flex-1 truncate">{session.title || 'Untitled video'}</span>
                            </Link>
                          )
                        })}
                      </div>
                    </section>
                  ) : null}
                  {renderSidebarSessionGroups({
                    nodes: globalFlattenedSessionNodes,
                    routeSessionId,
                    now: sidebarNow,
                    workspaceSlug: globalSessionWorkspaceSlug,
                    expandedAgentSessions,
                    agentSummaries: sidebarAgentSummaries,
                    compactingSession,
                    pendingActions: sidebarSessionActions,
                    selectionMode: sidebarSelectionMode,
                    selectedRootIDs: selectedSidebarRootIDs,
                    hideInactiveHours: sidebarHideInactiveHours,
                    thresholdSaving: sidebarThresholdSaving,
                    bulkArchivePending,
                    masterSelectionGroup: sidebarMasterSelectionGroup,
                    reviewCleanupOpen: needsReviewCleanupOpen,
                    gitHasGit: topWorkspaceHasGit,
                    gitAheadCount: topWorkspaceGitAheadCount,
                    gitBehindCount: topWorkspaceGitBehindCount,
                    gitDirtyCount: topWorkspaceGitDirtyCount,
                    onOpenGit: () => openMainWorktreeGitPanel(topWorkspacePath, topWorkspaceLabel),
                    onToggleReviewCleanup: () => setNeedsReviewCleanupOpen((open) => !open),
                    onEnterSelectionMode: handleEnterSidebarSelectionMode,
                    onClearSelection: handleClearSidebarSelection,
                    onBulkArchive: () => { void handleBulkArchiveSidebar() },
                    onThresholdChange: (hours) => { void handleSidebarThresholdChange(hours) },
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
      <aside
        data-testid="desktop-workspace-sidebar"
        data-sidebar-display-mode={sidebarDisplayMode}
        className={cn(
          'hidden shrink-0 flex-col overflow-hidden border-r border-[var(--app-border)] bg-[var(--app-surface)] transition-[width] sm:flex',
          focusMode ? 'sm:w-[56px]' : 'sm:w-[320px]',
        )}
      >
        {focusMode ? focusedSidebarContent : sidebarContent}
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
        {mobileCreationPage === 'task' && routeWorkspace ? (
          <BackgroundTaskForm
            presentation="page"
            workspaceName={routeWorkspace.workspaceName || routeWorkspace.path}
            request={backgroundTaskRequest}
            busy={false}
            error={backgroundTaskError}
            onRequestChange={setBackgroundTaskRequest}
            onSubmit={(request) => { void handleStartBackgroundRouterSession(request) }}
            onClose={closeBackgroundTaskModal}
          />
        ) : mobileCreationPage === 'worktree' && routeWorkspace ? (
          <div className="flex h-full flex-1 items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">Managed worktree</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">Worktree intent is chosen before routing; Swarm chooses the branch only after you send the new chat.</p>
              <Button className="mt-4" onClick={() => openRouteWorkspaceWorktree()}>Use a managed worktree</Button>
            </Card>
          </div>
        ) : routeSessionUnavailable ? (
          <div className="flex h-full flex-1 items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">Session not available</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                Desktop state route readiness marked this session as {routeReadinessStatus}. Refresh the workspace if this session was just created elsewhere.
              </p>
            </Card>
          </div>
        ) : routeSessionId ? (
          <div className="flex min-h-0 flex-1 flex-col">
            {videoStudioRoute ? (
              <div className="flex min-h-10 shrink-0 items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-xs">
                <span className="inline-flex min-w-0 items-center gap-2 font-semibold text-[var(--app-text)]">
                  <Film size={14} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                  <span className="truncate">Video Studio</span>
                </span>
                <Link
                  to="/$workspaceSlug"
                  params={{ workspaceSlug: routeWorkspaceSlug }}
                  className="shrink-0 rounded-md px-2 py-1 font-medium text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)]"
                >
                  Back to Workspace
                </Link>
              </div>
            ) : null}
          <DesktopV3ExistingConversationPane
            key={`existing:${routeSessionId}`}
            sessionId={routeSessionId}
            composerFocusSignal={composerFocusSignal}
            initialHydrateStatus={desktopInitialHydrate.status}
            renderedMessages={selectedDesktopV3Messages}
            messagesLoaded={selectedDesktopV3MessagesLoaded}
            loadedMessageCount={selectedDesktopV3LoadedMessageCount}
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
            onOpenChildSession={(sessionId, workspacePath) => {
              const workspaceSlug = workspacePath
                ? workspaceSlugByPath.get(workspacePath) ?? workspaceRouteSlugBase({ path: workspacePath, workspaceName: 'Workspace' })
                : routeWorkspaceSlug
              if (!workspaceSlug) return
              void selectAndHydrateDesktopV3Session(sessionId)
              const childSession = sessionById.get(sessionId)
              if (childSession?.metadata?.experience === 'video_studio' && childSession.metadata.launch_source === 'video_tool') {
                void navigate({ to: '/$workspaceSlug/video/$videoSessionId', params: { workspaceSlug, videoSessionId: sessionId } })
              } else {
                void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug, sessionId } })
              }
            }}
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
            agentSettingsOpenSignal={agentSettingsOpenSignal}
            agentSettingsInitialAgent={requestedAgentName}
            onOpenPlan={() => openPlanModalForSession(routeSessionId)}
            planSidebarBelowActions={planSidebarGitPanel}
          />
          </div>
        ) : routeWorkspaceSlug && !chatWorkspacePath && workspacesLoading ? (
          <div className="flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]" aria-busy="true" data-testid="desktop-v3-workspace-route-loading">
            <DesktopV3ChatHeader
              title="New chat"
              workspaceName="Loading workspace…"
              runStatus={{ kind: 'starting', label: 'Loading…', active: false }}
            />
            <div className="flex min-h-0 flex-1 items-center justify-center px-6">
              <div className="flex items-center gap-3 text-sm text-[var(--app-text-muted)]" role="status">
                <LoaderCircle size={18} className="animate-spin motion-reduce:animate-none" aria-hidden="true" />
                <span>Preparing this workspace…</span>
              </div>
            </div>
          </div>
        ) : routeWorkspaceSlug && !chatWorkspacePath ? (
          <div className="flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]">
            <DesktopV3ChatHeader title="New chat" workspaceName="Workspace unavailable" />
            <div className="flex h-full flex-1 items-center justify-center px-6">
              <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
                <div className="text-lg font-semibold">Workspace not found</div>
                <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                  We couldn’t resolve that workspace URL.
                </p>
              </Card>
            </div>
          </div>
        ) : topWorkspace?.path && activeWorkspaceAuthority ? (
          <DesktopV3NewSessionPane
            key={`new:${topWorkspace.path}:${newSessionEpoch}`}
            workspace={topWorkspace}
            workspaceAuthority={activeWorkspaceAuthority}
            onRoutedSessionResolved={handleRoutedSessionResolved}
            composerFocusSignal={composerFocusSignal}
            initialPrompt={newSessionIntent?.workspacePath === topWorkspace.path ? newSessionIntent.prompt : undefined}
            initialWorktreeRequested={newSessionIntent?.workspacePath === topWorkspace.path ? newSessionIntent.worktreeRequested : requestedNewWorktree}
            initialPlanModeRequested={newSessionIntent?.workspacePath === topWorkspace.path ? newSessionIntent.planModeRequested : requestedNewPlan}
            agentSettingsOpenSignal={agentSettingsOpenSignal}
            agentSettingsInitialAgent={requestedAgentName}
            mobileSessionQuickMenu={mobileSessionQuickMenu}
            onSlashCommand={handleSlashCommand}
            workspaces={mergedSidebarWorkspaceEntries}
            onSelectWorkspace={mergedSidebarWorkspaceEntries.length > 1 ? handleSelectWorkspaceFromPicker : undefined}
            onSetWorkspaceIcon={setWorkspaceIcon}
            onOpenActionSettings={() => handleOpenSettingsTab('actions')}
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

      {needsReviewCleanupOpen ? <ReviewWorktreesModal workspacePath={topWorkspacePath || undefined} onClose={() => setNeedsReviewCleanupOpen(false)} repairFixAvailable={reviewFixAvailable} onAskSwarmFix={handleAskSwarmToFixReviewIntegration} /> : null}

      <DesktopQuickSettingsModal
        tab={quickSettingsTab}
        activeWorkspacePath={topWorkspacePath || null}
        onClose={() => setQuickSettingsTab(null)}
        onOpenFullSettings={handleOpenSettingsTab}
      />

      <DesktopQuickActionsModal
        open={quickActionsOpen}
        actions={quickActions}
        onClose={() => setQuickActionsOpen(false)}
        onOpenShortcutsSettings={() => handleOpenSettingsTab('shortcuts')}
      />

      <DesktopWorkspacePicker
        open={workspacePickerOpen}
        workspaces={mergedSidebarWorkspaceEntries}
        currentWorkspacePath={topWorkspacePath}
        onClose={() => setWorkspacePickerOpen(false)}
        onSelect={handleSelectWorkspaceFromPicker}
      />

      <SearchChatsModal
        open={searchModalOpen}
        onOpenChange={setSearchModalOpen}
        onOpenSession={handleOpenSearchResult}
      />

      <DesktopCodexUsageModal
        open={codexUsageOpen}
        onOpenChange={setCodexUsageOpen}
        onOpenAuthSettings={() => {
          setCodexUsageOpen(false)
          handleOpenSettingsTab('auth')
        }}
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
        error={planModalError}
        onOpenChange={(open) => {
          if (!open) setPlanModal(null)
        }}
        onCopy={handleCopyPlanText}
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
          onOpenManagedSession={(sessionId) => {
            const workspaceSlug = workspaceSlugByPath.get(todoModal.workspacePath) ?? workspaceRouteSlugBase({ path: todoModal.workspacePath, workspaceName: todoModal.workspaceName })
            closeTodoModal()
            void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug, sessionId } })
          }}
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

      {workspaceActionPresentation ? (
        <div className="absolute bottom-[calc(var(--app-safe-area-bottom)+1rem)] left-4 right-4 z-[65] max-h-[min(70vh,36rem)] overflow-y-auto sm:left-auto sm:right-6 sm:w-[28rem]" data-testid="workspace-action-run">
          <DesktopWorkspaceActionPanel
            workspacePath={workspaceActionPresentation.workspacePath}
            sessionId={workspaceActionPresentation.sessionId}
            action={workspaceActionPresentation.action}
            autoCloseOnSuccess={false}
            initialRun={workspaceActionPresentation.initialRun}
            contextNotice={workspaceActionPresentation.mode === 'post-commit' ? 'The AI commit succeeded. This Action is now running.' : ''}
            onRunChange={(run) => {
              if (run.status === 'succeeded') setDesktopToast({ message: `${run.actionName} completed successfully.`, tone: 'success' })
              else if (run.status !== 'running') setDesktopToast({ message: `${run.actionName} failed: ${run.error || run.status.replace('_', ' ')}`, tone: 'error' })
            }}
            onClose={() => setWorkspaceActionPresentation(null)}
          />
        </div>
      ) : null}

      {desktopToast ? (
        <div className="pointer-events-none absolute left-4 right-4 top-[calc(var(--app-safe-area-top)+1rem)] z-[70] sm:left-auto sm:right-6 sm:top-6 sm:max-w-md" role="status" aria-live="polite">
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

      {backgroundTaskOpen && routeWorkspace && mobileCreationPage !== 'task' ? (
        <BackgroundTaskForm
          presentation="dialog"
          workspaceName={routeWorkspace.workspaceName || routeWorkspace.path}
          request={backgroundTaskRequest}
          busy={false}
          error={backgroundTaskError}
          onRequestChange={setBackgroundTaskRequest}
          onSubmit={() => { void handleStartBackgroundRouterSession() }}
          onClose={closeBackgroundTaskModal}
        />
      ) : null}
      {gitCommitModal ? <Dialog>
        <DialogBackdrop onClick={() => { if (!gitCommitBusy) setGitCommitModal(null) }} />
        <DialogPanel className="w-[min(560px,100%)] gap-4">
          <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); void handleGitCommit() }}>
            <div><div className="text-sm font-semibold text-[var(--app-text)]">Commit all changes</div><div className="mt-1 text-xs text-[var(--app-text-subtle)]">This explicitly stages and commits all {gitCommitModal.files.length} shown files, including untracked files.</div></div>
            <div className="max-h-48 overflow-y-auto border border-[var(--app-border)] font-mono text-xs">{gitCommitModal.files.map((file) => <div key={`${file.kind}:${file.path}`} className="flex gap-2 border-b border-[var(--app-border)] px-2 py-1 last:border-0"><span className="text-[var(--app-text-subtle)]">{gitFileStatusLabel(file)}</span><span className="truncate">{file.path}</span></div>)}</div>
            <label className="grid gap-1 text-xs text-[var(--app-text-muted)]"><span>Commit message</span><input ref={gitCommitMessageInputRef} autoFocus value={gitCommitMessage} disabled={gitCommitBusy} onChange={(event) => setGitCommitMessage(event.target.value)} className="h-10 border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 text-[var(--app-text)] outline-none disabled:opacity-60" /></label>
            {gitCommitModal.worktree && gitCommitModal.canIntegrate ? <div className="grid gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 text-xs"><label className="flex items-start gap-2 text-[var(--app-text)]"><input type="checkbox" className="mt-0.5" checked={gitCommitIntegrate} disabled={gitCommitBusy} onChange={(event) => { const checked = event.target.checked; setGitCommitIntegrate(checked); if (!checked) setGitCommitArchive(false) }} /><span><strong>Integrate into {gitCommitModal.targetBranch || 'target branch'}</strong><span className="mt-0.5 block text-[var(--app-text-subtle)]">After the commit succeeds, safely apply this worktree’s missing commit stack to the target checkout.</span></span></label><label className={cn('flex items-start gap-2', gitCommitIntegrate ? 'text-[var(--app-text)]' : 'text-[var(--app-text-subtle)]')}><input type="checkbox" className="mt-0.5" checked={gitCommitArchive} disabled={gitCommitBusy || !gitCommitIntegrate} onChange={(event) => setGitCommitArchive(event.target.checked)} /><span><strong>Archive session after integration</strong><span className="mt-0.5 block text-[var(--app-text-subtle)]">Only archives after the backend verifies integration succeeded.</span></span></label></div> : null}
            {!gitCommitModal.worktree && gitCommitModal.sessionId ? <label className="flex items-start gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 text-xs text-[var(--app-text)]"><input type="checkbox" className="mt-0.5" checked={gitCommitArchive} disabled={gitCommitBusy} onChange={(event) => setGitCommitArchive(event.target.checked)} /><span><strong>Archive session after commit</strong><span className="mt-0.5 block text-[var(--app-text-subtle)]">Archives this chat only after the commit succeeds.</span></span></label> : null}
            {gitCommitError ? <div className="text-xs text-[var(--app-warning)]" role="alert">{gitCommitError}</div> : null}
            <div className="flex justify-end gap-2"><Button variant="ghost" disabled={gitCommitBusy} onClick={() => setGitCommitModal(null)}>Cancel</Button><Button type="submit" disabled={gitCommitBusy || !gitCommitMessage.trim()}>{gitCommitBusy ? gitCommitIntegrate ? 'Committing and integrating…' : 'Committing…' : gitCommitIntegrate ? 'Commit and integrate' : 'Commit all changes'}</Button></div>
          </form>
        </DialogPanel>
      </Dialog> : null}
      {gitIntegrateModal && gitIntegrateModal.presentation !== 'sidebar-popout' ? <Dialog>
        <DialogBackdrop onClick={() => { if (!gitIntegrateBusy) setGitIntegrateModal(null) }} />
        <DialogPanel className="w-[min(520px,100%)] gap-4">
          <form className="grid gap-4" onSubmit={(event) => { event.preventDefault(); void handleGitIntegrate() }}>
            <div><div className="text-sm font-semibold text-[var(--app-text)]">{gitIntegrateModal.integrationComplete ? `Integration into ${gitIntegrateModal.targetBranch} complete` : `Integrate ${gitIntegrateModal.worktreeBranch} into ${gitIntegrateModal.targetBranch}`}</div><div className="mt-1 text-xs text-[var(--app-text-subtle)]">{gitIntegrateModal.integrationComplete ? 'The commit stack is integrated. Retry only the remaining archive step.' : 'Swarm preflights the complete missing commit stack and leaves the target unchanged if integration conflicts.'}</div></div>
            <label className="flex items-start gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 text-xs text-[var(--app-text)]"><input type="checkbox" className="mt-0.5" checked={gitIntegrateArchive} disabled={gitIntegrateBusy} onChange={(event) => setGitIntegrateArchive(event.target.checked)} /><span><strong>Archive session after integration</strong><span className="mt-0.5 block text-[var(--app-text-subtle)]">Only archives after the backend verifies the worktree is integrated.</span></span></label>
            {gitIntegrateError ? <div className="rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-3 text-xs text-[var(--app-danger)]">{gitIntegrateError}</div> : null}
            <div className="flex justify-end gap-2"><Button variant="ghost" disabled={gitIntegrateBusy} onClick={() => setGitIntegrateModal(null)}>Cancel</Button><Button type="submit" disabled={gitIntegrateBusy}>{gitIntegrateBusy ? gitIntegrateModal.integrationComplete ? 'Archiving…' : 'Integrating…' : gitIntegrateModal.integrationComplete ? 'Archive session' : gitIntegrateError ? 'Try integration again' : 'Integrate commits'}</Button></div>
          </form>
        </DialogPanel>
      </Dialog> : null}
      <GitDetailsOverlay
        state={gitPanel}
        snapshot={gitPanel ? topWorkspaceGitSnapshot : null}
        loading={Boolean(gitPanel && topWorkspaceGitStatusQuery.isFetching)}
        error={gitPanel && topWorkspaceGitStatusQuery.error instanceof Error ? topWorkspaceGitStatusQuery.error.message : null}
        onRefresh={() => { if (gitPanel) void queryClient.invalidateQueries({ queryKey: gitStatusQueryKey(gitPanel.workspacePath) }) }}
        onCommit={(files) => {
          if (!gitPanel || files.length === 0) return
          openGitCommitReview({ workspacePath: gitPanel.workspacePath, sessionId: '', files })
          setGitPanel(null)
        }}
        aiCommitControl={gitPanel && topWorkspaceGitSnapshot?.files.length ? <AICommitButton phase={gitAICommitPhase} disabled={gitCommitBusy} onGenerate={() => { void handleAICommit({ workspacePath: gitPanel.workspacePath, sessionId: '' }); setGitPanel(null) }} /> : null}
        onClose={closeGitPanel}
      />
      {pwaDebugEnabled ? <PwaLayoutDebugOverlay /> : null}

    </div>
  )
}
