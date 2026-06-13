import assert from 'node:assert/strict'
import test from 'node:test'

import type { ChatMessageRecord } from '../chat/types/chat'
import type { DesktopSessionRecord } from '../types/realtime'
import {
  clearV3RuntimePersistenceForTests,
  createV3RuntimeController,
  createV3SnapshotEnvelope,
  installV3RuntimePersistence,
  normalizeV3RealtimeFrame,
  readV3RuntimePersistedSnapshot,
  restoreV3RuntimeFromPersistence,
} from './index'

function session(id: string, updatedAt: number): DesktopSessionRecord {
  return {
    id,
    title: `Session ${id}`,
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    mode: 'auto',
    messageCount: 0,
    updatedAt,
    createdAt: updatedAt,
    permissionsHydrated: true,
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
  }
}

function message(sessionId: string, id: string, globalSeq: number): ChatMessageRecord {
  return {
    id,
    sessionId,
    globalSeq,
    role: 'assistant',
    content: `message ${id}`,
    createdAt: globalSeq,
  }
}

async function flushPersistenceWrites(): Promise<void> {
  await new Promise((resolve) => setImmediate(resolve))
  await new Promise((resolve) => setImmediate(resolve))
}

test('V3 runtime persistence stores compact snapshots and restores through persisted restore envelopes', async () => {
  clearV3RuntimePersistenceForTests()
  const runtime = createV3RuntimeController()
  const unsubscribe = installV3RuntimePersistence(runtime)

  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 1,
    sessionsById: { s1: session('s1', 1) },
    sessionOrder: ['s1'],
  }, { receivedAt: 1 }))
  runtime.applyEnvelope(normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-2',
    rev: 2,
    prevRev: 1,
    high_watermark_seq: 2,
    event_type: 'desktop/message/upsert',
    event: {
      session_id: 's1',
      event_type: 'desktop/message/upsert',
      payload: { message: message('s1', 'm1', 2) },
      seq: 2,
    },
  }, { receivedAt: 2 }))

  await flushPersistenceWrites()
  unsubscribe()

  const persisted = await readV3RuntimePersistedSnapshot()
  assert.equal(persisted?.desktop.rev, 2)
  assert.equal(persisted?.desktop.messagesBySessionId?.s1?.[0]?.id, 'm1')
  assert.equal(persisted?.cursorsByScope['session:s1']?.endpointCursor, 'cursor-2')

  const restoredRuntime = createV3RuntimeController()
  const restored = await restoreV3RuntimeFromPersistence(restoredRuntime)
  assert.equal(restored?.applied, true)
  assert.equal(restoredRuntime.getDesktopSnapshot().messagesBySessionId.s1?.[0]?.id, 'm1')
  assert.equal(restoredRuntime.getSnapshot().cursorsByScope['session:s1']?.endpointCursor, 'cursor-2')
  assert.equal(restoredRuntime.getSnapshot().cursorsByScope['session:s1']?.sourceKind, 'persisted')

  clearV3RuntimePersistenceForTests()
})
