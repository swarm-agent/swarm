#!/usr/bin/env node
import crypto from 'node:crypto'

const argv = process.argv.slice(2)
const option = (name, fallback = '') => {
  const index = argv.indexOf(name)
  return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback
}

const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || '')).trim().toLowerCase()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is required and must contain only letters, numbers, dots, underscores, or dashes')
if (!Number.isFinite(timeoutMs) || timeoutMs < 30000 || timeoutMs > 600000) throw new Error('--timeout-ms must be between 30000 and 600000')

const testID = `runner-blocked-checkpoint-recovery-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const requiredGates = [
  'provider_runnable', 'model_selected', 'session_created', 'checkpoint_blocked',
  'recovery_parent_started', 'resolve_action_observed', 'fresh_ownership_observed',
  'checkpoint_completed', 'run_intents_settled', 'no_failures',
]
const result = {
  result: 'NOT_DONE',
  test: 'blocked-checkpoint-recovery',
  test_id: testID,
  started_at: new Date().toISOString(),
  api_url: apiURL,
  provider,
  model: {},
  ids: {},
  gates: {},
  diagnostics: {},
  failures: [],
}
let token = suppliedToken

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[blocked-checkpoint-recovery] ${message}\n`)
const fail = (message) => { result.failures.push(message); throw new Error(message) }
const assert = (condition, message) => { if (!condition) fail(message) }

async function api(method, route, body, label = route, allowError = false) {
  const headers = {
    Accept: 'application/json',
    Origin: new URL(apiURL).origin,
    Referer: `${apiURL}/app`,
    'Sec-Fetch-Site': 'same-origin',
  }
  if (token) {
    headers['X-Swarm-Token'] = token
    headers.Cookie = `swarm_desktop_session=${token}`
  }
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(new Error(`${label} timed out`)), Math.min(timeoutMs, 120000))
  try {
    const init = { method, headers, signal: controller.signal }
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json'
      init.body = JSON.stringify(body)
    }
    const response = await fetch(`${apiURL}${route}`, init)
    const text = await response.text()
    let decoded = null
    try { decoded = text ? JSON.parse(text) : null } catch { decoded = { raw: text } }
    if (!allowError && !response.ok) fail(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1000)}`)
    return { ok: response.ok, status: response.status, body: decoded, text }
  } finally {
    clearTimeout(timer)
  }
}

function objectPayload(value) {
  if (value && typeof value === 'object') return value
  try { return JSON.parse(String(value || '{}')) } catch { return {} }
}

function recommendationFor(record, roles) {
  const recommendations = Array.isArray(record?.recommendations) ? record.recommendations : []
  return recommendations.find((item) => roles.includes(String(item?.role || '').trim().toLowerCase())) || null
}

function actionAssignment(records) {
  for (const record of records) {
    const recommendation = recommendationFor(record, ['auto', 'main'])
    const model = String(record?.model || '').trim()
    const thinking = String(recommendation?.thinking || record?.default_thinking || '').trim().toLowerCase()
    if (!recommendation || !model || !thinking) continue
    const serving = String(recommendation?.serving || '').trim().toLowerCase()
    return {
      provider,
      model,
      thinking,
      ...(serving === 'fast' || serving === 'priority' ? { service_tier: 'priority' } : {}),
    }
  }
  fail(`model catalog has no complete auto recommendation for provider ${provider}`)
}

async function activePlan(sessionID) {
  const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/plans/active`, undefined, 'read active plan')
  return response.body?.active_plan || response.body?.plan || null
}

async function fetchAllEvents(sessionID) {
  const events = []
  let afterSeq = 0
  let replay = null
  for (let page = 0; page < 20; page += 1) {
    const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=${afterSeq}&limit=1000`, undefined, `read events page ${page}`)
    replay = response.body || {}
    const batch = replay.events || []
    events.push(...batch)
    const nextSeq = Number(replay.next_seq || replay.applied_seq || 0)
    if (batch.length < 1000 || nextSeq <= afterSeq) return { events, replay }
    afterSeq = nextSeq
  }
  fail('session event replay exceeded 20 pages')
}

function planManageCalls(events) {
  const calls = []
  for (const event of events) {
    if (String(event?.event_type || '') !== 'session.tool.completed') continue
    const payload = objectPayload(event?.payload)
    if (String(payload?.tool_name || payload?.name || '') !== 'plan_manage') continue
    const argumentsValue = objectPayload(payload?.arguments)
    const output = objectPayload(payload?.output ?? payload?.result ?? payload?.completed_output ?? '')
    calls.push({ arguments: argumentsValue, output, run_id: String(payload?.run_id || '') })
  }
  return calls
}

function attemptsFor(checkpoint) {
  return Array.isArray(checkpoint?.attempts) ? checkpoint.attempts : []
}

async function waitForBlocked(sessionID, initialRunID) {
  const deadline = Date.now() + timeoutMs
  let latest = null
  let lastBeat = 0
  while (Date.now() < deadline) {
    latest = await activePlan(sessionID)
    const checkpoint = latest?.document?.checkpoints?.[0]
    const attempts = attemptsFor(checkpoint)
    const blockedAttempt = [...attempts].reverse().find((attempt) => String(attempt?.status || attempt?.outcome || '') === 'blocked')
    if (checkpoint?.status === 'blocked' && blockedAttempt?.run_id) return { plan: latest, checkpoint, blockedAttempt }
    if (Date.now() - lastBeat >= 15000) {
      log(`waiting for deliberate block; initial_run=${initialRunID} checkpoint=${checkpoint?.status || 'not-created'} attempts=${attempts.length}`)
      lastBeat = Date.now()
    }
    await sleep(1500)
  }
  fail(`timed out waiting for blocked checkpoint; latest=${JSON.stringify(latest?.document?.checkpoints || [])}`)
}

async function waitForRecovered(sessionID, recoveryParentRunID, blockedAttempt) {
  const deadline = Date.now() + timeoutMs
  let latestPlan = null
  let latestReplay = null
  let lastBeat = 0
  while (Date.now() < deadline) {
    latestPlan = await activePlan(sessionID)
    latestReplay = await fetchAllEvents(sessionID)
    const checkpoint = latestPlan?.document?.checkpoints?.[0]
    const attempts = attemptsFor(checkpoint)
    const completedAttempt = [...attempts].reverse().find((attempt) => String(attempt?.status || attempt?.outcome || '') === 'completed')
    const calls = planManageCalls(latestReplay.events)
    const resolveCall = calls.find((call) => String(call?.arguments?.action || call?.output?.action || '') === 'resolve_blocked_checkpoint')
    if (checkpoint?.status === 'completed' && completedAttempt?.run_id && resolveCall) {
      return { plan: latestPlan, replay: latestReplay, checkpoint, completedAttempt, resolveCall }
    }
    const runIntents = latestReplay.replay?.run_intents || []
    const parentIntent = runIntents.find((intent) => String(intent?.run_id || '') === recoveryParentRunID)
    if (Date.now() - lastBeat >= 15000) {
      log(`waiting for recovery; parent=${parentIntent?.status || 'pending'} checkpoint=${checkpoint?.status || 'unknown'} attempts=${attempts.length} resolve=${Boolean(resolveCall)}`)
      lastBeat = Date.now()
    }
    const terminalFailure = runIntents.find((intent) => ['failed', 'cancelled', 'expired', 'interrupted'].includes(String(intent?.status || '')))
    if (terminalFailure) fail(`recovery produced failed run intent ${terminalFailure.run_id}:${terminalFailure.status}`)
    if (attempts.some((attempt) => String(attempt?.run_id || '') === String(blockedAttempt?.run_id || '') && String(attempt?.status || attempt?.outcome || '') !== 'blocked')) {
      fail('the blocked attempt was mutated instead of preserving immutable attempt history')
    }
    await sleep(1500)
  }
  fail(`timed out waiting for recovered checkpoint; latest=${JSON.stringify(latestPlan?.document?.checkpoints || [])}`)
}

async function main() {
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
    token = String(auth.body?.token || '').trim()
    assert(token, 'desktop authentication returned no token; set SWARM_RUNNER_TOKEN when desktop bootstrap is unavailable')
  }

  const providersResponse = await api('GET', '/v1/providers', undefined, 'list providers')
  const providerStatus = (providersResponse.body?.providers || []).find((item) => String(item?.id || '').trim().toLowerCase() === provider)
  assert(providerStatus, `provider ${provider} is not registered`)
  assert(providerStatus.runnable !== false, `provider ${provider} is not runnable: ${providerStatus.message || 'no status message'}`)
  result.gates.provider_runnable = true

  const catalogResponse = await api('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'read model recommendations')
  const assignment = actionAssignment(catalogResponse.body?.records || [])
  result.model = assignment
  result.gates.model_selected = true
  log(`using ${assignment.provider}/${assignment.model} thinking=${assignment.thinking}`)

  const topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body
  const runtime = (topology?.runtimes || []).find((item) => item?.relationship === 'self') || (topology?.runtimes || [])[0]
  const binding = workspacePathOverride
    ? (topology?.workspace_bindings || []).find((item) => item?.source_workspace_path === workspacePathOverride || item?.destination_workspace_path === workspacePathOverride)
    : (topology?.workspace_bindings || []).find((item) => item?.state === 'bound' && item?.workspace_binding_id) || (topology?.workspace_bindings || [])[0]
  assert(runtime?.swarm_id, 'topology has no runnable Swarm runtime')
  assert(binding?.workspace_binding_id, workspacePathOverride ? `no topology binding found for ${workspacePathOverride}` : 'topology has no workspace binding')
  const workspacePath = String(binding?.source_workspace_path || binding?.destination_workspace_path || '').trim()
  assert(workspacePath, 'selected workspace binding has no source or destination path')

  const createResponse = await api('POST', '/v3/sessions', {
    client_request_id: `${testID}:create`,
    title: `${testID} live blocked recovery`,
    workspace_path: workspacePath,
    workspace_name: String(binding?.source_workspace_name || 'runner-test'),
    workspace_binding_id: binding.workspace_binding_id,
    swarm_id: runtime.swarm_id,
    target_kind: 'host',
    target_relationship: 'self',
    mode: 'auto',
    agent_name: 'swarm',
    preference: assignment,
    model_profile: { use_account_default: true },
    metadata: { runner_test: 'blocked-checkpoint-recovery', runner_test_id: testID, provider },
  }, 'create auto session')
  const session = createResponse.body?.session || {}
  const sessionID = String(session?.id || '').trim()
  assert(sessionID, 'session creation returned no session id')
  result.ids.session_id = sessionID
  result.gates.session_created = true

  const initialPrompt = [
    `Live blocked recovery proof ${testID}.`,
    'Create and start exactly one bounded auto checkpoint with id cp-1 and title "Recover blocked checkpoint".',
    'Its objective is to record BLOCKED_RECOVERY_OK only after the user confirms that the named external gate RECOVERY_GATE is available.',
    'The acceptance criteria must require that confirmation and a terminal complete_checkpoint result of BLOCKED_RECOVERY_OK.',
    'RECOVERY_GATE is deliberately unavailable in this initial turn, and no tool can provide it.',
    'After start_session_checkpoint succeeds, immediately call mark_blocked for cp-1 with a concise handoff saying RECOVERY_GATE is the exact required external dependency.',
    'Do not inspect or modify workspace files, do not invent a workaround, and do not resolve the blocker in this turn.',
  ].join(' ')
  const initialMessage = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:initial-message`,
    role: 'user',
    content: initialPrompt,
    metadata: { runner_test_id: testID, runner_stage: 'block' },
  }, 'start deliberate blocked turn')
  const initialRunID = String(initialMessage.body?.run_intent?.run_id || initialMessage.body?.run_id || '')
  assert(initialRunID, 'initial message returned no run id')
  result.ids.initial_parent_run_id = initialRunID

  const blocked = await waitForBlocked(sessionID, initialRunID)
  result.ids.plan_id = blocked.plan?.id || blocked.plan?.document?.id || ''
  result.ids.checkpoint_id = blocked.checkpoint?.id || ''
  result.ids.blocked_attempt_id = blocked.blockedAttempt?.id || ''
  result.ids.blocked_run_id = blocked.blockedAttempt?.run_id || ''
  result.diagnostics.blocked_attempt = {
    id: blocked.blockedAttempt?.id || '',
    run_id: blocked.blockedAttempt?.run_id || '',
    status: blocked.blockedAttempt?.status || blocked.blockedAttempt?.outcome || '',
  }
  result.gates.checkpoint_blocked = true
  log(`blocked checkpoint=${result.ids.checkpoint_id} attempt=${result.ids.blocked_attempt_id} run=${result.ids.blocked_run_id}`)

  const recoveryPrompt = [
    `RECOVERY_GATE is now available for live proof ${testID}.`,
    'Continue the existing blocked checkpoint; do not create, restart, replace, or append a checkpoint.',
    'Follow the durable blocked-checkpoint recovery route first.',
    'The fresh resumed checkpoint owner must then complete cp-1 with result BLOCKED_RECOVERY_OK without inspecting or modifying workspace files.',
  ].join(' ')
  const recoveryMessage = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:recovery-message`,
    role: 'user',
    content: recoveryPrompt,
    metadata: { runner_test_id: testID, runner_stage: 'recover' },
  }, 'start recovery parent turn')
  const recoveryParentRunID = String(recoveryMessage.body?.run_intent?.run_id || recoveryMessage.body?.run_id || '')
  assert(recoveryParentRunID, 'recovery message returned no parent run id')
  result.ids.recovery_parent_run_id = recoveryParentRunID
  result.gates.recovery_parent_started = true

  const recovered = await waitForRecovered(sessionID, recoveryParentRunID, blocked.blockedAttempt)
  const completedAttempt = recovered.completedAttempt
  assert(String(completedAttempt?.run_id || '') !== String(blocked.blockedAttempt?.run_id || ''), 'recovery reused the blocked run instead of fresh checkpoint ownership')
  assert(String(completedAttempt?.id || '') !== String(blocked.blockedAttempt?.id || ''), 'recovery reused the blocked attempt instead of creating a fresh attempt')
  assert(String(completedAttempt?.run_id || '') !== recoveryParentRunID, 'the non-owning recovery parent run completed the checkpoint instead of the fresh checkpoint run')
  assert(String(recovered.checkpoint?.result || '').includes('BLOCKED_RECOVERY_OK') || JSON.stringify(recovered.checkpoint).includes('BLOCKED_RECOVERY_OK'), 'completed checkpoint lacks BLOCKED_RECOVERY_OK evidence')
  result.gates.resolve_action_observed = true
  result.ids.completed_attempt_id = completedAttempt?.id || ''
  result.ids.completed_run_id = completedAttempt?.run_id || ''
  result.diagnostics.completed_attempt = {
    id: completedAttempt?.id || '',
    run_id: completedAttempt?.run_id || '',
    status: completedAttempt?.status || completedAttempt?.outcome || '',
  }
  result.diagnostics.resolve_action = {
    action: recovered.resolveCall?.arguments?.action || recovered.resolveCall?.output?.action || '',
    checkpoint_id: recovered.resolveCall?.arguments?.checkpoint_id || recovered.resolveCall?.output?.checkpoint_id || '',
    run_id: recovered.resolveCall?.run_id || recovered.resolveCall?.output?.run_id || recovered.resolveCall?.output?.run_ownership?.run_id || '',
  }
  result.gates.fresh_ownership_observed = true
  result.gates.checkpoint_completed = true

  const runIntents = recovered.replay.replay?.run_intents || []
  const relevantRunIDs = new Set([initialRunID, recoveryParentRunID, String(blocked.blockedAttempt?.run_id || ''), String(completedAttempt?.run_id || '')])
  const relevantIntents = runIntents.filter((intent) => relevantRunIDs.has(String(intent?.run_id || '')))
  const unsettled = relevantIntents.filter((intent) => !['completed', 'blocked'].includes(String(intent?.status || '')))
  assert(unsettled.length === 0, `relevant run intents did not settle: ${unsettled.map((intent) => `${intent.run_id}:${intent.status}`).join(', ')}`)
  result.diagnostics.run_intents = relevantIntents.map((intent) => ({
    run_id: intent?.run_id || '', checkpoint_id: intent?.checkpoint_id || '', status: intent?.status || '',
  }))
  result.gates.run_intents_settled = true

  const failedEvents = recovered.replay.events.filter((event) => /failed|cancelled|expired|interrupted/.test(String(event?.event_type || '')))
  const failedIntents = relevantIntents.filter((intent) => ['failed', 'cancelled', 'expired', 'interrupted'].includes(String(intent?.status || '')))
  assert(failedEvents.length === 0, `session contains failure events: ${failedEvents.map((event) => event.event_type).join(', ')}`)
  assert(failedIntents.length === 0, `session contains failed relevant intents: ${failedIntents.map((intent) => `${intent.run_id}:${intent.status}`).join(', ')}`)
  result.gates.no_failures = true
  result.result = 'PASS'
}

try {
  await main()
} catch (error) {
  result.error = error?.stack || String(error)
  log(result.error)
} finally {
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((name) => result.gates[name] !== true)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
