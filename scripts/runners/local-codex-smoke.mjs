#!/usr/bin/env node
// Requirement: prove one exact local candidate can use its dedicated Codex
// broker at Luna/medium without host credentials, tool execution or fallback.
// Authority: local_testbench_codex.py checks HEAD/generation before this runner;
// AttachClient bounds authenticated same-origin API calls; V3 hydrate supplies
// durable completion evidence. This live runner is not a hermetic critical test.
import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { AttachClient } from '../testbench-attach.mjs'

const client = new AttachClient(process.env.SWARM_DESKTOP_URL, { stageMs: 180000 })
let sessionID = ''
let completed = false
const assignment = { provider: 'codex', model: 'gpt-5.6-luna', thinking: 'medium' }
try {
  client.token = (await client.get('/v1/auth/desktop/session')).token
  assert.equal(typeof client.token, 'string')
  assert.ok(client.token)
  // Fresh candidates have no saved workspace and therefore no topology binding.
  // Register only the known isolated guest source; never change current selection.
  let topology = await client.get('/v1/swarm/topology')
  let binding = topology.workspace_bindings?.find(b => b.source_workspace_path === '/candidate/source')
  if (!binding) {
    await client.request('POST', '/v1/workspace/add', { path: '/candidate/source', name: 'local-codex-proof', make_current: false })
    topology = await client.get('/v1/swarm/topology')
    binding = topology.workspace_bindings?.find(b => b.source_workspace_path === '/candidate/source')
  }
  assert.ok(binding?.workspace_binding_id, 'isolated source binding required')
  const runtime = topology.runtimes?.find(r => r.relationship === 'self' && r.status === 'online')
  assert.ok(runtime?.swarm_id, 'online candidate identity required')
  const evidence = await client.inspect(runtime.swarm_id)
  const settings = (await client.get('/v1/agent-model-settings')).agent_model_settings
  for (const role of [settings.swarm.action, settings.swarm.plan, ...['compact', 'finder', 'coder', 'designer', 'router'].map(k => settings.system_agents[k])]) {
    for (const [key, value] of Object.entries(assignment)) assert.equal(role[key], value, `wrong ${key}`)
  }
  const created = await client.request('POST', '/v3/sessions', {
    client_request_id: randomUUID(), title: 'Local Codex Luna smoke',
    workspace_path: '/candidate/source', workspace_binding_id: binding.workspace_binding_id,
    swarm_id: runtime.swarm_id, target_kind: 'host', target_relationship: 'self',
    mode: 'auto', agent_name: 'swarm', preference: assignment,
  })
  sessionID = created.session_id
  assert.match(sessionID, /^[a-zA-Z0-9_-]+$/)
  console.log(JSON.stringify({ stage: 'created', session_id: sessionID, ...evidence }))
  await client.request('POST', `/v3/sessions/${sessionID}/messages`, {
    client_request_id: randomUUID(), role: 'user',
    content: 'This is a bounded provider connectivity test. Do not use tools, edit files, or create a plan. Reply with exactly LOCAL_CODEX_OK.',
  })
  for (let attempt = 0; attempt < 10; attempt++) {
    await new Promise(resolve => setTimeout(resolve, 15000))
    const snapshot = await client.request('POST', '/v3/sync/hydrate', {
      surface: 'desktop', session_ids: [sessionID],
      history: { mode: 'tail', max_messages_per_session: 10, max_events_per_session: 0, manifest_policy: 'manifest' },
      resources: { messages: true, run_intents: true, current_run_state: true, session_view: true }, include_active: true,
    })
    const runs = snapshot.run_intents_by_session?.[sessionID] || []
    console.log(JSON.stringify({ stage: 'waiting', statuses: runs.map(r => r.status) }))
    if (runs.some(r => ['failed', 'cancelled', 'interrupted', 'expired'].includes(r.status))) throw new Error('smoke run did not complete')
    if (runs.length && runs.every(r => r.status === 'completed')) {
      const messages = snapshot.messages_by_session?.[sessionID] || []
      assert.ok(messages.some(m => m.role === 'assistant' && m.content === 'LOCAL_CODEX_OK'), 'assistant marker missing')
      for (const [key, value] of Object.entries(assignment)) assert.equal(snapshot.sessions_by_id?.[sessionID]?.preference?.[key], value)
      const after = await client.inspect(runtime.swarm_id)
      assert.equal(after.settings_sha256, evidence.settings_sha256)
      completed = true
      console.log(JSON.stringify({ stage: 'PASS', session_id: sessionID, ...assignment, assistant_marker_verified: true }))
      break
    }
  }
  assert.ok(completed, 'smoke deadline exceeded')
} catch (error) {
  // Never print response bodies, auth values or provider diagnostics.
  console.error(`local Codex smoke failed: ${error instanceof assert.AssertionError ? 'contract assertion failed' : error.message}`)
  process.exitCode = 1
} finally {
  if (sessionID && !completed) {
    // A separate bounded client can stop only this runner's session after deadline.
    const cleanup = new AttachClient(client.origin, { stageMs: 15000 })
    cleanup.token = client.token
    try { await cleanup.request('POST', `/v3/sessions/${sessionID}/run/stop`, {}) }
    catch { console.error('owned smoke run stop could not be confirmed') }
  }
}
