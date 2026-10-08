import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync, symlinkSync, readFileSync, existsSync } from 'node:fs'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { SCENARIOS, parseOptions, createReceipt, validateReceipt, requiredAssertions, toolEvidence, mediaEvidence, runScenario, assertSafeToolRouting, mutationRequestID, observeRun, waitForRun, recordFailure } from './orchestrator-pr.mjs'

import { runBrowserAdapter, parseBrowserOptions } from './orchestrator-pr-browser.mjs'
import { parseOptions as parseLiveOptions, createReceipt as createLiveReceipt, validateReceipt as validateLiveReceipt, REQUIRED_ASSERTIONS as LIVE_ASSERTIONS, runLiveE2E } from './orchestrator-live-e2e.mjs'

const candidate = 'a'.repeat(40)
function fixture(t) {
  assert.ok(process.env.TMPDIR, 'parent must supply run TMPDIR')
  const root = mkdtempSync(path.join(process.env.TMPDIR, 'orchestrator-contract-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const env = { TMPDIR: root, SWARM_RUNNER_TOKEN: 'unit-test-token-not-a-credential' }
  const args = ['--api-url', 'http://127.0.0.1:15555', '--workspace-path', path.join(root, 'source'), '--scenario', 'session-api',
    '--timeout-ms', '30000', '--output', path.join(root, 'receipt.json'), '--candidate-revision', candidate, '--run-id', 'unit-run']
  return { root, env, args, options: parseOptions(args, env) }
}

// Purpose: parseOptions is the paid-work admission boundary. Its narrow pure CLI
// layer proves single explicit scenario selection, identity and private scratch
// output; rejected discovery/model/legacy selectors cannot launch any workload.
test('dispatch rejects implicit, legacy, duplicate, remote and model override inputs', { timeout: 5000 }, t => {
  const { env, args } = fixture(t)
  for (const scenario of SCENARIOS) {
    const selected = [...args]; selected[selected.indexOf('--scenario') + 1] = scenario
    assert.equal(parseOptions(selected, env).scenario, scenario)
    assert.ok(requiredAssertions(scenario).length > 0)
  }
  for (const extra of [['--model', 'unapproved'], ['--scenario', 'image'], ['--provider', 'unapproved']]) {
    assert.throws(() => parseOptions([...args, ...extra], env), /invalid_option/)
  }
  for (const [key, value] of [['--scenario', 'all'], ['--scenario', 'new-router'], ['--api-url', 'https://example.invalid'],
    ['--api-url', 'http://user:secret@127.0.0.1'], ['--timeout-ms', '900001'], ['--candidate-revision', 'short'], ['--run-id', 'bad\nidentity']]) {
    const bad = [...args]; bad[bad.indexOf(key) + 1] = value
    assert.throws(() => parseOptions(bad, env))
  }
  assert.throws(() => parseOptions(args.slice(2), env), /explicit_inputs/)
  assert.throws(() => parseOptions(args, { ...env, SWARM_RUNNER_TOKEN: '' }), /token_environment/)
})

// Purpose: parseOptions must reject scratch traversal/symlink redirects before
// file reservation or provider work. Filesystem unit scope proves no foreign
// receipt is created and no existing directory is changed by failed admission.
test('receipt output requires canonical TMPDIR containment', { timeout: 5000 }, t => {
  const { root, env, args } = fixture(t)
  const parent = path.dirname(root)
  symlinkSync(parent, path.join(root, 'redirect'))
  for (const output of ['relative.json', path.join(parent, 'foreign.json'), path.join(root, 'redirect', 'foreign.json'), root + '/x/../receipt.json']) {
    const bad = [...args]; bad[bad.indexOf('--output') + 1] = output
    assert.throws(() => parseOptions(bad, env))
  }
  assert.equal(parseOptions(args, env).output, path.join(root, 'receipt.json'))
})

// Purpose: consumer validation must not accept empty/partial, skipped capability,
// stale identity or nonzero native exits as qualification. validateReceipt owns
// this protocol boundary; pure mutations exercise every fail-closed condition.
test('PASS requires exact identity, native zero and every named assertion', { timeout: 5000 }, t => {
  const { options } = fixture(t)
  for (const scenario of SCENARIOS) {
    const o = { ...options, scenario }, r = createReceipt(o)
    assert.throws(() => validateReceipt(r, o, 0))
    Object.assign(r, { status: 'PASS', native_exit: 0, assertion_count: r.assertions.length })
    r.assertions.forEach(a => { a.passed = true })
    assert.equal(validateReceipt(r, o, 0), true)
    for (const mutate of [x => { x.candidate_revision = 'b'.repeat(40) }, x => { x.run_id = 'other' }, x => { x.scenario = 'other' },
      x => { x.schema = 'other' }, x => { x.assertion_count = 0 }, x => { x.assertions.pop() }, x => { x.assertions[0].passed = false },
      x => { x.assertions[0].name = 'invented' }, x => { x.failures.push('failure') }, x => { x.status = 'NOT_RUN' }, x => { x.native_exit = 2 }]) {
      const changed = structuredClone(r); mutate(changed)
      assert.throws(() => validateReceipt(changed, o, 0))
    }
    assert.throws(() => validateReceipt(r, o, 2))
  }
})

const tool = (action, output, extra = {}) => ({ name: 'manage_artifact', args: { action, ...extra }, output })
function media(scenario) {
  const data = Buffer.from('unit protocol bytes, not generated media')
  const digest = createHash('sha256').update(data).digest('hex')
  const ref = { session_id: 'owned-session', collection_id: 'collection', variant_id: 'variant', event_seq: 3 }
  const generated = tool(`generate_${scenario}`, { reference: ref, artifact: { id: ref.variant_id, session_id: ref.session_id,
    collection_id: ref.collection_id, event_seq: 3, status: 'ready', media_type: `${scenario}/test`, size: data.length, digest_sha256: digest } },
    scenario === 'video' ? {} : { capability_token: 'unit-capability' })
  return { data, generated, tools: scenario === 'video' ? [generated] : [tool(`${scenario}_capabilities`, { [`${scenario}_capabilities`]: { capability_token: 'unit-capability' } }), generated] }
}

// Purpose: mediaEvidence must require canonical capabilities, one ready complete
// same-session exact reference and byte digest—not prose or an operation receipt.
// This protocol unit layer guards selection only, not provider/media correctness.
test('media protocol rejects unavailable, mismatched, duplicated and overridden generation', { timeout: 5000 }, () => {
  for (const scenario of ['image', 'video', 'audio']) {
    const { tools } = media(scenario)
    assert.equal(mediaEvidence(tools, scenario, 'owned-session').artifact_reference.event_seq, 3)
    for (const change of [x => { x.at(-1).output.artifact.status = 'staging' }, x => { x.at(-1).output.reference.event_seq++ },
      x => { x.at(-1).output.artifact.digest_sha256 = '' }, x => { x.at(-1).args.count = 2 }, x => { x.at(-1).args.model = 'override' },
      x => { x.at(-1).output.artifact.session_id = 'foreign' }, x => { x.push(x.at(-1)) }]) {
      const bad = structuredClone(tools); change(bad)
      assert.throws(() => mediaEvidence(bad, scenario, 'owned-session'))
    }
    if (scenario !== 'video') {
      assert.throws(() => mediaEvidence(tools.slice(1), scenario, 'owned-session'), /capability/)
      assert.throws(() => mediaEvidence([...tools].reverse(), scenario, 'owned-session'), /capability/)
      const bad = structuredClone(tools); bad[1].args.capability_token = 'different'
      assert.throws(() => mediaEvidence(bad, scenario, 'owned-session'), /token_mismatch/)
    }
  }
})

// Purpose: toolEvidence reads executor-owned durable session.tool.completed event
// payloads, not assistant prose, tool messages or cross-run state. The parser
// rejects failed/duplicate/incomplete current-run evidence without manufacturing it.
test('tool evidence is completion- and run-scoped', { timeout: 5000 }, () => {
  const event = { event_type: 'session.tool.completed', payload: JSON.stringify({ run_id: 'owned-run', call_id: 'call', tool_name: 'manage_projects',
    arguments: JSON.stringify({ action: 'propose_task' }), output: 'Task proposed', raw_output: JSON.stringify({ task: { id: 'task' } }) }) }
  assert.equal(toolEvidence([event], 'owned-run')[0].output.task.id, 'task')
  assert.deepEqual(toolEvidence([event], 'foreign-run'), [])
  assert.deepEqual(toolEvidence([{ ...event, event_type: 'session.tool.started' }], 'owned-run'), [])
  assert.throws(() => toolEvidence([event, event], 'owned-run'), /invalid_tool/)
  assert.throws(() => toolEvidence([{ ...event, event_type: 'session.tool.failed' }], 'owned-run'), /tool_failed/)
  assert.throws(() => toolEvidence([{ ...event, payload: { run_id: 'owned-run', call_id: 'call' } }], 'owned-run'), /tool_output/)
})

// Test-only account store for the canonical create identity/replay contract:
// stableSessionsV3PrimarySessionID uses account + key (NOT route/project),
// handleProjectConversations supplies project_id, createSessionsV3Primary checks
// project identity, and ApplyV3SessionMutation compares operation payload hashes.
// CreateProjectWithContext likewise fences account + key by creation hash.
// Project/message records share the account but retain their operation scope.
function accountIdempotencyStore() {
  const account = 'unit-account', records = new Map(), sessions = new Map()
  const identity = key => createHash('sha256').update(account + '\0' + key).digest('hex').slice(0, 32)
  const conflict = code => Response.json({ error_code: code }, { status: 409 })
  function apply(operation, sessionID, body, create) {
    const key = JSON.stringify([account, sessionID, operation, body.client_request_id])
    const payloadHash = createHash('sha256').update(JSON.stringify(body)).digest('hex'), prior = records.get(key)
    if (prior && prior.payloadHash !== payloadHash) return conflict('idempotency_conflict')
    if (prior) return Response.json(prior.result)
    const result = create()
    records.set(key, { payloadHash, result })
    return Response.json(result)
  }
  function createSession(body, projectID) {
    const sessionID = identity(body.client_request_id), prior = sessions.get(sessionID)
    if (prior && projectID && prior.project_id !== projectID) return conflict('session_identity_mismatch')
    return apply('create_session', sessionID, { ...body, project_id: projectID }, () => {
      const session = { id: sessionID, mode: 'auto', project_id: projectID }
      sessions.set(sessionID, session)
      return { session_id: sessionID, session }
    })
  }
  return { apply, createSession, identity, records, sessions,
    snapshot: () => JSON.stringify({ records: [...records], sessions: [...sessions] }) }
}

// Purpose: runScenario dispatch must use real create/send/hydrate routes and never
// task POST as AI evidence or settings/permission writes. An immediate protocol
// fixture is the narrowest deterministic runner test; it is NOT live E2E, latency
// evidence, a simulated workload or a benchmark. Assert requests and failure stop.
function protocol(options, scenario, { failed = false, missingCapability = false, badBytes = false, wrongModel = false, stopFailure = false, store } = {}) {
  const calls = []; let prompt = '', projectID = 'project', sessionID = 'owned-session'
  const { tools, data } = media(scenario)
  const fetch = async (url, init) => {
    const route = new URL(url).pathname, body = init.body ? JSON.parse(init.body) : undefined
    calls.push({ method: init.method, route, body })
    let result
    if (route === '/v1/agent-model-settings') result = { agent_model_settings: { swarm: { action: { provider: 'configured-provider', model: 'configured', thinking: 'off' }, plan: { provider: 'configured-provider', model: 'configured', thinking: 'off' } }, system_agents: { router: { model: 'configured' } } } }
    else if (route === '/v1/swarm/topology') result = { runtimes: [{ relationship: 'self', swarm_id: 'self' }], workspace_bindings: [{ state: 'bound', source_workspace_path: options.workspacePath, workspace_binding_id: 'binding' }] }
    else if (route === '/v3/projects') {
      if (store) {
        const response = store.apply('create_project', '', body, () => ({ project: { id: 'proj_' + store.identity(body.client_request_id) } }))
        if (!response.ok) return response
        result = await response.json(); projectID = result.project.id
      } else result = { project: { id: projectID } }
    }
    else if (route === '/v3/sessions' || route === `/v3/projects/${projectID}/sessions`) {
      if (store) {
        const response = store.createSession(body, route === '/v3/sessions' ? '' : projectID)
        if (!response.ok) return response
        result = await response.json(); sessionID = result.session_id
        for (const record of tools) {
          if (record.output.reference) record.output.reference.session_id = sessionID
          if (record.output.artifact) record.output.artifact.session_id = sessionID
        }
      } else result = { session_id: sessionID, session: { id: sessionID, mode: 'auto', project_id: projectID } }
    }
    else if (route === `/v3/sessions/${sessionID}/messages`) {
      if (store) {
        const response = store.apply('append_message', sessionID, body, () => ({ run_intent: { run_id: 'provider-run' } }))
        if (!response.ok) return response
        result = await response.json()
      } else result = { run_intent: { run_id: 'provider-run' } }
      prompt = body.content
    }
    else if (route.endsWith('/run/stop')) {
      if (stopFailure) return Response.json({ error: 'not included in evidence' }, { status: 500 })
      result = { cancelled: true }
    }
    else if (route === `/v3/projects/${projectID}/tasks/task`) result = { task: { id: 'task', project_id: projectID, agent: 'swarm', status: 'pending_approval', source_workspace: { path: options.workspacePath }, session_id: 'task-session' } }
    else if (route === '/v3/sync/hydrate' && body.session_ids[0] === 'task-session') result = { run_intents_by_session: { 'task-session': [] } }
    else if (route === '/v3/sync/hydrate') {
      const routed = scenario === 'orchestrator-chat' ? [{ name: 'manage_projects', args: { action: 'propose_task', project_id: projectID }, output: { task: { id: 'task', title: 'PR-' + options.runID } } }]
        : scenario === 'session-api' ? [] : missingCapability ? tools.slice(1) : tools
      result = { run_intents_by_session: { [sessionID]: [{ session_id: sessionID, run_id: 'provider-run', status: failed ? 'failed' : 'completed' }] },
        messages_by_session: { [sessionID]: [{ role: 'user', content: prompt }, { role: 'assistant', content: scenario === 'session-api' ? 'PR-' + options.runID : 'Provider reply', metadata: { run_id: 'provider-run', provider: 'configured-provider', model: wrongModel ? 'wrong-model' : 'configured' } }] },
        events_by_session: { [sessionID]: routed.map((record, i) => ({ event_type: 'session.tool.completed', payload: { run_id: 'provider-run', call_id: 'call-' + i, tool_name: record.name, arguments: record.args, output: record.name === 'manage_projects' ? 'Task proposed' : record.output, raw_output: JSON.stringify(record.output) } })) } }
    } else if (route.endsWith('/artifacts/variant')) return new Response(badBytes ? Buffer.from('wrong') : data, { headers: { 'content-type': `${scenario}/test` } })
    else throw new Error('unexpected_route')
    return Response.json(result)
  }
  return { fetch, calls }
}
test('current scenarios dispatch narrowly and qualify only complete protocol assertions', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  for (const scenario of SCENARIOS) {
    const o = { ...options, scenario }, receipt = createReceipt(o), p = protocol(o, scenario)
    await runScenario(o, receipt, { fetch: p.fetch, verifyCandidate: () => {} })
    assert.ok(receipt.assertions.every(a => a.passed))
    assert.equal(p.calls.filter(c => c.route.endsWith('/messages')).length, 1)
    assert.ok(p.calls.filter(c => c.route === '/v3/sync/hydrate').length >= 2)
    assert.ok(!p.calls.some(c => ['PATCH', 'DELETE'].includes(c.method) || /\/tasks$/.test(c.route)))
    if (scenario !== 'session-api') {
      const create = p.calls.find(c => c.route === '/v3/projects/project/sessions')
      assert.deepEqual(Object.keys(create.body), ['client_request_id'])
    }
  }
})

// Purpose: runScenario must isolate all mutations by scenario even when the
// qualification launcher supplies one build runID. This shared protocol store
// exercises actual sequential runner requests against canonical account identity
// and operation replay rules, reproduces the old 409, and rejects payload changes
// without state changes. This proves runner protocol only, not live daemon/AI E2E.
test('shared build runID reproduces old session collision without admitting project run', { timeout: 5000 }, async t => {
  const { options } = fixture(t), store = accountIdempotencyStore()
  for (const scenario of ['session-api', 'orchestrator-chat']) {
    const o = { ...options, scenario }, receipt = createReceipt(o), p = protocol(o, scenario, { store })
    // Reproduce the exact former request-key scheme at the transport boundary.
    let collisionCode
    const legacyFetch = async (url, init) => {
      const body = init.body ? JSON.parse(init.body) : undefined
      if (body?.client_request_id) {
        const route = new URL(url).pathname
        body.client_request_id = o.runID + (route.endsWith('/messages') ? ':message' : route === '/v3/projects' ? ':project' : ':session')
        init = { ...init, body: JSON.stringify(body) }
      }
      const before = store.snapshot(), response = await p.fetch(url, init)
      if (!response.ok) {
        collisionCode = (await response.clone().json()).error_code
        assert.equal(store.snapshot(), before)
      }
      return response
    }
    const run = runScenario(o, receipt, { fetch: legacyFetch, verifyCandidate: () => {} })
    if (scenario === 'session-api') {
      await run
      assert.ok(receipt.assertions.every(a => a.passed))
    } else {
      await assert.rejects(run, /^Error: http_409$/)
      assert.equal(receipt.status, 'NOT_RUN')
      assert.equal(collisionCode, 'session_identity_mismatch')
      assert.deepEqual(receipt.assertions.filter(a => a.passed).map(a => a.name), ['candidate_revision', 'configured_models', 'workspace_binding'])
      assert.ok(!p.calls.some(c => c.route.endsWith('/messages') || c.route.endsWith('/run/stop')))
      assert.equal(store.sessions.size, 1)
      assert.equal([...store.sessions.values()][0].project_id, '')
      assert.equal(store.records.size, 3) // Original session/message + retained project, no failed session.
    }
  }
})

// Purpose: the corrected runner must use distinct stable project/session/message
// keys across all five scenarios, preserve build/candidate receipt identity and
// canonical project session payloads, and replay identical requests without new
// records. Transport-level changed-payload rejection also protects retry safety.
test('sequential scenarios share build identity but isolate mutations and replay safely', { timeout: 5000 }, async t => {
  const { options } = fixture(t), store = accountIdempotencyStore(), keys = new Set(), sessionIDs = new Set(), projectIDs = new Set()
  for (const scenario of SCENARIOS) {
    const o = { ...options, scenario }, receipt = createReceipt(o), p = protocol(o, scenario, { store })
    await runScenario(o, receipt, { fetch: p.fetch, verifyCandidate: () => {} })
    assert.ok(receipt.assertions.every(a => a.passed))
    assert.equal(receipt.run_id, options.runID)
    assert.equal(receipt.candidate_revision, candidate)
    const mutations = p.calls.filter(c => c.body?.client_request_id)
    assert.equal(mutations.length, scenario === 'session-api' ? 2 : 3)
    for (const call of mutations) {
      const operation = call.route === '/v3/projects' ? 'project' : call.route.endsWith('/messages') ? 'message' : 'session'
      assert.equal(call.body.client_request_id, mutationRequestID(o, operation))
      assert.equal(keys.has(call.body.client_request_id), false)
      keys.add(call.body.client_request_id)
    }
    const sessionID = receipt.evidence.find(e => e.session_id)?.session_id
    assert.ok(sessionID); assert.equal(sessionIDs.has(sessionID), false); sessionIDs.add(sessionID)
    if (scenario !== 'session-api') {
      const projectID = receipt.evidence.find(e => e.project_id).project_id
      assert.equal(projectIDs.has(projectID), false); projectIDs.add(projectID)
      assert.deepEqual(Object.keys(mutations.find(c => c.route.endsWith('/sessions')).body), ['client_request_id'])
      assert.equal(store.sessions.get(sessionID).project_id, projectID)
    }
    const before = store.snapshot(), retryReceipt = createReceipt(o), retry = protocol(o, scenario, { store })
    await runScenario(o, retryReceipt, { fetch: retry.fetch, verifyCandidate: () => {} })
    assert.ok(retryReceipt.assertions.every(a => a.passed))
    assert.deepEqual(retryReceipt.evidence, receipt.evidence)
    assert.deepEqual(retry.calls.filter(c => c.body?.client_request_id), mutations)
    assert.equal(store.snapshot(), before)
    for (const call of mutations) {
      const body = { ...call.body, ...(call.route === '/v3/projects' ? { description: 'Changed intent' }
        : call.route.endsWith('/messages') ? { content: 'Changed intent' } : { title: 'Changed intent' }) }
      const response = await p.fetch(o.apiURL + call.route, { method: call.method, body: JSON.stringify(body) })
      assert.equal(response.status, 409)
      assert.equal((await response.json()).error_code, 'idempotency_conflict')
      assert.equal(store.snapshot(), before)
    }
  }
  assert.equal(keys.size, 14)
  assert.equal(store.records.size, 14)
  assert.equal(store.sessions.size, 5)
  assert.equal(projectIDs.size, 4)
})

// Purpose: mutationRequestID must bound every accepted 200-character build ID
// without truncating identity, payload-based keys or time-dependent retries. The
// pure key boundary is the narrowest proof; receipt identity remains unmodified.
test('mutation keys are bounded, deterministic and sensitive to full run identity', { timeout: 5000 }, t => {
  const { options, args, env } = fixture(t), keys = new Set()
  for (const runID of ['r'.repeat(200), 'r'.repeat(199) + 's']) {
    const input = [...args]; input[input.indexOf('--run-id') + 1] = runID
    const parsed = parseOptions(input, env)
    for (const scenario of SCENARIOS) for (const operation of ['project', 'session', 'message']) {
      const o = { ...parsed, scenario }, key = mutationRequestID(o, operation)
      assert.match(key, /^[a-zA-Z0-9_.:-]{1,200}$/)
      assert.equal(key, mutationRequestID({ ...o, candidate: 'b'.repeat(40), workspacePath: 'changed-payload' }, operation))
      assert.equal(keys.has(key), false); keys.add(key)
      assert.equal(createReceipt(o).run_id, runID)
    }
  }
  for (const [o, operation] of [[options, 'stop'], [{ ...options, scenario: 'unknown' }, 'session'], [{ ...options, runID: 'x'.repeat(201) }, 'session']]) {
    assert.throws(() => mutationRequestID(o, operation), /invalid_mutation_identity/)
  }
  assert.equal(keys.size, 30)
})

// Purpose: required missing capability/byte failure is never PASS. Failed runs
// are stopped only by exact owned session/run/runtime, preserving pending tasks
// and artifacts. Immediate failure injection observes no unrelated cleanup.
test('required capability, byte and run failures never qualify and stop only owned live run', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  for (const failure of [{ failed: true }, { missingCapability: true }, { badBytes: true }, { wrongModel: true }, { failed: true, stopFailure: true }]) {
    const o = { ...options, scenario: 'image' }, receipt = createReceipt(o), p = protocol(o, 'image', failure)
    await assert.rejects(runScenario(o, receipt, { fetch: p.fetch, verifyCandidate: () => {} }), failure.failed ? /run_failed/ : undefined)
    if (failure.stopFailure) {
      assert.ok(receipt.failures.includes('owned_run_stop_failed'))
      assert.ok(receipt.failures.includes('last_run_failed'))
    }
    assert.notEqual(receipt.status, 'PASS')
    assert.ok(receipt.assertions.some(a => !a.passed))
    const stops = p.calls.filter(c => c.route.endsWith('/run/stop'))
    assert.equal(stops.length, failure.failed ? 1 : 0)
    if (failure.failed) assert.deepEqual(stops[0].body, { run_id: 'provider-run', target_swarm_id: 'self', reason: 'PR qualification deadline or failure' })
    assert.ok(!p.calls.some(c => c.method === 'DELETE'))
  }
})

// Purpose: the stable external adapter must reject old selectors before network,
// preserving supported direct session checks in their separate existing runners.
// Bounded subprocess exit is the narrowest executable compatibility assertion.
test('legacy task-routing selectors fail closed with no receipt or success stdout', { timeout: 5000 }, () => {
  const runner = fileURLToPath(new URL('./task-routing.mjs', import.meta.url))
  const result = spawnSync(process.execPath, [runner, '--scenario', 'new-router'], { timeout: 3000, maxBuffer: 4096, encoding: 'utf8' })
  assert.equal(result.error, undefined)
  assert.equal(result.status, 2)
  assert.equal(result.stdout, '')
  assert.match(result.stderr, /legacy.*retired/)
})

// Purpose: the browser receipt adapter cannot turn readiness/early exit/cleanup
// failure into success or overwrite an existing receipt. The wrapper owns real
// UI proof; this unit test proves only propagation and reservation ordering.
test('browser adapter qualifies only completed owned wrapper and reserves fresh receipt first', { timeout: 5000 }, async t => {
  const { options, root } = fixture(t)
  for (const fail of [false, true]) {
    const o = { ...options, scenario: 'new-task-browser', output: path.join(root, `browser-${fail}.json`), wrapperArgs: ['unit-protocol'] }
    let calls = 0
    const code = await runBrowserAdapter(o, { verifyCandidate: () => {}, runWrapper: async args => {
      calls++; assert.deepEqual(args, ['unit-protocol'])
      if (fail) throw new Error('readiness_only_or_cleanup_failed')
    } })
    const r = JSON.parse(readFileSync(o.output, 'utf8'))
    assert.equal(calls, 1)
    if (fail) { assert.equal(code, 2); assert.throws(() => validateReceipt(r, o, code)); assert.equal(r.assertion_count, 1) }
    else { assert.equal(code, 0); assert.equal(validateReceipt(r, o, code), true) }
    await assert.rejects(runBrowserAdapter(o, { runWrapper: async () => { calls++ } }), /EEXIST/)
    assert.equal(calls, 1)
  }
  const output = path.join(root, 'symlink.json'), target = path.join(root, 'absent.json')
  symlinkSync(target, output)
  await assert.rejects(runBrowserAdapter({ ...options, scenario: 'new-task-browser', output }, { runWrapper: async () => assert.fail('must not dispatch') }), /EEXIST/)
  assert.equal(existsSync(target), false)
})

// Purpose: qualification must reject unexpected account/task mutations and paid
// fan-out as soon as durable tool-start evidence appears, not after completion.
// assertSafeToolRouting is an observation guard, not daemon permission authority.
// Allow required Orchestrator list_sources/inspect_source discovery only, keeping
// all approval/deployment/settings mutations and non-chat project actions denied.
test('tool-start guard rejects settings, approval, delegation and multiple generations', { timeout: 5000 }, () => {
  const started = (name, args, call = 'call') => ({ event_type: 'session.tool.started', payload: { run_id: 'owned', call_id: call, tool_name: name, arguments: args } })
  for (const action of ['propose_task', 'list_sources', 'inspect_source']) {
    assert.doesNotThrow(() => assertSafeToolRouting([started('manage_projects', { action })], 'owned', 'orchestrator-chat'))
  }
  for (const action of ['list_sources', 'inspect_source']) {
    assert.throws(() => assertSafeToolRouting([started('manage_projects', { action })], 'owned', 'image'), /unexpected_project_mutation/)
  }
  for (const [name, args] of [['manage_agent', { action: 'update' }], ['task', { mode: 'swarm' }], ['manage_projects', { action: 'approve_task' }], ['manage_projects', { action: 'deploy_task' }], ['manage_projects', { action: 'update' }], ['manage_projects', { action: 'inspect_files' }]]) {
    assert.throws(() => assertSafeToolRouting([started(name, args)], 'owned', 'orchestrator-chat'))
  }
  assert.throws(() => assertSafeToolRouting([started('read', { path: 'README.md' })], 'owned', 'session-api'), /unexpected_tool/)
  assert.throws(() => assertSafeToolRouting([started('manage_artifact', { action: 'generate_image', count: 2 })], 'owned', 'image'), /media_mutation/)
  assert.throws(() => assertSafeToolRouting([started('manage_artifact', { action: 'generate_image' }), started('manage_artifact', { action: 'generate_image' }, 'second')], 'owned', 'image'), /single_media/)
  assert.doesNotThrow(() => assertSafeToolRouting([started('task', { mode: 'swarm' })], 'foreign', 'image'))
})

// Purpose: browser CLI must forward only the maintained isolated wrapper contract,
// retaining credential-free acknowledgement and wrapper-generated ownership.
// Pure parsing proves explicit receipt identity and rejects duplicate/owner input.
test('browser adapter forwards maintained inputs and rejects unsafe adapter selectors', { timeout: 5000 }, t => {
  const { root, env } = fixture(t)
  const wrapper = ['--daemon-bin', path.join(root, 'swarmd'), '--bootstrap-bin', path.join(root, 'bootstrap'),
    '--desktop-url', 'http://127.0.0.1:15655/', '--api-port', '17881', '--peer-port', '17882',
    '--fixture-repo', path.join(root, 'fixture'), '--model-settings-file', path.join(root, 'settings.json'),
    '--isolated-no-provider-egress', '--timeout-ms', '120000']
  const args = ['--candidate-revision', candidate, '--run-id', 'browser-run', '--output', path.join(root, 'browser.json'), ...wrapper]
  const o = parseBrowserOptions(args, env)
  assert.deepEqual(o.wrapperArgs, wrapper)
  assert.equal(o.scenario, 'new-task-browser')
  assert.throws(() => parseBrowserOptions([...args, '--run-id', 'duplicate'], env), /invalid_adapter/)
  assert.throws(() => parseBrowserOptions([...args, '--owner', 'invented'], env), /owner/)
  assert.throws(() => parseBrowserOptions([...args, '--model', 'override'], env))
  assert.throws(() => parseBrowserOptions(args.filter(a => a !== '--isolated-no-provider-egress'), env))
})

// Purpose: orchestrator-live-e2e runner option parsing and receipt validation
// must strictly enforce candidate identity, tmpdir containment, and all 6 named assertions.
test('orchestrator live e2e runner parses options, validates receipt schema and handles fail-closed lifecycle', { timeout: 5000 }, async t => {
  const { root, env } = fixture(t)
  const args = [
    '--api-url', 'http://127.0.0.1:15555',
    '--workspace-path', path.join(root, 'source'),
    '--scenario', 'orchestrator-live-e2e',
    '--timeout-ms', '60000',
    '--output', path.join(root, 'live-receipt.json'),
    '--candidate-revision', candidate,
    '--run-id', 'live-test-run',
  ]
  const o = parseLiveOptions(args, env)
  assert.equal(o.scenario, 'orchestrator-live-e2e')
  assert.equal(o.candidate, candidate)
  assert.equal(o.runID, 'live-test-run')
  assert.equal(o.timeoutMs, 60000)

  // Invalid options fail closed
  const replaceArg = (flag, oldVal, newVal) => args.map(a => a === oldVal ? newVal : a)
  assert.throws(() => parseLiveOptions(replaceArg('--timeout-ms', '60000', '20000'), env), /invalid_deadline/)
  assert.throws(() => parseLiveOptions(replaceArg('--candidate-revision', candidate, 'invalid'), env), /candidate_and_run_identity/)
  assert.throws(() => parseLiveOptions(replaceArg('--scenario', 'orchestrator-live-e2e', 'unknown'), env), /invalid_scenario/)

  // requiredAssertions returns the 6 contract assertions
  assert.equal(requiredAssertions('orchestrator-live-e2e').length, 6)

  // Receipt creation and validation lifecycle
  const r = createLiveReceipt(o)
  assert.equal(r.schema, 'swarm.orchestrator-live-e2e.v1')
  assert.equal(r.status, 'NOT_RUN')
  assert.equal(r.native_exit, 2)
  assert.equal(r.assertions.length, LIVE_ASSERTIONS.length)
  assert.throws(() => validateLiveReceipt(r, o, 0))

  // When all assertions pass, validateLiveReceipt succeeds
  r.status = 'PASS'
  r.native_exit = 0
  r.assertion_count = LIVE_ASSERTIONS.length
  r.assertions.forEach(a => { a.passed = true })
  assert.equal(validateLiveReceipt(r, o, 0), true)

  // Receipt mutation fails closed
  const mutated = structuredClone(r)
  mutated.assertions[0].passed = false
  assert.throws(() => validateLiveReceipt(mutated, o, 0))
})

function runSnapshot(status = 'running', extra = {}) {
  const state = { session_id: 'owned-session', run_id: 'provider-run', status, active: status === 'running', event_seq: 2 }
  return { current_run_state_by_session: { 'owned-session': state },
    run_intents_by_session: { 'owned-session': [state] },
    session_views_by_id: { 'owned-session': { pending_permissions: [] } },
    messages_by_session: { 'owned-session': [] }, events_by_session: { 'owned-session': [] }, ...extra }
}
const observerOptions = { sessionID: 'owned-session', runID: 'provider-run', scenario: 'orchestrator-chat' }

// Purpose: observeRun consumes V3SessionRunState/RunIntent and canonical sync
// session views, not lifecycle guesses or provider error bodies. Protocol-level
// negative snapshots prove exact identity, terminal/blocked and permission guards
// without changing backend permissions or treating absent evidence as completion.
test('exact observer classifies canonical blocked, terminal and permission states', { timeout: 5000 }, async () => {
  for (const status of ['dispatch_blocked', 'waiting_tasks', 'failed', 'cancelled', 'expired', 'interrupted']) {
    let samples = 0
    await assert.rejects(waitForRun({ ...observerOptions, deadline: 1000, stallMs: 900, now: () => 0,
      sample: async () => { samples++; return runSnapshot(status) }, pause: () => assert.fail('must fail immediately') }), new RegExp(`^Error: run_${status}$`))
    assert.equal(samples, 1)
  }
  const permission = { id: 'permission', session_id: 'owned-session', run_id: 'provider-run', status: 'pending', tool_arguments: 'SECRET', reason: 'SECRET' }
  const snapshot = runSnapshot('running', { session_views_by_id: { 'owned-session': { pending_permissions: [permission] } } })
  assert.equal(observeRun(snapshot, 'owned-session', 'provider-run').failure, 'run_permission_pending')
  permission.run_id = 'foreign'
  assert.equal(observeRun(snapshot, 'owned-session', 'provider-run').failure, '')
  const eventSnapshot = runSnapshot()
  delete eventSnapshot.session_views_by_id
  eventSnapshot.events_by_session['owned-session'] = [{ event_type: 'permission.requested', seq: 3,
    payload: { run_id: 'provider-run', permission: { ...permission, run_id: 'provider-run' } } }]
  assert.equal(observeRun(eventSnapshot, 'owned-session', 'provider-run').failure, 'run_permission_pending')
  for (const snapshot of [{}, runSnapshot('completed', { current_run_state_by_session: { 'owned-session': { session_id: 'owned-session', run_id: 'foreign', status: 'completed' } } }),
    runSnapshot('completed', { current_run_state_by_session: { 'owned-session': { session_id: 'foreign', run_id: 'provider-run', status: 'completed' } } })]) {
    assert.throws(() => observeRun(snapshot, 'owned-session', 'provider-run'), /missing_run_evidence|foreign_run_evidence/)
  }
  const inconsistent = runSnapshot('completed')
  inconsistent.run_intents_by_session['owned-session'] = [{ session_id: 'owned-session', run_id: 'provider-run', status: 'running' }]
  assert.throws(() => observeRun(inconsistent, 'owned-session', 'provider-run'), /run_evidence_conflict/)
  assert.equal(observeRun(runSnapshot('completed', { run_intents_by_session: {} }), 'owned-session', 'provider-run').status, 'completed')
})

// Purpose: waitForRun must measure semantic exact-run durable progress, not sync
// rev/cursor/watermark churn, timestamp rewrites, foreign usage, or tail eviction.
// An injected clock exercises real observer logic with finite deterministic bounds;
// this is not a workload simulation, live provider test or timing benchmark.
test('unrelated hydration churn and tail eviction cannot hide a stalled run', { timeout: 5000 }, async () => {
  let clock = 0, samples = 0
  await assert.rejects(waitForRun({ ...observerOptions, deadline: 10000, stallMs: 2000, now: () => clock,
    pause: async ms => { clock += ms }, sample: async () => {
      samples++
      const snapshot = runSnapshot()
      Object.assign(snapshot, { rev: samples, snapshot_endpoint_cursor: 'opaque-' + samples, watermarks: { seq: samples } })
      snapshot.current_run_state_by_session['owned-session'].updated_at = samples
      snapshot.current_run_state_by_session['owned-session'].event_seq = samples
      snapshot.messages_by_session['owned-session'] = samples % 2 ? [{ id: 'message', global_seq: 3, role: 'assistant', metadata: { run_id: 'provider-run' } }] : []
      snapshot.events_by_session['owned-session'] = [{ seq: samples + 100, event_type: 'session.usage.updated', payload: { run_id: 'provider-run' } },
        { seq: samples + 200, event_type: 'session.tool.started', payload: { run_id: 'foreign' } }]
      return snapshot
    } }), /^Error: no_progress_work_retained$/)
  assert.equal(clock, 2000)
  assert.equal(samples, 5)
})

// Purpose: real run progress may postpone stall classification but never the total
// stage deadline. Completion sampled after deadline is rejected. Narrow observer
// tests prove this independently of paid work and wall-clock timer scheduling.
test('genuine durable progress has a finite total deadline and late completion fails', { timeout: 5000 }, async () => {
  for (const late of [false, true]) {
    let clock = 0, samples = 0
    await assert.rejects(waitForRun({ ...observerOptions, deadline: 2000, stallMs: 1000, now: () => clock,
      pause: async ms => { clock += ms }, sample: async () => {
        samples++
        if (late) { clock = 2000; return runSnapshot('completed') }
        return runSnapshot('running', { messages_by_session: { 'owned-session': [{ id: 'm-' + samples, global_seq: samples, role: 'assistant', metadata: { run_id: 'provider-run' } }] } })
      } }), /^Error: stage_deadline_work_retained$/)
    assert.equal(clock, 2000)
    assert.equal(samples, late ? 1 : 4)
  }
})

// Purpose: runScenario must retain safe pre-stop diagnostics in the failures array
// consumed by operations, stop only its admitted run using an independent signal,
// and report stop failure separately. Canonical multi-snapshot protocol injection
// exercises the actual runner, with no live network, approval or provider calls.
test('runner failures preserve sanitized last state before independently bounded stop', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  for (const kind of ['dispatch_blocked', 'failed', 'permission', 'deadline', 'missing', 'foreign', 'stop_failure']) {
    const o = { ...options, scenario: 'orchestrator-chat' }, receipt = createReceipt(o), p = protocol(o, o.scenario)
    let clock = 0, samples = 0, stopSignal
    const fetch = async (url, init) => {
      const route = new URL(url).pathname
      if (route.endsWith('/run/stop')) {
        stopSignal = init.signal
        assert.ok(receipt.failures.some(code => code.startsWith('last_run_')))
        assert.equal(init.signal.aborted, false)
        const response = await p.fetch(url, init)
        return kind === 'stop_failure' ? Response.json({ error: 'SECRET provider/auth body' }, { status: 500 }) : response
      }
      const response = await p.fetch(url, init)
      if (route !== '/v3/sync/hydrate') return response
      samples++
      if (samples === 1) return Response.json(runSnapshot('pending_executor'))
      if (kind === 'missing') return Response.json({})
      if (kind === 'foreign') return Response.json(runSnapshot('completed', { current_run_state_by_session: { 'owned-session': { session_id: 'owned-session', run_id: 'foreign', status: 'completed' } } }))
      const snapshot = runSnapshot(['failed', 'stop_failure'].includes(kind) ? 'failed' : kind === 'dispatch_blocked' ? kind : 'running')
      snapshot.current_run_state_by_session['owned-session'].blocked_reason = 'SECRET provider/auth reason'
      if (kind === 'permission') snapshot.session_views_by_id['owned-session'].pending_permissions = [{ session_id: 'owned-session', run_id: 'provider-run', status: 'pending', tool_arguments: 'SECRET' }]
      if (kind === 'deadline') snapshot.messages_by_session['owned-session'] = [{ global_seq: samples, role: 'assistant', metadata: { run_id: 'provider-run' } }]
      return Response.json(snapshot)
    }
    const expected = kind === 'deadline' ? 'stage_deadline_work_retained' : kind === 'permission' ? 'permission_identity_rejected'
      : kind === 'missing' ? 'missing_run_evidence' : kind === 'foreign' ? 'foreign_run_evidence' : kind === 'stop_failure' ? 'run_failed' : `run_${kind}`
    await assert.rejects(runScenario(o, receipt, { fetch, verifyCandidate: () => {}, now: () => clock, pause: async ms => { clock += ms } }), new RegExp(`^Error: ${expected}$`))
    assert.ok(stopSignal)
    assert.equal(receipt.status, 'FAIL')
    assert.equal(receipt.assertions.find(a => a.name === 'run_completed').passed, false)
    assert.ok(receipt.failures.every(code => /^[a-z_]{1,80}$/.test(code)))
    assert.ok(!JSON.stringify(receipt).includes('SECRET'))
    if (kind === 'deadline') {
      assert.equal(clock, o.timeoutMs)
      assert.ok(receipt.failures.includes('last_run_running'))
      assert.ok(receipt.failures.includes('last_run_messages_present'))
    }
    if (kind === 'stop_failure') assert.ok(receipt.failures.includes('owned_run_stop_failed'))
    const stops = p.calls.filter(c => c.route.endsWith('/run/stop'))
    assert.equal(stops.length, 1)
    assert.equal(stops[0].body.run_id, 'provider-run')
    assert.equal(stops[0].body.target_swarm_id, 'self')
    assert.ok(!p.calls.some(c => c.method === 'DELETE' || /approve|deploy/.test(c.route)))
  }
})

// Purpose: runner qualification requires a multi-step exact completed provider run,
// durable tool pairs and a pending task with zero intents. Incremental hydration
// must retain completed tools even after tail eviction; neither foreign completion
// nor task approval is a substitute. This protocol fixture proves runner behavior,
// not actual provider completion or backend transactional correctness.
test('multi-step project run retains tool evidence and qualifies pending unexecuted task', { timeout: 5000 }, async t => {
  const { options } = fixture(t), o = { ...options, scenario: 'orchestrator-chat' }, receipt = createReceipt(o), p = protocol(o, o.scenario)
  let clock = 0, samples = 0
  const fetch = async (url, init) => {
    const response = await p.fetch(url, init)
    if (new URL(url).pathname !== '/v3/sync/hydrate' || JSON.parse(init.body).session_ids[0] === 'task-session') return response
    samples++
    const final = await response.json()
    const proposal = final.events_by_session['owned-session'][0]
    const discovery = { session_id: 'owned-session', seq: 3, event_type: 'session.tool.started', payload: { run_id: 'provider-run', step: 1, call_id: 'discovery', tool_name: 'manage_projects', arguments: { action: 'list_sources' } } }
    const discovered = { ...discovery, seq: 4, event_type: 'session.tool.completed', payload: { ...discovery.payload, output: 'Sources inspected', raw_output: JSON.stringify({ sources: [] }) } }
    if (samples === 1) return Response.json(runSnapshot('pending_executor'))
    if (samples === 2) return Response.json(runSnapshot('running', { events_by_session: { 'owned-session': [discovery] } }))
    if (samples === 3) return Response.json(runSnapshot('running', { events_by_session: { 'owned-session': [discovery, discovered] } }))
    if (samples === 4) return Response.json(runSnapshot('running', { events_by_session: { 'owned-session': [{ ...proposal, seq: 5, event_type: 'session.tool.started', payload: { ...proposal.payload, output: undefined, step: 2 } }] } }))
    final.current_run_state_by_session = { 'owned-session': { session_id: 'owned-session', run_id: 'provider-run', status: 'completed', active: false } }
    final.session_views_by_id = { 'owned-session': { pending_permissions: [] } }
    final.events_by_session['owned-session'] = samples === 5 ? [{ ...proposal, seq: 6, payload: { ...proposal.payload, step: 2 } }] : []
    return Response.json(final)
  }
  await runScenario(o, receipt, { fetch, verifyCandidate: () => {}, now: () => clock, pause: async ms => { clock += ms } })
  assert.equal(samples, 6)
  assert.equal(clock, 2000)
  assert.ok(receipt.assertions.every(a => a.passed))
  assert.deepEqual(receipt.failures, [])
  assert.ok(p.calls.some(c => c.route === '/v3/sync/hydrate' && c.body.session_ids[0] === 'task-session'))
  assert.ok(!p.calls.some(c => c.route.endsWith('/run/stop') || /approve|deploy/.test(c.route)))
})

// Purpose: message admission is the ownership boundary for run/stop. Invalid or
// cross-session admission responses must not authorize cleanup, even when a run
// identifier is present. Actual runScenario transport assertions prove no stop or
// hydration follows rejected admission; no foreign run is mutated.
test('invalid or foreign admission never authorizes an owned-run stop', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  for (const run_intent of [{ run_id: 'bad/run' }, { run_id: 'foreign-run', session_id: 'foreign-session' }]) {
    const receipt = createReceipt(options), p = protocol(options, 'session-api')
    const fetch = async (url, init) => {
      const response = await p.fetch(url, init)
      return new URL(url).pathname.endsWith('/messages') ? Response.json({ run_intent }) : response
    }
    await assert.rejects(runScenario(options, receipt, { fetch, verifyCandidate: () => {} }), /^Error: run_not_admitted$/)
    assert.equal(receipt.assertions.find(a => a.name === 'run_admitted').passed, false)
    assert.ok(!p.calls.some(c => c.route.endsWith('/run/stop') || c.route === '/v3/sync/hydrate'))
  }
})

// Purpose: runScenario's explicit fixture consent must bind PermissionRecord's
// original executor call to the owned session/run/project/source, then require
// durable completion and pending-task zero intents. This transport-level test
// exercises the real runner, not backend transactional correctness or live AI.
function consentProtocol(o, { mutate = () => {}, duplicate = false, stale = false, resolution = 'allow', missing = false, badOutcome = false, batch = '', changed = false, mutateResolved = () => {}, mutateReply = reply => reply, mutateCompletion = () => {} } = {}) {
  const p = protocol(o, o.scenario), resolutions = []
  let clock = 0, samples = 0, index = 0, resolved = false
  const completedEvents = []
  const args = [
    { action: 'list_sources', project_id: 'project' },
    { action: 'inspect_source', project_id: 'project', workspace_path: o.workspacePath, workspace_id: 'source', workspace_generation: 1 },
    { action: 'propose_task', project_id: 'project', title: 'PR-' + o.runID, workspace_path: o.workspacePath,
      workspace_id: 'source', workspace_generation: 1, agent: 'swarm', feature_size: 'big', auto_approve: false,
      prompt: 'Add a short README explanation later.' },
  ]
  const record = () => {
    const r = { id: 'permission-' + index, session_id: 'owned-session', run_id: 'provider-run', call_id: 'call-' + index,
      step: index + 1, tool_name: 'manage_projects', status: 'pending', requirement: 'tool', mode: 'auto',
      decision: '', reason: '', resolved_at: 0, created_at: 1, updated_at: 1, approved_arguments: '{}',
      tool_arguments: JSON.stringify({ display_summary: 'SECRET non-authoritative summary' }), tool_call_arguments: JSON.stringify(args[index]) }
    if (changed && resolved) r.call_id = 'changed-call'
    mutate(r, index)
    return r
  }
  const fetch = async (url, init) => {
    const route = new URL(url).pathname
    if (/\/permissions\/[^/]+\/resolve$/.test(route)) {
      const body = JSON.parse(init.body), r = record()
      resolutions.push({ route, body })
      assert.deepEqual(body, { action: 'allow_once', reason: 'Exact owned pending-only PR fixture call' })
      if (resolution === 'http') return Response.json({ error: 'SECRET auth body' }, { status: 409 })
      resolved = true
      const permission = { ...r, status: resolution === 'deny' ? 'denied' : 'approved', decision: resolution === 'deny' ? 'deny' : 'allow_once',
        reason: body.reason, resolved_at: 2, updated_at: 2,
        ...(resolution === 'foreign' ? { run_id: 'foreign' } : {}) }
      // Direct resolve returns empty ApprovedArguments omitted by omitempty;
      // later store hydration/replay normalizes it to the JSON string '{}'.
      delete permission.approved_arguments
      mutateResolved(permission, index)
      // Actual API response: nil *PolicyRule becomes null; persistent rules are objects.
      return Response.json(mutateReply({ ok: true, session_id: 'owned-session',
        saved_rule: resolution === 'rule' ? { id: 'unexpected-rule', decision: 'allow' } : null, permission }))
    }
    const response = await p.fetch(url, init)
    if (route === '/v1/swarm/topology') {
      const result = await response.json()
      Object.assign(result.workspace_bindings[0], { source_workspace_id: 'source', source_workspace_generation: 1 })
      return Response.json(result)
    }
    if (route !== '/v3/sync/hydrate' || JSON.parse(init.body).session_ids[0] === 'task-session') {
      if (badOutcome && route.endsWith('/tasks/task')) {
        const result = await response.json(); result.task.status = 'in_progress'; return Response.json(result)
      }
      return response
    }
    samples++
    if (resolved && !(duplicate || stale)) {
      const payload = { run_id: 'provider-run', call_id: 'call-' + index, tool_name: 'manage_projects',
        arguments: JSON.stringify(args[index]), output: 'Completed project operation',
        raw_output: JSON.stringify(index === 2 ? { task: { id: 'task', title: 'PR-' + o.runID } } : { sources: [] }) }
      mutateCompletion(payload, index)
      completedEvents.push({ seq: index + 1, event_type: 'session.tool.completed', payload })
      index++; resolved = false
    }
    else if (resolved && duplicate && !stale) { duplicate = false } // one stale snapshot, no second grant
    if (index < args.length) {
      const snapshot = runSnapshot(), r = record()
      snapshot.events_by_session['owned-session'] = [...completedEvents]
      if (missing) {
        delete snapshot.session_views_by_id
        snapshot.events_by_session['owned-session'] = [{ event_type: 'permission.requested', payload: { run_id: 'provider-run', permission: r } }]
      } else snapshot.session_views_by_id['owned-session'].pending_permissions = batch === 'duplicate' ? [r, r]
        : batch === 'mixed' ? [r, { ...r, id: 'unsafe', call_id: 'unsafe', tool_name: 'task' }]
          : batch === 'budget' ? Array.from({ length: 13 }, (_, i) => ({ ...r, id: 'p-' + i, call_id: 'c-' + i })) : [r]
      return Response.json(snapshot)
    }
    const result = await response.json()
    result.session_views_by_id = { 'owned-session': { pending_permissions: [] } }
    // Retain discovery completions through the next provider step and final
    // rehydration; display previews and canonical raw JSON are separate fields.
    result.events_by_session['owned-session'] = [...completedEvents]
    return Response.json(result)
  }
  return { ...p, fetch, resolutions, samples: () => samples, deps: { fetch, verifyCandidate: () => {}, now: () => clock, pause: async ms => { clock += ms } } }
}
test('exact discovery and pending proposal receive only allow_once and require canonical outcome', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  // Store hydration emits '{}'; Service.resolveLocked can return an omitted
  // omitempty string before persistence sanitizes it. Empty string/whitespace
  // use the same sanitizePermissionArguments no-override normalization.
  for (const config of [{}, { duplicate: true },
    // Isolate the old resolved truthiness defect from the pending check.
    { mutate: r => { delete r.approved_arguments }, mutateResolved: r => { r.approved_arguments = '{}' } },
    ...[undefined, '', ' ', '{}', ' { } '].map(value => ({ mutateResolved: r => {
      if (value === undefined) delete r.approved_arguments
      else r.approved_arguments = value
    } })),
    ...[undefined, '', ' '].map(value => ({ mutate: r => {
      if (value === undefined) delete r.approved_arguments
      else r.approved_arguments = value
    } }))]) {
    const o = { ...options, scenario: 'orchestrator-chat' }, r = createReceipt(o), p = consentProtocol(o, config)
    await runScenario(o, r, p.deps)
    assert.ok(r.assertions.every(a => a.passed))
    assert.equal(p.resolutions.length, 3)
    assert.equal(new Set(p.resolutions.map(c => c.route)).size, 3)
    assert.ok(p.calls.some(c => c.route === '/v3/sync/hydrate' && c.body.session_ids[0] === 'task-session'))
    assert.ok(!p.calls.some(c => /approve|deploy|resolve_all/.test(c.route) || c.route.endsWith('/run/stop')))
    assert.ok(!JSON.stringify(r).includes('SECRET'))
  }
})

// Purpose: no unsafe/ambiguous permission may cause any grant or successful
// assertion. Unknown fields, wrong ownership, source identity, proposal semantics
// and nested/string JSON are checked at the runner's per-call boundary; rejection
// categories must not expose raw arguments, provider bodies or secret summaries.
test('fixture permission rejects unsafe or ambiguous calls before any resolution', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  const changes = [
    r => { r.tool_name = 'task' }, r => { r.session_id = 'foreign' }, r => { r.run_id = 'foreign' },
    r => { delete r.call_id }, r => { delete r.id }, r => { r.status = 'approved' },
    r => { r.tool_arguments = JSON.stringify({ approved_arguments: { action: 'deploy_task' } }) },
    // Even a plausible full display call cannot substitute for executor evidence.
    r => { r.tool_arguments = r.tool_call_arguments; r.tool_call_arguments = '{}' },
    r => { r.tool_arguments = r.tool_call_arguments; delete r.tool_call_arguments },
    r => { r.tool_arguments = r.tool_call_arguments; r.tool_call_arguments = '' },
    r => { r.tool_call_arguments = 'SECRET malformed JSON' }, r => { r.tool_call_arguments = JSON.stringify(JSON.stringify({ action: 'list_sources' })) },
    r => { r.tool_call_arguments = JSON.stringify({ action: 'inspect_source', project_id: 'project', workspace_path: { path: options.workspacePath } }) },
    ...['approve_task', 'accept_task', 'deploy_task', 'create_task', 'list', 'get', 'inspect_files'].map(action => r => {
      r.tool_call_arguments = JSON.stringify({ action, project_id: 'project' })
    }),
    ...[
      { project_id: 'foreign' }, { workspace_path: 'foreign' }, { workspace_id: 'foreign' }, { workspace_generation: 2 },
      { title: 'foreign' }, { auto_approve: true }, { auto_approve: 'false' }, { auto_approve: null },
      { agent: 'image' }, { feature_size: 'small' }, { task_program: {} }, { operation: 'create' }, { model: 'override' },
      { deploy: false }, { approved_arguments: {} },
    ].map(patch => r => { r.tool_call_arguments = JSON.stringify({ action: 'propose_task', project_id: 'project', title: 'PR-' + options.runID,
      workspace_path: options.workspacePath, agent: 'swarm', feature_size: 'big', auto_approve: false, ...patch }) }),
    ...['auto_approve', 'workspace_path', 'agent', 'feature_size'].map(key => r => {
      const a = { action: 'propose_task', project_id: 'project', title: 'PR-' + options.runID, workspace_path: options.workspacePath,
        agent: 'swarm', feature_size: 'big', auto_approve: false }; delete a[key]; r.tool_call_arguments = JSON.stringify(a)
    }),
  ]
  for (const mutate of changes) {
    const o = { ...options, scenario: 'orchestrator-chat' }, r = createReceipt(o), p = consentProtocol(o, { mutate })
    await assert.rejects(runScenario(o, r, p.deps), /^Error: permission_[a-z_]+$/)
    assert.equal(p.resolutions.length, 0)
    assert.equal(p.samples(), 1)
    assert.equal(r.assertions.find(a => a.name === 'run_completed').passed, false)
    assert.ok(!JSON.stringify(r).includes('SECRET'))
  }
  for (const scenario of ['session-api', 'image', 'video', 'audio']) {
    const o = { ...options, scenario }, r = createReceipt(o), p = consentProtocol(o)
    await assert.rejects(runScenario(o, r, p.deps), /^Error: run_permission_pending$/)
    assert.equal(p.resolutions.length, 0)
  }
})

// Purpose: allow_once replies are not completion. A racing deny/foreign record,
// unexpected saved rule, failed request, missing canonical call evidence or
// repeated stale pending record must fail without a retry grant or false PASS.
test('permission races failures and stale snapshots never become qualification success', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  for (const config of [{ resolution: 'http' }, { resolution: 'deny' }, { resolution: 'foreign' }, { resolution: 'rule' },
    { stale: true }, { stale: true, changed: true }, { missing: true }, { badOutcome: true },
    { batch: 'duplicate' }, { batch: 'mixed' }, { batch: 'budget' }]) {
    const o = { ...options, scenario: 'orchestrator-chat' }, r = createReceipt(o), p = consentProtocol(o, config)
    await assert.rejects(runScenario(o, r, p.deps), /^Error: (permission_[a-z_]+|task_not_pending)$/)
    assert.equal(p.resolutions.length, config.missing || config.batch ? 0 : config.badOutcome ? 3 : 1)
    assert.ok(p.samples() <= 5)
    assert.notEqual(r.status, 'PASS')
    assert.ok(r.assertions.some(a => !a.passed))
    assert.ok(!JSON.stringify(r).includes('SECRET'))
  }
})

// Purpose: fixture drift must not hide PermissionRecord's string normalization.
// This source-contract tripwire ties the wire fixture to PutPermissionWithSummary,
// sanitizePermissionRecord, Service.resolveLocked and the canonical V3 reply.
// It proves only source shape, not store durability or backend runtime behavior;
// the transport regressions below prove the runner's observable fail-closed gate.
test('permission fixture tracks canonical stored strings and direct resolve serialization', () => {
  const source = relative => readFileSync(new URL('../../' + relative, import.meta.url), 'utf8')
  const store = source('swarmd/internal/store/pebble/permission_store.go')
  const service = source('swarmd/internal/permission/service.go')
  const api = source('swarmd/internal/api/sessions_v3_primary.go')
  assert.match(store, /ApprovedArguments\s+string `json:"approved_arguments,omitempty"`/)
  assert.match(store, /ToolCallArguments\s+string `json:"tool_call_arguments,omitempty"`/)
  assert.match(store, /record\.ApprovedArguments = sanitizePermissionArguments\(record\.ApprovedArguments\)/)
  assert.match(store, /record\.ToolCallArguments = sanitizePermissionArguments\(record\.ToolCallArguments\)/)
  assert.match(store, /func sanitizePermissionArguments\(raw string\) string \{\s*trimmed := strings\.TrimSpace\(raw\)\s*if trimmed == "" \{\s*return "\{\}"/)
  assert.match(service, /updated\.ApprovedArguments, err = approvedArgumentsForResolution/)
  assert.match(service, /PutPermissionWithSummary\(updated, &record, summary\)/)
  assert.match(service, /return updated, true, nil/)
  assert.match(service, /func sanitizeApprovedArguments[\s\S]*?if approvedArguments == "" \{\s*return ""/)
  assert.match(api, /PendingPermissions\s+\[\]pebblestore\.PermissionRecord/)
  assert.match(api, /"permission": record, "saved_rule": savedRule/)
})

// Purpose: fixtureConsent must distinguish no override from any unsafe override
// at both hydration and allow_once reply boundaries. Production store strings
// '{}' are the success baseline above (old truthiness checks reject it). Reject
// malformed, encoded, scalar, null, array and nonempty objects, including changes
// only at resolution, with no grant retries, task approval/deployment or false PASS.
test('permission overrides fail closed at pending and resolved boundaries', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  const unsafe = ['SECRET malformed JSON', '[]', '[{}]', 'null', 'false', '0', '""', '"{}"',
    '{"action":"deploy_task"}', '{"unused":null}', JSON.stringify(JSON.stringify({ action: 'deploy_task' })),
    null, false, 0, [], {}, { action: 'deploy_task' }]
  for (const phase of ['pending', 'resolved']) {
    for (const value of unsafe) {
      const o = { ...options, scenario: 'orchestrator-chat' }, r = createReceipt(o)
      const mutate = record => { record.approved_arguments = value }
      const p = consentProtocol(o, phase === 'pending' ? { mutate } : { mutateResolved: mutate })
      await assert.rejects(runScenario(o, r, p.deps), phase === 'pending'
        ? /^Error: permission_override_rejected$/ : /^Error: permission_resolution_override_rejected$/)
      assert.equal(p.resolutions.length, phase === 'pending' ? 0 : 1)
      assert.equal(p.samples(), 1)
      assert.equal(r.assertions.find(a => a.name === 'run_completed').passed, false)
      assert.notEqual(r.status, 'PASS')
      assert.ok(!p.calls.some(c => /approve|deploy|resolve_all/.test(c.route)))
      assert.ok(!JSON.stringify(r).includes('SECRET'))
    }
  }
})

// Purpose: override normalization cannot weaken exact executor-call identity at
// resolution. Changed session/run/call/tool/project/source/title or missing original
// call must fail with one attempted grant and no subsequent task execution.
test('normalized empty overrides never authorize changed resolution evidence', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  const changes = [
    r => { r.session_id = 'foreign' }, r => { r.run_id = 'foreign' }, r => { r.call_id = 'foreign' },
    r => { r.tool_name = 'task' }, r => { r.tool_arguments = r.tool_call_arguments; r.tool_call_arguments = '{}' },
    ...[{ project_id: 'foreign' }, { action: 'deploy_task' }, { workspace_path: 'foreign' }, { title: 'foreign' }]
      .map(patch => r => { r.tool_call_arguments = JSON.stringify({ ...JSON.parse(r.tool_call_arguments), ...patch }) }),
  ]
  for (const mutateResolved of changes) {
    const o = { ...options, scenario: 'orchestrator-chat' }, r = createReceipt(o), p = consentProtocol(o, { mutateResolved })
    await assert.rejects(runScenario(o, r, p.deps), /^Error: permission_resolution_mismatch$/)
    assert.equal(p.resolutions.length, 1)
    assert.equal(p.samples(), 1)
    assert.equal(r.assertions.find(a => a.name === 'run_completed').passed, false)
    assert.ok(!p.calls.some(c => /approve|deploy|resolve_all/.test(c.route)))
  }
})

// Purpose: fixtureConsent must require the canonical explicit null PolicyRule,
// not truthiness or the old boolean fixture. At the HTTP reply boundary reject
// malformed envelopes, identity/decision drift and persistent rules after one
// grant, without retry, completion, task approval/deployment or secret output.
test('allow_once requires explicit null saved rule and strict resolution envelope', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  const cases = [
    ...[{}, { id: 'SECRET rule' }, true, false, 0, 1, '', 'null', [], undefined].map(value => ({
      code: 'permission_resolution_rule_rejected', mutateReply: reply => {
        if (value === undefined) delete reply.saved_rule
        else reply.saved_rule = value
        return reply
      },
    })),
    ...[null, [], false, 'SECRET envelope', {}, { ok: true, permission: [] }].map(value => ({
      code: 'permission_resolution_envelope_rejected', mutateReply: () => value,
    })),
    { code: 'permission_resolution_envelope_rejected', mutateReply: reply => ({ ...reply, ok: 'true' }) },
    { code: 'permission_resolution_mismatch', mutateReply: reply => ({ ...reply, session_id: 'foreign' }) },
    ...[{ status: 'pending' }, { decision: 'allow_always' }, { decision: false }, { decision: undefined }].map(patch => ({
      code: 'permission_resolution_decision_rejected', mutateResolved: record => Object.assign(record, patch),
    })),
  ]
  for (const { code, ...config } of cases) {
    const o = { ...options, scenario: 'orchestrator-chat' }, r = createReceipt(o), p = consentProtocol(o, config)
    await assert.rejects(runScenario(o, r, p.deps), new RegExp('^Error: ' + code + '$'))
    assert.equal(p.resolutions.length, 1)
    assert.equal(p.samples(), 1)
    assert.equal(r.assertions.find(a => a.name === 'run_completed').passed, false)
    assert.equal(r.assertions.find(a => a.name === 'zero_task_intents').passed, false)
    assert.notEqual(r.status, 'PASS')
    assert.ok(!p.calls.some(c => /approve|deploy|resolve_all/.test(c.route)))
    assert.ok(!JSON.stringify(r).includes('SECRET'))
  }
})

// Purpose: recordProviderToolEvent persists a display preview separately from
// liveStreamRawOutput's exact manage_projects JSON. formatToolCompletedOutput
// summarizes this tool, so display text cannot prove discovery or a proposal.
// The event parser is the narrowest runner boundary; this is not live RCA proof.
test('project completion uses canonical raw output, never display proposal text', () => {
  const event = { event_type: 'session.tool.completed', payload: {
    run_id: 'owned-run', call_id: 'call', tool_name: 'manage_projects',
    arguments: JSON.stringify({ action: 'propose_task' }),
    output: 'SECRET display preview', raw_output: JSON.stringify({ task: { id: 'task' } }),
  } }
  assert.equal(toolEvidence([event], 'owned-run')[0].output.task.id, 'task')
  for (const raw_output of [undefined, 'SECRET malformed', 'null', '[]', '"{}"']) {
    const bad = structuredClone(event); bad.payload.raw_output = raw_output
    assert.throws(() => toolEvidence([bad], 'owned-run'), /^Error: tool_output_contract_invalid$/)
  }
  const bad = structuredClone(event); bad.payload.arguments = 'SECRET malformed'
  assert.throws(() => toolEvidence([bad], 'owned-run'), /^Error: tool_arguments_invalid$/)
})

// Purpose: receipt diagnostics must preserve only runner-owned codes and fixed
// phases, never arbitrary exception messages (even code-looking secret values),
// non-Error throws or provider/auth JSON. Exercise the same terminal serializer.
test('terminal failure diagnostics reject arbitrary messages and non-Error throws', () => {
  for (const error of [new Error('secret_marker'), new SyntaxError('SECRET token'), null, undefined, 'SECRET', { message: 'secret_marker' }]) {
    const r = { status: 'FAIL', failures: [] }
    recordFailure(r, error)
    assert.deepEqual(r.failures, ['runtime_exception'])
    assert.equal(r.native_exit, 2)
    assert.ok(!JSON.stringify(r).includes('SECRET'))
    assert.ok(!JSON.stringify(r).includes('secret_marker'))
  }
})

// Purpose: canonical consent -> completion -> next step must reject corrupt
// executor evidence before another grant, without trusting a display proposal.
// runScenario is the narrowest protocol layer; existing success cases above
// also require final rehydration, pending task/source and zero task intents.
test('malformed completion stops consent progression with safe phase diagnostics', { timeout: 5000 }, async t => {
  const { options } = fixture(t), o = { ...options, scenario: 'orchestrator-chat' }
  for (const [field, value, code] of [
    ['raw_output', 'SECRET malformed', 'tool_output_contract_invalid'],
    ['raw_output', 'null', 'tool_output_contract_invalid'],
    ['raw_output', undefined, 'tool_output_contract_invalid'],
    ['arguments', 'SECRET malformed', 'tool_arguments_invalid'],
  ]) {
    const r = createReceipt(o), p = consentProtocol(o, { mutateCompletion: (payload, index) => {
      if (index === 0) payload[field] = value
    } })
    try { await runScenario(o, r, p.deps); assert.fail('corrupt completion admitted') }
    catch (error) { assert.equal(error.message, code); recordFailure(r, error) }
    assert.equal(p.resolutions.length, 1)
    assert.equal(r.assertions.find(a => a.name === 'run_completed').passed, false)
    assert.equal(r.assertions.find(a => a.name === 'zero_task_intents').passed, false)
    assert.ok(r.failures.includes('phase_run_observation'))
    assert.ok(r.failures.includes(code))
    assert.ok(!JSON.stringify(r).includes('SECRET'))
    assert.ok(!p.calls.some(c => /approve|deploy/.test(c.route)))
  }
})

// Purpose: API transport/decode and unknown runtime errors must remain distinct
// in the existing failures array, without leaking even code-shaped messages.
// Execute real runScenario boundaries, not a separate mock error classifier.
test('API and runtime failures retain fixed codes and phase without bodies', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  for (const [fetch, code] of [
    [async () => { throw new Error('SECRET credentials') }, 'api_transport_failed'],
    [async () => new Response('SECRET invalid JSON'), 'api_json_invalid'],
    [async () => Response.json({ secret: 'SECRET' }, { status: 409 }), 'http_409'],
  ]) {
    const r = createReceipt(options)
    try { await runScenario(options, r, { fetch, verifyCandidate: () => {} }); assert.fail('bad API admitted') }
    catch (error) { recordFailure(r, error) }
    assert.deepEqual(r.failures, ['phase_model_settings', code])
    assert.equal(r.native_exit, 2)
    assert.ok(!JSON.stringify(r).includes('SECRET'))
    assert.equal(r.assertions.find(a => a.name === 'session_identity').passed, false)
  }
})

// Purpose: catch fixture drift at the exact producer/event/hydration boundaries.
// This source tripwire is not backend runtime proof: protocol assertions above
// exercise runner behavior; parent must separately validate Go serialization.
test('completion fixture tracks canonical raw result and display preview producers', () => {
  const source = relative => readFileSync(new URL('../../' + relative, import.meta.url), 'utf8')
  const formatter = source('swarmd/internal/run/service_tool_output.go')
  const executor = source('swarmd/internal/api/sessions_v3_executor.go')
  const invoker = source('swarmd/internal/run/provider_tool_invoker.go')
  const projects = source('swarmd/internal/tool/runtime_manage_projects.go')
  assert.match(formatter, /func liveStreamRawOutput[\s\S]*?!= "bash" \{\s*return output/)
  assert.match(formatter, /func formatToolCompletedOutput[\s\S]*?return summarizeToolOutput/)
  assert.match(invoker, /RawOutput:\s+liveStreamRawOutput\(call, result\)/)
  assert.match(executor, /payload\["output"\] = output/)
  assert.match(executor, /payload\["raw_output"\] = rawOutput/)
  assert.match(projects, /response\["task"\] = task/)
  assert.match(projects, /json.Marshal\(response\)/)
})
