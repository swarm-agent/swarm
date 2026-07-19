export type WorkspaceTodoPriority = 'low' | 'medium' | 'high' | 'urgent'

export type WorkspaceTodoOwnerKind = 'user' | 'agent'
export type WorkspaceTodoAIState = 'queued' | 'preparing' | 'in_progress' | 'failed'

export interface WorkspaceTodoOwnerSummary {
  taskCount: number
  openCount: number
  inProgressCount: number
}

export interface WorkspaceTodoItem {
  id: string
  workspacePath: string
  ownerKind: WorkspaceTodoOwnerKind
  text: string
  done: boolean
  priority: WorkspaceTodoPriority
  group: string
  tags: string[]
  inProgress: boolean
  sessionId: string
  parentId: string
  aiState: WorkspaceTodoAIState | ''
  aiMode: 'plan' | 'auto' | ''
  aiWorktree: boolean
  aiRequest: string
  aiError: string
  managedSessionId: string
  accountScopeId: string
  workspaceId: string
  originSessionId: string
  preparationSessionId: string
  preparationRunId: string
  preparationAttemptId: string
  finalRunId: string
  aiStateVersion: number
  sortIndex: number
  createdAt: number
  updatedAt: number
  completedAt: number
}

export interface WorkspaceTodoSummary {
  taskCount: number
  openCount: number
  inProgressCount: number
  user: WorkspaceTodoOwnerSummary
  agent: WorkspaceTodoOwnerSummary
}

interface WorkspaceTodoItemWire {
  id: string
  workspace_path: string
  owner_kind?: string
  text: string
  done: boolean
  priority?: string
  group?: string
  tags?: string[]
  in_progress?: boolean
  session_id?: string
  parent_id?: string
  ai_state?: string
  ai_mode?: string
  ai_worktree?: boolean
  ai_request?: string
  ai_error?: string
  managed_session_id?: string
  account_scope_id?: string
  workspace_id?: string
  origin_session_id?: string
  preparation_session_id?: string
  preparation_run_id?: string
  preparation_attempt_id?: string
  final_run_id?: string
  ai_state_version?: number
  sort_index: number
  created_at: number
  updated_at: number
  completed_at?: number
}

export interface WorkspaceTodoSummaryWire {
  task_count?: number
  open_count?: number
  in_progress_count?: number
  user?: {
    task_count?: number
    open_count?: number
    in_progress_count?: number
  }
  agent?: {
    task_count?: number
    open_count?: number
    in_progress_count?: number
  }
}

export function createEmptyWorkspaceTodoOwnerSummary(): WorkspaceTodoOwnerSummary {
  return { taskCount: 0, openCount: 0, inProgressCount: 0 }
}

export function createEmptyWorkspaceTodoSummary(): WorkspaceTodoSummary {
  return {
    taskCount: 0,
    openCount: 0,
    inProgressCount: 0,
    user: createEmptyWorkspaceTodoOwnerSummary(),
    agent: createEmptyWorkspaceTodoOwnerSummary(),
  }
}

function normalizePriority(value: string | undefined): WorkspaceTodoPriority {
  switch ((value ?? '').trim().toLowerCase()) {
    case 'low':
    case 'high':
    case 'urgent':
      return value!.trim().toLowerCase() as WorkspaceTodoPriority
    default:
      return 'medium'
  }
}

function normalizeOwnerKind(value: string | undefined): WorkspaceTodoOwnerKind {
  return value?.trim().toLowerCase() === 'agent' ? 'agent' : 'user'
}

function normalizeAIState(value: string | undefined): WorkspaceTodoAIState | '' {
  const state = value?.trim().toLowerCase()
  return state === 'queued' || state === 'preparing' || state === 'in_progress' || state === 'failed' ? state : ''
}

function mapWorkspaceTodoOwnerSummary(summary: WorkspaceTodoSummaryWire['user'] | undefined): WorkspaceTodoOwnerSummary {
  return {
    taskCount: typeof summary?.task_count === 'number' ? summary.task_count : 0,
    openCount: typeof summary?.open_count === 'number' ? summary.open_count : 0,
    inProgressCount: typeof summary?.in_progress_count === 'number' ? summary.in_progress_count : 0,
  }
}

export function mapWorkspaceTodoItem(item: WorkspaceTodoItemWire): WorkspaceTodoItem {
  return {
    id: item.id,
    workspacePath: item.workspace_path,
    ownerKind: normalizeOwnerKind(item.owner_kind),
    text: item.text,
    done: Boolean(item.done),
    priority: normalizePriority(item.priority),
    group: item.group?.trim() ?? '',
    tags: Array.isArray(item.tags) ? item.tags.map((tag) => tag.trim()).filter((tag) => tag !== '') : [],
    inProgress: Boolean(item.in_progress),
    sessionId: (item.session_id ?? '').trim(),
    parentId: (item.parent_id ?? '').trim(),
    aiState: normalizeAIState(item.ai_state),
    aiMode: item.ai_mode?.trim().toLowerCase() === 'plan' ? 'plan' : item.ai_mode?.trim().toLowerCase() === 'auto' ? 'auto' : '',
    aiWorktree: Boolean(item.ai_worktree),
    aiRequest: (item.ai_request ?? '').trim(),
    aiError: (item.ai_error ?? '').trim(),
    managedSessionId: (item.managed_session_id ?? '').trim(),
    accountScopeId: (item.account_scope_id ?? '').trim(),
    workspaceId: (item.workspace_id ?? '').trim(),
    originSessionId: (item.origin_session_id ?? '').trim(),
    preparationSessionId: (item.preparation_session_id ?? '').trim(),
    preparationRunId: (item.preparation_run_id ?? '').trim(),
    preparationAttemptId: (item.preparation_attempt_id ?? '').trim(),
    finalRunId: (item.final_run_id ?? '').trim(),
    aiStateVersion: typeof item.ai_state_version === 'number' ? item.ai_state_version : 0,
    sortIndex: item.sort_index,
    createdAt: item.created_at,
    updatedAt: item.updated_at,
    completedAt: typeof item.completed_at === 'number' ? item.completed_at : 0,
  }
}

export function mapWorkspaceTodoSummary(summary: WorkspaceTodoSummaryWire | undefined): WorkspaceTodoSummary {
  if (!summary) {
    return createEmptyWorkspaceTodoSummary()
  }
  const user = mapWorkspaceTodoOwnerSummary(summary.user)
  const agent = mapWorkspaceTodoOwnerSummary(summary.agent)
  return {
    taskCount: user.taskCount,
    openCount: user.openCount,
    inProgressCount: user.inProgressCount,
    user,
    agent,
  }
}

function workspaceTodosUnavailable(): never {
  throw new Error('Workspace todos and /task are temporarily unavailable because the blocking workspace todo API was removed.')
}

export async function fetchWorkspaceTodos(_workspacePath: string, _ownerKind?: WorkspaceTodoOwnerKind, _sessionId?: string, _signal?: AbortSignal): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  return workspaceTodosUnavailable()
}

export async function createWorkspaceAITask(workspacePath: string, request: string, idempotencyKey: string, originSessionId?: string): Promise<{ item: WorkspaceTodoItem; summary: WorkspaceTodoSummary; status: string }> {
  const normalizedRequest = request.trim()
  if (!normalizedRequest) {
    throw new Error('Enter a task request after /task.')
  }
  if (!idempotencyKey.trim()) {
    throw new Error('AI task idempotency key is required')
  }
  void workspacePath
  void originSessionId
  return workspaceTodosUnavailable()
}

export async function createWorkspaceTodo(input: {
  workspacePath: string
  ownerKind?: WorkspaceTodoOwnerKind
  text: string
  priority?: WorkspaceTodoPriority
  group?: string
  tags?: string[]
  inProgress?: boolean
  sessionId?: string
  parentId?: string
}): Promise<{ item: WorkspaceTodoItem; summary: WorkspaceTodoSummary }> {
  void input
  return workspaceTodosUnavailable()
}

export async function updateWorkspaceTodo(input: {
  workspacePath: string
  ownerKind?: WorkspaceTodoOwnerKind
  id: string
  text?: string
  done?: boolean
  priority?: WorkspaceTodoPriority
  group?: string
  tags?: string[]
  inProgress?: boolean
  sessionId?: string
  parentId?: string
}): Promise<{ item: WorkspaceTodoItem; summary: WorkspaceTodoSummary }> {
  void input
  return workspaceTodosUnavailable()
}

export async function deleteWorkspaceTodo(workspacePath: string, id: string, ownerKind?: WorkspaceTodoOwnerKind, sessionId?: string): Promise<WorkspaceTodoSummary> {
  void workspacePath
  void id
  void ownerKind
  void sessionId
  return workspaceTodosUnavailable()
}

export async function deleteDoneWorkspaceTodos(workspacePath: string, ownerKind?: WorkspaceTodoOwnerKind, sessionId?: string): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  void workspacePath
  void ownerKind
  void sessionId
  return workspaceTodosUnavailable()
}

export async function deleteAllWorkspaceTodos(workspacePath: string, ownerKind?: WorkspaceTodoOwnerKind, sessionId?: string): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  void workspacePath
  void ownerKind
  void sessionId
  return workspaceTodosUnavailable()
}

export async function reorderWorkspaceTodos(workspacePath: string, orderedIDs: string[], ownerKind?: WorkspaceTodoOwnerKind, sessionId?: string): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  void workspacePath
  void orderedIDs
  void ownerKind
  void sessionId
  return workspaceTodosUnavailable()
}

export async function setWorkspaceTodoInProgress(workspacePath: string, id: string, ownerKind?: WorkspaceTodoOwnerKind, sessionId?: string): Promise<{ item: WorkspaceTodoItem; summary: WorkspaceTodoSummary }> {
  void workspacePath
  void id
  void ownerKind
  void sessionId
  return workspaceTodosUnavailable()
}
