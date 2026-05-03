import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopSessionRecord } from '../../types/realtime'
import { formatAgentTodoBadge, metadataTodoSummary, resolveSessionEffectiveAgentName, sessionUsesReadOnlyFlowIdentity } from './desktop-chat-panel'

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

function makeSession(overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id: 'session-1',
    title: 'Saved flow title',
    workspacePath: '/repo',
    workspaceName: 'repo',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 0,
    createdAt: 0,
    permissionsHydrated: false,
    lifecycle: null,
    live: {
      runId: null,
      agentName: null,
      startedAt: null,
      status: 'idle',
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
      lastEventAt: null,
      error: null,
      seq: 0,
      assistantDraft: '',
      retainedAssistantSegments: [],
      reasoningSummary: '',
      reasoningText: '',
      reasoningState: 'idle',
      reasoningSegment: 0,
      reasoningStartedAt: null,
      awaitingAck: false,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
    ...overrides,
  }
}

test('flow sessions are treated as read-only flow identity and resolve their real flow agent name', () => {
  const session = makeSession({
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
      flow_agent_name: 'memory',
      agent_name: 'swarm',
      requested_subagent: 'explorer',
    },
  })

  assert.equal(sessionUsesReadOnlyFlowIdentity(session), true)
  assert.equal(resolveSessionEffectiveAgentName(session, 'swarm'), 'memory')
})

test('non-flow sessions still resolve requested subagent before falling back to primary', () => {
  const session = makeSession({
    metadata: {
      requested_subagent: 'explorer',
    },
  })

  assert.equal(sessionUsesReadOnlyFlowIdentity(session), false)
  assert.equal(resolveSessionEffectiveAgentName(session, 'swarm'), 'explorer')
})
