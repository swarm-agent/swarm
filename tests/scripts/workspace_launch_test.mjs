// Requirement: runner fixture/session ownership must precede every mutation;
// shared defaults/models remain unchanged and only an admitted owned run stops.
// Authority: WorkspaceTrial + AttachClient; loopback fake HTTP proves exact
// requests and failure postconditions without a provider or real saved workspace.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import http from 'node:http'
import { once } from 'node:events'
import { WorkspaceTrial, completedTools, repositoryState } from '../../scripts/runners/workspace-launch.mjs'

async function fixture(t, { lostMessage = false, foreignOnly = false, pagination = '' } = {}) {
  const requests = []
  const settings = { swarm: { action: { provider: 'fixture', model: 'action' }, plan: { model: 'plan' } } }
  const s = http.createServer(async (req, res) => {
    let raw = ''; for await (const b of req) raw += b
    const body = raw ? JSON.parse(raw) : undefined
    requests.push({ method: req.method, route: req.url, body })
    if (req.url !== '/v1/auth/desktop/session') assert.equal(req.headers['x-swarm-token'], 'fixture-token')
    res.setHeader('Content-Type', 'application/json')
    const send = value => res.end(JSON.stringify(value))
    if (req.url === '/v1/auth/desktop/session') return send({ token: 'fixture-token' })
    if (req.url === '/v1/swarm/topology') return send({ runtimes: [{ relationship: 'self', swarm_id: 'runtime', status: 'online' }] })
    if (req.url === '/v1/agent-model-settings') return send({ agent_model_settings: settings })
    if (req.url === '/v1/workspace/current') return send({ workspace: { workspace_id: 'unchanged' } })
    if (req.url === '/v1/workspace/folders/create') return send({ folder: { path: body.parent_path + '/' + body.name } })
    if (req.url === '/v1/workspace/repository/setup') return send({ repository: { head_commit: 'a'.repeat(40) } })
    if (req.url === '/v1/workspace/add') { assert.equal(body.make_current, false); return send({ workspace: { workspace_id: 'saved', local_workspace_binding_id: 'binding' } }) }
    if (req.url === '/v3/sessions') return send({ session: { id: 'owned', workspace_path: body.workspace_path, worktree_root_path: '/fixture-lane', worktree_enabled: true } })
    if (pagination && req.url.startsWith('/v3/sessions/owned/repositories?limit=20')) {
      const second = req.url.includes('&cursor=opaque-token')
      return send({ items: [{ id: second ? 'second' : 'first' }], next_cursor: !second || pagination === 'repeat' ? 'opaque-token' : '' })
    }
    if (req.url === '/v3/sessions/owned/repositories?limit=20') return send({ items: [{ kind: 'parent', workspace_path: '/fixture-lane', source_path: requests.find(r => r.route === '/v3/sessions').body.workspace_path, base_commit: 'a'.repeat(40) }] })
    if (req.url === '/v3/sessions/owned/permissions/exact/resolve') {
      assert.deepEqual(body, { action: 'allow_once', reason: 'Exact owned launch fixture call only' })
      return send({ permission: { status: 'resolved' }, saved_rule: null })
    }
    if (req.url === '/v3/sessions/owned/messages' && lostMessage) { req.socket.destroy(); return }
    if (req.url === '/v3/sync/hydrate') {
      const admitted = requests.find(r => r.route === '/v3/sessions/owned/messages')?.body.run_id
      return send({ run_intents_by_session: { owned: [{ run_id: foreignOnly ? 'foreign-run' : admitted, status: 'running' }] } })
    }
    if (req.url === '/v3/sessions/owned/run/stop') {
      const admitted = requests.find(r => r.route === '/v3/sessions/owned/messages')?.body.run_id || 'owned-run'
      assert.equal(body.run_id, admitted); assert.equal(body.target_swarm_id, 'runtime'); return send({ status: 'cancelled' })
    }
    res.statusCode = 404; send({})
  })
  s.listen(0, '127.0.0.1'); await once(s, 'listening')
  t.after(() => { s.closeAllConnections(); s.close() })
  const trial = new WorkspaceTrial(`http://127.0.0.1:${s.address().port}/`)
  return { trial, requests }
}

test('new fixtures, immutable session replay, shared settings/default untouched', { timeout: 5000 }, async t => {
  const { trial, requests } = await fixture(t)
  await trial.initialize()
  const repo = await trial.fixture('/disposable', 'a')
  assert(repo.path.startsWith('/disposable/workspace-launch-'))
  const session = await trial.create(repo, 'a')
  assert.equal(session.id, 'owned')
  const creates = requests.filter(r => r.route === '/v3/sessions')
  assert.equal(creates.length, 2)
  assert.deepEqual(creates[0].body, creates[1].body)
  assert.deepEqual(creates[0].body.preference, trial.settings.swarm.action)
  await trial.unchanged()
  assert(!JSON.stringify(trial.evidence).includes('fixture-token'))
  assert(requests.every(r => !r.route.includes('settings') || r.method === 'GET'))
})

test('unowned sessions/fixtures, ambiguous parent and shared writes make no request', { timeout: 5000 }, async t => {
  const { trial, requests } = await fixture(t)
  await trial.initialize()
  const before = requests.length
  await assert.rejects(trial.hydrate('foreign'), /unowned/)
  await assert.rejects(trial.run('foreign', 'touch things'), /unowned/)
  await assert.rejects(trial.status('foreign', '/user-repo'), /unowned/)
  await assert.rejects(trial.create({ path: '/user-repo' }, 'bad'), /unowned/)
  for (const parent of ['', '.', '/', '/a/../b']) await assert.rejects(trial.fixture(parent, 'a'))
  for (const route of ['/v1/agent-model-settings', '/v1/workspace/select', '/v1/workspace/actions/run', '//elsewhere']) await assert.rejects(trial.request('POST', route, {}))
  await assert.rejects(trial.request('POST', '/v1/workspace/add', { path: '/user-repo' }))
  assert.equal(requests.length, before)
})

test('expired stage stops only recorded owned run and retains cleanup evidence', { timeout: 5000 }, async t => {
  const { trial, requests } = await fixture(t)
  await trial.initialize()
  trial.sessions.set('owned', { id: 'owned' })
  trial.activeRuns.set('owned', 'owned-run')
  trial.deadline = 0
  await trial.stopOwned()
  assert.equal(trial.activeRuns.size, 0)
  assert.deepEqual(trial.evidence.cleanup, [{ session: 'owned', run: 'owned-run', status: 'cancelled' }])
  assert.equal(requests.filter(r => r.route.endsWith('/run/stop')).length, 1)
})

test('tool observations cannot accept prose or another run completion', () => {
  const snapshot = { events_by_session: { own: [
    { event_type: 'session.message.created', run_id: 'run', payload: { content: 'read passed' } },
    { event_type: 'session.tool.completed', run_id: 'old', payload: { tool_name: 'read' } },
    { event_type: 'session.tool.completed', run_id: 'run', payload: JSON.stringify({ tool_name: 'list' }) },
  ] } }
  assert.deepEqual(completedTools(snapshot, 'own', 'run').map(x => x.payload.tool_name), ['list'])
})

// Requirement: response loss must not leak an admitted provider or stop an
// unrelated intent. Exact caller RunID is supported by acceptSessionsV3Message.
for (const foreignOnly of [false, true]) test(`lost admission response recovery foreignOnly=${foreignOnly}`, { timeout: 5000 }, async t => {
  const { trial, requests } = await fixture(t, { lostMessage: true, foreignOnly })
  await trial.initialize()
  trial.sessions.set('owned', { id: 'owned' })
  trial.evidence.sessions.push({ id: 'owned' })
  await assert.rejects(trial.run('owned', 'fixture'))
  assert.equal(trial.pendingRuns.size, 1)
  trial.interrupt()
  await trial.stopOwned()
  assert.equal(requests.filter(r => r.route.endsWith('/messages')).length, 1, 'never resubmit lost message')
  const stops = requests.filter(r => r.route.endsWith('/run/stop'))
  assert.equal(stops.length, foreignOnly ? 0 : 1)
  assert.equal(trial.pendingRuns.size, foreignOnly ? 1 : 0)
  assert.equal(trial.activeRuns.size, 0)
  if (foreignOnly) assert.match(trial.evidence.cleanup[0].status, /admission unresolved/)
  else assert.equal(stops[0].body.run_id, trial.evidence.sessions[0].pending_message.run_id)
})

// Requirement: unchanged-root assertions ignore only observation timing, never
// dirty bytes/paths, branch, HEAD or Git identity from gitstatus.Snapshot.
test('repository comparison retains every substantive field', () => {
  const state = { has_git: true, repo_root: '/fixture', head_oid: 'a'.repeat(40), branch: 'dev', files: [], refreshed_at: 'first', duration_ms: 1 }
  assert.deepEqual(repositoryState(state), repositoryState({ ...state, refreshed_at: 'second', duration_ms: 9 }))
  for (const change of [{ files: [{ path: 'foreign.txt' }] }, { head_oid: 'b'.repeat(40) }, { branch: 'wrong' }, { repo_root: '/other' }]) assert.notDeepEqual(repositoryState(state), repositoryState({ ...state, ...change }))
  assert.throws(() => repositoryState({ ...state, head_oid: '' }))
})

// Requirement: the real OS supervisor must allow WorkspaceTrial's asynchronous
// authenticated stop to finish after TERM, including an in-flight lost response.
// One Node worker and a loopback fake server prove the composed cleanup path.
test('supervised trial cancels exact admitted run before KILL and preserves sibling', { timeout: 12000 }, async t => {
  const { spawn } = await import('node:child_process')
  const { mkdtemp, readFile, rm } = await import('node:fs/promises')
  const path = await import('node:path')
  const { pathToFileURL } = await import('node:url')
  const dir = await mkdtemp(path.join(process.env.TMPDIR, 'workspace-supervisor-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  let admission, acknowledge
  const admitted = new Promise(resolve => { admission = resolve })
  const stopped = new Promise(resolve => { acknowledge = resolve })
  let runID, stopID
  const server = http.createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk
    assert.equal(req.headers['x-swarm-token'], 'fixture-token')
    const body = JSON.parse(raw)
    res.setHeader('Content-Type', 'application/json')
    if (req.url.endsWith('/messages')) { runID = body.run_id; admission(); return }
    if (req.url === '/v3/sync/hydrate') return res.end(JSON.stringify({ run_intents_by_session: { owned: [{ run_id: runID, status: 'running' }] } }))
    assert.equal(req.url, '/v3/sessions/owned/run/stop')
    stopID = body.run_id
    // Beyond the retired .5s grace, still within the exact two-second fixture cap.
    setTimeout(() => { res.end(JSON.stringify({ status: 'cancelled' })); acknowledge() }, 800)
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  t.after(() => { server.closeAllConnections(); server.close() })
  const moduleURL = pathToFileURL(path.resolve('scripts/runners/workspace-launch.mjs')).href
  const code = `import {WorkspaceTrial} from ${JSON.stringify(moduleURL)};
    const t = new WorkspaceTrial(${JSON.stringify(`http://127.0.0.1:${server.address().port}/`)}, {evidencePath:${JSON.stringify(path.join(dir, 'trial.json'))}});
    t.token='fixture-token'; t.identity={runtime_id:'runtime'}; t.sessions.set('owned',{id:'owned'}); t.evidence.sessions.push({id:'owned'});
    process.on('SIGTERM',()=>t.interrupt());
    try {await t.run('owned','fixture')} catch {} finally {await t.stopOwned(); process.exitCode=1}`
  const proc = spawn('python3', ['scripts/launch-prerun-supervisor.py', path.join(dir, 'run'), '2'], {
    env: { ...process.env, SWARM_LAUNCH_WALL_SECONDS: '5', SWARM_LAUNCH_STALL_SECONDS: '5' }, stdio: ['pipe', 'pipe', 'pipe'],
  })
  t.after(() => { if (proc.exitCode === null) proc.kill('SIGTERM') })
  let output = ''; for (const stream of [proc.stdout, proc.stderr]) stream.on('data', chunk => { output += chunk; assert(output.length < 16384) })
  const exited = once(proc, 'exit')
  proc.stdin.end(JSON.stringify([{ id: 'trial', argv: [process.execPath, '--input-type=module', '-e', code], cleanup_seconds: 2 }, { id: 'sibling', argv: [process.execPath, '-e', 'process.exit(0)'] }]))
  await admitted
  // Wait for durable sibling completion, not a scheduling sleep.
  for (let i = 0; i < 100; i++) {
    try { if ((await readFile(path.join(dir, 'run/status/sibling.exit'), 'utf8')).trim() === '0') break } catch {}
    if (i === 99) assert.fail('sibling never completed')
    await new Promise(resolve => setTimeout(resolve, 10))
  }
  proc.kill('SIGTERM')
  await stopped
  await exited
  assert.equal(stopID, runID)
  const evidence = JSON.parse(await readFile(path.join(dir, 'trial.json'), 'utf8'))
  assert.equal(evidence.cleanup[0].status, 'cancelled')
  const result = JSON.parse(await readFile(path.join(dir, 'run/results.json'), 'utf8'))
  assert.deepEqual(result.counts, { pass: 1, fail: 1, 'not-run': 0 })
  assert.deepEqual(result.results.find(r => r.id === 'trial').remaining_pids, [])
})

// Requirement: executable proof reads exact bounded bytes and actual Git
// common-dir/HEAD without relying on assistant prose. Use only a fresh temp repo.
test('controlled filesystem proof executes with exact bytes and rejects symlink', { timeout: 6000 }, async t => {
  const { execFileSync } = await import('node:child_process')
  const { mkdtemp, writeFile, symlink, rm } = await import('node:fs/promises')
  const path = await import('node:path')
  const { proofCommand } = await import('../../scripts/runners/workspace-launch.mjs')
  const repo = await mkdtemp(path.join(process.env.TMPDIR, "proof-'"))
  t.after(() => rm(repo, { recursive: true, force: true }))
  const git = (...args) => execFileSync('git', ['-C', repo, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', ...args], { timeout: 2000, maxBuffer: 65536, env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1' } }).toString().trim()
  git('init', '-b', 'dev'); git('commit', '--allow-empty', '-m', 'fixture')
  await writeFile(path.join(repo, 'marker.txt'), 'exact\n')
  const command = proofCommand([repo], ['marker.txt'])
  const before = git('status', '--porcelain')
  const proof = JSON.parse(execFileSync('bash', ['-c', command], { cwd: repo, timeout: 3000, maxBuffer: 65536 }).toString())
  assert.equal(proof.cwd, repo)
  assert.equal(proof.repositories[0].files['marker.txt'], 'exact\n')
  assert.equal(proof.repositories[0].common, git('rev-parse', '--path-format=absolute', '--git-common-dir'))
  assert.equal(proof.repositories[0].head, git('rev-parse', 'HEAD'))
  assert.equal(git('status', '--porcelain'), before)
  await symlink(path.join(repo, 'marker.txt'), path.join(repo, 'link.txt'))
  assert.throws(() => execFileSync('bash', ['-c', proofCommand([repo], ['link.txt'])], { cwd: repo, timeout: 3000, maxBuffer: 65536, stdio: 'pipe' }))
})

test('attachment verification requires exact saved set and non-first default', { timeout: 5000 }, async t => {
  const { trial } = await fixture(t)
  trial.sessions.set('owned', { id: 'owned' })
  const a = { path: '/a', workspace: { workspace_id: 'a' } }, b = { path: '/b', workspace: { workspace_id: 'b' } }
  trial.fixtures.set('a', a); trial.fixtures.set('b', b)
  const session = { id: 'owned', workspace_path: '/b', worktree_enabled: true, worktree_root_path: '/lane-b', metadata: { swarm_v3_source_workspace_id: 'b' } }
  let prompt
  trial.run = async (_id, content) => { prompt = content; return { snapshot: { sessions_by_id: { owned: session } } } }
  trial.repositories = async () => [{ kind: 'source', workspace_id: 'a', attached: true, default: false }, { kind: 'source', workspace_id: 'b', attached: true, default: true }]
  trial.unchanged = async () => {}
  assert.equal(await trial.attach('owned', [a, b], b), session)
  assert.match(prompt, /workspace_ids=\["a","b"\], primary_workspace_id="b"/)
  await assert.rejects(trial.attach('owned', [a, b], { ...b }), /unowned/)
  session.metadata.swarm_v3_source_workspace_id = 'a'
  await assert.rejects(trial.attach('owned', [a, b], b), /explicit default/)
})

test('worker verification rejects wrong common-dir, dirty handoff, base and byte leakage', async () => {
  const { verifyWorkerProof } = await import('../../scripts/runners/workspace-launch.mjs')
  const head = 'a'.repeat(40), next = 'b'.repeat(40)
  const before = { repositories: [{ path: '/a', common: '/a/.git', head }, { path: '/b', common: '/b/.git', head }] }
  const children = ['a', 'b'].map(id => ({ workspace_path: `/lane-${id}`, source_path: `/${id}`, branch: `agent/${id}`, base_commit: head }))
  const markers = { 'left.txt': 'left\n', 'right.txt': 'right\n' }
  const proof = { repositories: children.map((child, i) => ({ path: child.workspace_path, common: before.repositories[i].common, branch: child.branch, head: next, status: '', files: { 'left.txt': i === 0 ? markers['left.txt'] : null, 'right.txt': i === 1 ? markers['right.txt'] : null } })) }
  verifyWorkerProof('regular', before, children, proof, markers)
  for (const change of [{ common: '/wrong' }, { status: ' M dirty' }, { head }, { files: { ...markers } }]) {
    const bad = structuredClone(proof); Object.assign(bad.repositories[0], change)
    assert.throws(() => verifyWorkerProof('regular', before, children, bad, markers))
  }
  const bad = structuredClone(children); bad[0].base_commit = next
  assert.throws(() => verifyWorkerProof('regular', before, bad, proof, markers))
})

test('staged worker proof rejects stale later base and missing dependency bytes', async () => {
  const { verifyWorkerProof } = await import('../../scripts/runners/workspace-launch.mjs')
  const base = 'a'.repeat(40), integrated = 'b'.repeat(40), head = 'c'.repeat(40)
  const before = { repositories: [{ path: '/a', common: '/a/.git', head: base }, { path: '/b', common: '/b/.git', head: base }, {}, { head: base }] }
  const markers = { 'left.txt': 'left\n', 'right.txt': 'right\n' }
  const children = ['left', 'right', 'next'].map((id, i) => ({ workspace_path: `/lane-${id}`, source_path: '/b', branch: `agent/${id}`, base_commit: i < 2 ? base : integrated }))
  const proof = { repositories: children.map((child, i) => ({ path: child.workspace_path, common: '/b/.git', branch: child.branch, head, status: '', files: { 'left.txt': i !== 1 ? markers['left.txt'] : null, 'right.txt': i !== 0 ? markers['right.txt'] : null, 'check.txt': i === 2 ? 'verified\n' : null } })) }
  verifyWorkerProof('program', before, children, proof, markers)
  const stale = structuredClone(children); stale[2].base_commit = base
  assert.throws(() => verifyWorkerProof('program', before, stale, proof, markers))
  const missing = structuredClone(proof); missing.repositories[2].files['right.txt'] = null
  assert.throws(() => verifyWorkerProof('program', before, children, missing, markers))
})

// Requirement: explicit test authorization is call-specific, never a blanket
// allow or saved policy. Validate whole pending batch before first resolution.
test('permission review allows only exact owned call once and rejects batch widening', { timeout: 5000 }, async t => {
  const { trial, requests } = await fixture(t)
  await trial.initialize(); trial.sessions.set('owned', { id: 'owned' })
  const call = { name: 'bash', args: { command: 'pwd', critical: false, category: 'read', explanation: ['Fixture cwd.'] } }
  const pending = { id: 'exact', session_id: 'owned', run_id: 'run', status: 'pending', tool_name: 'bash', tool_call_arguments: JSON.stringify(call.args) }
  await trial.resolvePermissions('owned', 'run', [pending], [call])
  const before = requests.length
  for (const patch of [{ run_id: 'foreign' }, { session_id: 'foreign' }, { tool_name: 'task' }, { tool_call_arguments: JSON.stringify({ ...call.args, command: 'touch unsafe' }) }]) {
    await assert.rejects(trial.resolvePermissions('owned', 'run', [pending, { ...pending, ...patch }], [call]))
    assert.equal(requests.length, before)
  }
  await assert.rejects(trial.request('POST', '/v3/sessions/owned/permissions/exact/resolve', { action: 'allow_always' }))
  await assert.rejects(trial.request('POST', '/v3/sessions/owned/permissions/exact/resolve', { action: 'allow_once', approved_arguments: {} }))
  assert.equal(requests.length, before)
})

// Requirement: repository proofs consume opaque pages, never partial inventories
// or repeated cursors. Fake HTTP is the narrowest AttachClient route contract.
for (const pagination of ['complete', 'repeat']) test(`owned repository pagination ${pagination}`, { timeout: 5000 }, async t => {
  const { trial, requests } = await fixture(t, { pagination })
  await trial.initialize(); trial.sessions.set('owned', { id: 'owned' })
  if (pagination === 'repeat') await assert.rejects(trial.repositories('owned'), /cursor repeated/)
  else assert.deepEqual(await trial.repositories('owned'), [{ id: 'first' }, { id: 'second' }])
  const pages = requests.filter(r => r.route.includes('/repositories?'))
  assert.equal(pages.length, 2)
  assert.equal(pages[1].route, '/v3/sessions/owned/repositories?limit=20&cursor=opaque-token')
  assert(pages.every(r => r.method === 'GET'))
})

// Requirement: provider event stdout, not the display summary, proves exact
// filesystem bytes; missing/truncated or incomplete output must fail closed.
test('filesystem proof consumes only complete exact raw stdout', async () => {
  const { proofCommand } = await import('../../scripts/runners/workspace-launch.mjs')
  const trial = new WorkspaceTrial('http://127.0.0.1:12345/')
  trial.sessions.set('owned', { worktree_root_path: '/lane' })
  trial.repositories = async () => [{ workspace_path: '/lane', availability: 'available' }]
  const command = proofCommand(['/lane'], ['marker.txt'])
  const proof = { complete: true, cwd: '/lane', repositories: [{ path: '/lane' }] }
  let raw = JSON.stringify(proof)
  trial.run = async (_id, prompt, calls) => {
    assert.equal(calls[0].args.command, command)
    assert(prompt.includes(JSON.stringify(calls[0].args)), 'complete arguments must reach provider')
    assert.deepEqual(calls[0].args.explanation, ['Read only the disposable trial repositories.'])
    return { runID: 'run', snapshot: { events_by_session: { owned: [{ event_type: 'session.tool.completed', run_id: 'run', payload: { tool_name: 'bash', arguments: JSON.stringify(calls[0].args), output: 'display only', raw_output: raw } }] } } }
  }
  assert.deepEqual(await trial.prove('owned', ['/lane']), proof)
  for (const invalid of [undefined, '{', JSON.stringify({ ...proof, complete: false }), JSON.stringify({ ...proof, cwd: '/wrong' })]) {
    raw = invalid
    await assert.rejects(trial.prove('owned', ['/lane']))
  }
})

// Requirement: canonical hydrated tool records remain usable when event history
// is manifested. buildV3ProviderManagedToolResultRecord owns this envelope.
// This narrow parser test rejects prose, foreign identity and incomplete stdout.
test('canonical tool messages survive event omission without accepting prose or failed Bash', () => {
  const record = { path_id: 'run.v3.provider-tool-result.v1', type: 'v3_provider_tool_result', run_id: 'run', call_id: 'call', tool_name: 'bash', arguments: '{}', output: JSON.stringify({ exit_code: 0, output: 'proof' }) }
  const message = { session_id: 'owned', role: 'tool', content: JSON.stringify(record) }
  const snapshot = { messages_by_session: { owned: [message] } }
  assert.equal(completedTools(snapshot, 'owned', 'run')[0].payload.raw_output, 'proof')
  snapshot.events_by_session = { owned: [{ event_type: 'session.tool.completed', run_id: 'run', payload: record }] }
  assert.equal(completedTools(snapshot, 'owned', 'run').length, 1)
  delete snapshot.events_by_session
  for (const patch of [{ role: 'assistant' }, { session_id: 'foreign' }, { content: JSON.stringify({ ...record, run_id: 'foreign' }) }]) {
    snapshot.messages_by_session.owned = [{ ...message, ...patch }]
    assert.deepEqual(completedTools(snapshot, 'owned', 'run'), [])
  }
  for (const patch of [{ exit_code: 1 }, { exit_code: 0, truncated: true }, { exit_code: 0, timed_out: true }]) {
    snapshot.messages_by_session.owned = [{ ...message, content: JSON.stringify({ ...record, output: JSON.stringify(patch) }) }]
    assert.throws(() => completedTools(snapshot, 'owned', 'run'))
  }
})

// Requirement: parent cancellation is not proof of child termination. Verify
// repository lifecycle postconditions after canonical CancelRun context teardown.
test('worker cleanup waits for terminal children and only inspects owned parent', async () => {
  const trial = new WorkspaceTrial('http://127.0.0.1:12345/')
  trial.sessions.set('owned', { id: 'owned' }); trial.workerParents.add('owned')
  let count = 0
  trial.repositories = async id => {
    assert.equal(id, 'owned'); count++
    return [{ kind: 'worker', session_id: 'child', lifecycle: count === 1 ? 'running' : 'cancelled' }]
  }
  await trial.stopOwned()
  assert.equal(count, 2)
  assert.deepEqual(trial.evidence.cleanup[0].children, [{ session: 'child', state: 'cancelled' }])
})
