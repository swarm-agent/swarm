#!/usr/bin/env node
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'

const argv = process.argv.slice(2)
const option = (name, fallback = '') => {
  const index = argv.indexOf(name)
  return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback
}

const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'codex')).trim().toLowerCase()
const modelOverride = String(option('--model', process.env.SWARM_RUNNER_MODEL || '')).trim()
const thinkingOverride = String(option('--thinking', process.env.SWARM_RUNNER_THINKING || 'high')).trim().toLowerCase()
const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || thinkingOverride)).trim().toLowerCase()
const designerModel = String(option('--designer-model', process.env.SWARM_RUNNER_DESIGNER_MODEL || '')).trim()
const designerThinking = String(option('--designer-thinking', process.env.SWARM_RUNNER_DESIGNER_THINKING || thinkingOverride)).trim().toLowerCase()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const heartbeatMs = 15000
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()
const selfTest = argv.includes('--self-test')
const stage = String(option('--stage', process.env.SWARM_RUNNER_STAGE || 'all')).trim().toLowerCase()
const sessionOverride = String(option('--session-id', process.env.SWARM_RUNNER_SESSION_ID || '')).trim()
const sourceSessionOverride = String(option('--source-session-id', process.env.SWARM_RUNNER_SOURCE_SESSION_ID || '')).trim()
const sourceCollectionOverride = String(option('--source-collection-id', process.env.SWARM_RUNNER_SOURCE_COLLECTION_ID || '')).trim()
const sourceVariantOverride = String(option('--source-variant-id', process.env.SWARM_RUNNER_SOURCE_VARIANT_ID || '')).trim()
const sourceEventSeqOverride = Number(option('--source-event-seq', process.env.SWARM_RUNNER_SOURCE_EVENT_SEQ || '0'))

if (!selfTest && (!apiURL || !/^https?:\/\//.test(apiURL))) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is invalid')
if (!Number.isFinite(timeoutMs) || timeoutMs < 300000 || timeoutMs > 600000) throw new Error('--timeout-ms must be between 300000 and 600000; split longer proofs into resumable stages')
if (!['root', 'regular3', 'focused', 'grouping', 'pinning', 'multi2', 'multi3', 'multi23', 'whole', 'managed', 'workspace', 'all'].includes(stage)) throw new Error('--stage must be root, regular3, focused, grouping, pinning, multi2, multi3, multi23, whole, managed, workspace, or all')
if (!['root', 'regular3', 'all', 'multi2', 'multi3', 'multi23'].includes(stage) && !sessionOverride) throw new Error('--session-id is required for this resumed stage')
if (stage === 'regular3' && (!actionModel || !designerModel)) throw new Error('regular3 requires --action-model and --designer-model from the canonical testbench wrapper')
if (![thinkingOverride, actionThinking, designerThinking].every((value) => ['off', 'low', 'medium', 'high', 'xhigh'].includes(value))) throw new Error('thinking levels must be off, low, medium, high, or xhigh')
if (['multi2', 'multi3', 'multi23'].includes(stage) && (!sourceSessionOverride || !sourceCollectionOverride || !sourceVariantOverride || !Number.isInteger(sourceEventSeqOverride) || sourceEventSeqOverride <= 0)) throw new Error('multi-target stages require --source-session-id, --source-collection-id, --source-variant-id, and --source-event-seq')

const testID = `designer-artifact-flow-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const stageStartedAt = Date.now()
const stageDeadline = stageStartedAt + timeoutMs
const partContract = [
  { id: 'part-1', label: 'Part 1 · Opening', kind: 'temporal', start_ms: 0, end_ms: 4000 },
  { id: 'part-2', label: 'Part 2 · Transformation', kind: 'temporal', start_ms: 4000, end_ms: 8000 },
  { id: 'part-3', label: 'Part 3 · Resolution', kind: 'temporal', start_ms: 8000, end_ms: 12000 },
]
const commonGates = ['provider_runnable', 'model_selected', 'workspace_bound', 'session_created']
const stageGates = {
  root: [...commonGates, 'root_single_html', 'root_three_parts'],
  regular3: [...commonGates, 'models_configured', 'regular_three_ready', 'regular_one_wave', 'regular_three_parts', 'regular_distinct_outputs', 'regular_no_failures', 'models_restored'],
  focused: [...commonGates, 'root_single_html', 'root_three_parts', 'focused_five_ready', 'focused_lineage', 'focused_grouping'],
  grouping: [...commonGates, 'iteration_groups_durable'],
  pinning: [...commonGates, 'in_progress_pin_durable'],
  multi2: [...commonGates, 'multi_target_ready', 'multi_target_lineage', 'multi_target_parts_preserved'],
  multi3: [...commonGates, 'multi_target_ready', 'multi_target_lineage', 'multi_target_parts_preserved'],
  multi23: [...commonGates, 'multi_target_ready', 'multi_target_lineage', 'multi_target_parts_preserved', 'multi_target_three_ready', 'multi_target_grouping'],
  whole: [...commonGates, 'focused_five_ready', 'focused_lineage', 'whole_five_ready', 'whole_lineage', 'whole_parts_preserved'],
  managed: [...commonGates, 'whole_five_ready', 'whole_lineage', 'managed_read_ready', 'managed_read_lineage'],
  workspace: [...commonGates, 'whole_five_ready', 'whole_lineage', 'workspace_designer_completed', 'workspace_file_visible'],
  all: [...commonGates, 'root_single_html', 'root_three_parts', 'focused_five_ready', 'focused_lineage', 'whole_five_ready', 'whole_lineage', 'whole_parts_preserved', 'managed_read_ready', 'managed_read_lineage', 'workspace_designer_completed', 'workspace_file_visible', 'no_zip_outputs', 'no_task_failures'],
}
const requiredGates = stageGates[stage]
const result = {
  result: 'NOT_DONE',
  test: 'designer-artifact-flow',
  test_id: testID,
  stage,
  started_at: new Date().toISOString(),
  api_url: apiURL,
  provider,
  model: {},
  ids: {},
  references: {},
  rounds: {},
  catalog_state: {},
  workspace_output: {},
  permissions_approved: [],
  gates: {},
  failures: [],
}
let token = suppliedToken
let assignment = null
let originalSwarmSettings = null
let originalDesignerSettings = null
let modelSettingsChanged = false

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[designer-artifact-flow] ${message}\n`)
const regular3Prompt = () => [
  `Create exactly three different managed animated HTML concepts for live E2E ${testID}.`,
  'Use one regular task wave with exactly three managed Designer launches, not an Iteration Swarm, Task Program, sequential launches, or replacement launches.',
  'Give the launches distinct assignments: orbital signal system, kinetic typographic relay, and modular architecture assembly.',
  'Every Designer must independently publish exactly one self-contained single-file text/html animation using animation_profile motion_ui.',
  'Every animation must use swarm.animation/v1 with duration_ms 12000 and fps 30 plus swarm.iteration/v1 with exactly three ordered temporal sections: part-1 "Part 1 · Opening" 0-4000ms; part-2 "Part 2 · Transformation" 4000-8000ms; part-3 "Part 3 · Resolution" 8000-12000ms.',
  'Each assignment must explicitly require a minimal parser-time runtime that binds before any DOM/Canvas/Path2D scene construction, a shared deterministic renderAt/ready/seek/stop timeline, a self-starting scheduler, a swarm-player/v1 bridge, and a direct fixed 1920x1080 stage at x=0/y=0 with no responsive scale wrapper.',
  'Every animation must render a substantive, intentional, legible opening composition at exactly time_ms=0; no blank, near-black, empty-background, or fade-in-only prelude is allowed because the trusted start-frame inspection occurs at t=0.',
  'Wait for that one task call to return all three ready exact artifact references, then finish successfully. Do not export, create a video project, relaunch a successful or failed slot, or make any additional managed artifact.',
].join(' ')
const fail = (message) => {
  result.failures.push(message)
  throw new Error(message)
}
const assert = (condition, message) => {
  if (!condition) fail(message)
}
const slug = (value) => String(value || '').trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')

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

function exactRef(item) {
  return {
    session_id: String(item?.session_id || ''),
    collection_id: String(item?.collection_id || ''),
    variant_id: String(item?.artifact_id || ''),
    event_seq: Number(item?.event_seq || 0),
  }
}

function validRef(ref) {
  return Boolean(ref.session_id && ref.collection_id && ref.variant_id && ref.event_seq > 0)
}

function sameSource(lineage, ref) {
  return String(lineage?.source_session_id || '') === ref.session_id
    && String(lineage?.source_collection_id || '') === ref.collection_id
    && String(lineage?.source_variant_id || '') === ref.variant_id
    && Number(lineage?.source_event_seq || 0) === ref.event_seq
}

function hasPartContract(item) {
  const byID = new Map((item?.parts || []).map((part) => [String(part?.id || ''), part]))
  return partContract.every((expected) => {
    const actual = byID.get(expected.id)
    return actual
      && String(actual.kind || '') === expected.kind
      && Number(actual.start_ms || 0) === expected.start_ms
      && Number(actual.end_ms || 0) === expected.end_ms
  })
}

function terminalStatus(status) {
  return ['completed', 'failed', 'cancelled', 'expired', 'interrupted'].includes(String(status || '').trim().toLowerCase())
}

function canonicalWorkspaceBinding(bindings, workspaces, workspaceOverride = '') {
  const byID = new Map((workspaces || []).filter((entry) =>
    String(entry?.workspace_id || '').trim()
      && Number(entry?.workspace_generation || 0) > 0
      && String(entry?.state || '').trim().toLowerCase() === 'active'
      && String(entry?.path || '').trim())
    .map((entry) => [String(entry.workspace_id).trim(), entry]))
  const requestedPath = String(workspaceOverride || '').trim()
  return (bindings || []).find((binding) => {
    if (String(binding?.state || '').trim().toLowerCase() !== 'bound' || !String(binding?.workspace_binding_id || '').trim()) return false
    const workspace = byID.get(String(binding?.source_workspace_id || '').trim())
    if (!workspace) return false
    const sourcePath = String(binding?.source_workspace_path || '').trim()
    if (!sourcePath || path.resolve(sourcePath) !== path.resolve(String(workspace.path || ''))) return false
    if (Number(binding?.source_workspace_generation || 0) !== Number(workspace.workspace_generation || 0)) return false
    return !requestedPath || path.resolve(requestedPath) === path.resolve(sourcePath) || path.resolve(requestedPath) === path.resolve(String(binding?.destination_workspace_path || ''))
  }) || null
}

async function hydrate(sessionID) {
  const response = await api('POST', '/v3/sync/hydrate', {
    surface: 'desktop',
    session_ids: [sessionID],
    history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' },
    resources: {
      messages: true,
      events: true,
      run_intents: true,
      current_run_state: true,
      session_view: true,
      active_plan: true,
      permission_summaries: true,
    },
    include_active: true,
  }, `hydrate ${sessionID}`)
  return response.body || {}
}

async function approvePending(sessionID) {
  const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`, undefined, 'list pending permissions')
  const pending = response.body?.permissions || []
  if (pending.length === 0) return 0
  const approved = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/resolve_all`, {
    action: 'allow_once',
    reason: `${testID} checked-in live E2E approval`,
    limit: 50,
  }, 'approve pending E2E permissions')
  for (const record of approved.body?.resolved || []) {
    result.permissions_approved.push({ id: record?.id, tool_name: record?.tool_name, run_id: record?.run_id })
  }
  return Number(approved.body?.count || 0)
}

async function waitForTurn(sessionID, runID, label) {
  const startedAt = Date.now()
  const deadline = stageDeadline
  let latest = null
  let nextHeartbeatAt = startedAt
  let lastStatus = ''
  while (Date.now() < deadline) {
    const approvals = await approvePending(sessionID)
    latest = await hydrate(sessionID)
    const intents = latest.run_intents_by_session?.[sessionID] || []
    const intent = intents.find((candidate) => String(candidate?.run_id || '') === runID)
    const failed = intents.filter((candidate) => /failed|cancelled|expired|interrupted/.test(String(candidate?.status || '').toLowerCase()))
    if (failed.length > 0) fail(`${label} has failed run intent(s): ${failed.map((item) => `${item.run_id}:${item.status}`).join(', ')}`)
    const status = String(intent?.status || 'pending')
    if (status !== lastStatus || Date.now() >= nextHeartbeatAt) {
      const elapsed = Math.floor((Date.now() - startedAt) / 1000)
      const artifacts = (await catalog(sessionID)).filter((item) => item?.session_id === sessionID)
      const ready = artifacts.filter((item) => item?.status === 'ready').length
      const staging = artifacts.filter((item) => item?.status === 'staging').length
      log(`${label} progress elapsed=${elapsed}s run=${runID} status=${status} ready_artifacts=${ready} staging_artifacts=${staging} approvals=${approvals}`)
      lastStatus = status
      nextHeartbeatAt = Date.now() + heartbeatMs
    }
    if (intent && terminalStatus(intent.status)) {
      const messages = latest.messages_by_session?.[sessionID] || []
      const assistant = [...messages].reverse().find((message) => String(message?.role || '').toLowerCase() === 'assistant')
      return { snapshot: latest, intent, assistant }
    }
    await sleep(1500)
  }
  fail(`${label} timed out after ${Math.floor(timeoutMs / 1000)}s waiting for run ${runID}; inspect the durable session before retrying`)
}

async function postTurn(sessionID, label, content, artifactSelections = []) {
  log(label)
  const response = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:${label}:${crypto.randomBytes(3).toString('hex')}`,
    role: 'user',
    content,
    artifact_selections: artifactSelections,
    metadata: { runner_test: 'designer-artifact-flow', runner_test_id: testID, stage: label },
  }, `post ${label}`)
  const runID = String(response.body?.run_intent?.run_id || response.body?.run_id || '')
  assert(runID, `${label} returned no run id`)
  result.ids[`${label}_run_id`] = runID
  return waitForTurn(sessionID, runID, label)
}

async function catalog(sessionID) {
  const response = await api('GET', `/v3/artifacts?limit=2000&session_id=${encodeURIComponent(sessionID)}`, undefined, 'read artifact catalog')
  return response.body?.artifacts || []
}

async function bootstrapSessions() {
  const response = await api('POST', '/v3/sync/bootstrap', {
    surface: 'desktop',
    selector: { kind: 'global', global: true, recent: { limit: 200 } },
    history: { mode: 'none' },
    resources: { messages: false, events: false, run_intents: false, current_run_state: true, active_plan: true, plan_revisions: false, permission_summaries: true },
    include_active: true,
  }, 'bootstrap delegated Designer sessions')
  return response.body || {}
}

function delegatedDesigners(bootstrap, parentID) {
  return Object.values(bootstrap.sessions_by_id || {}).filter((session) =>
    String(session?.metadata?.parent_session_id || '') === parentID &&
    String(session?.metadata?.lineage_kind || '') === 'delegated_subagent' &&
    String(session?.metadata?.requested_subagent || '').trim().toLowerCase() === 'designer')
}

async function artifactDigest(sessionID, artifactID) {
  const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/artifacts/${encodeURIComponent(artifactID)}`, undefined, `read artifact ${artifactID}`)
  return crypto.createHash('sha256').update(response.text).digest('hex')
}

async function waitForArtifacts(sessionID, predicate, count, label) {
  const startedAt = Date.now()
  const deadline = stageDeadline
  let matches = []
  let nextHeartbeatAt = startedAt
  let lastCount = -1
  while (Date.now() < deadline) {
    const artifacts = await catalog(sessionID)
    matches = artifacts.filter((item) => item?.status === 'ready' && predicate(item))
    const staging = artifacts.filter((item) => item?.status === 'staging' && predicate(item)).length
    const failed = artifacts.filter((item) => item?.status === 'failed' && predicate(item))
    if (failed.length > 0) {
      fail(`${label} recorded a durable failed artifact: ${failed.map((item) => `${item?.artifact_id || 'unknown'}:${item?.failure_code || 'unknown_failure'}`).join(',')}`)
    }
    if (matches.length !== lastCount || Date.now() >= nextHeartbeatAt) {
      log(`${label} progress elapsed=${Math.floor((Date.now() - startedAt) / 1000)}s ready=${matches.length}/${count} staging=${staging}`)
      lastCount = matches.length
      nextHeartbeatAt = Date.now() + heartbeatMs
    }
    if (matches.length >= count) return matches
    await sleep(2000)
  }
  fail(`${label} timed out after ${Math.floor(timeoutMs / 1000)}s with ${matches.length}/${count} ready artifacts; inspect the durable session before retrying`)
}

async function selectModel() {
  const providers = await api('GET', '/v1/providers', undefined, 'read providers')
  const providerStatus = (providers.body?.providers || []).find((item) => String(item?.id || '').toLowerCase() === provider)
  assert(providerStatus && providerStatus.runnable !== false, `provider ${provider} is not runnable`)
  result.gates.provider_runnable = true

  const response = await api('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'read model catalog')
  const records = response.body?.records || []
  const selectedModel = stage === 'regular3' ? actionModel : modelOverride
  const selectedThinking = stage === 'regular3' ? actionThinking : thinkingOverride
  let record = selectedModel ? records.find((candidate) => String(candidate?.model || '') === selectedModel) : null
  if (!record && stage !== 'regular3') {
    record = records.find((candidate) => (candidate?.recommendations || []).some((rec) => ['auto', 'main'].includes(String(rec?.role || '').toLowerCase()))) || records[0]
  }
  assert(record?.model, selectedModel ? `model catalog does not contain ${provider}/${selectedModel}` : `no model available for ${provider}`)
  const thinkingOptions = Array.isArray(record.thinking_options) ? record.thinking_options.map((value) => String(value).toLowerCase()) : []
  const thinking = thinkingOptions.includes(selectedThinking) ? selectedThinking : String(record.default_thinking || thinkingOptions[0] || 'low').toLowerCase()
  assignment = { provider, model: String(record.model), thinking }
  result.model = assignment
  if (stage === 'regular3') {
    const designerRecord = records.find((candidate) => String(candidate?.model || '') === designerModel)
    assert(designerRecord?.model, `model catalog does not contain ${provider}/${designerModel} for Designer`)
    const designerOptions = Array.isArray(designerRecord.thinking_options) ? designerRecord.thinking_options.map((value) => String(value).toLowerCase()) : []
    const resolvedDesignerThinking = designerOptions.includes(designerThinking) ? designerThinking : String(designerRecord.default_thinking || designerOptions[0] || 'low').toLowerCase()
    result.model = { action: assignment, designer: { provider, model: String(designerRecord.model), thinking: resolvedDesignerThinking } }
  }
  result.gates.model_selected = true
}

async function main() {
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
    token = String(auth.body?.token || '').trim()
    assert(token, 'desktop authentication returned no token')
  }
  await selectModel()
  if (stage === 'regular3') {
    const settings = (await api('GET', '/v1/agent-model-settings', undefined, 'read Designer flow model settings')).body?.agent_model_settings || {}
    originalSwarmSettings = settings.swarm || null
    originalDesignerSettings = settings.system_agents?.designer || null
    assert(originalSwarmSettings && originalDesignerSettings, 'canonical Swarm or Designer model setting is missing')
    modelSettingsChanged = true
    await api('PATCH', '/v1/agent-model-settings', { swarm: { action: assignment, plan: originalSwarmSettings.plan || assignment } }, 'configure regular3 Swarm model')
    await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer: result.model.designer } }, 'configure regular3 Designer model')
    const configured = (await api('GET', '/v1/agent-model-settings', undefined, 'verify regular3 model settings')).body?.agent_model_settings || {}
    assert(configured?.swarm?.action?.model === assignment.model && configured?.system_agents?.designer?.model === result.model.designer.model, 'regular3 model settings did not persist')
    result.gates.models_configured = true
  }

  const topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const workspaceCatalog = (await api('GET', '/v1/workspace/list?limit=200', undefined, 'read workspace catalog')).body?.workspaces || []
  const runtime = (topology.runtimes || []).find((item) => item?.relationship === 'self') || (topology.runtimes || [])[0]
  const bindings = topology.workspace_bindings || []
  const binding = canonicalWorkspaceBinding(bindings, workspaceCatalog, workspacePathOverride)
  assert(runtime?.swarm_id, 'topology has no self runtime')
  assert(binding?.workspace_binding_id, workspacePathOverride
    ? `topology has no current canonical bound workspace for ${workspacePathOverride}`
    : 'topology has no current canonical bound workspace; stale source workspace generations are not eligible')
  const workspacePath = String(binding.source_workspace_path || binding.destination_workspace_path || '')
  assert(workspacePath, 'workspace binding has no path')
  result.ids.workspace_binding_id = binding.workspace_binding_id
  result.workspace_output.workspace_name = String(binding.source_workspace_name || binding.destination_workspace_name || 'workspace')
  result.gates.workspace_bound = true

  let sessionID = sessionOverride
  const createStageSession = !sessionID && (stage === 'root' || stage === 'regular3' || stage === 'all' || stage === 'multi2' || stage === 'multi3' || stage === 'multi23')
  if (createStageSession) {
    const created = await api('POST', '/v3/sessions', {
      client_request_id: `${testID}:create`,
      title: `${testID} Designer artifact proof`,
      workspace_path: workspacePath,
      workspace_name: String(binding.source_workspace_name || 'designer-e2e'),
      workspace_binding_id: binding.workspace_binding_id,
      swarm_id: runtime.swarm_id,
      target_kind: 'host',
      target_relationship: 'self',
      mode: 'auto',
      agent_name: 'swarm',
      preference: assignment,
      model_profile: { temporary: { ...assignment, name: `${testID} temporary model` } },
      metadata: { runner_test: 'designer-artifact-flow', runner_test_id: testID },
    }, 'create E2E session')
    sessionID = String(created.body?.session?.id || '')
    assert(sessionID, 'session creation returned no ID')
  } else if (sessionID) {
    const existing = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}`, undefined, 'read resumed E2E session')
    assert(String(existing.body?.session?.id || existing.body?.id || '') === sessionID, 'resumed session was not found')
  }
  result.ids.session_id = sessionID
  result.ids.desktop_path = `/${slug(binding.source_workspace_name || 'workspace')}/${sessionID}`
  result.gates.session_created = true

  if (stage === 'regular3') {
    const before = delegatedDesigners(await bootstrapSessions(), sessionID)
    assert(before.length === 0, `fresh regular3 session already has ${before.length} delegated Designer child session(s)`)
    await postTurn(sessionID, 'regular3', regular3Prompt())
    const artifacts = await catalog(sessionID)
    const candidates = artifacts.filter((item) => item?.session_id === sessionID && item?.media_type === 'text/html' && String(item?.lineage?.task_call_id || '').trim())
    const ready = candidates.filter((item) => item?.status === 'ready')
    const failed = candidates.filter((item) => item?.status === 'failed' || item?.status === 'unavailable')
    assert(ready.length === 3, `regular3 produced ${ready.length}/3 ready animations; candidates=${candidates.map((item) => `${item?.artifact_id || 'unknown'}:${item?.status || 'unknown'}:${item?.failure_code || 'none'}`).join(',') || 'none'}`)
    assert(failed.length === 0, `regular3 recorded failed/unavailable managed candidates: ${failed.map((item) => `${item?.artifact_id}:${item?.failure_code || item?.status}`).join(',')}`)
    assert(ready.every(hasPartContract), 'regular3 did not preserve the exact three-part temporal contract in every animation')
    const taskCalls = new Set(ready.map((item) => String(item?.lineage?.task_call_id || '').trim()).filter(Boolean))
    const childIDs = new Set(ready.map((item) => String(item?.lineage?.child_session_id || '').trim()).filter(Boolean))
    assert(taskCalls.size === 1, `regular3 artifacts came from ${taskCalls.size} task calls instead of one`)
    assert(childIDs.size === 3, `regular3 artifacts came from ${childIDs.size} child sessions instead of three`)
    const taskCallID = [...taskCalls][0]
    const children = delegatedDesigners(await bootstrapSessions(), sessionID)
    const waveChildren = children.filter((child) => String(child?.metadata?.parent_task_call_id || '') === taskCallID)
    const artifactChildIDs = new Set([...childIDs])
    assert(children.length === 3 && waveChildren.length === 3, `regular3 created ${children.length} Designer children total and ${waveChildren.length} in the artifact task call; want exactly three once`)
    assert(waveChildren.every((child) => artifactChildIDs.has(String(child?.id || ''))), 'regular3 task-call children do not exactly match artifact child lineage')
    const digests = await Promise.all(ready.map((item) => artifactDigest(sessionID, item.artifact_id)))
    assert(new Set(digests).size === 3, `regular3 animations are not byte-distinct: ${digests.join(',')}`)
    result.rounds.regular3 = ready.map((item, index) => ({ reference: exactRef(item), digest_sha256: digests[index], child_session_id: item?.lineage?.child_session_id, task_call_id: item?.lineage?.task_call_id, parts: item.parts }))
    result.gates.regular_three_ready = true
    result.gates.regular_one_wave = true
    result.gates.regular_three_parts = true
    result.gates.regular_distinct_outputs = true
    result.gates.regular_no_failures = true
    return
  }

  if (stage === 'grouping' || stage === 'pinning') {
    const artifacts = (await catalog(sessionID)).filter((item) => item?.session_id === sessionID || item?.lineage?.parent_session_id === sessionID)
    const iterationGroups = new Map()
    for (const item of artifacts) {
      const groupID = String(item?.lineage?.iteration_group_id || item?.composition?.iteration_group_id || '').trim()
      if (!groupID) continue
      iterationGroups.set(groupID, [...(iterationGroups.get(groupID) || []), item])
    }
    result.catalog_state.iteration_groups = [...iterationGroups.entries()].map(([group_id, entries]) => ({
      group_id,
      count: entries.length,
      collections: [...new Set(entries.map((item) => String(item?.collection_id || '')).filter(Boolean))],
      chains: [...new Set(entries.map((item) => String(item?.artifact_chain_id || '')).filter(Boolean))],
      statuses: entries.map((item) => `${item?.artifact_id || 'unknown'}:${item?.status || 'unknown'}`),
    }))
    if (stage === 'grouping') {
      assert(result.catalog_state.iteration_groups.some((group) => group.count > 1), `catalog has no durable multi-candidate iteration group; groups=${JSON.stringify(result.catalog_state.iteration_groups)}`)
      result.gates.iteration_groups_durable = true
      result.result = 'PASS'
      return
    }
    const focusedEntries = artifacts.filter((item) => item?.media_type === 'text/html' && (String(item?.lineage?.selected_review_target_ids || '').trim() || String(item?.lineage?.part_id || item?.lineage?.iteration_section_id || '').trim()) && String(item?.artifact_chain_id || '').trim())
      .sort((a, b) => Number(a?.event_seq || 0) - Number(b?.event_seq || 0))
    assert(focusedEntries.length > 0, 'catalog has no durable focused-part revision to pin as In Progress')
    const pinnedChainID = String(focusedEntries[0].artifact_chain_id)
    const pinnedEntries = focusedEntries.filter((item) => String(item?.artifact_chain_id || '') === pinnedChainID)
    const pinnedHead = [...pinnedEntries].filter((item) => item?.status === 'ready')
      .sort((a, b) => Number(b?.revision_number || 0) - Number(a?.revision_number || 0) || Number(b?.event_seq || 0) - Number(a?.event_seq || 0))[0]
    assert(pinnedHead, `earliest focused chain ${pinnedChainID} has no ready authoritative head`)
    result.catalog_state.in_progress_chain_id = pinnedChainID
    result.catalog_state.in_progress_candidates = [exactRef(pinnedHead)]
    result.catalog_state.focused_chain_ids = [...new Set(focusedEntries.map((item) => String(item?.artifact_chain_id || '')).filter(Boolean))]
    result.gates.in_progress_pin_durable = true
    result.result = 'PASS'
    return
  }

  if (stage === 'multi2' || stage === 'multi3' || stage === 'multi23') {
    const sourceRef = {
      session_id: sourceSessionOverride,
      collection_id: sourceCollectionOverride,
      variant_id: sourceVariantOverride,
      event_seq: sourceEventSeqOverride,
    }
    const targetCount = stage === 'multi3' ? 3 : 2
    const candidateCount = stage === 'multi23' ? 3 : 1
    const selectedTargets = stage === 'multi3' ? partContract : stage === 'multi23' ? [partContract[1], partContract[2]] : [partContract[0], partContract[2]]
    const targetIDs = selectedTargets.map((item) => item.id)
    const selection = { ...sourceRef, action: 'use' }
    const prompt = [
      `Use the selected exact monolithic HTML artifact and remake exactly these ${targetCount} locator targets together: ${targetIDs.join(', ')}.`,
      `Launch one managed Designer Iteration Swarm with count=${candidateCount}, source_artifact set to the exact selection, section_targets set to the complete exact ${targetCount}-target list including kind/start_ms/end_ms, and animation_profile motion_ui.`,
      'The Designer must inspect the exact complete HTML, change every selected target atomically, preserve every non-target target plus all canonical IDs/timings, and publish exactly one complete text/html revision with one manage_artifact create call.',
      'Do not create or modify a plan. Launch the one Designer swarm directly in this turn.',
      'Do not use read_parts, publish_parts, read_part, publish_part, create_package, ZIP, initial_parts, workspace output, or retries through multipart tools.',
      `Wait for all ${candidateCount} ready candidate${candidateCount === 1 ? '' : 's'} and finish.`,
    ].join(' ')
    await postTurn(sessionID, stage, prompt, [selection])
    const targetKey = targetIDs.join(',')
    const artifacts = await catalog(sessionID)
    const sourceCandidates = artifacts.filter((item) => sameSource(item.lineage, sourceRef)
      && String(item?.lineage?.selected_review_target_ids || '') === targetKey)
    const candidates = sourceCandidates.filter((item) => item?.status === 'ready' && item.media_type === 'text/html')
    assert(candidates.length >= candidateCount, `${stage} terminal run produced ${candidates.length}/${candidateCount} ready complete candidates; target candidates=${sourceCandidates.map((item) => `${item.artifact_id}:${item.status}:${item.failure_code || 'none'}`).join(',') || 'none'}`)
    const orderedCandidates = candidates.sort((a, b) => Number(a.candidate_index || 0) - Number(b.candidate_index || 0) || Number(a.event_seq || 0) - Number(b.event_seq || 0)).slice(0, candidateCount)
    const candidate = orderedCandidates[0]
    assert(orderedCandidates.every(hasPartContract), `${stage} candidate did not preserve the canonical part contract`)
    result.references.source = sourceRef
    result.references.multi_target = exactRef(candidate)
    result.rounds.multi_target = orderedCandidates.map((item) => ({ reference: exactRef(item), candidate_index: item.candidate_index, revision_round_id: item.revision_round_id, lineage: item.lineage, parts: item.parts }))
    result.gates.multi_target_ready = true
    result.gates.multi_target_lineage = orderedCandidates.every((item) => sameSource(item.lineage, sourceRef) && String(item.lineage?.selected_review_target_ids || '') === targetKey)
    result.gates.multi_target_parts_preserved = orderedCandidates.every(hasPartContract)
    if (stage === 'multi23') {
      const indexes = new Set(orderedCandidates.map((item) => Number(item.candidate_index || 0)))
      const groups = new Set(orderedCandidates.map((item) => String(item?.lineage?.iteration_group_id || item?.composition?.iteration_group_id || '')).filter(Boolean))
      result.gates.multi_target_three_ready = orderedCandidates.length === 3 && indexes.size === 3
      result.gates.multi_target_grouping = groups.size === 1
      assert(result.gates.multi_target_three_ready, `multi23 candidates do not expose three distinct candidate indexes: ${[...indexes].join(',')}`)
      assert(result.gates.multi_target_grouping, `multi23 candidates do not share one durable iteration group: ${[...groups].join(',') || 'none'}`)
    }
    result.result = 'PASS'
    return
  }

  if (stage === 'root' || stage === 'all') {
    const rootPrompt = [
      `Create the root artifact for live E2E ${testID}.`,
      'Create exactly one ready managed artifact: a self-contained, single-file text/html animated presentation.',
      'It must use swarm.animation/v1 with duration_ms 12000 and fps 30, plus swarm.iteration/v1 with exactly these ordered sections:',
      'part-1 "Part 1 · Opening" 0-4000ms; part-2 "Part 2 · Transformation" 4000-8000ms; part-3 "Part 3 · Resolution" 8000-12000ms.',
      'Install the required deterministic renderAt/ready/seek APIs, self-starting scheduler, swarm-player/v1 bridge, and visible section buttons using those exact IDs and boundaries.',
      'The trusted viewport audit is strict: set html/body margin 0, width/height 100%, overflow hidden, and border-box sizing; keep every visible element and pseudo-element fully inside 1920x1080 at all representative timestamps. Use opacity or inset clipping for transitions instead of translating visible content beyond viewport bounds, and keep shadows/glows inset from every edge.',
      'Keep the artifact monolithic text/html. Use one manage_artifact create call with top-level content and animation_profile motion_ui so the trusted publication preflight must pass before ready. Do not use initial_parts, multipart tools, create_package, ZIP, workspace files, or task delegation.',
      'Omit top-level parts so the server must derive the three temporal edit targets from the iteration manifest.',
      'After the artifact is ready, finish the turn without creating extra artifacts.',
    ].join(' ')
    await postTurn(sessionID, 'root', rootPrompt)
  }
  const roots = await waitForArtifacts(sessionID, (item) => item.media_type === 'text/html' && !item?.lineage?.source_variant_id && hasPartContract(item), 1, 'root artifact')
  const root = roots.sort((a, b) => Number(b.event_seq || 0) - Number(a.event_seq || 0))[0]
  const rootRef = exactRef(root)
  assert(validRef(rootRef), 'root artifact has no exact reference')
  assert(root.media_type === 'text/html' && !String(root.filename || '').endsWith('.zip'), 'root artifact was converted from single-file HTML')
  result.references.root = rootRef
  result.gates.root_single_html = true
  result.gates.root_three_parts = true
  if (stage === 'root') {
    result.result = 'PASS'
    return
  }

  const focusedSelection = {
    ...rootRef,
    action: 'use',
    part_id: 'part-2',
    part: partContract[1],
  }
  if (stage === 'focused' || stage === 'all') {
    const focusedPrompt = [
      'Use the selected exact artifact and selected Part 2 target.',
      'Launch one Designer Iteration Swarm with count=5, source_artifact set to the exact selection, section_target exactly part-2, animation_profile motion_ui, and focused iteration_controls.',
      'Each Designer must inspect the exact monolithic HTML, change only Part 2 visual treatment, preserve Parts 1 and 3 plus all IDs/times, and publish exactly one complete text/html revision with one manage_artifact create call.',
      'Do not use read_part, publish_part, multipart payloads, create_package, ZIP, workspace output, or generic unattached Designer launches.',
      'Wait for all five candidates and then finish.',
    ].join(' ')
    await postTurn(sessionID, 'focused', focusedPrompt, [focusedSelection])
  }
  const focused = await waitForArtifacts(sessionID, (item) => sameSource(item.lineage, rootRef)
    && String(item?.lineage?.iteration_section_id || item?.targeted_part_id || '') === 'part-2', 5, 'focused candidates')
  const focusedRound = focused.sort((a, b) => Number(a.candidate_index || 0) - Number(b.candidate_index || 0)).slice(0, 5)
  assert(new Set(focusedRound.map((item) => Number(item.candidate_index || 0))).size === 5, 'focused candidates do not have five distinct indexes')
  assert(focusedRound.every((item) => item.media_type === 'text/html' && hasPartContract(item)), 'focused candidates did not preserve complete HTML/part identities')
  result.rounds.focused = focusedRound.map((item) => ({ reference: exactRef(item), candidate_index: item.candidate_index, revision_round_id: item.revision_round_id, targeted_part_id: item.targeted_part_id, lineage: item.lineage }))
  result.gates.focused_five_ready = true
  result.gates.focused_lineage = focusedRound.every((item) => sameSource(item.lineage, rootRef))
  const focusedGroupIDs = new Set(focusedRound.map((item) => String(item?.lineage?.iteration_group_id || item?.composition?.iteration_group_id || '')).filter(Boolean))
  result.gates.focused_grouping = focusedGroupIDs.size === 1
  assert(result.gates.focused_grouping, `focused candidates do not share one durable iteration group: ${[...focusedGroupIDs].join(',') || 'none'}`)
  if (stage === 'focused') {
    result.result = 'PASS'
    return
  }

  const selectedFocused = focusedRound[0]
  const selectedFocusedRef = exactRef(selectedFocused)
  result.references.selected_focused = selectedFocusedRef
  const wholeSelection = { ...selectedFocusedRef, action: 'use' }
  if (stage === 'whole' || stage === 'all') {
    const wholePrompt = [
      'Take the selected focused candidate as the exact source for a new whole-revision round.',
      'Launch one Designer Iteration Swarm with count=5 and this exact source_artifact, but do not pass section_target or section_targets.',
      'Each Designer must read the complete selected HTML and remake the entire animation as a distinct full concept while preserving the canonical three IDs, labels, exact timing boundaries, swarm.animation/v1 and swarm.iteration/v1 contracts.',
      'Each must publish one complete single-file text/html revision with one manage_artifact create call and exact source lineage. No ZIP, package, multipart, or workspace output.',
      'Wait for all five complete revisions and finish.',
    ].join(' ')
    await postTurn(sessionID, 'whole', wholePrompt, [wholeSelection])
  }
  const whole = await waitForArtifacts(sessionID, (item) => sameSource(item.lineage, selectedFocusedRef)
    && !String(item?.lineage?.iteration_section_id || '') && !String(item?.targeted_part_id || ''), 5, 'whole candidates')
  const wholeRound = whole.sort((a, b) => Number(a.candidate_index || 0) - Number(b.candidate_index || 0)).slice(0, 5)
  assert(new Set(wholeRound.map((item) => Number(item.candidate_index || 0))).size === 5, 'whole candidates do not have five distinct indexes')
  assert(wholeRound.every((item) => item.media_type === 'text/html' && hasPartContract(item)), 'whole candidates did not preserve canonical part identities')
  result.rounds.whole = wholeRound.map((item) => ({ reference: exactRef(item), candidate_index: item.candidate_index, revision_round_id: item.revision_round_id, lineage: item.lineage, parts: item.parts }))
  result.gates.whole_five_ready = true
  result.gates.whole_lineage = wholeRound.every((item) => sameSource(item.lineage, selectedFocusedRef))
  result.gates.whole_parts_preserved = wholeRound.every(hasPartContract)
  if (stage === 'whole') {
    result.result = 'PASS'
    return
  }

  const selectedWhole = wholeRound[0]
  const selectedWholeRef = exactRef(selectedWhole)
  result.references.selected_whole = selectedWholeRef
  if (stage === 'managed' || stage === 'all') {
    const managedPrompt = [
      'Use the selected exact whole-revision artifact.',
      'Launch exactly one regular managed Designer. It must read/inspect the complete exact source, verify all three canonical animation sections are present, make one subtle typography refinement, and publish one complete text/html revision with exact source lineage.',
      'Do not use focused-part or multipart tools, ZIP, workspace mode, or more than one Designer.',
      'Wait for it and finish.',
    ].join(' ')
    await postTurn(sessionID, 'managed_read', managedPrompt, [{ ...selectedWholeRef, action: 'use' }])
    const managedRead = await waitForArtifacts(sessionID, (item) => sameSource(item.lineage, selectedWholeRef) && item.media_type === 'text/html', 1, 'managed read revision')
    const managedItem = managedRead.sort((a, b) => Number(b.event_seq || 0) - Number(a.event_seq || 0))[0]
    assert(hasPartContract(managedItem), 'managed read Designer did not preserve the three-part contract')
    result.references.managed_read = exactRef(managedItem)
    result.gates.managed_read_ready = true
    result.gates.managed_read_lineage = sameSource(managedItem.lineage, selectedWholeRef)
    if (stage === 'managed') {
      result.result = 'PASS'
      return
    }
  }

  const workspaceRelative = `.tmp/${testID}/workspace-designer.html`
  const workspacePrompt = [
    'Use the selected exact managed HTML artifact.',
    `Launch exactly one regular Designer in output_mode=workspace with owned_scope exactly ["${workspaceRelative}"].`,
    'Pass the exact source_artifact. The backend must materialize the exact single-file HTML into that one target before the Designer runs.',
    `The workspace Designer must read the materialized HTML, preserve all three canonical IDs and timing boundaries, add the visible text marker "WORKSPACE_DESIGNER_E2E_OK_${testID}", and save the same HTML file in place.`,
    'It must not use manage_artifact, Git, Bash, ZIP, package conversion, multipart tools, or any other workspace path.',
    'Wait for it and finish.',
  ].join(' ')
  await postTurn(sessionID, 'workspace', workspacePrompt, [{ ...selectedWholeRef, action: 'use' }])
  const workspaceTarget = path.resolve(workspacePath, workspaceRelative)
  assert(workspaceTarget.startsWith(`${path.resolve(workspacePath)}${path.sep}`), 'workspace Designer target escaped the bound workspace')
  const workspaceContent = fs.readFileSync(workspaceTarget, 'utf8')
  assert(workspaceContent.includes(`WORKSPACE_DESIGNER_E2E_OK_${testID}`), 'workspace Designer output marker is missing')
  assert(partContract.every((part) => workspaceContent.includes(part.id)), 'workspace Designer output lost canonical part IDs')
  result.workspace_output.relative_path = workspaceRelative
  result.workspace_output.marker = `WORKSPACE_DESIGNER_E2E_OK_${testID}`
  result.gates.workspace_designer_completed = true
  result.gates.workspace_file_visible = true
  if (stage === 'workspace') {
    result.result = 'PASS'
    return
  }

  const allArtifacts = await catalog(sessionID)
  const testArtifacts = allArtifacts.filter((item) => item?.session_id === sessionID)
  result.gates.no_zip_outputs = testArtifacts.every((item) => item?.media_type !== 'application/zip' && !String(item?.filename || '').endsWith('.zip'))
  const finalSnapshot = await hydrate(sessionID)
  const events = finalSnapshot.events_by_session?.[sessionID] || []
  const failureEvents = events.filter((event) => /failed|cancelled|expired|interrupted/.test(String(event?.event_type || '').toLowerCase()))
  assert(failureEvents.length === 0, `session recorded failure events: ${failureEvents.map((event) => event.event_type).join(', ')}`)
  result.gates.no_task_failures = true
  result.result = requiredGates.every((gate) => result.gates[gate] === true) ? 'PASS' : 'NOT_DONE'
}

try {
  if (selfTest) {
    assert(stage === 'regular3', '--self-test requires --stage regular3')
    const prompt = regular3Prompt()
    assert(prompt.includes('one regular task wave with exactly three managed Designer launches'), 'regular3 prompt lost one-wave topology')
    assert(prompt.includes('orbital signal system') && prompt.includes('kinetic typographic relay') && prompt.includes('modular architecture assembly'), 'regular3 prompt lost distinct assignments')
    assert(partContract.length === 3 && prompt.includes('part-1') && prompt.includes('part-2') && prompt.includes('part-3'), 'regular3 prompt lost three-part contract')
    assert(prompt.includes('binds before any DOM/Canvas/Path2D scene construction') && prompt.includes('direct fixed 1920x1080 stage at x=0/y=0') && prompt.includes('no responsive scale wrapper'), 'regular3 prompt lost bounded parser-time or viewport authoring guidance')
    assert(prompt.includes('opening composition at exactly time_ms=0') && prompt.includes('no blank, near-black, empty-background, or fade-in-only prelude'), 'regular3 prompt lost start-frame inspection guidance')
    const workspace = { workspace_id: 'workspace-live', workspace_generation: 2, state: 'active', path: '/workspace/live' }
    const stale = { workspace_binding_id: 'binding-stale', source_workspace_id: workspace.workspace_id, source_workspace_generation: 1, source_workspace_path: workspace.path, destination_workspace_path: workspace.path, state: 'bound' }
    const current = { ...stale, workspace_binding_id: 'binding-live', source_workspace_generation: 2 }
    assert(canonicalWorkspaceBinding([stale, current], [workspace])?.workspace_binding_id === 'binding-live', 'regular3 workspace selection did not exclude a stale source generation')
    assert(['off', 'low', 'medium', 'high', 'xhigh'].includes(designerThinking), 'regular3 rejected a canonical Designer thinking setting')
    result.gates = Object.fromEntries(requiredGates.map((gate) => [gate, true]))
    result.result = 'PASS'
  } else {
    await main()
  }
} catch (error) {
  result.error = error?.stack || String(error)
  log(result.error)
} finally {
  let restoreFailed = false
  if (modelSettingsChanged) {
    try {
      if (originalDesignerSettings) await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer: originalDesignerSettings } }, 'restore Designer model setting')
      if (originalSwarmSettings) await api('PATCH', '/v1/agent-model-settings', { swarm: originalSwarmSettings }, 'restore Swarm model setting')
    } catch (error) {
      result.failures.push(`failed to restore model settings: ${error?.message || error}`)
      restoreFailed = true
    }
    if (!restoreFailed) result.gates.models_restored = true
  }
  if (stage === 'regular3' && !restoreFailed && requiredGates.every((gate) => result.gates[gate] === true)) result.result = 'PASS'
  if (restoreFailed) result.result = 'NOT_DONE'
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((gate) => result.gates[gate] !== true)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
