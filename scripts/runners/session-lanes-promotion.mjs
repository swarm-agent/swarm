#!/usr/bin/env node
import crypto from 'node:crypto'
import { execFile } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import path from 'node:path'
import { promisify } from 'node:util'

const exec = promisify(execFile)
const argv = process.argv.slice(2)
const option = (name, fallback = '') => {
  const index = argv.indexOf(name)
  return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback
}
const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || '')).trim().toLowerCase()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '900000'))
let token = String(process.env.SWARM_RUNNER_TOKEN || '').trim()
let originalSwarmSettings = null
let settingsChanged = false
const createdSessionIDs = new Set()
if (!/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is required')
if (!Number.isFinite(timeoutMs) || timeoutMs < 30000) throw new Error('--timeout-ms must be at least 30000')

const testID = `session-lanes-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const requiredGates = [
  'disposable_repository', 'workspace_bound', 'routed_lane', 'opt_out_absent',
  'swarm_branch_name', 'deployed_lane', 'task_stage_integrated_to_lane',
  'dev_unchanged_before_promotion', 'automatic_promotion_rejected',
  'explicit_promotion_advanced_dev', 'cleanup',
]
const result = { result: 'NOT_DONE', test: 'session-lanes-promotion', test_id: testID, provider, gates: {}, sessions: {}, failures: [] }
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const assert = (value, message) => { if (!value) throw new Error(message) }
const log = (message) => process.stderr.write(`[session-lanes] ${message}\n`)

async function request(method, route, body, label = route, allowError = false) {
  const headers = { Accept: 'application/json', Origin: new URL(apiURL).origin, Referer: `${apiURL}/app`, 'Sec-Fetch-Site': 'same-origin' }
  if (token) {
    headers['X-Swarm-Token'] = token
    headers.Cookie = `swarm_desktop_session=${token}`
  }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), Math.min(timeoutMs, 120000))
  try {
    const response = await fetch(`${apiURL}${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal })
    const text = await response.text()
    let decoded = null
    try { decoded = text ? JSON.parse(text) : null } catch { decoded = { raw: text } }
    if (!allowError && !response.ok) throw new Error(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return { ok: response.ok, status: response.status, body: decoded, text }
  } finally { clearTimeout(timer) }
}

async function git(repo, ...args) {
  const { stdout } = await exec('git', ['-C', repo, ...args], { maxBuffer: 1024 * 1024 })
  return stdout.trim()
}

function recommendationFor(record, roles) {
  const recommendations = Array.isArray(record?.recommendations) ? record.recommendations : []
  return recommendations.find((item) => roles.includes(String(item?.role || '').trim().toLowerCase())) || null
}
function recommendedAssignment(records, roles, label) {
  for (const record of records) {
    const recommendation = recommendationFor(record, roles)
    const model = String(record?.model || '').trim()
    const thinking = String(recommendation?.thinking || record?.default_thinking || '').trim().toLowerCase()
    if (recommendation && model && thinking) {
      const serving = String(recommendation?.serving || '').trim().toLowerCase()
      return { provider, model, thinking, ...(serving === 'fast' || serving === 'priority' ? { service_tier: 'priority' } : {}) }
    }
  }
  throw new Error(`model catalog has no complete ${label} recommendation for provider ${provider}`)
}

function bindingSourcePath(binding) { return String(binding?.source_workspace_path || binding?.host_workspace_path || '').trim() }
function bindingRuntimePath(binding) { return String(binding?.destination_workspace_path || binding?.runtime_workspace_path || bindingSourcePath(binding)).trim() }
function bindingID(binding) { return String(binding?.workspace_binding_id || binding?.id || '').trim() }
function runtimeID(topology) {
  const runtimes = Array.isArray(topology?.runtimes) ? topology.runtimes : []
  return String((runtimes.find((item) => item?.relationship === 'self') || runtimes[0])?.swarm_id || '').trim()
}
function authorityFor(topology, binding) {
  return {
    workspace_path: bindingSourcePath(binding), host_workspace_path: bindingSourcePath(binding),
    runtime_workspace_path: bindingRuntimePath(binding), workspace_binding_id: bindingID(binding),
    swarm_id: runtimeID(topology), target_kind: 'host', target_relationship: 'self',
  }
}

async function hydrate(sessionID) {
  return (await request('POST', '/v3/sync/hydrate', {
    surface: 'desktop', session_ids: [sessionID],
    history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' },
    resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: true, plan_revisions: false, permission_summaries: true },
    include_active: true,
  }, `hydrate ${sessionID}`)).body || {}
}

async function approvePending(sessionID) {
  const pending = (await request('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=200`, undefined, `permissions ${sessionID}`)).body?.permissions || []
  for (const permission of pending) {
    await request('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/${encodeURIComponent(permission.id)}/resolve`, {
      action: 'allow_once', reason: `${testID}: explicit live-testbench approval`,
    }, `approve ${permission.tool_name || permission.id}`)
  }
  return pending.length
}

function terminal(status) { return ['completed', 'failed', 'cancelled', 'expired', 'interrupted'].includes(String(status || '').toLowerCase()) }
async function waitForSession(sessionID, predicate, label) {
  const deadline = Date.now() + timeoutMs
  let latest = null
  while (Date.now() < deadline) {
    await approvePending(sessionID)
    latest = await hydrate(sessionID)
    if (predicate(latest)) return latest
    await sleep(1000)
  }
  const intents = latest?.run_intents_by_session?.[sessionID] || []
  throw new Error(`${label} timed out; intents=${intents.map((item) => `${item.run_id}:${item.status}`).join(',')}`)
}

async function bootstrap() {
  return (await request('POST', '/v3/sync/bootstrap', {
    surface: 'desktop', selector: { kind: 'recent', global: true, recent: { limit: 100 } }, history: { mode: 'none' },
    resources: { messages: false, events: false, run_intents: false, current_run_state: true, session_view: false, active_plan: true, plan_revisions: false, permission_summaries: true }, include_active: true,
  }, 'bootstrap sessions')).body || {}
}

let repo = ''
let workspaceAdded = false
try {
  if (!token) {
    token = String((await request('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')).body?.token || '').trim()
    assert(token, 'desktop authentication returned no token')
  }

  const providers = (await request('GET', '/v1/providers', undefined, 'list providers')).body?.providers || []
  const providerStatus = providers.find((item) => String(item?.id || '').trim().toLowerCase() === provider)
  assert(providerStatus && providerStatus.runnable !== false, `provider ${provider} is unavailable`)
  const catalog = (await request('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'read model recommendations')).body?.records || []
  const actionAssignment = recommendedAssignment(catalog, ['auto', 'main'], 'auto')
  const planAssignment = recommendedAssignment(catalog, ['plan'], 'plan')
  originalSwarmSettings = (await request('GET', '/v1/agent-model-settings', undefined, 'read Swarm model settings')).body?.agent_model_settings?.swarm || null
  assert(originalSwarmSettings?.action?.model, 'canonical Swarm action model setting is missing')
  await request('PATCH', '/v1/agent-model-settings', { swarm: { action: actionAssignment, plan: planAssignment } }, 'configure live-testbench models')
  settingsChanged = true

  const scratchRoot = String(process.env.TMPDIR || '').trim()
  assert(scratchRoot, 'TMPDIR is required for disposable live-testbench repositories')
  repo = await mkdtemp(path.join(scratchRoot, `${testID}.`))
  await exec('git', ['init', '-b', 'dev', repo])
  await git(repo, 'config', 'user.name', 'Swarm Testbench')
  await git(repo, 'config', 'user.email', 'swarm-testbench@example.invalid')
  await exec('node', ['-e', "require('fs').writeFileSync(process.argv[1], 'base\\n')", path.join(repo, 'README.md')])
  await git(repo, 'add', 'README.md')
  await git(repo, 'commit', '-m', 'testbench base')
  const originalDev = await git(repo, 'rev-parse', 'dev')
  result.repository = { path: repo, original_dev: originalDev }
  result.gates.disposable_repository = true

  const added = (await request('POST', '/v1/workspace/add', { path: repo, name: testID, make_current: false }, 'bind disposable workspace')).body || {}
  workspaceAdded = true
  assert(added.workspace_id && added.local_workspace_binding_id, 'workspace add returned no canonical ids')
  result.gates.workspace_bound = true

  const topology = (await request('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const binding = (topology.workspace_bindings || []).find((item) => bindingSourcePath(item) === repo)
  assert(binding && bindingID(binding), 'disposable workspace has no topology binding')
  const authority = authorityFor(topology, binding)
  const semanticName = `Lane Contract ${testID.slice(-8)}`
  const clientRequestID = `${testID}:routed`
  const prompt = [
    `This is the checked-in ${testID} live testbench.`,
    'First call manage_sessions deploy to create one Auto Swarm session in this same workspace, with worktree_name "Deployed Lane Proof", whose prompt is: "Reply with exactly DEPLOYED and do not use tools."',
    'Then start one staged Task Program with one Coder job. The Coder must create lane-stage-proof.txt containing exactly "session lane integration proof\\n", commit it, and leave clean. Use this current repository and a narrow owned_scope containing only lane-stage-proof.txt.',
    'After the Task Program integrates, create a single session checkpoint if needed and finish it with mark_needs_review so this session is explicitly promotion-ready.',
    'Do not edit, checkout, merge, or advance dev. Do not skip either manage_sessions deploy or the Task Program.',
  ].join(' ')
  const launched = (await request('POST', '/v3/sessions:routed', {
    ...authority, input: prompt, client_request_id: clientRequestID, idempotency_key: clientRequestID,
    agent_name: 'swarm', plan_mode_requested: false, worktree_name: semanticName,
    metadata: { source: 'checked-in-live-testbench', runner_test: 'session-lanes-promotion', runner_test_id: testID },
  }, 'start routed lane session')).body || {}
  const sessionID = String(launched.session_id || '').trim()
  if (sessionID) createdSessionIDs.add(sessionID)
  const immediate = launched.session || {}
  assert(sessionID && immediate.worktree_enabled === true && immediate.worktree_root_path && immediate.worktree_branch, 'routed session did not return a managed lane')
  assert(immediate.worktree_root_path !== repo && immediate.workspace_path !== repo, 'routed session uses captured checkout directly')
  result.sessions.parent = { id: sessionID, worktree_root_path: immediate.worktree_root_path, worktree_branch: immediate.worktree_branch }
  result.gates.routed_lane = true
  assert(String(immediate.worktree_branch).startsWith('agent/') && String(immediate.worktree_branch).includes('lane-contract'), `branch is not Swarm-authored semantic agent lane: ${immediate.worktree_branch}`)
  result.gates.swarm_branch_name = true

  const optOutID = `${testID}:opt-out`
  const optOut = await request('POST', '/v3/sessions:routed', {
    ...authority, input: 'Reply with exactly OPT_OUT_REJECTED and do not use tools.', client_request_id: optOutID, idempotency_key: optOutID,
    agent_name: 'swarm', plan_mode_requested: false, worktree_name: 'Opt Out Probe', worktree_mode: 'off',
  }, 'probe removed worktree opt-out', true)
  if (optOut.ok) {
    const probe = optOut.body?.session || {}
    assert(probe.worktree_enabled === true && probe.worktree_root_path && probe.worktree_root_path !== repo, 'worktree opt-out was honored')
    const probeID = String(optOut.body?.session_id || '').trim()
    if (probeID) createdSessionIDs.add(probeID)
    result.sessions.opt_out_probe = { id: probeID, worktree_root_path: probe.worktree_root_path }
  } else {
    assert(optOut.status === 400, `worktree opt-out failed with unexpected HTTP ${optOut.status}`)
  }
  result.gates.opt_out_absent = true

  const settled = await waitForSession(sessionID, (snapshot) => {
    const intents = snapshot.run_intents_by_session?.[sessionID] || []
    const plan = snapshot.session_views_by_id?.[sessionID]?.active_plan
    const allTerminal = intents.length > 0 && intents.every((intent) => terminal(intent.status))
    const needsReview = String(plan?.document?.execution_state?.status || plan?.execution_state?.status || '').toLowerCase().includes('review')
    return allTerminal && needsReview
  }, 'session lane workflow')
  const parent = settled.sessions_by_id?.[sessionID] || immediate
  const laneHead = await git(parent.worktree_root_path, 'rev-parse', 'HEAD')
  assert(laneHead !== originalDev, 'Task Program did not advance the session lane')
  assert(await git(parent.worktree_root_path, 'status', '--porcelain') === '', 'session lane is dirty after Task Program')
  assert(await git(repo, 'rev-parse', 'dev') === originalDev, 'dev advanced during routed/deploy/Task Program work')
  result.repository.lane_head = laneHead
  result.gates.task_stage_integrated_to_lane = true
  result.gates.dev_unchanged_before_promotion = true

  const sessions = await bootstrap()
  const deployed = Object.values(sessions.sessions_by_id || {}).find((item) =>
    item?.id !== sessionID && item?.metadata?.parent_session_id === sessionID && item?.metadata?.lineage_kind === 'session_deploy')
  assert(deployed?.worktree_enabled === true && deployed?.worktree_root_path && deployed.worktree_root_path !== parent.worktree_root_path && deployed.worktree_root_path !== repo, 'manage_sessions deploy child has no distinct managed lane')
  createdSessionIDs.add(deployed.id)
  result.sessions.deployed = { id: deployed.id, worktree_root_path: deployed.worktree_root_path, worktree_branch: deployed.worktree_branch }
  result.gates.deployed_lane = true

  const targetBranch = String(parent.worktree_base_branch || '').trim()
  assert(targetBranch === 'dev', `routed lane captured target branch ${targetBranch}, want dev`)
  const promotion = {
    workspace_path: repo, promote_session_ids: [sessionID], source_head_by_session_id: { [sessionID]: laneHead },
    target_branch: targetBranch, target_head: originalDev,
  }
  const automatic = await request('POST', '/v3/sessions:review-worktrees', { ...promotion, automatic: true }, 'reject automatic promotion', true)
  assert(automatic.status === 400 && /explicit user action/i.test(automatic.text), `automatic promotion was not explicitly rejected: HTTP ${automatic.status} ${automatic.text}`)
  assert(await git(repo, 'rev-parse', 'dev') === originalDev, 'automatic promotion advanced dev')
  result.gates.automatic_promotion_rejected = true

  await request('POST', '/v3/sessions:review-worktrees', promotion, 'explicit user-approved promotion')
  const promotedHead = await git(repo, 'rev-parse', 'dev')
  const [promotedTree, laneTree] = await Promise.all([
    git(repo, 'rev-parse', `${promotedHead}^{tree}`),
    git(repo, 'rev-parse', `${laneHead}^{tree}`),
  ])
  assert(promotedHead !== originalDev && promotedTree === laneTree, `explicit promotion head=${promotedHead} tree=${promotedTree}, want lane tree=${laneTree}`)
  result.repository.promoted_dev = promotedHead
  result.gates.explicit_promotion_advanced_dev = true
  result.result = 'PASS'
} catch (error) {
  result.failures.push(error?.stack || String(error))
  log(result.failures[result.failures.length - 1])
} finally {
  const cleanupFailures = []
  for (const sessionID of createdSessionIDs) {
    try {
      const response = await request('DELETE', `/v3/sessions/${encodeURIComponent(sessionID)}`, undefined, `delete test session ${sessionID}`, true)
      if (!response.ok && response.status !== 404) cleanupFailures.push(`delete session ${sessionID}: HTTP ${response.status}`)
    } catch (error) { cleanupFailures.push(`delete session ${sessionID}: ${error?.message || error}`) }
  }
  if (settingsChanged && originalSwarmSettings) {
    try { await request('PATCH', '/v1/agent-model-settings', { swarm: originalSwarmSettings }, 'restore Swarm model settings') } catch (error) {
      cleanupFailures.push(`restore Swarm model settings: ${error?.message || error}`)
    }
  }
  if (workspaceAdded && repo) {
    try {
      const response = await request('DELETE', `/v1/worktrees?workspace_path=${encodeURIComponent(repo)}`, undefined, 'prune managed test worktrees', true)
      if (!response.ok) cleanupFailures.push(`prune managed test worktrees: HTTP ${response.status}`)
    } catch (error) { cleanupFailures.push(`prune managed test worktrees: ${error?.message || error}`) }
    try {
      const response = await request('POST', '/v1/workspace/delete', { path: repo }, 'remove disposable workspace binding', true)
      if (!response.ok) cleanupFailures.push(`remove disposable workspace binding: HTTP ${response.status}`)
    } catch (error) { cleanupFailures.push(`remove disposable workspace binding: ${error?.message || error}`) }
  }
  if (repo) {
    try { await rm(repo, { recursive: true, force: true }) } catch (error) { cleanupFailures.push(`remove disposable repository: ${error?.message || error}`) }
  }
  result.failures.push(...cleanupFailures)
  result.gates.cleanup = cleanupFailures.length === 0
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((gate) => result.gates[gate] !== true)
  if (result.failed_gates.length > 0) result.result = 'NOT_DONE'
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
