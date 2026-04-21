import test from 'node:test'
import assert from 'node:assert/strict'

import { formatAgentTodoBadge, metadataTodoSummary } from './desktop-chat-panel'

test('formatAgentTodoBadge shows progress-first badge with active count', () => {
  assert.equal(formatAgentTodoBadge({ taskCount: 6, openCount: 2, inProgressCount: 1 }), '4/6 complete • 1 active')
})

test('formatAgentTodoBadge shows complete state when no tasks remain open', () => {
  assert.equal(formatAgentTodoBadge({ taskCount: 6, openCount: 0, inProgressCount: 0 }), 'Complete · 6/6')
})

test('metadataTodoSummary reads agent-scoped counts from metadata', () => {
  assert.deepEqual(metadataTodoSummary({
    agent_todo_summary: {
      task_count: 5,
      open_count: 3,
      in_progress_count: 1,
      user: { task_count: 2, open_count: 1, in_progress_count: 0 },
      agent: { task_count: 3, open_count: 2, in_progress_count: 1 },
    },
  }), {
    taskCount: 3,
    openCount: 2,
    inProgressCount: 1,
  })
})
