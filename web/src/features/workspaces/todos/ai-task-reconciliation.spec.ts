import assert from 'node:assert/strict'
import test from 'node:test'
import { mergeWorkspaceAITaskMonotonic } from './ai-task-reconciliation'
import type { WorkspaceTodoItem } from './types'

function task(patch: Partial<WorkspaceTodoItem> = {}): WorkspaceTodoItem {
  return {
    id: 'task-1', workspacePath: '/workspace', ownerKind: 'user', text: 'ship it', done: false,
    priority: 'medium', group: '', tags: [], inProgress: false, sessionId: '', parentId: '',
    aiState: 'queued', aiMode: '', aiWorktree: false, aiRequest: 'ship it', aiError: '', aiDisplayTitle: '', aiResult: '',
    managedSessionId: '', accountScopeId: 'acct-1', workspaceId: 'workspace-1', originSessionId: '',
    preparationSessionId: '', preparationRunId: '', preparationAttemptId: '', finalRunId: '',
    aiStateVersion: 1, sortIndex: 0, createdAt: 1, updatedAt: 1, completedAt: 0,
    ...patch,
  }
}

test('stale AI task responses cannot regress state or diagnostic linkage', () => {
  const current = task({
    aiState: 'in_progress', aiStateVersion: 3, updatedAt: 30,
    managedSessionId: 'managed-1', preparationSessionId: 'prep-1', preparationRunId: 'prep-run-1',
    preparationAttemptId: 'attempt-1', finalRunId: 'final-run-1', aiError: 'durable detail',
  })
  const stale = task({ aiState: 'preparing', aiStateVersion: 2, updatedAt: 20 })
  assert.deepEqual(mergeWorkspaceAITaskMonotonic(current, stale), current)
})

test('newer AI task responses preserve nonempty correlation fields', () => {
  const current = task({ aiState: 'preparing', aiStateVersion: 2, preparationSessionId: 'prep-1' })
  const incoming = task({ aiState: 'in_progress', aiStateVersion: 3, updatedAt: 30, managedSessionId: 'managed-1' })
  const merged = mergeWorkspaceAITaskMonotonic(current, incoming)
  assert.equal(merged.aiState, 'in_progress')
  assert.equal(merged.managedSessionId, 'managed-1')
  assert.equal(merged.preparationSessionId, 'prep-1')
})
