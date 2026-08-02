import test from 'node:test'
import assert from 'node:assert/strict'

import {
  DesktopV3RoutedNewSessionController,
  createDesktopV3CreateOnlySessionOperation,
  createDesktopV3NewSessionOperation,
  createDesktopV3RoutedComposerSnapshot,
  createDesktopV3RoutedDraftState,
  desktopV3RoutedRequestInput,
  createDesktopV3RoutedStartOperation,
  createDesktopV3RoutedWorktreePrimedState,
  loadDesktopV3RoutedStartOperation,
  persistDesktopV3RoutedStartOperation,
  restoreDesktopV3RoutedNewSessionState,
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

async function withAsyncSessionStorage(run: (storage: Map<string, string>) => Promise<void>): Promise<void> {
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
    await run(storage)
  } finally {
    globalThis.window = previousWindow
  }
}

function makeRoutedResult(sessionId: string) {
  return {
    ok: true as const,
    session_id: sessionId,
    title: 'Router title',
    starting_mode: 'auto',
    replayed: false,
    session: { id: sessionId },
    session_view: { identity: { session_id: sessionId } },
    first_message: { id: 'message-1', session_id: sessionId },
    projection: { session_id: sessionId, last_event_seq: 1 },
    mutation: { session_id: sessionId, message: { id: 'message-1' } },
  }
}

test('routed draft defaults to current workspace while preserving explicit local composer state', () => {
  assert.deepEqual(createDesktopV3RoutedDraftState('draft'), {
    phase: 'draft',
    prompt: 'draft',
    snapshot: { prompt: 'draft', attachments: [], selectedAction: null, selectedSkill: null, worktreePrimed: false, planModeRequested: false },
  })
  assert.deepEqual(createDesktopV3RoutedWorktreePrimedState('primed'), {
    phase: 'worktree-primed',
    prompt: 'primed',
    snapshot: { prompt: 'primed', attachments: [], selectedAction: null, selectedSkill: null, worktreePrimed: true, planModeRequested: false },
  })
  assert.equal('sessionId' in createDesktopV3RoutedDraftState(), false)
  assert.equal('workspace' in createDesktopV3RoutedWorktreePrimedState(), false)
})

test('routed operation persists one stable transport identity across reload', () => withSessionStorage(() => {
  const snapshot = createDesktopV3RoutedComposerSnapshot({
    prompt: ' route this ',
    attachments: [{ staging_id: ' staged-1 ', modality: ' image ', file_type: ' png ' }],
    selectedAction: { id: 'action-1', arguments: ['--exact', ' value '] },
    selectedSkill: { canonicalName: 'skill/example', scope: 'workspace' },
    worktreePrimed: true,
    planModeRequested: true,
  })
  const operation = createDesktopV3RoutedStartOperation({
    snapshot,
    agentName: ' swarm ',
    metadata: { source: 'desktop-v3' },
  })

  assert.equal(operation.version, 1)
  assert.deepEqual(operation.snapshot, snapshot)
  assert.equal(operation.request.input, 'route this')
  assert.equal(operation.request.managed_worktree_requested, true)
  assert.equal(operation.request.plan_mode_requested, true)
  assert.equal(operation.request.agent_name, 'swarm')
  assert.equal(operation.request.client_request_id, `desktop-v3-routed:${operation.operationId}`)
  assert.equal(operation.request.idempotency_key, operation.request.client_request_id)
  assert.deepEqual(operation.request.media, [{ staging_id: 'staged-1', modality: 'image', file_type: 'png' }])
  assert.equal('session_id' in operation.request, false)
  assert.equal('workspace_path' in operation.request, false)
  assert.equal('mode' in operation.request, false)
  assert.equal('model' in operation.request, false)

  persistDesktopV3RoutedStartOperation(operation)
  assert.deepEqual(loadDesktopV3RoutedStartOperation(), JSON.parse(JSON.stringify(operation)))
  const restored = restoreDesktopV3RoutedNewSessionState()
  assert.equal(restored.phase, 'failed')
  if (restored.phase === 'failed') {
    assert.deepEqual(restored.snapshot, snapshot)
    assert.equal(restored.prompt, ' route this ')
    assert.equal(restored.operation.operationId, operation.operationId)
    assert.equal(restored.operation.request.client_request_id, operation.request.client_request_id)
  }
}))

test('routed worktree prime carries explicit request authority without mutating user input or introducing a name field', () => {
  const snapshot = createDesktopV3RoutedComposerSnapshot({ prompt: 'route me', worktreePrimed: true })
  assert.equal(desktopV3RoutedRequestInput(snapshot), 'route me')
  assert.equal(createDesktopV3RoutedStartOperation({ snapshot }).request.managed_worktree_requested, true)
  assert.deepEqual(Object.keys(snapshot), ['prompt', 'attachments', 'selectedAction', 'selectedSkill', 'worktreePrimed', 'planModeRequested'])
})

test('routed operation omits optional authority fields when the caller supplies no value', () => {
  const operation = createDesktopV3RoutedStartOperation({ prompt: 'route me' })

  assert.deepEqual(Object.keys(operation.request).sort(), [
    'client_request_id', 'idempotency_key', 'input', 'managed_worktree_requested', 'media', 'metadata', 'plan_mode_requested',
  ])
})

test('routed operation rejects malformed reserved identity', () => {
  assert.throws(() => createDesktopV3RoutedStartOperation({
    prompt: 'route me',
    identity: { operationId: 'operation-a', clientRequestId: 'desktop-v3-routed:operation-b' },
  }), /operation identity is invalid/)
})

test('routed controller reserves one identity before staging and reuses it for submit', async () => withAsyncSessionStorage(async () => {
  let postedClientRequestID = ''
  const controller = new DesktopV3RoutedNewSessionController(async (request) => {
    postedClientRequestID = request.client_request_id
    return makeRoutedResult('canonical-session')
  }, createDesktopV3RoutedDraftState('route me'))

  const first = controller.prepareOperationIdentity()
  const second = controller.prepareOperationIdentity()
  assert.deepEqual(second, first)

  const state = await controller.submit({
    prompt: 'route me',
    media: [{ staging_id: 'staged-1', modality: 'image', file_type: 'png' }],
  })
  assert.equal(state.phase, 'resolved')
  assert.equal(postedClientRequestID, first.clientRequestId)

  const nextController = new DesktopV3RoutedNewSessionController(async () => makeRoutedResult('canonical-session-2'), createDesktopV3RoutedDraftState('next'))
  const discarded = nextController.prepareOperationIdentity()
  nextController.startDraft('replacement')
  assert.notDeepEqual(nextController.prepareOperationIdentity(), discarded)
}))

test('routed controller keeps the pending prompt local and resolves only canonical response', async () => withAsyncSessionStorage(async (storage) => {
  let resolveRequest!: (value: ReturnType<typeof makeRoutedResult>) => void
  const requests: unknown[] = []
  const controller = new DesktopV3RoutedNewSessionController((request) => {
    requests.push(request)
    return new Promise((resolve) => { resolveRequest = resolve })
  }, createDesktopV3RoutedDraftState('route me'))
  const phases: string[] = []
  controller.subscribe((state) => phases.push(state.phase))

  const pending = controller.submit({ prompt: 'route me' })
  const routing = controller.getState()
  assert.equal(routing.phase, 'routing')
  assert.equal(routing.prompt, 'route me')
  assert.equal(requests.length, 1)
  assert.equal(storage.size, 1)
  if (routing.phase !== 'routing') throw new Error('expected routing state')

  resolveRequest(makeRoutedResult('canonical-session'))
  const resolved = await pending
  assert.equal(resolved.phase, 'resolved')
  if (resolved.phase === 'resolved') {
    assert.equal(resolved.result.session_id, 'canonical-session')
  }
  assert.deepEqual(phases, ['routing', 'resolved'])
  assert.equal(storage.size, 1)
  if (resolved.phase !== 'resolved') throw new Error('expected resolved state')
  controller.acknowledgeResolved(resolved.operation.operationId)
  assert.equal(storage.size, 0)
}))

test('routed activation rejection restores the exact operation and retry identity', async () => withAsyncSessionStorage(async (storage) => {
  const requestIDs: string[] = []
  const controller = new DesktopV3RoutedNewSessionController(async (request) => {
    requestIDs.push(request.client_request_id)
    return makeRoutedResult('canonical-session')
  }, createDesktopV3RoutedDraftState('activate me'))
  const snapshot = createDesktopV3RoutedComposerSnapshot({
    prompt: ' activate me exactly ',
    attachments: [{ staging_id: 'staged-1', modality: 'image', file_type: 'png' }],
    selectedAction: { id: 'action-1' },
    selectedSkill: { canonicalName: 'skill-1' },
    worktreePrimed: true,
  })

  const resolved = await controller.submit({ snapshot })
  assert.equal(resolved.phase, 'resolved')
  if (resolved.phase !== 'resolved') throw new Error('expected resolved state')
  assert.equal(storage.size, 1)

  const failed = controller.rejectResolved(resolved.operation.operationId, new Error('activation failed'))
  assert.equal(failed.phase, 'failed')
  assert.deepEqual(failed.snapshot, snapshot)
  assert.match(failed.error, /activation failed/)
  assert.equal(storage.size, 1)

  const retried = await controller.retry()
  assert.equal(retried.phase, 'resolved')
  assert.equal(requestIDs.length, 2)
  assert.equal(requestIDs[0], requestIDs[1])
}))

test('routed failure restores the exact composer snapshot and retries the same operation', async () => withAsyncSessionStorage(async () => {
  const requestIDs: string[] = []
  let attempts = 0
  const controller = new DesktopV3RoutedNewSessionController(async (request) => {
    requestIDs.push(request.client_request_id)
    attempts += 1
    if (attempts === 1) throw new Error('network ambiguous')
    return makeRoutedResult('canonical-session')
  }, createDesktopV3RoutedDraftState('retry me'))
  const snapshot = createDesktopV3RoutedComposerSnapshot({
    prompt: ' retry me exactly ',
    attachments: [
      { staging_id: 'staged-2', modality: 'image', file_type: 'png' },
      { staging_id: 'staged-1', modality: 'file', file_type: 'pdf' },
    ],
    selectedAction: { id: 'action-7', name: 'Deploy', arguments: ['--keep-order'] },
    selectedSkill: { canonicalName: 'skill/release', description: ' exact ' },
    worktreePrimed: true,
  })

  const failed = await controller.submit({ snapshot })
  assert.equal(failed.phase, 'failed')
  if (failed.phase === 'failed') {
    assert.match(failed.error, /network ambiguous/)
    assert.deepEqual(failed.snapshot, snapshot)
    assert.equal(failed.prompt, snapshot.prompt)
    assert.equal(failed.operation.request.input, snapshot.prompt.trim())
    assert.equal(failed.operation.request.managed_worktree_requested, true)
    assert.deepEqual(failed.operation.request.media, snapshot.attachments)
  }
  const resolved = await controller.retry()
  assert.equal(resolved.phase, 'resolved')
  assert.equal(requestIDs.length, 2)
  assert.equal(requestIDs[0], requestIDs[1])
}))

test('routed interruption recovery rejects corrupted snapshots and retains exact valid snapshots', () => withSessionStorage((storage) => {
  const snapshot = createDesktopV3RoutedComposerSnapshot({
    prompt: ' interrupted draft ',
    attachments: [{ staging_id: 'stage-a', modality: 'image' }],
    selectedAction: { id: 'action-a' },
    selectedSkill: { canonicalName: 'skill-a' },
    worktreePrimed: false,
  })
  const operation = createDesktopV3RoutedStartOperation({ snapshot })
  persistDesktopV3RoutedStartOperation(operation)

  const recovered = restoreDesktopV3RoutedNewSessionState()
  assert.equal(recovered.phase, 'failed')
  if (recovered.phase === 'failed') {
    assert.deepEqual(recovered.snapshot, snapshot)
    assert.equal(recovered.operation.request.client_request_id, operation.request.client_request_id)
  }

  const corrupted = JSON.parse([...storage.values()][0] ?? '{}')
  corrupted.snapshot.attachments[0].staging_id = ''
  storage.set('swarm.desktop.v3.routed-new-session.v1', JSON.stringify(corrupted))
  assert.equal(loadDesktopV3RoutedStartOperation(), null)
  assert.equal(storage.size, 0)
}))

test('routed invalidation clears only the invalidated operation and preserves the newer exact draft', async () => withAsyncSessionStorage(async (storage) => {
  let resolveRequest!: (value: ReturnType<typeof makeRoutedResult>) => void
  const controller = new DesktopV3RoutedNewSessionController(() => new Promise((resolve) => {
    resolveRequest = resolve
  }), createDesktopV3RoutedDraftState('old prompt'))
  const oldSnapshot = createDesktopV3RoutedComposerSnapshot({
    prompt: 'old prompt',
    attachments: [{ staging_id: 'old-stage' }],
    selectedAction: { id: 'old-action' },
    selectedSkill: null,
    worktreePrimed: true,
  })
  const pending = controller.submit({ snapshot: oldSnapshot })

  const newSnapshot = createDesktopV3RoutedComposerSnapshot({
    prompt: 'new prompt ',
    attachments: [{ staging_id: 'new-stage' }],
    selectedAction: null,
    selectedSkill: { canonicalName: 'new-skill' },
    worktreePrimed: false,
  })
  const draft = controller.startDraft(newSnapshot.prompt, newSnapshot)
  assert.deepEqual(draft.snapshot, newSnapshot)
  assert.equal(storage.size, 0)

  resolveRequest(makeRoutedResult('stale-session'))
  const stale = await pending
  assert.equal(stale.phase, 'draft')
  assert.equal(stale.prompt, newSnapshot.prompt)
}))

test('stale routed success cannot activate after a newer local draft starts', async () => withAsyncSessionStorage(async () => {
  let resolveRequest!: (value: ReturnType<typeof makeRoutedResult>) => void
  const controller = new DesktopV3RoutedNewSessionController(() => new Promise((resolve) => {
    resolveRequest = resolve
  }), createDesktopV3RoutedDraftState('old prompt'))

  const pending = controller.submit({ prompt: 'old prompt' })
  controller.startDraft('new prompt')
  resolveRequest(makeRoutedResult('stale-session'))
  const result = await pending

  assert.equal(result.phase, 'draft')
  assert.equal(controller.getState().phase, 'draft')
  assert.equal(controller.getState().prompt, 'new prompt')
}))

test('stale routed failure cannot replace a newer local draft', async () => withAsyncSessionStorage(async () => {
  let rejectRequest!: (reason: Error) => void
  const controller = new DesktopV3RoutedNewSessionController(() => new Promise((_resolve, reject) => {
    rejectRequest = reject
  }), createDesktopV3RoutedDraftState('old prompt'))

  const pending = controller.submit({ prompt: 'old prompt' })
  controller.startDraft('new prompt')
  rejectRequest(new Error('stale failure'))
  const result = await pending

  assert.equal(result.phase, 'draft')
  assert.equal(controller.getState().prompt, 'new prompt')
}))

test('mismatched routed response remains local failed state', async () => withAsyncSessionStorage(async () => {
  const controller = new DesktopV3RoutedNewSessionController(async () => ({
    ...makeRoutedResult('canonical-session'),
    projection: { session_id: 'other-session', last_event_seq: 1 },
  }), createDesktopV3RoutedDraftState('route me'))

  const state = await controller.submit({ prompt: 'route me' })
  assert.equal(state.phase, 'failed')
  if (state.phase === 'failed') assert.match(state.error, /mismatched projection/)
}))

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
    const pendingAction = actions.find((action) => action.type === 'pendingUser.upsert')
    assert.equal(pendingAction?.type, 'pendingUser.upsert')
    if (pendingAction?.type === 'pendingUser.upsert') {
      assert.equal(pendingAction.input.runId, operation.firstMessageRequest.run_id)
    }
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
      'release',
      'dispatch:pendingUser.upsert',
      `message:${operation.sessionId}:${operation.firstMessageRequest.message_id}`,
      'dispatch:mutation.messageResult',
    ])
    assert.equal(actions.find((action) => action.type === 'mutation.sessionCreateResult')?.sidebarScopeId, 'scope-primed')
    const pendingAction = actions.find((action) => action.type === 'pendingUser.upsert')
    assert.equal(pendingAction?.type, 'pendingUser.upsert')
    if (pendingAction?.type === 'pendingUser.upsert') {
      assert.equal(pendingAction.input.runId, operation.firstMessageRequest.run_id)
    }
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
