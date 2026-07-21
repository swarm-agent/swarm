import assert from 'node:assert/strict'
import test from 'node:test'

import { createEmptyDesktopV3CacheState as createInitialDesktopV3CacheState } from './desktop-v3-cache-reducer'
import { DESKTOP_V3_TASK_TOOL_ACTIVITY_CALL_LIMIT, DESKTOP_V3_TASK_TOOL_ACTIVITY_GROUP_LIMIT, selectDesktopV3TaskChildViewModel, summarizeDesktopV3TaskToolActivity } from './desktop-v3-cache-selectors'
import type { TaskToolRow } from '../chat/types/chat'

const row: TaskToolRow = {
  launchIndex: 1,
  childSessionId: 'child-1',
  status: 'pending',
  phase: 'spawned',
  agent: 'explorer',
  assignmentLabel: 'Inspect state',
  modelLabel: '',
  tool: '-',
  time: '',
  previewKind: 'assistant',
  previewText: 'private assistant text',
  launchStartedAtMs: 10,
  currentToolStartedAtMs: 0,
  elapsedMs: 0,
  currentToolMs: 0,
  terminal: false,
}

test('task child view joins canonical child run, usage, target, and bounded tool state', () => {
  const state = createInitialDesktopV3CacheState()
  state.sessionsById['child-1'] = {
    kind: 'full',
    needsHydrate: false,
    session: {
      id: 'child-1', workspace_path: '/workspace', workspace_name: 'Workspace', title: 'Child', mode: 'auto',
      metadata: { swarm_v3_runtime_swarm_id: 'swarm-local' }, created_at: 1, updated_at: 2, message_count: 0, last_message_at: 0,
    },
  }
  state.sessionViewsById['child-1'] = { agentic_settings: { mode: 'auto', agent_name: 'explorer', resolved_agent_name: 'explorer', context_window: 1000 } }
  state.currentRunIntentBySession['child-1'] = {
    session_id: 'child-1', run_id: 'run-1', status: 'running', created_at: 10, started_at: 20, updated_at: 30, event_seq: 2,
  }
  state.usageBySession['child-1'] = { context_window: 1000, remaining_tokens: 250, model: 'model-x', updated_at: 40 }
  state.preferencesBySession['child-1'] = { model: 'model-x' }
  state.liveRunsBySession['child-1'] = {
    'run-1': {
      sessionId: 'child-1', runId: 'run-1', status: 'running', toolCallsByCallId: {
        c1: { callId: 'c1', toolName: 'search', status: 'running', updatedAt: 50 },
      },
    },
  }

  assert.deepEqual(selectDesktopV3TaskChildViewModel(state, row), {
    sessionId: 'child-1', hydrated: true, loading: false, unavailable: false, stale: false, terminal: false,
    status: 'running', runId: 'run-1', currentTool: 'search', toolActivitySummary: 'search', startedAt: 20, elapsedMs: 0,
    modelLabel: 'model-x', contextWindow: 1000, remainingTokens: 250, contextUpdatedAt: 40,
    workspacePath: '/workspace', workspaceName: 'Workspace', targetSwarmId: 'swarm-local', error: '',
  })
})

test('task child view groups repeated tools, retains completed calls, and prioritizes active tools', () => {
  const summary = summarizeDesktopV3TaskToolActivity([
    { callId: 'read-1', toolName: 'read', status: 'completed', updatedAt: 50 },
    { callId: 'search-1', toolName: 'search', status: 'completed', updatedAt: 40 },
    { callId: 'read-2', toolName: 'read', status: 'completed', updatedAt: 30 },
    { callId: 'search-active', toolName: 'search', status: 'running', updatedAt: 10 },
    { callId: 'read-3', toolName: 'read', status: 'done', updatedAt: 20 },
  ])

  assert.equal(summary, 'search ×2 · read ×3')
})

test('task child tool activity summary remains bounded', () => {
  const summary = summarizeDesktopV3TaskToolActivity(Array.from(
    { length: DESKTOP_V3_TASK_TOOL_ACTIVITY_CALL_LIMIT + 20 },
    (_, index) => ({
      callId: `call-${index}`,
      toolName: `tool-${index}`,
      status: 'completed',
      updatedAt: index,
    }),
  ))

  assert.equal(summary.split(' · ').length, DESKTOP_V3_TASK_TOOL_ACTIVITY_GROUP_LIMIT)
  assert.equal(summary.includes('tool-0'), false)
})

test('task child view keeps launch metadata bounded before child hydration', () => {
  const state = createInitialDesktopV3CacheState()
  state.messagesBySession['child-1'] = {
    items: [{ id: 'private-transcript', session_id: 'child-1', role: 'assistant', content: 'must not drive card state', created_at: 99 }],
    sourceMessageCount: 1,
    sourceLastMessageAt: 99,
    source: 'network',
  }
  const view = selectDesktopV3TaskChildViewModel(state, row)
  assert.equal(view?.hydrated, false)
  assert.equal(view?.status, 'pending')
  assert.equal(view?.runId, '')
  assert.equal(view?.contextWindow, 0)
  assert.equal(view?.remainingTokens, null)
  assert.equal(JSON.stringify(view).includes('must not drive card state'), false)
})

test('task child view prefers fresh canonical usage, clamps remaining context, and derives terminal intent', () => {
  const state = createInitialDesktopV3CacheState()
  state.sessionsById['child-1'] = {
    kind: 'full',
    needsHydrate: false,
    session: { id: 'child-1', workspace_path: '/workspace', workspace_name: 'Workspace', title: 'Child', mode: 'auto', metadata: {}, created_at: 1, updated_at: 2, message_count: 0, last_message_at: 0 },
  }
  state.sessionViewsById['child-1'] = { agentic_settings: { mode: 'auto', agent_name: 'explorer', resolved_agent_name: 'explorer', context_window: 1000 } }
  state.currentRunIntentBySession['child-1'] = { session_id: 'child-1', run_id: 'run-done', status: 'completed', created_at: 10, updated_at: 50, event_seq: 3 }
  state.usageBySession['child-1'] = { context_window: 1000, remaining_tokens: -250, model: 'fresh-model', updated_at: 60 }

  const view = selectDesktopV3TaskChildViewModel(state, { ...row, status: 'running', modelLabel: 'stale-model' })
  assert.equal(view?.terminal, true)
  assert.equal(view?.status, 'completed')
  assert.equal(view?.modelLabel, 'fresh-model')
  assert.equal(view?.contextWindow, 1000)
  assert.equal(view?.remainingTokens, 0)
})
