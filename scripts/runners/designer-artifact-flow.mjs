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
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const heartbeatMs = 15000
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()
const stage = String(option('--stage', process.env.SWARM_RUNNER_STAGE || 'all')).trim().toLowerCase()
const sessionOverride = String(option('--session-id', process.env.SWARM_RUNNER_SESSION_ID || '')).trim()
const sourceSessionOverride = String(option('--source-session-id', process.env.SWARM_RUNNER_SOURCE_SESSION_ID || '')).trim()
const sourceCollectionOverride = String(option('--source-collection-id', process.env.SWARM_RUNNER_SOURCE_COLLECTION_ID || '')).trim()
const sourceVariantOverride = String(option('--source-variant-id', process.env.SWARM_RUNNER_SOURCE_VARIANT_ID || '')).trim()
const sourceEventSeqOverride = Number(option('--source-event-seq', process.env.SWARM_RUNNER_SOURCE_EVENT_SEQ || '0'))

if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!provider || !/^[a-z0-9._-]+$/.test(provider)) throw new Error('--provider is invalid')
if (!Number.isFinite(timeoutMs) || timeoutMs < 300000 || timeoutMs > 600000) throw new Error('--timeout-ms must be between 300000 and 600000; split longer proofs into resumable stages')
if (!['root', 'focused', 'multi2', 'multi3', 'whole', 'managed', 'workspace', 'all'].includes(stage)) throw new Error('--stage must be root, focused, multi2, multi3, whole, managed, workspace, or all')
if (!['root', 'all', 'multi2', 'multi3'].includes(stage) && !sessionOverride) throw new Error('--session-id is required for this resumed stage')
if (['multi2', 'multi3'].includes(stage) && (!sourceSessionOverride || !sourceCollectionOverride || !sourceVariantOverride || !Number.isInteger(sourceEventSeqOverride) || sourceEventSeqOverride <= 0)) throw new Error('multi-target stages require --source-session-id, --source-collection-id, --source-variant-id, and --source-event-seq')

const testID = `designer-artifact-flow-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const partContract = [
  { id: 'part-1', label: 'Part 1 · Opening', kind: 'temporal', start_ms: 0, end_ms: 4000 },
  { id: 'part-2', label: 'Part 2 · Transformation', kind: 'temporal', start_ms: 4000, end_ms: 8000 },
  { id: 'part-3', label: 'Part 3 · Resolution', kind: 'temporal', start_ms: 8000, end_ms: 12000 },
]
const commonGates = ['provider_runnable', 'model_selected', 'workspace_bound', 'session_created']
const stageGates = {
  root: [...commonGates, 'root_single_html', 'root_three_parts'],
  focused: [...commonGates, 'root_single_html', 'root_three_parts', 'focused_five_ready', 'focused_lineage'],
  multi2: [...commonGates, 'multi_target_ready', 'multi_target_lineage', 'multi_target_parts_preserved'],
  multi3: [...commonGates, 'multi_target_ready', 'multi_target_lineage', 'multi_target_parts_preserved'],
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
  workspace_output: {},
  permissions_approved: [],
  gates: {},
  failures: [],
}
let token = suppliedToken
let assignment = null

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[designer-artifact-flow] ${message}\n`)
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
  const deadline = startedAt + timeoutMs
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

async function waitForArtifacts(sessionID, predicate, count, label) {
  const startedAt = Date.now()
  const deadline = startedAt + timeoutMs
  let matches = []
  let nextHeartbeatAt = startedAt
  let lastCount = -1
  while (Date.now() < deadline) {
    const artifacts = await catalog(sessionID)
    matches = artifacts.filter((item) => item?.status === 'ready' && predicate(item))
    const staging = artifacts.filter((item) => item?.status === 'staging' && predicate(item)).length
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
  let record = modelOverride ? records.find((candidate) => String(candidate?.model || '') === modelOverride) : null
  if (!record) {
    record = records.find((candidate) => (candidate?.recommendations || []).some((rec) => ['auto', 'main'].includes(String(rec?.role || '').toLowerCase()))) || records[0]
  }
  assert(record?.model, `no model available for ${provider}`)
  const thinkingOptions = Array.isArray(record.thinking_options) ? record.thinking_options.map((value) => String(value).toLowerCase()) : []
  const thinking = thinkingOptions.includes(thinkingOverride) ? thinkingOverride : String(record.default_thinking || thinkingOptions[0] || 'low').toLowerCase()
  assignment = { provider, model: String(record.model), thinking }
  result.model = assignment
  result.gates.model_selected = true
}

async function main() {
  if (!token) {
    const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
    token = String(auth.body?.token || '').trim()
    assert(token, 'desktop authentication returned no token')
  }
  await selectModel()

  const topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const runtime = (topology.runtimes || []).find((item) => item?.relationship === 'self') || (topology.runtimes || [])[0]
  const bindings = topology.workspace_bindings || []
  const binding = workspacePathOverride
    ? bindings.find((item) => item?.source_workspace_path === workspacePathOverride || item?.destination_workspace_path === workspacePathOverride)
    : bindings.find((item) => item?.state === 'bound' && item?.workspace_binding_id) || bindings[0]
  assert(runtime?.swarm_id, 'topology has no self runtime')
  assert(binding?.workspace_binding_id, 'topology has no bound workspace')
  const workspacePath = String(binding.source_workspace_path || binding.destination_workspace_path || '')
  assert(workspacePath, 'workspace binding has no path')
  result.ids.workspace_binding_id = binding.workspace_binding_id
  result.workspace_output.workspace_name = String(binding.source_workspace_name || binding.destination_workspace_name || 'workspace')
  result.gates.workspace_bound = true

  let sessionID = sessionOverride
  const createStageSession = !sessionID && (stage === 'root' || stage === 'all' || stage === 'multi2' || stage === 'multi3')
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

  if (stage === 'multi2' || stage === 'multi3') {
    const sourceRef = {
      session_id: sourceSessionOverride,
      collection_id: sourceCollectionOverride,
      variant_id: sourceVariantOverride,
      event_seq: sourceEventSeqOverride,
    }
    const targetCount = stage === 'multi2' ? 2 : 3
    const selectedTargets = stage === 'multi2' ? [partContract[0], partContract[2]] : partContract
    const targetIDs = selectedTargets.map((item) => item.id)
    const selection = { ...sourceRef, action: 'use' }
    const prompt = [
      `Use the selected exact monolithic HTML artifact and remake exactly these ${targetCount} locator targets together: ${targetIDs.join(', ')}.`,
      `Launch one managed Designer Iteration Swarm with count=1, source_artifact set to the exact selection, section_targets set to the complete exact ${targetCount}-target list including kind/start_ms/end_ms, and animation_profile motion_ui.`,
      'The Designer must inspect the exact complete HTML, change every selected target atomically, preserve every non-target target plus all canonical IDs/timings, and publish exactly one complete text/html revision with one manage_artifact create call.',
      'Do not create or modify a plan. Launch the one Designer swarm directly in this turn.',
      'Do not use read_parts, publish_parts, read_part, publish_part, create_package, ZIP, initial_parts, workspace output, or retries through multipart tools.',
      'Wait for the one ready candidate and finish.',
    ].join(' ')
    await postTurn(sessionID, stage, prompt, [selection])
    const targetKey = targetIDs.join(',')
    const candidates = await waitForArtifacts(sessionID, (item) => sameSource(item.lineage, sourceRef)
      && String(item?.lineage?.selected_review_target_ids || '') === targetKey
      && item.media_type === 'text/html', 1, `${stage} candidate`)
    const candidate = candidates.sort((a, b) => Number(b.event_seq || 0) - Number(a.event_seq || 0))[0]
    assert(hasPartContract(candidate), `${stage} candidate did not preserve the canonical part contract`)
    result.references.source = sourceRef
    result.references.multi_target = exactRef(candidate)
    result.rounds.multi_target = [{ reference: exactRef(candidate), lineage: candidate.lineage, parts: candidate.parts }]
    result.gates.multi_target_ready = true
    result.gates.multi_target_lineage = sameSource(candidate.lineage, sourceRef) && String(candidate.lineage?.selected_review_target_ids || '') === targetKey
    result.gates.multi_target_parts_preserved = hasPartContract(candidate)
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
      'Keep the artifact monolithic text/html. Use one manage_artifact create call with top-level content. Do not use initial_parts, multipart tools, create_package, ZIP, workspace files, or task delegation.',
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
  await main()
} catch (error) {
  result.error = error?.stack || String(error)
  log(result.error)
} finally {
  result.completed_at = new Date().toISOString()
  result.failed_gates = requiredGates.filter((gate) => result.gates[gate] !== true)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (result.result !== 'PASS') process.exitCode = 2
}
