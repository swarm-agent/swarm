import {
  mapWorkspaceTodoSummary,
  type WorkspaceTodoSummary,
  type WorkspaceTodoSummaryWire,
} from '../../todos/types'
import type { DesktopPermissionRecord, DesktopSessionRecord } from '../../../desktop/types/realtime'
import {
  canonicalSessionWorkspaceName,
  canonicalSessionWorkspacePath,
} from '../../../desktop/services/session-workspace'
import {
  mapWorkspaceDiscoverEntry,
  mapWorkspaceEntry,
  mapWorkspaceResolution,
  type WorkspaceDiscoverEntry,
  type WorkspaceDiscoverEntryWire,
  type WorkspaceEntry,
  type WorkspaceEntryWire,
  type WorkspaceResolution,
  type WorkspaceResolutionWire,
} from './workspace'

export interface WorkspaceOverviewPermissionWire {
  id?: string
  session_id?: string
  run_id?: string
  call_id?: string
  tool_name?: string
  tool_arguments?: string
  status?: string
  decision?: string
  reason?: string
  requirement?: string
  mode?: string
  created_at?: number
  updated_at?: number
  resolved_at?: number
  permission_requested_at?: number
}

export interface WorkspaceOverviewActiveRunWire {
  run_id?: string
  status?: string
  last_seq?: number
}

export interface WorkspaceOverviewLifecycleWire {
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
}

export interface WorkspaceOverviewSessionWire {
  id?: string
  title?: string
  workspace_path?: string
  workspace_name?: string
  mode?: string
  metadata?: Record<string, unknown>
  message_count?: number
  updated_at?: number
  created_at?: number
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
  pending_permissions?: WorkspaceOverviewPermissionWire[]
  pending_permission_count?: number
  active_run?: WorkspaceOverviewActiveRunWire | null
  session_status?: string
  lifecycle?: WorkspaceOverviewLifecycleWire | null
}

export interface WorkspaceOverviewWorkspaceWire extends WorkspaceEntryWire {
  sessions?: WorkspaceOverviewSessionWire[]
  todo_summary?: WorkspaceTodoSummaryWire
}

export interface WorkspaceOverviewSwarmTargetWire {
  swarm_id?: string
  name?: string
  role?: string
  relationship?: string
  kind?: string
  current?: boolean
}

export interface WorkspaceOverviewResponseWire {
  ok?: boolean
  current_workspace?: WorkspaceResolutionWire | null
  workspaces?: WorkspaceOverviewWorkspaceWire[]
  directories?: WorkspaceDiscoverEntryWire[]
  swarm_target?: WorkspaceOverviewSwarmTargetWire | null
}

export interface WorkspaceOverviewWorkspace extends WorkspaceEntry {
  sessions: DesktopSessionRecord[]
  todoSummary?: WorkspaceTodoSummary
}

export interface WorkspaceOverviewSwarmTarget {
  swarmId: string
  name: string
  role: string
  relationship: string
  kind: string
  current: boolean
}

export interface WorkspaceOverviewResponse {
  ok: boolean
  currentWorkspace: WorkspaceResolution | null
  workspaces: WorkspaceOverviewWorkspace[]
  discovered: WorkspaceDiscoverEntry[]
  swarmTarget: WorkspaceOverviewSwarmTarget | null
}

function normalizeSessionStatus(status: string): DesktopSessionRecord['live']['status'] {
  switch (status.trim().toLowerCase()) {
    case 'starting':
    case 'running':
    case 'blocked':
    case 'error':
      return status.trim().toLowerCase() as DesktopSessionRecord['live']['status']
    default:
      return 'idle'
  }
}

function mapOverviewSwarmTarget(target: WorkspaceOverviewSwarmTargetWire | null | undefined): WorkspaceOverviewSwarmTarget | null {
  if (!target || typeof target !== 'object') {
    return null
  }
  const swarmId = String(target.swarm_id ?? '').trim()
  return {
    swarmId,
    name: String(target.name ?? '').trim(),
    role: String(target.role ?? '').trim(),
    relationship: String(target.relationship ?? '').trim(),
    kind: String(target.kind ?? '').trim(),
    current: Boolean(target.current),
  }
}

function overviewPrefersRuntimeWorkspacePaths(response: WorkspaceOverviewResponseWire): boolean {
  const target = mapOverviewSwarmTarget(response.swarm_target)
  if (!target) {
    return false
  }
  if (target.kind === 'self') {
    return false
  }
  const relationship = target.relationship.trim().toLowerCase()
  return relationship !== '' && relationship !== 'self'
}

function mapOverviewPermission(permission: WorkspaceOverviewPermissionWire): DesktopPermissionRecord {
  return {
    id: String(permission.id ?? '').trim(),
    sessionId: String(permission.session_id ?? '').trim(),
    runId: String(permission.run_id ?? '').trim(),
    callId: String(permission.call_id ?? '').trim(),
    toolName: String(permission.tool_name ?? '').trim(),
    toolArguments: String(permission.tool_arguments ?? '').trim(),
    status: String(permission.status ?? '').trim(),
    decision: String(permission.decision ?? '').trim(),
    reason: String(permission.reason ?? '').trim(),
    requirement: String(permission.requirement ?? '').trim(),
    mode: String(permission.mode ?? '').trim(),
    createdAt: typeof permission.created_at === 'number' ? permission.created_at : 0,
    updatedAt: typeof permission.updated_at === 'number' ? permission.updated_at : 0,
    resolvedAt: typeof permission.resolved_at === 'number' ? permission.resolved_at : 0,
    permissionRequestedAt: typeof permission.permission_requested_at === 'number' ? permission.permission_requested_at : 0,
  }
}

function mapOverviewSession(session: WorkspaceOverviewSessionWire, preferRuntimeWorkspacePath = false): DesktopSessionRecord {
  const pendingPermissions = Array.isArray(session.pending_permissions)
    ? session.pending_permissions.map(mapOverviewPermission).filter((entry) => entry.id && entry.sessionId)
    : []
  const activeRun = session.active_run && typeof session.active_run === 'object' ? session.active_run : null
  const activeRunID = String(activeRun?.run_id ?? '').trim()
  const sessionStatus = normalizeSessionStatus(String(session.session_status ?? ''))
  const lifecycle = session.lifecycle && typeof session.lifecycle === 'object'
    ? {
        sessionId: String(session.lifecycle.session_id ?? session.id ?? '').trim(),
        runId: String(session.lifecycle.run_id ?? '').trim() || null,
        active: Boolean(session.lifecycle.active),
        phase: String(session.lifecycle.phase ?? '').trim(),
        startedAt: typeof session.lifecycle.started_at === 'number' ? session.lifecycle.started_at : 0,
        endedAt: typeof session.lifecycle.ended_at === 'number' ? session.lifecycle.ended_at : 0,
        updatedAt: typeof session.lifecycle.updated_at === 'number' ? session.lifecycle.updated_at : 0,
        generation: typeof session.lifecycle.generation === 'number' ? session.lifecycle.generation : 0,
        stopReason: String(session.lifecycle.stop_reason ?? '').trim() || null,
        error: String(session.lifecycle.error ?? '').trim() || null,
        ownerTransport: String(session.lifecycle.owner_transport ?? '').trim() || null,
      }
    : null
  const activeRunId = lifecycle?.active
    ? lifecycle.runId
    : ['starting', 'running', 'blocked'].includes(sessionStatus)
      ? (activeRunID || null)
      : null
  const lifecycleStatus = lifecycle?.active
    ? normalizeSessionStatus(lifecycle.phase || 'running')
    : lifecycle?.phase === 'errored'
      ? 'error'
      : sessionStatus
  const metadata = session.metadata && typeof session.metadata === 'object'
    ? session.metadata as Record<string, unknown>
    : undefined
  const workspacePath = String(session.workspace_path ?? '').trim()
  const hostedHostWorkspacePath = typeof metadata?.swarm_routed_host_workspace_path === 'string'
    ? metadata.swarm_routed_host_workspace_path.trim()
    : ''
  const hostedRuntimeWorkspacePath = typeof metadata?.swarm_routed_runtime_workspace_path === 'string'
    ? metadata.swarm_routed_runtime_workspace_path.trim()
    : ''
  const worktreeEnabled = Boolean(session.worktree_enabled)
  const worktreeRootPath = String(session.worktree_root_path ?? '').trim()
  const canonicalWorkspacePath = canonicalSessionWorkspacePath({
    workspacePath,
    hostedHostWorkspacePath,
    hostedRuntimeWorkspacePath,
    preferHostedRuntimeWorkspacePath: preferRuntimeWorkspacePath,
    worktreeEnabled,
    worktreeRootPath,
  })

  return {
    id: String(session.id ?? '').trim(),
    title: String(session.title ?? '').trim(),
    workspacePath: canonicalWorkspacePath,
    workspaceName: canonicalSessionWorkspaceName(String(session.workspace_name ?? ''), workspacePath, canonicalWorkspacePath),
    mode: String(session.mode ?? 'plan').trim() || 'plan',
    metadata,
    messageCount: typeof session.message_count === 'number' ? session.message_count : 0,
    updatedAt: typeof session.updated_at === 'number' ? session.updated_at : 0,
    createdAt: typeof session.created_at === 'number' ? session.created_at : 0,
    permissionsHydrated: false,
    runtimeWorkspacePath: hostedRuntimeWorkspacePath || workspacePath,
    worktreeEnabled,
    worktreeRootPath,
    worktreeBaseBranch: String(session.worktree_base_branch ?? '').trim(),
    worktreeBranch: String(session.worktree_branch ?? '').trim(),
    gitBranch: String(session.git_branch ?? '').trim(),
    gitHasGit: Boolean(session.git_has_git),
    gitClean: Boolean(session.git_clean),
    gitDirtyCount: typeof session.git_dirty_count === 'number' ? session.git_dirty_count : 0,
    gitStagedCount: typeof session.git_staged_count === 'number' ? session.git_staged_count : 0,
    gitModifiedCount: typeof session.git_modified_count === 'number' ? session.git_modified_count : 0,
    gitUntrackedCount: typeof session.git_untracked_count === 'number' ? session.git_untracked_count : 0,
    gitConflictCount: typeof session.git_conflict_count === 'number' ? session.git_conflict_count : 0,
    gitAheadCount: typeof session.git_ahead_count === 'number' ? session.git_ahead_count : 0,
    gitBehindCount: typeof session.git_behind_count === 'number' ? session.git_behind_count : 0,
    gitCommitDetected: Boolean(session.git_commit_detected),
    gitCommitCount: typeof session.git_commit_count === 'number' ? session.git_commit_count : 0,
    gitCommittedFileCount: typeof session.git_committed_file_count === 'number' ? session.git_committed_file_count : 0,
    gitCommittedAdditions: typeof session.git_committed_additions === 'number' ? session.git_committed_additions : 0,
    gitCommittedDeletions: typeof session.git_committed_deletions === 'number' ? session.git_committed_deletions : 0,
    lifecycle,
    live: {
      runId: activeRunId,
      agentName: null,
      startedAt: lifecycle?.active && lifecycle.startedAt > 0 ? lifecycle.startedAt : null,
      status: lifecycleStatus,
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
      lastEventAt: lifecycle?.updatedAt ? lifecycle.updatedAt : null,
      error: lifecycle?.error ?? null,
      seq: typeof activeRun?.last_seq === 'number' ? activeRun.last_seq : 0,
      assistantDraft: '',
      retainedAssistantSegments: [],
      reasoningSummary: '',
      reasoningText: '',
      reasoningState: 'idle',
      reasoningSegment: 0,
      reasoningStartedAt: null,
      awaitingAck: false,
    },
    pendingPermissions,
    pendingPermissionCount: typeof session.pending_permission_count === 'number'
      ? session.pending_permission_count
      : pendingPermissions.length,
    usage: null,
  }
}

function mapOverviewWorkspace(workspace: WorkspaceOverviewWorkspaceWire, preferRuntimeWorkspacePath: boolean): WorkspaceOverviewWorkspace {
  return {
    ...mapWorkspaceEntry(workspace),
    sessions: Array.isArray(workspace.sessions)
      ? workspace.sessions.map((session) => mapOverviewSession(session, preferRuntimeWorkspacePath)).filter((session) => session.id)
      : [],
    todoSummary: mapWorkspaceTodoSummary(workspace.todo_summary),
  }
}

export function mapWorkspaceOverviewResponse(response: WorkspaceOverviewResponseWire): WorkspaceOverviewResponse {
  const swarmTarget = mapOverviewSwarmTarget(response.swarm_target)
  const preferRuntimeWorkspacePath = overviewPrefersRuntimeWorkspacePaths(response)
  return {
    ok: Boolean(response.ok),
    currentWorkspace: response.current_workspace ? mapWorkspaceResolution(response.current_workspace) : null,
    workspaces: Array.isArray(response.workspaces) ? response.workspaces.map((workspace) => mapOverviewWorkspace(workspace, preferRuntimeWorkspacePath)) : [],
    discovered: Array.isArray(response.directories) ? response.directories.map(mapWorkspaceDiscoverEntry) : [],
    swarmTarget,
  }
}
