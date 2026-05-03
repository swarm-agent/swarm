import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'
import { mergeSessionRecords } from './session-records'

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
