import { requestJson } from '../../../app/api'

export type WorkspaceTodoPriority = 'low' | 'medium' | 'high' | 'urgent'

export type WorkspaceTodoOwnerKind = 'user' | 'agent'

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

interface WorkspaceTodosResponseWire {
  ok: boolean
  workspace_path: string
  items: WorkspaceTodoItemWire[]
  summary: WorkspaceTodoSummaryWire
}

interface WorkspaceTodoMutationResponseWire {
  ok: boolean
  item?: WorkspaceTodoItemWire
  items?: WorkspaceTodoItemWire[]
  id?: string
  summary: WorkspaceTodoSummaryWire
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
  return {
    taskCount: typeof summary.task_count === 'number' ? summary.task_count : 0,
    openCount: typeof summary.open_count === 'number' ? summary.open_count : 0,
    inProgressCount: typeof summary.in_progress_count === 'number' ? summary.in_progress_count : 0,
    user: mapWorkspaceTodoOwnerSummary(summary.user),
    agent: mapWorkspaceTodoOwnerSummary(summary.agent),
  }
}

export async function fetchWorkspaceTodos(workspacePath: string, ownerKind?: WorkspaceTodoOwnerKind): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  const search = new URLSearchParams({ workspace_path: workspacePath })
  if (ownerKind) {
    search.set('owner_kind', ownerKind)
  }
  const response = await requestJson<WorkspaceTodosResponseWire>(`/v1/workspace/todos?${search.toString()}`)
  return {
    items: Array.isArray(response.items) ? response.items.map(mapWorkspaceTodoItem) : [],
    summary: mapWorkspaceTodoSummary(response.summary),
  }
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
  const response = await requestJson<WorkspaceTodoMutationResponseWire>('/v1/workspace/todos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'create',
      workspace_path: input.workspacePath,
      owner_kind: input.ownerKind,
      text: input.text,
      priority: input.priority ?? 'medium',
      group: input.group ?? '',
      tags: input.tags ?? [],
      in_progress: Boolean(input.inProgress),
      session_id: input.sessionId,
      parent_id: input.parentId,
    }),
  })
  if (!response.item) {
    throw new Error('todo create returned no item')
  }
  return { item: mapWorkspaceTodoItem(response.item), summary: mapWorkspaceTodoSummary(response.summary) }
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
  const response = await requestJson<WorkspaceTodoMutationResponseWire>('/v1/workspace/todos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'update',
      workspace_path: input.workspacePath,
      id: input.id,
      owner_kind: input.ownerKind,
      text: input.text,
      done: input.done,
      priority: input.priority,
      group: input.group,
      tags: input.tags,
      in_progress: input.inProgress,
      session_id: input.sessionId,
      parent_id: input.parentId,
    }),
  })
  if (!response.item) {
    throw new Error('todo update returned no item')
  }
  return { item: mapWorkspaceTodoItem(response.item), summary: mapWorkspaceTodoSummary(response.summary) }
}

export async function deleteWorkspaceTodo(workspacePath: string, id: string, ownerKind?: WorkspaceTodoOwnerKind): Promise<WorkspaceTodoSummary> {
  const response = await requestJson<WorkspaceTodoMutationResponseWire>('/v1/workspace/todos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'delete', workspace_path: workspacePath, owner_kind: ownerKind, id }),
  })
  return mapWorkspaceTodoSummary(response.summary)
}

export async function deleteDoneWorkspaceTodos(workspacePath: string, ownerKind?: WorkspaceTodoOwnerKind): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  const response = await requestJson<WorkspaceTodoMutationResponseWire>('/v1/workspace/todos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'delete_done', workspace_path: workspacePath, owner_kind: ownerKind }),
  })
  return {
    items: Array.isArray(response.items) ? response.items.map(mapWorkspaceTodoItem) : [],
    summary: mapWorkspaceTodoSummary(response.summary),
  }
}

export async function deleteAllWorkspaceTodos(workspacePath: string, ownerKind?: WorkspaceTodoOwnerKind): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  const response = await requestJson<WorkspaceTodoMutationResponseWire>('/v1/workspace/todos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'delete_all', workspace_path: workspacePath, owner_kind: ownerKind }),
  })
  return {
    items: Array.isArray(response.items) ? response.items.map(mapWorkspaceTodoItem) : [],
    summary: mapWorkspaceTodoSummary(response.summary),
  }
}

export async function reorderWorkspaceTodos(workspacePath: string, orderedIDs: string[], ownerKind?: WorkspaceTodoOwnerKind): Promise<{ items: WorkspaceTodoItem[]; summary: WorkspaceTodoSummary }> {
  const response = await requestJson<WorkspaceTodoMutationResponseWire>('/v1/workspace/todos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'reorder', workspace_path: workspacePath, owner_kind: ownerKind, ordered_ids: orderedIDs }),
  })
  return {
    items: Array.isArray(response.items) ? response.items.map(mapWorkspaceTodoItem) : [],
    summary: mapWorkspaceTodoSummary(response.summary),
  }
}

export async function setWorkspaceTodoInProgress(workspacePath: string, id: string, ownerKind?: WorkspaceTodoOwnerKind): Promise<{ item: WorkspaceTodoItem; summary: WorkspaceTodoSummary }> {
  const response = await requestJson<WorkspaceTodoMutationResponseWire>('/v1/workspace/todos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'in_progress', workspace_path: workspacePath, owner_kind: ownerKind, id }),
  })
  if (!response.item) {
    throw new Error('todo in-progress returned no item')
  }
  return { item: mapWorkspaceTodoItem(response.item), summary: mapWorkspaceTodoSummary(response.summary) }
}

