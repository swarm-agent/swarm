import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, JSX, PointerEvent as ReactPointerEvent, ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { Bell, Bot, CheckCircle2, ChevronDown, ChevronLeft, ChevronRight, Download, Eye, EyeOff, GitBranch, GitCommitHorizontal, Home, ListChecks, LoaderCircle, Menu, Plus, Settings, X, XCircle } from 'lucide-react'
import { debugLog } from '../../../lib/debug-log'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { DesktopNotificationsModal } from '../notifications/components/desktop-notifications-modal'
import { cn } from '../../../lib/cn'
import { useDesktopStore } from '../state/use-desktop-store'
import {
  loadDesktopChatRouteForSession,
} from '../chat/services/chat-routing'
import { useWorkspaceLauncher } from '../../workspaces/launcher/state/use-workspace-launcher'
import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'
import { prefetchSessionRuntimeData, uiSettingsQueryKey, workspaceOverviewQueryOptions } from '../../queries/query-options'
import type { DesktopSessionRecord } from '../types/realtime'
import type { SettingsTabID } from '../settings/types/settings-tabs'
import { DesktopChatPanel } from '../chat/components/desktop-chat-panel'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import { syncWorkspaceOverviewSession } from '../../workspaces/launcher/services/workspace-overview-cache'
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
  fetchWorkspaceTodos,
  reorderWorkspaceTodos,
  setWorkspaceTodoInProgress,
  updateWorkspaceTodo,
} from '../../workspaces/todos/types'
import { getSwarmSettings } from '../settings/swarm/queries/get-swarm-settings'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveLocalContainerUpdateWarningDismissal } from '../settings/swarm/mutations/save-local-container-update-warning-dismissal'
import { localContainerUpdateWarningDismissed, normalizeSwarmSettings, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { fetchSwarmTargets, selectSwarmTarget, type SwarmTarget } from '../swarm/api/swarm-targets'
import { fetchRemoteDeploySessions, type RemoteDeploySession } from '../swarm/api/deploy-container'
import { fetchSession } from '../chat/queries/chat-queries'
import { fetchDesktopUpdateJob, fetchDesktopUpdateStatus, fetchLocalContainerUpdatePlan, startDesktopUpdate, type DesktopUpdateJob, type LocalContainerUpdatePlan } from '../update/api'
import {
  sessionBackgroundInfo,
  sessionChildDescriptor,
  sessionParentSessionID,
  type SidebarSessionNodeKind,
} from './sidebar-session-lineage'

const DESKTOP_SIDEBAR_LAYOUT_STORAGE_KEY = 'swarm.web.desktop.sidebar.layout'
const MIN_WORKSPACE_SECTION_HEIGHT_PX = 120
const SIDEBAR_ACTIVITY_GRACE_MS = 15_000
const UPDATE_STATUS_REFETCH_INTERVAL_MS = 5 * 60_000
const SIDEBAR_ACTION_RAIL_WIDTH_CLASS = 'w-[52px]'
const SIDEBAR_ACTION_ROW_CLASS = `grid min-w-0 grid-cols-[minmax(0,1fr)_52px] items-center gap-2.5`
const SIDEBAR_ACTION_RAIL_CLASS = `grid ${SIDEBAR_ACTION_RAIL_WIDTH_CLASS} shrink-0 grid-cols-[24px_24px] justify-end gap-1`
const SIDEBAR_ACTION_BOX_CLASS = 'grid h-6 min-h-6 w-6 min-w-6 shrink-0 place-items-center border-0 bg-transparent p-0 font-inherit'
const SIDEBAR_ACTION_BUTTON_CLASS = `${SIDEBAR_ACTION_BOX_CLASS} text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]`
const UPDATE_PROGRESS_STEP_TITLES = [
  'Start update helper',
  'Stop Swarm backend',
  'Rebuild/apply Swarm',
  'Restart backend',
  'Update container images',
] as const

function SidebarActionRail({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn(SIDEBAR_ACTION_RAIL_CLASS, className)}>{children}</div>
}

function SidebarActionRailSpacer() {
  return <span aria-hidden="true" className={SIDEBAR_ACTION_BOX_CLASS} />
}

interface SidebarWorkspaceLayout {
  collapsed: boolean
  hidden: boolean
  ratio: number
}

interface SidebarResizeState {
  topPath: string
  bottomPath: string
  startY: number
  topRatio: number
  bottomRatio: number
  totalVisibleRatio: number
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

interface SwarmTargetMenuState {
  open: boolean
}

interface LocalContainerUpdateConfirmState {
  plan: LocalContainerUpdatePlan
  remoteSessions: RemoteDeploySession[]
  pendingDismiss: boolean
}

function DesktopNotificationsOverlay({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const connectionState = useDesktopStore((state) => state.connectionState)
  const notificationCenter = useDesktopStore((state) => state.notificationCenter)
  const updateNotificationRecord = useDesktopStore((state) => state.updateNotificationRecord)
  return (
    <DesktopNotificationsModal
      open={open}
      onOpenChange={onOpenChange}
      notifications={notificationCenter.items}
      summary={notificationCenter.summary}
      loading={notificationCenter.loading}
      connectionState={connectionState}
      onMarkRead={async (record) => {
        await updateNotificationRecord(record.id, { read: true })
      }}
      onAcknowledge={async (record) => {
        await updateNotificationRecord(record.id, { acked: true, status: 'resolved' })
      }}
      onMute={async (record) => {
        await updateNotificationRecord(record.id, { muted: true })
      }}
      onClearAll={async () => {
        await useDesktopStore.getState().clearNotifications()
      }}
    />
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

function buildTemporaryWorkspaceEntry(path: string, workspaceName: string): WorkspaceEntry {
  return {
    path,
    workspaceName,
    themeId: '',
    directories: [path],
    isGitRepo: false,
    replicationLinks: [],
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

function connectionTone(connectionState: 'idle' | 'connecting' | 'open' | 'closed' | 'error'): 'muted' | 'success' | 'warning' | 'danger' {
  switch (connectionState) {
    case 'open':
      return 'success'
    case 'connecting':
      return 'warning'
    case 'error':
      return 'danger'
    default:
      return 'muted'
  }
}

function workspaceSectionHeightStyle(ratio: number, totalVisibleRatio: number, collapsed: boolean): CSSProperties {
  if (collapsed) {
    return {
      flexShrink: 0,
      flexGrow: 0,
    }
  }
  const safeTotal = totalVisibleRatio > 0 ? totalVisibleRatio : 1
  const safeRatio = normalizeRatio(ratio)
  return {
    flexGrow: safeRatio,
    flexBasis: `${(safeRatio / safeTotal) * 100}%`,
    minHeight: MIN_WORKSPACE_SECTION_HEIGHT_PX,
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

function updateProgressStepIndex(job: DesktopUpdateJob | null): number {
  const status = job?.status?.trim().toLowerCase() ?? ''
  const message = updateJobMessage(job).toLowerCase()
  if (status === 'completed') {
    return UPDATE_PROGRESS_STEP_TITLES.length
  }
  if (message.includes('container image') || message.includes('container images') || message.includes('local and remote')) {
    return 4
  }
  if (message.includes('restart') || message.includes('reconnect')) {
    return 3
  }
  if (message.includes('rebuild') || message.includes('build') || message.includes('applying') || message.includes('installing') || message.includes('staging') || message.includes('fingerprint') || message.includes('syncing')) {
    return 2
  }
  if (message.includes('shut down') || message.includes('stop')) {
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

function connectionDotClass(connectionState: 'idle' | 'connecting' | 'open' | 'closed' | 'error'): string {
  switch (connectionTone(connectionState)) {
    case 'success':
      return 'bg-emerald-500'
    case 'warning':
      return 'bg-amber-400'
    case 'danger':
      return 'bg-rose-500'
    default:
      return 'bg-[var(--app-border-strong)]'
  }
}

function swarmKindDotClass(kind: SwarmTarget['kind'] | undefined, online = true): string {
  if (!online) {
    return 'bg-[var(--app-warning)]'
  }
  if (kind === 'remote') {
    return 'bg-[var(--app-info)]'
  }
  return 'bg-[var(--app-success)]'
}

function swarmKindLabel(target: SwarmTarget): string {
  if (target.kind === 'self') {
    return 'Master'
  }
  return target.kind === 'remote' ? 'remote' : 'local'
}

function sessionPendingPermissionCount(session: DesktopSessionRecord): number {
  return countApprovalRequiredPermissions(session.pendingPermissions, session.mode)
}

function sessionHasPendingPermission(session: DesktopSessionRecord): boolean {
  return sessionPendingPermissionCount(session) > 0
}

function sessionStatusTone(session: DesktopSessionRecord): 'blocked' | 'running' | 'error' | 'idle' {
  if (sessionHasPendingPermission(session)) {
    return 'blocked'
  }

  switch (session.live.status) {
    case 'blocked':
      return session.live.runId || session.live.startedAt !== null || session.live.awaitingAck ? 'running' : 'idle'
    case 'starting':
    case 'running':
      return 'running'
    case 'error':
      return 'error'
    default:
      return 'idle'
  }
}

function sessionMeta(session: DesktopSessionRecord): string | null {
  if (sessionHasPendingPermission(session)) {
    return 'Blocked • needs approval'
  }

  switch (session.live.status) {
    case 'blocked':
      return session.live.toolName ? `running ${session.live.toolName}` : 'running'
    case 'error':
      return 'failed'
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

function metadataString(metadata: Record<string, unknown> | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function sessionOriginLabel(session: DesktopSessionRecord, fallbackSwarmName: string): string {
  const route = loadDesktopChatRouteForSession(session.id)
  const routeLabel = route?.label?.trim() ?? ''
  if (routeLabel) {
    return routeLabel
  }
  const targetLabel = metadataString(session.metadata, 'swarm_target_name')
    || metadataString(session.metadata, 'target_display_name')
  if (targetLabel) {
    return targetLabel
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

function workspaceGitBarTone(workspace: WorkspaceEntry): string {
  if (!workspace.isGitRepo || !workspace.gitHasGit) {
    return 'text-[var(--app-text-muted)]'
  }
  if (Math.max(0, Number(workspace.gitConflictCount ?? 0)) > 0) {
    return 'text-[var(--app-danger)]'
  }
  if (Math.max(0, Number(workspace.gitDirtyCount ?? 0)) > 0) {
    return 'text-[var(--app-warning)]'
  }
  return 'text-[var(--app-text-subtle)]'
}

function workspaceWorktreeTitle(enabled: boolean, busy: boolean): string {
  if (busy) {
    return 'Updating worktree setting…'
  }
  return enabled
    ? 'Worktrees on for new sessions. Click to turn them off.'
    : 'Worktrees off for new sessions. Click to turn them on.'
}

function renderWorkspaceGitBar(args: {
  workspace: WorkspaceEntry
  worktreeBusy: boolean
  onToggle: () => void
}): JSX.Element {
  const { workspace, worktreeBusy, onToggle } = args
  const enabled = workspace.worktreeEnabled
  const title = workspaceWorktreeTitle(enabled, worktreeBusy)
  const tone = workspaceGitBarTone(workspace)
  const branch = workspace.gitBranch?.trim() || 'git'
  const ahead = Math.max(0, Number(workspace.gitAheadCount ?? 0))
  const behind = Math.max(0, Number(workspace.gitBehindCount ?? 0))
  const syncLabel = `↑${ahead} ↓${behind}`
  const modified = Math.max(0, Number(workspace.gitModifiedCount ?? 0))
  const untracked = Math.max(0, Number(workspace.gitUntrackedCount ?? 0))
  const dirtyDetailParts: string[] = []
  if (modified > 0) {
    dirtyDetailParts.push(`${modified}M`)
  }
  if (untracked > 0) {
    dirtyDetailParts.push(`${untracked}U`)
  }
  const dirtyLabel = dirtyDetailParts.join(' ')

  return (
    <div className={cn(SIDEBAR_ACTION_ROW_CLASS, 'pb-2 pl-6 pr-1 pt-0.5 text-[11px]')}>
      <div className="flex min-w-0 items-center gap-2 overflow-hidden">
        <span className={cn('truncate font-semibold', tone)}>{branch}</span>
        <span className="shrink-0 text-[var(--app-text-muted)]">/</span>
        <span className="shrink-0 text-[var(--app-text-muted)]">{syncLabel}</span>
        {dirtyLabel ? <span className="shrink-0 text-[var(--app-text-muted)]">/</span> : null}
        {dirtyLabel ? <span className={cn('shrink-0 text-[10px]', tone)}>{dirtyLabel}</span> : null}
      </div>
      <SidebarActionRail>
        <SidebarActionRailSpacer />
        <button
          type="button"
          className={cn(
            SIDEBAR_ACTION_BUTTON_CLASS,
            'text-[10px]',
            enabled ? 'text-[var(--app-selection)]' : 'text-[var(--app-text-muted)] opacity-45 hover:opacity-85',
            worktreeBusy && 'cursor-progress opacity-70',
          )}
          onClick={onToggle}
          aria-busy={worktreeBusy}
          aria-disabled={worktreeBusy}
          aria-pressed={enabled}
          title={title}
        >
          wt
        </button>
      </SidebarActionRail>
    </div>
  )
}

function sidebarSummaryLabel(session: DesktopSessionRecord): string {
  const summary = session.live.summary?.trim() ?? ''
  if (!summary) {
    return ''
  }

  const normalized = summary.toLowerCase()
  if (
    summary.includes('\n')
    || normalized === 'starting...'
    || normalized === 'starting…'
    || normalized.startsWith('tool.')
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

function sessionIsActive(session: DesktopSessionRecord): boolean {
  return sessionHasPendingPermission(session) || session.live.awaitingAck || ['starting', 'running', 'blocked'].includes(session.live.status)
}

function sessionActivityAnchor(session: DesktopSessionRecord): number {
  return session.live.startedAt
    ?? (session.lifecycle?.startedAt && session.lifecycle.startedAt > 0 ? session.lifecycle.startedAt : null)
    ?? session.live.lastEventAt
    ?? session.updatedAt
    ?? 0
}

function sessionDurableActivityAt(session: DesktopSessionRecord): number {
  if (session.updatedAt > 0) {
    return session.updatedAt
  }
  if (session.createdAt > 0) {
    return session.createdAt
  }
  return 0
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
    return sessionActivityAnchor(session)
  }
  return sessionDurableActivityAt(session)
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

function compareSidebarSessions(left: DesktopSessionRecord, right: DesktopSessionRecord, now: number): number {
  const leftPinned = sessionShouldPinInSidebar(left, now)
  const rightPinned = sessionShouldPinInSidebar(right, now)
  if (leftPinned !== rightPinned) {
    return leftPinned ? -1 : 1
  }

  if (leftPinned && rightPinned) {
    const anchorDelta = sessionSidebarSortAnchor(left) - sessionSidebarSortAnchor(right)
    if (anchorDelta !== 0) {
      return anchorDelta
    }
  }

  const updatedDelta = right.updatedAt - left.updatedAt
  if (updatedDelta !== 0) {
    return updatedDelta
  }

  const createdDelta = right.createdAt - left.createdAt
  if (createdDelta !== 0) {
    return createdDelta
  }

  return left.id.localeCompare(right.id)
}

function sessionStatusDetail(session: DesktopSessionRecord, now: number): string {
  return formatRelativeTime(sessionSidebarDisplayTimestamp(session), now)
}

function sessionTimerLabel(session: DesktopSessionRecord, now: number): string {
  const activeSince = session.live.startedAt ?? session.live.lastEventAt ?? session.updatedAt
  return typeof activeSince === 'number' && activeSince > 0
    ? formatDurationCompact(now - activeSince)
    : 'live'
}

function sessionActivityLabel(session: DesktopSessionRecord): string {
  if (sessionHasPendingPermission(session)) {
    return 'Needs approval'
  }

  switch (session.live.status) {
    case 'blocked':
      return session.live.toolName?.trim()
        || sidebarSummaryLabel(session)
        || 'running'
    case 'error':
      return 'failed'
    case 'starting':
      return 'Starting'
    case 'running':
      return session.live.toolName?.trim()
        || sidebarSummaryLabel(session)
        || 'running'
    default:
      return ''
  }
}

function sessionNeedsRefresh(session: DesktopSessionRecord | null): boolean {
  if (!session) {
    return true
  }

  if (session.lifecycle && !session.lifecycle.active) {
    return false
  }

  if (session.live.summary === 'Reconnecting…') {
    return true
  }

  if (session.lifecycle?.active) {
    return false
  }

  return (session.live.status !== 'idle' && session.live.status !== 'error')
    || session.live.awaitingAck
    || session.live.runId !== null
    || session.live.startedAt !== null
}

interface SidebarSessionNode {
  session: DesktopSessionRecord
  children: SidebarSessionNode[]
  depth: number
  kind: SidebarSessionNodeKind
  label: string | null
}

function buildSidebarSessionTree(sessions: DesktopSessionRecord[], now: number): SidebarSessionNode[] {
  const sortedSessions = sessions.length > 1
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
  sortNodes(uniqueRoots)
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
  depth?: number
  childLabel?: string | null
  childKind?: SidebarSessionNode['kind']
  agentSummary: SessionAgentSummary
  agentsExpanded: boolean
  onSelect: (sessionId: string) => void
  onPrefetch: (sessionId: string) => void
  onToggleAgents: (sessionId: string) => void
}

function SessionRow({ active, now, session: initialSession, fallbackSwarmName, depth = 0, childLabel = null, childKind = 'root', agentSummary, agentsExpanded, onSelect, onPrefetch, onToggleAgents }: SessionRowProps) {
  const session = useDesktopStore((state) => state.sessions[initialSession.id] ?? initialSession)
  const activeSession = sessionIsActive(session)
  const originLabel = sessionOriginLabel(session, fallbackSwarmName)
  const backgroundInfo = sessionBackgroundInfo(session, originLabel)
  const timerLabel = activeSession ? sessionTimerLabel(session, now) : ''
  const bottomLeftLabel = backgroundInfo?.targetLabel || originLabel
  const bottomRightLabel = backgroundInfo?.active
    ? timerLabel
    : activeSession
      ? sessionActivityLabel(session) || sessionMeta(session) || ''
      : sessionStatusDetail(session, now) || sessionMeta(session) || ''
  const commitSummary = sessionCommitSummary(session)
  const committedFileSummary = sessionCommittedFileSummary(session)
  const committedDeltaSummary = sessionCommittedDeltaSummary(session)
  const commitMetaLabel = [commitSummary, committedFileSummary, committedDeltaSummary].filter(Boolean).join(' · ')
  const tooltip = sessionStatusTooltip(session)
  const indentStyle = depth > 0 ? { paddingLeft: `${Math.min(depth, 5) * 16}px` } : undefined
  const hasAgentChildren = agentSummary.total > 0
  const agentDescriptor = agentSummaryDescriptor(agentSummary)

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={() => onSelect(session.id)}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          onSelect(session.id)
        }
      }}
      onMouseEnter={() => onPrefetch(session.id)}
      onFocus={() => onPrefetch(session.id)}
      className={cn(
        'grid w-full min-w-0 gap-1 rounded-lg px-3 py-2 text-left transition-colors outline-none',
        active
          ? 'bg-[var(--app-surface-subtle)]'
          : 'bg-transparent hover:bg-[var(--app-surface-subtle)]',
        depth > 0 && childKind === 'subagent' ? 'border-l border-sky-400/25' : null,
        hasAgentChildren && agentsExpanded ? 'ring-1 ring-sky-400/20' : null,
      )}
      style={indentStyle}
    >
      <div className="flex items-center justify-between gap-3 min-w-0 w-full">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          {depth > 0 ? <span className="text-[var(--app-text-subtle)]">↳</span> : null}
          <span className="truncate flex-1 min-w-0 text-sm font-medium text-[var(--app-text)]">{session.title || 'New conversation'}</span>
        </div>
        <span
          className={cn(
            'h-2 w-2 shrink-0 rounded-full',
            sessionStatusTone(session) === 'running'
              ? 'bg-emerald-500'
              : sessionStatusTone(session) === 'blocked'
                ? 'bg-amber-400'
                : sessionStatusTone(session) === 'error'
                  ? 'bg-rose-500'
                  : 'bg-[var(--app-border-strong)]',
          )}
        />
      </div>
      <div className="flex items-center justify-between gap-3 text-xs text-[var(--app-text-muted)] min-w-0 w-full">
        <div className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden">
          <span className={cn(backgroundInfo?.active ? 'max-w-[8rem]' : null, 'truncate')}>{bottomLeftLabel}</span>
          {backgroundInfo ? (
            <span className="inline-flex h-4 shrink-0 items-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-1.5 text-[10px] font-medium leading-none text-[var(--app-text-subtle)]">
              {backgroundInfo.badge}
            </span>
          ) : childLabel ? (
            <span className={cn(
              'shrink-0 truncate text-[11px]',
              childKind === 'subagent' ? 'text-sky-300/90' : 'text-[var(--app-text-subtle)]',
            )}>
              {childLabel}
            </span>
          ) : null}
        </div>
        <span className={cn('shrink-0 text-[var(--app-text-subtle)]', backgroundInfo?.active ? 'font-mono tabular-nums' : null)}>{bottomRightLabel}</span>
      </div>
      {session.worktreeEnabled || commitMetaLabel || hasAgentChildren ? (
        <div className="flex items-center justify-between gap-3 text-[10px] leading-4 text-[var(--app-text-subtle)] min-w-0 w-full">
          {commitMetaLabel ? (
            <span
              className="inline-flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden font-mono tabular-nums"
              title={tooltip}
            >
              <GitCommitHorizontal size={11} className="shrink-0 opacity-70" />
              <span className="truncate">{commitMetaLabel}</span>
            </span>
          ) : <span className="min-w-0 flex-1" />}
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {hasAgentChildren ? (
              <button
                type="button"
                className={cn(
                  'inline-flex h-5 shrink-0 items-center gap-1 rounded-md border px-1.5 transition-colors',
                  agentsExpanded
                    ? 'border-[var(--app-border-strong)] text-[var(--app-text)]'
                    : 'border-transparent text-[var(--app-text-subtle)] hover:text-[var(--app-text)]',
                )}
                onClick={(event) => {
                  event.stopPropagation()
                  onToggleAgents(session.id)
                }}
                aria-label={`${agentSummary.running} running of ${agentSummary.total} subagents`}
                aria-pressed={agentsExpanded}
                title={`${agentSummary.total} subagent${agentSummary.total === 1 ? '' : 's'} · ${agentSummary.running} running${agentsExpanded ? ' · click to hide subagent sessions' : ' · click to show subagent sessions'}`}
              >
                <Bot size={11} className={cn('shrink-0', agentSummary.running > 0 ? 'animate-pulse' : null)} />
                <span className={cn('font-mono tabular-nums text-[10px] leading-none', agentSummary.running > 0 ? 'text-emerald-300' : null)}>{agentDescriptor.primary}</span>
                {agentDescriptor.secondary ? (
                  <span className={cn(
                    'font-mono tabular-nums text-[10px] leading-none',
                    agentSummary.running > 0 ? 'text-[var(--app-text-subtle)]' : 'text-[var(--app-text)]',
                  )}>{agentDescriptor.secondary}</span>
                ) : null}
              </button>
            ) : null}
            {session.worktreeEnabled ? (
              <span
                className="inline-flex shrink-0 items-center justify-center text-[var(--app-text-subtle)] opacity-80"
                title={tooltip || 'Worktree enabled'}
              >
                <GitBranch size={12} />
              </span>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  )
}

export function DesktopAppPage() {
  debugLog('desktop-app-page', 'render')
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const workspaceSessionMatch = matchRoute({ to: '/$workspaceSlug/$sessionId', fuzzy: false })
  const workspaceMatch = matchRoute({ to: '/$workspaceSlug', fuzzy: false })
  const routeWorkspaceSlug = (workspaceSessionMatch ? workspaceSessionMatch.workspaceSlug : workspaceMatch ? workspaceMatch.workspaceSlug : '').trim()
  const routeSessionId = (workspaceSessionMatch ? workspaceSessionMatch.sessionId : '').trim()
  const { workspaces, selectingPath, savingPath, saveWorkspace, setWorktreeEnabled, loading: workspacesLoading } = useWorkspaceLauncher()
  const connectionState = useDesktopStore((state) => state.connectionState)
  const liveSessions = useDesktopStore((state) => state.sessions)
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)
  const activeWorkspacePath = useDesktopStore((state) => state.activeWorkspacePath)
  const refreshNotifications = useDesktopStore((state) => state.refreshNotifications)
  const notificationCenter = useDesktopStore((state) => state.notificationCenter)
  const setActiveSession = useDesktopStore((state) => state.setActiveSession)
  const setActiveWorkspacePath = useDesktopStore((state) => state.setActiveWorkspacePath)
  const upsertSession = useDesktopStore((state) => state.upsertSession)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [expandedAgentSessions, setExpandedAgentSessions] = useState<Record<string, boolean>>({})
  const [notificationsOpen, setNotificationsOpen] = useState(false)
  const [todoModal, setTodoModal] = useState<TodoModalState | null>(null)
  const [todoItems, setTodoItems] = useState<Record<string, WorkspaceTodoItem[]>>({})
  const [todoSummaries, setTodoSummaries] = useState<Record<string, WorkspaceTodoSummary>>({})
  const [swarmMenu, setSwarmMenu] = useState<SwarmTargetMenuState>({ open: false })
  const [workspaceMenuOpen, setWorkspaceMenuOpen] = useState(false)
  const [updateRunning, setUpdateRunning] = useState(false)
  const [updateError, setUpdateError] = useState<string | null>(null)
  const [updateProgress, setUpdateProgress] = useState<DesktopUpdateProgressState>({ open: false, job: null, startedAt: null })
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [localContainerUpdateConfirm, setLocalContainerUpdateConfirm] = useState<LocalContainerUpdateConfirmState | null>(null)
  const [todoSavingWorkspacePath, setTodoSavingWorkspacePath] = useState<string | null>(null)
  const [workspaceLayout, setWorkspaceLayout] = useState<Record<string, SidebarWorkspaceLayout>>(() => loadSidebarWorkspaceLayout())
  const [routeSessionPending, setRouteSessionPending] = useState(false)
  const [sidebarNow, setSidebarNow] = useState(() => Date.now())
  const sidebarBodyRef = useRef<HTMLDivElement | null>(null)
  const resizeStateRef = useRef<SidebarResizeState | null>(null)
  const workspaceByPath = useMemo<Map<string, WorkspaceEntry>>(
    () => new Map(workspaces.map((workspace) => [workspace.path, workspace] as const)),
    [workspaces],
  )
  const routeWorkspace = useMemo(
    () => (routeWorkspaceSlug ? resolveWorkspaceBySlug(workspaces, routeWorkspaceSlug) : null),
    [routeWorkspaceSlug, workspaces],
  )
  const temporaryRouteWorkspace = useMemo<WorkspaceEntry | null>(() => {
    const candidatePath = activeWorkspacePath?.trim() ?? ''
    if (!routeWorkspaceSlug || routeSessionId || routeWorkspace || !candidatePath || workspaceByPath.has(candidatePath)) {
      return null
    }
    const workspaceName = fallbackWorkspaceNameFromPath(candidatePath)
    if (workspaceRouteSlugBase({ path: candidatePath, workspaceName }) !== routeWorkspaceSlug) {
      return null
    }
    return buildTemporaryWorkspaceEntry(candidatePath, workspaceName)
  }, [activeWorkspacePath, routeSessionId, routeWorkspace, routeWorkspaceSlug, workspaceByPath])
  const selectedWorkspacePath = useMemo<string | null>(() => {
    const routeSession = routeSessionId ? liveSessions[routeSessionId] ?? null : null
    if (routeSession?.workspacePath) {
      return routeSession.workspacePath
    }
    if (routeWorkspace?.path) {
      return routeWorkspace.path
    }
    if (temporaryRouteWorkspace?.path) {
      return temporaryRouteWorkspace.path
    }
    return null
  }, [liveSessions, routeSessionId, routeWorkspace?.path, temporaryRouteWorkspace])
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

  const overviewQuery = useQuery({
    ...workspaceOverviewQueryOptions([], 25),
    placeholderData: (previousData) => previousData,
  })
  const uiSettingsQuery = useQuery({
    queryKey: uiSettingsQueryKey(),
    queryFn: () => getUISettings(),
    staleTime: 30_000,
  })
  useEffect(() => {
    if (uiSettingsQuery.data) {
      setUISettings(uiSettingsQuery.data)
    }
  }, [uiSettingsQuery.data])
  const swarmSettingsQuery = useQuery({
    queryKey: ['ui-settings', 'swarm'] as const,
    queryFn: () => getSwarmSettings(),
    staleTime: 30_000,
  })
  const swarmTargetsQuery = useQuery({
    queryKey: ['swarm-targets'] as const,
    queryFn: () => fetchSwarmTargets(),
    staleTime: 15_000,
    refetchInterval: 15_000,
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
  const masterWorkspaceName = selectedWorkspace?.workspaceName ?? routeWorkspace?.workspaceName ?? fallbackWorkspaceNameFromPath(selectedWorkspacePath ?? '')
  const swarmTargetCounts = useMemo(() => {
    const local = swarmTargets.filter((target) => target.kind === 'self' || target.kind === 'local').length
    const remote = swarmTargets.filter((target) => target.kind === 'remote').length
    return { local, remote }
  }, [swarmTargets])
  const swarmTargetSummary = `${swarmTargetCounts.local} local · ${swarmTargetCounts.remote} remote`
  const workspaceCount = mergedSidebarWorkspaceEntries.length
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
    debugLog('desktop-app-page', 'route-state', {
      routeWorkspaceSlug,
      routeSessionId,
      selectedWorkspacePath,
      activeWorkspacePath,
      activeSessionId,
      liveSessionCount: Object.keys(liveSessions).length,
      workspacesLoading,
      connectionState,
    })
  }, [activeSessionId, activeWorkspacePath, connectionState, liveSessions, routeSessionId, routeWorkspaceSlug, selectedWorkspacePath, workspacesLoading])

  useEffect(() => {
    debugLog('desktop-app-page', 'overview-query-state', {
      status: overviewQuery.status,
      fetchStatus: overviewQuery.fetchStatus,
      workspaceCount: overviewQuery.data?.workspaces?.length ?? 0,
    })
  }, [overviewQuery.data?.workspaces, overviewQuery.fetchStatus, overviewQuery.status])

  useEffect(() => {
    if (!swarmTopologySignature) {
      return
    }
    void queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
  }, [queryClient, swarmTopologySignature])


  const openTodoModal = useCallback((workspacePath: string, workspaceName: string) => {
    const normalizedPath = workspacePath.trim()
    if (!normalizedPath) {
      return
    }
    setTodoModal({ workspacePath: normalizedPath, workspaceName })
    void Promise.all([
      fetchWorkspaceTodos(normalizedPath, 'user'),
      fetchWorkspaceTodos(normalizedPath, 'agent'),
    ])
      .then(([userResult, agentResult]) => {
        setTodoItems((current) => ({
          ...current,
          [normalizedPath]: [...userResult.items, ...agentResult.items],
        }))
        setTodoSummaries((current) => ({ ...current, [normalizedPath]: normalizeWorkspaceTodoSummary(userResult.summary) }))
      })
      .catch(() => {
        setTodoItems((current) => ({ ...current, [normalizedPath]: [] }))
        setTodoSummaries((current) => ({ ...current, [normalizedPath]: createEmptyWorkspaceTodoSummary() }))
      })
  }, [])

  const closeTodoModal = useCallback(() => {
    setTodoModal(null)
  }, [])

  const handleSelectSwarmTarget = useCallback(async (target: SwarmTarget) => {
    if (!target.selectable || target.current) {
      setSwarmMenu({ open: false })
      return
    }
    setSwarmSwitchError(null)
    try {
      await selectSwarmTarget(target.swarm_id)
      setSwarmMenu({ open: false })
      queryClient.removeQueries({ queryKey: ['workspace-overview'] })
      queryClient.removeQueries({ queryKey: ['session-messages'] })
      queryClient.removeQueries({ queryKey: ['session-preference'] })
      queryClient.removeQueries({ queryKey: ['swarm-targets'] })
      useDesktopStore.getState().disconnect()
      useDesktopStore.setState((state) => ({
        ...state,
        sessions: {},
        activeSessionId: null,
        activeWorkspacePath: null,
        notifications: state.notifications.filter((notification) => notification.source !== 'swarm'),
        notificationCenter: {
          items: [],
          summary: {
            swarmID: '',
            totalCount: 0,
            unreadCount: 0,
            activeCount: 0,
            updatedAt: 0,
          },
          loading: false,
          hydrated: false,
        },
      }))
      await queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
      await queryClient.invalidateQueries({ queryKey: ['swarm-targets'] })
      void useDesktopStore.getState().hydrate()
      void useDesktopStore.getState().refreshNotifications()
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to switch swarm target'
      setSwarmSwitchError(message)
      console.error('[desktop-app-page] failed to switch swarm target', error)
    }
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

  const sessionsByWorkspace = useMemo<Map<string, DesktopSessionRecord[]>>(() => {
    const grouped = new Map<string, DesktopSessionRecord[]>()

    for (const workspace of mergedSidebarWorkspaceEntries) {
      grouped.set(workspace.path, [])
    }
    for (const workspace of overviewQuery.data?.workspaces ?? []) {
      grouped.set(workspace.path, workspace.sessions)
    }
    for (const session of Object.values(liveSessions)) {
      const workspacePath = session.workspacePath?.trim()
      if (!workspacePath || !grouped.has(workspacePath)) {
        continue
      }
      const existing = grouped.get(workspacePath) ?? []
      const existingIndex = existing.findIndex((entry) => entry.id === session.id)
      if (existingIndex >= 0) {
        const next = [...existing]
        next[existingIndex] = session
        grouped.set(workspacePath, next)
        continue
      }
      grouped.set(workspacePath, [session, ...existing])
    }

    return grouped
  }, [liveSessions, overviewQuery.data?.workspaces, mergedSidebarWorkspaceEntries])

  const allSessions = useMemo<DesktopSessionRecord[]>(
    () => {
      const sessionMap = new Map<string, DesktopSessionRecord>()
      for (const session of Array.from(sessionsByWorkspace.values()).flat()) {
        sessionMap.set(session.id, session)
      }
      const activeSession = activeSessionId ? useDesktopStore.getState().sessions[activeSessionId] : null
      if (activeSession?.id) {
        sessionMap.set(activeSession.id, activeSession)
      }
      return Array.from(sessionMap.values())
    },
    [activeSessionId, sessionsByWorkspace],
  )

  const sessionById = useMemo<Map<string, DesktopSessionRecord>>(
    () => new Map(allSessions.map((session) => [session.id, session] as const)),
    [allSessions],
  )

  const workspaceSlugByPath = useMemo(() => buildWorkspaceRouteSlugMap(mergedSidebarWorkspaceEntries), [mergedSidebarWorkspaceEntries])

  useEffect(() => {
    const normalizedRouteSessionId = routeSessionId?.trim() ?? ''
    debugLog('desktop-app-page', 'effect:route-session-hydration-check', {
      normalizedRouteSessionId,
      sessionCached: Boolean(normalizedRouteSessionId && sessionById.get(normalizedRouteSessionId)),
    })
    if (!normalizedRouteSessionId) {
      setRouteSessionPending(false)
      return
    }

    const cachedSession = sessionById.get(normalizedRouteSessionId) ?? null
    if (cachedSession && !sessionNeedsRefresh(cachedSession)) {
      debugLog('desktop-app-page', 'effect:route-session-hydration-skip-cache-hit', {
        normalizedRouteSessionId,
      })
      setRouteSessionPending(false)
      return
    }

    let cancelled = false
    setRouteSessionPending(true)

    void (async () => {
      try {
        debugLog('desktop-app-page', 'effect:route-session-fetch-start', {
          normalizedRouteSessionId,
        })
        const fetchedSession = await fetchSession(normalizedRouteSessionId)
        if (cancelled || !fetchedSession) {
          debugLog('desktop-app-page', 'effect:route-session-fetch-abort', {
            cancelled,
            hasSession: Boolean(fetchedSession),
          })
          return
        }
        debugLog('desktop-app-page', 'effect:route-session-fetch-success', {
          normalizedRouteSessionId,
          workspacePath: fetchedSession.workspacePath,
        })
        upsertSession(fetchedSession)
        void useDesktopStore.getState().refreshSessionPermissions(normalizedRouteSessionId)
        syncWorkspaceOverviewSession(queryClient, fetchedSession)
      } catch (error) {
        if (!cancelled) {
          console.error('[desktop-app] failed to hydrate route session', error)
        }
      } finally {
        if (!cancelled) {
          setRouteSessionPending(false)
        }
      }
    })()

    return () => {
      cancelled = true
    }
  }, [queryClient, routeSessionId, sessionById, upsertSession])

  const routeSession = routeSessionId ? sessionById.get(routeSessionId) ?? null : null

  const selectedSession = routeSessionId ? routeSession : null

  useEffect(() => {
    if (!selectedWorkspacePath) {
      if (activeWorkspacePath !== null) {
        setActiveWorkspacePath(null)
      }
      if (activeSessionId !== null) {
        setActiveSession(null)
      }
      return
    }

    if (selectedWorkspacePath !== activeWorkspacePath) {
      setActiveWorkspacePath(selectedWorkspacePath)
    }

    if (routeSessionId && selectedSession?.id) {
      if (selectedSession.id !== activeSessionId) {
        setActiveSession(selectedSession.id)
      }
      return
    }

    if (activeSessionId !== null) {
      setActiveSession(null)
    }
  }, [activeSessionId, activeWorkspacePath, routeSessionId, selectedSession, selectedWorkspacePath, setActiveSession, setActiveWorkspacePath])

  const visibleWorkspacePaths = useMemo<string[]>(() => visibleSidebarWorkspaceEntries.map((workspace) => workspace.path), [visibleSidebarWorkspaceEntries])

  const stopResize = useCallback(() => {
    resizeStateRef.current = null
  }, [])

  const handlePointerMove = useCallback((event: PointerEvent) => {
    const activeResize = resizeStateRef.current
    const containerHeight = sidebarBodyRef.current?.getBoundingClientRect().height ?? 0
    if (!activeResize || containerHeight <= 0) {
      return
    }

    const pairRatio = activeResize.topRatio + activeResize.bottomRatio
    const desiredMinRatio = (MIN_WORKSPACE_SECTION_HEIGHT_PX / containerHeight) * activeResize.totalVisibleRatio
    const minRatio = Math.min(Math.max(desiredMinRatio, 0.12), Math.max((pairRatio - 0.12) / 2, 0.06))
    const deltaRatio = ((event.clientY - activeResize.startY) / containerHeight) * activeResize.totalVisibleRatio
    const nextTopRatio = Math.min(Math.max(activeResize.topRatio + deltaRatio, minRatio), pairRatio - minRatio)
    const nextBottomRatio = pairRatio - nextTopRatio

    setWorkspaceLayout((current) => ({
      ...current,
      [activeResize.topPath]: {
        collapsed: current[activeResize.topPath]?.collapsed ?? true,
        hidden: current[activeResize.topPath]?.hidden ?? false,
        ratio: nextTopRatio,
      },
      [activeResize.bottomPath]: {
        collapsed: current[activeResize.bottomPath]?.collapsed ?? true,
        hidden: current[activeResize.bottomPath]?.hidden ?? false,
        ratio: nextBottomRatio,
      },
    }))
  }, [])

  useEffect(() => {
    const handlePointerUp = () => {
      stopResize()
    }

    window.addEventListener('pointermove', handlePointerMove)
    window.addEventListener('pointerup', handlePointerUp)
    return () => {
      window.removeEventListener('pointermove', handlePointerMove)
      window.removeEventListener('pointerup', handlePointerUp)
    }
  }, [handlePointerMove, stopResize])

  const handleResizeStart = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>, topPath: string, bottomPath: string) => {
      event.preventDefault()

      resizeStateRef.current = {
        topPath,
        bottomPath,
        startY: event.clientY,
        topRatio: normalizeRatio(workspaceLayout[topPath]?.ratio),
        bottomRatio: normalizeRatio(workspaceLayout[bottomPath]?.ratio),
        totalVisibleRatio: visibleWorkspacePaths.reduce((sum, path) => sum + normalizeRatio(workspaceLayout[path]?.ratio), 0) || visibleWorkspacePaths.length || 1,
      }
    },
    [visibleWorkspacePaths, workspaceLayout],
  )

  const toggleWorkspaceCollapse = useCallback((path: string) => {
    setWorkspaceLayout((current) => ({
      ...current,
      [path]: {
        collapsed: !current[path]?.collapsed,
        hidden: current[path]?.hidden ?? false,
        ratio: normalizeRatio(current[path]?.ratio),
      },
    }))
  }, [])

  const toggleWorkspaceHidden = useCallback((path: string) => {
    setWorkspaceLayout((current) => ({
      ...current,
      [path]: {
        collapsed: current[path]?.collapsed ?? true,
        hidden: !current[path]?.hidden,
        ratio: normalizeRatio(current[path]?.ratio),
      },
    }))
  }, [])


  useEffect(() => {
    if (!routeWorkspaceSlug || routeSessionId || !routeWorkspace?.path) {
      return
    }
    const canonicalWorkspaceSlug = workspaceSlugByPath.get(routeWorkspace.path)
    if (!canonicalWorkspaceSlug || canonicalWorkspaceSlug === routeWorkspaceSlug) {
      return
    }
    debugLog('desktop-app-page', 'effect:canonicalize-workspace-route', {
      from: routeWorkspaceSlug,
      to: canonicalWorkspaceSlug,
      workspacePath: routeWorkspace.path,
    })
    void navigate({
      to: '/$workspaceSlug',
      params: { workspaceSlug: canonicalWorkspaceSlug },
      replace: true,
    })
  }, [navigate, routeSessionId, routeWorkspace?.path, routeWorkspaceSlug, workspaceSlugByPath])

  useEffect(() => {
    if (!routeSession?.id || !routeSession.workspacePath) {
      return
    }
    const canonicalWorkspaceSlug = workspaceSlugByPath.get(routeSession.workspacePath)
    if (!canonicalWorkspaceSlug || (canonicalWorkspaceSlug === routeWorkspaceSlug && routeSession.id === routeSessionId)) {
      return
    }
    debugLog('desktop-app-page', 'effect:canonicalize-session-route', {
      fromWorkspaceSlug: routeWorkspaceSlug,
      toWorkspaceSlug: canonicalWorkspaceSlug,
      routeSessionId,
      canonicalSessionId: routeSession.id,
    })
    void navigate({
      to: '/$workspaceSlug/$sessionId',
      params: {
        workspaceSlug: canonicalWorkspaceSlug,
        sessionId: routeSession.id,
      },
      replace: true,
    })
  }, [navigate, routeSession?.id, routeSession?.workspacePath, routeSessionId, routeWorkspaceSlug, workspaceSlugByPath])

  const handleSelectSession = useCallback((sessionId: string) => {
    const session = sessionById.get(sessionId)
    if (!session?.workspacePath) {
      return
    }
    setMobileSidebarOpen(false)
    const workspaceSlug = workspaceSlugByPath.get(session.workspacePath)
      ?? workspaceRouteSlugBase({ path: session.workspacePath, workspaceName: session.workspaceName })
    void navigate({
      to: '/$workspaceSlug/$sessionId',
      params: {
        workspaceSlug,
        sessionId: session.id,
      },
    })
  }, [navigate, sessionById, workspaceSlugByPath])

  const handleSessionCreated = useCallback((session: DesktopSessionRecord) => {
    setActiveSession(session.id)
    if (session.workspacePath) {
      setActiveWorkspacePath(session.workspacePath)
    }
    if (!session.workspacePath) {
      return
    }
    const workspaceSlug = workspaceSlugByPath.get(session.workspacePath)
      ?? workspaceRouteSlugBase({ path: session.workspacePath, workspaceName: session.workspaceName })
    void navigate({
      to: '/$workspaceSlug/$sessionId',
      params: {
        workspaceSlug,
        sessionId: session.id,
      },
    })
  }, [navigate, setActiveSession, setActiveWorkspacePath, workspaceSlugByPath])

  const chatWorkspacePath = selectedSession?.workspacePath || selectedWorkspace?.path || ''
  const chatWorkspaceName = selectedSession?.workspaceName || selectedWorkspace?.workspaceName || ''

  const handleStartNewSessionInWorkspace = useCallback((wsPath: string, wsName: string) => {
    setMobileSidebarOpen(false)
    setActiveSession(null)
    setActiveWorkspacePath(wsPath)
    const workspaceSlug = workspaceSlugByPath.get(wsPath)
      ?? workspaceRouteSlugBase({ path: wsPath, workspaceName: wsName })
    void navigate({
      to: '/$workspaceSlug',
      params: { workspaceSlug },
    })
  }, [navigate, setActiveSession, setActiveWorkspacePath, workspaceSlugByPath])

  const handleOpenSettingsTab = useCallback((tab: SettingsTabID) => {
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/settings', params: { workspaceSlug: routeWorkspaceSlug }, search: { tab } })
      return
    }
    void navigate({ to: '/settings', search: { tab } })
  }, [navigate, routeWorkspaceSlug])

  const handleOpenPermissions = useCallback(() => {
    handleOpenSettingsTab('permissions')
  }, [handleOpenSettingsTab])

  const handleOpenSwarmDashboard = useCallback(() => {
    handleOpenSettingsTab('swarm')
  }, [handleOpenSettingsTab])

  useEffect(() => {
    if (!updateAvailable) {
      return
    }
    void refreshNotifications()
  }, [refreshNotifications, updateAvailable, updateLatestVersion])

  const runDesktopUpdate = useCallback(async () => {
    setUpdateRunning(true)
    setUpdateProgress({ open: true, job: null, startedAt: Date.now() })
    try {
      const initialJob = await startDesktopUpdate()
      setUpdateProgress((current) => ({ ...current, job: initialJob }))
      await refreshNotifications()
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
          await refreshNotifications()
          window.location.reload()
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
      await refreshNotifications()
    } finally {
      setUpdateRunning(false)
    }
  }, [refreshNotifications, updateDevMode])

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
      const warningDismissed = localContainerUpdateWarningDismissed(settings)
      if (!warningDismissed || remoteUpdateCount > 0) {
        const plan = await fetchLocalContainerUpdatePlan({ devMode: status.dev_mode, targetVersion: status.latest_version, postRebuildCheck: status.dev_mode })
        if ((!warningDismissed && localContainerUpdateAffected(plan)) || remoteUpdateCount > 0) {
          setLocalContainerUpdateConfirm({ plan, remoteSessions, pendingDismiss: false })
          return
        }
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to check container images before update'
      setUpdateError(message)
      return
    }
    await runDesktopUpdate()
  }, [effectiveUISettings, localContainerUpdateConfirm, runDesktopUpdate, uiSettingsQuery, updateRunning, updateStatus, updateStatusError, updateStatusQuery])

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

  const handleOpenWorkspaceLauncher = useCallback(() => {
    setMobileSidebarOpen(false)
    setActiveSession(null)
    void navigate({ to: '/' })
  }, [navigate, setActiveSession])

  const handleOpenMobileSidebar = useCallback(() => {
    setSidebarCollapsed(false)
    setMobileSidebarOpen(true)
  }, [])

  const handlePrefetchSession = useCallback((sessionId: string) => {
    void prefetchSessionRuntimeData(queryClient, sessionId)
  }, [queryClient])

  const handleToggleAgentSessions = useCallback((sessionId: string) => {
    setExpandedAgentSessions((current) => ({
      ...current,
      [sessionId]: !current[sessionId],
    }))
  }, [])

  useEffect(() => {
    setMobileSidebarOpen(false)
  }, [routeSessionId, routeWorkspaceSlug])

  const totalVisibleRatio = useMemo(
    () => visibleSidebarWorkspaceEntries.reduce((sum, workspace) => {
      const layout = workspaceLayout[workspace.path]
      if (layout?.collapsed) return sum
      return sum + normalizeRatio(layout?.ratio)
    }, 0) || visibleSidebarWorkspaceEntries.length || 1,
    [visibleSidebarWorkspaceEntries, workspaceLayout],
  )

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
          <Button variant="ghost" className="h-12 w-12 min-w-12 p-0" onClick={() => { if (selectedWorkspacePath) { openTodoModal(selectedWorkspacePath, selectedWorkspace?.workspaceName ?? 'Workspace') } }} aria-label="Open tasks" disabled={!selectedWorkspacePath}>
            <ListChecks size={24} className="shrink-0" />
          </Button>
          {updateAttentionVisible ? (
            <Button variant="ghost" className="relative h-12 w-12 min-w-12 p-0" onClick={() => { void handleDesktopUpdate() }} aria-label={updateActionLabel} title={updateActionTitle} disabled={updateRunning || !updateActionEnabled}>
              <Download size={24} className={cn('shrink-0', updateRunning && 'animate-pulse', updateActionEnabled && 'text-[var(--app-primary)]', updateError && 'text-[var(--app-error)]')} />
              {updateActionEnabled ? <span aria-hidden="true" className="absolute right-2 top-2 h-2.5 w-2.5 rounded-full bg-[var(--app-primary)] shadow-[0_0_10px_var(--app-primary)]" /> : null}
            </Button>
          ) : null}
          <div className="mt-2 flex flex-col items-center">
            <span className={cn('h-2.5 w-2.5 rounded-full', connectionDotClass(connectionState))} />
          </div>
        </div>
      ) : (
        <div className="flex h-full flex-col min-h-0">
          <div className="border-b border-[var(--app-border)] font-mono">
            <div className="min-h-[124px] border border-[color-mix(in_srgb,var(--app-border)_74%,transparent)] bg-[var(--app-surface)]">
              <div className="p-[12px_0_11px_13px]">
                <div className={updateAttentionVisible ? 'grid min-w-0 grid-cols-[minmax(0,1fr)_78px] items-center gap-2.5 min-h-7 pr-4' : cn(SIDEBAR_ACTION_ROW_CLASS, 'min-h-7 pr-4')}>
                  <div className="min-w-0">
                    <div className="truncate text-[15px] font-semibold tracking-[-0.035em] text-[var(--app-text)]">{swarmName}</div>
                    <div className="mt-px truncate text-[10px] leading-[1.25] text-[var(--app-text-subtle)]">
                      <strong className="font-medium text-[var(--app-text-muted)]">Master</strong> · {masterWorkspaceName}
                    </div>
                  </div>
                  <SidebarActionRail className={updateAttentionVisible ? '!w-[78px] !grid-cols-[24px_24px_24px]' : undefined}>
                    <button
                      type="button"
                      className={cn(
                        SIDEBAR_ACTION_BUTTON_CLASS,
                        'text-[var(--app-text-subtle)]',
                        notificationCenter.summary.unreadCount > 0 && 'text-[color-mix(in_srgb,var(--app-warning)_82%,var(--app-text-muted))] hover:bg-[var(--app-warning-bg)] hover:text-[var(--app-primary-hover)]',
                      )}
                      onClick={() => setNotificationsOpen(true)}
                      aria-label="Open notifications"
                      title={notificationCenter.summary.unreadCount > 0 ? `${notificationCenter.summary.unreadCount} unread notifications` : 'Notifications'}
                    >
                      <Bell size={14} strokeWidth={1.8} className="shrink-0" />
                    </button>
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

                <div className={cn(SIDEBAR_ACTION_ROW_CLASS, 'mt-[7px] min-h-[30px] pr-4 text-[11px] text-[var(--app-text-subtle)]')}>
                  <button
                    type="button"
                    className="flex min-w-0 items-center gap-2 overflow-hidden whitespace-nowrap border-0 bg-transparent p-0 text-left font-inherit hover:text-[var(--app-text)]"
                    onClick={() => {
                      setWorkspaceMenuOpen(false)
                      setSwarmMenu((current) => ({ open: !current.open }))
                    }}
                    aria-expanded={swarmMenu.open}
                    aria-label="Choose swarm target"
                    title={swarmTargetSummary}
                  >
                    <span className="shrink-0 text-[color-mix(in_srgb,var(--app-success)_58%,var(--app-text-subtle))]">{swarmTargetCounts.local} local</span>
                    <span className="shrink-0 opacity-55">·</span>
                    <span className="truncate text-[color-mix(in_srgb,var(--app-info)_58%,var(--app-text-subtle))]">{swarmTargetCounts.remote} remote</span>
                  </button>
                  <SidebarActionRail>
                    <SidebarActionRailSpacer />
                    <button
                      type="button"
                      className={SIDEBAR_ACTION_BUTTON_CLASS}
                      onClick={handleOpenSwarmDashboard}
                      aria-label="Add swarm"
                      title="Add swarm"
                    >
                      <Plus size={14} strokeWidth={1.8} className="shrink-0" />
                    </button>
                  </SidebarActionRail>
                </div>

                {swarmMenu.open ? (
                  <div className="mr-4 mt-1.5 border border-[var(--app-border)] bg-[var(--app-surface)] py-1">
                    {swarmTargets.map((target) => (
                      <button
                        key={target.swarm_id}
                        type="button"
                        onClick={() => { void handleSelectSwarmTarget(target) }}
                        disabled={!target.selectable}
                        className={cn(
                          SIDEBAR_ACTION_ROW_CLASS,
                          'min-h-[30px] w-full px-[7px] py-[5px] text-left text-[12px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                          target.current && 'bg-[var(--app-surface-active)] text-[var(--app-text)] shadow-[inset_2px_0_0_var(--app-success)]',
                          !target.selectable && 'cursor-not-allowed opacity-50',
                        )}
                        title={`${swarmKindLabel(target)} · ${target.online ? 'online' : (target.attach_status || 'offline')}`}
                      >
                        <span className="flex min-w-0 items-center gap-2">
                          <span className={cn('h-[5px] w-[5px] shrink-0 rounded-full', swarmKindDotClass(target.kind, target.online))} />
                          <span className="truncate">{target.name}</span>
                        </span>
                        <span className="shrink-0 truncate text-right text-[10px] text-[var(--app-text-subtle)]">
                          {swarmKindLabel(target)} · {target.current ? 'active' : target.online ? 'online' : (target.attach_status || 'offline')}
                        </span>
                      </button>
                    ))}
                    {swarmSwitchError ? <div className="mt-1 border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-2 py-1.5 text-[10px] text-[var(--app-warning)]">{swarmSwitchError}</div> : null}
                  </div>
                ) : null}

                <div className={cn(SIDEBAR_ACTION_ROW_CLASS, 'mt-[7px] min-h-7 border-t border-[color-mix(in_srgb,var(--app-border)_54%,transparent)] pr-4 pt-[7px] text-[11px] text-[var(--app-text-subtle)]')}>
                  <button
                    type="button"
                    className="flex min-h-[22px] min-w-0 items-center gap-1 overflow-hidden border-0 bg-transparent p-0 font-inherit text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                    onClick={() => {
                      setSwarmMenu({ open: false })
                      setWorkspaceMenuOpen((open) => !open)
                    }}
                    aria-expanded={workspaceMenuOpen}
                    aria-label={`${workspaceMenuOpen ? 'Collapse' : 'Expand'} workspace list`}
                  >
                    <span className="truncate">{workspaceCount} workspaces</span>
                    {workspaceMenuOpen ? <ChevronDown size={14} strokeWidth={1.8} className="shrink-0 rotate-180" /> : <ChevronDown size={14} strokeWidth={1.8} className="shrink-0" />}
                  </button>
                  <SidebarActionRail>
                    <SidebarActionRailSpacer />
                    <button
                      type="button"
                      className={SIDEBAR_ACTION_BUTTON_CLASS}
                      onClick={() => handleOpenSettingsTab('agents')}
                      aria-label="Open agent settings"
                      title="Settings"
                    >
                      <Settings size={14} strokeWidth={1.8} className="shrink-0" />
                    </button>
                  </SidebarActionRail>
                </div>
              </div>
            </div>

            {workspaceMenuOpen ? (
              <div className="mt-2 flex flex-col border border-[var(--app-border)] bg-[var(--app-surface)] p-1 font-mono">
                {mergedSidebarWorkspaceEntries.length === 0 ? (
                  <div className="px-2 py-1.5 text-[11px] text-[var(--app-text-subtle)]">No saved workspaces.</div>
                ) : mergedSidebarWorkspaceEntries.map((workspace) => {
                  const hidden = workspaceLayout[workspace.path]?.hidden ?? false
                  return (
                    <div key={workspace.path} className={cn('group min-h-[30px] px-[7px] py-[5px] text-[12px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]', SIDEBAR_ACTION_ROW_CLASS)}>
                      <button
                        type="button"
                        className="flex min-w-0 items-center gap-2 overflow-hidden text-left"
                        onClick={() => handleStartNewSessionInWorkspace(workspace.path, workspace.workspaceName)}
                        title={workspace.path}
                      >
                        <span className={cn('h-[5px] w-[5px] shrink-0 rounded-full', hidden ? 'bg-[var(--app-text-subtle)]' : 'bg-[var(--app-success)]')} />
                        <span className={cn('truncate', hidden && 'opacity-60')}>{workspace.workspaceName}</span>
                      </button>
                      <SidebarActionRail>
                        <SidebarActionRailSpacer />
                        <button
                          type="button"
                          className={cn(SIDEBAR_ACTION_BUTTON_CLASS, 'text-[var(--app-text-subtle)] opacity-0 hover:bg-[var(--app-surface-active)] group-hover:opacity-100')}
                          onClick={(e) => { e.stopPropagation(); toggleWorkspaceHidden(workspace.path) }}
                          aria-label={`${hidden ? 'Show' : 'Hide'} ${workspace.workspaceName} in sidebar`}
                          title={`${hidden ? 'Show' : 'Hide'} in sidebar`}
                        >
                          {hidden ? <EyeOff size={14} /> : <Eye size={14} />}
                        </button>
                      </SidebarActionRail>
                    </div>
                  )
                })}
                <div className="my-1 h-px bg-[var(--app-border)]" />
                <button
                  type="button"
                  className="flex min-h-[30px] items-center gap-2 px-[7px] py-[5px] text-left text-[12px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                  onClick={handleOpenWorkspaceLauncher}
                >
                  <Home size={14} className="shrink-0" />
                  Workspace settings
                </button>
              </div>
            ) : null}
          </div>
          <div className="flex min-h-0 flex-1 flex-col">
            <div ref={sidebarBodyRef} className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 py-3">
              {visibleSidebarWorkspaceEntries.map((workspace, index) => {
                const workspaceSessions = sessionsByWorkspace.get(workspace.path) ?? []
                const sessionNodes = buildSidebarSessionTree(workspaceSessions, sidebarNow)
                const flattenedSessionNodes = flattenVisibleSidebarSessionNodes(sessionNodes, expandedAgentSessions, selectedSession?.id)
                const layout = workspaceLayout[workspace.path]
                const collapsed = layout?.collapsed ?? true
                const worktreeBusy = savingPath === workspace.path
                const handleToggleWorkspaceWorktree = () => {
                  if (worktreeBusy) {
                    return
                  }
                  void setWorktreeEnabled(workspace.path, !workspace.worktreeEnabled)
                }
                return (
                  <Fragment key={workspace.path}>
                    <section style={workspaceSectionHeightStyle(layout?.ratio ?? 1, totalVisibleRatio, collapsed)} className="flex min-h-0 flex-col overflow-hidden">
                      <div className={cn(SIDEBAR_ACTION_ROW_CLASS, 'px-1 py-1')}>
                        <button
                          type="button"
                          onClick={() => {
                            toggleWorkspaceCollapse(workspace.path)
                          }}
                          className="flex min-w-0 items-center gap-2 overflow-hidden text-left transition-colors hover:text-[var(--app-text)]"
                          aria-label={`${collapsed ? 'Expand' : 'Collapse'} ${workspace.workspaceName}`}
                        >
                          {collapsed ? <ChevronRight size={16} className="shrink-0 text-[var(--app-text-subtle)]" /> : <ChevronDown size={16} className="shrink-0 text-[var(--app-text-subtle)]" />}
                          <div className="truncate text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">{workspace.workspaceName}</div>
                        </button>
                        <SidebarActionRail>
                          {!workspaceByPath.has(workspace.path) ? (
                            <button
                              type="button"
                              className={cn(SIDEBAR_ACTION_BUTTON_CLASS, 'text-[var(--app-warning)] hover:bg-[var(--app-warning-bg)] hover:text-[var(--app-warning)]')}
                              onClick={() => {
                                void saveWorkspace({
                                  path: workspace.path,
                                  name: workspace.workspaceName,
                                  themeId: workspace.themeId,
                                  makeCurrent: true,
                                  linkedDirectories: [],
                                })
                              }}
                              aria-label="Save this temporary folder as a workspace"
                              title="Save this temporary folder as a workspace"
                            >
                              !
                            </button>
                          ) : selectingPath === workspace.path ? <span className="grid h-6 w-6 place-items-center text-[10px] text-[var(--app-text-muted)]">…</span> : (
                            <button type="button" className={SIDEBAR_ACTION_BUTTON_CLASS} onClick={() => { openTodoModal(workspace.path, workspace.workspaceName) }} aria-label={`Open tasks for ${workspace.workspaceName}`} title={`${workspace.todoSummary?.user.taskCount ?? 0} tasks`}>
                              <ListChecks size={14} strokeWidth={1.8} className="shrink-0" />
                            </button>
                          )}
                          <button type="button" className={SIDEBAR_ACTION_BUTTON_CLASS} onClick={() => handleStartNewSessionInWorkspace(workspace.path, workspace.workspaceName)} aria-label={`New session in ${workspace.workspaceName}`} title="New session">
                            <Plus size={14} strokeWidth={1.8} className="shrink-0" />
                          </button>
                        </SidebarActionRail>
                      </div>
                      {!collapsed && renderWorkspaceGitBar({
                        workspace,
                        worktreeBusy,
                        onToggle: handleToggleWorkspaceWorktree,
                      })}
                      {!collapsed && (
                        <div className="grid min-h-0 flex-1 content-start gap-0.5 overflow-y-auto">
                          {flattenedSessionNodes.length === 0 ? null : flattenedSessionNodes.map((node) => (
                            <SessionRow
                              key={node.session.id}
                              active={selectedSession?.id === node.session.id}
                              now={sidebarNow}
                              session={node.session}
                              fallbackSwarmName={swarmName}
                              depth={node.depth}
                              childLabel={node.label}
                              childKind={node.kind}
                              agentSummary={summarizeSubagentDescendants(node)}
                              agentsExpanded={Boolean(expandedAgentSessions[node.session.id]) || nodeContainsDescendantSession(node, selectedSession?.id)}
                              onSelect={handleSelectSession}
                              onPrefetch={handlePrefetchSession}
                              onToggleAgents={handleToggleAgentSessions}
                            />
                          ))}
                        </div>
                      )}
                    </section>
                    {index < visibleSidebarWorkspaceEntries.length - 1 && !collapsed && !workspaceLayout[visibleSidebarWorkspaceEntries[index + 1].path]?.collapsed ? (
                      <div
                        role="separator"
                        aria-orientation="horizontal"
                        className="relative my-2 h-4 cursor-row-resize group px-1"
                        onPointerDown={(event) => handleResizeStart(event, workspace.path, visibleSidebarWorkspaceEntries[index + 1].path)}
                      >
                        <div className="absolute inset-x-1 top-1/2 h-px bg-[var(--app-border)] group-hover:bg-[var(--app-border-strong)] transition-colors" />
                      </div>
                    ) : index < visibleSidebarWorkspaceEntries.length - 1 ? (
                      <div className="h-2" />
                    ) : null}
                  </Fragment>
                )
              })}
            </div>

          </div>
        </div>
      )}
    </>
  )

  const localContainerConfirmPlan = localContainerUpdateConfirm?.plan ?? null
  const remoteContainerUpdateCount = localContainerUpdateConfirm ? remoteDeployUpdateSessionCount(localContainerUpdateConfirm.remoteSessions) : 0
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
    <div className="flex absolute inset-0 overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      <aside className={cn('hidden shrink-0 flex-col border-r border-[var(--app-border)] bg-[var(--app-surface)] sm:flex', sidebarCollapsed ? 'sm:w-[56px]' : 'sm:w-[320px]')}>
        {sidebarContent}
      </aside>
      {mobileSidebarOpen ? (
        <div className="absolute inset-0 z-40 flex sm:hidden" aria-modal="true" role="dialog">
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
              <Button variant="ghost" className="h-10 w-10 p-0" onClick={() => setMobileSidebarOpen(false)} aria-label="Close sidebar">
                <X size={18} />
              </Button>
            </div>
            <div className="min-h-0 flex-1">{sidebarContent}</div>
          </div>
        </div>
      ) : null}

      <main className="flex-1 min-w-0 min-h-0 flex flex-col h-full overflow-hidden">
        {routeSessionId && routeSessionPending && !selectedSession ? (
          <div className="flex h-full flex-1 items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">Loading session…</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                Resolving the requested conversation.
              </p>
            </Card>
          </div>
        ) : routeSessionId && !selectedSession ? (
          <div className="flex h-full flex-1 items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">Session not found</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                We couldn’t find that session in cache or on the server.
              </p>
            </Card>
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
        ) : chatWorkspacePath ? (
          <DesktopChatPanel
            hostSwarmName={swarmName}
            workspacePath={chatWorkspacePath}
            workspaceName={chatWorkspaceName}
            workspaceWorktreeEnabled={selectedWorkspace?.worktreeEnabled ?? false}
            workspaceReplicationLinks={selectedWorkspace?.replicationLinks ?? []}
            session={selectedSession}
            onSessionCreated={handleSessionCreated}
            onOpenSettingsTab={handleOpenSettingsTab}
            onOpenPermissions={handleOpenPermissions}
            onOpenWorkspaceLauncher={handleOpenWorkspaceLauncher}
            onOpenSidebarMenu={handleOpenMobileSidebar}
            onStartNewSession={handleStartNewSessionInWorkspace}
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
            {remoteContainerUpdateCount === 0 ? (
              <label className="mt-4 flex items-center gap-2 text-sm text-[var(--app-text-muted)]">
                <input
                  type="checkbox"
                  checked={localContainerUpdateConfirm?.pendingDismiss ?? false}
                  onChange={(event) => handleToggleLocalContainerUpdateDismissal(event.currentTarget.checked)}
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

      <DesktopNotificationsOverlay open={notificationsOpen} onOpenChange={setNotificationsOpen} />

    </div>
  )
}
