#!/usr/bin/env node
import crypto from 'node:crypto'

const argv = process.argv.slice(2)
const option = (name, fallback = '') => {
  const index = argv.indexOf(name)
  return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback
}

const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || '')).trim().toLowerCase()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '900000'))
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
let token = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

if (!/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is required')
if (!Number.isFinite(timeoutMs) || timeoutMs < 30000) throw new Error('--timeout-ms must be at least 30000')

const stageTimeoutMs = Math.min(timeoutMs, 9 * 60 * 1000)
const heartbeatEveryMs = 15000
const terminalEvidenceGraceMs = 5000
const testID = `workspace-map-checkpoints-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const markerA = `WMAP_A_${crypto.randomBytes(8).toString('hex').toUpperCase()}`
const markerB = `WMAP_B_${crypto.randomBytes(8).toString('hex').toUpperCase()}`
const requiredGates = [
  'provider_runnable', 'models_configured', 'initial_map_captured', 'explicit_update_applied',
  'same_execution_later_checkpoint', 'fresh_provider_run_visibility', 'self_update_applied',
  'revision_and_digest_changed', 'durable_session_evidence', 'models_restored',
]
const result = {
  result: 'NOT_DONE', test: 'workspace-map-checkpoints', test_id: testID, provider,
  markers: { initial: markerA, self_update: markerB }, ids: {}, revisions: {}, gates: {}, failures: [],
}
let originalSwarmSettings = null
let settingsChanged = false

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[workspace-map-checkpoints] ${message}\n`)
const assert = (value, message) => { if (!value) throw new Error(message) }

async function request(method, route, body, label = route, allowError = false) {
  const headers = { Accept: 'application/json', Origin: new URL(apiURL).origin, Referer: `${apiURL}/app`, 'Sec-Fetch-Site': 'same-origin' }
  if (token) {
    headers['X-Swarm-Token'] = token
    headers.Cookie = `swarm_desktop_session=${token}`
  }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(new Error(`${label} timed out`)), Math.min(stageTimeoutMs, 120000))
  try {
    const response = await fetch(`${apiURL}${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal })
    const text = await response.text()
    let decoded = null
    try { decoded = text ? JSON.parse(text) : null } catch { decoded = { raw: text } }
    if (!allowError && !response.ok) throw new Error(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return { ok: response.ok, status: response.status, body: decoded, text }
  } finally { clearTimeout(timer) }
}

function recommendationFor(record, roles) {
  return (Array.isArray(record?.recommendations) ? record.recommendations : [])
    .find((item) => roles.includes(String(item?.role || '').trim().toLowerCase())) || null
}

function recommendedAssignment(records, roles, label) {
  for (const record of records) {
    const recommendation = recommendationFor(record, roles)
    const model = String(record?.model || '').trim()
    const thinking = String(recommendation?.thinking || record?.default_thinking || '').trim().toLowerCase()
    if (!recommendation || !model || !thinking) continue
    const serving = String(recommendation?.serving || '').trim().toLowerCase()
    return { provider, model, thinking, ...(serving === 'fast' || serving === 'priority' ? { service_tier: 'priority' } : {}) }
  }
  throw new Error(`model catalog has no complete ${label} recommendation for provider ${provider}`)
}

function terminal(status) {
  return ['completed', 'failed', 'cancelled', 'expired', 'interrupted'].includes(String(status || '').toLowerCase())
}

function decodeObject(value) {
  if (value && typeof value === 'object') return value
  try { return JSON.parse(String(value || '{}')) } catch { return {} }
}

function messagesFor(snapshot, sessionID) {
  return snapshot?.messages_by_session?.[sessionID] || snapshot?.resources?.messages_by_session?.[sessionID] || []
}

function durableText(snapshot, sessionID) {
  return messagesFor(snapshot, sessionID).filter((message) => message?.role === 'assistant' || message?.role === 'system').map((message) => String(message?.content || '')).join('\n')
}

function toolOutputs(snapshot, sessionID, action) {
  const outputs = []
  for (const message of messagesFor(snapshot, sessionID)) {
    if (message?.role !== 'tool') continue
    const envelope = decodeObject(message.content)
    const toolName = String(envelope.tool_name || envelope.tool || '')
    const args = decodeObject(envelope.arguments)
    const output = decodeObject(envelope.output || envelope.completed_output)
    if (toolName === 'manage_workspace' && (!action || String(args.action || args.op || output.action || '') === action)) outputs.push({ envelope, args, output })
  }
  return outputs
}

async function hydrate(sessionID) {
  return (await request('POST', '/v3/sync/hydrate', {
    surface: 'desktop', session_ids: [sessionID],
    history: { mode: 'full', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' },
    resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: true, plan_revisions: true, permission_summaries: true },
    include_active: true,
  }, `hydrate ${sessionID}`)).body || {}
}

function canonicalApprovedArguments(permission, toolName) {
  if (toolName === 'exit_plan_mode') return { execution_granularity: 'checkpointed', continuation_policy: 'automatic', continue_automatically: true }
  const payload = decodeObject(permission.tool_arguments || permission.arguments)
  return payload.approved_arguments || decodeObject(permission.approved_arguments)
}

async function approvePending(sessionID) {
  const pending = (await request('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=200`, undefined, `permissions ${sessionID}`)).body?.permissions || []
  const allowedTools = new Set(['exit_plan_mode', 'workspace_map_update'])
  for (const permission of pending) {
    const toolName = String(permission.tool_name || '')
    assert(allowedTools.has(toolName), `unexpected pending permission ${toolName || permission.id}`)
    const approvedArguments = canonicalApprovedArguments(permission, toolName)
    if (toolName === 'workspace_map_update') assert(approvedArguments?.permission_scope === 'workspace_map_update', 'Workspace Map permission omitted canonical approved arguments')
    await request('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/${encodeURIComponent(permission.id)}/resolve`, {
      action: 'allow_once', reason: `${testID}: explicit live-testbench approval`,
      ...(approvedArguments ? { approved_arguments: approvedArguments } : {}),
    }, `approve ${toolName || permission.id}`)
  }
  return pending.length
}

function terminalDiagnostics(snapshot, sessionID) {
  const intents = snapshot?.run_intents_by_session?.[sessionID] || []
  const tools = toolOutputs(snapshot, sessionID).map(({ args, envelope }) => `${String(args.action || envelope.tool_name || 'tool')}:${String(envelope.error || 'ok')}`)
  return `intents=${intents.map((item) => `${item.run_id}:${item.status}`).join(',') || 'none'} tools=${tools.join(',') || 'none'}`
}

async function runStep(sessionID, label, predicate, options = {}) {
  const deadline = Date.now() + Math.min(stageTimeoutMs, Number(options.timeoutMs || stageTimeoutMs))
  let latest = null
  let nextHeartbeat = 0
  let allTerminalSince = 0
  while (Date.now() < deadline) {
    await approvePending(sessionID)
    latest = await hydrate(sessionID)
    const intents = latest.run_intents_by_session?.[sessionID] || []
    const failures = intents.filter((intent) => ['failed', 'cancelled', 'expired', 'interrupted'].includes(String(intent?.status || '').toLowerCase()))
    if (failures.length > 0) throw new Error(`${label} failed: ${terminalDiagnostics(latest, sessionID)}`)
    const blocked = intents.find((intent) => String(intent?.status || '').toLowerCase() === 'blocked')
    if (blocked) throw new Error(`${label} blocked: ${terminalDiagnostics(latest, sessionID)}`)
    const toolFailure = toolOutputs(latest, sessionID).find(({ envelope }) => String(envelope.error || '').trim())
    if (toolFailure) throw new Error(`${label} tool failed: ${String(toolFailure.envelope.error)}; ${terminalDiagnostics(latest, sessionID)}`)
    if (predicate(latest)) return latest
    const allTerminal = intents.length > 0 && intents.every((intent) => terminal(intent.status))
    if (allTerminal) {
      if (!allTerminalSince) allTerminalSince = Date.now()
      if (Date.now() - allTerminalSince >= terminalEvidenceGraceMs) {
        throw new Error(`${label} reached terminal runs without required evidence: ${terminalDiagnostics(latest, sessionID)}`)
      }
    } else {
      allTerminalSince = 0
    }
    if (Date.now() >= nextHeartbeat) {
      log(`${label}: waiting; ${terminalDiagnostics(latest, sessionID)}`)
      nextHeartbeat = Date.now() + heartbeatEveryMs
    }
    await sleep(1000)
  }
  throw new Error(`${label} timed out under ${Math.min(stageTimeoutMs, Number(options.timeoutMs || stageTimeoutMs))}ms: ${terminalDiagnostics(latest, sessionID)}`)
}

async function createPlanSession(authority, title, input, suffix, planAssignment) {
  const clientRequestID = `${testID}:${suffix}`
  const created = (await request('POST', '/v3/sessions', {
    client_request_id: `${clientRequestID}:create`, title,
    workspace_path: authority.workspace_path, workspace_name: 'workspace-map-test',
    workspace_binding_id: authority.workspace_binding_id, swarm_id: authority.swarm_id,
    target_kind: authority.target_kind, target_relationship: authority.target_relationship,
    mode: 'plan', agent_name: 'swarm', preference: planAssignment,
    model_profile: { use_account_default: true },
    metadata: { source: 'checked-in-live-testbench', runner_test: 'workspace-map-checkpoints', runner_test_id: testID },
  }, `create ${suffix} plan session`)).body || {}
  const sessionID = String(created.session?.id || '').trim()
  assert(sessionID, `${suffix} plan session creation returned no session id`)
  await request('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${clientRequestID}:message`, role: 'user', content: input,
    metadata: { runner_test_id: testID },
  }, `start ${suffix} plan turn`)
  return sessionID
}

async function createAutoSession(authority, input, suffix) {
  const clientRequestID = `${testID}:${suffix}`
  const launched = (await request('POST', '/v3/sessions:routed', {
    ...authority, input, client_request_id: clientRequestID, idempotency_key: clientRequestID,
    agent_name: 'swarm', plan_mode_requested: false,
    metadata: { source: 'checked-in-live-testbench', runner_test: 'workspace-map-checkpoints', runner_test_id: testID },
  }, `create ${suffix} session`)).body || {}
  const sessionID = String(launched.session_id || launched.session?.id || '').trim()
  assert(sessionID, `${suffix} session creation returned no session id`)
  return sessionID
}

async function main() {
  if (!token) {
    token = String((await request('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')).body?.token || '').trim()
    assert(token, 'desktop authentication returned no token')
  }

  const providers = (await request('GET', '/v1/providers', undefined, 'list providers')).body?.providers || []
  const providerStatus = providers.find((item) => String(item?.id || '').trim().toLowerCase() === provider)
  assert(providerStatus && providerStatus.runnable !== false, `provider ${provider} is unavailable`)
  result.gates.provider_runnable = true

  const catalog = (await request('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'read model recommendations')).body?.records || []
  const actionAssignment = recommendedAssignment(catalog, ['auto', 'main'], 'auto')
  const planAssignment = recommendedAssignment(catalog, ['plan'], 'plan')
  originalSwarmSettings = (await request('GET', '/v1/agent-model-settings', undefined, 'read Swarm model settings')).body?.agent_model_settings?.swarm || null
  assert(originalSwarmSettings?.action?.model && originalSwarmSettings?.plan?.model, 'canonical Swarm model settings are missing')
  await request('PATCH', '/v1/agent-model-settings', { swarm: { action: actionAssignment, plan: planAssignment } }, 'configure live-testbench models')
  settingsChanged = true
  result.gates.models_configured = true

  const topology = (await request('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const runtime = (topology.runtimes || []).find((item) => item?.relationship === 'self') || (topology.runtimes || [])[0]
  const binding = workspacePathOverride
    ? (topology.workspace_bindings || []).find((item) => item?.source_workspace_path === workspacePathOverride || item?.destination_workspace_path === workspacePathOverride)
    : (topology.workspace_bindings || []).find((item) => item?.state === 'bound' && item?.workspace_binding_id) || (topology.workspace_bindings || [])[0]
  assert(runtime?.swarm_id && binding?.workspace_binding_id, 'topology has no runnable self runtime and bound workspace')
  const workspacePath = String(binding.source_workspace_path || binding.destination_workspace_path || '').trim()
  assert(workspacePath, 'selected workspace binding has no path')
  const authority = {
    workspace_path: workspacePath, host_workspace_path: workspacePath,
    runtime_workspace_path: String(binding.destination_workspace_path || workspacePath),
    workspace_binding_id: binding.workspace_binding_id, swarm_id: runtime.swarm_id,
    target_kind: 'host', target_relationship: 'self',
  }

  const sessionOnePrompt = [
    `Live Workspace Map evidence ${testID}.`,
    `This is an explicit user request to update the account Workspace Map so it contains the exact marker ${markerA}.`,
    'Submit exactly two ordered checkpoints with ids cp-map-update and cp-map-observe, each containing exactly one task, through exit_plan_mode with automatic checkpoint execution.',
    `Checkpoint cp-map-update must inspect the map with manage_workspace inspect_map, preserve all existing content, append a concise test entry containing exactly ${markerA}, and call manage_workspace update_map with the returned expected_revision and a user-readable intent.`,
    `Checkpoint cp-map-observe must run afterward in fresh checkpoint context, inspect the map again, and report exactly SAME_EXECUTION_MARKER ${markerA} REVISION <number> DIGEST <digest> in the terminal checkpoint evidence.`,
    'Use no other mutation tools. The runner will approve only the explicit map update and plan/checkpoint lifecycle requests.',
  ].join(' ')
  const sessionOne = await createPlanSession(authority, `${testID} same execution`, sessionOnePrompt, 'same-execution', planAssignment)
  result.ids.same_execution_session_id = sessionOne
  await runStep(sessionOne, 'step 1: explicit map update', (snapshot) => {
    const update = toolOutputs(snapshot, sessionOne, 'update_map').at(-1)
    return Number(update?.output?.workspace_map?.revision) > 0 && String(update?.output?.workspace_map?.content || '').includes(markerA)
  })
  const settledOne = await runStep(sessionOne, 'step 2: later checkpoint observation', (snapshot) => {
    const intents = snapshot.run_intents_by_session?.[sessionOne] || []
    const checkpointIntents = intents.filter((intent) => String(intent?.checkpoint_id || ''))
    const inspections = toolOutputs(snapshot, sessionOne, 'inspect_map')
    const observed = inspections.at(-1)?.output?.workspace_map || {}
    return checkpointIntents.length === 2 && checkpointIntents.every((intent) => terminal(intent.status)) && inspections.length >= 2 && String(observed.content || '').includes(markerA) && Number(observed.revision) > 0 && String(observed.digest || '').length === 64
  })
  const firstInspect = toolOutputs(settledOne, sessionOne, 'inspect_map')[0] || toolOutputs(settledOne, sessionOne, 'get_map')[0]
  const firstUpdate = toolOutputs(settledOne, sessionOne, 'update_map').at(-1)
  const before = firstInspect?.output?.workspace_map || {}
  const afterFirst = firstUpdate?.output?.workspace_map || {}
  assert(Number(before.revision) > 0 && String(before.digest || '').length === 64, 'initial inspect did not return revision/digest evidence')
  assert(Number(afterFirst.revision) === Number(before.revision) + 1 && String(afterFirst.digest || '').length === 64 && afterFirst.digest !== before.digest, 'explicit update did not advance revision and digest')
  assert(String(afterFirst.content || '').includes(markerA), 'explicit update output omitted marker A')
  result.revisions.initial = { revision: before.revision, digest: before.digest }
  result.revisions.after_explicit_update = { revision: afterFirst.revision, digest: afterFirst.digest }
  result.gates.initial_map_captured = true
  result.gates.explicit_update_applied = true
  result.gates.same_execution_later_checkpoint = true
  log(`same-execution evidence complete at revision ${afterFirst.revision}`)

  const sessionTwoPrompt = [
    `Fresh provider-run Workspace Map evidence ${testID}.`,
    `Without using tools first, report exactly FRESH_RUN_MARKER ${markerA} if that marker is present in the account Workspace Map injected into this fresh run.`,
    `Then this is an explicit user request to update that same map: inspect it with manage_workspace inspect_map, preserve all existing content, append a concise entry containing exactly ${markerB}, and update it with manage_workspace update_map using the returned expected_revision and a user-readable intent.`,
    `Finally inspect again and report exactly SELF_UPDATE_MARKER ${markerB} REVISION <number> DIGEST <digest>.`,
    'Use no other mutation tools.',
  ].join(' ')
  const sessionTwo = await createAutoSession(authority, sessionTwoPrompt, 'fresh-run')
  result.ids.fresh_run_session_id = sessionTwo
  await runStep(sessionTwo, 'step 3: fresh-run injected marker visibility', (snapshot) => durableText(snapshot, sessionTwo).includes(`FRESH_RUN_MARKER ${markerA}`))
  await runStep(sessionTwo, 'step 4: requested self-update', (snapshot) => {
    const update = toolOutputs(snapshot, sessionTwo, 'update_map').at(-1)
    return Number(update?.output?.workspace_map?.revision) > Number(afterFirst.revision) && String(update?.output?.workspace_map?.content || '').includes(markerB)
  })
  const settledTwo = await runStep(sessionTwo, 'step 5: self-update terminal confirmation', (snapshot) => {
    const intents = snapshot.run_intents_by_session?.[sessionTwo] || []
    const text = durableText(snapshot, sessionTwo)
    return intents.length >= 1 && intents.every((intent) => terminal(intent.status)) && text.includes(`SELF_UPDATE_MARKER ${markerB}`)
  })
  const secondUpdate = toolOutputs(settledTwo, sessionTwo, 'update_map').at(-1)
  const afterSecond = secondUpdate?.output?.workspace_map || {}
  assert(Number(afterSecond.revision) === Number(afterFirst.revision) + 1, `self-update revision=${afterSecond.revision}, want ${Number(afterFirst.revision) + 1}`)
  assert(String(afterSecond.digest || '').length === 64 && afterSecond.digest !== afterFirst.digest, 'self-update did not change digest')
  assert(String(afterSecond.content || '').includes(markerA) && String(afterSecond.content || '').includes(markerB), 'self-update did not preserve marker A and add marker B')
  result.revisions.after_self_update = { revision: afterSecond.revision, digest: afterSecond.digest }
  result.gates.fresh_provider_run_visibility = true
  result.gates.self_update_applied = true
  result.gates.revision_and_digest_changed = true
  const durableSessionEvidence = messagesFor(settledOne, sessionOne).length > 0 && messagesFor(settledTwo, sessionTwo).length > 0
  assert(durableSessionEvidence, 'durable session evidence was not hydrated for both test sessions')
  result.gates.durable_session_evidence = true
  result.result = 'PASS'
}

try {
  await main()
} catch (error) {
  result.failures.push(error?.stack || String(error))
  log(result.failures[result.failures.length - 1])
} finally {
  if (settingsChanged && originalSwarmSettings) {
    try {
      await request('PATCH', '/v1/agent-model-settings', { swarm: originalSwarmSettings }, 'restore Swarm model settings')
      result.gates.models_restored = true
    } catch (error) {
      result.failures.push(`restore Swarm model settings: ${error?.message || error}`)
    }
  }
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((gate) => result.gates[gate] !== true)
  if (result.failed_gates.length > 0) result.result = 'NOT_DONE'
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
