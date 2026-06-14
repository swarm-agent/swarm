import assert from 'node:assert/strict'
import test from 'node:test'

import { getDesktopSnapshot, replaceDesktopFromSnapshot, subscribeDesktop } from './desktop-state-store'
import { fetchDesktopSessionDiscovery, loadDesktopStateSnapshot, normalizeDesktopStateSnapshot } from './desktop-state-snapshot'
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
  })

  assert.equal(snapshot.rev, 42)
  assert.deepEqual(snapshot.sessionOrder, ['next', 'old'])
  assert.equal(snapshot.sessionsById?.next?.workspacePath, '/workspace/next')
  assert.equal(snapshot.messagesBySessionId?.next?.[0]?.id, 'message-2')
  assert.deepEqual(snapshot.permissionsById, {})
  assert.equal(snapshot.sessionsById?.next?.permissionsHydrated, false)
  assert.deepEqual(snapshot.sessionsById?.next?.pendingPermissions, [])
  assert.equal(snapshot.sessionsById?.next?.pendingPermissionCount, 0)
  assert.equal(snapshot.sessionsById?.next?.live.status, 'idle')
  assert.equal(snapshot.usageBySessionId?.next, undefined)
  assert.equal(snapshot.runIntentsBySessionId?.next, undefined)
  assert.equal(snapshot.workspacesByPath?.['/workspace/next']?.sessionIds[0], 'next')
  assert.equal(snapshot.routeReadinessBySessionId?.next?.ready, true)
  assert.equal(snapshot.preferencesBySessionId, undefined)
  assert.equal(snapshot.agentModelPolicyBySessionId, undefined)
})

test('normalizeDesktopStateSnapshot maps active run intent into live sidebar state', () => {
  const snapshot = normalizeDesktopStateSnapshot({
    rev: 44,
    sessions_by_id: {
      active: {
        ...sessionWire('active', 40),
        run_intent: {
          session_id: 'active',
          run_id: 'run-active',
          status: 'running',
          created_at: 38,
          updated_at: 40,
          event_seq: 7,
        },
      },
    },
    session_order: ['active'],
  })

  const session = snapshot.sessionsById?.active
  assert.equal(session?.runIntent?.runId, 'run-active')
  assert.equal(session?.live.status, 'running')
  assert.equal(session?.live.runId, 'run-active')
  assert.equal(session?.live.startedAt, 38)
})

test('normalizeDesktopStateSnapshot ignores legacy top-level run_intents_by_session for ordinary workset snapshots', () => {
  const snapshot = normalizeDesktopStateSnapshot({
    rev: 45,
    sessions_by_id: {
      active: sessionWire('active', 40),
    },
    session_order: ['active'],
    run_intents_by_session: {
      active: [{
        session_id: 'active',
        run_id: 'run-top-level',
        status: 'running',
        created_at: 38,
        updated_at: 40,
        event_seq: 7,
      }],
    },
  })

  const session = snapshot.sessionsById?.active
  assert.equal(session?.runIntent, null)
  assert.equal(session?.live.status, 'idle')
  assert.equal(session?.live.runId, null)
  assert.equal(snapshot.runIntentsBySessionId?.active, undefined)
})


test('normalizeDesktopStateSnapshot treats durable active run intent as authoritative over stale lifecycle', () => {
  const snapshot = normalizeDesktopStateSnapshot({
    rev: 46,
    sessions_by_id: {
      terminal: {
        ...sessionWire('terminal', 50),
        lifecycle: {
          session_id: 'terminal',
          run_id: 'run-stale',
          active: false,
          phase: 'completed',
          started_at: 30,
          ended_at: 50,
          updated_at: 50,
          generation: 1,
        },
      },
    },
    session_order: ['terminal'],
    current_run_intent_by_session: {
      terminal: {
        session_id: 'terminal',
        run_id: 'run-active-durable',
        status: 'pending_executor',
        created_at: 51,
        updated_at: 52,
        event_seq: 10,
      },
    },
  })

  const session = snapshot.sessionsById?.terminal
  assert.equal(session?.lifecycle?.active, false)
  assert.equal(session?.runIntent?.runId, 'run-active-durable')
  assert.equal(session?.live.status, 'starting')
  assert.equal(session?.live.runId, 'run-active-durable')
  assert.equal(session?.live.startedAt, 51)
  assert.equal(snapshot.runIntentsBySessionId?.terminal?.runId, 'run-active-durable')
})

test('normalizeDesktopStateSnapshot hydrates persisted V3 provider tool messages', () => {
  const toolEnvelope = JSON.stringify({
    path_id: 'run.v3.provider-tool-result.v1',
    type: 'v3_provider_tool_result',
    tool_name: 'read',
    call_id: 'call-read',
    tool_instance_id: 'step-1:call-read',
    arguments: '{"path":"facts.txt"}',
    output: 'file contents',
    completed_output: 'file contents',
  })

  const snapshot = normalizeDesktopStateSnapshot({
    rev: 43,
    sessions_by_id: {
      next: sessionWire('next', 30),
    },
    session_order: ['next'],
    messages_by_session: {
      next: [{ id: 'tool-message-1', session_id: 'next', global_seq: 2, role: 'tool', content: toolEnvelope, created_at: 2 }],
    },
  })

  const message = snapshot.messagesBySessionId?.next?.[0]
  assert.equal(message?.role, 'tool')
  assert.equal(message?.toolMessage?.pathId, 'run.v3.provider-tool-result.v1')
  assert.equal(message?.toolMessage?.tool, 'read')
  assert.equal(message?.toolMessage?.toolInstanceId, 'step-1:call-read')
  assert.equal(message?.toolMessage?.completedOutput, 'file contents')
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
      resources: {},
    },
  }])
  assert.deepEqual(notifications, [12])
  assert.equal(getDesktopSnapshot().rev, 12)
  assert.equal(getDesktopSnapshot().sessionsById.old, undefined)
  assert.equal(getDesktopSnapshot().sessionsById.next?.id, 'next')
})


test('fetchDesktopSessionDiscovery uses narrow metadata-only discovery endpoint', async () => {
  const requests: Array<{ url: string; body: unknown }> = []

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({
      url: String(input),
      body: init?.body ? JSON.parse(String(init.body)) : null,
    })
    return new Response(JSON.stringify({
      rev: 14,
      snapshot_endpoint_cursor: 'cursor-14',
      sessions_by_id: { discovered: sessionWire('discovered', 20) },
      projections_by_session: {
        discovered: { session_id: 'discovered', last_event_seq: 9, projection_high_watermark_seq: 11, updated_at: 25 },
      },
      session_order: ['discovered'],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  const snapshot = await fetchDesktopSessionDiscovery({
    workspacePaths: ['/workspace/discovered'],
    recent: { limit: 50 },
    history: { mode: 'full', maxMessagesPerSession: 200, maxEventsPerSession: 200, includeEvents: true },
    resources: { messages: true, events: true, runIntents: true },
    includeActive: true,
  })

  assert.equal(snapshot.rev, 14)
  assert.equal(snapshot.sessionsById.discovered?.lastEventSeq, 9)
  assert.equal(snapshot.sessionsById.discovered?.projectionHighWatermarkSeq, 11)
  assert.equal(snapshot.sessionsById.discovered?.updatedAt, 25)
  assert.deepEqual(requests, [{
    url: '/v3/sessions:discover',
    body: {
      workspace: { workspace_paths: ['/workspace/discovered'] },
      recent: { limit: 50 },
    },
  }])
})
