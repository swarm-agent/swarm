import test from 'node:test'
import assert from 'node:assert/strict'

import {
  createDesktopV3CreateOnlySessionOperation,
  createDesktopV3NewSessionOperation,
  desktopV3NewSessionOperationMatchesRoute,
  loadDesktopV3NewSessionOperation,
  persistDesktopV3NewSessionOperation,
  setDesktopV3NewSessionFlowDepsForTests,
  startDesktopV3CreateOnlySession,
  startNewDesktopV3Session,
  type DesktopV3NewSessionOperation,
} from './new-session-flow'
import type { DesktopV3RealtimeController, DesktopV3RealtimeLease } from '../realtime/v3-realtime-controller'
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
      updated_at: 2,
      message_count: 0,
      last_message_at: 0,
    },
    projection: {
      session_id: operation.sessionId,
      last_event_seq: 1,
      projection_high_watermark_seq: 1,
      updated_at: 2,
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
  assert.equal(operation.createRequest.client_request_id, `desktop-v3-start:${operation.operationId}:create`)
  assert.equal(operation.createRequest.swarm_id, 'swarm-self')
  assert.equal(operation.createRequest.workspace_binding_id, 'binding-self')
  assert.equal(operation.createRequest.agent_name, 'swarm')
  assert.equal(operation.createRequest.preference, undefined)
  assert.equal(operation.createRequest.mode, 'auto')
  assert.equal(operation.firstMessageRequest.client_request_id, `desktop-v3-first-message:${operation.operationId}`)
  assert.equal(operation.firstMessageRequest.message_id, `desktop-v3-message:${operation.operationId}`)
  assert.equal(operation.firstMessageRequest.run_id, `desktop-v3-run:${operation.operationId}`)
  assert.equal(operation.firstMessageRequest.content, 'hello')
})

test('Path A operation defaults omitted session mode to auto', () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'hello',
    agentName: 'swarm',
  })

  assert.equal(operation.createRequest.mode, 'auto')
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

test('stored Path A operation must match the selected primary route authority', () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'hello',
    agentName: 'swarm',
  })

  assert.equal(desktopV3NewSessionOperationMatchesRoute(operation, route), true)
  assert.equal(desktopV3NewSessionOperationMatchesRoute(operation, {
    ...route,
    swarmId: 'other-swarm',
  }), false)
  assert.equal(desktopV3NewSessionOperationMatchesRoute(operation, {
    ...route,
    workspaceBindingId: 'other-binding',
  }), false)
  assert.equal(desktopV3NewSessionOperationMatchesRoute(operation, {
    ...route,
    targetKind: 'self',
  }), false)
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

test('Path A operation rejects partial preference instead of sending empty provider/model overrides', () => {
  assert.throws(() => createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'hello',
    agentName: 'swarm',
    preference: { provider: '', model: 'gpt-5.4', thinking: 'medium' },
  }), /provider, model, and thinking/)

  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'hello',
    agentName: 'swarm',
    preference: { provider: ' codex ', model: ' gpt-5.4 ', thinking: ' medium ', serviceTier: ' fast ' },
  })
  assert.deepEqual(operation.createRequest.preference, {
    provider: 'codex',
    model: 'gpt-5.4',
    thinking: 'medium',
    service_tier: 'fast',
    context_mode: undefined,
  })
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

test('Path A discards retained operations missing agent_name', () => withSessionStorage((storage) => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'retry me',
    agentName: 'swarm',
  })
  const retained = JSON.parse(JSON.stringify(operation)) as DesktopV3NewSessionOperation
  retained.createRequest.agent_name = ''
  persistDesktopV3NewSessionOperation(retained)

  assert.equal(loadDesktopV3NewSessionOperation('/workspace'), null)
  assert.equal(storage.size, 0)
}))

test('Path A discards retained operations with legacy branch worktree mode', () => withSessionStorage((storage) => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'retry me',
    agentName: 'swarm',
    worktree: { mode: 'on', branchName: 'agent/stage-4' },
  })
  const retained = JSON.parse(JSON.stringify(operation)) as DesktopV3NewSessionOperation
  retained.createRequest.worktree_mode = 'branch'
  persistDesktopV3NewSessionOperation(retained)

  assert.equal(loadDesktopV3NewSessionOperation('/workspace'), null)
  assert.equal(storage.size, 0)
}))

test('Path A retries valid retained operation unchanged', () => withSessionStorage(() => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'retry me',
    agentName: 'swarm',
    worktree: { mode: 'on', branchName: 'agent/stage-4' },
  })
  persistDesktopV3NewSessionOperation(operation)

  const loaded = loadDesktopV3NewSessionOperation('/workspace')
  assert.deepEqual(loaded?.createRequest, JSON.parse(JSON.stringify(operation.createRequest)))
  assert.deepEqual(loaded?.firstMessageRequest, JSON.parse(JSON.stringify(operation.firstMessageRequest)))
}))

test('startNewDesktopV3Session creates, appends first message, selects, and applies result before navigation', async () => {
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
  let capturedRequest: unknown
  const restore = setDesktopV3NewSessionFlowDepsForTests({
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
    postCreateSession: async (request) => {
      capturedRequest = request
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
    assert.equal(result.createResponse.session_id, operation.sessionId)
    assert.equal(result.messageResponse.run_intent?.event_seq, 2)
    assert.equal(navigated, operation.sessionId)
    assert.deepEqual(calls, [
      `create:${operation.sessionId}`,
      'dispatch:mutation.sessionCreateResult',
      'dispatch:session.select',
      `connect:${operation.sessionId}`,
      'dispatch:pendingUser.upsert',
      `message:${operation.sessionId}:${operation.firstMessageRequest.message_id}`,
      'dispatch:mutation.messageResult',
      `navigate:${operation.sessionId}`,
    ])
    assert.deepEqual(capturedRequest, operation.createRequest)
    assert.equal(actions.find((action) => action.type === 'session.select')?.sessionId, operation.sessionId)
  } finally {
    restore()
  }
})

test('startNewDesktopV3Session primes sidebar bootstrap before create on first desktop load', async () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'start',
    agentName: 'swarm',
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  const calls: string[] = []
  const actions: DesktopV3CacheAction[] = []
  let released = false
  const restore = setDesktopV3NewSessionFlowDepsForTests({
    getSnapshot: () => state,
    ensureSidebarBootstrap: async () => {
      calls.push('bootstrap')
      state.desktopSidebarBootstrap.scopeId = 'scope-primed'
      return { response: {} as never }
    },
    retainRealtimeController: (): DesktopV3RealtimeLease => {
      calls.push('retain')
      return {
        ready: Promise.resolve(),
        release: () => {
          released = true
          calls.push('release')
        },
      }
    },
    requireControllerReady: async (): Promise<DesktopV3RealtimeController> => ({
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
    await startNewDesktopV3Session({ operation })

    assert.equal(released, true)
    assert.deepEqual(calls, [
      'bootstrap',
      'retain',
      `create:${operation.sessionId}`,
      'dispatch:mutation.sessionCreateResult',
      'dispatch:session.select',
      `connect:${operation.sessionId}`,
      'dispatch:pendingUser.upsert',
      `message:${operation.sessionId}:${operation.firstMessageRequest.message_id}`,
      'dispatch:mutation.messageResult',
      'release',
    ])
    assert.equal(actions.find((action) => action.type === 'mutation.sessionCreateResult')?.sidebarScopeId, 'scope-primed')
  } finally {
    restore()
  }
})

test('startDesktopV3CreateOnlySession creates, selects, connects, and navigates without appending a message', async () => {
  const operation = createDesktopV3CreateOnlySessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    title: 'Worktree title',
    agentName: 'swarm',
    worktree: { mode: 'on', branchName: 'agent/worktree-title' },
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'scope-global'
  const calls: string[] = []
  const actions: DesktopV3CacheAction[] = []
  const restore = setDesktopV3NewSessionFlowDepsForTests({
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
    postCreateSession: async (request) => {
      calls.push(`create:${request.session_id}`)
      return makeCreateResponse(operation)
    },
    postAppendMessage: async () => {
      throw new Error('create-only start must not append a first message')
    },
  })

  try {
    let navigated = ''
    const result = await startDesktopV3CreateOnlySession({
      operation,
      onSessionStarted: (sessionId) => {
        calls.push(`navigate:${sessionId}`)
        navigated = sessionId
      },
    })

    assert.equal(result.sessionId, operation.sessionId)
    assert.equal(result.createResponse.session_id, operation.sessionId)
    assert.equal(navigated, operation.sessionId)
    assert.deepEqual(calls, [
      `create:${operation.sessionId}`,
      'dispatch:mutation.sessionCreateResult',
      'dispatch:session.select',
      `connect:${operation.sessionId}`,
      `navigate:${operation.sessionId}`,
    ])
    assert.equal(operation.createRequest.title, 'Worktree title')
    assert.equal(operation.createRequest.worktree_mode, 'on')
    assert.equal(operation.createRequest.worktree_branch_name, 'agent/worktree-title')
    assert.equal(actions.some((action) => action.type === 'pendingUser.upsert'), false)
    assert.equal(actions.some((action) => action.type === 'mutation.messageResult'), false)
  } finally {
    restore()
  }
})

test('startNewDesktopV3Session skips selection when delayed start resolves after route unmount', async () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'start',
    agentName: 'swarm',
  })
  const state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'scope-global'
  state.selectedSessionId = 'session-b'
  const calls: string[] = []
  const actions: DesktopV3CacheAction[] = []
  const restore = setDesktopV3NewSessionFlowDepsForTests({
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
      if (action.type === 'session.select') state.selectedSessionId = action.sessionId
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
    let mounted = false
    let navigated = false
    const result = await startNewDesktopV3Session({
      operation,
      shouldSelectSession: () => mounted,
      onSessionStarted: () => {
        if (mounted) navigated = true
      },
    })

    assert.equal(result.sessionId, operation.sessionId)
    assert.deepEqual(calls, [
      `create:${operation.sessionId}`,
      'dispatch:mutation.sessionCreateResult',
      `connect:${operation.sessionId}`,
      'dispatch:pendingUser.upsert',
      `message:${operation.sessionId}:${operation.firstMessageRequest.message_id}`,
      'dispatch:mutation.messageResult',
    ])
    assert.equal(actions.some((action) => action.type === 'session.select'), false)
    assert.equal(state.selectedSessionId, 'session-b')
    assert.equal(navigated, false)
  } finally {
    restore()
  }
})

test('startNewDesktopV3Session marks pending message failed when create fails', async () => {
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
      ensureSessionConnected: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => actions.push(action),
    postCreateSession: async () => {
      throw new Error('network ambiguous')
    },
    postAppendMessage: async () => makeMessageResponse(operation),
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
    assert.deepEqual(actions.map((action) => action.type), [])
  } finally {
    restore()
  }
})

test('startNewDesktopV3Session rejects create response with mismatched session identity', async () => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    route,
    prompt: 'start',
    agentName: 'swarm',
  })
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'scope-global'
  const restore = setDesktopV3NewSessionFlowDepsForTests({
    getSnapshot: () => state,
    requireControllerReady: async () => ({
      ensureSessionConnected: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: () => undefined,
    postCreateSession: async () => ({
      ...makeCreateResponse(operation),
      session_id: 'different-session',
    }),
    postAppendMessage: async () => makeMessageResponse(operation),
  })

  try {
    await assert.rejects(startNewDesktopV3Session({ operation }), /different session_id/)
  } finally {
    restore()
  }
})
