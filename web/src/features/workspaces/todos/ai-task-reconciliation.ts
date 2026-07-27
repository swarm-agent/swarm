import type { WorkspaceTodoItem } from './types'

const AI_TASK_STATE_RANK: Record<string, number> = {
  queued: 1,
  preparing: 2,
  in_progress: 3,
  completed: 4,
  failed: 4,
  cancelled: 4,
}

export function isWorkspaceAITaskActive(item: WorkspaceTodoItem): boolean {
  return item.aiState === 'queued' || item.aiState === 'preparing' || item.aiState === 'in_progress'
}

export function mergeWorkspaceAITaskMonotonic(current: WorkspaceTodoItem | undefined, incoming: WorkspaceTodoItem): WorkspaceTodoItem {
  if (!current || current.id !== incoming.id) return incoming

  const currentRank = AI_TASK_STATE_RANK[current.aiState] ?? 0
  const incomingRank = AI_TASK_STATE_RANK[incoming.aiState] ?? 0
  const incomingIsOlder = incoming.aiStateVersion < current.aiStateVersion
    || (incoming.aiStateVersion === current.aiStateVersion && incoming.updatedAt < current.updatedAt)
    || incomingRank < currentRank
  if (incomingIsOlder) return current

  return {
    ...incoming,
    priority: current.priority !== 'medium' && incoming.priority === 'medium' ? current.priority : incoming.priority,
    group: incoming.group || current.group,
    tags: incoming.tags.length > 0 ? incoming.tags : current.tags,
    sessionId: incoming.sessionId || current.sessionId,
    parentId: incoming.parentId || current.parentId,
    aiMode: incoming.aiMode || current.aiMode,
    aiWorktree: incoming.aiWorktree || current.aiWorktree,
    aiRequest: incoming.aiRequest || current.aiRequest,
    accountScopeId: incoming.accountScopeId || current.accountScopeId,
    workspaceId: incoming.workspaceId || current.workspaceId,
    originSessionId: incoming.originSessionId || current.originSessionId,
    managedSessionId: incoming.managedSessionId || current.managedSessionId,
    preparationSessionId: incoming.preparationSessionId || current.preparationSessionId,
    preparationRunId: incoming.preparationRunId || current.preparationRunId,
    preparationAttemptId: incoming.preparationAttemptId || current.preparationAttemptId,
    finalRunId: incoming.finalRunId || current.finalRunId,
    aiDisplayTitle: incoming.aiDisplayTitle || current.aiDisplayTitle,
    aiResult: incoming.aiResult || current.aiResult,
    aiError: incoming.aiError || current.aiError,
  }
}

export function mergeWorkspaceTodoListsMonotonic(current: WorkspaceTodoItem[], incoming: WorkspaceTodoItem[]): WorkspaceTodoItem[] {
  const currentByID = new Map(current.map((item) => [item.id, item] as const))
  return incoming.map((item) => {
    const previous = currentByID.get(item.id)
    return previous?.aiState || item.aiState ? mergeWorkspaceAITaskMonotonic(previous, item) : item
  })
}
