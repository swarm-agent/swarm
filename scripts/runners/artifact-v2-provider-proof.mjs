#!/usr/bin/env node
import crypto from 'node:crypto'

const argv = process.argv.slice(2)
const option = (name, fallback = '') => { const index = argv.indexOf(name); return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback }
const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'codex')).trim().toLowerCase()
const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || 'high')).trim().toLowerCase()
const designerModel = String(option('--designer-model', process.env.SWARM_RUNNER_DESIGNER_MODEL || '')).trim()
const designerThinking = String(option('--designer-thinking', process.env.SWARM_RUNNER_DESIGNER_THINKING || 'high')).trim().toLowerCase()
const workspacePathOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const stage = String(option('--stage', process.env.SWARM_RUNNER_STAGE || 'single1')).trim().toLowerCase()
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()
if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
if (!['single1', 'single2', 'iteration3'].includes(stage)) throw new Error('--stage must be single1, single2, or iteration3')
if (!actionModel || !designerModel) throw new Error('--action-model and --designer-model are required')
if (!Number.isFinite(timeoutMs) || timeoutMs < 300000 || timeoutMs > 600000) throw new Error('--timeout-ms must be between 300000 and 600000')

const testID = `artifact-v2-provider-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const deadline = Date.now() + timeoutMs
const heartbeatMs = 15000
const result = { result: 'NOT_DONE', test: 'artifact-v2-provider-proof', test_id: testID, stage, started_at: new Date().toISOString(), ids: {}, model: {}, journeys: [], pixel_inspections: [], conversion: {}, gates: {}, failures: [] }
let token = suppliedToken
let originalSwarm = null
let originalDesigner = null
let settingsChanged = false
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[artifact-v2-provider-proof] ${message}\n`)
const assert = (condition, message) => { if (!condition) { result.failures.push(message); throw new Error(message) } }

async function api(method, route, body, label = route, allowError = false) {
  const headers = { Accept: 'application/json', Origin: new URL(apiURL).origin, Referer: `${apiURL}/app`, 'Sec-Fetch-Site': 'same-origin' }
  if (token) { headers['X-Swarm-Token'] = token; headers.Cookie = `swarm_desktop_session=${token}` }
  const controller = new AbortController(); const timer = setTimeout(() => controller.abort(), 120000)
  try {
    const init = { method, headers, signal: controller.signal }
    if (body !== undefined) { headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body) }
    const response = await fetch(`${apiURL}${route}`, init); const text = await response.text(); let decoded = null
    try { decoded = text ? JSON.parse(text) : null } catch { decoded = { raw: text } }
    if (!allowError && !response.ok) throw new Error(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return { ok: response.ok, status: response.status, body: decoded, text }
  } finally { clearTimeout(timer) }
}

async function selectModel(model, thinking, label) {
  const catalog = await api('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, `read ${label} model catalog`)
  const record = (catalog.body?.records || []).find((item) => String(item?.model || '') === model)
  assert(record, `${label} model ${provider}/${model} is not in the live catalog`)
  const options = Array.isArray(record.thinking_options) ? record.thinking_options.map((value) => String(value).toLowerCase()) : []
  return { provider, model, thinking: options.includes(thinking) ? thinking : String(record.default_thinking || options[0] || 'low').toLowerCase() }
}

async function canonicalWorkspace() {
  const topology = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const workspaces = (await api('GET', '/v1/workspace/list?limit=200', undefined, 'read workspaces')).body?.workspaces || []
  const byID = new Map(workspaces.filter((item) => item?.state === 'active').map((item) => [String(item.workspace_id), item]))
  const binding = (topology.workspace_bindings || []).find((item) => {
    const workspace = byID.get(String(item?.source_workspace_id || '')); if (!workspace || item?.state !== 'bound') return false
    if (Number(item?.source_workspace_generation || 0) !== Number(workspace.workspace_generation || 0)) return false
    const path = String(item.source_workspace_path || ''); return !workspacePathOverride || path === workspacePathOverride || String(item.destination_workspace_path || '') === workspacePathOverride
  })
  assert(binding, 'no current bound workspace is available for the provider proof')
  const runtime = (topology.runtimes || []).find((item) => item?.relationship === 'self') || (topology.runtimes || [])[0]
  assert(runtime?.swarm_id, 'topology has no self runtime')
  return { binding, runtime }
}

async function createSession(title, assignment, topology) {
  const binding = topology.binding
  const created = await api('POST', '/v3/sessions', { client_request_id: `${testID}:${title}`, title, workspace_path: String(binding.source_workspace_path || binding.destination_workspace_path || ''), workspace_name: String(binding.source_workspace_name || 'artifact-v2-e2e'), workspace_binding_id: binding.workspace_binding_id, swarm_id: topology.runtime.swarm_id, target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: 'swarm', preference: assignment, model_profile: { temporary: { ...assignment, name: `${testID} model` } }, metadata: { runner_test: 'artifact-v2-provider-proof', runner_test_id: testID, stage } }, `create ${title}`)
  const id = String(created.body?.session?.id || ''); assert(id, `${title} returned no session id`); return id
}

async function hydrate(sessionID) {
  return (await api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [sessionID], history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' }, resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: true, permission_summaries: true }, include_active: true }, `hydrate ${sessionID}`)).body || {}
}

async function approvePending(sessionID) {
  const pending = (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`, undefined, 'read pending permissions')).body?.permissions || []
  if (pending.length === 0) return 0
  return Number((await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/resolve_all`, { action: 'allow_once', reason: `${testID} checked-in provider proof`, limit: 50 }, 'approve provider proof')).body?.count || 0)
}

async function artifactCatalog(sessionID) { return (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/artifact-v2`, undefined, 'read Artifact V2 catalog')).body?.artifacts || [] }
async function artifactStudio(sessionID, artifactID) { return (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/artifact-v2/${encodeURIComponent(artifactID)}`, undefined, 'read Artifact V2 Studio')).body?.artifact }

async function waitForTurn(sessionID, runID, label) {
  let nextHeartbeat = 0
  while (Date.now() < deadline) {
    const approvals = await approvePending(sessionID); const snapshot = await hydrate(sessionID)
    const intents = snapshot.run_intents_by_session?.[sessionID] || []; const intent = intents.find((item) => String(item?.run_id || '') === runID)
    if (intent && ['failed', 'cancelled', 'expired', 'interrupted'].includes(String(intent.status || '').toLowerCase())) throw new Error(`${label} failed: ${intent.status}`)
    if (Date.now() >= nextHeartbeat) { const artifacts = await artifactCatalog(sessionID); log(`${label} status=${intent?.status || 'pending'} artifact_v2=${artifacts.length} approvals=${approvals}`); nextHeartbeat = Date.now() + heartbeatMs }
    if (intent && ['completed', 'failed', 'cancelled', 'expired', 'interrupted'].includes(String(intent.status || '').toLowerCase())) return snapshot
    await sleep(1500)
  }
  throw new Error(`${label} timed out`)
}

async function postTurn(sessionID, label, content) {
  const response = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, { client_request_id: `${testID}:${label}:${crypto.randomBytes(3).toString('hex')}`, role: 'user', content, metadata: { runner_test: 'artifact-v2-provider-proof', runner_test_id: testID, stage: label } }, `post ${label}`)
  const runID = String(response.body?.run_intent?.run_id || response.body?.run_id || ''); assert(runID, `${label} returned no run id`)
  await waitForTurn(sessionID, runID, label); return runID
}

function singlePrompt(index) {
  return [`Create Artifact V2 provider journey ${index} for ${testID}.`, 'Use exactly one regular managed Designer launch with animation_profile motion_ui and landscape_video output requirements.', 'The Designer must use artifact_v2_author only. Declare exactly two real parts: scene (application/vnd.swarm.artifact-v2.motion-scene+json) and behavior (application/vnd.swarm.artifact-v2.motion-behavior+json).', 'Create a 6000ms 30fps 1920x1080 motion scene with three visible text or rectangle elements, two ordered sections named opening 0-3000ms and payoff 3000-6000ms, and bounded opacity/translate/scale behavior. Keep every element fully inside the stage at all representative times.', 'Request server build and trusted Chrome validation, repair exact part revisions if diagnostics require it, submit_candidate, and finish only after the exact V2 published head is returned. Never call manage_artifact, create a V1 artifact, or author HTML/runtime bytes.'].join(' ')
}

function validateJourney(studio, label) {
  assert(studio?.working?.state === 'published_view', `${label} is not published_view`)
  assert(studio?.working?.published_head?.published_head_id, `${label} has no published head`)
  assert(studio.parts?.length === 2, `${label} has ${studio.parts?.length || 0}/2 real parts`)
  assert((studio.part_revisions || []).length >= 2, `${label} has no independently stored part revisions`)
  assert((studio.builds || []).some((item) => item.id === studio.working.latest_build_id && item.status === 'succeeded'), `${label} has no exact successful build`)
  const validation = (studio.validations || []).find((item) => item.id === studio.working.latest_validation_id)
  assert(validation?.status === 'valid' && Array.isArray(validation.evidence_digests) && validation.evidence_digests.length >= 3, `${label} has no trusted rendered-frame evidence`)
  assert((studio.published_heads || []).some((item) => item.id === studio.working.published_head.published_head_id), `${label} published history is missing`)
  return { artifact_id: studio.working.id, published_head_id: studio.working.published_head.published_head_id, composition_id: studio.working.composition_head.composition_id, working_revision: studio.working.revision, composition_head_revision: studio.working.composition_head.head_revision, target_part_ids: [studio.parts.find((part) => part.key === 'scene')?.id].filter(Boolean) }
}

async function inspectPixels(sessionID, studio, label) {
  const build = (studio.builds || []).find((item) => item.id === studio.working.latest_build_id)
  const validation = (studio.validations || []).find((item) => item.id === studio.working.latest_validation_id)
  assert(build?.representative_timestamps_ms?.length >= 3, `${label} has insufficient representative timestamps`)
  assert(validation?.renderer_snapshot === 'trusted-chrome-animation-1', `${label} did not use trusted Chrome`)
  assert((validation.evidence_digests || []).length >= build.representative_timestamps_ms.length, `${label} rendered evidence is incomplete`)
  const preview = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/artifact-v2/${encodeURIComponent(studio.working.id)}/preview`, undefined, `${label} preview`)
  assert(preview.ok && /swarm\.animation\/v1/.test(preview.text) && /swarm-player\/v1/.test(preview.text), `${label} preview lacks server runtime contracts`)
  result.pixel_inspections.push({ label, artifact_id: studio.working.id, renderer_snapshot: validation.renderer_snapshot, representative_timestamps_ms: build.representative_timestamps_ms, evidence_digests: validation.evidence_digests, preview_bytes: Buffer.byteLength(preview.text) })
}

async function convertPending(sessionID, source, label) {
  const projectID = `video-${crypto.createHash('sha256').update(`${testID}:${label}`).digest('hex').slice(0, 20)}`
  const created = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/video/projects`, { project_id: projectID, title: `${label} Video Studio`, output_preset: 'landscape_1080p' }, `${label} create video project`)
  const baseRevision = String(created.body?.project?.current_revision_id || created.body?.revision?.id || ''); assert(baseRevision, `${label} video project has no base revision`)
  await postTurn(sessionID, `${label}-convert`, [`Convert the exact published Artifact V2 head to a pending Video Studio proposal with manage_video action=convert_artifact_v2.`, `project_id=${projectID}`, `base_revision_id=${baseRevision}`, `artifact_v2_artifact_id=${source.artifact_id}`, `artifact_v2_published_head_id=${source.published_head_id}`, 'Do not reconstruct plan parts, candidates, fallbacks, or exact-reference arrays. Do not accept the proposal or start rendering.'].join('\n'))
  const proposals = (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/video/projects/${encodeURIComponent(projectID)}/edit-proposals`, undefined, `${label} list proposals`)).body?.proposals || []
  const proposal = proposals.find((item) => item?.intent === 'artifact_v2_convert' || item?.status === 'pending')
  assert(proposal?.status === 'pending', `${label} conversion did not remain pending`)
  return { project_id: projectID, proposal_id: proposal.id, status: proposal.status, intent: proposal.intent, working_revision_id: proposal.working_revision_id }
}

async function runSingle(index, topology, assignment) {
  const sessionID = await createSession(`${testID} single ${index}`, assignment, topology)
  await postTurn(sessionID, `single-${index}`, singlePrompt(index))
  const catalog = await artifactCatalog(sessionID); assert(catalog.length === 1, `single ${index} produced ${catalog.length}/1 V2 artifacts`)
  const studio = await artifactStudio(sessionID, catalog[0].working.id); const source = validateJourney(studio, `single ${index}`)
  await inspectPixels(sessionID, studio, `single ${index}`)
  const conversion = await convertPending(sessionID, source, `single-${index}`)
  const journey = { index, session_id: sessionID, source, part_count: studio.parts.length, build_id: studio.working.latest_build_id, validation_id: studio.working.latest_validation_id, conversion }
  result.journeys.push(journey); return journey
}

async function runIteration(baseJourney, topology, assignment) {
  const sessionID = baseJourney.session_id; const source = baseJourney.source
  const studioBefore = await artifactStudio(sessionID, source.artifact_id); const scene = studioBefore.parts.find((part) => part.key === 'scene'); assert(scene, 'iteration source scene part missing')
  source.target_part_ids = [scene.id]
  const exact = JSON.stringify(source)
  await postTurn(sessionID, 'iteration-3', [`Create exactly three alternatives for the exact Artifact V2 scene Part.`, `Launch one Designer Iteration Swarm with count=3, artifact_v2_source=${exact}, section_target={"id":"${scene.id}","label":"${scene.label}","kind":"semantic"}, themes=["quiet editorial","kinetic signal","modular systems"], and iteration_controls change=["scene typography, palette, and motion treatment"], preserve=["behavior part, 6000ms duration, 30fps, two section IDs and timings"], exclude=["new claims","new parts","HTML authoring","manage_artifact"].`, 'Wait for all three V2 candidates. Do not select one, convert it, or create any V1 artifact.'].join('\n'))
  const after = await artifactStudio(sessionID, source.artifact_id); const round = (after.iterations || []).find((item) => item.requested_candidates === 3 && item.target_part_ids?.length === 1 && item.target_part_ids[0] === scene.id)
  assert(round?.status === 'awaiting_selection' && round.candidates?.length === 3, 'three-candidate V2 iteration is not awaiting selection')
  const base = after.compositions.find((item) => item.id === round.base_composition_id); assert(base, 'iteration base composition missing')
  for (const candidate of round.candidates) {
    const composition = after.compositions.find((item) => item.id === candidate.composition_id); assert(composition, `iteration candidate ${candidate.slot_id} missing composition`)
    const changed = composition.parts.filter((part, index) => part.part_revision_id !== base.parts[index]?.part_revision_id)
    assert(changed.length === 1 && changed[0].part_id === scene.id, `candidate ${candidate.slot_id} did not preserve non-target parts`)
  }
  result.iteration = { session_id: sessionID, artifact_id: source.artifact_id, iteration_id: round.id, status: round.status, candidates: round.candidates.map((item) => ({ slot_id: item.slot_id, composition_id: item.composition_id, status: item.status })) }
  result.gates.iteration_three_candidates = true
}

try {
  if (!token) { const auth = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication'); token = String(auth.body?.token || ''); assert(token, 'desktop authentication returned no token') }
  const providers = await api('GET', '/v1/providers', undefined, 'read providers'); assert((providers.body?.providers || []).some((item) => String(item?.id || '').toLowerCase() === provider && item?.runnable !== false), `provider ${provider} is not runnable`)
  const action = await selectModel(actionModel, actionThinking, 'action'); const designer = await selectModel(designerModel, designerThinking, 'Designer'); result.model = { action, designer }
  const settings = (await api('GET', '/v1/agent-model-settings', undefined, 'read model settings')).body?.agent_model_settings || {}; originalSwarm = settings.swarm; originalDesigner = settings.system_agents?.designer; assert(originalSwarm && originalDesigner, 'canonical Swarm or Designer model setting is missing')
  settingsChanged = true
  await api('PATCH', '/v1/agent-model-settings', { swarm: { action, plan: originalSwarm.plan || action } }, 'configure action model')
  await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer } }, 'configure Designer model')
  const topology = await canonicalWorkspace()
  const first = await runSingle(1, topology, action)
  if (stage === 'single1') result.result = 'PASS'
  else {
    const second = await runSingle(2, topology, action)
    if (stage === 'single2') result.result = 'PASS'
    else { await runIteration(second, topology, action); result.result = 'PASS' }
  }
  result.gates.provider_journeys = true; result.gates.pixel_inspection = result.pixel_inspections.length >= (stage === 'single1' ? 1 : 2); result.gates.pending_conversion = result.journeys.every((item) => item.conversion.status === 'pending')
} catch (error) {
  result.error = error?.stack || String(error); log(result.error)
} finally {
  if (settingsChanged) {
    try { await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer: originalDesigner } }, 'restore Designer model'); await api('PATCH', '/v1/agent-model-settings', { swarm: originalSwarm }, 'restore Swarm model') } catch (error) { result.failures.push(`failed to restore model settings: ${error?.message || error}`); result.result = 'NOT_DONE' }
  }
  result.completed_at = new Date().toISOString(); process.stdout.write(`${JSON.stringify(result, null, 2)}\n`); if (result.result !== 'PASS') process.exitCode = 2
}
