#!/usr/bin/env node
import crypto from 'node:crypto'

const argv = process.argv.slice(2)
const option = (name, fallback = '') => {
  const index = argv.indexOf(name)
  return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback
}

const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'codex')).trim().toLowerCase()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const linkedWorkspacePathOverride = String(option('--linked-workspace-path', process.env.SWARM_RUNNER_LINKED_WORKSPACE_PATH || '')).trim()
const suppliedModel = String(option('--coder-model', option('--model', process.env.SWARM_RUNNER_CODER_MODEL || process.env.SWARM_RUNNER_MODEL || ''))).trim()
const suppliedThinking = String(option('--coder-thinking', option('--thinking', process.env.SWARM_RUNNER_CODER_THINKING || process.env.SWARM_RUNNER_THINKING || 'medium'))).trim().toLowerCase()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is invalid')
if (!Number.isFinite(timeoutMs) || timeoutMs < 30000 || timeoutMs > 600000) throw new Error('--timeout-ms must be between 30000 and 600000')
if (!suppliedModel) throw new Error('--coder-model is required; use scripts/run-testbench-runner.sh so the ignored .env supplies the explicit Fireworks Coder model')
if (!['low', 'medium', 'high', 'xhigh'].includes(suppliedThinking)) throw new Error('--thinking must be low, medium, high, or xhigh')

const testID = `runner-task-program-worktrees-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const requiredGates = [
  'provider_runnable', 'model_available', 'models_configured', 'workspace_bindings_ready',
  'same_repo_program', 'linked_repo_program', 'parent_mode_worktree', 'parent_mode_current_workspace',
  'coder_model_verified', 'sparse_worktrees_verified', 'integration_verified', 'no_failures',
]
const result = {
  result: 'NOT_DONE', test: 'task-program-worktrees', test_id: testID,
  started_at: new Date().toISOString(), api_url: apiURL, provider,
  model: {}, workspaces: {}, programs: [], gates: {}, failures: [],
}
let token = suppliedToken
let modelAssignment = null

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[task-program-worktrees] ${message}\n`)
const fail = (message) => { result.failures.push(message); throw new Error(message) }
const assert = (condition, message) => { if (!condition) fail(message) }

async function api(method, route, body, label = route, allowError = false) {
  const headers = {
    Accept: 'application/json', Origin: new URL(apiURL).origin,
    Referer: `${apiURL}/app`, 'Sec-Fetch-Site': 'same-origin',
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
  } finally { clearTimeout(timer) }
}

function assignmentFor(records) {
  const record = records.find((item) => String(item?.model || '') === suppliedModel)
  assert(record, `model catalog does not contain ${provider}/${suppliedModel}`)
  return { provider, model: suppliedModel, thinking: suppliedThinking }
}

function bindingPath(binding) {
  return String(binding?.source_workspace_path || binding?.host_workspace_path || binding?.destination_workspace_path || '').trim()
}
function bindingRuntimePath(binding) {
  return String(binding?.destination_workspace_path || binding?.runtime_workspace_path || bindingPath(binding)).trim()
}
function bindingID(binding) { return String(binding?.workspace_binding_id || binding?.id || '').trim() }
function chooseBinding(bindings, wanted, excluded = '') {
  if (wanted) {
    const match = bindings.find((item) => bindingPath(item) === wanted || bindingRuntimePath(item) === wanted)
    if (match && bindingPath(match) !== excluded) return match
    return null
  }
  return bindings.find((item) => item?.state === 'bound' && bindingID(item) && bindingPath(item) !== excluded)
    || bindings.find((item) => bindingID(item) && bindingPath(item) !== excluded) || null
}
function objectPayload(value) {
  if (value && typeof value === 'object') return value
  try { return JSON.parse(String(value || '{}')) } catch { return {} }
}
function isTerminal(status) {
  return ['completed', 'failed', 'cancelled', 'expired', 'interrupted'].includes(String(status || '').trim().toLowerCase())
}

async function createSession({ binding, runtimeID, worktreeMode, label }) {
  const requestID = `${testID}:create:${label}`
  const body = {
    client_request_id: requestID, idempotency_key: requestID,
    title: `${testID} ${label}`, workspace_path: bindingPath(binding),
    workspace_name: String(binding?.source_workspace_name || label), workspace_binding_id: bindingID(binding),
    swarm_id: runtimeID, target_kind: 'host', target_relationship: 'self',
    mode: 'auto', agent_name: 'swarm', worktree_mode: worktreeMode,
    preference: modelAssignment, model_profile: { use_account_default: true },
    metadata: { runner_test: 'task-program-worktrees', runner_test_id: testID, runner_case: label },
  }
  if (worktreeMode === 'on') body.worktree_branch_name = `agent/task-program-${crypto.randomBytes(4).toString('hex')}`
  const response = await api('POST', '/v3/sessions', body, `create ${label} parent session`)
  const session = response.body?.session || {}
  assert(session?.id, `${label} session creation returned no id`)
  if (worktreeMode === 'on') {
    assert(session?.worktree_enabled === true && String(session?.worktree_root_path || '').trim(), `${label} parent did not materialize the requested worktree`)
  } else {
    assert(session?.worktree_enabled !== true && !String(session?.worktree_root_path || '').trim(), `${label} parent unexpectedly materialized a worktree`)
  }
  return { ...session, worktree_enabled: session?.worktree_enabled === true }
}

async function hydrate(sessionID) {
  const response = await api('POST', '/v3/sync/hydrate', {
    surface: 'desktop', session_ids: [sessionID],
    history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' },
    resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: false, plan_revisions: false, permission_summaries: true },
    include_active: true,
  }, `hydrate ${sessionID}`)
  return response.body || {}
}

async function resolvePendingTaskPermissions(sessionID) {
  const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`, undefined, `list pending task permissions for ${sessionID}`)
  const pending = (response.body?.permissions || []).filter((item) => String(item?.tool_name || '') === 'task')
  for (const permission of pending) {
    await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/${encodeURIComponent(permission.id)}/resolve`, {
      action: 'allow_once', reason: `${testID} bounded task-program verification`,
    }, `approve task permission ${permission.id}`)
  }
  return pending.length
}

function taskToolOutputs(snapshot, sessionID) {
  const events = snapshot.events_by_session?.[sessionID] || []
  const outputs = []
  for (const event of events) {
    if (String(event?.event_type || '') !== 'session.tool.completed') continue
    const payload = objectPayload(event?.payload)
    if (String(payload?.tool_name || payload?.name || '') !== 'task') continue
    const raw = payload?.output ?? payload?.result ?? payload?.completed_output ?? ''
    const decoded = objectPayload(raw)
    if (decoded && typeof decoded === 'object') outputs.push(decoded)
  }
  return outputs
}

async function waitForProgram(sessionID, runID, label) {
  const deadline = Date.now() + timeoutMs
  let latest = null
  let lastBeat = 0
  while (Date.now() < deadline) {
    latest = await hydrate(sessionID)
    await resolvePendingTaskPermissions(sessionID)
    const intents = latest.run_intents_by_session?.[sessionID] || []
    const intent = intents.find((item) => String(item?.run_id || '') === runID)
    const outputs = taskToolOutputs(latest, sessionID)
    const completed = outputs.find((item) => String(item?.state || item?.program_state || item?.status || '') === 'completed')
    if (completed && intent?.status === 'completed') return { snapshot: latest, output: completed, intent }
    const events = latest.events_by_session?.[sessionID] || []
    const failedTaskEvent = events.find((event) => {
      if (String(event?.event_type || '') !== 'session.tool.failed') return false
      const payload = objectPayload(event?.payload)
      return String(payload?.tool_name || payload?.name || '') === 'task'
    })
    if (failedTaskEvent) {
      const payload = objectPayload(failedTaskEvent.payload)
      fail(`${label} task failed: ${String(payload?.error || payload?.output || 'unknown task failure').slice(0, 1000)}`)
    }
    const failed = outputs.find((item) => ['failed', 'blocked'].includes(String(item?.state || item?.program_state || item?.status || '')))
    if (failed || (intent && isTerminal(intent.status) && intent.status !== 'completed')) fail(`${label} failed: ${JSON.stringify(failed || intent).slice(0, 2000)}`)
    if (Date.now() - lastBeat >= 15000) {
      log(`${label}: waiting; run=${intent?.status || 'pending'} task_outputs=${outputs.length}`)
      lastBeat = Date.now()
    }
    await sleep(1500)
  }
  fail(`${label} timed out waiting for a completed Task Program`)
}

function programPrompt({ label, targetWorkspace, markerName }) {
  const targetClause = targetWorkspace ? ` Set top-level workspace_path to exactly ${JSON.stringify(targetWorkspace)}.` : ''
  return [
    `Critical task-program worktree probe ${testID} case ${label}.`,
    'Call the task tool exactly once with action=start and one fully declared Task Program.',
    targetClause,
    `Use program id ${label === 'same-repo-current-parent' ? 'same_repo_probe' : 'linked_repo_probe'}, one stage id verify, max_concurrency 1, and exactly one Coder job.`,
    `The Coder job must own only docs/task-program-probes/** and must create docs/task-program-probes/${markerName} containing one short public-safe line, commit it, and finish clean.`,
    `Its acceptance criterion is that the committed file docs/task-program-probes/${markerName} exists. The parent must let the Task Program integrate the committed result and then reply exactly PROGRAM_OK.`,
    'Do not use any other tool. Do not inspect unrelated files. Do not include private paths, hostnames, credentials, or topology in the file.',
  ].filter(Boolean).join(' ')
}

async function runProgram({ parent, label, targetWorkspace }) {
  const markerName = `verified-${crypto.randomBytes(4).toString('hex')}.txt`
  const response = await api('POST', `/v3/sessions/${encodeURIComponent(parent.id)}/messages`, {
    client_request_id: `${testID}:message:${label}`, role: 'user', content: programPrompt({ label, targetWorkspace, markerName }),
    metadata: { runner_test_id: testID, runner_case: label },
  }, `start ${label} task program`)
  const runID = String(response.body?.run_intent?.run_id || response.body?.run_id || '').trim()
  assert(runID, `${label} message returned no run id`)
  const settled = await waitForProgram(parent.id, runID, label)
  const messages = settled.snapshot.messages_by_session?.[parent.id] || []
  const assistant = [...messages].reverse().find((item) => String(item?.role || '').toLowerCase() === 'assistant' && String(item?.content || '').trim())
  assert(String(assistant?.content || '').trim().includes('PROGRAM_OK'), `${label} parent did not report PROGRAM_OK`)
  const output = settled.output
  const jobs = output?.jobs || output?.program_status?.jobs || output?.program?.jobs || []
  assert(Array.isArray(jobs) && jobs.length === 1, `${label} completed output does not contain exactly one job`)
  const job = jobs[0]
  assert(['integrated', 'completed'].includes(String(job?.state || job?.status || '')), `${label} job state is ${job?.state || job?.status}`)
  const workspace = String(job?.workspace_path || '').trim()
  const base = String(job?.immutable_stage_base || output?.parent_head || '').trim()
  const head = String(job?.child_head || '').trim()
  assert(base && head, `${label} job is missing immutable_stage_base/child_head evidence`)
  result.programs.push({ label, parent_session_id: parent.id, parent_worktree_enabled: parent.worktree_enabled, program_id: output?.program_id || output?.program?.id || '', job_state: job?.state || job?.status, workspace_path: workspace, base_commit: base, head_commit: head, resulting_parent_head: output?.resulting_parent_head || output?.parent_head || '' })
  return result.programs.at(-1)
}

async function main() {
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
    token = String(auth.body?.token || '').trim()
    assert(token, 'desktop authentication returned no token')
  }
  const providers = (await api('GET', '/v1/providers', undefined, 'list providers')).body?.providers || []
  const providerStatus = providers.find((item) => String(item?.id || '').trim().toLowerCase() === provider)
  assert(providerStatus && providerStatus.runnable !== false, `provider ${provider} is not runnable`)
  result.gates.provider_runnable = true

  const records = (await api('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'read model catalog')).body?.records || []
  modelAssignment = assignmentFor(records)
  result.model = modelAssignment
  result.gates.model_available = true
  const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
  const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || '')).trim().toLowerCase()
  const planModel = String(option('--plan-model', process.env.SWARM_RUNNER_PLAN_MODEL || '')).trim()
  const planThinking = String(option('--plan-thinking', process.env.SWARM_RUNNER_PLAN_THINKING || '')).trim().toLowerCase()
  const designerModel = String(option('--designer-model', process.env.SWARM_RUNNER_DESIGNER_MODEL || '')).trim()
  const designerThinking = String(option('--designer-thinking', process.env.SWARM_RUNNER_DESIGNER_THINKING || '')).trim().toLowerCase()
  assert(actionModel && planModel && designerModel, 'explicit --action-model, --plan-model, and --designer-model values are required from the ignored .env')
  assert([actionThinking, planThinking, designerThinking].every((value) => ['low', 'medium', 'high', 'xhigh'].includes(value)), 'explicit role thinking values must be low, medium, high, or xhigh')
  const exact = (model, thinking, label) => {
    assert(records.some((record) => String(record?.model || '').trim() === model), `model catalog does not contain ${provider}/${model} for ${label}`)
    return { provider, model, thinking }
  }
  const actionAssignment = exact(actionModel, actionThinking, 'Swarm auto')
  const planAssignment = exact(planModel, planThinking, 'Swarm plan')
  const designerAssignment = exact(designerModel, designerThinking, 'Designer')
  await api('PATCH', '/v1/agent-model-settings', { swarm: { action: actionAssignment, plan: planAssignment } }, 'configure Swarm models')
  await api('PATCH', '/v1/agent-model-settings', { system_agents: { coder: modelAssignment } }, 'configure Coder model')
  await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer: designerAssignment } }, 'configure Designer model')
  const settings = (await api('GET', '/v1/agent-model-settings', undefined, 'verify model settings')).body?.agent_model_settings || {}
  assert(settings?.swarm?.action?.model === actionAssignment.model && settings?.swarm?.plan?.model === planAssignment.model, 'Swarm model settings did not persist')
  assert(settings?.system_agents?.coder?.model === modelAssignment.model, 'Coder model setting did not persist')
  assert(settings?.system_agents?.designer?.model === designerAssignment.model, 'Designer model setting did not persist')
  result.gates.models_configured = true

  if (workspacePathOverride) {
    await api('POST', '/v1/workspace/add', { path: workspacePathOverride, name: 'task-program-primary', make_current: true }, 'bind primary fixture workspace')
  }
  if (linkedWorkspacePathOverride) {
    await api('POST', '/v1/workspace/add', { path: linkedWorkspacePathOverride, name: 'task-program-linked', make_current: false }, 'bind linked fixture workspace')
    if (workspacePathOverride) {
      await api('POST', '/v1/workspace/directories/add', { workspace_path: workspacePathOverride, directory_path: linkedWorkspacePathOverride }, 'authorize linked fixture for primary workspace')
    }
  }
  const topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const runtime = (topology?.runtimes || []).find((item) => item?.relationship === 'self') || (topology?.runtimes || [])[0]
  assert(runtime?.swarm_id, 'topology has no self runtime')
  const bindings = topology?.workspace_bindings || []
  const primary = chooseBinding(bindings, workspacePathOverride)
  assert(primary && bindingID(primary), 'no primary workspace binding is available')
  const linked = chooseBinding(bindings, linkedWorkspacePathOverride, bindingPath(primary))
  assert(linked && bindingID(linked), 'a second linked repository binding is required for the multi-repository case')
  assert(bindingPath(primary) !== bindingPath(linked), 'primary and linked workspace bindings resolve to the same path')
  result.workspaces = { primary: { binding_id: bindingID(primary), path: bindingPath(primary) }, linked: { binding_id: bindingID(linked), path: bindingPath(linked) } }
  result.gates.workspace_bindings_ready = true

  const currentParent = await createSession({ binding: primary, runtimeID: runtime.swarm_id, worktreeMode: 'off', label: 'current-workspace-parent' })
  const sameRepo = await runProgram({ parent: currentParent, label: 'same-repo-current-parent', targetWorkspace: '' })
  result.gates.same_repo_program = true
  result.gates.parent_mode_current_workspace = true

  const managedParent = await createSession({ binding: linked, runtimeID: runtime.swarm_id, worktreeMode: 'on', label: 'managed-worktree-parent' })
  const linkedSourcePath = bindingPath(linked)
  const linkedRepo = await runProgram({ parent: managedParent, label: 'linked-repo-managed-parent', targetWorkspace: linkedSourcePath })
  result.gates.linked_repo_program = true
  result.gates.parent_mode_worktree = true

  assert(sameRepo.workspace_path && linkedRepo.workspace_path, 'program job worktree paths are missing')
  assert(sameRepo.workspace_path !== bindingPath(primary), 'same-repo Coder ran in the parent checkout instead of an isolated worktree')
  assert(linkedRepo.workspace_path !== linkedSourcePath, 'linked-repo Coder ran in the linked checkout instead of an isolated worktree')
  assert(sameRepo.base_commit !== sameRepo.head_commit && linkedRepo.base_commit !== linkedRepo.head_commit, 'Coder job did not create a distinct committed head')
  result.gates.sparse_worktrees_verified = true
  result.gates.integration_verified = true

  const usage = []
  for (const program of result.programs) {
    const response = await api('GET', `/v1/sessions/${encodeURIComponent(program.parent_session_id)}/usage?limit=100`, undefined, `read usage for ${program.label}`)
    usage.push(...(response.body?.turn_usage_records || []))
  }
  const matchingUsage = usage.filter((item) => String(item?.model || '') === modelAssignment.model)
  assert(matchingUsage.length >= 2, `runtime usage has ${matchingUsage.length} records for ${modelAssignment.model}, want at least 2`)
  const configured = (await api('GET', '/v1/agent-model-settings', undefined, 'verify final model settings')).body?.agent_model_settings || {}
  assert(configured?.swarm?.action?.model === modelAssignment.model && configured?.swarm?.action?.thinking === modelAssignment.thinking, 'final Swarm action assignment does not match the requested model posture')
  assert(configured?.system_agents?.coder?.model === modelAssignment.model && configured?.system_agents?.coder?.thinking === modelAssignment.thinking, 'final Coder assignment does not match the requested model posture')
  result.gates.coder_model_verified = true
  result.gates.no_failures = true
  result.result = 'PASS'
}

try { await main() } catch (error) {
  result.error = error?.stack || String(error)
  log(result.error)
} finally {
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((name) => result.gates[name] !== true)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
