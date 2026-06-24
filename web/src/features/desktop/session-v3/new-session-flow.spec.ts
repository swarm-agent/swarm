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
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type {
  DesktopV3CacheAction,
  DesktopV3CacheState,
} from '../state/desktop-v3-cache-types'
import type { SessionStartResponse } from '../session-connection/contract.generated'
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

function makeStartResponse(operation: DesktopV3NewSessionOperation): SessionStartResponse {
  return {
    ok: true,
    contract_version: 1,
    session_id: operation.sessionId,
    snapshot: {
      event_seq: 2,
      session: {
        id: operation.sessionId,
        workspace_path: operation.createRequest.workspace_path,
        workspace_name: operation.createRequest.workspace_name ?? 'workspace',
        title: operation.createRequest.title ?? '',
        mode: operation.createRequest.mode ?? 'auto',
        created_at: 1,
        updated_at: 2,
        message_count: 1,
        last_message_at: 2,
      },
      messages: [{
        id: operation.firstMessageRequest.message_id,
        session_id: operation.sessionId,
        global_seq: 1,
        role: 'user',
        content: operation.firstMessageRequest.content,
        created_at: 2,
      }],
      current_run: {
        run_id: operation.firstMessageRequest.run_id,
        phase: 'pending_executor',
      },
      pending_permissions: [],
      active_plan: null,
      usage: null,
    },
    connection: {
      connection_id: `conn:${operation.sessionId}`,
      transport: 'websocket',
      protocol: 'swarm.session-stream.v1',
      stream_url: `/v3/sessions/${operation.sessionId}/stream`,
      resume_token: `resume:${operation.sessionId}`,
      ready_timeout_ms: 1000,
    },
    message: {
      id: operation.firstMessageRequest.message_id,
      session_id: operation.sessionId,
      global_seq: 1,
      role: 'user',
      content: operation.firstMessageRequest.content,
      created_at: 2,
    },
    run: {
      run_id: operation.firstMessageRequest.run_id,
      phase: 'pending_executor',
    },
    accepted_event_seq: 2,
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

test('startNewDesktopV3Session performs atomic start, selects, and applies accepted message before navigation', async () => {
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
      currentEndpointCursor: () => {
        throw new Error('new-session start must not read endpoint cursor')
      },
      connectSession: async () => {
        throw new Error('new-session start must not call connectSession separately')
      },
      startSession: async (request) => {
        capturedRequest = request
        calls.push(`start:${request.session_id}:${request.request_id}:${request.first_message.message_id}`)
        return makeStartResponse(operation)
      },
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      actions.push(action)
      calls.push(`dispatch:${action.type}`)
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
    assert.equal(result.startResponse.session_id, operation.sessionId)
    assert.equal(result.messageResponse.accepted_event_seq, 2)
    assert.equal(navigated, operation.sessionId)
    assert.deepEqual(calls, [
      'dispatch:pendingUser.upsert',
      `start:${operation.sessionId}:${operation.operationId}:${operation.firstMessageRequest.message_id}`,
      'dispatch:session.select',
      'dispatch:mutation.messageResult',
      `navigate:${operation.sessionId}`,
    ])
    assert.deepEqual(capturedRequest, {
      ...operation.createRequest,
      request_id: operation.operationId,
      first_message: operation.firstMessageRequest,
    })
    assert.equal(actions.find((action) => action.type === 'session.select')?.sessionId, operation.sessionId)
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
      currentEndpointCursor: () => '',
      connectSession: async () => undefined,
      startSession: async (request) => {
        calls.push(`start:${request.session_id}`)
        return makeStartResponse(operation)
      },
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => {
      actions.push(action)
      calls.push(`dispatch:${action.type}`)
      if (action.type === 'session.select') state.selectedSessionId = action.sessionId
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
      'dispatch:pendingUser.upsert',
      `start:${operation.sessionId}`,
      'dispatch:mutation.messageResult',
    ])
    assert.equal(actions.some((action) => action.type === 'session.select'), false)
    assert.equal(state.selectedSessionId, 'session-b')
    assert.equal(navigated, false)
  } finally {
    restore()
  }
})

test('startNewDesktopV3Session marks pending message failed when atomic start fails', async () => {
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
      currentEndpointCursor: () => '',
      connectSession: async () => undefined,
      startSession: async () => {
        throw new Error('network ambiguous')
      },
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: (action) => actions.push(action),
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
    assert.deepEqual(actions.map((action) => action.type), [
      'pendingUser.upsert',
      'mutation.messageResult',
    ])
  } finally {
    restore()
  }
})

test('startNewDesktopV3Session rejects atomic start response with mismatched session identity', async () => {
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
      currentEndpointCursor: () => '',
      connectSession: async () => undefined,
      startSession: async () => ({
        ...makeStartResponse(operation),
        session_id: 'different-session',
      }),
      ensureSessionHistory: async () => undefined,
      start: async () => undefined,
      stop: () => undefined,
    }),
    dispatch: () => undefined,
  })

  try {
    await assert.rejects(startNewDesktopV3Session({ operation }), /different session_id/)
  } finally {
    restore()
  }
})
