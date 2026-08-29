#!/usr/bin/env node
import crypto from 'node:crypto'
import { execFile } from 'node:child_process'
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises'
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
if (!/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is required')
if (!Number.isFinite(timeoutMs) || timeoutMs < 30000) throw new Error('--timeout-ms must be at least 30000')

const stageTimeoutMs = Math.min(timeoutMs, 9 * 60 * 1000)
const heartbeatEveryMs = 15000
const stallTimeoutMs = Math.min(stageTimeoutMs - 5000, 3 * 60 * 1000)
const testID = `task-program-worktrees-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const tokens = {
  alpha: `ALPHA_${crypto.randomBytes(6).toString('hex').toUpperCase()}`,
  beta: `BETA_${crypto.randomBytes(6).toString('hex').toUpperCase()}`,
  integrated: `INTEGRATED_${crypto.randomBytes(6).toString('hex').toUpperCase()}`,
}
const requiredGates = [
  'provider_runnable', 'models_configured', 'subagent_policy_configured', 'disposable_repository',
  'workspace_bound', 'parent_managed_lane', 'foundation_coders_isolated', 'dependent_coder_saw_integrated_inputs',
  'three_committed_coder_handoffs', 'parent_lane_integrated_clean', 'source_dev_unchanged',
  'no_permission_requests', 'bounded_step_evidence', 'cleanup', 'settings_restored',
]
const result = {
  result: 'NOT_DONE', test: 'task-program-worktree-permissions', test_id: testID,
  provider, tokens, gates: {}, steps: [], sessions: {}, repository: {}, permission_records: [], failures: [],
}
let repo = ''
let workspaceAdded = false
let parentSessionID = ''
let originalSettings = null
let originalSubagentPolicy = null
let settingsChanged = false
let policyChanged = false

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[task-program-worktrees] ${message}\n`)
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

async function git(target, ...args) {
  const { stdout } = await exec('git', ['-C', target, ...args], { maxBuffer: 1024 * 1024 })
  return stdout.trim()
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

async function bootstrapSessions() {
  return (await request('POST', '/v3/sync/bootstrap', {
    surface: 'desktop', selector: { kind: 'global', global: true, recent: { limit: 200 } },
    history: { mode: 'none' },
    resources: { messages: false, events: false, run_intents: false, current_run_state: true, active_plan: true, plan_revisions: false, permission_summaries: true },
    include_active: true,
  }, 'bootstrap delegated sessions')).body || {}
}

async function hydrate(sessionID) {
  return (await request('POST', '/v3/sync/hydrate', {
    surface: 'desktop', session_ids: [sessionID],
    history: { mode: 'full', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' },
    resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: true, plan_revisions: true, permission_summaries: true },
    include_active: true,
  }, `hydrate ${sessionID}`)).body || {}
}

function childSessions(bootstrap, parentID) {
  return Object.values(bootstrap.sessions_by_id || {}).filter((session) =>
    String(session?.metadata?.parent_session_id || '') === parentID &&
    String(session?.metadata?.lineage_kind || '') === 'delegated_subagent' &&
    String(session?.metadata?.requested_subagent || '').toLowerCase() === 'coder')
}

function failureEvidence(snapshot, sessionID) {
  const intents = snapshot.run_intents_by_session?.[sessionID] || []
  const failed = intents.filter((intent) => ['failed', 'cancelled', 'expired', 'interrupted'].includes(String(intent?.status || '').toLowerCase()))
  const plan = snapshot.session_views_by_id?.[sessionID]?.active_plan || snapshot.active_plans_by_session?.[sessionID] || {}
  const executionStatus = String(plan?.document?.execution_state?.status || plan?.execution_state?.status || '').toLowerCase()
  return { intents, failed, executionStatus }
}

function progressSignature(snapshot, bootstrap, sessionID) {
  const evidence = failureEvidence(snapshot, sessionID)
  const children = childSessions(bootstrap, sessionID)
  return JSON.stringify({
    intents: evidence.intents.map((intent) => [intent.run_id, intent.status]),
    execution_status: evidence.executionStatus,
    children: children.map((child) => [child.id, child.metadata?.task_program_job_id, child.metadata?.runtime_state, child.worktree_branch]).sort(),
  })
}

async function runStep(label, predicate) {
  const started = Date.now()
  const deadline = started + stageTimeoutMs
  let nextHeartbeat = 0
  let lastProgressAt = started
  let lastSignature = ''
  let terminalWithoutEvidenceSince = 0
  let latest = null
  let latestBootstrap = null
  while (Date.now() < deadline) {
    latest = await hydrate(parentSessionID)
    latestBootstrap = await bootstrapSessions()
    const evidence = failureEvidence(latest, parentSessionID)
    if (evidence.failed.length > 0 || evidence.executionStatus === 'failed' || evidence.executionStatus === 'blocked') {
      throw new Error(`${label} failed: intents=${evidence.intents.map((item) => `${item.run_id}:${item.status}`).join(',')} plan=${evidence.executionStatus}`)
    }
    const pending = (await request('GET', `/v3/sessions/${encodeURIComponent(parentSessionID)}/permissions?status=pending&limit=200`, undefined, `${label} pending permissions`)).body?.permissions || []
    const children = childSessions(latestBootstrap, parentSessionID)
    for (const child of children) {
      const childPending = (await request('GET', `/v3/sessions/${encodeURIComponent(child.id)}/permissions?status=pending&limit=200`, undefined, `${label} child permissions ${child.id}`)).body?.permissions || []
      pending.push(...childPending)
    }
    if (pending.length > 0) throw new Error(`${label} requested permission instead of using default in-worktree authority: ${pending.map((item) => `${item.session_id || '?'}:${item.tool_name || item.id}`).join(', ')}`)
    if (await predicate(latest, latestBootstrap, children)) {
      result.steps.push({ label, elapsed_ms: Date.now() - started, child_count: children.length, execution_status: evidence.executionStatus || 'running' })
      log(`${label}: passed in ${Date.now() - started}ms`)
      return { snapshot: latest, bootstrap: latestBootstrap, children }
    }
    const allTerminal = evidence.intents.length > 0 && evidence.intents.every((intent) => terminal(intent.status))
    if (allTerminal || evidence.executionStatus === 'waiting_review') {
      if (!terminalWithoutEvidenceSince) terminalWithoutEvidenceSince = Date.now()
      if (Date.now() - terminalWithoutEvidenceSince >= 10000) {
        throw new Error(`${label} reached terminal state without required evidence: ${progressSignature(latest, latestBootstrap, parentSessionID)}`)
      }
    } else {
      terminalWithoutEvidenceSince = 0
    }
    const signature = progressSignature(latest, latestBootstrap, parentSessionID)
    if (signature !== lastSignature) {
      lastSignature = signature
      lastProgressAt = Date.now()
    } else if (Date.now() - lastProgressAt >= stallTimeoutMs) {
      throw new Error(`${label} stalled for ${stallTimeoutMs}ms without durable state progress: ${signature}`)
    }
    if (Date.now() >= nextHeartbeat) {
      log(`${label}: waiting; children=${children.length} ${signature}`)
      nextHeartbeat = Date.now() + heartbeatEveryMs
    }
    await sleep(1000)
  }
  throw new Error(`${label} timed out after ${stageTimeoutMs}ms`)
}

function taskOutputs(snapshot, sessionID) {
  const messages = snapshot.messages_by_session?.[sessionID] || []
  const outputs = []
  for (const message of messages) {
    if (message?.role !== 'tool') continue
    const envelope = decodeObject(message.content)
    const toolName = String(envelope.tool_name || envelope.tool || '')
    if (toolName !== 'task') continue
    const output = decodeObject(envelope.output || envelope.completed_output)
    outputs.push(output)
  }
  return outputs
}

async function allPermissions(sessionIDs) {
  const records = []
  for (const sessionID of sessionIDs) {
    const listed = (await request('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=all&limit=500`, undefined, `all permissions ${sessionID}`)).body?.permissions || []
    records.push(...listed.map((record) => ({ session_id: sessionID, id: record.id, tool_name: record.tool_name, status: record.status })))
  }
  return records
}

async function exists(target) {
  try { await stat(target); return true } catch { return false }
}

try {
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
  let coderAssignment
  try { coderAssignment = recommendedAssignment(catalog, ['coder'], 'Coder') } catch { coderAssignment = actionAssignment }
  originalSettings = (await request('GET', '/v1/agent-model-settings', undefined, 'read agent model settings')).body?.agent_model_settings || null
  assert(originalSettings?.swarm?.action?.model && originalSettings?.system_agents?.coder?.model, 'canonical Swarm/Coder model settings are missing')
  await request('PATCH', '/v1/agent-model-settings', { swarm: { action: actionAssignment, plan: planAssignment } }, 'configure Swarm testbench models')
  await request('PATCH', '/v1/agent-model-settings', { system_agents: { coder: coderAssignment } }, 'configure Coder testbench model')
  settingsChanged = true
  result.gates.models_configured = true

  originalSubagentPolicy = (await request('GET', '/v1/permissions/subagents', undefined, 'read subagent policy')).body?.subagents || null
  assert(originalSubagentPolicy, 'subagent policy is missing')
  await request('PUT', '/v1/permissions/subagents', {
    mode: 'bounded', automatic_launches_per_parent_run: 5, active_child_limit: 10,
    swarm_active_child_limit: 50, over_budget_action: 'ask', require_write_isolation: true,
  }, 'configure bounded isolated Coder policy')
  policyChanged = true
  result.gates.subagent_policy_configured = true

  const scratchRoot = String(process.env.TMPDIR || '').trim()
  assert(scratchRoot, 'TMPDIR is required for the disposable Task Program repository')
  repo = await mkdtemp(path.join(scratchRoot, `${testID}.`))
  await exec('git', ['init', '-b', 'dev', repo])
  await git(repo, 'config', 'user.name', 'Swarm Testbench')
  await git(repo, 'config', 'user.email', 'swarm-testbench@example.invalid')
  await exec('node', ['-e', "require('fs').writeFileSync(process.argv[1], '# Task Program worktree permission proof\\n')", path.join(repo, 'README.md')])
  await git(repo, 'add', 'README.md')
  await git(repo, 'commit', '-m', 'testbench base')
  const originalDev = await git(repo, 'rev-parse', 'dev')
  result.repository = { path: repo, original_dev: originalDev }
  result.gates.disposable_repository = true

  const added = (await request('POST', '/v1/workspace/add', { path: repo, name: testID, make_current: false }, 'bind disposable workspace')).body || {}
  workspaceAdded = true
  assert(added.workspace_id && added.local_workspace_binding_id, 'workspace add returned no canonical identities')
  result.gates.workspace_bound = true
  const topology = (await request('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const runtime = (topology.runtimes || []).find((item) => item?.relationship === 'self') || (topology.runtimes || [])[0]
  assert(runtime?.swarm_id, 'topology has no self runtime')

  const created = (await request('POST', '/v3/sessions', {
    client_request_id: `${testID}:create`, title: `${testID} nested Task Program`,
    workspace_path: repo, workspace_name: testID, workspace_binding_id: added.local_workspace_binding_id,
    swarm_id: runtime.swarm_id, target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: 'swarm',
    preference: actionAssignment, model_profile: { use_account_default: true },
    worktree_mode: 'on', worktree_base_branch: 'dev', worktree_branch_name: `agent/${testID}`,
    metadata: { source: 'checked-in-live-testbench', runner_test: 'task-program-worktree-permissions', runner_test_id: testID },
  }, 'create parent managed worktree session')).body || {}
  parentSessionID = String(created.session?.id || '').trim()
  const parent = created.session || {}
  assert(parentSessionID && parent.worktree_enabled === true && parent.worktree_root_path && parent.worktree_branch, 'parent session did not receive a managed worktree')
  assert(parent.worktree_root_path !== repo, 'parent session worktree root is the captured source checkout')
  assert(String(parent.workspace_path || '') === repo, 'parent session lost canonical source workspace identity')
  assert(parent.worktree_branch !== 'dev' && parent.worktree_branch !== parent.worktree_base_branch, 'parent session is running on its base branch')
  result.sessions.parent = { id: parentSessionID, worktree_root_path: parent.worktree_root_path, worktree_branch: parent.worktree_branch }
  result.gates.parent_managed_lane = true

  const identitySuffix = crypto.createHash('sha256').update(testID).digest('hex').slice(0, 16)
  const programID = `worktree_permission_${identitySuffix}`
  const planID = `worktree-permission-plan-${identitySuffix}`
  const document = {
    id: planID, title: `${testID} Task Program worktree permission proof`,
    info: { goal: 'Prove a dependent three-Coder Task Program stays isolated and uses no routine permission prompts.' },
    execution_policy: { mode: 'automatic', shape: 'checkpointed' }, active_checkpoint_id: 'cp-task-program',
    checkpoints: [{
      id: 'cp-task-program', title: 'Run isolated nested Task Program', status: 'pending',
      tasks: [
        'Start the approved task_program with task action=start and no program argument.',
        'When the Task Program returns completed, call plan_manage complete_checkpoint with report TASK_PROGRAM_WORKTREE_PERMISSION_OK. Do not call Bash, manage_worktree, ask_user, or any permission-management tool.',
      ],
      acceptance_criteria: [
        'Three Coders create clean commits in distinct managed worktrees.',
        'The dependent Coder reads the two integrated foundation outputs in its own later-stage worktree.',
        'The parent managed lane integrates the complete program while the source dev branch remains unchanged.',
        'No parent or child routine tool call creates a permission request.',
      ],
      task_program: {
        id: programID,
        stages: [
          { id: 'foundation', dependency_evidence: 'The two file outputs are independent and use non-overlapping scopes.' },
          { id: 'verification', depends_on: ['foundation'], dependency_evidence: 'The verifier requires both foundation commits integrated into the parent lane.' },
        ],
        jobs: [
          {
            id: 'alpha', stage_id: 'foundation', agent_type: 'coder', title: 'Alpha Worktree Commit',
            meta_prompt: `Using only routine Coder tools in your allocated worktree, create alpha.txt containing exactly ${tokens.alpha} followed by a newline. Inspect status, stage, commit with message "test: add alpha proof", verify clean status, and finish. Never ask for permission or access the source checkout.`,
            deliverable: 'Clean committed alpha.txt', owned_scope: ['alpha.txt'], dependency_evidence: 'Independent file output.',
            acceptance_criteria: [`alpha.txt contains ${tokens.alpha} and the child worktree is clean and committed.`],
          },
          {
            id: 'beta', stage_id: 'foundation', agent_type: 'coder', title: 'Beta Worktree Commit',
            meta_prompt: `Using only routine Coder tools in your allocated worktree, create nested/beta.txt containing exactly ${tokens.beta} followed by a newline. Inspect status, stage, commit with message "test: add beta proof", verify clean status, and finish. Never ask for permission or access the source checkout.`,
            deliverable: 'Clean committed nested/beta.txt', owned_scope: ['nested/beta.txt'], dependency_evidence: 'Independent file output.',
            acceptance_criteria: [`nested/beta.txt contains ${tokens.beta} and the child worktree is clean and committed.`],
          },
          {
            id: 'verifier', stage_id: 'verification', depends_on: ['alpha', 'beta'], agent_type: 'coder', title: 'Integrated Worktree Verifier',
            meta_prompt: `This later-stage worktree must already contain the integrated prior Coder outputs. Read alpha.txt and nested/beta.txt from your authoritative allocated worktree and verify the exact tokens ${tokens.alpha} and ${tokens.beta}. Then create integration.txt with exactly three lines: ${tokens.alpha}, ${tokens.beta}, and ${tokens.integrated}. Stage, commit with message "test: verify integrated worktrees", verify clean status, and finish. Never ask for permission, use the source checkout, or request workspace expansion.`,
            deliverable: 'Clean committed integration.txt proving prior integrated work is visible', owned_scope: ['integration.txt'],
            dependency_evidence: 'Both foundation jobs are integrated before this stage.',
            acceptance_criteria: [`integration.txt contains all three exact tokens and the child worktree is clean and committed.`],
          },
        ],
      },
    }],
  }
  await request('POST', `/v3/sessions/${encodeURIComponent(parentSessionID)}/plans`, {
    plan_id: planID, title: document.title, document, status: 'approved', approval_state: 'approved', activate: true,
  }, 'save approved Task Program plan')
  const started = (await request('POST', `/v3/sessions/${encodeURIComponent(parentSessionID)}/plan-mode/checkpoints/cp-task-program/start`, { plan_id: planID }, 'start Task Program checkpoint')).body || {}
  assert(started.run_start?.run_intent?.run_id || started.run_intent?.run_id, 'checkpoint start returned no run id')

  await runStep('step 1: foundation Coder lanes allocated', async (_snapshot, _bootstrap, children) => {
    if (children.length < 2) return false
    const roots = new Set(children.map((child) => String(child.worktree_root_path || '')))
    return roots.size >= 2 && children.every((child) => child.worktree_enabled === true && child.worktree_root_path && child.worktree_branch && child.worktree_root_path !== repo && child.worktree_root_path !== parent.worktree_root_path && child.worktree_branch !== 'dev')
  })
  result.gates.foundation_coders_isolated = true

  const verifierStep = await runStep('step 2: dependent Coder sees integrated prior outputs', async (_snapshot, _bootstrap, children) => {
    for (const child of children) {
      if (!child?.worktree_root_path || !(await exists(child.worktree_root_path))) continue
      const alpha = await readFile(path.join(child.worktree_root_path, 'alpha.txt'), 'utf8').catch(() => '')
      const beta = await readFile(path.join(child.worktree_root_path, 'nested', 'beta.txt'), 'utf8').catch(() => '')
      if (alpha.trim() === tokens.alpha && beta.trim() === tokens.beta) return true
    }
    return false
  })
  let verifier = null
  for (const child of verifierStep.children) {
    if (!child?.worktree_root_path || !(await exists(child.worktree_root_path))) continue
    const alpha = await readFile(path.join(child.worktree_root_path, 'alpha.txt'), 'utf8').catch(() => '')
    const beta = await readFile(path.join(child.worktree_root_path, 'nested', 'beta.txt'), 'utf8').catch(() => '')
    if (alpha.trim() === tokens.alpha && beta.trim() === tokens.beta) { verifier = child; break }
  }
  assert(verifier, 'dependent Coder worktree with both integrated inputs was not found')
  assert(verifier.worktree_root_path !== repo && verifier.worktree_root_path !== parent.worktree_root_path && verifier.worktree_branch !== 'dev', 'dependent Coder did not receive a distinct managed worktree')
  result.gates.dependent_coder_saw_integrated_inputs = true

  const completed = await runStep('step 3: Task Program and checkpoint complete', async (snapshot) => {
    const outputs = taskOutputs(snapshot, parentSessionID)
    const completedProgram = outputs.find((output) => output?.program_state === 'completed' || output?.status === 'completed')
    const intents = snapshot.run_intents_by_session?.[parentSessionID] || []
    const allTerminal = intents.length > 0 && intents.every((intent) => terminal(intent.status))
    return Boolean(completedProgram) && allTerminal
  })
  const finalChildren = childSessions(completed.bootstrap, parentSessionID)
  assert(finalChildren.length === 3, `Task Program created ${finalChildren.length} Coder sessions, want 3`)
  const finalTaskOutput = taskOutputs(completed.snapshot, parentSessionID).find((output) => output?.program_state === 'completed' || output?.status === 'completed') || {}
  const launches = Array.isArray(finalTaskOutput.launches) ? finalTaskOutput.launches : []
  assert(launches.length === 3, `Task Program completed with ${launches.length} handoffs, want 3`)
  assert(launches.every((launch) => launch.worktree_enabled === true && launch.worktree_clean === true && launch.base_commit && launch.head_commit && launch.base_commit !== launch.head_commit), `Task Program has an uncommitted or non-isolated Coder handoff: ${JSON.stringify(launches)}`)
  for (const child of finalChildren) {
    assert(child.worktree_enabled === true && child.worktree_root_path !== repo && child.worktree_root_path !== parent.worktree_root_path && child.worktree_branch !== 'dev' && child.worktree_branch !== child.worktree_base_branch, `Coder child escaped isolation: ${JSON.stringify(child)}`)
    const branchHead = await git(repo, 'rev-parse', child.worktree_branch)
    assert(branchHead, `Coder branch ${child.worktree_branch} has no commit`)
  }
  result.sessions.children = finalChildren.map((child) => ({ id: child.id, worktree_root_path: child.worktree_root_path, worktree_branch: child.worktree_branch }))
  result.gates.three_committed_coder_handoffs = true

  const parentView = completed.snapshot.sessions_by_id?.[parentSessionID] || parent
  const parentRoot = String(parentView.worktree_root_path || parent.worktree_root_path)
  assert(await git(parentRoot, 'status', '--porcelain') === '', 'parent managed lane is dirty after Task Program integration')
  assert((await readFile(path.join(parentRoot, 'alpha.txt'), 'utf8')).trim() === tokens.alpha, 'parent lane missing alpha output')
  assert((await readFile(path.join(parentRoot, 'nested', 'beta.txt'), 'utf8')).trim() === tokens.beta, 'parent lane missing beta output')
  const integratedLines = (await readFile(path.join(parentRoot, 'integration.txt'), 'utf8')).trim().split('\n')
  assert(JSON.stringify(integratedLines) === JSON.stringify([tokens.alpha, tokens.beta, tokens.integrated]), `parent integration.txt mismatch: ${JSON.stringify(integratedLines)}`)
  result.repository.parent_head = await git(parentRoot, 'rev-parse', 'HEAD')
  assert(result.repository.parent_head !== originalDev, 'Task Program did not advance the managed parent lane')
  result.gates.parent_lane_integrated_clean = true
  assert(await git(repo, 'rev-parse', 'dev') === originalDev, 'Task Program advanced the captured source dev branch')
  assert(await git(repo, 'status', '--porcelain') === '', 'captured source checkout became dirty')
  result.gates.source_dev_unchanged = true

  result.permission_records = await allPermissions([parentSessionID, ...finalChildren.map((child) => child.id)])
  assert(result.permission_records.length === 0, `routine parent/child actions created permission records: ${JSON.stringify(result.permission_records)}`)
  const allEvents = completed.snapshot.events_by_session?.[parentSessionID] || []
  assert(!allEvents.some((event) => String(event?.event_type || '') === 'permission.requested'), 'parent Task Program emitted permission.requested')
  result.gates.no_permission_requests = true
  assert(result.steps.length === 3 && result.steps.every((step) => step.elapsed_ms < stageTimeoutMs), `bounded step evidence is incomplete: ${JSON.stringify(result.steps)}`)
  result.gates.bounded_step_evidence = true
  result.result = 'PASS'
} catch (error) {
  result.failures.push(error?.stack || String(error))
  log(result.failures[result.failures.length - 1])
} finally {
  const cleanupFailures = []
  if (parentSessionID) {
    try {
      const children = childSessions(await bootstrapSessions(), parentSessionID)
      for (const child of children) {
        const response = await request('DELETE', `/v3/sessions/${encodeURIComponent(child.id)}`, undefined, `delete child test session ${child.id}`, true)
        if (!response.ok && response.status !== 404 && response.status !== 409) cleanupFailures.push(`delete child session ${child.id}: HTTP ${response.status}`)
      }
      const response = await request('DELETE', `/v3/sessions/${encodeURIComponent(parentSessionID)}`, undefined, 'delete parent test session', true)
      if (!response.ok && response.status !== 404 && response.status !== 409) cleanupFailures.push(`delete parent session: HTTP ${response.status}`)
    } catch (error) { cleanupFailures.push(`delete test sessions: ${error?.message || error}`) }
  }
  if (workspaceAdded && repo) {
    try {
      const response = await request('DELETE', `/v1/worktrees?workspace_path=${encodeURIComponent(repo)}`, undefined, 'prune test worktrees', true)
      if (!response.ok) cleanupFailures.push(`prune test worktrees: HTTP ${response.status}`)
    } catch (error) { cleanupFailures.push(`prune test worktrees: ${error?.message || error}`) }
    try {
      const response = await request('POST', '/v1/workspace/delete', { path: repo }, 'remove disposable workspace binding', true)
      if (!response.ok) cleanupFailures.push(`remove workspace binding: HTTP ${response.status}`)
    } catch (error) { cleanupFailures.push(`remove workspace binding: ${error?.message || error}`) }
  }
  if (repo) {
    try { await rm(repo, { recursive: true, force: true }) } catch (error) { cleanupFailures.push(`remove disposable repository: ${error?.message || error}`) }
  }
  result.gates.cleanup = cleanupFailures.length === 0
  if (policyChanged && originalSubagentPolicy) {
    try { await request('PUT', '/v1/permissions/subagents', originalSubagentPolicy, 'restore subagent policy') } catch (error) { cleanupFailures.push(`restore subagent policy: ${error?.message || error}`) }
  }
  if (settingsChanged && originalSettings) {
    try {
      await request('PATCH', '/v1/agent-model-settings', { swarm: originalSettings.swarm }, 'restore Swarm model settings')
      await request('PATCH', '/v1/agent-model-settings', { system_agents: { coder: originalSettings.system_agents.coder } }, 'restore Coder model setting')
    } catch (error) { cleanupFailures.push(`restore model settings: ${error?.message || error}`) }
  }
  result.gates.settings_restored = cleanupFailures.filter((item) => item.includes('restore')).length === 0
  result.failures.push(...cleanupFailures)
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((gate) => result.gates[gate] !== true)
  if (result.failed_gates.length > 0) result.result = 'NOT_DONE'
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
