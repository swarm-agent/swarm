import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopSessionRecord } from '../types/realtime'

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

test('session map prefers live store flow session over overview placeholder for the same id', () => {
  const overviewSession = makeSession({
    id: 'flow-session',
    title: 'New Session',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
    },
  })
  const liveSession = makeSession({
    id: 'flow-session',
    title: 'Memory sweep',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
    },
    updatedAt: 10,
  })

  const sessionMap = new Map<string, DesktopSessionRecord>()
  for (const session of [overviewSession]) {
    sessionMap.set(session.id, session)
  }
  sessionMap.set(liveSession.id, liveSession)

  assert.equal(sessionMap.get('flow-session')?.title, 'Memory sweep')
})
