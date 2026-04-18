import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'

import { sessionRequiresSnapshotHydration } from './session-snapshot-hydration'

function makeSession(overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id: 'session-1',
    title: 'New Session',
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

test('requires authoritative hydration when a later stream event only created a placeholder session', () => {
  const session = makeSession({
    workspacePath: '',
    workspaceName: '',
    createdAt: 0,
    lifecycle: {
      sessionId: 'session-1',
      runId: 'run-1',
      active: true,
      phase: 'running',
      startedAt: 1,
      endedAt: 0,
      updatedAt: 2,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: 'background_api',
    },
  })

  assert.equal(sessionRequiresSnapshotHydration(session, 'session.lifecycle.updated'), true)
})

test('does not hydrate again for a full session.created snapshot', () => {
  const session = makeSession({
    title: 'Iterating minimal git bar interface design (Compact #2)',
    updatedAt: 2,
    createdAt: 1,
  })

  assert.equal(sessionRequiresSnapshotHydration(session, 'session.created'), false)
})

test('keeps hydrating after title updates when workspace identity is still missing', () => {
  const session = makeSession({
    title: 'Iterating minimal git bar interface design (Compact #2)',
    workspacePath: '',
    workspaceName: '',
    createdAt: 0,
    updatedAt: 2,
  })

  assert.equal(sessionRequiresSnapshotHydration(session, 'run.session.title.updated'), true)
})
