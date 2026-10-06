import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtempSync, rmSync, symlinkSync, readFileSync, existsSync } from 'node:fs'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { SCENARIOS, parseOptions, createReceipt, validateReceipt, requiredAssertions, toolEvidence, mediaEvidence, runScenario, assertSafeToolRouting } from './orchestrator-pr.mjs'

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
    arguments: JSON.stringify({ action: 'propose_task' }), output: JSON.stringify({ task: { id: 'task' } }) }) }
  assert.equal(toolEvidence([event], 'owned-run')[0].output.task.id, 'task')
  assert.deepEqual(toolEvidence([event], 'foreign-run'), [])
  assert.deepEqual(toolEvidence([{ ...event, event_type: 'session.tool.started' }], 'owned-run'), [])
  assert.throws(() => toolEvidence([event, event], 'owned-run'), /invalid_tool/)
  assert.throws(() => toolEvidence([{ ...event, event_type: 'session.tool.failed' }], 'owned-run'), /tool_failed/)
  assert.throws(() => toolEvidence([{ ...event, payload: { run_id: 'owned-run', call_id: 'call' } }], 'owned-run'), /tool_output/)
})

// Purpose: runScenario dispatch must use real create/send/hydrate routes and never
// task POST as AI evidence or settings/permission writes. An immediate protocol
// fixture is the narrowest deterministic runner test; it is NOT live E2E, latency
// evidence, a simulated workload or a benchmark. Assert requests and failure stop.
function protocol(options, scenario, { failed = false, missingCapability = false, badBytes = false, wrongModel = false, stopFailure = false } = {}) {
  const calls = []; let prompt = ''
  const { tools, data } = media(scenario)
  const fetch = async (url, init) => {
    const route = new URL(url).pathname, body = init.body ? JSON.parse(init.body) : undefined
    calls.push({ method: init.method, route, body })
    let result
    if (route === '/v1/agent-model-settings') result = { agent_model_settings: { swarm: { action: { provider: 'configured-provider', model: 'configured', thinking: 'off' }, plan: { provider: 'configured-provider', model: 'configured', thinking: 'off' } }, system_agents: { router: { model: 'configured' } } } }
    else if (route === '/v1/swarm/topology') result = { runtimes: [{ relationship: 'self', swarm_id: 'self' }], workspace_bindings: [{ state: 'bound', source_workspace_path: options.workspacePath, workspace_binding_id: 'binding' }] }
    else if (route === '/v3/projects') result = { project: { id: 'project' } }
    else if (route === '/v3/sessions' || route === '/v3/projects/project/sessions') result = { session_id: 'owned-session', session: { id: 'owned-session', mode: 'auto', project_id: 'project' } }
    else if (route.endsWith('/messages')) { prompt = body.content; result = { run_intent: { run_id: 'provider-run' } } }
    else if (route.endsWith('/run/stop')) {
      if (stopFailure) return Response.json({ error: 'not included in evidence' }, { status: 500 })
      result = { cancelled: true }
    }
    else if (route === '/v3/projects/project/tasks/task') result = { task: { id: 'task', project_id: 'project', agent: 'swarm', status: 'pending_approval', source_workspace: { path: options.workspacePath }, session_id: 'task-session' } }
    else if (route === '/v3/sync/hydrate' && body.session_ids[0] === 'task-session') result = { run_intents_by_session: { 'task-session': [] } }
    else if (route === '/v3/sync/hydrate') {
      const routed = scenario === 'orchestrator-chat' ? [{ name: 'manage_projects', args: { action: 'propose_task', project_id: 'project' }, output: { task: { id: 'task', title: 'PR-' + options.runID } } }]
        : scenario === 'session-api' ? [] : missingCapability ? tools.slice(1) : tools
      result = { run_intents_by_session: { 'owned-session': [{ run_id: 'provider-run', status: failed ? 'failed' : 'completed' }] },
        messages_by_session: { 'owned-session': [{ role: 'user', content: prompt }, { role: 'assistant', content: scenario === 'session-api' ? 'PR-' + options.runID : 'Provider reply', metadata: { run_id: 'provider-run', provider: 'configured-provider', model: wrongModel ? 'wrong-model' : 'configured' } }] },
        events_by_session: { 'owned-session': routed.map((record, i) => ({ event_type: 'session.tool.completed', payload: { run_id: 'provider-run', call_id: 'call-' + i, tool_name: record.name, arguments: record.args, output: record.output } })) } }
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

// Purpose: required missing capability/byte failure is never PASS. Failed runs
// are stopped only by exact owned session/run/runtime, preserving pending tasks
// and artifacts. Immediate failure injection observes no unrelated cleanup.
test('required capability, byte and run failures never qualify and stop only owned live run', { timeout: 5000 }, async t => {
  const { options } = fixture(t)
  for (const failure of [{ failed: true }, { missingCapability: true }, { badBytes: true }, { wrongModel: true }, { failed: true, stopFailure: true }]) {
    const o = { ...options, scenario: 'image' }, receipt = createReceipt(o), p = protocol(o, 'image', failure)
    await assert.rejects(runScenario(o, receipt, { fetch: p.fetch, verifyCandidate: () => {} }), failure.failed ? /run_failed/ : undefined)
    if (failure.stopFailure) assert.deepEqual(receipt.failures, ['owned_run_stop_failed'])
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
test('tool-start guard rejects settings, approval, delegation and multiple generations', { timeout: 5000 }, () => {
  const started = (name, args, call = 'call') => ({ event_type: 'session.tool.started', payload: { run_id: 'owned', call_id: call, tool_name: name, arguments: args } })
  assert.doesNotThrow(() => assertSafeToolRouting([started('manage_projects', { action: 'propose_task' })], 'owned', 'orchestrator-chat'))
  for (const [name, args] of [['manage_agent', { action: 'update' }], ['task', { mode: 'swarm' }], ['manage_projects', { action: 'approve_task' }]]) {
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
