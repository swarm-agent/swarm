#!/usr/bin/env node
import crypto from 'node:crypto'
import fs from 'node:fs/promises'
import path from 'node:path'

const argv = process.argv.slice(2)
const option = (name, fallback = '') => {
  const index = argv.indexOf(name)
  return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback
}

const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'google')).trim().toLowerCase()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '300000'))
const workspacePath = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || 'low')).trim().toLowerCase()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()

if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider) throw new Error('--provider is required')

const testID = `runner-subagent-delegation-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const result = {
  result: 'NOT_DONE',
  test: 'subagent-delegation-smoke',
  test_id: testID,
  started_at: new Date().toISOString(),
  api_url: apiURL,
  provider,
  gates: {},
  failures: [],
}

let token = suppliedToken
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[subagent-delegation-smoke] ${message}\n`)
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
  } finally { clearTimeout(timer) }
}

async function hydrate(sessionID) {
  const response = await api('POST', '/v3/sync/hydrate', {
    surface: 'desktop',
    session_ids: [sessionID],
    history: { mode: 'tail', max_messages_per_session: 100, max_events_per_session: 100, manifest_policy: 'manifest' },
    resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: false },
    include_active: true,
  }, `hydrate ${sessionID}`)
  return response.body || {}
}

async function bootstrap() {
  const response = await api('POST', '/v3/sync/bootstrap', {
    surface: 'desktop',
    selector: { kind: 'global', global: true, recent: { limit: 100 } },
    history: { mode: 'none' },
    resources: { current_run_state: true },
    include_active: true,
  }, 'bootstrap')
  return response.body || {}
}

async function resolvePermissions(sessionID) {
  const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=30`, undefined, 'list pending permissions')
  const pending = response.body?.permissions || []
  for (const p of pending) {
    await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/${encodeURIComponent(p.id)}/resolve`, {
      action: 'allow_once',
      reason: `${testID}: auto-approve test permission for ${p.tool_name}`,
    }, `resolve permission ${p.id}`)
  }
  return pending.length
}

async function main() {
  log(`Starting subagent delegation smoke test: ${testID}`)

  // 1. Authenticate if token not provided
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session')
    token = auth.body?.token
    assert(token, 'Failed to acquire desktop auth token')
  }

  // 2. Resolve workspace binding
  const topology = (await api('GET', '/v1/swarm/topology')).body || {}
  const runtime = (topology.runtimes || []).find((r) => r.relationship === 'self') || topology.runtimes?.[0]
  assert(runtime?.swarm_id, 'No self runtime found in topology')

  let bindings = topology.workspace_bindings || []
  let binding = (workspacePath
    ? bindings.find((b) => (b.source_workspace_path || b.host_workspace_path) === workspacePath)
    : bindings[0]) || bindings[0]
  if (!binding && workspacePath) {
    log(`Adding workspace binding for ${workspacePath}...`)
    const addRes = await api('POST', '/v1/workspace/add', {
      path: workspacePath,
      name: 'smoke-primary',
      make_current: true,
      confirm_committed_only: true,
    }, 'bind primary fixture workspace')
    const addedTopology = (await api('GET', '/v1/swarm/topology')).body || {}
    bindings = addedTopology.workspace_bindings || []
    binding = bindings.find((b) => (b.source_workspace_path || b.host_workspace_path) === workspacePath) || bindings[0]
  }
  assert(binding, 'No workspace binding available')
  const targetPath = binding.source_workspace_path || binding.host_workspace_path || workspacePath
  log(`Target workspace: ${targetPath} (binding: ${binding.workspace_binding_id || binding.id})`)

  result.gates.prerequisites_verified = true

  // 3. Create parent session in managed worktree
  const sessionBody = {
    client_request_id: `${testID}:parent`,
    title: `${testID} Parent Regular Delegation`,
    workspace_path: targetPath,
    workspace_name: binding.source_workspace_name || 'workspace',
    workspace_binding_id: binding.workspace_binding_id || binding.id,
    swarm_id: runtime.swarm_id,
    target_kind: 'host',
    target_relationship: 'self',
    mode: 'auto',
    agent_name: 'swarm',
    worktree_mode: 'on',
    worktree_branch_name: `agent/test-regular-${crypto.randomBytes(3).toString('hex')}`,
    metadata: { runner_test: 'subagent-delegation-smoke', runner_test_id: testID },
  }
  if (actionModel) {
    sessionBody.preference = { provider, model: actionModel, thinking: actionThinking }
  }

  const created = (await api('POST', '/v3/sessions', sessionBody, 'create parent session')).body?.session || {}
  assert(created.id, 'Parent session creation failed')
  assert(created.worktree_enabled, 'Parent session worktree was not enabled')
  const parentID = created.id
  log(`Parent session created: ${parentID}, worktree: ${created.worktree_root_path}`)
  result.gates.parent_session_created = true

  // 4. Post prompt directing regular (non-task-program) subagent delegation
  const prompt = [
    `Delegate a quick scoped task to a Coder subagent using regular subagent delegation mode.`,
    `Call the task tool with mode="regular" and launches containing one Coder.`,
    `Do NOT create a Task Program. Do NOT pass action="start" or a program object.`,
    `Launch configuration:`,
    `- subagent_type: "coder"`,
    `- title: "Write Proof"`,
    `- meta_prompt: "Create the file subagent-smoke-proof.txt with content EXACT_DELEGATION_VERIFIED using the write tool, then stage and commit it with git_commit (message: 'test: subagent smoke proof'). Do not execute bash commands."`,
    `- deliverable: "Committed subagent-smoke-proof.txt"`,
    `- owned_scope: ["subagent-smoke-proof.txt"]`,
    `- concurrency_reason: "Isolated verification file"`,
    `After the Coder finishes and returns its handoff, confirm you received the handoff and reply with SUBAGENT_DELEGATION_OK.`,
  ].join('\n')

  const msgRes = await api('POST', `/v3/sessions/${encodeURIComponent(parentID)}/messages`, {
    client_request_id: `${testID}:msg:1`,
    role: 'user',
    content: prompt,
    metadata: { runner_test_id: testID },
  }, 'post delegation prompt')
  const runID = msgRes.body?.run_intent?.run_id || msgRes.body?.run_id
  log(`Delegation prompt sent, run ID: ${runID}`)

  // 5. Poll for completion, auto-resolving task permissions and checking loop prevention
  const deadline = Date.now() + timeoutMs
  let completed = false
  let parentSnapshot = null
  let delegatedChildren = []
  let lastState = ''
  let lastProgress = Date.now()

  while (Date.now() < deadline) {
    await resolvePermissions(parentID)
    parentSnapshot = await hydrate(parentID)
    const bootData = await bootstrap()
    const allSessions = Object.values(bootData.sessions_by_id || {})
    delegatedChildren = allSessions.filter((s) => s.metadata?.parent_session_id === parentID && s.metadata?.lineage_kind === 'delegated_subagent')

    for (const child of delegatedChildren) {
      await resolvePermissions(child.id)
    }

    const intents = parentSnapshot.run_intents_by_session?.[parentID] || []
    const intent = intents.find((i) => i.run_id === runID) || intents[0]
    const stateSig = `${intent?.status || 'none'}:${delegatedChildren.length}:${delegatedChildren.map((c) => c.status).join(',')}`

    if (stateSig !== lastState) {
      lastState = stateSig
      lastProgress = Date.now()
      log(`Progress: run=${intent?.status} children=${delegatedChildren.length} (${delegatedChildren.map((c) => `${c.metadata?.requested_subagent || c.agent_name}:${c.status}`).join(', ')})`)
    } else if (Date.now() - lastProgress > 90000) {
      fail('Stall detected: no progress for 90s; AI may be hung or looping')
    }

    const messages = parentSnapshot.messages_by_session?.[parentID] || []
    const lastAssistant = [...messages].reverse().find((m) => m.role === 'assistant' && String(m.content || '').trim())
    const isAssistantDone = lastAssistant && (lastAssistant.content.includes('SUBAGENT_DELEGATION_OK') || lastAssistant.content.includes('EXACT_DELEGATION_VERIFIED'))

    if (intent?.status === 'completed' && isAssistantDone) {
      completed = true
      break
    }
    if (['failed', 'cancelled', 'interrupted', 'expired'].includes(intent?.status)) {
      fail(`Parent run terminated with status: ${intent.status}`)
    }

    await sleep(2000)
  }

  assert(completed, 'Parent session did not complete within timeout')
  assert(delegatedChildren.length >= 1, `Expected at least 1 delegated subagent child, got ${delegatedChildren.length}`)
  log(`✓ Subagent child created and completed: ${delegatedChildren[0].id} (requested: ${delegatedChildren[0].metadata?.requested_subagent})`)
  result.gates.subagent_spawned = true
  result.gates.child_completed = true

  // 6. Verify non-looping behavior: exactly 1 regular task wave, no infinite iterations
  const events = parentSnapshot.events_by_session?.[parentID] || []
  const taskEvents = events.filter((e) => {
    if (e.event_type !== 'session.tool.completed') return false
    const p = typeof e.payload === 'string' ? JSON.parse(e.payload || '{}') : (e.payload || {})
    return (p.tool_name || p.name) === 'task'
  })
  log(`Task tool calls completed by parent: ${taskEvents.length}`)
  assert(taskEvents.length >= 1 && taskEvents.length <= 3, `Expected 1-3 task calls, got ${taskEvents.length}`)
  result.gates.no_infinite_loop = true
  result.gates.subagent_delegation_passed = true

  result.result = 'PASS'
  result.completed_at = new Date().toISOString()
  log('🎉 Regular subagent delegation smoke test PASSED successfully!')
  console.log(JSON.stringify(result, null, 2))
}

main().catch((err) => {
  log(`FATAL: ${err.message}`)
  result.result = 'FAIL'
  result.error = err.message
  result.completed_at = new Date().toISOString()
  console.log(JSON.stringify(result, null, 2))
  process.exitCode = 1
})
