import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { DesktopRunStreamController, type RunStreamEventMessage } from './run-stream-controller'

const originalFetch = globalThis.fetch
const originalWindow = globalThis.window
const originalWebSocket = globalThis.WebSocket

type Listener = (event: { data?: string }) => void

class FakeWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  static instances: FakeWebSocket[] = []

  readyState = FakeWebSocket.OPEN
  readonly listeners = new Map<string, Listener[]>()

  constructor(_input: string | URL) {
    FakeWebSocket.instances.push(this)
  }

  addEventListener(type: string, callback: EventListenerOrEventListenerObject) {
    const listener: Listener = (event) => {
      if (typeof callback === 'function') {
        callback(event as Event)
      } else {
        callback.handleEvent(event as Event)
      }
    }
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener])
  }

  send(_payload: string) {}

  close() {
    if (this.readyState === FakeWebSocket.CLOSED) {
      return
    }
    this.readyState = FakeWebSocket.CLOSED
    this.emit('close', {})
  }

  emit(type: string, event: { data?: string }) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event)
    }
  }

  emitFrame(payload: RunStreamEventMessage) {
    this.emit('message', { data: JSON.stringify(payload) })
  }
}

function installBrowserStubs() {
  FakeWebSocket.instances = []
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    addEventListener() {},
    removeEventListener() {},
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
  } as unknown as Window & typeof globalThis
}

function makeController(sessionApi: string | null = 'v3') {
  const frames: RunStreamEventMessage[] = []
  const controller = new DesktopRunStreamController({
    getResumeRequest: () => ({
      sessionId: 'session-v3',
      runId: 'run-v3',
      lastSeq: 0,
      sessionApi,
      afterSeq: 0,
    }),
    onFrame: (_sessionId, payload) => frames.push(payload),
    onReconnectPending() {},
    onResumeFailure(_sessionId, message) {
      throw new Error(message)
    },
  })
  return { controller, frames }
}

afterEach(() => {
  globalThis.fetch = originalFetch
  globalThis.WebSocket = originalWebSocket
  if (originalWindow) {
    globalThis.window = originalWindow
  } else {
    Reflect.deleteProperty(globalThis, 'window')
  }
})

test('Desktop V3 run stream ignores turn.completed without a durable terminal run intent', async () => {
  installBrowserStubs()
  const { controller, frames } = makeController('v3')

  await controller.ensure('session-v3', 'run-v3')
  const socket = FakeWebSocket.instances[0]
  assert.ok(socket)

  socket.emitFrame({ type: 'turn.completed', run_id: 'run-v3', status: 'completed' })

  assert.equal(controller.activeSessionCount(), 1)
  assert.equal(socket.readyState, FakeWebSocket.OPEN)
  assert.equal(frames.length, 1)

  controller.closeAll()
})

test('Desktop V3 run stream closes only on terminal run intent for the active run', async () => {
  installBrowserStubs()
  const { controller } = makeController('v3')

  await controller.ensure('session-v3', 'run-v3')
  const socket = FakeWebSocket.instances[0]
  assert.ok(socket)

  socket.emitFrame({
    type: 'event',
    session_id: 'session-v3',
    event: {
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.assistant.completed',
      payload: {
        session_id: 'session-v3',
        run_id: 'run-other',
        run_intent: { session_id: 'session-v3', run_id: 'run-other', status: 'completed' },
      },
    },
  })

  assert.equal(controller.activeSessionCount(), 1)
  assert.equal(socket.readyState, FakeWebSocket.OPEN)

  socket.emitFrame({
    type: 'event',
    session_id: 'session-v3',
    event: {
      session_id: 'session-v3',
      seq: 3,
      event_type: 'session.assistant.completed',
      payload: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'completed' },
      },
    },
  })

  assert.equal(controller.activeSessionCount(), 0)
  assert.equal(socket.readyState, FakeWebSocket.CLOSED)
})
