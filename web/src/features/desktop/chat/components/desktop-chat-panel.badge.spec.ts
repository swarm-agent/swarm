import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopSessionRecord } from '../../types/realtime'
import type { ChatMessageRecord } from '../types/chat'
import { formatAgentTodoBadge, formatMobileAgentTodoBadge, isDesktopCompactionCheckpointMessage, isDesktopManualCompactionAckMessage, metadataTodoSummary, resolveSessionEffectiveAgentName, savedRuleCountdownSeconds, sessionUsesReadOnlyFlowIdentity, visibleDesktopChatMessages } from './desktop-chat-panel'

test('formatAgentTodoBadge shows progress-first badge with active count', () => {
  assert.equal(formatAgentTodoBadge({ taskCount: 6, openCount: 2, inProgressCount: 1, activeText: '' }), '4/6 complete • 1 active')
})

test('formatAgentTodoBadge shows complete state when no tasks remain open', () => {
  assert.equal(formatAgentTodoBadge({ taskCount: 6, openCount: 0, inProgressCount: 0, activeText: '' }), 'Complete · 6/6')
})

test('formatAgentTodoBadge shows active todo text when available', () => {
  const summary = { taskCount: 6, openCount: 2, inProgressCount: 1, activeText: 'Validate task badge states on desktop' }

  assert.equal(formatAgentTodoBadge(summary), 'Validate task badge states on desktop')
})

test('formatMobileAgentTodoBadge stays compact when active todo text is available', () => {
  const summary = { taskCount: 6, openCount: 2, inProgressCount: 1, activeText: 'Validate task badge states on mobile' }

  assert.equal(formatMobileAgentTodoBadge(summary), '4/6')
})

test('formatMobileAgentTodoBadge shows state labels at mobile edges', () => {
  assert.equal(formatMobileAgentTodoBadge({ taskCount: 5, openCount: 5, inProgressCount: 1, activeText: 'Start the checklist' }), 'Active')
  assert.equal(formatMobileAgentTodoBadge({ taskCount: 5, openCount: 0, inProgressCount: 0, activeText: '' }), 'Complete')
})

test('metadataTodoSummary reads agent-scoped counts from metadata', () => {
  assert.deepEqual(metadataTodoSummary({
    agent_todo_summary: {
      task_count: 5,
      open_count: 3,
      in_progress_count: 1,
      user: { task_count: 2, open_count: 1, in_progress_count: 0 },
      agent: { task_count: 3, open_count: 2, in_progress_count: 1, active_todo: { text: 'Make mobile badge readable' } },
    },
  }), {
    taskCount: 3,
    openCount: 2,
    inProgressCount: 1,
    activeText: 'Make mobile badge readable',
  })
})

test('savedRuleCountdownSeconds counts down the visible saved-rule notice', () => {
  assert.equal(savedRuleCountdownSeconds(5_000, 0), 5)
  assert.equal(savedRuleCountdownSeconds(5_000, 1), 5)
  assert.equal(savedRuleCountdownSeconds(5_000, 1_001), 4)
  assert.equal(savedRuleCountdownSeconds(5_000, 5_000), 0)
  assert.equal(savedRuleCountdownSeconds(null, 0), 0)
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

function makeMessage(overrides: Partial<ChatMessageRecord> = {}): ChatMessageRecord {
  return {
    id: 'message-1',
    sessionId: 'session-1',
    globalSeq: 1,
    role: 'assistant',
    content: 'hello',
    createdAt: 0,
    ...overrides,
  }
}

test('desktop chat shows the context compact checkpoint and hides the duplicate ack', () => {
  const checkpoint = makeMessage({
    id: 'checkpoint',
    role: 'system',
    content: '[context-compact] index=3 origin=manual\n\nCompacted recap:\nsummary',
  })
  const ack = makeMessage({
    id: 'ack',
    globalSeq: 2,
    role: 'assistant',
    content: 'Manual context compact complete (Compact #3).',
  })

  assert.equal(isDesktopCompactionCheckpointMessage(checkpoint), true)
  assert.equal(isDesktopManualCompactionAckMessage(ack), true)
  assert.deepEqual(visibleDesktopChatMessages([checkpoint, ack]).map((message) => message.id), ['checkpoint'])
})
