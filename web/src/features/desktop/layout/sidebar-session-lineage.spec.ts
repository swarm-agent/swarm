import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'

import { sessionChildDescriptor, sessionParentSessionID } from './sidebar-session-lineage'

function makeSession(overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id: 'session-self-parent',
    title: 'Iterating minimal git bar interface design (Compact #2)',
    workspacePath: '/workspaces/swarm-go',
    workspaceName: 'swarm-go',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 1,
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

test('session lineage ignores self-parent task launch metadata for routed TUI sessions', () => {
  const session = makeSession({
    id: '314afac2-1c7e-4509-90d7-987ae31e48f8',
    metadata: {
      task_launches: {
        call_wpmcLJxjXdSb5qKt0UHYAWsH: {
          parent_session_id: '314afac2-1c7e-4509-90d7-987ae31e48f8',
          child_session_id: '07192fc8-d329-48da-80b4-b734c45d3b92',
        },
      },
    },
    lifecycle: {
      sessionId: '314afac2-1c7e-4509-90d7-987ae31e48f8',
      runId: 'run_1776327848901_000001',
      active: false,
      phase: 'completed',
      startedAt: 1,
      endedAt: 2,
      updatedAt: 2,
      generation: 19,
      stopReason: null,
      error: null,
      ownerTransport: 'background_api',
    },
  })

  assert.equal(sessionParentSessionID(session), '')
  assert.deepEqual(sessionChildDescriptor(session), { kind: 'root', label: null })
})

test('session lineage keeps real background children background-targeted from direct metadata', () => {
  const session = makeSession({
    id: 'child-session',
    metadata: {
      parent_session_id: 'parent-session',
      lineage_kind: 'background_agent',
      background_agent: 'commit',
      launch_mode: 'background',
    },
    lifecycle: {
      sessionId: 'child-session',
      runId: 'run-background',
      active: false,
      phase: 'completed',
      startedAt: 1,
      endedAt: 2,
      updatedAt: 2,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: 'background_api',
    },
  })

  assert.equal(sessionParentSessionID(session), 'parent-session')
  assert.deepEqual(sessionChildDescriptor(session), { kind: 'background', label: 'background' })
})

test('session lineage keeps real subagent children labeled as subagents', () => {
  const session = makeSession({
    id: 'child-session',
    metadata: {
      parent_session_id: 'parent-session',
      requested_subagent: 'parallel',
    },
    lifecycle: {
      sessionId: 'child-session',
      runId: 'run-subagent',
      active: false,
      phase: 'completed',
      startedAt: 1,
      endedAt: 2,
      updatedAt: 2,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: 'background_api',
    },
  })

  assert.equal(sessionParentSessionID(session), 'parent-session')
  assert.deepEqual(sessionChildDescriptor(session), { kind: 'subagent', label: '@parallel' })
})
