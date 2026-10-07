#!/usr/bin/env node
// Opt-in live qualification, never a benchmark or a hermetic-tier member.
// Observes exact canonical session/hydration evidence with finite deadlines;
// no account settings, auth bootstrap or source writes.
import { createHash } from 'node:crypto'
import { openSync, closeSync, writeFileSync, realpathSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'
import { setTimeout as sleep } from 'node:timers/promises'

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
  if (scenario === 'orchestrator-live-e2e') return ['candidate_revision', 'configured_models', 'in_chat_proposals_no_self_approval', 'structured_plan_handoff_completion', 'multistage_task_program_dependency_completion', 'owned_cleanup']
  check(SCENARIOS.includes(scenario), 'unknown_scenario')
  return ['candidate_revision', 'configured_models', 'workspace_binding', 'session_identity', 'run_admitted',
    'provider_response', 'history_rehydrate', 'run_completed', ...(scenario === 'orchestrator-chat'
      ? ['ai_task_routing', 'durable_pending_task', 'task_source', 'zero_task_intents']
      : scenario !== 'session-api' ? ['media_tool_routing', 'ready_exact_reference', 'media_bytes',
        ...(scenario !== 'video' ? ['capability_discovery'] : [])] : [])]
}

// Session creation derives identity from account + request key, not its route.
// Bound long build IDs without changing receipt identity or payload retry checks.
export function mutationRequestID(o, operation) {
  check(id(o.runID) && SCENARIOS.includes(o.scenario) && ['project', 'session', 'message'].includes(operation), 'invalid_mutation_identity')
  const digest = createHash('sha256').update(JSON.stringify([o.runID, o.scenario, operation])).digest('hex')
  return `orchestrator-pr:${o.scenario}:${operation}:${digest}`
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
      check(scenario === 'orchestrator-chat' && ['help', 'list', 'get', 'list_sources', 'inspect_source', 'list_tasks', 'get_task', 'propose_task', 'create_task'].includes(args.action), 'unexpected_project_mutation')
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

const RUN_STATES = Object.freeze(['pending_executor', 'running', 'waiting_tasks', 'completed', 'failed', 'cancelled', 'expired', 'interrupted', 'dispatch_blocked'])

// Canonical sync run state is authoritative; the intent tail may omit old runs.
// Only semantic exact-run evidence counts as progress, never snapshot rev/cursors,
// usage, timestamps, provider bodies or unrelated sessions/runs.
export function observeRun(snapshot, sessionID, runID) {
  const current = snapshot.current_run_state_by_session?.[sessionID]
  const intents = (snapshot.run_intents_by_session?.[sessionID] || []).filter(i => i.run_id === runID)
  check(intents.length <= 1, 'run_evidence_conflict')
  if (current) check(current.run_id === runID && current.session_id === sessionID, 'foreign_run_evidence')
  const intent = intents[0]
  if (intent) check(intent.session_id === sessionID, 'foreign_run_evidence')
  const state = current || intent
  check(state, 'missing_run_evidence')
  check(RUN_STATES.includes(state.status), 'unknown_run_state')
  if (current && intent) check(current.status === intent.status, 'run_evidence_conflict')
  const events = (snapshot.events_by_session?.[sessionID] || []).filter(e => {
    const payload = decode(e.payload)
    return payload?.run_id === runID && (!e.session_id || e.session_id === sessionID)
      && (e.event_type?.startsWith('session.tool.') || ['session.run.started', 'session.run.completed', 'session.run.failed', 'permission.requested', 'permission.updated'].includes(e.event_type))
  })
  const messages = (snapshot.messages_by_session?.[sessionID] || []).filter(m => m.metadata?.run_id === runID && (!m.session_id || m.session_id === sessionID))
  const permissions = snapshot.session_views_by_id?.[sessionID]?.pending_permissions
  const pending = (permissions || []).some(p => p.session_id === sessionID && p.run_id === runID && p.status === 'pending')
  const permissionEvent = events.some(e => {
    const p = decode(e.payload)?.permission
    return p?.session_id === sessionID && p.run_id === runID && p.status === 'pending'
      && !events.some(other => other.event_type === 'permission.updated' && decode(other.payload)?.permission?.id === p.id && decode(other.payload)?.permission?.status !== 'pending')
  })
  const failure = pending || (!Array.isArray(permissions) && permissionEvent) ? 'run_permission_pending'
    : ['waiting_tasks', 'dispatch_blocked', 'failed', 'cancelled', 'expired', 'interrupted'].includes(state.status) ? `run_${state.status}` : ''
  return { status: state.status, failure, messages: messages.length, tools: events.filter(e => e.event_type === 'session.tool.completed').length,
    messageSeq: Math.max(0, ...messages.map(m => Number.isSafeInteger(m.global_seq) ? m.global_seq : 0)),
    eventSeq: Math.max(0, ...events.map(e => Number.isSafeInteger(e.seq) ? e.seq : 0)) }
}

// PermissionRecord.ToolCallArguments is the executor's original call; do not
// authorize from a display summary, a reservation or assistant prose. Unknown
// fields are rejected rather than trusting backend defaults for launch behavior.
const object = value => value && typeof value === 'object' && !Array.isArray(value)
const canonical = value => JSON.stringify(object(value)
  ? Object.fromEntries(Object.keys(value).sort().map(key => [key, JSON.parse(canonical(value[key]))]))
  : Array.isArray(value) ? value.map(item => JSON.parse(canonical(item))) : value)
function permissionArgs(record) {
  try {
    const args = decode(record.tool_call_arguments || record.tool_arguments)
    const summary = record.tool_arguments ? decode(record.tool_arguments) : {}
    check(object(args) && object(summary) && !Object.hasOwn(summary, 'approved_arguments'), 'permission_arguments_invalid')
    return args
  } catch { throw new Error('permission_arguments_invalid') }
}
function fixtureCall(args, fixture) {
  check(args.project_id === fixture.projectID, 'permission_project_mismatch')
  const discovery = ['list_sources', 'inspect_source'].includes(args.action)
  const fields = discovery ? ['action', 'project_id', 'workspace_path', 'workspace_id', 'workspace_generation']
    : ['action', 'project_id', 'title', 'prompt', 'description', 'agent', 'feature_size', 'workspace_path', 'workspace_id', 'workspace_generation', 'auto_approve', 'client_request_id']
  check(Object.keys(args).every(key => fields.includes(key)), 'permission_fields_rejected')
  check(discovery || args.action === 'propose_task', 'permission_action_rejected')
  if (args.workspace_path !== undefined) check(args.workspace_path === fixture.workspacePath, 'permission_source_mismatch')
  if (args.workspace_id !== undefined) check(id(fixture.workspaceID) && args.workspace_id === fixture.workspaceID, 'permission_source_identity_mismatch')
  if (args.workspace_generation !== undefined) check(Number.isSafeInteger(fixture.workspaceGeneration) && fixture.workspaceGeneration > 0
    && args.workspace_generation === fixture.workspaceGeneration, 'permission_source_identity_mismatch')
  if (args.action === 'inspect_source' || !discovery) check(args.workspace_path === fixture.workspacePath, 'permission_source_missing')
  if (!discovery) {
    check(args.title === fixture.marker, 'permission_title_mismatch')
    check(args.agent === 'swarm' && args.feature_size === 'big' && args.auto_approve === false, 'permission_pending_contract_rejected')
    for (const field of ['prompt', 'description']) if (args[field] !== undefined) check(typeof args[field] === 'string' && args[field].trim() && args[field].length <= 4000, 'permission_arguments_invalid')
    if (args.client_request_id !== undefined) check(id(args.client_request_id), 'permission_arguments_invalid')
  }
  return !discovery
}

// One bounded, exact-call consent surface for this harness only. No saved rules,
// argument rewrites, bulk resolution or bypass. HTTP races fail closed; never
// retry a grant. A duplicate pending snapshot gets one rehydration, not a loop.
function fixtureConsent(api, fixture) {
  const seen = new Map(), calls = new Set()
  let proposalCall = ''
  return async (snapshot, observation) => {
    const pending = snapshot.session_views_by_id?.[fixture.sessionID]?.pending_permissions
    if (!Array.isArray(pending)) {
      check(observation.failure !== 'run_permission_pending', 'permission_evidence_missing')
      return false
    }
    check(pending.length <= 12, 'permission_budget_exceeded')
    if (!pending.length) return false
    check(fixture.scenario === 'orchestrator-chat', 'run_permission_pending')
    check(observation.status === 'running', 'permission_run_not_running')
    // Validate the entire batch before making any mutation.
    const batch = pending.map(record => {
      check(object(record) && record.session_id === fixture.sessionID && record.run_id === fixture.runID && record.status === 'pending'
        && /^[a-zA-Z0-9_-]{1,200}$/.test(record.id || '') && id(record.call_id), 'permission_identity_rejected')
      check(record.tool_name === 'manage_projects' && !record.approved_arguments, 'permission_tool_rejected')
      const args = permissionArgs(record), proposal = fixtureCall(args, fixture)
      const fingerprint = canonical([record.session_id, record.run_id, record.call_id, record.tool_name, args])
      const prior = seen.get(record.id)
      if (prior) check(prior.fingerprint === fingerprint && prior.duplicates === 0, 'permission_stale_or_changed')
      else check(!calls.has(record.call_id), 'permission_duplicate_call')
      return { record, fingerprint, prior, proposal }
    })
    check(new Set(batch.map(p => p.record.id)).size === batch.length && new Set(batch.map(p => p.record.call_id)).size === batch.length, 'permission_duplicate_call')
    const proposals = batch.filter(p => p.proposal)
    check(proposals.length <= 1 && (!proposals.length || !proposalCall || proposalCall === proposals[0].record.call_id), 'permission_multiple_proposals')
    check(seen.size + batch.filter(p => !p.prior).length <= 12, 'permission_budget_exceeded')
    for (const { record, fingerprint, prior, proposal } of batch) {
      if (prior) { prior.duplicates++; continue }
      seen.set(record.id, { fingerprint, duplicates: 0 }); calls.add(record.call_id)
      if (proposal) proposalCall = record.call_id
      let result
      try {
        result = await api('POST', `/v3/sessions/${encodeURIComponent(fixture.sessionID)}/permissions/${encodeURIComponent(record.id)}/resolve`,
          { action: 'allow_once', reason: 'Exact owned pending-only PR fixture call' })
      } catch { throw new Error('permission_resolution_failed') }
      const resolved = result.permission
      check(result.ok === true && result.session_id === fixture.sessionID && result.saved_rule === false
        && resolved?.id === record.id && resolved.status === 'approved' && resolved.decision === 'allow_once'
        && !resolved.approved_arguments && canonical([resolved.session_id, resolved.run_id, resolved.call_id, resolved.tool_name, permissionArgs(resolved)]) === fingerprint,
      'permission_resolution_mismatch')
    }
    return true
  }
}

export async function waitForRun({ sample, sessionID, runID, scenario, deadline, stallMs, now = Date.now, pause = sleep, onObservation = () => {}, heartbeat = () => {}, consent }) {
  let changed = now(), prior, beat = now(), messageSeq = 0, eventSeq = 0
  for (;;) {
    check(now() < deadline, 'stage_deadline_work_retained')
    const snapshot = await sample()
    const observation = observeRun(snapshot, sessionID, runID)
    onObservation(observation)
    check(now() < deadline, 'stage_deadline_work_retained')
    const events = snapshot.events_by_session?.[sessionID] || []
    assertSafeToolRouting(events, runID, scenario)
    toolEvidence(events, runID)
    const resolving = consent ? await consent(snapshot, observation) : false
    check(now() < deadline, 'stage_deadline_work_retained')
    check(!observation.failure || (resolving && observation.failure === 'run_permission_pending'), observation.failure)
    if (!resolving && observation.status === 'completed') return snapshot
    messageSeq = Math.max(messageSeq, observation.messageSeq)
    eventSeq = Math.max(eventSeq, observation.eventSeq)
    const fingerprint = JSON.stringify([observation.status, messageSeq, eventSeq])
    if (fingerprint !== prior) { prior = fingerprint; changed = now() }
    check(now() - changed < stallMs, 'no_progress_work_retained')
    if (now() >= beat) { heartbeat(); beat = now() + 10000 }
    await pause(Math.min(500, deadline - now()))
  }
}

export async function runScenario(o, r, deps = {}) {
  const mark = name => { const a = r.assertions.find(a => a.name === name); check(a, 'unknown_assertion'); a.passed = true }
  ;(deps.verifyCandidate ?? verifyCandidate)(o.candidate); mark('candidate_revision')
  const now = deps.now ?? Date.now
  const deadline = now() + o.timeoutMs
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), o.timeoutMs)
  const interrupt = () => controller.abort()
  process.once('SIGINT', interrupt); process.once('SIGTERM', interrupt)
  let primaryFailure, lastObservation
  let sessionID = '', runID = '', swarmID = '', completed = false, requests = 0, metadataBytes = 0
  const headers = { 'X-Swarm-Token': o.token, Origin: o.apiURL, Referer: `${o.apiURL}/app`, 'Sec-Fetch-Site': 'same-origin', Accept: 'application/json' }
  async function api(method, route, body, { bytes = false, cleanup = false } = {}) {
    check(cleanup || now() < deadline, 'stage_deadline_work_retained')
    check(cleanup || ++requests <= 1800, 'request_budget_exceeded')
    const signal = cleanup ? AbortSignal.timeout(5000) : AbortSignal.any([controller.signal, AbortSignal.timeout(Math.min(15000, Math.max(1, deadline - now())))])
    const response = await (deps.fetch ?? fetch)(o.apiURL + route, { method, headers: { ...headers, ...(body ? { 'Content-Type': 'application/json' } : {}) },
      ...(body ? { body: JSON.stringify(body) } : {}), redirect: 'error', signal })
    check(response.ok, `http_${response.status}`) // Never emit provider/auth bodies.
    const data = await boundedBytes(response, bytes ? 64 * 1024 * 1024 : 2 * 1024 * 1024)
    check(cleanup || now() < deadline, 'stage_deadline_work_retained')
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
      const project = (await api('POST', '/v3/projects', { client_request_id: mutationRequestID(o, 'project'), name: 'PR qualification ' + o.runID,
        description: 'Disposable qualification project; never approve tasks.', workspaces: [{ path: o.workspacePath, role: 'primary_code' }] })).project
      check(id(project?.id), 'project_identity_unavailable'); projectID = project.id
      r.evidence.push({ project_id: projectID, retained: true })
    }
    const created = await api('POST', projectID ? `/v3/projects/${encodeURIComponent(projectID)}/sessions` : '/v3/sessions', {
      client_request_id: mutationRequestID(o, 'session'), ...(projectID ? {} : { ...authority, mode: 'auto', agent_name: 'swarm', model_profile: { use_account_default: true } }) })
    r.status = 'FAIL'
    sessionID = created.session_id || created.session?.id
    check(id(sessionID) && created.session?.id === sessionID && created.session.mode === 'auto', 'session_identity_mismatch')
    if (projectID) check(created.session.project_id === projectID || created.session.metadata?.project_id === projectID, 'project_session_mismatch')
    mark('session_identity'); r.evidence.push({ session_id: sessionID, retained: true })
    const marker = `PR-${o.runID}`
    const common = 'Do not change models, settings, permissions, files or unrelated projects. Do not retry generation or delegate. '
    const prompt = o.scenario === 'session-api' ? `Reply with exactly ${marker}. Do not use tools.`
      : o.scenario === 'orchestrator-chat' ? `${common}In this project propose exactly one Big Feature Swarm task titled ${marker} with explicit source ${o.workspacePath}. The task should add a short README explanation later. Use manage_projects propose_task with explicit agent="swarm", feature_size="big", workspace_path for that exact source and auto_approve=false. Only read-only manage_projects list_sources/inspect_source discovery for this project/source and this one pending proposal are authorized by the fixture. Do not approve, deploy or execute it. Stop after creating the pending task.`
      : `${common}Generate exactly one ${o.scenario} of a calm abstract blue wave, title ${marker}, using the account-configured model. Use manage_artifact generate_${o.scenario}. ${o.scenario === 'video' ? 'One silent clip, shortest supported duration; no story or soundtrack.' : `First discover ${o.scenario}_capabilities and use its exact capability token and supported settings.${o.scenario === 'audio' ? ' Use the shortest supported duration.' : ''}`} Return the exact ready reference. Missing capability must be reported, never replaced.`
    const sent = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, { client_request_id: mutationRequestID(o, 'message'), role: 'user', content: prompt })
    const admittedRunID = sent.run_intent?.run_id || sent.run_id
    check(id(admittedRunID) && (!sent.run_intent?.session_id || sent.run_intent.session_id === sessionID), 'run_not_admitted')
    runID = admittedRunID; r.status = 'FAIL'; mark('run_admitted')
    r.evidence.push({ session_id: sessionID, run_id: runID })
    const consent = fixtureConsent(api, { scenario: o.scenario, sessionID, runID, projectID, marker, workspacePath: o.workspacePath,
      workspaceID: binding.source_workspace_id, workspaceGeneration: binding.source_workspace_generation })
    const settled = await waitForRun({ sample: hydrate, sessionID, runID, scenario: o.scenario, deadline,
      stallMs: Math.min(90000, o.timeoutMs), now, pause: deps.pause ?? sleep, consent,
      onObservation: observation => { lastObservation = observation },
      heartbeat: () => process.stderr.write('orchestrator-pr: awaiting durable run completion\n') })
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
    const finalObservation = observeRun(rehydrated, sessionID, runID)
    lastObservation = finalObservation
    check(!await consent(rehydrated, finalObservation), 'permission_after_completion')
    check(finalObservation.status === 'completed' && !finalObservation.failure, finalObservation.failure || 'run_completion_not_retained')
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
  } catch (error) {
    primaryFailure = now() >= deadline ? new Error('stage_deadline_work_retained')
      : controller.signal.aborted ? new Error('run_observation_interrupted') : error
    // Capture BEFORE stop can change durable state. Only fixed typed codes survive
    // the operations sanitizer; never interpolate status/reason/provider content.
    if (runID) {
      r.failures.push(lastObservation ? `last_run_${lastObservation.status}` : 'last_run_unobserved')
      if (lastObservation?.messages) r.failures.push('last_run_messages_present')
      if (lastObservation?.tools) r.failures.push('last_run_tools_completed')
    }
    throw primaryFailure
  } finally {
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
