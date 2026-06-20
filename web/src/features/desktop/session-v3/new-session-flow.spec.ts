import test from 'node:test'
import assert from 'node:assert/strict'

import {
  createDesktopV3NewSessionOperation,
  loadDesktopV3NewSessionOperation,
  persistDesktopV3NewSessionOperation,
  setDesktopV3NewSessionFlowDepsForTests,
  startNewDesktopV3Session,
  type DesktopV3NewSessionOperation,
} from './new-session-flow'
import { postDesktopV3CreateSession } from './write-api'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type {
  DesktopV3CacheAction,
  DesktopV3CacheState,
  SessionCreateMutationResponse,
  SessionMessageMutationResponse,
} from '../state/desktop-v3-cache-types'
import type { DesktopChatRoute } from '../chat/services/chat-routing'

const route: DesktopChatRoute = {
  id: 'swarm:swarm-self:binding:binding-self',
  label: 'Self',
  swarmId: 'swarm-self',
  targetKind: 'host',
  targetRelationship: 'self',
  hostSwarmId: 'swarm-self',
  hostSwarmName: 'Host',
  hostWorkspacePath: '/workspace',
  hostWorkspaceName: 'workspace',
  runtimeWorkspacePath: '/workspace',
  workspaceBindingId: 'binding-self',
  workspaceName: 'workspace',
}

function makeCreateResponse(operation: DesktopV3NewSessionOperation): SessionCreateMutationResponse {
  return {
    ok: true,
    session_id: operation.sessionId,
    session: {
      id: operation.sessionId,
      workspace_path: operation.createRequest.workspace_path,
      workspace_name: operation.createRequest.workspace_name ?? 'workspace',
      title: operation.createRequest.title ?? '',
      mode: operation.createRequest.mode ?? 'auto',
      created_at: 1,
      updated_at: 1,
      message_count: 0,
      last_message_at: 0,
    },
    projection: {
      session_id: operation.sessionId,
      last_event_seq: 1,
      projection_high_watermark_seq: 1,
      updated_at: 1,
    },
    mutation: {},
    realtime_outbox: null,
  }
}

function makeMessageResponse(operation: DesktopV3NewSessionOperation): SessionMessageMutationResponse {
  return {
    ok: true,
    session_id: operation.sessionId,
    message: {
      id: operation.firstMessageRequest.message_id,
      session_id: operation.sessionId,
      global_seq: 1,
      role: 'user',
      content: operation.firstMessageRequest.content,
      created_at: 2,
    },
    run_intent: {
      session_id: operation.sessionId,
      run_id: operation.firstMessageRequest.run_id,
      status: 'pending_executor',
      created_at: 2,
      updated_at: 2,
      event_seq: 2,
    },
    mutation: {},
    realtime_outbox: null,
  }
}

function withSessionStorage(run: () => void): void {
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
    run()
  } finally {
    globalThis.window = previousWindow
  }
}

test('Path A operation contains stable create and first-message wire payloads', () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: ' hello ',
    mode: 'auto',
    agentName: 'swarm',
  })

  assert.equal(operation.version, 1)
  assert.equal(operation.createRequest.session_id, operation.sessionId)
  assert.equal(operation.createRequest.client_request_id, `desktop-v3-create:${operation.operationId}`)
  assert.equal(operation.createRequest.swarm_id, 'swarm-self')
  assert.equal(operation.createRequest.workspace_binding_id, 'binding-self')
  assert.equal(operation.createRequest.agent_name, 'swarm')
  assert.equal(operation.firstMessageRequest.client_request_id, `desktop-v3-first-message:${operation.operationId}`)
  assert.equal(operation.firstMessageRequest.message_id, `desktop-v3-message:${operation.operationId}`)
  assert.equal(operation.firstMessageRequest.run_id, `desktop-v3-run:${operation.operationId}`)
  assert.equal(operation.firstMessageRequest.content, 'hello')
})

test('Path A operation rejects missing writable create authority before HTTP', () => {
  assert.throws(() => createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route: { ...route, workspaceBindingId: '' },
    prompt: 'hello',
    agentName: 'swarm',
  }), /workspace_binding_id/)
})

test('Path A operation rejects missing agent_name before HTTP', () => {
  assert.throws(() => createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'hello',
    agentName: '  ',
  }), /agent_name/)
})

test('Path A create HTTP POST body includes selected agent_name', async () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'hello',
    agentName: 'selected-agent',
  })
  const originalFetch = globalThis.fetch
  let capturedUrl = ''
  let capturedInit: RequestInit | undefined
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    capturedUrl = String(input)
    capturedInit = init
    return new Response(JSON.stringify(makeCreateResponse(operation)), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    await postDesktopV3CreateSession(operation.createRequest)
  } finally {
    globalThis.fetch = originalFetch
  }

  const body = JSON.parse(String(capturedInit?.body))
  assert.equal(capturedUrl, '/v3/sessions')
  assert.equal(capturedInit?.method, 'POST')
  assert.equal(body.session_id, operation.sessionId)
  assert.equal(body.client_request_id, operation.createRequest.client_request_id)
  assert.equal(body.workspace_binding_id, 'binding-self')
  assert.equal(body.swarm_id, 'swarm-self')
  assert.equal(body.agent_name, 'selected-agent')
})

test('Path A operation persists exact retry payload in sessionStorage', () => withSessionStorage(() => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'retry me',
    agentName: 'swarm',
  })
  persistDesktopV3NewSessionOperation(operation)

  const loaded = loadDesktopV3NewSessionOperation('/workspace')
  assert.equal(loaded?.operationId, operation.operationId)
  assert.equal(loaded?.sessionId, operation.sessionId)
  assert.deepEqual(loaded?.createRequest, JSON.parse(JSON.stringify(operation.createRequest)))
  assert.deepEqual(loaded?.firstMessageRequest, JSON.parse(JSON.stringify(operation.firstMessageRequest)))
}))

test('startNewDesktopV3Session performs create, subscribe/select, then first message before navigation', async () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'start',
    agentName: 'swarm',
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'scope-global'
  const calls: string[] = []
  const actions: DesktopV3CacheAction[] = []
  const restore = setDesktopV3NewSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionSubscription: (sessionId: string) => calls.push(`subscribe:${sessionId}`),
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      actions.push(action)
      calls.push(`dispatch:${action.type}`)
    },
    postCreateSession: async (request) => {
      calls.push(`create:${request.session_id}`)
      return makeCreateResponse(operation)
    },
    postAppendMessage: async (sessionId, request) => {
      calls.push(`message:${sessionId}:${request.message_id}`)
      return makeMessageResponse(operation)
    },
  })

  try {
    let navigated = ''
    const result = await startNewDesktopV3Session({
      operation,
      onSessionStarted: (sessionId) => {
        calls.push(`navigate:${sessionId}`)
        navigated = sessionId
      },
    })

    assert.equal(result.sessionId, operation.sessionId)
    assert.equal(navigated, operation.sessionId)
    assert.deepEqual(calls, [
      `create:${operation.sessionId}`,
      'dispatch:mutation.sessionCreateResult',
      `subscribe:${operation.sessionId}`,
      'dispatch:session.select',
      'dispatch:pendingUser.upsert',
      `message:${operation.sessionId}:${operation.firstMessageRequest.message_id}`,
      'dispatch:mutation.messageResult',
      `navigate:${operation.sessionId}`,
    ])
    assert.equal(actions.find((action) => action.type === 'session.select')?.sessionId, operation.sessionId)
  } finally {
    restore()
  }
})

test('startNewDesktopV3Session keeps operation unresolved when first message fails', async () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'start',
    agentName: 'swarm',
  })
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'scope-global'
  const actions: DesktopV3CacheAction[] = []
  const restore = setDesktopV3NewSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionSubscription: () => undefined,
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => actions.push(action),
    postCreateSession: async () => makeCreateResponse(operation),
    postAppendMessage: async () => ({ ok: false, error: 'network ambiguous' }),
  })

  try {
    let navigated = false
    await assert.rejects(startNewDesktopV3Session({
      operation,
      onSessionStarted: () => {
        navigated = true
      },
    }), /network ambiguous/)
    assert.equal(navigated, false)
    assert.equal(actions.at(-1)?.type, 'mutation.messageResult')
  } finally {
    restore()
  }
})
