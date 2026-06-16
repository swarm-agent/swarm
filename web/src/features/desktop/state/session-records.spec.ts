import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopLiveReasoningRecord, DesktopSessionRecord } from '../types/realtime'
import { mergeSessionRecords } from './session-records'

function reasoningRecord(overrides: Partial<DesktopLiveReasoningRecord> = {}): DesktopLiveReasoningRecord {
  return {
    key: 'run-1:step-1:reasoning-1',
    runId: 'run-1',
    step: 1,
    stepId: 'step-1',
    reasoningId: 'reasoning-1',
    reasoningKey: 'summary-1',
    text: 'Thinking one',
    summary: 'Thinking one',
    state: 'done',
    startedAt: 10,
    completedAt: 12,
    timelineSeq: 11,
    updatedSeq: 12,
    ...overrides,
  }
}

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
    createdAt: 1,
    permissionsHydrated: false,
    lifecycle: null,
    live: {
      runId: null,
      agentName: null,
      startedAt: null,
      status: 'idle',
      step: 0,
      toolName: null,
    sidebarToolName: null,
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

test('flow merge preserves saved title when incoming title is a placeholder', () => {
  const existing = makeSession({
    title: 'Memory sweep',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
    },
  })
  const incoming = makeSession({
    title: 'New Session',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
    },
    updatedAt: 5,
  })

  const merged = mergeSessionRecords(existing, incoming)
  assert.equal(merged.title, 'Memory sweep')
})

test('non-flow merge still accepts incoming title updates', () => {
  const existing = makeSession({ title: 'Old title' })
  const incoming = makeSession({ title: 'New title', updatedAt: 5 })

  const merged = mergeSessionRecords(existing, incoming)
  assert.equal(merged.title, 'New title')
})

test('flow merge still accepts non-placeholder incoming titles', () => {
  const existing = makeSession({
    title: 'Old flow title',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
    },
  })
  const incoming = makeSession({
    title: 'Renamed flow title',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
    },
    updatedAt: 5,
  })

  const merged = mergeSessionRecords(existing, incoming)
  assert.equal(merged.title, 'Renamed flow title')
})

test('merge preserves client-only live history when incoming hydration has empty live arrays', () => {
  const existing = makeSession({
    live: {
      ...makeSession().live,
      seq: 8,
      assistantDraft: 'streaming text',
      retainedAssistantSegments: [
        { id: 'live-assistant:1:4:0', content: 'assistant before tool', createdAt: 1, seq: 4 },
      ],
      toolHistory: [
        {
          key: 'session-1\u001frun-1\u001fstep-1\u001fcall-1\u001fstep-1:call-1',
          sessionId: 'session-1',
          runId: 'run-1',
          stepId: 'step-1',
          callId: 'call-1',
          toolInstanceId: 'step-1:call-1',
          toolName: 'read',
          toolArguments: '{"path":"file.txt"}',
          toolOutput: 'done',
          state: 'done',
          step: 1,
          seq: 5,
          startedAt: 2,
          updatedAt: 3,
          completedAt: 3,
        },
      ],
    },
  })
  const incoming = makeSession({
    updatedAt: 10,
    live: {
      ...makeSession().live,
      seq: 3,
      retainedAssistantSegments: [],
      toolHistory: [],
      status: 'idle',
    },
  })

  const merged = mergeSessionRecords(existing, incoming)

  assert.equal(merged.live.seq, 8)
  assert.deepEqual(merged.live.retainedAssistantSegments.map((segment) => [segment.content, segment.seq]), [
    ['assistant before tool', 4],
  ])
  assert.deepEqual(merged.live.toolHistory?.map((item) => [item.callId, item.seq]), [
    ['call-1', 5],
  ])
})

test('merge unions reasoning history and dedupes by stable identity', () => {
  const existing = makeSession({
    live: {
      ...makeSession().live,
      reasoningHistory: [
        reasoningRecord({
          key: 'run-1:step-1:reasoning-1',
          text: 'old duplicate text',
          summary: 'old duplicate text',
          updatedSeq: 12,
        }),
      ],
    },
  })
  const incoming = makeSession({
    live: {
      ...makeSession().live,
      reasoningHistory: [
        reasoningRecord({
          key: 'run-1:step-1:reasoning-1',
          text: 'updated duplicate text',
          summary: 'updated duplicate text',
          updatedSeq: 14,
        }),
        reasoningRecord({
          key: 'run-2:step-1:reasoning-2',
          runId: 'run-2',
          reasoningId: 'reasoning-2',
          reasoningKey: 'summary-2',
          text: 'Thinking two',
          summary: 'Thinking two',
          startedAt: 20,
          completedAt: 22,
          timelineSeq: 21,
          updatedSeq: 22,
        }),
      ],
    },
  })

  const merged = mergeSessionRecords(existing, incoming)

  assert.deepEqual(merged.live.reasoningHistory?.map((record) => [record.key, record.text, record.updatedSeq]), [
    ['run-2:step-1:reasoning-2', 'Thinking two', 22],
    ['run-1:step-1:reasoning-1', 'updated duplicate text', 14],
  ])
})

test('merge preserves existing reasoning history when incoming history is empty', () => {
  const existingHistory = [reasoningRecord({ key: 'run-1:step-1:reasoning-1' })]
  const existing = makeSession({
    live: {
      ...makeSession().live,
      reasoningHistory: existingHistory,
    },
  })
  const incoming = makeSession({
    updatedAt: 5,
    live: {
      ...makeSession().live,
      reasoningHistory: [],
    },
  })

  const merged = mergeSessionRecords(existing, incoming)

  assert.deepEqual(merged.live.reasoningHistory, existingHistory)
})

test('merge preserves live tool details when scoped hydration only carries run status', () => {
  const existing = makeSession({
    updatedAt: 20,
    lastEventSeq: 12,
    projectionHighWatermarkSeq: 12,
    live: {
      ...makeSession().live,
      status: 'running',
      runId: 'run-1',
      startedAt: 10,
      seq: 12,
      lastEventType: 'session.tool.delta',
      lastEventAt: 22,
      toolName: 'read',
      toolCallId: 'call-1',
      toolArguments: '{"path":"README.md"}',
      toolOutput: 'partial output',
      summary: 'read',
      retainedAssistantSegments: [
        { id: 'live-assistant:10:9:0', content: 'checking files', createdAt: 10, seq: 9 },
      ],
      toolHistory: [
        {
          key: 'session-1\u001frun-1\u001fstep-1\u001fcall-1\u001ftool-1',
          sessionId: 'session-1',
          runId: 'run-1',
          stepId: 'step-1',
          callId: 'call-1',
          toolInstanceId: 'tool-1',
          toolName: 'read',
          toolArguments: '{"path":"README.md"}',
          toolOutput: 'partial output',
          state: 'running',
          step: 1,
          seq: 12,
          startedAt: 20,
          updatedAt: 22,
          completedAt: null,
        },
      ],
    },
  })
  const incoming = makeSession({
    updatedAt: 25,
    lastEventSeq: 12,
    projectionHighWatermarkSeq: 12,
    lifecycle: {
      sessionId: 'session-1',
      runId: 'run-1',
      active: true,
      phase: 'running',
      startedAt: 10,
      endedAt: 0,
      updatedAt: 25,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: null,
    },
    live: {
      ...makeSession().live,
      status: 'running',
      runId: 'run-1',
      startedAt: 10,
      summary: 'Assistant responding…',
      lastEventType: 'session.lifecycle.updated',
      lastEventAt: 25,
    },
  })

  const merged = mergeSessionRecords(existing, incoming)

  assert.equal(merged.live.toolName, 'read')
  assert.equal(merged.live.toolCallId, 'call-1')
  assert.equal(merged.live.toolArguments, '{"path":"README.md"}')
  assert.equal(merged.live.toolOutput, 'partial output')
  assert.equal(merged.live.summary, 'read')
  assert.equal(merged.live.toolHistory?.[0]?.toolName, 'read')
  assert.equal(merged.live.retainedAssistantSegments[0]?.content, 'checking files')
})
