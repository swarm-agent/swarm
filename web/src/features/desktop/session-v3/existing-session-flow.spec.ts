import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type {
  DesktopV3CacheAction,
  DesktopV3CacheState,
  SessionMessageMutationResponse,
} from '../state/desktop-v3-cache-types'
import {
  clearDesktopV3ExistingMessageOperation,
  continueDesktopV3Conversation,
  createDesktopV3ExistingMessageOperation,
  loadDesktopV3ExistingMessageOperation,
  persistDesktopV3ExistingMessageOperation,
  setDesktopV3ExistingSessionFlowDepsForTests,
  type DesktopV3ExistingMessageOperation,
} from './existing-session-flow'

function makeMessageResponse(operation: DesktopV3ExistingMessageOperation): SessionMessageMutationResponse {
  return {
    ok: true,
    session_id: operation.sessionId,
    message: {
      id: operation.request.message_id,
      session_id: operation.sessionId,
      global_seq: 3,
      role: 'user',
      content: operation.request.content,
      created_at: 2,
    },
    run_intent: {
      session_id: operation.sessionId,
      run_id: operation.request.run_id,
      status: 'pending_executor',
      created_at: 2,
      updated_at: 2,
      event_seq: 4,
    },
    mutation: {},
    realtime_outbox: null,
  }
}

function withSessionStorage(run: (storage: Map<string, string>) => void): void {
  const previousWindow = globalThis.window
  const storage = new Map<string, string>()
  globalThis.window = {
    sessionStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    },
  } as Window & typeof globalThis
  try {
    run(storage)
  } finally {
    globalThis.window = previousWindow
  }
}

function deferred<T = void>(): { promise: Promise<T>; resolve: (value: T | PromiseLike<T>) => void; reject: (reason?: unknown) => void } {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

async function flushAsyncWork(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
}

test('Path B operation contains stable existing-session message payload', () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: ' session-1 ',
    prompt: ' hello ',
    metadata: { source: 'test' },
  })

  assert.equal(operation.version, 1)
  assert.equal(operation.sessionId, 'session-1')
  assert.equal(operation.request.client_request_id, `desktop-v3-existing-message:session-1:${operation.operationId}`)
  assert.equal(operation.request.message_id, `desktop-v3-message:${operation.operationId}`)
  assert.equal(operation.request.run_id, `desktop-v3-run:${operation.operationId}`)
  assert.equal(operation.request.role, 'user')
  assert.equal(operation.request.content, 'hello')
  assert.deepEqual(operation.request.metadata, { source: 'test' })
})

test('Path B operation rejects missing sessionId or prompt before HTTP', () => {
  assert.throws(() => createDesktopV3ExistingMessageOperation({
    sessionId: ' ',
    prompt: 'hello',
  }), /sessionId/)
  assert.throws(() => createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: ' ',
  }), /prompt/)
})

test('Path B operation persists exact retry payload per session', () => withSessionStorage((storage) => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'retry me',
  })
  persistDesktopV3ExistingMessageOperation(operation)

  const loaded = loadDesktopV3ExistingMessageOperation('session-1')
  assert.equal(loaded?.operationId, operation.operationId)
  assert.deepEqual(loaded?.request, JSON.parse(JSON.stringify(operation.request)))

  assert.equal(loadDesktopV3ExistingMessageOperation('session-2'), null)
  clearDesktopV3ExistingMessageOperation('session-1', 'different-operation')
  assert.equal(storage.size, 1)
  clearDesktopV3ExistingMessageOperation('session-1', operation.operationId)
  assert.equal(storage.size, 0)
}))

test('Path B discards invalid retained operations', () => withSessionStorage(() => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'retry me',
  })
  const invalid = JSON.parse(JSON.stringify(operation)) as DesktopV3ExistingMessageOperation
  invalid.request.message_id = ''
  persistDesktopV3ExistingMessageOperation(invalid)
  assert.equal(loadDesktopV3ExistingMessageOperation('session-1'), null)
}))

test('continueDesktopV3Conversation subscribes, starts hydrate, appends message, and applies result', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'continue',
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  const calls: string[] = []
  const actions: DesktopV3CacheAction[] = []
  let hydrateResolved = false
  let resolveHydrate: (() => void) | undefined
  const hydratePromise = new Promise<void>((resolve) => {
    resolveHydrate = () => {
      hydrateResolved = true
      resolve()
    }
  })

  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      currentEndpointCursor: () => {
        calls.push('capture-cursor')
        return 'cursor-before-send'
      },
      connectSession: async (input: { sessionId: string; endpointCursor?: string }) => {
        calls.push(`connect:${input.sessionId}:${input.endpointCursor}`)
      },
      ensureSessionHistory: async (sessionId: string) => {
        calls.push(`hydrate:${sessionId}`)
        await hydratePromise
      },
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      actions.push(action)
      calls.push(`dispatch:${action.type}`)
    },
    postAppendMessage: async (sessionId, request) => {
      assert.equal(hydrateResolved, false, 'targeted hydrate must not block message append')
      calls.push(`message:${sessionId}:${request.message_id}`)
      return makeMessageResponse(operation)
    },
  })

  try {
    const response = await continueDesktopV3Conversation(operation)
    resolveHydrate?.()
    assert.equal(response.session_id, 'session-1')
    assert.deepEqual(calls, [
      'capture-cursor',
      'dispatch:pendingUser.upsert',
      'hydrate:session-1',
      'connect:session-1:cursor-before-send',
      `message:session-1:${operation.request.message_id}`,
      'dispatch:mutation.messageResult',
    ])
    assert.equal(actions[0]?.type, 'pendingUser.upsert')
    assert.equal(actions[1]?.type, 'mutation.messageResult')
  } finally {
    resolveHydrate?.()
    restore()
  }
})


test('Desktop V3 existing session appends message even if connectSession never resolves', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'continue',
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  const connect = deferred<void>()
  const hydrate = deferred<void>()
  const calls: string[] = []
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      currentEndpointCursor: () => 'cursor-before-send',
      connectSession: async (input: { sessionId: string; endpointCursor?: string }) => {
        calls.push(`connect:start:${input.sessionId}:${input.endpointCursor}`)
        await connect.promise
        calls.push(`connect:complete:${input.sessionId}`)
      },
      ensureSessionHistory: async (sessionId: string) => {
        calls.push(`hydrate:${sessionId}`)
        await hydrate.promise
      },
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      calls.push(`dispatch:${action.type}`)
    },
    postAppendMessage: async (sessionId, request) => {
      calls.push(`message:${sessionId}:${request.message_id}`)
      return makeMessageResponse(operation)
    },
  })

  try {
    const response = await continueDesktopV3Conversation(operation)
    assert.equal(response.session_id, 'session-1')
    assert.deepEqual(calls, [
      'dispatch:pendingUser.upsert',
      'hydrate:session-1',
      'connect:start:session-1:cursor-before-send',
      `message:session-1:${operation.request.message_id}`,
      'dispatch:mutation.messageResult',
    ])
  } finally {
    connect.resolve()
    hydrate.resolve()
    restore()
  }
})

test('Desktop V3 existing session appends message even if connectSession rejects', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'continue',
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  let appended = false
  const actions: DesktopV3CacheAction[] = []
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      currentEndpointCursor: () => 'cursor-before-send',
      connectSession: async () => {
        throw new Error('subscribe rejected')
      },
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => actions.push(action),
    postAppendMessage: async () => {
      appended = true
      return makeMessageResponse(operation)
    },
  })

  try {
    const response = await continueDesktopV3Conversation(operation)
    await flushAsyncWork()
    assert.equal(response.session_id, 'session-1')
    assert.equal(appended, true)
    assert.deepEqual(actions.map((action) => action.type), [
      'pendingUser.upsert',
      'mutation.messageResult',
      'realtime.statusChanged',
    ])
    const stale = actions.at(-1)
    assert.equal(stale?.type, 'realtime.statusChanged')
    assert.equal(stale?.status, 'stale')
    assert.equal(stale?.errorCode, 'session_subscribe_failed')
  } finally {
    restore()
  }
})

test('continueDesktopV3Conversation rejects deleted sessions before append', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-deleted',
    prompt: 'continue',
  })
  const state = createEmptyDesktopV3CacheState()
  state.tombstonesBySession['session-deleted'] = {
    session_id: 'session-deleted',
    account_scope_id: 'acct',
    user_id: 'user',
    updated_at: 1,
    deleted: true,
  }
  let appended = false
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    postAppendMessage: async () => {
      appended = true
      return makeMessageResponse(operation)
    },
  })

  try {
    await assert.rejects(continueDesktopV3Conversation(operation), /deleted/)
    assert.equal(appended, false)
  } finally {
    restore()
  }
})

test('continueDesktopV3Conversation requires queued or running run intent', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'continue',
  })
  const state = createEmptyDesktopV3CacheState()
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      currentEndpointCursor: () => 'cursor-before-send',
      connectSession: async () => undefined,
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: () => undefined,
    postAppendMessage: async () => ({
      ...makeMessageResponse(operation),
      run_intent: { ...makeMessageResponse(operation).run_intent!, status: 'dispatch_blocked', blocked_reason: 'blocked' },
    }),
  })

  try {
    await assert.rejects(continueDesktopV3Conversation(operation), /blocked/)
  } finally {
    restore()
  }
})

test('existing-session-flow has no session-create API import or call', () => {
  const filename = fileURLToPath(new URL('./existing-session-flow.ts', import.meta.url))
  const source = readFileSync(filename, 'utf8')
  assert.equal(source.includes('postDesktopV3CreateSession'), false)
  assert.equal(source.includes("'/v3/sessions'"), false)
})
