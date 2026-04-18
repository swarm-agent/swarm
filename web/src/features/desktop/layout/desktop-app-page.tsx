import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, JSX, PointerEvent as ReactPointerEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { Bell, ChevronDown, ChevronLeft, ChevronRight, ChevronsUpDown, GitBranch, GitCommitHorizontal, Home, ListChecks, Plus, Settings } from 'lucide-react'
import { debugLog } from '../../../lib/debug-log'
import { Button } from '../../../components/ui/button'
import { Card } from '../../../components/ui/card'
import { cn } from '../../../lib/cn'
import { useDesktopStore } from '../state/use-desktop-store'
import {
  loadDesktopChatRouteForSession,
} from '../chat/services/chat-routing'
import { useWorkspaceLauncher } from '../../workspaces/launcher/state/use-workspace-launcher'
import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'
import { prefetchSessionRuntimeData, workspaceOverviewQueryOptions } from '../../queries/query-options'
import type { DesktopSessionRecord } from '../types/realtime'
import { DesktopSettingsModal } from '../settings/components/desktop-settings-modal'
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
import { fetchSwarmTargets, selectSwarmTarget, type SwarmTarget } from '../swarm/api/swarm-targets'
import { fetchSession } from '../chat/queries/chat-queries'
import {
  sessionChildDescriptor,
  sessionParentSessionID,
  type SidebarSessionNodeKind,
} from './sidebar-session-lineage'

const DESKTOP_SIDEBAR_LAYOUT_STORAGE_KEY = 'swarm.web.desktop.sidebar.layout'
const MIN_WORKSPACE_SECTION_HEIGHT_PX = 120
const SIDEBAR_ACTIVITY_GRACE_MS = 15_000

interface SidebarWorkspaceLayout {
  collapsed: boolean
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

interface SwarmTargetMenuState {
  open: boolean
}

function normalizeWorkspaceTodoSummary(summary: WorkspaceTodoSummary): WorkspaceTodoSummary {
  return {
    ...summary,
    taskCount: summary.user.taskCount + summary.agent.taskCount,
    openCount: summary.user.openCount + summary.agent.openCount,
    inProgressCount: summary.user.inProgressCount + summary.agent.inProgressCount,
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
          collapsed: Boolean(entry?.collapsed ?? entry?.hidden),
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

function sessionOriginLabel(session: DesktopSessionRecord, fallbackSwarmName: string): string {
  const route = loadDesktopChatRouteForSession(session.id)
  const routeLabel = route?.label?.trim() ?? ''
  if (routeLabel) {
    return routeLabel
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
    <div className="flex items-center gap-2 px-6 pb-2 pt-0.5 text-[11px] min-w-0">
      <span className={cn('truncate font-semibold', tone)}>{branch}</span>
      <span className="shrink-0 text-[var(--app-text-muted)]">/</span>
      <span className="shrink-0 text-[var(--app-text-muted)]">{syncLabel}</span>
      {dirtyLabel ? <span className="shrink-0 text-[var(--app-text-muted)]">/</span> : null}
      {dirtyLabel ? <span className={cn('shrink-0 text-[10px]', tone)}>{dirtyLabel}</span> : null}
      <button
        type="button"
        className={cn(
          'ml-auto shrink-0 rounded px-1.5 py-0.5 text-[10px]',
          enabled ? 'text-[var(--app-selection)]' : 'text-[var(--app-text-muted)] opacity-45 hover:opacity-85',
          worktreeBusy && 'cursor-progress opacity-70',
        )}
        onClick={onToggle}
        aria-busy={worktreeBusy}
        aria-disabled={worktreeBusy}
        aria-pressed={enabled}
        title={title}
      >
        {enabled ? 'worktree' : 'wt'}
      </button>
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

function flattenSidebarSessionNodes(nodes: SidebarSessionNode[]): SidebarSessionNode[] {
  const output: SidebarSessionNode[] = []
  const visit = (node: SidebarSessionNode) => {
    output.push(node)
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
  depth?: number
  childLabel?: string | null
  childKind?: SidebarSessionNode['kind']
  onSelect: (sessionId: string) => void
  onPrefetch: (sessionId: string) => void
}

function SessionRow({ active, now, session: initialSession, fallbackSwarmName, depth = 0, childLabel = null, childKind = 'root', onSelect, onPrefetch }: SessionRowProps) {
  const session = useDesktopStore((state) => state.sessions[initialSession.id] ?? initialSession)
  const activeSession = sessionIsActive(session)
  const originLabel = sessionOriginLabel(session, fallbackSwarmName)
  const timerLabel = activeSession ? sessionTimerLabel(session, now) : ''
  const rightLabel = activeSession
    ? sessionActivityLabel(session) || sessionMeta(session) || ''
    : sessionStatusDetail(session, now) || sessionMeta(session) || ''
  const commitSummary = sessionCommitSummary(session)
  const committedFileSummary = sessionCommittedFileSummary(session)
  const committedDeltaSummary = sessionCommittedDeltaSummary(session)
  const commitMetaLabel = [commitSummary, committedFileSummary, committedDeltaSummary].filter(Boolean).join(' · ')
  const tooltip = sessionStatusTooltip(session)
  const indentStyle = depth > 0 ? { paddingLeft: `${Math.min(depth, 5) * 16}px` } : undefined

  return (
    <button
      type="button"
      onClick={() => onSelect(session.id)}
      onMouseEnter={() => onPrefetch(session.id)}
      onFocus={() => onPrefetch(session.id)}
      className={cn(
        'grid w-full min-w-0 gap-1 rounded-lg px-3 py-2 text-left transition-colors',
        active
          ? 'bg-[var(--app-surface-subtle)]'
          : 'bg-transparent hover:bg-[var(--app-surface-subtle)]',
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
        {activeSession ? (
          <>
            <div className="flex min-w-0 shrink-0 items-center gap-2 overflow-hidden">
              <span className="max-w-[8rem] truncate">{originLabel}</span>
              {childLabel ? (
                <span className={cn(
                  'shrink-0 truncate text-[11px]',
                  childKind === 'subagent' ? 'text-sky-300/90' : 'text-[var(--app-text-subtle)]',
                )}>
                  {childLabel}
                </span>
              ) : null}
              <span className="shrink-0 text-[var(--app-text-subtle)]">{timerLabel}</span>
            </div>
            <span className="min-w-0 flex-1 truncate text-right text-[var(--app-text-subtle)]">{rightLabel}</span>
          </>
        ) : (
          <>
            <div className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden">
              <span className="truncate">{originLabel}</span>
              {childLabel ? (
                <span className={cn(
                  'shrink-0 truncate text-[11px]',
                  childKind === 'subagent' ? 'text-sky-300/90' : 'text-[var(--app-text-subtle)]',
                )}>
                  {childLabel}
                </span>
              ) : null}
            </div>
            <span className="shrink-0 text-[var(--app-text-subtle)]">{rightLabel}</span>
          </>
        )}
      </div>
      {session.worktreeEnabled || commitMetaLabel ? (
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
          {session.worktreeEnabled ? (
            <span
              className="inline-flex shrink-0 items-center justify-center text-[var(--app-text-subtle)] opacity-80"
              title={tooltip || 'Worktree enabled'}
            >
              <GitBranch size={12} />
            </span>
          ) : null}
        </div>
      ) : null}
    </button>
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
  const vault = useDesktopStore((state) => state.vault)
  const liveSessions = useDesktopStore((state) => state.sessions)
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)
  const activeWorkspacePath = useDesktopStore((state) => state.activeWorkspacePath)
  const notifications = useDesktopStore((state) => state.notifications)
  const setActiveSession = useDesktopStore((state) => state.setActiveSession)
  const setActiveWorkspacePath = useDesktopStore((state) => state.setActiveWorkspacePath)
  const upsertSession = useDesktopStore((state) => state.upsertSession)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [settingsTab, setSettingsTab] = useState<SettingsTabID>('agents')
  const [todoModal, setTodoModal] = useState<TodoModalState | null>(null)
  const [todoItems, setTodoItems] = useState<Record<string, WorkspaceTodoItem[]>>({})
  const [todoSummaries, setTodoSummaries] = useState<Record<string, WorkspaceTodoSummary>>({})
  const [swarmMenu, setSwarmMenu] = useState<SwarmTargetMenuState>({ open: false })
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
  const selectedWorkspacePath = useMemo<string | null>(() => {
    const routeSession = routeSessionId ? liveSessions[routeSessionId] ?? null : null
    if (routeSession?.workspacePath) {
      return routeSession.workspacePath
    }
    if (routeWorkspace?.path) {
      return routeWorkspace.path
    }
    return null
  }, [liveSessions, routeSessionId, routeWorkspace?.path])
  const selectedWorkspace = selectedWorkspacePath ? workspaceByPath.get(selectedWorkspacePath) ?? null : null
  const sidebarWorkspaceEntries = useMemo<WorkspaceEntry[]>(() => {
    if (!selectedWorkspacePath || selectedWorkspace) {
      return workspaces
    }
    return [{
      path: selectedWorkspacePath,
      workspaceName: routeWorkspace?.workspaceName || 'Temporary folder',
      themeId: '',
      directories: [selectedWorkspacePath],
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
    }, ...workspaces]
  }, [routeWorkspace?.workspaceName, selectedWorkspace, selectedWorkspacePath, workspaces])
  const mergedSidebarWorkspaceEntries = useMemo(() => sidebarWorkspaceEntries.map((workspace) => ({
    ...workspace,
    todoSummary: todoSummaries[workspace.path] ?? workspace.todoSummary,
  })), [sidebarWorkspaceEntries, todoSummaries])

  const overviewQuery = useQuery({
    ...workspaceOverviewQueryOptions([], 25),
    placeholderData: (previousData) => previousData,
  })
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

  const swarmTargets = swarmTargetsQuery.data?.targets ?? []
  const currentSwarmTarget = swarmTargets.find((target) => target.current) ?? null
  const swarmName = currentSwarmTarget?.name ?? swarmSettingsQuery.data?.name ?? 'Local'
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
  const swarmNotifications = useMemo(
    () => notifications.filter((notification) => notification.source === 'swarm'),
    [notifications],
  )
  const swarmNotificationCount = swarmNotifications.length
  const latestSwarmNotification = swarmNotifications[0] ?? null
  const swarmTargetSubtitle = currentSwarmTarget
    ? `${currentSwarmTarget.relationship}${currentSwarmTarget.online ? '' : ' · offline'}`
    : (latestSwarmNotification?.title ?? null)
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
        setTodoSummaries((current) => ({ ...current, [normalizedPath]: normalizeWorkspaceTodoSummary(agentResult.summary) }))
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
      }))
      await queryClient.invalidateQueries({ queryKey: ['workspace-overview'] })
      await queryClient.invalidateQueries({ queryKey: ['swarm-targets'] })
      void useDesktopStore.getState().hydrate()
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
          collapsed: existing?.collapsed ?? false,
          ratio: normalizeRatio(existing?.ratio),
        }
        next[workspace.path] = normalizedEntry
        if (!existing || existing.collapsed !== normalizedEntry.collapsed || normalizeRatio(existing.ratio) !== normalizedEntry.ratio) {
          changed = true
        }
      }

      return changed ? next : current
    })
  }, [mergedSidebarWorkspaceEntries])

  useEffect(() => {
    saveStoredValue(DESKTOP_SIDEBAR_LAYOUT_STORAGE_KEY, JSON.stringify(workspaceLayout))
  }, [workspaceLayout])

  useEffect(() => {
    if (vault.enabled) {
      setSettingsTab('vault')
    }
  }, [vault.enabled])

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

  const workspaceSessionOptions = useMemo<Map<string, { id: string; title: string; summary: string | null; isActive: boolean; updatedAt: number }[]>>(() => {
    const grouped = new Map<string, { id: string; title: string; summary: string | null; isActive: boolean; updatedAt: number }[]>()
    for (const workspace of mergedSidebarWorkspaceEntries) {
      const sessions = sessionsByWorkspace.get(workspace.path) ?? []
      grouped.set(
        workspace.path,
        sessions
          .filter((session) => session.id.trim() !== '')
          .map((session) => ({
            id: session.id,
            title: session.title.trim() || 'Untitled conversation',
            summary: session.live.summary?.trim() || session.metadata?.summary?.toString().trim() || null,
            isActive: activeSessionId === session.id,
            updatedAt: session.updatedAt,
          })),
      )
    }
    return grouped
  }, [activeSessionId, mergedSidebarWorkspaceEntries, sessionsByWorkspace])

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

  const visibleWorkspacePaths = useMemo<string[]>(() => mergedSidebarWorkspaceEntries.map((workspace) => workspace.path), [mergedSidebarWorkspaceEntries])

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
        collapsed: current[activeResize.topPath]?.collapsed ?? false,
        ratio: nextTopRatio,
      },
      [activeResize.bottomPath]: {
        collapsed: current[activeResize.bottomPath]?.collapsed ?? false,
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
    setSettingsTab(tab)
    setSettingsOpen(true)
  }, [])

  const handleOpenPermissions = useCallback(() => {
    setSettingsTab('permissions')
    setSettingsOpen(true)
  }, [])

  const handleOpenSwarmDashboardFromMenu = useCallback(() => {
    setSwarmMenu({ open: false })
    if (!routeWorkspaceSlug) {
      return
    }
    void navigate({
      to: '/$workspaceSlug/swarm',
      params: { workspaceSlug: routeWorkspaceSlug },
    })
  }, [navigate, routeWorkspaceSlug])

  const handleOpenSwarmDashboard = useCallback(() => {
    if (routeWorkspaceSlug) {
      void navigate({
        to: '/$workspaceSlug/swarm',
        params: { workspaceSlug: routeWorkspaceSlug },
      })
      return
    }
    void navigate({ to: '/swarm' })
  }, [navigate, routeWorkspaceSlug])

  const handleOpenWorkspaceLauncher = useCallback(() => {
    setActiveSession(null)
    void navigate({ to: '/' })
  }, [navigate, setActiveSession])

  const handlePrefetchSession = useCallback((sessionId: string) => {
    void prefetchSessionRuntimeData(queryClient, sessionId)
  }, [queryClient])

  const totalVisibleRatio = useMemo(
    () => mergedSidebarWorkspaceEntries.reduce((sum, workspace) => {
      const layout = workspaceLayout[workspace.path]
      if (layout?.collapsed) return sum
      return sum + normalizeRatio(layout?.ratio)
    }, 0) || mergedSidebarWorkspaceEntries.length || 1,
    [mergedSidebarWorkspaceEntries, workspaceLayout],
  )

  return (
    <div className="flex absolute inset-0 overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      <aside className={cn('flex flex-col border-r border-[var(--app-border)] bg-[var(--app-surface)] shrink-0', sidebarCollapsed ? 'w-[56px]' : 'w-[320px]')}>
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
            <div className="mt-2 flex flex-col items-center">
              <span className={cn('h-2.5 w-2.5 rounded-full', connectionDotClass(connectionState))} />
            </div>
          </div>
        ) : (
          <div className="flex h-full flex-col min-h-0">
            <div className="flex h-[60px] items-center border-b border-[var(--app-border)] px-4">
              <div className="flex min-w-0 flex-1 items-center justify-between gap-2">
                <div className="flex min-w-0 flex-1 items-center gap-3">
                  <span className={cn('h-2.5 w-2.5 rounded-full', connectionDotClass(connectionState))} />
                  <Button
                    variant="ghost"
                    className="h-12 min-w-0 flex-1 justify-start gap-2 px-3 text-left"
                    onClick={() => setSwarmMenu((current) => ({ open: !current.open }))}
                    aria-label="Choose swarm target"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm font-semibold">{swarmName}{swarmNotificationCount > 0 ? ` !${swarmNotificationCount}` : ''}</div>
                      {swarmTargetSubtitle ? <div className="truncate text-xs text-[var(--app-text-muted)]">{swarmTargetSubtitle}</div> : null}
                    </div>
                    <ChevronsUpDown size={16} className="shrink-0 text-[var(--app-text-subtle)]" />
                  </Button>
                </div>
                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    className="relative h-12 w-12 min-w-12 p-0"
                    onClick={handleOpenSwarmDashboard}
                    aria-label="Open swarm dashboard"
                    title={swarmNotificationCount > 0 ? `${swarmNotificationCount} swarm notification${swarmNotificationCount === 1 ? '' : 's'}` : 'No swarm notifications'}
                  >
                    <Bell size={22} className="shrink-0" />
                    {swarmNotificationCount > 0 ? (
                      <span className="absolute right-2 top-2 min-w-[18px] rounded-full bg-[var(--app-primary)] px-1 text-center text-[10px] font-semibold leading-[18px] text-white">
                        {swarmNotificationCount > 9 ? '9+' : swarmNotificationCount}
                      </span>
                    ) : null}
                  </Button>
                  <Button variant="ghost" className="h-12 w-12 min-w-12 p-0" onClick={() => setSidebarCollapsed(true)} aria-label="Collapse sidebar">
                    <ChevronLeft size={28} className="shrink-0" />
                  </Button>
                </div>
              </div>
            </div>
            {swarmMenu.open ? (
              <div className="border-b border-[var(--app-border)] px-3 py-2">
                <div className="space-y-1">
                  {swarmTargets.map((target) => (
                    <button
                      key={target.swarm_id}
                      type="button"
                      onClick={() => { void handleSelectSwarmTarget(target) }}
                      disabled={!target.selectable}
                      className={cn(
                        'flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-sm transition-colors',
                        target.current ? 'bg-[var(--app-primary)]/10 text-[var(--app-text)]' : 'hover:bg-[var(--app-surface-hover)]',
                        !target.selectable && 'cursor-not-allowed opacity-50',
                      )}
                    >
                      <div className="min-w-0">
                        <div className="truncate font-medium">{target.name}</div>
                        <div className="truncate text-xs text-[var(--app-text-muted)]">{target.relationship} · {target.role} · {target.online ? 'online' : (target.attach_status || 'offline')}</div>
                        {target.last_error ? <div className="truncate text-[11px] text-[var(--app-warning)]">{target.last_error}</div> : null}
                      </div>
                      {target.current ? <span className="text-xs font-semibold text-[var(--app-primary)]">current</span> : null}
                    </button>
                  ))}
                  {swarmSwitchError ? <div className="rounded-md border border-[var(--app-warning)]/40 bg-[var(--app-warning)]/10 px-3 py-2 text-xs text-[var(--app-warning)]">{swarmSwitchError}</div> : null}
                </div>
                <div className="mt-3 border-t border-[var(--app-border)] pt-3">
                  <Button
                    variant="outline"
                    className="h-10 w-full justify-center rounded-xl"
                    onClick={handleOpenSwarmDashboardFromMenu}
                    aria-label="Open swarm dashboard"
                  >
                    <Settings size={16} className="shrink-0" />
                    <span>Swarm dashboard</span>
                  </Button>
                </div>
              </div>
            ) : null}
            <div className="flex min-h-0 flex-1 flex-col">
              <div ref={sidebarBodyRef} className="flex min-h-0 flex-1 flex-col overflow-y-auto px-3 py-3">
                {mergedSidebarWorkspaceEntries.map((workspace, index) => {
                  const workspaceSessions = sessionsByWorkspace.get(workspace.path) ?? []
                  const sessionNodes = buildSidebarSessionTree(workspaceSessions, sidebarNow)
                  const flattenedSessionNodes = flattenSidebarSessionNodes(sessionNodes)
                  const layout = workspaceLayout[workspace.path]
                  const collapsed = layout?.collapsed ?? false
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
                        <div className="flex items-center justify-between gap-2 px-1 py-1">
                          <button
                            type="button"
                            onClick={() => {
                              toggleWorkspaceCollapse(workspace.path)
                            }}
                            className="flex min-w-0 flex-1 items-center gap-2 text-left transition-colors hover:text-[var(--app-text)]"
                            aria-label={`${collapsed ? 'Expand' : 'Collapse'} ${workspace.workspaceName}`}
                          >
                            {collapsed ? <ChevronRight size={16} className="shrink-0 text-[var(--app-text-subtle)]" /> : <ChevronDown size={16} className="shrink-0 text-[var(--app-text-subtle)]" />}
                            <div className="truncate text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">{workspace.workspaceName}</div>
                          </button>
                          <div className="flex items-center gap-1">
                            {!workspaceByPath.has(workspace.path) ? (
                              <Button
                                variant="ghost"
                                className="h-12 min-h-12 shrink-0 rounded-xl px-3 text-[var(--app-warning)] hover:bg-[var(--app-warning-bg)] hover:text-[var(--app-warning)]"
                                onClick={() => {
                                  void saveWorkspace({
                                    path: workspace.path,
                                    name: workspace.workspaceName,
                                    themeId: workspace.themeId,
                                    makeCurrent: true,
                                    linkedDirectories: [],
                                  })
                                }}
                                title="Save this temporary folder as a workspace"
                              >
                                ! Save
                              </Button>
                            ) : null}
                            {selectingPath === workspace.path ? <span className="text-xs text-[var(--app-text-muted)]">opening…</span> : null}
                            <Button variant="ghost" className="h-12 w-12 min-h-12 min-w-12 shrink-0 rounded-xl p-0 text-[var(--app-text-muted)] opacity-80 hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] hover:opacity-100" onClick={() => { openTodoModal(workspace.path, workspace.workspaceName) }} aria-label={`Open tasks for ${workspace.workspaceName}`} title={`${workspace.todoSummary?.taskCount ?? 0} tasks`}>
                              <ListChecks size={17} strokeWidth={2.25} className="shrink-0" />
                            </Button>
                            <Button variant="ghost" className="h-12 w-12 min-h-12 min-w-12 shrink-0 rounded-xl p-0 text-[var(--app-text-muted)] opacity-80 hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] hover:opacity-100" onClick={() => handleStartNewSessionInWorkspace(workspace.path, workspace.workspaceName)} aria-label={`New session in ${workspace.workspaceName}`} title="New session">
                              <Plus size={17} strokeWidth={2.25} className="shrink-0" />
                            </Button>
                          </div>
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
                                onSelect={handleSelectSession}
                                onPrefetch={handlePrefetchSession}
                              />
                            ))}
                          </div>
                        )}
                      </section>
                      {index < mergedSidebarWorkspaceEntries.length - 1 && !collapsed && !workspaceLayout[mergedSidebarWorkspaceEntries[index + 1].path]?.collapsed ? (
                        <div
                          role="separator"
                          aria-orientation="horizontal"
                          className="relative my-2 h-4 cursor-row-resize group px-1"
                          onPointerDown={(event) => handleResizeStart(event, workspace.path, mergedSidebarWorkspaceEntries[index + 1].path)}
                        >
                          <div className="absolute inset-x-1 top-1/2 h-px bg-[var(--app-border)] group-hover:bg-[var(--app-border-strong)] transition-colors" />
                        </div>
                      ) : index < mergedSidebarWorkspaceEntries.length - 1 ? (
                        <div className="h-2" />
                      ) : null}
                    </Fragment>
                  )
                })}
              </div>
              <div className="grid grid-cols-2 gap-2 border-t border-[var(--app-border)] px-4 py-3">
                <Button
                  variant="outline"
                  className="h-11 min-h-11 w-full justify-center rounded-xl"
                  onClick={handleOpenWorkspaceLauncher}
                  aria-label="Open workspaces"
                >
                  <Home size={18} className="shrink-0" />
                  <span>Workspaces</span>
                </Button>
                <Button
                  variant="outline"
                  className="h-11 min-h-11 w-full justify-center rounded-xl"
                  onClick={() => { setSettingsTab('agents'); setSettingsOpen(true) }}
                  aria-label="Open settings"
                >
                  <Settings size={18} className="shrink-0" />
                  <span>Settings</span>
                </Button>
              </div>
            </div>
          </div>
        )}
      </aside>

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
          agentSessions={workspaceSessionOptions.get(todoModal.workspacePath) ?? []}
          defaultAgentSessionId={activeSessionId ?? ''}
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
          agentSection={{
            ownerKind: 'agent',
            title: 'Agent Checklist',
            description: 'Execution checklist grouped by conversation and nested by parent/child steps.',
            emptyText: 'Drop agent checklist steps here',
            items: (todoItems[todoModal.workspacePath] ?? []).filter((item) => item.ownerKind === 'agent'),
            summary: {
              ...(todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()),
              taskCount: (todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()).agent.taskCount,
              openCount: (todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()).agent.openCount,
              inProgressCount: (todoSummaries[todoModal.workspacePath] ?? createEmptyWorkspaceTodoSummary()).agent.inProgressCount,
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
                ? setWorkspaceTodoInProgress(todoModal.workspacePath, item.id, item.ownerKind)
                : updateWorkspaceTodo({ workspacePath: todoModal.workspacePath, ownerKind: item.ownerKind, id: item.id, inProgress: false })),
            )
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: upsertWorkspaceTodoItem(current[todoModal.workspacePath] ?? [], result.item),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onUpdate={async (item, patch) => {
            const result = await mutateTodoState(todoModal.workspacePath, () => updateWorkspaceTodo({ workspacePath: todoModal.workspacePath, ownerKind: item.ownerKind, id: item.id, ...patch }))
            setTodoItems((current) => ({
              ...current,
              [todoModal.workspacePath]: upsertWorkspaceTodoItem(current[todoModal.workspacePath] ?? [], result.item),
            }))
            setTodoSummaries((current) => ({ ...current, [todoModal.workspacePath]: normalizeWorkspaceTodoSummary(result.summary) }))
          }}
          onDelete={async (item) => {
            const summary = await mutateTodoState(todoModal.workspacePath, () => deleteWorkspaceTodo(todoModal.workspacePath, item.id, item.ownerKind))
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

      <DesktopSettingsModal
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        onOpenSwarmDashboard={handleOpenSwarmDashboard}
        initialTab={settingsTab}
      />
    </div>
  )
}
