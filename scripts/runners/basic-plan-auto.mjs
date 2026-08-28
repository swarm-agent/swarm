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
const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || 'medium')).trim().toLowerCase()
const planModel = String(option('--plan-model', process.env.SWARM_RUNNER_PLAN_MODEL || '')).trim()
const planThinking = String(option('--plan-thinking', process.env.SWARM_RUNNER_PLAN_THINKING || 'high')).trim().toLowerCase()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is required and must contain only letters, numbers, dots, underscores, or dashes')
if (!Number.isFinite(timeoutMs) || timeoutMs < 30000) throw new Error('--timeout-ms must be at least 30000')
if (![actionThinking, planThinking].every((value) => ['low', 'medium', 'high', 'xhigh'].includes(value))) throw new Error('role thinking must be low, medium, high, or xhigh')

const startedAt = new Date().toISOString()
const testID = `runner-basic-plan-auto-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const requiredGates = [
  'provider_runnable', 'recommended_models', 'models_configured', 'plan_mode_created',
  'model_profile_snapshot', 'two_checkpoint_proposal', 'plan_approved_automatically',
  'mode_switched_to_auto', 'checkpoints_completed', 'subtasks_completed',
  'plan_model_verified', 'auto_model_verified', 'no_failures', 'models_restored',
]
const result = {
  result: 'NOT_DONE',
  test: 'basic-plan-auto',
  test_id: testID,
  started_at: startedAt,
  api_url: apiURL,
  provider,
  models: {},
  ids: {},
  gates: {},
  diagnostics: {},
  failures: [],
}
let token = suppliedToken
let originalSwarmSettings = null
let settingsChanged = false

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const fail = (message) => {
  result.failures.push(message)
  throw new Error(message)
}
const assert = (condition, message) => {
  if (!condition) fail(message)
}
const log = (message) => process.stderr.write(`[basic-plan-auto] ${message}\n`)

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
    try {
      decoded = text ? JSON.parse(text) : null
    } catch {
      decoded = { raw: text }
    }
    if (!allowError && !response.ok) {
      fail(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1000)}`)
    }
    return { ok: response.ok, status: response.status, body: decoded, text }
  } finally {
    clearTimeout(timer)
  }
}

function recommendationFor(record, roles) {
  const recommendations = Array.isArray(record?.recommendations) ? record.recommendations : []
  return recommendations.find((item) => roles.includes(String(item?.role || '').trim().toLowerCase())) || null
}

function exactAssignment(records, model, thinking, label) {
  if (!model) return null
  assert(records.some((record) => String(record?.model || '').trim() === model), `model catalog does not contain ${provider}/${model} for ${label}`)
  return { provider, model, thinking, service_tier: 'fast' }
}

function recommendedAssignment(records, roles, label) {
  for (const record of records) {
    const recommendation = recommendationFor(record, roles)
    if (!recommendation) continue
    const model = String(record?.model || '').trim()
    const thinking = String(recommendation?.thinking || record?.default_thinking || '').trim().toLowerCase()
    if (!model || !thinking) continue
    const serving = String(recommendation?.serving || '').trim().toLowerCase()
    return {
      provider,
      model,
      thinking,
      ...(serving === 'fast' || serving === 'priority' ? { service_tier: 'priority' } : {}),
    }
  }
  fail(`model catalog has no complete ${label} recommendation for provider ${provider}`)
}

function parseToolArguments(permission) {
  const raw = permission?.tool_arguments ?? permission?.arguments ?? permission?.payload ?? {}
  if (raw && typeof raw === 'object') return raw
  try {
    return JSON.parse(String(raw || '{}'))
  } catch {
    return {}
  }
}

async function waitForPlanPermission(sessionID, runID) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`, undefined, 'list pending permissions')
    const permission = (response.body?.permissions || []).find((item) =>
      String(item?.tool_name || '') === 'exit_plan_mode' && (!runID || String(item?.run_id || '') === runID),
    )
    if (permission?.id) return permission
    await sleep(1000)
  }
  fail(`timed out waiting for exit_plan_mode permission for run ${runID}`)
}

async function waitForMode(sessionID, wantedMode) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}`, undefined, 'read session mode')
    const session = response.body?.session || response.body
    if (String(session?.mode || '') === wantedMode) return session
    await sleep(1000)
  }
  fail(`timed out waiting for session mode ${wantedMode}`)
}

async function waitForCompletedPlan(sessionID) {
  const deadline = Date.now() + timeoutMs
  let latest = null
  while (Date.now() < deadline) {
    const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/plans/active`, undefined, 'read active plan')
    latest = response.body?.active_plan || response.body?.plan || null
    const checkpoints = latest?.document?.checkpoints || []
    if (checkpoints.length === 2 && checkpoints.every((checkpoint) => checkpoint?.status === 'completed')) return latest
    await sleep(2000)
  }
  fail(`timed out waiting for two completed checkpoints; latest=${JSON.stringify(latest?.document?.checkpoints || [])}`)
}

async function fetchAllEvents(sessionID) {
  const events = []
  let afterSeq = 0
  for (let page = 0; page < 20; page += 1) {
    const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=${afterSeq}&limit=1000`, undefined, `read events page ${page}`)
    const batch = response.body?.events || []
    events.push(...batch)
    const nextSeq = Number(response.body?.next_seq || response.body?.applied_seq || 0)
    if (batch.length < 1000 || nextSeq <= afterSeq) return { events, replay: response.body }
    afterSeq = nextSeq
  }
  fail('session event replay exceeded 20 pages')
}

function objectPayload(value) {
  if (value && typeof value === 'object') return value
  try {
    return JSON.parse(String(value || '{}'))
  } catch {
    return {}
  }
}

function usageRecordsFromEvents(events) {
  const records = []
  for (const event of events) {
    const payload = objectPayload(event?.payload)
    const usage = payload?.turn_usage || payload?.TurnUsage
    if (usage && typeof usage === 'object') records.push(usage)
  }
  return records
}

async function main() {
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
    token = String(auth.body?.token || '').trim()
    assert(token, 'desktop authentication returned no token; set SWARM_RUNNER_TOKEN when the target does not permit desktop bootstrap')
  }

  const providersResponse = await api('GET', '/v1/providers', undefined, 'list providers')
  const providerStatus = (providersResponse.body?.providers || []).find((item) => String(item?.id || '').trim().toLowerCase() === provider)
  assert(providerStatus, `provider ${provider} is not registered`)
  assert(providerStatus.runnable !== false, `provider ${provider} is not runnable: ${providerStatus.message || 'no status message'}`)
  result.gates.provider_runnable = true

  const catalogResponse = await api('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'read model recommendations')
  const records = catalogResponse.body?.records || []
  const planAssignment = exactAssignment(records, planModel, planThinking, 'plan') || recommendedAssignment(records, ['plan'], 'plan')
  const actionAssignment = exactAssignment(records, actionModel, actionThinking, 'auto') || recommendedAssignment(records, ['auto', 'main'], 'auto')
  result.models = { plan: planAssignment, auto: actionAssignment }
  result.gates.recommended_models = true
  log(`using ${provider} plan=${planAssignment.model} auto=${actionAssignment.model}`)

  const settingsResponse = await api('GET', '/v1/agent-model-settings', undefined, 'read agent model settings')
  originalSwarmSettings = settingsResponse.body?.agent_model_settings?.swarm || null
  assert(originalSwarmSettings?.action?.model && originalSwarmSettings?.plan?.model, 'canonical Swarm action/plan model settings are missing')
  await api('PATCH', '/v1/agent-model-settings', { swarm: { action: actionAssignment, plan: planAssignment } }, 'apply runner model settings')
  settingsChanged = true
  result.gates.models_configured = true

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
    title: `${testID} basic plan auto`,
    workspace_path: workspacePath,
    workspace_name: String(binding?.source_workspace_name || 'runner-test'),
    workspace_binding_id: binding.workspace_binding_id,
    swarm_id: runtime.swarm_id,
    target_kind: 'host',
    target_relationship: 'self',
    mode: 'plan',
    agent_name: 'swarm',
    preference: planAssignment,
    model_profile: { use_account_default: true },
    metadata: { runner_test: 'basic-plan-auto', runner_test_id: testID, provider },
  }, 'create plan session')
  const session = createResponse.body?.session || {}
  const sessionID = String(session?.id || '').trim()
  assert(sessionID, 'session creation returned no session id')
  result.ids.session_id = sessionID
  assert(session.mode === 'plan', `created session mode is ${session.mode}, want plan`)
  assert(session.model_profile?.plan?.model === planAssignment.model, `session plan model is ${session.model_profile?.plan?.model}, want ${planAssignment.model}`)
  assert(session.model_profile?.action?.model === actionAssignment.model, `session action model is ${session.model_profile?.action?.model}, want ${actionAssignment.model}`)
  result.gates.plan_mode_created = true
  result.gates.model_profile_snapshot = true

  const prompt = [
    `Basic runner flow ${testID}.`,
    'Create exactly two ordered checkpoints with ids cp-1 and cp-2.',
    'Each checkpoint must contain exactly one simple task so it materializes exactly one subtask.',
    'Checkpoint cp-1 should complete by calling plan_manage complete_checkpoint with result BASIC_CP1_OK.',
    'Checkpoint cp-2 should complete by calling plan_manage complete_checkpoint with result BASIC_CP2_OK.',
    'Use automatic checkpoint execution and submit the complete structured plan now with exit_plan_mode.',
    'Do not inspect or modify workspace files; this test only verifies plan lifecycle and model switching.',
  ].join(' ')
  const messageResponse = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:message`,
    role: 'user',
    content: prompt,
    metadata: { runner_test_id: testID },
  }, 'start plan turn')
  const initialRunID = String(messageResponse.body?.run_intent?.run_id || messageResponse.body?.run_id || '')
  assert(initialRunID, 'plan message returned no run id')
  result.ids.plan_run_id = initialRunID

  const permission = await waitForPlanPermission(sessionID, initialRunID)
  result.ids.exit_plan_permission_id = permission.id
  const proposed = parseToolArguments(permission)
  const proposedDocument = proposed?.document || proposed?.approved_arguments?.document || {}
  const proposedCheckpoints = proposedDocument?.checkpoints || []
  assert(proposedCheckpoints.length === 2, `AI proposed ${proposedCheckpoints.length} checkpoints, want exactly 2`)
  assert(proposedCheckpoints.every((checkpoint) => Array.isArray(checkpoint?.tasks) && checkpoint.tasks.length === 1), 'each proposed checkpoint must have exactly one task')
  result.gates.two_checkpoint_proposal = true

  const resolveResponse = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/${encodeURIComponent(permission.id)}/resolve`, {
    action: 'allow_once',
    reason: `${testID} automatic checkpoint acceptance`,
    approved_arguments: {
      execution_granularity: 'checkpointed',
      continuation_policy: 'automatic',
      continue_automatically: true,
    },
  }, 'approve plan automatically')
  assert(resolveResponse.body?.permission?.status === 'approved', 'exit_plan_mode permission was not approved')
  result.gates.plan_approved_automatically = true

  await waitForMode(sessionID, 'auto')
  result.gates.mode_switched_to_auto = true
  const completedPlan = await waitForCompletedPlan(sessionID)
  const checkpoints = completedPlan.document?.checkpoints || []
  assert(checkpoints.length === 2, `completed plan has ${checkpoints.length} checkpoints, want exactly 2`)
  assert(checkpoints.every((checkpoint) => (checkpoint?.subtasks || []).length === 1), 'each checkpoint must materialize exactly one subtask')
  assert(checkpoints.every((checkpoint) => checkpoint.subtasks[0]?.status === 'completed'), 'every checkpoint subtask must complete automatically')
  assert(checkpoints.every((checkpoint) => checkpoint?.handoff || checkpoint?.report || checkpoint?.result), 'every checkpoint must preserve terminal evidence')
  result.ids.plan_id = completedPlan.id || completedPlan.document?.id || ''
  result.ids.checkpoint_run_ids = checkpoints.map((checkpoint) => checkpoint?.run_id || checkpoint?.attempts?.at(-1)?.run_id || '').filter(Boolean)
  result.gates.checkpoints_completed = true
  result.gates.subtasks_completed = true

  const { events, replay } = await fetchAllEvents(sessionID)
  const runIntents = replay?.run_intents || []
  const failedReplayIntents = runIntents.filter((intent) => /failed|cancelled|expired|interrupted/.test(String(intent?.status || '')))
  assert(failedReplayIntents.length === 0, `session has failed run intents: ${failedReplayIntents.map((intent) => `${intent.run_id}:${intent.status}`).join(', ')}`)
  const expectedCheckpointRunIDs = new Set(result.ids.checkpoint_run_ids)
  const checkpointIntents = runIntents.filter((intent) => expectedCheckpointRunIDs.has(String(intent?.run_id || '')))
  assert(checkpointIntents.length === expectedCheckpointRunIDs.size && checkpointIntents.every((intent) => intent?.status === 'completed'), 'completed checkpoint run intents are missing from event replay')
  result.ids.checkpoint_run_ids = checkpointIntents.map((intent) => String(intent.run_id || '')).filter(Boolean)
  const usageFromEvents = usageRecordsFromEvents(events)
  const usageResponse = await api('GET', `/v1/sessions/${encodeURIComponent(sessionID)}/usage?limit=100`, undefined, 'read usage evidence')
  const usageRecords = [...usageFromEvents, ...(usageResponse.body?.turn_usage_records || [])]
  const usageBelongsToRun = (usage, runID) => {
    const usageRunID = String(usage?.run_id || '')
    return usageRunID === runID || usageRunID.startsWith(`${runID}/`)
  }
  const planUsage = usageRecords.find((usage) => usageBelongsToRun(usage, initialRunID) && String(usage?.provider || '') === provider && String(usage?.model || '') === planAssignment.model)
  assert(planUsage, `no runtime usage evidence found for plan model ${provider}/${planAssignment.model} on run ${initialRunID}`)
  const checkpointRunIDs = new Set(result.ids.checkpoint_run_ids)
  const autoUsage = usageRecords.filter((usage) => [...checkpointRunIDs].some((runID) => usageBelongsToRun(usage, runID)) && String(usage?.provider || '') === provider && String(usage?.model || '') === actionAssignment.model)
  assert(autoUsage.length >= 2, `found ${autoUsage.length} checkpoint usage records for auto model ${provider}/${actionAssignment.model}, want at least 2`)
  result.diagnostics.runtime_usage = {
    plan: { run_id: initialRunID, provider: planUsage.provider, model: planUsage.model },
    auto: autoUsage.map((usage) => ({ run_id: usage.run_id, provider: usage.provider, model: usage.model })),
  }
  result.gates.plan_model_verified = true
  result.gates.auto_model_verified = true

  const failedEvents = events.filter((event) => /failed|cancelled|expired|interrupted/.test(String(event?.event_type || '')))
  const failedIntents = (replay?.run_intents || []).filter((intent) => !['completed'].includes(String(intent?.status || '')))
  assert(failedEvents.length === 0, `session contains failure events: ${failedEvents.map((event) => event.event_type).join(', ')}`)
  assert(failedIntents.length === 0, `session contains non-completed run intents: ${failedIntents.map((intent) => `${intent.run_id}:${intent.status}`).join(', ')}`)
  result.gates.no_failures = true

  result.result = 'PASS'
}

try {
  await main()
} catch (error) {
  result.error = error?.stack || String(error)
  log(result.error)
} finally {
  if (settingsChanged && originalSwarmSettings) {
    try {
      await api('PATCH', '/v1/agent-model-settings', { swarm: originalSwarmSettings }, 'restore agent model settings')
      result.gates.models_restored = true
    } catch (restoreError) {
      result.failures.push(`failed to restore Swarm model settings: ${restoreError?.message || restoreError}`)
      result.result = 'NOT_DONE'
    }
  }
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((name) => result.gates[name] !== true)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
