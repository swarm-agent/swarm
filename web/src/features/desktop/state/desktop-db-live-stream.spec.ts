import assert from 'node:assert/strict'
import { beforeEach, test } from 'node:test'

import type { ChatMessageRecord } from '../chat/types/chat'
import { createPendingUserMessage } from '../chat/services/message-cache'
import type { DesktopSessionRecord } from '../types/realtime'

import {
  applyDurableEventToDesktopDB,
  applyOptimisticRunStartToDesktopDB,
  applyRunIntentToDesktopDB,
  mergeDesktopDBDurablePatch,
  desktopMessagesCollection,
  desktopRunIntentsCollection,
  desktopSessionsCollection,
  readDesktopDbMessages,
  readDesktopDbSession,
  upsertDesktopDbRecord,
} from './desktop-db'

const testSessionIds = [
  'session-db-live-assistant',
  'session-db-live-reasoning',
  'session-db-live-tool',
  'session-db-optimistic-run',
  'session-db-pending-reconcile',
  'session-db-seq-collision',
]

function emptyLiveState(): DesktopSessionRecord['live'] {
  return {
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
    toolHistory: [],
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
  }
}

function message(input: Partial<ChatMessageRecord> & Pick<ChatMessageRecord, 'id' | 'sessionId' | 'globalSeq' | 'role' | 'content'>): ChatMessageRecord {
  return {
    createdAt: input.createdAt ?? input.globalSeq,
    ...input,
  }
}

function makeSession(id: string, overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id,
    title: 'Live stream test',
    workspacePath: '/repo',
    workspaceName: 'repo',
    mode: 'auto',
    metadata: undefined,
    sessionApi: 'v3',
    messageCount: 0,
    updatedAt: 1,
    createdAt: 1,
    permissionsHydrated: false,
    gitCommitDetected: false,
    gitCommitCount: 0,
    gitCommittedFileCount: 0,
    gitCommittedAdditions: 0,
    gitCommittedDeletions: 0,
    lifecycle: null,
    runIntent: null,
    live: emptyLiveState(),
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
    ...overrides,
  }
}

function clearSessionRecords(sessionId: string): void {
  if (desktopSessionsCollection.has(sessionId)) {
    desktopSessionsCollection.delete(sessionId)
  }
  if (desktopRunIntentsCollection.has(sessionId)) {
    desktopRunIntentsCollection.delete(sessionId)
  }
  for (const message of Array.from(desktopMessagesCollection.values())) {
    if (message.sessionId === sessionId) {
      desktopMessagesCollection.delete(desktopMessagesCollection.getKeyFromItem(message))
    }
  }
}

function seedSession(sessionId: string): void {
  upsertDesktopDbRecord(desktopSessionsCollection, makeSession(sessionId))
}

function emit(event: {
  sessionId: string
  eventType: string
  seq: number
  ts?: number
  payload: Record<string, unknown>
}): void {
  applyDurableEventToDesktopDB({
    global_seq: event.seq,
    source_seq: event.seq,
    stream: `session:${event.sessionId}`,
    event_type: event.eventType,
    entity_id: event.sessionId,
    ts_unix_ms: event.ts ?? event.seq,
    payload: {
      session_id: event.sessionId,
      ...event.payload,
    },
  })
}

beforeEach(() => {
  for (const sessionId of testSessionIds) {
    clearSessionRecords(sessionId)
  }
})

test('Desktop DB durable reducer streams assistant deltas and final assistant message', () => {
  const sessionId = 'session-db-live-assistant'
  seedSession(sessionId)

  emit({
    sessionId,
    eventType: 'session.assistant.started',
    seq: 10,
    ts: 100,
    payload: { run_id: 'run-live-assistant' },
  })
  emit({
    sessionId,
    eventType: 'session.assistant.delta',
    seq: 11,
    ts: 110,
    payload: { run_id: 'run-live-assistant', delta: 'Hello ' },
  })
  emit({
    sessionId,
    eventType: 'session.assistant.delta',
    seq: 12,
    ts: 120,
    payload: { run_id: 'run-live-assistant', delta: 'world' },
  })

  let session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.runId, 'run-live-assistant')
  assert.equal(session?.live.status, 'running')
  assert.equal(session?.live.assistantDraft, 'Hello world')
  assert.equal(session?.live.summary, 'Streaming response…')
  assert.equal(session?.live.lastEventType, 'session.assistant.delta')
  assert.equal(session?.live.seq, 12)

  emit({
    sessionId,
    eventType: 'session.assistant.completed',
    seq: 13,
    ts: 130,
    payload: {
      run_id: 'run-live-assistant',
      message: {
        id: 'msg-live-assistant',
        session_id: sessionId,
        global_seq: 13,
        role: 'assistant',
        content: 'Hello world',
        created_at: 130,
      },
      run_intent: {
        session_id: sessionId,
        run_id: 'run-live-assistant',
        status: 'completed',
        event_seq: 13,
        updated_at: 130,
      },
    },
  })

  session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.status, 'idle')
  assert.equal(session?.live.runId, null)
  assert.equal(session?.live.assistantDraft, '')
  assert.deepEqual(readDesktopDbMessages(sessionId).map((message) => message.id), ['msg-live-assistant'])
})

test('Desktop DB durable reducer streams reasoning state', () => {
  const sessionId = 'session-db-live-reasoning'
  seedSession(sessionId)

  emit({
    sessionId,
    eventType: 'session.reasoning.started',
    seq: 20,
    ts: 200,
    payload: { run_id: 'run-live-reasoning', summary: 'Checking context' },
  })
  emit({
    sessionId,
    eventType: 'session.reasoning.delta',
    seq: 21,
    ts: 210,
    payload: { run_id: 'run-live-reasoning', delta: 'Looking at files' },
  })

  let session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.runId, 'run-live-reasoning')
  assert.equal(session?.live.status, 'running')
  assert.equal(session?.live.reasoningState, 'running')
  assert.equal(session?.live.reasoningText, 'Looking at files')
  assert.equal(session?.live.reasoningSummary, 'Looking at files')
  assert.equal(session?.live.reasoningSegment, 1)
  assert.equal(session?.live.summary, 'Thinking…')
  assert.equal(session?.live.lastEventType, 'session.reasoning.delta')

  emit({
    sessionId,
    eventType: 'session.reasoning.completed',
    seq: 22,
    ts: 220,
    payload: { run_id: 'run-live-reasoning', summary: 'Plan ready' },
  })

  session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.reasoningState, 'done')
  assert.equal(session?.live.reasoningText, 'Plan ready')
  assert.equal(session?.live.summary, 'Thinking complete')
  assert.equal(session?.live.lastEventType, 'session.reasoning.completed')
  assert.equal(session?.live.seq, 22)
})

test('Desktop DB durable reducer streams live tool calls and retained completed output', () => {
  const sessionId = 'session-db-live-tool'
  seedSession(sessionId)

  emit({
    sessionId,
    eventType: 'session.tool.started',
    seq: 30,
    ts: 300,
    payload: {
      run_id: 'run-live-tool',
      tool_name: 'read',
      call_id: 'call-live-tool',
      step_id: 'step-live-tool',
      tool_instance_id: 'tool-instance-live',
      arguments: '{"path":"README.md"}',
      step: 1,
    },
  })

  let session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.status, 'running')
  assert.equal(session?.live.runId, 'run-live-tool')
  assert.equal(session?.live.toolName, 'read')
  assert.equal(session?.live.toolCallId, 'call-live-tool')
  assert.equal(session?.live.toolArguments, '{"path":"README.md"}')
  assert.equal(session?.live.summary, 'read')
  assert.equal(session?.live.toolHistory?.[0]?.state, 'running')

  emit({
    sessionId,
    eventType: 'session.tool.delta',
    seq: 31,
    ts: 310,
    payload: {
      run_id: 'run-live-tool',
      tool_name: 'read',
      call_id: 'call-live-tool',
      step_id: 'step-live-tool',
      tool_instance_id: 'tool-instance-live',
      output: 'partial output',
      step: 1,
    },
  })

  session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.toolOutput, 'partial output')
  assert.equal(session?.live.toolHistory?.[0]?.toolOutput, 'partial output')
  assert.equal(session?.live.toolHistory?.[0]?.seq, 30)

  emit({
    sessionId,
    eventType: 'session.tool.completed',
    seq: 32,
    ts: 320,
    payload: {
      run_id: 'run-live-tool',
      tool_name: 'read',
      call_id: 'call-live-tool',
      step_id: 'step-live-tool',
      tool_instance_id: 'tool-instance-live',
      output: 'final output',
      raw_output: 'final output',
      step: 1,
    },
  })

  session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.toolName, null)
  assert.equal(session?.live.toolOutput, '')
  assert.equal(session?.live.retainedToolName, 'read')
  assert.equal(session?.live.retainedToolCallId, 'call-live-tool')
  assert.equal(session?.live.retainedToolArguments, '{"path":"README.md"}')
  assert.equal(session?.live.retainedToolOutput, 'final output')
  assert.equal(session?.live.retainedToolState, 'done')
  assert.equal(session?.live.toolHistory?.[0]?.state, 'done')
  assert.equal(session?.live.toolHistory?.[0]?.completedAt, 320)
  assert.equal(session?.live.lastEventType, 'session.tool.completed')
  assert.equal(session?.live.seq, 32)
})


test('Desktop DB durable patch replaces matching pending user message with canonical message', () => {
  const sessionId = 'session-db-pending-reconcile'
  seedSession(sessionId)
  const pending = createPendingUserMessage(sessionId, 'send this', 10)
  upsertDesktopDbRecord(desktopMessagesCollection, pending)

  mergeDesktopDBDurablePatch({
    sessionId,
    messages: [message({
      id: 'msg-canonical-user',
      sessionId,
      globalSeq: 11,
      role: 'user',
      content: 'send this',
      createdAt: 1100,
      metadata: { client_request_id: pending.metadata?.client_request_id },
    })],
    appliedSeq: 11,
    highWatermark: 11,
  })

  const messages = readDesktopDbMessages(sessionId)
  assert.deepEqual(messages.map((entry) => entry.id), ['msg-canonical-user'])
  assert.equal(messages[0]?.metadata?.client_request_id, pending.metadata?.client_request_id)
})

test('Desktop DB durable patch preserves unrelated live tool messages with same sequence', () => {
  const sessionId = 'session-db-seq-collision'
  seedSession(sessionId)
  upsertDesktopDbRecord(desktopMessagesCollection, message({
    id: 'live-tool:call-bash',
    sessionId,
    globalSeq: 12,
    role: 'tool',
    content: '{"path_id":"run.tool-history.v2","tool":"bash","call_id":"call-bash"}',
    createdAt: 1200,
    toolMessage: {
      pathId: 'run.tool-history.v2',
      tool: 'bash',
      callId: 'call-bash',
      target: '',
      argumentsText: '{}',
      argumentsJson: {},
      output: '',
      completedOutput: '',
      error: '',
      durationMs: 0,
      summary: 'bash',
      state: 'running',
      editDiff: null,
      searchData: null,
      previewLines: [],
      taskRows: [],
    },
  }))

  mergeDesktopDBDurablePatch({
    sessionId,
    messages: [message({
      id: 'msg-assistant-same-seq',
      sessionId,
      globalSeq: 12,
      role: 'assistant',
      content: 'working',
      createdAt: 1210,
    })],
    appliedSeq: 12,
    highWatermark: 12,
  })

  const ids = readDesktopDbMessages(sessionId).map((entry) => entry.id)
  assert.deepEqual(ids, ['live-tool:call-bash', 'msg-assistant-same-seq'])
})


test('Desktop DB optimistic submit state starts and reconciles the header timer run state', () => {
  const sessionId = 'session-db-optimistic-run'
  seedSession(sessionId)

  applyOptimisticRunStartToDesktopDB({
    sessionId,
    startedAt: 1000,
    agentName: 'swarm',
  })

  let session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.status, 'starting')
  assert.equal(session?.live.startedAt, 1000)
  assert.equal(session?.live.awaitingAck, true)
  assert.equal(session?.live.summary, 'Starting…')
  assert.equal(session?.live.lastEventType, 'run.starting')
  assert.equal(desktopRunIntentsCollection.get(sessionId), undefined)

  applyRunIntentToDesktopDB(sessionId, {
    sessionId,
    runId: 'run-optimistic',
    status: 'pending_executor',
    blockedReason: '',
    createdAt: 1010,
    updatedAt: 1020,
    eventSeq: 40,
  }, 1020)

  session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.status, 'starting')
  assert.equal(session?.live.runId, 'run-optimistic')
  assert.equal(session?.live.startedAt, 1000)
  assert.equal(session?.live.awaitingAck, false)
  assert.equal(session?.live.summary, 'Pending executor…')
  assert.equal(session?.live.lastEventType, 'run.pending_executor')
  assert.equal(session?.live.seq, 40)
  assert.equal(desktopRunIntentsCollection.get(sessionId)?.runId, 'run-optimistic')

  applyRunIntentToDesktopDB(sessionId, {
    sessionId,
    runId: 'run-optimistic',
    status: 'completed',
    blockedReason: '',
    createdAt: 1010,
    updatedAt: 2000,
    eventSeq: 41,
  }, 2000)

  session = readDesktopDbSession(sessionId)
  assert.equal(session?.live.status, 'idle')
  assert.equal(session?.live.runId, null)
  assert.equal(session?.live.startedAt, null)
  assert.equal(session?.live.awaitingAck, false)
  assert.equal(session?.live.lastEventType, 'run.completed')
  assert.equal(desktopRunIntentsCollection.get(sessionId), undefined)
})
