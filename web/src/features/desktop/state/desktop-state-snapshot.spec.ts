import assert from 'node:assert/strict'
import test from 'node:test'

import { getDesktopSnapshot, replaceDesktopFromSnapshot, subscribeDesktop } from './desktop-state-store'
import { loadDesktopStateSnapshot, normalizeDesktopStateSnapshot } from './desktop-state-snapshot'
import type { DesktopSessionRecord } from '../types/realtime'

const originalFetch = globalThis.fetch

test.afterEach(() => {
  globalThis.fetch = originalFetch
  replaceDesktopFromSnapshot({ rev: 0 })
})

function sessionWire(id: string, updatedAt: number) {
  return {
    id,
    title: `Session ${id}`,
    workspace_path: `/workspace/${id}`,
    workspace_name: `workspace-${id}`,
    mode: 'auto',
    updated_at: updatedAt,
    created_at: updatedAt,
    message_count: 1,
    session_api: 'v3',
  }
}

function sessionRecord(id: string, updatedAt: number): DesktopSessionRecord {
  return {
    id,
    title: `Session ${id}`,
    workspacePath: `/workspace/${id}`,
    workspaceName: `workspace-${id}`,
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

test('normalizeDesktopStateSnapshot requires a valid daemon rev', () => {
  assert.throws(
    () => normalizeDesktopStateSnapshot({ sessions_by_id: { 'session-1': sessionWire('session-1', 10) } }),
    /valid daemon rev/,
  )
  assert.throws(
    () => normalizeDesktopStateSnapshot({ rev: Number.NaN }),
    /valid daemon rev/,
  )
})

test('normalizeDesktopStateSnapshot builds a plain replacement snapshot from workset wire data', () => {
  const snapshot = normalizeDesktopStateSnapshot({
    rev: 42,
    sessions_by_id: {
      old: sessionWire('old', 10),
      next: sessionWire('next', 30),
    },
    session_order: ['next'],
    messages_by_session: {
      next: [{ id: 'message-2', session_id: 'next', global_seq: 2, role: 'assistant', content: 'two', created_at: 2 }],
    },
    permissions_by_session: {
      next: [{ id: 'permission-1', session_id: 'next', status: 'pending' }],
    },
    run_intents_by_session: {
      next: [
        { session_id: 'next', run_id: 'run-old', status: 'completed', event_seq: 1 },
        { session_id: 'next', run_id: 'run-new', status: 'running', event_seq: 3 },
      ],
    },
  })

  assert.equal(snapshot.rev, 42)
  assert.deepEqual(snapshot.sessionOrder, ['next', 'old'])
  assert.equal(snapshot.sessionsById?.next?.workspacePath, '/workspace/next')
  assert.equal(snapshot.messagesBySessionId?.next?.[0]?.id, 'message-2')
  assert.equal(snapshot.permissionsById?.['permission-1']?.sessionId, 'next')
  assert.equal(snapshot.runIntentsBySessionId?.next?.runId, 'run-new')
  assert.equal(snapshot.workspacesByPath?.['/workspace/next']?.sessionIds[0], 'next')
  assert.equal(snapshot.routeReadinessBySessionId?.next?.ready, true)
})

test('loadDesktopStateSnapshot fetches the workset endpoint and replaces old store state exactly once', async () => {
  replaceDesktopFromSnapshot({
    rev: 10,
    sessionsById: { old: sessionRecord('old', 10) },
    sessionOrder: ['old'],
  })

  const notifications: number[] = []
  const unsubscribe = subscribeDesktop(() => notifications.push(getDesktopSnapshot().rev))
  const requests: Array<{ url: string; body: unknown }> = []

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({
      url: String(input),
      body: init?.body ? JSON.parse(String(init.body)) : null,
    })
    return new Response(JSON.stringify({
      rev: 12,
      sessions_by_id: { next: sessionWire('next', 20) },
      session_order: ['next'],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  const snapshot = await loadDesktopStateSnapshot({ recent: { limit: 1 }, history: { mode: 'none' } })
  unsubscribe()

  assert.equal(snapshot.rev, 12)
  assert.deepEqual(requests, [{
    url: '/v3/sessions:workset',
    body: {
      recent: { limit: 1 },
      history: {
        mode: 'none',
        max_events_per_session: 0,
        manifest_policy: 'manifest',
        include_events: false,
      },
    },
  }])
  assert.deepEqual(notifications, [12])
  assert.equal(getDesktopSnapshot().rev, 12)
  assert.equal(getDesktopSnapshot().sessionsById.old, undefined)
  assert.equal(getDesktopSnapshot().sessionsById.next?.id, 'next')
})
