#!/usr/bin/env node
import crypto from 'node:crypto'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SYNTHETIC_DUMP_TOPOLOGY = Object.freeze({
  project_id: 'project-synthetic', base_revision_id: 'revision-base-synthetic', working_revision_id: 'revision-working-synthetic', proposal_id: 'proposal-synthetic',
  clips: [
    { id: 'clip-html-1', part_id: 'part-html-1', media_type: 'text/html', start_ms: 0, end_ms: 10000, order: 0 },
    { id: 'clip-html-2', part_id: 'part-html-2', media_type: 'text/html', start_ms: 10000, end_ms: 20000, order: 1 },
  ],
  parts: [
    { id: 'part-html-1', clip_id: 'clip-html-1', media_type: 'text/html', start_ms: 0, end_ms: 10000, order: 0, candidates: [{ id: 'candidate-1a' }, { id: 'candidate-1b' }], selected_source: null, derivative: null },
    { id: 'part-html-2', clip_id: 'clip-html-2', media_type: 'text/html', start_ms: 10000, end_ms: 20000, order: 1, candidates: [{ id: 'candidate-2a' }, { id: 'candidate-2b' }], selected_source: { session_id: 'session-synthetic', collection_id: 'collection-synthetic', variant_id: 'source-2b', event_seq: 2 }, derivative: null },
  ],
})

const scalar = (object, names, fallback = '') => {
  for (const name of names) if (object?.[name] !== undefined && object?.[name] !== null) return object[name]
  return fallback
}
const idOf = (object, names = ['id']) => String(scalar(object, names, ''))
const arrayOf = (object, names) => {
  for (const name of names) if (Array.isArray(object?.[name])) return object[name]
  return []
}
const exactRef = (value) => {
  if (!value || typeof value !== 'object') return null
  const ref = value.artifact_ref || value.artifactRef || value.source || value
  const session_id = String(scalar(ref, ['session_id', 'sessionId'], ''))
  const collection_id = String(scalar(ref, ['collection_id', 'collectionId'], ''))
  const variant_id = String(scalar(ref, ['variant_id', 'variantId', 'artifact_id', 'artifactId'], ''))
  const event_seq = Number(scalar(ref, ['event_seq', 'eventSeq'], 0))
  return session_id && collection_id && variant_id && event_seq > 0 ? { session_id, collection_id, variant_id, event_seq } : null
}
const fingerprint = (value) => crypto.createHash('sha256').update(JSON.stringify(value)).digest('hex')

function normalizeCandidate(candidate, order) {
  return {
    id: idOf(candidate, ['candidate_id', 'candidateId', 'id']),
    source: exactRef(candidate),
    media_type: String(scalar(candidate, ['media_type', 'mediaType', 'mime_type', 'mimeType'], '')),
    order: Number(scalar(candidate, ['order', 'index', 'candidate_index', 'candidateIndex'], order)),
  }
}
function normalizePart(part, order) {
  const animation = scalar(part, ['animation_candidates', 'animationCandidates'], null)
  const candidates = (animation ? arrayOf(animation, ['candidates']) : arrayOf(part, ['candidates']))
    .map((candidate, candidateOrder) => ({ ...normalizeCandidate(candidate, candidateOrder), media_type: String(scalar(candidate, ['media_type', 'mediaType', 'mime_type', 'mimeType'], 'text/html')) }))
  const durationMs = Number(scalar(part, ['duration_ms', 'durationMs'], 0))
  return {
    id: idOf(part, ['part_id', 'partId', 'id']), clip_id: idOf(part, ['clip_id', 'clipId', 'id']),
    candidates,
    selected_candidate_id: String(scalar(animation || part, ['selected_candidate_id', 'selectedCandidateId'], '')),
    selected_source: exactRef(scalar(animation || part, ['selected_source', 'selectedSource', 'selected_animation_source', 'selectedAnimationSource'], null)),
    derivative: exactRef(scalar(animation || part, ['derivative', 'animation_derivative', 'animationDerivative', 'rendered_artifact'], null)),
    media_type: String(scalar(part, ['visual_media_type', 'visualMediaType', 'media_type', 'mediaType', 'mime_type', 'mimeType'], candidates.length ? 'text/html' : '')),
    start_ms: Number(scalar(part, ['start_ms', 'startMs'], 0)), end_ms: Number(scalar(part, ['end_ms', 'endMs'], durationMs)),
    duration_ms: durationMs,
    order: Number(scalar(part, ['order', 'index'], order)),
  }
}
function normalizeClip(clip, order) {
  const source = scalar(clip, ['source', 'visual', 'artifact_ref', 'artifactRef'], null)
  return {
    id: idOf(clip, ['clip_id', 'clipId', 'id']), part_id: idOf(clip, ['part_id', 'partId']), source: exactRef(source),
    derivative: exactRef(scalar(clip, ['derivative', 'animation_derivative', 'animationDerivative'], null)),
    media_type: String(scalar(clip, ['media_type', 'mediaType', 'mime_type', 'mimeType'], scalar(source, ['media_type', 'mediaType'], ''))),
    start_ms: Number(scalar(clip, ['start_ms', 'startMs', 'timeline_start_ms'], 0)),
    end_ms: Number(scalar(clip, ['end_ms', 'endMs', 'timeline_end_ms'], 0)), order: Number(scalar(clip, ['order', 'index'], order)),
  }
}

/** Canonical public-safe identity ledger row for one exact Video Studio state. */
export function normalizeVideoSnapshot(input) {
  const project = input?.project || input?.video_project || input?.videoProject || input
  const working = project?.working_revision || project?.workingRevision || input?.working_revision || input?.workingRevision || {}
  const base = project?.base_revision || project?.baseRevision || input?.base_revision || input?.baseRevision || {}
  const proposal = project?.proposal || project?.pending_proposal || project?.pendingProposal || input?.proposal || {}
  const proposalPlan = proposal?.plan || proposal?.video_plan || proposal?.videoPlan || {}
  const timeline = working?.timeline || project?.timeline || input?.timeline || {}
  const acceptedPlan = timeline?.metadata?.accepted_video_plan || timeline?.metadata?.acceptedVideoPlan || {}
  const clips = arrayOf(timeline, ['clips']).concat(arrayOf(working, ['clips'])).concat(arrayOf(project, ['clips'])).filter((item, index, all) => all.indexOf(item) === index).map(normalizeClip)
  const partByID = new Map()
  for (const item of arrayOf(acceptedPlan, ['parts']).concat(arrayOf(project, ['parts'])).concat(arrayOf(input, ['parts'])).concat(arrayOf(proposal, ['parts'])).concat(arrayOf(proposalPlan, ['parts']))) {
    const itemID = idOf(item, ['part_id', 'partId', 'id'])
    const existing = partByID.get(itemID)
    const part = normalizePart(item, existing ? existing.order : partByID.size)
    if (part.id) partByID.set(part.id, part)
  }
  const parts = [...partByID.values()]
  let nextPartStart = 0
  for (const part of parts.sort((a, b) => a.order - b.order)) {
    if (part.order > 0 && part.start_ms === 0) part.start_ms = nextPartStart
    if (part.end_ms <= part.start_ms && part.duration_ms > 0) part.end_ms = part.start_ms + part.duration_ms
    nextPartStart = Math.max(nextPartStart, part.end_ms)
  }
  const row = {
    project_id: idOf(project, ['project_id', 'projectId', 'id']),
    base_revision_id: idOf(base, ['revision_id', 'revisionId', 'id']) || idOf(proposal, ['base_revision_id', 'baseRevisionId']) || idOf(project, ['base_revision_id', 'baseRevisionId']),
    working_revision_id: idOf(working, ['revision_id', 'revisionId', 'id']) || idOf(proposal, ['working_revision_id', 'workingRevisionId']) || idOf(project, ['working_revision_id', 'workingRevisionId', 'current_revision_id', 'currentRevisionId']),
    proposal_id: idOf(proposal, ['proposal_id', 'proposalId', 'id']) || idOf(project, ['proposal_id', 'proposalId', 'pending_proposal_id', 'pendingProposalId']),
    clips: clips.sort((a, b) => a.order - b.order), parts: parts.sort((a, b) => a.order - b.order),
  }
  if (!row.project_id || !row.base_revision_id || !row.working_revision_id || !row.proposal_id || row.clips.length === 0 || row.parts.length === 0) {
    throw new Error(`incomplete Video Studio identity snapshot: project=${Boolean(row.project_id)} base=${Boolean(row.base_revision_id)} working=${Boolean(row.working_revision_id)} proposal=${Boolean(row.proposal_id)} clips=${row.clips.length} parts=${row.parts.length}`)
  }
  return row
}

export class IdentityLedger {
  constructor() { this.rows = [] }
  record(label, raw) { const row = normalizeVideoSnapshot(raw); this.rows.push({ label, row }); return row }
  assertNoDrift(before, after, { mutablePartIDs = [], mutableClipIDs = [], retimedClipIDs = [], allowAppendedParts = false, allowAppendedClips = false } = {}) {
    if (before.project_id !== after.project_id) throw new Error('project identity drifted')
    const compare = (kind, prior, next, mutable, allowAppend, retimed = []) => {
      const nextByID = new Map(next.map((item) => [item.id, item]))
      for (const item of prior) {
        if (!item.id) throw new Error(`${kind} has no identity`)
        const found = nextByID.get(item.id)
        if (!found) throw new Error(`${kind} ${item.id} disappeared`)
        let left = item, right = found
        if (retimed.includes(item.id)) {
          const { start_ms: _leftStart, end_ms: _leftEnd, order: _leftOrder, ...leftStable } = item
          const { start_ms: _rightStart, end_ms: _rightEnd, order: _rightOrder, ...rightStable } = found
          left = leftStable; right = rightStable
        }
        if (!mutable.includes(item.id) && JSON.stringify(left) !== JSON.stringify(right)) throw new Error(`non-target ${kind} ${item.id} drifted`)
      }
      if (!allowAppend && next.length !== prior.length) throw new Error(`${kind} count drifted`)
      if (allowAppend && next.length < prior.length) throw new Error(`${kind} count shrank`)
    }
    compare('part', before.parts, after.parts, mutablePartIDs, allowAppendedParts)
    compare('clip', before.clips, after.clips, mutableClipIDs, allowAppendedClips, retimedClipIDs)
  }
  summary() { return this.rows.map(({ label, row }) => ({ label, digest: fingerprint(row), clips: row.clips.length, parts: row.parts.length, candidates: row.parts.reduce((sum, part) => sum + part.candidates.length, 0) })) }
}

export function assertSyntheticDumpTopology() {
  const row = normalizeVideoSnapshot(SYNTHETIC_DUMP_TOPOLOGY)
  if (row.clips.length !== 2 || row.parts.length !== 2) throw new Error('dump contract must contain two clips and two parts')
  if (row.parts.some((part) => part.candidates.length !== 2)) throw new Error('dump contract must contain two candidates per part')
  if (row.parts.some((part) => part.end_ms - part.start_ms !== 10000)) throw new Error('dump contract parts must be 10 seconds')
  if (row.parts[0].selected_source !== null || row.parts[1].selected_source === null || row.parts[1].derivative !== null) throw new Error('dump selection/export state changed')
  return row
}

async function run() {
  const argv = process.argv.slice(2)
  const option = (name, fallback = '') => { const i = argv.indexOf(name); return i >= 0 && i + 1 < argv.length ? argv[i + 1] : fallback }
  const contractOnly = argv.includes('--contract-only')
  const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
  const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'codex')).trim().toLowerCase()
  const modelOverride = String(option('--model', process.env.SWARM_RUNNER_MODEL || '')).trim()
  const overallTimeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
  const requestTimeoutMs = Number(option('--request-timeout-ms', '120000'))
  const workspaceOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
  const testID = `video-studio-multi-turn-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
  const result = { result: 'NOT_DONE', test: 'video-studio-multi-turn', test_id: testID, started_at: new Date().toISOString(), gates: {}, ledger: [], failures: [] }
  assertSyntheticDumpTopology(); result.gates.synthetic_dump_topology = true
  if (contractOnly) { result.result = 'PASS'; result.completed_at = new Date().toISOString(); process.stdout.write(`${JSON.stringify(result, null, 2)}\n`); return }
  if (!apiURL || !/^https?:\/\//.test(apiURL)) throw new Error('--api-url must be an http or https URL')
  if (!Number.isFinite(overallTimeoutMs) || overallTimeoutMs < 120000 || overallTimeoutMs > 600000) throw new Error('--timeout-ms must be between 120000 and 600000')
  if (!Number.isFinite(requestTimeoutMs) || requestTimeoutMs < 1000 || requestTimeoutMs > 120000) throw new Error('--request-timeout-ms must be between 1000 and 120000')
  const deadline = Date.now() + overallTimeoutMs
  let token = String(process.env.SWARM_RUNNER_TOKEN || '').trim(), originalSettings = null, settingsChanged = false, sessionID = ''
  const ledger = new IdentityLedger()
  const fail = (message) => { result.failures.push(message); throw new Error(message) }
  const assert = (ok, message) => { if (!ok) fail(message) }
  const api = async (method, route, body, label = route, allowError = false) => {
    if (Date.now() >= deadline) fail(`overall timeout reached before ${label}`)
    const controller = new AbortController(); const timer = setTimeout(() => controller.abort(), Math.min(requestTimeoutMs, deadline - Date.now()))
    const headers = { Accept: 'application/json', Origin: new URL(apiURL).origin, Referer: `${apiURL}/app`, ...(token ? { 'X-Swarm-Token': token, Cookie: `swarm_desktop_session=${token}` } : {}) }
    try {
      if (body !== undefined) headers['Content-Type'] = 'application/json'
      const response = await fetch(`${apiURL}${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal })
      const text = await response.text(); let decoded = null; try { decoded = text ? JSON.parse(text) : null } catch { decoded = { raw: text.slice(0, 1000) } }
      if (!allowError && !response.ok) fail(`${label} failed with HTTP ${response.status}: ${text.slice(0, 1000)}`)
      return { ok: response.ok, status: response.status, body: decoded, text }
    } finally { clearTimeout(timer) }
  }
  const hydrate = async () => (await api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [sessionID], history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' }, resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true }, include_active: true }, 'hydrate session')).body || {}
  let projectID = ''
  const readVideoState = async (label) => {
    const listed = (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/video/projects?limit=32`, undefined, `${label} list projects`)).body?.projects || []
    if (!projectID) projectID = String([...listed].sort((left, right) => Number(right?.updated_at || 0) - Number(left?.updated_at || 0))[0]?.id || '')
    assert(projectID, `${label} exposed no Video Studio project`)
    const detail = (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/video/projects/${encodeURIComponent(projectID)}`, undefined, `${label} read project`)).body || {}
    const proposals = (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/video/projects/${encodeURIComponent(projectID)}/edit-proposals`, undefined, `${label} list proposals`)).body?.proposals || []
    const proposal = [...proposals].filter((item) => String(item?.status || '') === 'pending').sort((left, right) => Number(right?.updated_at || 0) - Number(left?.updated_at || 0))[0] || proposals.at(-1)
    assert(proposal?.id, `${label} exposed no Video Studio proposal`)
    return { project: detail.project || listed.find((item) => String(item?.id || '') === projectID), working_revision: detail.current_revision, proposal }
  }
  const approve = async () => {
    const pending = (await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`, undefined, 'list permissions')).body?.permissions || []
    if (pending.length) await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/resolve_all`, { action: 'allow_once', reason: `${testID} checked-in E2E` }, 'approve permissions')
  }
  const postTurn = async (number, content) => {
    const posted = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, { client_request_id: `${testID}:turn-${number}`, role: 'user', content, metadata: { runner_test: 'video-studio-multi-turn', runner_test_id: testID, turn: number } }, `post turn ${number}`)
    const runID = String(posted.body?.run_intent?.run_id || posted.body?.run_id || ''); assert(runID, `turn ${number} returned no run id`)
    let nextBeat = 0, latest
    while (Date.now() < deadline) {
      await approve(); latest = await hydrate()
      const intent = (latest.run_intents_by_session?.[sessionID] || []).find((item) => String(item?.run_id || '') === runID)
      const status = String(intent?.status || 'pending').toLowerCase()
      if (Date.now() >= nextBeat) { process.stderr.write(`[video-studio-multi-turn] turn=${number} run=${runID} status=${status} elapsed=${Math.floor((overallTimeoutMs - (deadline - Date.now())) / 1000)}s\n`); nextBeat = Date.now() + 15000 }
      if (['failed', 'cancelled', 'expired', 'interrupted'].includes(status)) fail(`turn ${number} ended ${status}`)
      if (status === 'completed') return latest
      await new Promise((resolve) => setTimeout(resolve, 1500))
    }
    fail(`turn ${number} exceeded overall timeout`)
  }
  const snapshot = async (label) => ledger.record(label, await readVideoState(label))
  try {
    if (!token) { token = String((await api('GET', '/v1/auth/desktop/session', undefined, 'desktop auth')).body?.token || ''); assert(token, 'desktop auth returned no token') }
    const providers = (await api('GET', '/v1/providers', undefined, 'providers')).body?.providers || []
    assert(providers.some((item) => String(item?.id || '').toLowerCase() === provider && item?.runnable !== false), `provider ${provider} is not runnable`)
    const records = (await api('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'model catalog')).body?.records || []
    const model = records.find((item) => String(item?.model || '') === modelOverride) || records.find((item) => (item?.recommendations || []).some((rec) => ['auto', 'main'].includes(String(rec?.role || '').toLowerCase()))) || records[0]
    assert(model?.model, `no model available for ${provider}`)
    const thinking = String(model?.default_thinking || model?.thinking_options?.[0] || 'low').toLowerCase(); const assignment = { provider, model: String(model.model), thinking }
    const settings = (await api('GET', '/v1/agent-model-settings', undefined, 'read model settings')).body?.agent_model_settings || {}
    originalSettings = settings.swarm; assert(originalSettings, 'Swarm model setting is missing')
    await api('PATCH', '/v1/agent-model-settings', { swarm: { action: assignment, plan: originalSettings.plan || assignment } }, 'apply runner model setting'); settingsChanged = true
    const topology = (await api('GET', '/v1/swarm/topology', undefined, 'topology')).body || {}
    const runtime = (topology.runtimes || []).find((item) => item?.relationship === 'self') || topology.runtimes?.[0]
    const binding = workspaceOverride ? (topology.workspace_bindings || []).find((item) => item?.source_workspace_path === workspaceOverride || item?.destination_workspace_path === workspaceOverride) : (topology.workspace_bindings || []).find((item) => item?.state === 'bound') || topology.workspace_bindings?.[0]
    assert(runtime?.swarm_id && binding?.workspace_binding_id, 'self runtime or bound workspace unavailable')
    const workspacePath = String(binding.source_workspace_path || binding.destination_workspace_path || ''); assert(workspacePath, 'workspace path unavailable')
    const created = await api('POST', '/v3/sessions', { client_request_id: `${testID}:create`, title: `${testID} Video Studio regression`, workspace_path: workspacePath, workspace_name: String(binding.source_workspace_name || 'runner-test'), workspace_binding_id: binding.workspace_binding_id, swarm_id: runtime.swarm_id, target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: 'swarm', preference: assignment, model_profile: { temporary: { ...assignment, name: `${testID} temporary model` } }, metadata: { runner_test: 'video-studio-multi-turn', runner_test_id: testID } }, 'create isolated session')
    sessionID = String(created.body?.session?.id || ''); assert(sessionID, 'session creation returned no ID'); result.session = { id: sessionID }
    const common = 'Use only managed artifacts and Video Studio tools. Finish the requested operation in this turn. Do not accept a proposal or start final render. Preserve exact IDs, timing, order, selected sources, derivatives, and media types for every non-target part.'
    await postTurn(1, `${common} Create a new Video Studio project whose working timeline has exactly three ordered 10-second clips: HTML part html-1 with exactly two live HTML animation candidates; HTML part html-2 with exactly two live HTML animation candidates; and source-video clip video-1 backed by one registered source video discovered through list_source_roots/browse_source. Select one candidate and promote its exported derivative for each HTML part. Keep one pending HTML iteration proposal for the two HTML parts. Report marker VIDEO_STUDIO_TURN_1_DONE.`)
    let state = await snapshot('turn-1')
    assert(state.parts.length === 2 && state.parts.every((p) => p.candidates.length === 2), 'turn 1 proposal is not two independently iterable HTML parts')
    assert(state.clips.length === 3 && state.clips.filter((clip) => clip.media_type === 'video/mp4' || !clip.source).length >= 1, 'turn 1 working timeline is not two HTML-derived clips plus one source-video clip')
    const htmlParts = state.parts; const videoClip = state.clips.find((clip) => !htmlParts.some((part) => part.id === clip.id)) || state.clips.at(-1)
    const prior = state
    await postTurn(2, `${common} On the existing pending project replace only HTML part ${htmlParts[1].id} with exactly two new live HTML candidates, select one, export it, and promote that derivative. Do not change HTML part ${htmlParts[0].id} or source-video clip ${videoClip.id}. Report marker VIDEO_STUDIO_TURN_2_DONE.`)
    state = await snapshot('turn-2')
    ledger.assertNoDrift(prior, state, { mutablePartIDs: [htmlParts[1].id], mutableClipIDs: [htmlParts[1].clip_id].filter(Boolean) })
    const beforeAppend = state
    await postTurn(3, `${common} Append exactly one new 10-second HTML part at the end with exactly two live HTML candidates. Select one candidate, export it, and promote its derivative. Do not change any existing part. Report marker VIDEO_STUDIO_TURN_3_DONE.`)
    state = await snapshot('turn-3')
    ledger.assertNoDrift(beforeAppend, state, { retimedClipIDs: [videoClip.id], allowAppendedParts: true, allowAppendedClips: true }); assert(state.parts.length === beforeAppend.parts.length + 1, 'turn 3 did not append exactly one part')
    const newPart = state.parts.find((part) => !beforeAppend.parts.some((old) => old.id === part.id)); assert(newPart, 'appended part identity unavailable')
    const staleProposal = beforeAppend.proposal_id, staleRevision = beforeAppend.working_revision_id, beforeReplace = state
    await postTurn(4, `${common} First deliberately call the appropriate Video Studio mutation with stale proposal ${staleProposal} and stale base/working revision ${staleRevision}; require the tool to reject it explicitly and do not retry that stale action. Then use the current exact proposal/revision to replace only appended part ${newPart.id} with exactly two new live HTML candidates, select one, export it, and promote its derivative. Report the explicit stale rejection plus marker VIDEO_STUDIO_TURN_4_DONE.`)
    state = await snapshot('turn-4')
    ledger.assertNoDrift(beforeReplace, state, { mutablePartIDs: [newPart.id], mutableClipIDs: [newPart.clip_id].filter(Boolean) })
    const finalHydrate = await hydrate(); const events = finalHydrate.events_by_session?.[sessionID] || []; const messages = finalHydrate.messages_by_session?.[sessionID] || []
    const staleEvidence = JSON.stringify([...events, ...messages]).toLowerCase()
    assert(staleEvidence.includes(String(staleProposal).toLowerCase()) && /(stale|superseded|base_revision|revision mismatch|conflict)/.test(staleEvidence), 'durable state contains no explicit stale proposal/revision rejection')
    result.gates.four_turns_completed = true; result.gates.non_target_identity_preserved = true; result.gates.stale_action_rejected = true; result.gates.model_setting_restored = false
    result.ledger = ledger.summary(); result.result = 'PASS'
  } catch (error) { result.failures.push(error?.message || String(error)); result.error = error?.stack || String(error) }
  finally {
    if (settingsChanged && originalSettings) {
      try { await api('PATCH', '/v1/agent-model-settings', { swarm: originalSettings }, 'restore model setting'); result.gates.model_setting_restored = true }
      catch (error) { result.failures.push(`model setting restoration failed: ${error?.message || error}`); result.result = 'NOT_DONE' }
    }
    if (result.result === 'PASS' && result.gates.model_setting_restored !== true) result.result = 'NOT_DONE'
    result.completed_at = new Date().toISOString(); result.session = result.session ? { id: result.session.id } : undefined
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`); if (result.result !== 'PASS') process.exitCode = 2
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  run().catch((error) => { process.stderr.write(`${error?.stack || error}\n`); process.exitCode = 2 })
}
