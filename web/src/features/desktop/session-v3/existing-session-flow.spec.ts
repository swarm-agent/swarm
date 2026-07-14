import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { applyDesktopV3LivePatchBatch, createEmptyDesktopV3CacheState, desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { selectRenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { buildDesktopV3ConversationRenderItems, desktopV3RenderItemKey } from '../chat/components/desktop-v3-existing-conversation-pane'
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

test('paused-chat resend reconciles the optimistic user row to one canonical timeline message', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-paused',
    prompt: 'continue after pause',
  })
  let state = createEmptyDesktopV3CacheState()
  state.messagesBySession[operation.sessionId] = {
    items: [{
      id: 'message-initial',
      session_id: operation.sessionId,
      global_seq: 1,
      role: 'user',
      content: 'initial request',
      created_at: 1,
    }],
    byMessageId: { 'message-initial': 0 },
    byGlobalSeq: { '1': 0 },
  }
  state.liveRunsBySession[operation.sessionId] = {
    'run-paused': {
      sessionId: operation.sessionId,
      runId: 'run-paused',
      status: 'cancelled',
      assistantDraft: {
        content: 'paused assistant response',
        updatedAt: 2,
        timelineSeq: 2,
        streamId: 'stream-paused',
        livePaused: true,
      },
      toolCallsByCallId: {},
      lastEventSeqSeen: 2,
    },
  }

  let pendingKey = ''
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionConnected: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      state = desktopV3CacheReducer(state, action)
      if (action.type !== 'pendingUser.upsert') return
      const pendingItems = buildDesktopV3ConversationRenderItems(
        selectRenderedSessionMessages(state, operation.sessionId),
      ).filter((item) => item.type !== 'live-working')
      assert.deepEqual(
        pendingItems.map((item) => item.type),
        ['message', 'live-assistant', 'pending-user'],
      )
      pendingKey = desktopV3RenderItemKey(pendingItems.at(-1)!)
    },
    postAppendMessage: async () => makeMessageResponse(operation),
  })

  try {
    await continueDesktopV3Conversation(operation)
  } finally {
    restore()
  }

  const committedItems = buildDesktopV3ConversationRenderItems(
    selectRenderedSessionMessages(state, operation.sessionId),
  ).filter((item) => item.type !== 'live-working')
  assert.deepEqual(
    committedItems.map((item) => item.type),
    ['message', 'live-assistant', 'message'],
  )
  assert.equal(desktopV3RenderItemKey(committedItems.at(-1)!), pendingKey)
  assert.equal(state.pendingUserByClientRequestId[operation.request.client_request_id], undefined)
  assert.equal(
    state.messagesBySession[operation.sessionId]?.items
      .filter((message) => message.id === operation.request.message_id).length,
    1,
  )

  state = applyDesktopV3LivePatchBatch(state, [{
    session_id: operation.sessionId,
    run_id: 'run-paused',
    stream_id: 'stream-paused',
    stream_kind: 'assistant_text',
    operation: 'append',
    step_id: 'step-paused',
    step: 1,
    live_seq_start: 1,
    live_seq_end: 2,
    offset_start: 25,
    offset_end: 38,
    text: ' delayed text',
    recorded_at: 4,
  }])
  const afterDelayedPausedPatch = buildDesktopV3ConversationRenderItems(
    selectRenderedSessionMessages(state, operation.sessionId),
  ).filter((item) => item.type !== 'live-working')
  assert.deepEqual(
    afterDelayedPausedPatch.map((item) => item.type),
    ['message', 'live-assistant', 'message'],
  )
  assert.equal(
    afterDelayedPausedPatch.filter((item) => item.type === 'message' && item.message.id === operation.request.message_id).length,
    1,
  )
})

test('continueDesktopV3Conversation appends message through canonical mutation and applies result', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'continue',
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  const calls: string[] = []
  const actions: DesktopV3CacheAction[] = []
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionConnected: async (sessionId: string) => {
        calls.push(`connect:${sessionId}`)
      },
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      actions.push(action)
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
      'connect:session-1',
      'dispatch:pendingUser.upsert',
      `message:session-1:${operation.request.message_id}`,
      'dispatch:mutation.messageResult',
    ])
    assert.equal(actions[0]?.type, 'pendingUser.upsert')
    assert.equal(actions[1]?.type, 'mutation.messageResult')
  } finally {
    restore()
  }
})


test('continueDesktopV3Conversation allows archived sessions so append can reactivate them', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-archived',
    prompt: 'continue',
  })
  const state = createEmptyDesktopV3CacheState()
  state.tombstonesBySession['session-archived'] = {
    session_id: 'session-archived',
    account_scope_id: 'acct',
    user_id: 'user',
    kind: 'archived',
    archived: true,
    deleted: false,
    updated_at: 1,
  }
  let appended = false
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionConnected: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: () => undefined,
    postAppendMessage: async () => {
      appended = true
      return makeMessageResponse(operation)
    },
  })

  try {
    await continueDesktopV3Conversation(operation)
    assert.equal(appended, true)
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

test('continueDesktopV3Conversation accepts terminal replay so retained operation can clear', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'continue',
  })
  const state = createEmptyDesktopV3CacheState()
  const actions: DesktopV3CacheAction[] = []
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionConnected: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      actions.push(action)
    },
    postAppendMessage: async () => ({
      ...makeMessageResponse(operation),
      run_intent: { ...makeMessageResponse(operation).run_intent!, status: 'cancelled' },
    }),
  })

  try {
    const response = await continueDesktopV3Conversation(operation)
    assert.equal(response.run_intent?.status, 'cancelled')
    assert.equal(actions.at(-1)?.type, 'mutation.messageResult')
  } finally {
    restore()
  }
})

test('continueDesktopV3Conversation still rejects dispatch-blocked run intent', async () => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-1',
    prompt: 'continue',
  })
  const state = createEmptyDesktopV3CacheState()
  const restore = setDesktopV3ExistingSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionConnected: async () => undefined,
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
