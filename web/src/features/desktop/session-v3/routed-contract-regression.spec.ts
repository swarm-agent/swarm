import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

import {
  DesktopV3RoutedNewSessionController,
  createDesktopV3RoutedComposerSnapshot,
  createDesktopV3RoutedDraftState,
  createDesktopV3RoutedStartOperation,
  type DesktopV3RoutedStartResult,
} from './new-session-flow'

const workspaceAuthority = {
  workspace_path: '/workspace', host_workspace_path: '/workspace', runtime_workspace_path: '/workspace',
  workspace_binding_id: 'binding-self', swarm_id: 'swarm-self', target_kind: 'host' as const, target_relationship: 'self' as const,
}

function routedResult(sessionId: string): DesktopV3RoutedStartResult {
  return {
    ok: true,
    session_id: sessionId,
    title: 'Router title',
    starting_mode: 'auto',
    replayed: false,
    session: { id: sessionId },
    session_view: {},
    first_message: { id: 'message-1', session_id: sessionId },
    projection: { session_id: sessionId },
    mutation: { session_id: sessionId },
  }
}

async function withRoutedStorage<T>(run: (storage: Map<string, string>) => Promise<T> | T): Promise<T> {
  const previousWindow = globalThis.window
  const storage = new Map<string, string>()
  globalThis.window = {
    sessionStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => { storage.set(key, value) },
      removeItem: (key: string) => { storage.delete(key) },
    },
  } as Window & typeof globalThis
  try {
    return await run(storage)
  } finally {
    globalThis.window = previousWindow
  }
}

test('routed contract exposes only local composer intent before canonical resolution', () => {
  const snapshot = createDesktopV3RoutedComposerSnapshot({
    prompt: 'Implement the routed change',
    attachments: [{ staging_id: 'stage-1', modality: 'image', file_type: 'png' }],
    selectedAction: { id: 'action-1' },
    selectedSkill: { canonicalName: 'skill-1' },
    worktreePrimed: true,
    planModeRequested: true,
  })
  const operation = createDesktopV3RoutedStartOperation({
    workspace: workspaceAuthority,
    snapshot,
    metadata: { source: 'desktop-v3' },
  })

  assert.deepEqual(Object.keys(operation.request).sort(), [
    'client_request_id', 'host_workspace_path', 'idempotency_key', 'input', 'managed_worktree_requested', 'media', 'metadata', 'plan_mode_requested',
    'runtime_workspace_path', 'swarm_id', 'target_kind', 'target_relationship', 'workspace_binding_id', 'workspace_path',
  ])
  for (const forbidden of [
    'session_id', 'title', 'mode',
    'preference', 'model', 'model_profile', 'worktree_name', 'worktree_branch_name',
  ]) {
    assert.equal(Object.hasOwn(operation.request, forbidden), false, `routed request must not preselect ${forbidden}`)
  }
  assert.equal('sessionId' in createDesktopV3RoutedDraftState(snapshot.prompt, snapshot), false)
  assert.equal(operation.request.input, 'Implement the routed change')
  assert.equal(operation.request.managed_worktree_requested, true)
  assert.equal(operation.request.plan_mode_requested, true)
})

test('routed pending shell remains local and failure restores exact controls for one-id retry', async () => {
  await withRoutedStorage(async (storage) => {
    const requests: string[] = []
    let attempt = 0
    const initialSnapshot = createDesktopV3RoutedComposerSnapshot({
      prompt: ' Keep exact spacing ',
      attachments: [{ staging_id: 'stage-2', modality: 'image', file_type: 'png' }],
      selectedAction: { id: 'action-2', arguments: ['--exact'] },
      selectedSkill: { canonicalName: 'skill-2' },
      worktreePrimed: true,
      planModeRequested: true,
    })
    const controller = new DesktopV3RoutedNewSessionController(async (request) => {
      requests.push(request.client_request_id)
      attempt += 1
      if (attempt === 1) throw new Error('ambiguous transport failure')
      return routedResult('canonical-session')
    }, createDesktopV3RoutedDraftState(initialSnapshot.prompt, initialSnapshot))

    const pending = controller.submit({ workspace: workspaceAuthority, snapshot: initialSnapshot })
    const routing = controller.getState()
    assert.equal(routing.phase, 'routing')
    assert.equal('result' in routing, false)
    assert.deepEqual(routing.snapshot, initialSnapshot)
    assert.equal(storage.size, 1)

    const failed = await pending
    assert.equal(failed.phase, 'failed')
    if (failed.phase !== 'failed') throw new Error('expected failed routed state')
    assert.deepEqual(failed.snapshot, initialSnapshot)
    assert.equal(failed.prompt, initialSnapshot.prompt)
    assert.equal(storage.size, 1)

    const resolved = await controller.retry()
    assert.equal(resolved.phase, 'resolved')
    if (resolved.phase !== 'resolved') throw new Error('expected resolved routed state')
    assert.equal(resolved.result.session_id, 'canonical-session')
    assert.deepEqual(resolved.snapshot, initialSnapshot)
    assert.deepEqual(requests, [requests[0], requests[0]])
    assert.equal(storage.size, 1)
    controller.acknowledgeResolved(resolved.operation.operationId)
    assert.equal(storage.size, 0)
  })
})

test('new-session pane keeps resolved authority behind pending shell until activation and restores failed controls', async () => {
  const source = await readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')

  assert.match(source, /const pendingState = routedState\.phase === 'resolved' \? 'routing' : routedState\.phase/)
  assert.match(source, /routedState\.phase === 'failed'[\s\S]*setDraft\(routedState\.snapshot\.prompt\)[\s\S]*setMode\(routedState\.snapshot\.planModeRequested \? 'plan' : 'auto'\)[\s\S]*setWorktreeIntent\(createDesktopRoutedWorktreeIntent\(routedState\.snapshot\.worktreePrimed\)\)[\s\S]*setRestoredSnapshot\(routedState\.snapshot\)/)
  assert.match(source, /routedState\.phase !== 'resolved'[\s\S]*resolvedCallbackRef\.current\(routedState\.result, routedState\.operation\.request\)/)
  assert.match(source, /showComposer = routedState\.phase === 'draft' \|\| routedState\.phase === 'worktree-primed' \|\| routedState\.phase === 'failed'/)
  assert.doesNotMatch(source, /dispatchDesktopV3Cache|selectSession|ensureSessionConnected|navigate\(/)
})
