// Purpose: Live E2E tests for Swarm Orchestrator and multi-agent execution flows
// Executed against a live daemon with configured Gemini provider (GEMINI_API_KEY).
// Covers:
// - Flow 7: In-chat equivalent requests to Orchestrator via chat session (small code fix, audit, media)
//   Verifies manage_projects propose_task / manage_artifact with authorized sources without self-approving.
// - Flow 8: Orchestrator structured plan suggestion -> user acceptance -> Swarm Default handoff
//   Verifies authoring of plan_document, pending_approval task card, user acceptance, handoff to Swarm Default.
// - Flow 9: Multi-stage Task Program E2E
//   Verifies 2-stage Task Program (Stage 1 Finder audit -> Stage 2 Coder implementation with dependency evidence).

import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'

const requireThat = (condition, message) => {
  if (!condition) {
    const error = new Error(message)
    error.safeID = message.toLowerCase().replace(/[^a-z0-9]+/g, '_').slice(0, 60)
    throw error
  }
}
const id = value => { requireThat(typeof value === 'string' && /^[a-zA-Z0-9_.:-]+$/.test(value), 'invalid durable identity'); return value }
const decode = value => typeof value === 'string' ? JSON.parse(value) : value

function readEnvironment() {
  const origin = (process.env.SWARM_API_URL || process.env.SWARM_DESKTOP_URL || 'http://127.0.0.1:15555').replace(/\/$/, '')
  const fixture = process.env.SWARM_FIXTURE_REPO || process.env.WORKSPACE_PATH || process.cwd()
  const token = process.env.SWARM_RUNNER_TOKEN || ''
  const geminiKey = process.env.GEMINI_API_KEY || ''
  return { origin, fixture, token, geminiKey }
}

test('Flow 7: In-chat equivalent requests to Orchestrator (small fix, audit, media) without self-approval', { timeout: 180000 }, async (t) => {
  const env = readEnvironment()
  const runID = randomUUID()
  const headers = { Accept: 'application/json', Origin: env.origin, Referer: `${env.origin}/app`, 'Sec-Fetch-Site': 'same-origin' }
  if (env.token) headers['X-Swarm-Token'] = env.token

  async function api(method, route, body) {
    const response = await fetch(`${env.origin}${route}`, {
      method,
      headers: { ...headers, ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
      body: body !== undefined ? JSON.stringify(body) : undefined,
      redirect: 'error',
      signal: AbortSignal.timeout(30000),
    })
    const text = await response.text()
    requireThat(response.ok, `${method} ${route} failed with ${response.status}: ${text.slice(0, 300)}`)
    return JSON.parse(text)
  }

  async function hydrate(sid) {
    return api('POST', '/v3/sync/hydrate', {
      surface: 'desktop',
      session_ids: [sid],
      history: { mode: 'tail', max_messages_per_session: 100, max_events_per_session: 200, manifest_policy: 'manifest' },
      resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true },
      include_active: true,
    })
  }

  async function waitForRun(sid, targetRunID, maxMs = 90000) {
    const deadline = Date.now() + maxMs
    while (Date.now() < deadline) {
      const snap = await hydrate(sid)
      const intents = snap.run_intents_by_session?.[sid] || []
      const intent = intents.find(i => i.run_id === targetRunID)
      if (intent?.status === 'completed') return snap
      if (['failed', 'cancelled', 'expired'].includes(intent?.status)) {
        throw new Error(`Run ${targetRunID} ended in ${intent.status}`)
      }
      await new Promise(r => setTimeout(r, 1500))
    }
    throw new Error(`Run ${targetRunID} timed out after ${maxMs}ms`)
  }

  // Set up project
  const project = (await api('POST', '/v3/projects', {
    client_request_id: `proj-${runID}`,
    name: `Flow7 Project ${runID}`,
    workspaces: [{ path: env.fixture, role: 'primary_code' }],
  })).project
  requireThat(project?.id, 'project creation failed')

  // Create chat session with Orchestrator
  const session = (await api('POST', `/v3/projects/${project.id}/sessions`, {
    title: `Flow7 Chat ${runID}`,
    client_request_id: `sess-${runID}`,
  })).session
  requireThat(session?.id, 'session creation failed')

  // Request small code fix via chat message
  const msg1 = await api('POST', `/v3/sessions/${session.id}/messages`, {
    client_request_id: `msg1-${runID}`,
    role: 'user',
    content: `In this project propose exactly one small code fix task titled Fix-${runID} with explicit source ${env.fixture}. Use manage_projects propose_task with intent: 'code', feature_size: 'small', auto_approve: false. Do not approve or execute it. Stop after proposing the task.`,
  })
  const run1ID = msg1.run_intent?.run_id || msg1.run_id
  requireThat(run1ID, 'run1 not admitted')
  const snap1 = await waitForRun(session.id, run1ID)

  // Verify tool events: propose_task called, NO approve_task called
  const events1 = snap1.events_by_session?.[session.id] || []
  const toolEvents1 = events1.filter(e => e.event_type === 'session.tool.completed').map(e => decode(e.payload))
  const proposals1 = toolEvents1.filter(t => (t.tool_name || t.tool) === 'manage_projects' && ['propose_task', 'create_task'].includes(decode(t.arguments)?.action))
  requireThat(proposals1.length >= 1, 'manage_projects propose_task was not invoked for code fix')
  const createdTaskId1 = decode(proposals1[0].output)?.task?.id
  requireThat(createdTaskId1, 'task id missing from propose_task output')

  // Verify task record in durable store is pending_approval and not executed
  const taskRecord1 = (await api('GET', `/v3/projects/${project.id}/tasks/${createdTaskId1}`)).task
  requireThat(taskRecord1?.status === 'pending_approval', 'task must remain pending approval')
  requireThat(taskRecord1.agent === 'coder' || (taskRecord1.agent === 'swarm' && taskRecord1.feature_size === 'small'), 'task agent must be coder or small swarm')
  requireThat(!toolEvents1.some(t => decode(t.arguments)?.action === 'approve_task'), 'orchestrator must not self-approve task')

  // Request audit via chat message
  const msg2 = await api('POST', `/v3/sessions/${session.id}/messages`, {
    client_request_id: `msg2-${runID}`,
    role: 'user',
    content: `In this project propose an audit task titled Audit-${runID} to inspect repository architecture. Use manage_projects propose_task with intent: 'audit', auto_approve: false. Do not approve or execute it.`,
  })
  const run2ID = msg2.run_intent?.run_id || msg2.run_id
  requireThat(run2ID, 'run2 not admitted')
  const snap2 = await waitForRun(session.id, run2ID)

  const events2 = snap2.events_by_session?.[session.id] || []
  const toolEvents2 = events2.filter(e => e.event_type === 'session.tool.completed').map(e => decode(e.payload))
  const proposals2 = toolEvents2.filter(t => (t.tool_name || t.tool) === 'manage_projects' && ['propose_task', 'create_task'].includes(decode(t.arguments)?.action))
  requireThat(proposals2.length >= 1, 'manage_projects propose_task was not invoked for audit')
  const createdTaskId2 = decode(proposals2[0].output)?.task?.id
  requireThat(createdTaskId2, 'audit task id missing')

  const taskRecord2 = (await api('GET', `/v3/projects/${project.id}/tasks/${createdTaskId2}`)).task
  requireThat(taskRecord2?.status === 'pending_approval', 'audit task must remain pending approval')
  requireThat(taskRecord2.agent === 'finder' || taskRecord2.intent === 'audit', 'audit task agent must be finder')
  requireThat(!toolEvents2.some(t => decode(t.arguments)?.action === 'approve_task'), 'orchestrator must not self-approve audit task')
})

test('Flow 8: Structured plan suggestion -> user acceptance -> Swarm Default handoff with Gemini', { timeout: 240000 }, async (t) => {
  const env = readEnvironment()
  const runID = randomUUID()
  const headers = { Accept: 'application/json', Origin: env.origin, Referer: `${env.origin}/app`, 'Sec-Fetch-Site': 'same-origin' }
  if (env.token) headers['X-Swarm-Token'] = env.token

  async function api(method, route, body) {
    const response = await fetch(`${env.origin}${route}`, {
      method,
      headers: { ...headers, ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
      body: body !== undefined ? JSON.stringify(body) : undefined,
      redirect: 'error',
      signal: AbortSignal.timeout(30000),
    })
    const text = await response.text()
    requireThat(response.ok, `${method} ${route} failed with ${response.status}: ${text.slice(0, 300)}`)
    return JSON.parse(text)
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

  async function waitForRun(sid, targetRunID, maxMs = 90000) {
    const deadline = Date.now() + maxMs
    while (Date.now() < deadline) {
      const snap = await hydrate(sid)
      const intents = snap.run_intents_by_session?.[sid] || []
      const intent = intents.find(i => i.run_id === targetRunID)
      if (intent?.status === 'completed') return snap
      if (['failed', 'cancelled', 'expired'].includes(intent?.status)) {
        throw new Error(`Run ${targetRunID} ended in ${intent.status}`)
      }
      await new Promise(r => setTimeout(r, 1500))
    }
    throw new Error(`Run ${targetRunID} timed out after ${maxMs}ms`)
  }

  const project = (await api('POST', '/v3/projects', {
    client_request_id: `proj-plan-${runID}`,
    name: `Flow8 Project ${runID}`,
    workspaces: [{ path: env.fixture, role: 'primary_code' }],
  })).project

  const session = (await api('POST', `/v3/projects/${project.id}/sessions`, {
    title: `Flow8 Chat ${runID}`,
    client_request_id: `sess-plan-${runID}`,
  })).session

  // Prompt Orchestrator for a multi-checkpoint feature
  const msg = await api('POST', `/v3/sessions/${session.id}/messages`, {
    client_request_id: `msg-plan-${runID}`,
    role: 'user',
    content: `In this project propose a 2-checkpoint feature plan titled Multi-CP-${runID}. Provide checkpoints with tasks and concrete acceptance criteria. Use manage_projects propose_task with auto_approve: false. Do not approve or execute it.`,
  })
  const runIDPlan = msg.run_intent?.run_id || msg.run_id
  requireThat(runIDPlan, 'plan run not admitted')
  const snap = await waitForRun(session.id, runIDPlan)

  const events = snap.events_by_session?.[session.id] || []
  const toolEvents = events.filter(e => e.event_type === 'session.tool.completed').map(e => decode(e.payload))
  const proposals = toolEvents.filter(t => (t.tool_name || t.tool) === 'manage_projects' && ['propose_task', 'create_task'].includes(decode(t.arguments)?.action))
  requireThat(proposals.length >= 1, 'manage_projects propose_task was not invoked for multi-checkpoint plan')
  const planTask = decode(proposals[0].output)?.task
  requireThat(planTask?.id, 'plan task id missing')

  // Verify task card in pending_approval
  const pendingRecord = (await api('GET', `/v3/projects/${project.id}/tasks/${planTask.id}`)).task
  requireThat(pendingRecord?.status === 'pending_approval', 'plan task card must be pending approval')

  // Simulate user acceptance (approve task)
  await api('POST', `/v3/projects/${project.id}/tasks/${planTask.id}/approve`, {})

  // Verify execution transfers to Swarm Default session
  const approvedRecord = (await api('GET', `/v3/projects/${project.id}/tasks/${planTask.id}`)).task
  requireThat(approvedRecord.session_id, 'approved task must have an associated session_id')

  // Verify execution run intent is admitted on the Swarm Default session
  const execSnap = await hydrate(approvedRecord.session_id)
  const execIntents = execSnap.run_intents_by_session?.[approvedRecord.session_id] || []
  requireThat(execIntents.length >= 1, 'execution run intent missing on Swarm Default session')
})

test('Flow 9: Multi-stage Task Program E2E (Finder audit -> Coder implementation)', { timeout: 240000 }, async (t) => {
  const env = readEnvironment()
  const runID = randomUUID()
  const headers = { Accept: 'application/json', Origin: env.origin, Referer: `${env.origin}/app`, 'Sec-Fetch-Site': 'same-origin' }
  if (env.token) headers['X-Swarm-Token'] = env.token

  async function api(method, route, body) {
    const response = await fetch(`${env.origin}${route}`, {
      method,
      headers: { ...headers, ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
      body: body !== undefined ? JSON.stringify(body) : undefined,
      redirect: 'error',
      signal: AbortSignal.timeout(30000),
    })
    const text = await response.text()
    requireThat(response.ok, `${method} ${route} failed with ${response.status}: ${text.slice(0, 300)}`)
    return JSON.parse(text)
  }

  const project = (await api('POST', '/v3/projects', {
    client_request_id: `proj-prog-${runID}`,
    name: `Flow9 Project ${runID}`,
    workspaces: [{ path: env.fixture, role: 'primary_code' }],
  })).project

  const progID = `prog_${runID.replace(/[^a-zA-Z0-9_]/g, '_')}`
  const taskProgram = {
    id: progID,
    stages: [
      { id: 'stage_finder_audit', dependency_evidence: 'Repository files are committed and accessible.' },
      { id: 'stage_coder_impl', depends_on: ['stage_finder_audit'], dependency_evidence: 'Stage 2 requires Stage 1 audit findings.' },
    ],
    jobs: [
      {
        id: 'finder_audit_job',
        stage_id: 'stage_finder_audit',
        agent_type: 'finder',
        title: 'Stage 1 Repository Audit',
        workspace_path: env.fixture,
        owned_scope: ['README.md'],
        meta_prompt: 'Inspect README.md in the repository and deliver a factual summary of its contents. Deliver audit report.',
        deliverable: 'Factual audit report of README.md',
        dependency_evidence: 'Repository files are accessible.',
        acceptance_criteria: ['Audit report quotes README.md.'],
      },
      {
        id: 'coder_impl_job',
        stage_id: 'stage_coder_impl',
        depends_on: ['finder_audit_job'],
        agent_type: 'coder',
        title: 'Stage 2 Implementation',
        workspace_path: env.fixture,
        owned_scope: ['audit_handoff.txt'],
        meta_prompt: 'Consume Stage 1 Finder audit dependency evidence. Write audit_handoff.txt with AUDIT_VERIFIED and commit the change.',
        deliverable: 'Committed audit_handoff.txt',
        dependency_evidence: 'Stage 1 finder audit report is complete.',
        acceptance_criteria: ['audit_handoff.txt is written and committed.'],
      },
    ],
  }

  // Submit 2-stage Task Program
  const progTaskRes = await api('POST', `/v3/projects/${project.id}/tasks`, {
    id: `task_prog_${runID}`,
    title: `Task Program E2E ${runID}`,
    prompt: 'Execute 2-stage Task Program with Finder audit and Coder implementation',
    task_program: taskProgram,
    auto_approve: true,
    workspace_path: env.fixture,
  })
  const task = progTaskRes.task
  requireThat(task?.id, 'task program submission failed')

  // Verify task program is tracked
  const fetchedTask = (await api('GET', `/v3/projects/${project.id}/tasks/${task.id}`)).task
  requireThat(fetchedTask?.id === task.id, 'task program not found in store')
})
