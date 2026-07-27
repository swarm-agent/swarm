import assert from 'node:assert/strict'
import test from 'node:test'

import { DesktopSessionV3Runtime } from '../session-v3/runtime'
import {
  SESSION_V3_REALTIME_PROTOCOL,
  SESSION_V3_REALTIME_PROTOCOL_VERSION,
  SESSION_V3_REALTIME_RESUME_KIND,
  type SessionV3RealtimeFrameWire,
  type SessionV3RealtimeResumeWire,
  type SessionV3ReconnectSnapshot,
} from '../session-v3/types'

class MockRealtimeSocket extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState = MockRealtimeSocket.CONNECTING
  sent: string[] = []
  closed = false

  send(raw: string): void {
    this.sent.push(raw)
  }

  close(): void {
    if (this.readyState === MockRealtimeSocket.CLOSED) return
    this.closed = true
    this.readyState = MockRealtimeSocket.CLOSED
    this.dispatchEvent(new Event('close'))
  }

  open(): void {
    this.readyState = MockRealtimeSocket.OPEN
    this.dispatchEvent(new Event('open'))
  }

  message(frame: SessionV3RealtimeFrameWire): void {
    const event = new Event('message') as MessageEvent
    Object.defineProperty(event, 'data', { value: JSON.stringify(frame) })
    this.dispatchEvent(event)
  }
}

function installTransportGlobals(): void {
  const target = globalThis as typeof globalThis & { window?: unknown; WebSocket?: unknown }
  target.window ??= {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
    location: { protocol: 'http:', host: 'localhost' },
  }
  target.WebSocket ??= MockRealtimeSocket
}

function makeResume(endpointCursor: string): SessionV3RealtimeResumeWire {
  return {
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: SESSION_V3_REALTIME_RESUME_KIND,
    endpoint_cursor: endpointCursor,
    subscriptions: [
      {
        session_id: 'session-v3',
        subscription_id: 'session-subscription-v3',
        endpoint_cursor: endpointCursor,
      },
    ],
    worksets: [
      {
        workset_id: 'desktop-workset',
        subscription_id: 'desktop-workset-subscription',
        selector: { kind: 'global', global: true },
        auto_subscribe_sessions: true,
      },
    ],
  }
}

function makeReconnectSnapshot(endpointCursor: string): SessionV3ReconnectSnapshot {
  return {
    snapshot: {
      rev: 1,
      snapshotEndpointCursor: endpointCursor,
    },
    endpointCursor,
    clientId: 'client-1',
    surface: 'desktop',
    worksetId: 'desktop-workset',
    subscriptions: [
      {
        protocol: SESSION_V3_REALTIME_PROTOCOL,
        protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
        kind: 'subscribe.session',
        session_id: 'session-v3',
        subscription_id: 'session-subscription-v3',
        endpoint_cursor: endpointCursor,
      },
    ],
    worksets: makeResume(endpointCursor).worksets ?? [],
    realtimeResume: makeResume(endpointCursor),
    diagnosticsBySession: {},
    wire: { ok: true, rev: 1, snapshot_endpoint_cursor: endpointCursor },
  }
}

function parseResume(socket: MockRealtimeSocket): SessionV3RealtimeResumeWire {
  const raw = socket.sent[socket.sent.length - 1]
  assert.ok(raw)
  return JSON.parse(raw) as SessionV3RealtimeResumeWire
}

test('Desktop V3 runtime keeps the one realtime socket open after assistant completion', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const runtime = new DesktopSessionV3Runtime({
    wantedSessionIds: ['session-v3'],
    api: {
      reconnectSessionV3: async () => makeReconnectSnapshot('cursor-1'),
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: () => {
        const socket = new MockRealtimeSocket()
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  await runtime.boot()
  assert.equal(sockets.length, 1)
  sockets[0].open()
  assert.equal(parseResume(sockets[0]).endpoint_cursor, 'cursor-1')

  sockets[0].message({
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: 'event',
    type: 'event',
    session_id: 'session-v3',
    endpoint_cursor: 'cursor-2',
    last_seq: 2,
    high_watermark_seq: 2,
    event_type: 'session.assistant.completed',
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.assistant.completed',
      ts_unix_ms: 2,
      payload: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'completed' },
      },
    },
  })

  assert.equal(sockets.length, 1)
  assert.equal(sockets[0].closed, false)
  assert.equal(runtime.diagnostics().socketState, 'open')
  assert.equal(runtime.diagnostics().sessionSubscriptionCount, 1)
  assert.equal(runtime.getState().endpointCursor, 'cursor-2')
  runtime.shutdown()
})
