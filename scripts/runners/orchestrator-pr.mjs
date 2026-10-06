#!/usr/bin/env node
// Opt-in live qualification, never a benchmark or a hermetic-tier member.
// Reuses the canonical session/hydration and progress observer pattern from
// artifact-v3-edit-repair; no account settings, auth bootstrap or source writes.
import { createHash } from 'node:crypto'
import { openSync, closeSync, writeFileSync, realpathSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'
import { waitStage } from './artifact-v3-edit-repair.mjs'

export const SCENARIOS = Object.freeze(['session-api', 'orchestrator-chat', 'image', 'video', 'audio'])
export const RECEIPT_SCHEMA = 'swarm.orchestrator-pr.v1'
const check = (value, code) => { if (!value) throw new Error(code) }
const decode = value => typeof value === 'string' ? JSON.parse(value) : value
const id = value => typeof value === 'string' && /^[a-zA-Z0-9_.:-]{1,200}$/.test(value)

export function parseOptions(argv, env = process.env) {
  const allowed = ['api-url', 'workspace-path', 'scenario', 'timeout-ms', 'output', 'candidate-revision', 'run-id']
  const o = {}
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i]?.slice(2), value = argv[i + 1]
    check(argv[i]?.startsWith('--') && allowed.includes(key) && !(key in o) && value && !value.startsWith('--'), 'invalid_option')
    o[key] = value
  }
  check(allowed.every(key => o[key]), 'explicit_inputs_required')
  const url = new URL(o['api-url'])
  check(['http:', 'https:'].includes(url.protocol) && ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
    && !url.username && !url.password && !url.search && !url.hash && url.pathname === '/', 'loopback_origin_required')
  check(SCENARIOS.includes(o.scenario), 'explicit_single_scenario_required')
  check(path.isAbsolute(o['workspace-path']) && path.normalize(o['workspace-path']) === o['workspace-path'], 'absolute_workspace_required')
  const timeoutMs = Number(o['timeout-ms'])
  check(Number.isInteger(timeoutMs) && timeoutMs >= 30000 && timeoutMs <= 900000, 'invalid_deadline')
  check(/^[a-f0-9]{40}$/.test(o['candidate-revision']) && id(o['run-id']), 'candidate_and_run_identity_required')
  validateOutput(o.output, env.TMPDIR)
  check(env.SWARM_RUNNER_TOKEN?.trim() && !/[\r\n]/.test(env.SWARM_RUNNER_TOKEN), 'token_environment_required')
  return { apiURL: url.origin, workspacePath: o['workspace-path'], scenario: o.scenario, timeoutMs,
    output: o.output, candidate: o['candidate-revision'], runID: o['run-id'], token: env.SWARM_RUNNER_TOKEN.trim() }
}

export function validateOutput(output, tmpdir) {
  check(tmpdir && path.isAbsolute(tmpdir) && path.isAbsolute(output) && path.normalize(output) === output, 'absolute_tmpdir_output_required')
  const tmp = realpathSync(tmpdir), parent = realpathSync(path.dirname(output))
  check(parent === path.dirname(output) && (parent === tmp || parent.startsWith(tmp + path.sep)), 'output_outside_tmpdir')
}

export function requiredAssertions(scenario) {
  if (scenario === 'new-task-browser') return ['candidate_revision', 'browser_creation_persistence', 'owned_cleanup']
  check(SCENARIOS.includes(scenario), 'unknown_scenario')
  return ['candidate_revision', 'configured_models', 'workspace_binding', 'session_identity', 'run_admitted',
    'provider_response', 'history_rehydrate', 'run_completed', ...(scenario === 'orchestrator-chat'
      ? ['ai_task_routing', 'durable_pending_task', 'task_source', 'zero_task_intents']
      : scenario !== 'session-api' ? ['media_tool_routing', 'ready_exact_reference', 'media_bytes',
        ...(scenario !== 'video' ? ['capability_discovery'] : [])] : [])]
}

export function createReceipt(o) {
  return { schema: RECEIPT_SCHEMA, scenario: o.scenario, candidate_revision: o.candidate, run_id: o.runID,
    status: 'NOT_RUN', native_exit: 2, assertion_count: 0,
    assertions: requiredAssertions(o.scenario).map(name => ({ name, passed: false })), failures: [], evidence: [] }
}

// Consumer must enforce identity, native process exit AND every named assertion.
export function validateReceipt(r, expected, exit) {
  const names = requiredAssertions(expected.scenario)
  check(r?.schema === RECEIPT_SCHEMA && r.scenario === expected.scenario && r.candidate_revision === expected.candidate
    && r.run_id === expected.runID, 'receipt_identity_mismatch')
  check(exit === 0 && r.native_exit === 0 && r.status === 'PASS' && Array.isArray(r.failures) && r.failures.length === 0, 'receipt_not_pass')
  check(r.assertion_count === names.length && Array.isArray(r.assertions) && r.assertions.length === names.length
    && names.every((name, i) => r.assertions[i]?.name === name && r.assertions[i]?.passed === true), 'receipt_assertions_incomplete')
  return true
}

export function assertSafeToolRouting(events, runID, scenario) {
  let starts = 0, generations = 0
  for (const event of events) {
    if (!['session.tool.started', 'session.tool.completed'].includes(event.event_type)) continue
    const e = decode(event.payload)
    if (e?.run_id !== runID) continue
    check(scenario !== 'session-api', 'session_api_unexpected_tool')
    const args = decode(e.arguments)
    check(args && ['manage_projects', 'manage_artifact', 'search', 'find', 'list', 'read', 'media_inspect'].includes(e.tool_name), 'unexpected_tool_routing')
    if (event.event_type === 'session.tool.started') {
      check(++starts <= 12, 'tool_budget_exceeded')
      if (e.tool_name === 'manage_artifact' && args.action?.startsWith('generate_')) check(++generations <= 1, 'single_media_generation_required')
    }
    if (e.tool_name === 'manage_projects') {
      check(scenario === 'orchestrator-chat' && ['help', 'list', 'get', 'list_tasks', 'get_task', 'propose_task', 'create_task'].includes(args.action), 'unexpected_project_mutation')
    }
    if (e.tool_name === 'manage_artifact') {
      check(['image', 'video', 'audio'].includes(scenario) && ['image_capabilities', 'audio_capabilities', 'help', `generate_${scenario}`].includes(args.action)
        && !args.model && !args.provider && (!args.count || args.count === 1) && !args.prompts && !args.scenes, 'unexpected_media_mutation')
    }
  }
}

export function toolEvidence(events, runID) {
  const tools = [], seen = new Set()
  for (const event of events) {
    const e = decode(event.payload)
    if (e?.run_id !== runID) continue
    check(!['session.tool.failed', 'session.tool.cancelled', 'session.tool.canceled'].includes(event.event_type), 'tool_failed')
    if (event.event_type !== 'session.tool.completed') continue
    check(e && !e.error && e.call_id && !seen.has(e.call_id) && (!e.run_id || e.run_id === runID), 'invalid_tool_evidence')
    seen.add(e.call_id)
    check(e.arguments && e.output, 'tool_output_unavailable')
    tools.push({ name: e.tool_name || e.tool, args: decode(e.arguments), output: decode(e.output) })
  }
  check(tools.length <= 12, 'tool_budget_exceeded')
  return tools
}

export function mediaEvidence(tools, scenario, sessionID) {
  const records = tools.filter(t => t.name === 'manage_artifact')
  const generated = records.filter(t => t.args.action === `generate_${scenario}`)
  check(records.filter(t => t.args.action?.startsWith('generate_')).length === 1, 'single_media_generation_required')
  check(generated.length === 1, 'single_media_generation_required')
  const result = generated[0], a = result.output.artifact, ref = result.output.reference
  check(a?.status === 'ready' && a.session_id === sessionID && id(a.id) && id(a.collection_id)
    && Number.isSafeInteger(a.event_seq) && a.event_seq > 0 && a.media_type?.startsWith(scenario + '/')
    && /^[a-f0-9]{64}$/.test(a.digest_sha256) && Number.isSafeInteger(a.size) && a.size > 0 && a.size <= 64 * 1024 * 1024, 'ready_media_unavailable')
  check(ref?.session_id === sessionID && ref.collection_id === a.collection_id && ref.variant_id === a.id && ref.event_seq === a.event_seq, 'exact_reference_mismatch')
  check(!result.args.model && !result.args.provider && (!result.args.count || result.args.count === 1)
    && !result.args.prompts && !result.args.scenes && (!result.output.references || result.output.references.length === 1), 'unbounded_or_overridden_generation')
  if (scenario !== 'video') {
    const caps = records.filter(t => t.args.action === `${scenario}_capabilities`)
    check(caps.length === 1 && records.indexOf(caps[0]) < records.indexOf(result), 'capability_discovery_required')
    const capability = caps[0].output[`${scenario}_capabilities`]
    check(capability?.capability_token && result.args.capability_token === capability.capability_token, 'capability_token_mismatch')
  }
  return { artifact_reference: { session_id: ref.session_id, collection_id: ref.collection_id, variant_id: ref.variant_id, event_seq: ref.event_seq },
    media_type: a.media_type, size: a.size, digest_sha256: a.digest_sha256 }
}

export function verifyCandidate(candidate) {
  const root = fileURLToPath(new URL('../../', import.meta.url))
  const result = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8', timeout: 5000, maxBuffer: 1024 })
  check(!result.error && result.status === 0 && result.stdout.trim() === candidate, 'candidate_revision_mismatch')
  const status = spawnSync('git', ['status', '--porcelain', '--untracked-files=normal'], { cwd: root, encoding: 'utf8', timeout: 5000, maxBuffer: 4096 })
  check(!status.error && status.status === 0 && status.stdout.trim() === '', 'candidate_worktree_dirty')
}

async function boundedBytes(response, limit) {
  check(response.body, 'response_body_unavailable')
  const chunks = []; let size = 0
  const reader = response.body.getReader()
  try {
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      size += value.byteLength
      check(size <= limit, 'response_budget_exceeded')
      chunks.push(Buffer.from(value))
    }
  } finally { await reader.cancel() }
  return Buffer.concat(chunks)
}

export async function runScenario(o, r, deps = {}) {
  const mark = name => { const a = r.assertions.find(a => a.name === name); check(a, 'unknown_assertion'); a.passed = true }
  ;(deps.verifyCandidate ?? verifyCandidate)(o.candidate); mark('candidate_revision')
  const deadline = Date.now() + o.timeoutMs
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), o.timeoutMs)
  const interrupt = () => controller.abort()
  process.once('SIGINT', interrupt); process.once('SIGTERM', interrupt)
  let primaryFailure
  let sessionID = '', runID = '', swarmID = '', completed = false, requests = 0, metadataBytes = 0
  const headers = { 'X-Swarm-Token': o.token, Origin: o.apiURL, Referer: `${o.apiURL}/app`, 'Sec-Fetch-Site': 'same-origin', Accept: 'application/json' }
  async function api(method, route, body, { bytes = false, cleanup = false } = {}) {
    check(cleanup || ++requests <= 1800, 'request_budget_exceeded')
    const signal = cleanup ? AbortSignal.timeout(5000) : AbortSignal.any([controller.signal, AbortSignal.timeout(Math.min(15000, Math.max(1, deadline - Date.now())))])
    const response = await (deps.fetch ?? fetch)(o.apiURL + route, { method, headers: { ...headers, ...(body ? { 'Content-Type': 'application/json' } : {}) },
      ...(body ? { body: JSON.stringify(body) } : {}), redirect: 'error', signal })
    check(response.ok, `http_${response.status}`) // Never emit provider/auth bodies.
    const data = await boundedBytes(response, bytes ? 64 * 1024 * 1024 : 2 * 1024 * 1024)
    if (!bytes && !cleanup) { metadataBytes += data.length; check(metadataBytes <= 64 * 1024 * 1024, 'aggregate_metadata_budget_exceeded') }
    return bytes ? { data, type: response.headers.get('content-type')?.split(';')[0] } : JSON.parse(data.toString('utf8'))
  }
  // Retain at most the 12 started/completed pairs from canonical hydration so
  // progress chatter cannot evict capability evidence from the bounded tail.
  const observedTools = new Map()
  const hydrate = async () => {
    const snapshot = await api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [sessionID],
      history: { mode: 'tail', max_messages_per_session: 100, max_events_per_session: 200, manifest_policy: 'manifest' },
      resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true }, include_active: true })
    const events = snapshot.events_by_session?.[sessionID] || []
    for (const event of events) {
      if (!['session.tool.started', 'session.tool.completed', 'session.tool.failed', 'session.tool.cancelled', 'session.tool.canceled'].includes(event.event_type)) continue
      const payload = decode(event.payload)
      if (payload?.run_id !== runID) continue
      const key = event.event_type + ':' + payload.call_id
      if (observedTools.has(key)) check(JSON.stringify(observedTools.get(key)) === JSON.stringify(event), 'conflicting_tool_event')
      else observedTools.set(key, event)
      check(observedTools.size <= 36, 'tool_event_budget_exceeded')
    }
    snapshot.events_by_session ??= {}
    snapshot.events_by_session[sessionID] = [...observedTools.values(), ...events.filter(e => !e.event_type?.startsWith('session.tool.'))]
    return snapshot
  }
  try {
    const settings = (await api('GET', '/v1/agent-model-settings')).agent_model_settings
    check(settings?.swarm?.action?.model && settings?.swarm?.plan?.model && settings?.system_agents?.router?.model, 'configured_models_unavailable')
    const expectedModel = o.scenario === 'session-api' ? settings.swarm.action : settings.swarm.plan
    check(expectedModel.provider && expectedModel.thinking, 'configured_model_identity_unavailable')
    mark('configured_models')
    const topology = await api('GET', '/v1/swarm/topology')
    const binding = topology.workspace_bindings?.find(b => b.state === 'bound' && b.source_workspace_path === o.workspacePath)
    const runtime = topology.runtimes?.find(s => s.relationship === 'self')
    check(binding?.workspace_binding_id && runtime?.swarm_id, 'explicit_workspace_binding_unavailable')
    swarmID = runtime.swarm_id; mark('workspace_binding')
    const authority = { workspace_path: o.workspacePath, workspace_binding_id: binding.workspace_binding_id,
      swarm_id: swarmID, target_kind: 'host', target_relationship: 'self' }
    let projectID = ''
    if (o.scenario !== 'session-api') {
      const project = (await api('POST', '/v3/projects', { client_request_id: o.runID + ':project', name: 'PR qualification ' + o.runID,
        description: 'Disposable qualification project; never approve tasks.', workspaces: [{ path: o.workspacePath, role: 'primary_code' }] })).project
      check(id(project?.id), 'project_identity_unavailable'); projectID = project.id
      r.evidence.push({ project_id: projectID, retained: true })
    }
    const created = await api('POST', projectID ? `/v3/projects/${encodeURIComponent(projectID)}/sessions` : '/v3/sessions', {
      client_request_id: o.runID + ':session', ...(projectID ? {} : { ...authority, mode: 'auto', agent_name: 'swarm', model_profile: { use_account_default: true } }) })
    r.status = 'FAIL'
    sessionID = created.session_id || created.session?.id
    check(id(sessionID) && created.session?.id === sessionID && created.session.mode === 'auto', 'session_identity_mismatch')
    if (projectID) check(created.session.project_id === projectID || created.session.metadata?.project_id === projectID, 'project_session_mismatch')
    mark('session_identity'); r.evidence.push({ session_id: sessionID, retained: true })
    const marker = `PR-${o.runID}`
    const common = 'Do not change models, settings, permissions, files or unrelated projects. Do not retry generation or delegate. '
    const prompt = o.scenario === 'session-api' ? `Reply with exactly ${marker}. Do not use tools.`
      : o.scenario === 'orchestrator-chat' ? `${common}In this project propose exactly one Big Feature Swarm task titled ${marker} with explicit source ${o.workspacePath}. The task should add a short README explanation later. Use manage_projects propose_task, auto-approval off. Do not approve, deploy or execute it. Stop after creating the pending task.`
      : `${common}Generate exactly one ${o.scenario} of a calm abstract blue wave, title ${marker}, using the account-configured model. Use manage_artifact generate_${o.scenario}. ${o.scenario === 'video' ? 'One silent clip, shortest supported duration; no story or soundtrack.' : `First discover ${o.scenario}_capabilities and use its exact capability token and supported settings.${o.scenario === 'audio' ? ' Use the shortest supported duration.' : ''}`} Return the exact ready reference. Missing capability must be reported, never replaced.`
    const sent = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, { client_request_id: o.runID + ':message', role: 'user', content: prompt })
    runID = sent.run_intent?.run_id || sent.run_id
    check(id(runID), 'run_not_admitted'); r.status = 'FAIL'; mark('run_admitted')
    r.evidence.push({ session_id: sessionID, run_id: runID })
    const settled = await waitStage({ sample: hydrate, stageMs: Math.max(1, deadline - Date.now()), stallMs: Math.min(90000, o.timeoutMs),
      done: snapshot => {
        const intents = snapshot.run_intents_by_session?.[sessionID] || []
        const intent = intents.find(i => i.run_id === runID)
        const events = snapshot.events_by_session?.[sessionID] || []
        assertSafeToolRouting(events, runID, o.scenario)
        toolEvidence(events, runID)
        check(!intent || !['failed', 'cancelled', 'expired', 'interrupted'].includes(intent.status), 'run_failed')
        return intent?.status === 'completed'
      }, heartbeat: () => process.stderr.write('orchestrator-pr: awaiting durable run completion\n') })
    completed = true; mark('run_completed')
    function verifyHistory(snapshot) {
      const messages = snapshot.messages_by_session?.[sessionID] || []
      check(messages.some(m => m.role === 'user' && m.content === prompt), 'durable_user_missing')
      const assistant = messages.find(m => m.role === 'assistant' && m.metadata?.run_id === runID && m.metadata?.provider === expectedModel.provider && m.metadata?.model === expectedModel.model && m.content?.trim())
      check(assistant, 'durable_provider_response_missing')
      if (o.scenario === 'session-api') check(assistant.content.trim() === marker, 'provider_ack_mismatch')
      return messages
    }
    verifyHistory(settled); mark('provider_response')
    const rehydrated = await hydrate()
    verifyHistory(rehydrated); mark('history_rehydrate')
    assertSafeToolRouting(rehydrated.events_by_session?.[sessionID] || [], runID, o.scenario)
    const tools = toolEvidence(rehydrated.events_by_session?.[sessionID] || [], runID)
    if (o.scenario === 'orchestrator-chat') {
      const proposals = tools.filter(t => t.name === 'manage_projects' && ['propose_task', 'create_task'].includes(t.args.action))
      check(proposals.length === 1 && proposals[0].args.project_id === projectID && proposals[0].output.task?.title === marker, 'ai_routed_task_missing')
      mark('ai_task_routing')
      const proposed = proposals[0].output.task
      check(id(proposed.id), 'task_identity_unavailable')
      const task = (await api('GET', `/v3/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(proposed.id)}`)).task
      check(task?.id === proposed.id && task.project_id === projectID && task.status === 'pending_approval' && task.agent === 'swarm', 'task_not_pending')
      mark('durable_pending_task')
      check(task.source_workspace?.path === o.workspacePath, 'task_source_mismatch'); mark('task_source')
      check(id(task.session_id), 'task_session_missing')
      const snapshot = await api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [task.session_id], history: { mode: 'none' }, resources: { run_intents: true }, include_active: true })
      check(Array.isArray(snapshot.run_intents_by_session?.[task.session_id]) && snapshot.run_intents_by_session[task.session_id].length === 0, 'unapproved_task_executed')
      mark('zero_task_intents'); r.evidence.push({ project_id: projectID, task_id: task.id, session_id: task.session_id, retained: true })
    } else if (o.scenario !== 'session-api') {
      const evidence = mediaEvidence(tools, o.scenario, sessionID)
      mark('media_tool_routing'); mark('ready_exact_reference')
      if (o.scenario !== 'video') mark('capability_discovery')
      const ref = evidence.artifact_reference
      const data = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/artifacts/${encodeURIComponent(ref.variant_id)}`, undefined, { bytes: true })
      check(data.type === evidence.media_type && data.data.length === evidence.size
        && createHash('sha256').update(data.data).digest('hex') === evidence.digest_sha256, 'media_bytes_mismatch')
      mark('media_bytes'); r.evidence.push(evidence)
    }
  } catch (error) { primaryFailure = error; throw error } finally {
    clearTimeout(timer)
    process.removeListener('SIGINT', interrupt); process.removeListener('SIGTERM', interrupt)
    // Retain owned records/artifacts. Cancel only the exact admitted live run on
    // failure; the parent disposes its managed deployment, never DELETE a task.
    if (sessionID && runID && !completed) {
      try {
        await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/run/stop`, { run_id: runID, target_swarm_id: swarmID, reason: 'PR qualification deadline or failure' }, { cleanup: true })
        r.evidence.push({ session_id: sessionID, run_id: runID, stop_requested: true, retained: true })
      } catch (error) {
        r.failures.push('owned_run_stop_failed')
        if (!primaryFailure) throw error
      }
    }
  }
}

export async function main(argv = process.argv.slice(2)) {
  const o = parseOptions(argv)
  const fd = openSync(o.output, 'wx', 0o600) // Reject symlinks/existing receipts before paid work.
  const r = createReceipt(o)
  try {
    await runScenario(o, r)
    r.assertion_count = r.assertions.filter(a => a.passed).length
    check(r.assertion_count === r.assertions.length, 'required_assertions_missing')
    r.status = 'PASS'; r.native_exit = 0
    validateReceipt(r, o, 0)
  } catch (e) {
    if (r.status === 'PASS') r.status = 'FAIL'
    r.failures.push(/^[a-z0-9_]{1,80}$/.test(e.message) ? e.message : 'operation_failed')
    r.native_exit = 2
  } finally {
    r.assertion_count = r.assertions.filter(a => a.passed).length
    try { writeFileSync(fd, JSON.stringify(r, null, 2) + '\n'); } finally { closeSync(fd) }
  }
  process.stderr.write(`orchestrator-pr: ${r.status} assertions=${r.assertion_count}/${r.assertions.length}\n`)
  return r.native_exit
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then(code => { process.exitCode = code }).catch(() => { console.error('orchestrator-pr: invalid preflight or receipt write failure'); process.exitCode = 2 })
}
