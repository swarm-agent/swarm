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
const linkedWorkspacePathOverride = String(option('--linked-workspace-path', process.env.SWARM_RUNNER_LINKED_WORKSPACE_PATH || '')).trim()
const scenarioOption = String(option('--scenario', process.env.SWARM_RUNNER_SCENARIO || 'all')).trim().toLowerCase()
const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || 'medium')).trim().toLowerCase()
const planModel = String(option('--plan-model', process.env.SWARM_RUNNER_PLAN_MODEL || '')).trim()
const planThinking = String(option('--plan-thinking', process.env.SWARM_RUNNER_PLAN_THINKING || 'high')).trim().toLowerCase()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is required and must contain only letters, numbers, dots, underscores, or dashes')
if (!Number.isFinite(timeoutMs) || timeoutMs < 30000) throw new Error('--timeout-ms must be at least 30000')
if (!actionModel || !planModel) throw new Error('--action-model and --plan-model are required; use scripts/run-testbench-runner.sh so the ignored .env supplies the explicit Fireworks role models')
if (![actionThinking, planThinking].every((value) => ['low', 'medium', 'high', 'xhigh'].includes(value))) throw new Error('role thinking must be low, medium, high, or xhigh')
if (!['all', 'new-router', 'existing-session'].includes(scenarioOption)) throw new Error('--scenario must be all, new-router, or existing-session')

const testID = `runner-task-routing-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const explicitWorkspaceCases = linkedWorkspacePathOverride ? 2 : 0
const expectedCallCount = (scenarioOption === 'all' ? 4 : 2) + (scenarioOption !== 'existing-session' ? explicitWorkspaceCases : 0)
const scenarioGates = scenarioOption === 'all'
  ? ['new_router_page_auto', 'new_router_page_plan', ...(linkedWorkspacePathOverride ? ['explicit_workspace_auto', 'explicit_workspace_plan'] : []), 'existing_session_auto', 'existing_session_plan']
  : scenarioOption === 'new-router'
    ? ['new_router_page_auto', 'new_router_page_plan', ...(linkedWorkspacePathOverride ? ['explicit_workspace_auto', 'explicit_workspace_plan'] : [])]
    : ['initial_sessions_loaded', 'first_session_selected', 'existing_session_auto', 'existing_session_plan']
const requiredGates = [
  'provider_runnable', 'explicit_models', 'models_configured', 'workspace_binding_ready', ...scenarioGates,
  'expected_task_calls', 'all_admitted', 'all_worktrees', 'mode_contracts',
  'plan_contracts', 'profiles_verified', 'no_failures', 'models_restored',
]
const result = {
  result: 'NOT_DONE',
  test: 'task-routing',
  test_id: testID,
  started_at: new Date().toISOString(),
  api_url: apiURL,
  provider,
  scenario: scenarioOption,
  expected_call_count: expectedCallCount,
  models: {},
  selected_first_session: {},
  calls: [],
  provider_runtime: [],
  gates: {},
  failures: [],
}
let token = suppliedToken
let originalSwarmSettings = null
let originalRouterSettings = null
let settingsChanged = false
let routerSettingsChanged = false

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const fail = (message) => {
  result.failures.push(message)
  throw new Error(message)
}
const assert = (condition, message) => {
  if (!condition) fail(message)
}
const log = (message) => process.stderr.write(`[task-routing] ${message}\n`)

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
    if (!allowError && !response.ok) fail(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1000)}`)
    return { ok: response.ok, status: response.status, body: decoded, text }
  } finally {
    clearTimeout(timer)
  }
}

function exactAssignment(records, model, thinking, label) {
  assert(records.some((record) => String(record?.model || '').trim() === model), `model catalog does not contain ${provider}/${model} for ${label}`)
  return { provider, model, thinking }
}

function metadataString(metadata, key) {
  const value = metadata && typeof metadata === 'object' ? metadata[key] : ''
  return typeof value === 'string' ? value.trim() : ''
}

function bindingSourcePath(binding) {
  return String(binding?.source_workspace_path || binding?.host_workspace_path || '').trim()
}

function bindingRuntimePath(binding) {
  return String(binding?.destination_workspace_path || binding?.runtime_workspace_path || bindingSourcePath(binding)).trim()
}

function bindingID(binding) {
  return String(binding?.workspace_binding_id || binding?.id || '').trim()
}

function runtimeID(topology) {
  const runtimes = Array.isArray(topology?.runtimes) ? topology.runtimes : []
  const runtime = runtimes.find((item) => item?.relationship === 'self') || runtimes[0]
  return String(runtime?.swarm_id || '').trim()
}

function chooseDefaultBinding(topology) {
  const bindings = Array.isArray(topology?.workspace_bindings) ? topology.workspace_bindings : []
  if (workspacePathOverride) {
    const matched = bindings.find((item) => bindingSourcePath(item) === workspacePathOverride || bindingRuntimePath(item) === workspacePathOverride)
    if (matched) return matched
  }
  return bindings.find((item) => item?.state === 'bound' && bindingID(item)) || bindings.find((item) => bindingID(item)) || null
}

function chooseBindingForSession(topology, session, fallback) {
  const bindings = Array.isArray(topology?.workspace_bindings) ? topology.workspace_bindings : []
  const metadata = session?.metadata || {}
  const wantedID = metadataString(metadata, 'swarm_v3_workspace_binding_id') || metadataString(metadata, 'local_workspace_binding_id')
  if (wantedID) {
    const matched = bindings.find((item) => bindingID(item) === wantedID)
    if (matched) return matched
  }
  const wantedPath = metadataString(metadata, 'swarm_v3_source_workspace_path') || String(session?.workspace_path || '').trim()
  if (wantedPath) {
    const matched = bindings.find((item) => bindingSourcePath(item) === wantedPath || bindingRuntimePath(item) === wantedPath)
    if (matched) return matched
  }
  return fallback
}

function authorityFor(topology, binding) {
  const workspacePath = bindingSourcePath(binding)
  const runtimeWorkspacePath = bindingRuntimePath(binding)
  const workspaceBindingID = bindingID(binding)
  const swarmID = runtimeID(topology)
  assert(workspacePath, 'selected topology binding has no source workspace path')
  assert(runtimeWorkspacePath, 'selected topology binding has no runtime workspace path')
  assert(workspaceBindingID, 'selected topology binding has no workspace_binding_id')
  assert(swarmID, 'topology has no self Swarm runtime')
  return {
    workspace_path: workspacePath,
    host_workspace_path: workspacePath,
    runtime_workspace_path: runtimeWorkspacePath,
    workspace_binding_id: workspaceBindingID,
    swarm_id: swarmID,
    target_kind: 'host',
    target_relationship: 'self',
  }
}

async function bootstrapSessions() {
  const response = await api('POST', '/v3/sync/bootstrap', {
    surface: 'desktop',
    selector: { kind: 'recent', global: true, recent: { limit: 50 } },
    history: { mode: 'none' },
    resources: {
      messages: false,
      events: false,
      run_intents: false,
      current_run_state: true,
      session_view: false,
      active_plan: true,
      plan_revisions: false,
      permission_summaries: true,
      notifications: false,
      notification_summary: false,
      tasks: false,
    },
    include_active: true,
  }, 'load initial Desktop sessions')
  return response.body || {}
}

async function hydrateSession(sessionID) {
  const response = await api('POST', '/v3/sync/hydrate', {
    surface: 'desktop',
    session_ids: [sessionID],
    history: {
      mode: 'tail',
      max_messages_per_session: 200,
      max_events_per_session: 200,
      manifest_policy: 'manifest',
    },
    resources: {
      messages: true,
      events: true,
      run_intents: true,
      current_run_state: true,
      session_view: true,
      active_plan: true,
      plan_revisions: false,
      permission_summaries: false,
    },
    include_active: true,
  }, `hydrate task session ${sessionID}`)
  return response.body || {}
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

function isTerminalIntentStatus(status) {
  return ['completed', 'failed', 'cancelled', 'expired', 'interrupted'].includes(String(status || '').trim().toLowerCase())
}

async function waitForAdmission(sessionID) {
  const deadline = Date.now() + Math.min(timeoutMs, 120000)
  let latest = null
  while (Date.now() < deadline) {
    latest = await hydrateSession(sessionID)
    const messages = latest.messages_by_session?.[sessionID] || []
    const user = messages.find((message) => String(message?.role || '').toLowerCase() === 'user' && String(message?.content || '').trim())
    const intents = latest.run_intents_by_session?.[sessionID] || []
    if (user && intents.length > 0) return { snapshot: latest, user, intents }
    await sleep(500)
  }
  const intents = latest?.run_intents_by_session?.[sessionID] || []
  fail(`timed out waiting for durable /task admission in ${sessionID}; intents=${intents.map((intent) => `${intent.run_id}:${intent.status}`).join(', ')}`)
}

async function usageForSession(sessionID, events) {
  const response = await api('GET', `/v1/sessions/${encodeURIComponent(sessionID)}/usage?limit=100`, undefined, `read usage for ${sessionID}`)
  return [...usageRecordsFromEvents(events), ...(response.body?.turn_usage_records || [])]
}

async function runTaskCall({ scenario, mode, authority, selectedSessionID, expectedAssignment }) {
  const sequence = result.calls.length + 1
  const clientRequestID = `${testID}:${scenario}:${mode}:${sequence}`
  const uniqueTaskLabel = `${testID.slice(-8)}-${scenario}-${mode}-${sequence}`
  const prompt = mode === 'plan'
    ? `Routing validation task ${uniqueTaskLabel}: verify creation of a managed-worktree Plan session. After the Swarm session starts, reply with exactly ACK and nothing else. Stay in Plan mode. Do not create, save, submit, approve, or mutate a plan. Do not use tools or inspect files.`
    : `Routing validation task ${uniqueTaskLabel}: verify creation of a managed-worktree Auto session. After the Swarm session starts, reply with exactly ACK and nothing else. Do not create, save, submit, approve, or mutate a plan. Do not use tools or inspect files.`
  log(`launching ${scenario} /task ${mode} call ${sequence}`)
  const response = await api('POST', '/v3/sessions:background-router', {
    ...authority,
    input: prompt,
    client_request_id: clientRequestID,
    idempotency_key: clientRequestID,
    agent_name: 'swarm',
    metadata: {
      source: 'desktop-v3-task-command',
      runner_test: 'task-routing',
      runner_test_id: testID,
      runner_scenario: scenario,
      runner_selected_session_id: selectedSessionID || '',
      runner_call: sequence,
    },
    plan_mode_requested: mode === 'plan',
  }, `start ${scenario} /task ${mode}`)
  const launched = response.body || {}
  const sessionID = String(launched.session_id || launched.session?.id || '').trim()
  assert(sessionID, `${scenario} /task ${mode} returned no session_id`)
  assert(String(launched.starting_mode || '') === mode, `${scenario} /task ${mode} started in ${launched.starting_mode}`)
  const immediateSession = launched.session || {}
  const identity = launched.session_view?.identity || {}
  assert(immediateSession.worktree_enabled === true || identity.worktree_enabled === true, `${scenario} /task ${mode} did not enable a managed worktree`)
  assert(String(immediateSession.worktree_root_path || identity.worktree_root_path || '').trim(), `${scenario} /task ${mode} has no worktree root`)
  assert(String(immediateSession.worktree_branch || identity.worktree_branch || '').trim(), `${scenario} /task ${mode} has no worktree branch`)

  const settled = await waitForAdmission(sessionID)
  const hydratedSession = settled.snapshot.sessions_by_id?.[sessionID] || {}
  const session = {
    ...immediateSession,
    ...hydratedSession,
    metadata: { ...(immediateSession?.metadata || {}), ...(hydratedSession?.metadata || {}) },
    model_profile: hydratedSession?.model_profile || immediateSession?.model_profile,
  }
  const view = settled.snapshot.session_views_by_id?.[sessionID] || launched.session_view || {}
  const messages = settled.snapshot.messages_by_session?.[sessionID] || []
  const events = settled.snapshot.events_by_session?.[sessionID] || []
  const assistantContent = String([...messages].reverse().find((message) => String(message?.role || '').toLowerCase() === 'assistant' && String(message?.content || '').trim())?.content || '').trim()
  assert(String(session?.mode || '') === mode, `${scenario} /task ${mode} settled in mode ${session?.mode}`)
  assert(session?.worktree_enabled === true, `${scenario} /task ${mode} settled without worktree_enabled`)
  assert(String(session?.worktree_root_path || '').trim(), `${scenario} /task ${mode} settled without worktree_root_path`)
  assert(String(session?.worktree_branch || '').trim(), `${scenario} /task ${mode} settled without worktree_branch`)
  assert(session?.metadata?.background_router_session === true, `${scenario} /task ${mode} lacks background Router metadata`)
  assert(session?.metadata?.routed_worktree_requested === true, `${scenario} /task ${mode} lacks required routed-worktree intent metadata`)
  assert(session?.metadata?.plan_mode_requested === (mode === 'plan'), `${scenario} /task ${mode} stored the wrong plan intent`)
  assert(metadataString(session?.metadata, 'swarm_v3_workspace_binding_id') === authority.workspace_binding_id, `${scenario} /task ${mode} settled on the wrong workspace binding`)
  assert(metadataString(session?.metadata, 'swarm_v3_source_workspace_path') === authority.workspace_path, `${scenario} /task ${mode} settled on the wrong source workspace`)
  assert(metadataString(session?.metadata, 'swarm_v3_worktree_owner_session_id') === sessionID, `${scenario} /task ${mode} worktree ownership does not match its session`)
  assert(String(session.worktree_root_path || '').trim() !== authority.workspace_path, `${scenario} /task ${mode} reused the shared source checkout`)
  const hasActivePlan = view?.has_active_plan === true || Boolean(view?.active_plan)
  assert(!hasActivePlan, `${scenario} /task ${mode} unexpectedly created an active plan`)
  const failedIntents = settled.intents.filter((intent) => ['failed', 'expired', 'interrupted'].includes(String(intent?.status || '').trim().toLowerCase()))
  assert(failedIntents.length === 0, `${scenario} /task ${mode} has failed intents: ${failedIntents.map((intent) => `${intent.run_id}:${intent.status}`).join(', ')}`)
  const failedEvents = events.filter((event) => /failed|expired|interrupted/.test(String(event?.event_type || '').toLowerCase()))
  assert(failedEvents.length === 0, `${scenario} /task ${mode} has failure events: ${failedEvents.map((event) => event.event_type).join(', ')}`)

  const modelProfile = session?.model_profile || immediateSession?.model_profile || {}
  const roleProfile = mode === 'plan' ? modelProfile?.plan : modelProfile?.action
  assert(String(roleProfile?.provider || '') === provider, `${scenario} /task ${mode} profile provider is ${roleProfile?.provider}, want ${provider}`)
  assert(String(roleProfile?.model || '') === expectedAssignment.model, `${scenario} /task ${mode} profile model is ${roleProfile?.model}, want ${expectedAssignment.model}`)
  assert(String(roleProfile?.thinking || '').toLowerCase() === expectedAssignment.thinking, `${scenario} /task ${mode} profile thinking is ${roleProfile?.thinking}, want ${expectedAssignment.thinking}`)
  const usageRecords = await usageForSession(sessionID, events)
  const providerUsage = usageRecords.filter((usage) => String(usage?.provider || '') === provider)
  const matchingUsage = providerUsage.find((usage) => String(usage?.model || '') === expectedAssignment.model) || providerUsage[0] || null
  result.provider_runtime.push({
    scenario, mode, session_id: sessionID,
    status: matchingUsage ? 'observed' : 'pending',
    provider: matchingUsage?.provider || null,
    model: matchingUsage?.model || null,
    run_id: matchingUsage?.run_id || null,
  })
  for (const intent of settled.intents.filter((item) => !isTerminalIntentStatus(item?.status))) {
    const stopped = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/run/stop`, {
      run_id: intent.run_id,
      target_swarm_id: authority.swarm_id,
      reason: `${testID} bounded task-routing admission verified`,
    }, `stop admitted ${scenario} /task ${mode}`, true)
    assert(stopped.ok, `${scenario} /task ${mode} could not stop bounded probe run ${intent.run_id}: HTTP ${stopped.status}`)
  }

  const userMessages = messages.filter((message) => String(message?.role || '').toLowerCase() === 'user')
  assert(userMessages.length >= 1, `${scenario} /task ${mode} did not preserve its durable user message`)
  const call = {
    sequence,
    scenario,
    mode,
    selected_session_id: selectedSessionID || null,
    session_id: sessionID,
    assistant: assistantContent || null,
    worktree_root_path: session.worktree_root_path,
    worktree_branch: session.worktree_branch,
    has_active_plan: hasActivePlan,
    model_profile: { provider: roleProfile.provider, model: roleProfile.model, thinking: roleProfile.thinking },
    runtime_usage: matchingUsage ? { provider: matchingUsage.provider, model: matchingUsage.model, run_id: matchingUsage.run_id } : null,
    run_ids: settled.intents.map((intent) => intent.run_id),
  }
  result.calls.push(call)
  result.gates[`${scenario}_${mode}`] = true
  log(`verified ${scenario} /task ${mode}: ${sessionID}`)
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
  const planAssignment = exactAssignment(records, planModel, planThinking, 'Swarm plan')
  const actionAssignment = exactAssignment(records, actionModel, actionThinking, 'Swarm auto')
  const routerAssignment = exactAssignment(records, actionModel, actionThinking, 'Router routing')
  result.models = { plan: planAssignment, auto: actionAssignment, router: routerAssignment }
  result.gates.explicit_models = true

  const settingsResponse = await api('GET', '/v1/agent-model-settings', undefined, 'read agent model settings')
  originalSwarmSettings = settingsResponse.body?.agent_model_settings?.swarm || null
  originalRouterSettings = settingsResponse.body?.agent_model_settings?.system_agents?.router || null
  assert(originalSwarmSettings?.action?.model && originalSwarmSettings?.plan?.model, 'canonical Swarm action/plan model settings are missing')
  assert(originalRouterSettings?.model, 'canonical Router model setting is missing')
  await api('PATCH', '/v1/agent-model-settings', { swarm: { action: actionAssignment, plan: planAssignment } }, 'apply task runner model settings')
  settingsChanged = true
  await api('PATCH', '/v1/agent-model-settings', { system_agents: { router: routerAssignment } }, 'apply task Router model setting')
  routerSettingsChanged = true
  result.gates.models_configured = true

  if (workspacePathOverride) {
    await api('POST', '/v1/workspace/add', { path: workspacePathOverride, name: 'task-routing-primary', make_current: true }, 'ensure canonical workspace self binding')
  }
  if (linkedWorkspacePathOverride) {
    await api('POST', '/v1/workspace/add', { path: linkedWorkspacePathOverride, name: 'task-routing-linked', make_current: false }, 'ensure linked saved-workspace self binding')
  }
  result.gates.workspace_binding_ready = true

  const topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const topologyBindings = Array.isArray(topology?.workspace_bindings) ? topology.workspace_bindings : []
  const topologyRuntimes = Array.isArray(topology?.runtimes) ? topology.runtimes : []
  assert(topologyBindings.length > 0, 'topology has zero workspace bindings')
  assert(topologyBindings.some((item) => bindingID(item)), 'topology workspace bindings have no binding IDs')
  assert(topologyRuntimes.some((item) => String(item?.swarm_id || '').trim()), 'topology has no runtime IDs')
  const defaultBinding = chooseDefaultBinding(topology)
  assert(defaultBinding, 'topology has no usable bound workspace')
  const newRouterAuthority = authorityFor(topology, defaultBinding)
  const linkedBinding = linkedWorkspacePathOverride
    ? topologyBindings.find((item) => bindingSourcePath(item) === linkedWorkspacePathOverride || bindingRuntimePath(item) === linkedWorkspacePathOverride)
    : null
  if (linkedWorkspacePathOverride) assert(linkedBinding, `topology has no bound linked workspace for the configured task-routing fixture`)

  if (scenarioOption !== 'existing-session') {
    await runTaskCall({ scenario: 'new_router_page', mode: 'auto', authority: newRouterAuthority, selectedSessionID: '', expectedAssignment: actionAssignment })
    await runTaskCall({ scenario: 'new_router_page', mode: 'plan', authority: newRouterAuthority, selectedSessionID: '', expectedAssignment: planAssignment })
    if (linkedBinding) {
      const linkedAuthority = authorityFor(topology, linkedBinding)
      await runTaskCall({ scenario: 'explicit_workspace', mode: 'auto', authority: linkedAuthority, selectedSessionID: '', expectedAssignment: actionAssignment })
      await runTaskCall({ scenario: 'explicit_workspace', mode: 'plan', authority: linkedAuthority, selectedSessionID: '', expectedAssignment: planAssignment })
    }
  }

  if (scenarioOption !== 'new-router') {
    // Match the Desktop interaction: refresh the sidebar, select its first session,
    // and issue the same two /task forms there. A split worker seeds one ordinary
    // non-AI V3 session because its fresh isolated Swarm has no prior sidebar history.
    if (scenarioOption === 'existing-session') {
      const seedRequestID = `${testID}:seed-existing-session`
      await api('POST', '/v3/sessions', {
        ...newRouterAuthority,
        client_request_id: seedRequestID,
        idempotency_key: seedRequestID,
        title: 'Routing Seed Session',
        mode: 'auto',
        agent_name: 'swarm',
        worktree_mode: 'off',
        metadata: { source: 'task-routing-seed', runner_test_id: testID },
      }, 'seed existing Desktop session')
    }
    const bootstrap = await bootstrapSessions()
    const sessionOrder = Array.isArray(bootstrap.session_order) ? bootstrap.session_order : []
    assert(sessionOrder.length > 0, 'Desktop session order is empty; cannot select the first existing session')
    result.gates.initial_sessions_loaded = true
    const firstSessionID = String(sessionOrder[0] || '').trim()
    const firstSession = bootstrap.sessions_by_id?.[firstSessionID]
    assert(firstSessionID && firstSession, 'Desktop first session is missing from sessions_by_id')
    const existingBinding = chooseBindingForSession(topology, firstSession, defaultBinding)
    assert(existingBinding, `first existing session ${firstSessionID} has no usable workspace binding`)
    const existingAuthority = authorityFor(topology, existingBinding)
    result.selected_first_session = {
      session_id: firstSessionID,
      title: firstSession.title,
      workspace_path: firstSession.workspace_path,
      workspace_binding_id: existingAuthority.workspace_binding_id,
    }
    result.gates.first_session_selected = true

    await runTaskCall({ scenario: 'existing_session', mode: 'auto', authority: existingAuthority, selectedSessionID: firstSessionID, expectedAssignment: actionAssignment })
    await runTaskCall({ scenario: 'existing_session', mode: 'plan', authority: existingAuthority, selectedSessionID: firstSessionID, expectedAssignment: planAssignment })
  }

  assert(result.calls.length === expectedCallCount, `runner completed ${result.calls.length} AI calls, want ${expectedCallCount}`)
  assert(result.calls.every((call) => call.run_ids.length > 0), 'not every task call published a durable run intent')
  assert(result.calls.every((call) => call.worktree_root_path && call.worktree_branch), 'not every task call used a durable worktree')
  assert(result.calls.filter((call) => call.mode === 'auto').every((call) => call.has_active_plan === false), 'an Auto task created a plan')
  assert(result.calls.filter((call) => call.mode === 'plan').every((call) => call.has_active_plan === false), 'a Plan acknowledgement unexpectedly created a plan')
  assert(result.calls.filter((call) => call.mode === 'auto').every((call) => call.model_profile.model === actionAssignment.model), 'an Auto task used the wrong model')
  assert(result.calls.filter((call) => call.mode === 'plan').every((call) => call.model_profile.model === planAssignment.model), 'a Plan task used the wrong model')
  result.gates.expected_task_calls = true
  result.gates.all_admitted = true
  result.gates.all_worktrees = true
  result.gates.mode_contracts = true
  result.gates.plan_contracts = true
  result.gates.profiles_verified = true
  result.gates.no_failures = true
  result.result = 'PASS'
}

try {
  await main()
} catch (error) {
  result.error = error?.stack || String(error)
  log(result.error)
} finally {
  let restoreFailed = false
  if (routerSettingsChanged && originalRouterSettings) {
    try {
      await api('PATCH', '/v1/agent-model-settings', { system_agents: { router: originalRouterSettings } }, 'restore Router model setting')
    } catch (restoreError) {
      result.failures.push(`failed to restore Router model setting: ${restoreError?.message || restoreError}`)
      restoreFailed = true
    }
  }
  if (settingsChanged && originalSwarmSettings) {
    try {
      await api('PATCH', '/v1/agent-model-settings', { swarm: originalSwarmSettings }, 'restore agent model settings')
    } catch (restoreError) {
      result.failures.push(`failed to restore Swarm model settings: ${restoreError?.message || restoreError}`)
      restoreFailed = true
    }
  }
  if ((settingsChanged || routerSettingsChanged) && !restoreFailed) result.gates.models_restored = true
  if (restoreFailed) result.result = 'NOT_DONE'
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((name) => result.gates[name] !== true)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
