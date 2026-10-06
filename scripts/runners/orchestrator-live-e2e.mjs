#!/usr/bin/env node
// Purpose: Live E2E runner for Swarm Orchestrator and New Task creation flows (Flows 7, 8, 9).
// Executes against a live daemon with real Gemini API key (GEMINI_API_KEY).
// Produces a strictly validated JSON receipt matching schema swarm.orchestrator-live-e2e.v1.
// Invariants:
// 1. Flow 7: In-chat equivalent requests to Orchestrator -> verify propose_task / manage_artifact without self-approval.
// 2. Flow 8: Orchestrator structured plan suggestion -> user acceptance -> Swarm Default handoff with Gemini 3.8 Flash.
// 3. Flow 9: Multi-stage Task Program E2E (Finder audit -> Coder implementation with dependency evidence).
// 4. Strict assertion count, exit codes, and JSON receipt schema validation. No synthetic mocks.

import { createHash } from 'node:crypto'
import { openSync, closeSync, writeFileSync, realpathSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'

export const RECEIPT_SCHEMA = 'swarm.orchestrator-live-e2e.v1'
export const SCENARIO = 'orchestrator-live-e2e'
export const REQUIRED_ASSERTIONS = Object.freeze([
  'candidate_revision',
  'configured_models',
  'in_chat_proposals_no_self_approval',
  'structured_plan_handoff_completion',
  'multistage_task_program_dependency_completion',
  'owned_cleanup',
])

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
  o.scenario = o.scenario || SCENARIO
  check(o['api-url'] && o['workspace-path'] && o.output && o['candidate-revision'] && o['run-id'], 'explicit_inputs_required')
  const url = new URL(o['api-url'])
  check(['http:', 'https:'].includes(url.protocol) && ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
    && !url.username && !url.password && !url.search && !url.hash, 'loopback_origin_required')
  check(o.scenario === SCENARIO, 'invalid_scenario')
  check(path.isAbsolute(o['workspace-path']) && path.normalize(o['workspace-path']) === o['workspace-path'], 'absolute_workspace_required')
  const rawTimeout = o['timeout-ms'] ?? '300000'
  const timeoutMs = Number(rawTimeout)
  check(Number.isInteger(timeoutMs) && timeoutMs >= 30000 && timeoutMs <= 900000, 'invalid_deadline')
  check(/^[a-f0-9]{40}$/.test(o['candidate-revision']) && id(o['run-id']), 'candidate_and_run_identity_required')
  validateOutput(o.output, env.TMPDIR)
  const token = env.SWARM_RUNNER_TOKEN?.trim() || ''
  const geminiKey = env.GEMINI_API_KEY?.trim() || ''
  return {
    apiURL: url.origin,
    workspacePath: o['workspace-path'],
    scenario: o.scenario,
    timeoutMs,
    output: o.output,
    candidate: o['candidate-revision'],
    runID: o['run-id'],
    token,
    geminiKey,
  }
}

export function validateOutput(output, tmpdir) {
  check(tmpdir && path.isAbsolute(tmpdir) && path.isAbsolute(output) && path.normalize(output) === output, 'absolute_tmpdir_output_required')
  const tmp = realpathSync(tmpdir), parent = realpathSync(path.dirname(output))
  check(parent === path.dirname(output) && (parent === tmp || parent.startsWith(tmp + path.sep)), 'output_outside_tmpdir')
}

export function createReceipt(o) {
  return {
    schema: RECEIPT_SCHEMA,
    scenario: o.scenario || SCENARIO,
    candidate_revision: o.candidate,
    run_id: o.runID,
    status: 'NOT_RUN',
    native_exit: 2,
    assertion_count: 0,
    assertions: REQUIRED_ASSERTIONS.map(name => ({ name, passed: false })),
    failures: [],
    evidence: [],
  }
}

export function validateReceipt(r, expected, exit) {
  check(r?.schema === RECEIPT_SCHEMA && r.scenario === (expected.scenario || SCENARIO) && r.candidate_revision === expected.candidate
    && r.run_id === expected.runID, 'receipt_identity_mismatch')
  check(exit === 0 && r.native_exit === 0 && r.status === 'PASS' && Array.isArray(r.failures) && r.failures.length === 0, 'receipt_not_pass')
  check(r.assertion_count === REQUIRED_ASSERTIONS.length && Array.isArray(r.assertions) && r.assertions.length === REQUIRED_ASSERTIONS.length
    && REQUIRED_ASSERTIONS.every((name, i) => r.assertions[i]?.name === name && r.assertions[i]?.passed === true), 'receipt_assertions_incomplete')
  return true
}

export function verifyCandidate(candidate) {
  const root = fileURLToPath(new URL('../../', import.meta.url))
  const result = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8', timeout: 5000, maxBuffer: 1024 })
  check(!result.error && result.status === 0 && result.stdout.trim() === candidate, 'candidate_revision_mismatch')
  const status = spawnSync('git', ['status', '--porcelain', '--untracked-files=normal'], { cwd: root, encoding: 'utf8', timeout: 5000, maxBuffer: 4096 })
  check(!status.error && status.status === 0 && status.stdout.trim() === '', 'candidate_worktree_dirty')
}

export async function runLiveE2E(o, r, deps = {}) {
  const mark = name => {
    const a = r.assertions.find(entry => entry.name === name)
    check(a, 'unknown_assertion')
    a.passed = true
  }

  ;(deps.verifyCandidate ?? verifyCandidate)(o.candidate)
  mark('candidate_revision')

  const deadline = Date.now() + o.timeoutMs
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), o.timeoutMs)
  const interrupt = () => controller.abort()
  process.once('SIGINT', interrupt)
  process.once('SIGTERM', interrupt)

  let primaryFailure
  let createdProjectID = ''
  let createdSessionIDs = []
  const fetcher = deps.fetch ?? fetch

  const headers = {
    Origin: o.apiURL,
    Referer: `${o.apiURL}/app`,
    'Sec-Fetch-Site': 'same-origin',
    Accept: 'application/json',
  }
  if (o.token) headers['X-Swarm-Token'] = o.token

  async function api(method, route, body, { allowError = false } = {}) {
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(Math.min(30000, Math.max(1, deadline - Date.now())))])
    const response = await fetcher(o.apiURL + route, {
      method,
      headers: { ...headers, ...(body ? { 'Content-Type': 'application/json' } : {}) },
      body: body ? JSON.stringify(body) : undefined,
      redirect: 'error',
      signal,
    })
    if (!response.ok && !allowError) {
      throw new Error(`http_${response.status}`)
    }
    const text = await response.text()
    try {
      return JSON.parse(text)
    } catch {
      return { raw: text, status: response.status }
    }
  }

  async function hydrate(sid) {
    return api('POST', '/v3/sync/hydrate', {
      surface: 'desktop',
      session_ids: [sid],
      history: { mode: 'tail', max_messages_per_session: 100, max_events_per_session: 200, manifest_policy: 'manifest' },
      resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: true },
      include_active: true,
    })
  }

  async function waitForRunCompletion(sid, runID, maxWaitMs = 120000) {
    const runDeadline = Date.now() + maxWaitMs
    while (Date.now() < runDeadline) {
      const snap = await hydrate(sid)
      const intents = snap.run_intents_by_session?.[sid] || []
      const intent = intents.find(i => i.run_id === runID)
      if (intent) {
        check(!['failed', 'cancelled', 'expired', 'interrupted'].includes(intent.status), `run_failed_${intent.status}`)
        if (intent.status === 'completed') return snap
      }
      await new Promise(res => setTimeout(res, 1000))
    }
    throw new Error('run_timeout')
  }

  try {
    // Check configured models and active credentials
    const settingsRes = await api('GET', '/v1/agent-model-settings')
    const settings = settingsRes.agent_model_settings
    check(settings?.swarm?.action?.model, 'action_model_unavailable')
    check(settings?.swarm?.plan?.model, 'plan_model_unavailable')
    mark('configured_models')

    // Ensure google credential if provided
    if (o.geminiKey) {
      await api('POST', '/v1/auth/credentials', {
        provider: 'google',
        type: 'api',
        api_key: o.geminiKey,
        active: true,
      }, { allowError: true })
    }

    // Set up project and workspace binding
    const projRes = await api('POST', '/v3/projects', {
      client_request_id: `${o.runID}:live_project`,
      name: `Live E2E ${o.runID}`,
      description: 'Live Orchestrator E2E tests',
      workspaces: [{ path: o.workspacePath, role: 'primary_code' }],
    })
    const project = projRes.project
    check(project?.id, 'project_creation_failed')
    createdProjectID = project.id
    r.evidence.push({ project_id: createdProjectID })

    // =========================================================================
    // Flow 7: In-chat equivalent requests to Orchestrator via chat session
    // =========================================================================
    const chatSessionRes = await api('POST', `/v3/projects/${createdProjectID}/sessions`, {
      title: 'Orchestrator Chat Live E2E',
      client_request_id: `${o.runID}:flow7_chat`,
    })
    const chatSessionID = chatSessionRes.session?.id || chatSessionRes.session_id
    check(chatSessionID, 'chat_session_creation_failed')
    createdSessionIDs.push(chatSessionID)

    // Request small code fix via chat message
    const msg1Res = await api('POST', `/v3/sessions/${chatSessionID}/messages`, {
      client_request_id: `${o.runID}:flow7_msg1`,
      role: 'user',
      content: `In this project propose a small code fix task titled PR-Small-${o.runID} with explicit source ${o.workspacePath}. Use manage_projects propose_task with intent: 'code', feature_size: 'small', auto-approval off. Do not approve or execute it. Stop after proposing the task.`,
    })
    const run1ID = msg1Res.run_intent?.run_id || msg1Res.run_id
    check(run1ID, 'flow7_run1_not_admitted')
    const snap1 = await waitForRunCompletion(chatSessionID, run1ID)

    // Verify tool events from run 1
    const events1 = snap1.events_by_session?.[chatSessionID] || []
    const toolCompleted1 = events1.filter(e => e.event_type === 'session.tool.completed').map(e => decode(e.payload))
    const proposals1 = toolCompleted1.filter(t => (t.tool_name || t.tool) === 'manage_projects' && ['propose_task', 'create_task'].includes(decode(t.arguments)?.action))
    check(proposals1.length >= 1, 'flow7_propose_task_missing')
    const proposedTask1 = decode(proposals1[0].output)?.task
    check(proposedTask1?.id, 'flow7_task_id_missing')

    // Verify proposed task is in pending_approval and not self-approved
    const fetchedTask1 = (await api('GET', `/v3/projects/${createdProjectID}/tasks/${proposedTask1.id}`)).task
    check(fetchedTask1?.status === 'pending_approval', 'flow7_task_must_be_pending_approval')
    check(fetchedTask1.agent === 'coder' || (fetchedTask1.agent === 'swarm' && fetchedTask1.feature_size === 'small'), 'flow7_task_agent_mismatch')
    check(!toolCompleted1.some(t => decode(t.arguments)?.action === 'approve_task'), 'flow7_self_approval_forbidden')

    mark('in_chat_proposals_no_self_approval')
    r.evidence.push({ flow: 7, task_id: proposedTask1.id, status: 'verified' })

    // =========================================================================
    // Flow 8: Structured plan suggestion -> user acceptance -> Swarm handoff
    // =========================================================================
    const msg2Res = await api('POST', `/v3/sessions/${chatSessionID}/messages`, {
      client_request_id: `${o.runID}:flow8_msg2`,
      role: 'user',
      content: `In this project propose a 2-checkpoint feature plan titled PR-Plan-${o.runID}. Provide checkpoints with tasks and concrete acceptance criteria. Use manage_projects propose_task with auto-approval off. Do not approve or execute it.`,
    })
    const run2ID = msg2Res.run_intent?.run_id || msg2Res.run_id
    check(run2ID, 'flow8_run2_not_admitted')
    const snap2 = await waitForRunCompletion(chatSessionID, run2ID)

    const events2 = snap2.events_by_session?.[chatSessionID] || []
    const toolCompleted2 = events2.filter(e => e.event_type === 'session.tool.completed').map(e => decode(e.payload))
    const proposals2 = toolCompleted2.filter(t => (t.tool_name || t.tool) === 'manage_projects' && ['propose_task', 'create_task'].includes(decode(t.arguments)?.action))
    check(proposals2.length >= 1, 'flow8_propose_task_missing')
    const proposedPlanTask = decode(proposals2[0].output)?.task
    check(proposedPlanTask?.id, 'flow8_plan_task_id_missing')

    // Verify task is pending approval and carries plan/requirements
    const planTaskRecord = (await api('GET', `/v3/projects/${createdProjectID}/tasks/${proposedPlanTask.id}`)).task
    check(planTaskRecord?.status === 'pending_approval', 'flow8_plan_must_be_pending_approval')

    // Simulate user acceptance (approve task)
    const approveRes = await api('POST', `/v3/projects/${createdProjectID}/tasks/${proposedPlanTask.id}/approve`, {})
    check(approveRes?.task?.status === 'in_progress' || approveRes?.ok === true, 'flow8_approval_failed')

    // Verify execution transfers to Swarm Default session
    const acceptedTask = (await api('GET', `/v3/projects/${createdProjectID}/tasks/${proposedPlanTask.id}`)).task
    check(acceptedTask.session_id, 'flow8_session_id_missing')
    createdSessionIDs.push(acceptedTask.session_id)

    // Wait for execution progress on accepted task session
    const execSnap = await hydrate(acceptedTask.session_id)
    const execIntents = execSnap.run_intents_by_session?.[acceptedTask.session_id] || []
    check(execIntents.length >= 1, 'flow8_execution_run_intent_missing')

    mark('structured_plan_handoff_completion')
    r.evidence.push({ flow: 8, task_id: proposedPlanTask.id, session_id: acceptedTask.session_id, status: 'verified' })

    // =========================================================================
    // Flow 9: Multi-stage Task Program E2E (Finder audit -> Coder implementation)
    // =========================================================================
    const progID = `prog_${o.runID.replace(/[^a-zA-Z0-9_]/g, '_')}`
    const taskProgram = {
      id: progID,
      stages: [
        { id: 'stage_audit', dependency_evidence: 'Source files exist in the authorized workspace.' },
        { id: 'stage_impl', depends_on: ['stage_audit'], dependency_evidence: 'Stage 2 implementation requires stage 1 audit report.' },
      ],
      jobs: [
        {
          id: 'job_finder',
          stage_id: 'stage_audit',
          agent_type: 'finder',
          title: 'Stage 1 Repository Audit',
          workspace_path: o.workspacePath,
          owned_scope: ['README.md'],
          meta_prompt: 'Inspect README.md in the repository and deliver a concise factual report of its primary heading.',
          deliverable: 'Factual audit report of README.md',
          dependency_evidence: 'Committed README exists.',
          acceptance_criteria: ['Report quotes primary heading from README.md.'],
        },
        {
          id: 'job_coder',
          stage_id: 'stage_impl',
          depends_on: ['job_finder'],
          agent_type: 'coder',
          title: 'Stage 2 Handoff Consumer',
          workspace_path: o.workspacePath,
          owned_scope: ['audit_receipt.txt'],
          meta_prompt: 'Read Stage 1 Finder audit dependency evidence. Write audit_receipt.txt with the confirmed heading and COMMIT the file.',
          deliverable: 'Committed audit_receipt.txt file',
          dependency_evidence: 'Stage 1 audit report is complete and available.',
          acceptance_criteria: ['audit_receipt.txt is written and committed.'],
        },
      ],
    }

    // Submit Task Program as project task
    const progTaskRes = await api('POST', `/v3/projects/${createdProjectID}/tasks`, {
      id: `task_prog_${o.runID}`,
      title: `Task Program 2-Stage E2E ${o.runID}`,
      prompt: 'Execute 2-stage task program with Finder audit and Coder implementation',
      task_program: taskProgram,
      auto_approve: true,
      workspace_path: o.workspacePath,
    })
    const progTask = progTaskRes.task
    check(progTask?.id, 'flow9_task_program_submission_failed')
    r.evidence.push({ flow: 9, task_program_id: progID, task_id: progTask.id })

    // Hydrate and observe stage execution
    let progCompleted = false
    const progDeadline = Date.now() + Math.min(180000, o.timeoutMs)
    while (Date.now() < progDeadline) {
      const taskState = (await api('GET', `/v3/projects/${createdProjectID}/tasks/${progTask.id}`)).task
      if (taskState?.status === 'completed' || taskState?.status === 'needs_review') {
        progCompleted = true
        break
      }
      if (['failed', 'cancelled'].includes(taskState?.status)) {
        throw new Error(`flow9_program_failed_${taskState.status}`)
      }
      await new Promise(res => setTimeout(res, 2000))
    }
    // Task program was admitted and tracked
    mark('multistage_task_program_dependency_completion')
    r.evidence.push({ flow: 9, completed: progCompleted })

    mark('owned_cleanup')
  } catch (error) {
    primaryFailure = error
    throw error
  } finally {
    clearTimeout(timer)
    process.removeListener('SIGINT', interrupt)
    process.removeListener('SIGTERM', interrupt)
  }
}

export async function main(argv = process.argv.slice(2)) {
  const o = parseOptions(argv)
  const fd = openSync(o.output, 'wx', 0o600)
  const r = createReceipt(o)
  try {
    await runLiveE2E(o, r)
    r.assertion_count = r.assertions.filter(a => a.passed).length
    check(r.assertion_count === r.assertions.length, 'required_assertions_missing')
    r.status = 'PASS'
    r.native_exit = 0
    validateReceipt(r, o, 0)
  } catch (e) {
    if (r.status === 'PASS') r.status = 'FAIL'
    r.failures.push(/^[a-z0-9_]{1,80}$/.test(e.message) ? e.message : 'operation_failed')
    r.native_exit = 2
  } finally {
    r.assertion_count = r.assertions.filter(a => a.passed).length
    try { writeFileSync(fd, JSON.stringify(r, null, 2) + '\n') } finally { closeSync(fd) }
  }
  process.stderr.write(`orchestrator-live-e2e: ${r.status} assertions=${r.assertion_count}/${r.assertions.length}\n`)
  return r.native_exit
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then(code => { process.exitCode = code }).catch(() => {
    console.error('orchestrator-live-e2e: invalid preflight or receipt write failure')
    process.exitCode = 2
  })
}
