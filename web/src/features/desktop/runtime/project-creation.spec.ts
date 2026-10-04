import test from 'node:test'
import assert from 'node:assert/strict'
import { ProjectCreationRuntime, registerProjectFolder, type ProjectCreationDeps } from './project-creation'
import { projectContextPending, type CreationProject, type ProjectCreationDraft } from '../state/project-creation'

const draft: ProjectCreationDraft = { client_request_id: 'creation-fixture', name: 'Example', description: '', workspaces: [{ workspace_id: 'docs', path: '/fixture/docs', label: 'Docs', role: 'auxiliary' }] }
const project = (status: 'running' | 'failed' | 'ready', attempt = 1): CreationProject => ({ id: 'example', name: 'Example', workspaces: draft.workspaces, context_generation: { status, attempt, error: status === 'failed' ? 'Provider unavailable' : '' }, project_context: status === 'ready' ? '# Generated fixture context' : '' })
function deps(request: ProjectCreationDeps['request'], conversation: ProjectCreationDeps['conversation'] = async () => 'session'): ProjectCreationDeps {
  return { request, conversation, saveDraft: () => {}, clearDraft: () => {} }
}

// Purpose: ProjectCreationRuntime is the mutation owner used by both Desktop entry
// paths. A lost receipt must replay the exact operation after remount, concurrent
// clicks must not duplicate it, and failed generation must never fabricate context
// or open a conversation. Deferred receipts are the narrowest deterministic proof.
test('creation freezes retry identity, suppresses duplicates and gates chat on generation', async () => {
  let saved: ProjectCreationDraft | undefined
  const bodies: unknown[] = [], chats: string[] = []
  let fail = true
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const dependencies = deps((async (_url, init) => {
    bodies.push(JSON.parse(String(init?.body))); await gate
    if (fail) throw Error('Lost response')
    return { project: project('failed') }
  }) as ProjectCreationDeps['request'], async (_id, key) => { chats.push(key); return 'session' })
  dependencies.saveDraft = value => { saved = value }
  dependencies.clearDraft = () => { saved = undefined }
  const first = new ProjectCreationRuntime(dependencies)
  const creating = first.create(draft)
  await first.create({ ...draft, client_request_id: 'duplicate' })
  assert.equal(bodies.length, 1); release(); await creating
  assert.equal(saved, draft); assert.match(first.snapshot().error, /Lost response/)
  fail = false
  const resumed = new ProjectCreationRuntime(dependencies, saved)
  await resumed.create({ ...draft, client_request_id: 'wrong-new-key' })
  assert.deepEqual(bodies, [draft, draft]); assert.equal(saved, undefined)
  assert.equal(resumed.snapshot().project?.project_context, '')
  assert.equal(await resumed.open(), undefined); assert.deepEqual(chats, [])
})

// Purpose: retry claims and snapshot refreshes share ProjectCreationRuntime. An
// older GET cannot replace a newer retry receipt. Opening must reuse its key even
// after a lost conversation response/remount; no workspace authority is introduced.
test('retry fences stale refresh and first conversation uses stable project identity', async () => {
  let finishRead!: (value: unknown) => void
  const keys: string[] = []
  const dependencies = deps((async (url, init) => {
    if (String(url).endsWith('context:retry')) {
      assert.deepEqual(JSON.parse(String(init?.body)), { expected_attempt: 1 })
      return { project: project('ready', 2) }
    }
    return new Promise(resolve => { finishRead = resolve })
  }) as ProjectCreationDeps['request'], async (_id, key) => { keys.push(key); if (keys.length === 1) throw Error('Lost chat response'); return 'session' })
  const runtime = new ProjectCreationRuntime(dependencies, undefined, project('failed'))
  const reading = runtime.refresh()
  await runtime.retry(); finishRead({ project: project('failed') }); await reading
  assert.equal(runtime.snapshot().project?.context_generation?.attempt, 2)
  assert.equal(await runtime.open(), undefined)
  const remount = new ProjectCreationRuntime(dependencies, undefined, runtime.snapshot().project)
  assert.equal(await remount.open(), 'session')
  assert.deepEqual(keys, ['desktop-project-first:example', 'desktop-project-first:example'])
  assert.equal(projectContextPending(undefined), false)
  assert.equal(projectContextPending({}), false)
  assert.equal(projectContextPending({ contextGeneration: project('failed').context_generation }), true)
})

// Purpose: registerProjectFolder must obtain canonical account catalog identity,
// never promote the user's arbitrary string after a failure. Lost-add recovery is
// allowed only with a matching registered row. API injection proves exact payloads.
test('folder registration is context-only and failures never invent workspace authority', async () => {
  const calls: unknown[] = []
  const registered = await registerProjectFolder('/fixture/docs', (async (_url, init) => {
    calls.push(JSON.parse(String(init?.body)))
    return { workspace: { workspace_id: 'canonical', resolved_path: '/fixture/docs', workspace_name: 'Docs' } }
  }) as ProjectCreationDeps['request'])
  assert.deepEqual(calls, [{ path: '/fixture/docs', context_only: true, make_current: false }])
  assert.equal(registered.workspace_id, 'canonical')
  await assert.rejects(registerProjectFolder('/denied', (async (url) => {
    if (String(url).includes('/add')) throw Error('Access denied')
    return { workspaces: [{ id: 'different', path: '/fixture/docs' }] }
  }) as ProjectCreationDeps['request']), /Access denied/)
  const replay = await registerProjectFolder('/fixture/docs/', (async url => {
    if (String(url).includes('/add')) throw Error('Already exists')
    return { workspaces: [{ id: 'canonical', path: '/fixture/docs' }] }
  }) as ProjectCreationDeps['request'])
  assert.equal(replay.workspace_id, 'canonical')
})
