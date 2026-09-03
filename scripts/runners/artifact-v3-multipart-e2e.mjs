#!/usr/bin/env node
import crypto from 'node:crypto'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const rootDir = path.resolve(scriptDir, '..', '..')
const webPackage = path.join(rootDir, 'web', 'package.json')
const argv = process.argv.slice(2)
const option = (name, fallback = '') => { const index = argv.indexOf(name); return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback }
const flag = (name) => argv.includes(name)
const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const desktopURL = String(option('--desktop-url', process.env.SWARM_DESKTOP_URL || apiURL)).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'fireworks')).trim().toLowerCase()
const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || 'high')).trim().toLowerCase()
const designerModel = String(option('--designer-model', process.env.SWARM_RUNNER_DESIGNER_MODEL || '')).trim()
const designerThinking = String(option('--designer-thinking', process.env.SWARM_RUNNER_DESIGNER_THINKING || 'high')).trim().toLowerCase()
const workspaceOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const sessionOverride = String(option('--session-id', process.env.SWARM_RUNNER_SESSION_ID || '')).trim()
const initialRunOverride = String(option('--initial-run-id', process.env.SWARM_RUNNER_INITIAL_RUN_ID || '')).trim()
const desktopPathOverride = String(option('--desktop-path', process.env.SWARM_RUNNER_DESKTOP_PATH || '')).trim()
const browserExecutable = String(option('--browser-executable', process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || '')).trim()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const preflight = flag('--preflight')
const headless = !flag('--headful')
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()
const testID = `artifact-v3-three-animated-parts-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const tmpRoot = process.env.TMPDIR || os.tmpdir()
const evidenceDir = path.resolve(option('--evidence-dir', path.join(tmpRoot, testID)))
const deadline = Date.now() + timeoutMs
const result = {
  result: 'NOT_DONE', test: 'artifact-v3-multipart-e2e', test_id: testID,
  started_at: new Date().toISOString(), provider, model: {}, ids: {}, revisions: {},
  ui: {}, animations: {}, screenshots: [], realtime: {}, gates: {}, failures: [],
}
let token = suppliedToken
let browser
let context
let page
let originalSwarmSettings = null
let originalDesignerSettings = null
let modelSettingsChanged = false
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[artifact-v3-multipart-e2e] ${message}\n`)
const fail = (message) => { result.failures.push(message); throw new Error(message) }
const assert = (condition, message) => { if (!condition) fail(message) }
const text = (value) => String(value ?? '').trim()
const sha256 = (value) => crypto.createHash('sha256').update(value).digest('hex')
const slug = (value) => text(value).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
const terminalStatus = (status) => ['completed', 'failed', 'cancelled', 'expired', 'interrupted'].includes(text(status).toLowerCase())
const artifactRoute = (sessionID, suffix = '') => `/v3/sessions/${encodeURIComponent(sessionID)}/artifacts-v3${suffix}`

function forbiddenLegacyWrite(value) {
  return /artifact_v2|artifact\.v2\.|artv2_|partv2_|prev2_|compv2_|published_head_id|collection_id|variant_id/.test(JSON.stringify(value))
}

async function api(method, route, body, label = route, allowError = false) {
  const headers = { Accept: 'application/json', Origin: new URL(apiURL).origin, Referer: `${desktopURL}/app`, 'Sec-Fetch-Site': 'same-origin' }
  if (token) { headers['X-Swarm-Token'] = token; headers.Cookie = `swarm_desktop_session=${token}` }
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(new Error(`${label} timed out`)), 120000)
  try {
    const init = { method, headers, signal: controller.signal }
    if (body !== undefined) { headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body) }
    const response = await fetch(`${apiURL}${route}`, init)
    const responseText = await response.text()
    let decoded = null
    try { decoded = responseText ? JSON.parse(responseText) : null } catch { decoded = { raw: responseText } }
    if (!allowError && !response.ok) fail(`${label} failed with HTTP ${response.status}: ${responseText.slice(0, 1200)}`)
    return { ok: response.ok, status: response.status, body: decoded, text: responseText }
  } finally { clearTimeout(timer) }
}

function loadPlaywright() {
  try { return createRequire(webPackage)('playwright') } catch (error) { fail(`Playwright is unavailable from web/package.json: ${error instanceof Error ? error.message : error}`) }
}

async function auth() {
  if (token) return
  const response = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
  token = text(response.body?.token)
  assert(token, 'desktop authentication returned no token')
}

function modelAssignment(records, model, thinking, role) {
  assert(model, `${role} model must be supplied by the canonical testbench configuration`)
  const record = records.find((candidate) => text(candidate?.model) === model)
  assert(record?.model, `model catalog does not contain ${provider}/${model} for ${role}`)
  const options = Array.isArray(record.thinking_options) ? record.thinking_options.map((value) => text(value).toLowerCase()) : []
  const resolvedThinking = options.includes(thinking) ? thinking : text(record.default_thinking || options[0] || 'low').toLowerCase()
  return { provider, model: text(record.model), thinking: resolvedThinking }
}

async function configureModels() {
  const providers = (await api('GET', '/v1/providers', undefined, 'read providers')).body?.providers || []
  const providerStatus = providers.find((item) => text(item?.id).toLowerCase() === provider)
  assert(providerStatus && providerStatus.runnable !== false, `provider ${provider} is not runnable through the synced testbench credential`)
  const records = (await api('GET', `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=500`, undefined, 'read model catalog')).body?.records || []
  const action = modelAssignment(records, actionModel, actionThinking, 'Swarm action')
  const designer = modelAssignment(records, designerModel, designerThinking, 'Designer')
  const settings = (await api('GET', '/v1/agent-model-settings', undefined, 'read model settings')).body?.agent_model_settings || {}
  originalSwarmSettings = settings.swarm || null
  originalDesignerSettings = settings.system_agents?.designer || null
  assert(originalSwarmSettings && originalDesignerSettings, 'canonical Swarm or Designer model setting is missing')
  modelSettingsChanged = true
  await api('PATCH', '/v1/agent-model-settings', { swarm: { action, plan: originalSwarmSettings.plan || action } }, 'configure Fireworks Swarm model')
  await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer } }, 'configure Fireworks Designer model')
  const configured = (await api('GET', '/v1/agent-model-settings', undefined, 'verify model settings')).body?.agent_model_settings || {}
  assert(configured?.swarm?.action?.model === action.model && configured?.system_agents?.designer?.model === designer.model, 'Fireworks model settings did not persist')
  result.model = { action, designer }
  result.gates.provider_runnable = true
  result.gates.models_configured = true
  return action
}

async function restoreModels() {
  if (!modelSettingsChanged) return
  await api('PATCH', '/v1/agent-model-settings', { swarm: originalSwarmSettings }, 'restore Swarm model')
  await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer: originalDesignerSettings } }, 'restore Designer model')
  result.gates.models_restored = true
  modelSettingsChanged = false
}

async function topology() {
  const topologyBody = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const workspaces = (await api('GET', '/v1/workspace/list?limit=200', undefined, 'read workspaces')).body?.workspaces || []
  const byID = new Map(workspaces.filter((item) => item?.state === 'active').map((item) => [text(item.workspace_id), item]))
  const binding = (topologyBody.workspace_bindings || []).find((item) => {
    const workspace = byID.get(text(item?.source_workspace_id))
    if (!workspace || item?.state !== 'bound' || Number(item?.source_workspace_generation || 0) !== Number(workspace.workspace_generation || 0)) return false
    const candidate = text(item.source_workspace_path || item.destination_workspace_path)
    return !workspaceOverride || path.resolve(candidate) === path.resolve(workspaceOverride)
  })
  const runtime = (topologyBody.runtimes || []).find((item) => item?.relationship === 'self') || (topologyBody.runtimes || [])[0]
  assert(binding?.workspace_binding_id && runtime?.swarm_id, 'no current bound workspace and self runtime are available')
  return { binding, runtime }
}

async function createSession(selected, assignment) {
  const binding = selected.binding
  const workspacePath = text(binding.source_workspace_path || binding.destination_workspace_path)
  const workspaceName = text(binding.source_workspace_name || binding.destination_workspace_name || 'artifact-v3-e2e')
  const response = await api('POST', '/v3/sessions', {
    client_request_id: `${testID}:session`, title: `${testID} provider journey`,
    workspace_path: workspacePath, workspace_name: workspaceName, workspace_binding_id: binding.workspace_binding_id,
    swarm_id: selected.runtime.swarm_id, target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: 'swarm',
    preference: assignment, model_profile: { temporary: { ...assignment, name: `${testID} Fireworks action model` } },
    metadata: { runner_test: 'artifact-v3-multipart-e2e', runner_test_id: testID },
  }, 'create V3 artifact session')
  const sessionID = text(response.body?.session?.id || response.body?.session_id)
  assert(sessionID, 'session create returned no ID')
  result.ids.session_id = sessionID
  result.ids.desktop_path = `/${slug(workspaceName)}/${sessionID}`
  return { sessionID, workspacePath, workspaceName }
}

async function approvePending(sessionID) {
  const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`, undefined, 'list pending permissions')
  const pending = response.body?.permissions || []
  if (pending.length === 0) return 0
  const resolved = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/resolve_all`, { action: 'allow_once', reason: `${testID} checked-in Artifact V3 journey`, limit: 50 }, 'approve Artifact V3 journey permissions')
  return Number(resolved.body?.count || 0)
}

async function hydrate(sessionID) {
  return (await api('POST', '/v3/sync/hydrate', {
    surface: 'desktop', session_ids: [sessionID], history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' },
    resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: true, permission_summaries: true }, include_active: true,
  }, 'hydrate artifact session')).body || {}
}

async function waitForRun(sessionID, runID, label) {
  let nextHeartbeat = 0
  while (Date.now() < deadline) {
    const approvals = await approvePending(sessionID)
    const snapshot = await hydrate(sessionID)
    const intent = (snapshot.run_intents_by_session?.[sessionID] || []).find((item) => text(item?.run_id) === runID)
    if (Date.now() >= nextHeartbeat) { log(`${label} status=${intent?.status || 'pending'} approvals=${approvals}`); nextHeartbeat = Date.now() + 15000 }
    if (intent && terminalStatus(intent.status)) {
      assert(text(intent.status) === 'completed', `${label} terminated as ${intent.status}`)
      return snapshot
    }
    await sleep(1500)
  }
  fail(`${label} timed out; inspect durable session ${sessionID} before retrying`)
}

async function postTurn(sessionID, label, content) {
  const response = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:${label}:${crypto.randomBytes(3).toString('hex')}`, role: 'user', content,
    metadata: { runner_test: 'artifact-v3-multipart-e2e', runner_test_id: testID, stage: label },
  }, `post ${label}`)
  const runID = text(response.body?.run_intent?.run_id || response.body?.run_id)
  assert(runID, `${label} returned no run ID`)
  result.ids[`${label}_run_id`] = runID
  return waitForRun(sessionID, runID, label)
}

async function bootstrapSessions() {
  return (await api('POST', '/v3/sync/bootstrap', {
    surface: 'desktop', selector: { kind: 'global', global: true, recent: { limit: 200 } }, history: { mode: 'none' },
    resources: { messages: false, events: false, run_intents: false, current_run_state: true, active_plan: true, permission_summaries: true }, include_active: true,
  }, 'bootstrap delegated sessions')).body || {}
}

function delegatedDesigners(bootstrap, parentID) {
  return Object.values(bootstrap.sessions_by_id || {}).filter((session) =>
    text(session?.metadata?.parent_session_id) === parentID &&
    text(session?.metadata?.lineage_kind) === 'delegated_subagent' &&
    text(session?.metadata?.requested_subagent).toLowerCase() === 'designer')
}

async function catalog(sessionID) {
  const response = await api('GET', artifactRoute(sessionID), undefined, 'read Artifact V3 catalog')
  assert(response.body?.ok === true && Array.isArray(response.body?.artifacts), 'Artifact V3 catalog shape is invalid')
  assert(!forbiddenLegacyWrite(response.body), 'Artifact V3 catalog contains a legacy write identity')
  return response.body.artifacts
}

async function detail(sessionID, artifactID) {
  const response = await api('GET', artifactRoute(sessionID, `/${encodeURIComponent(artifactID)}`), undefined, 'read Artifact V3 detail')
  const artifact = response.body?.artifact
  assert(response.body?.ok === true && (artifact?.id === artifactID || artifact?.artifact_id === artifactID), 'Artifact V3 detail shape is invalid')
  assert(!forbiddenLegacyWrite(response.body), 'Artifact V3 detail contains a legacy write identity')
  return artifact
}

function currentRevision(artifact) {
  const revision = artifact?.head || artifact?.current_revision
  assert(revision?.commit_oid && revision?.tree_oid && revision?.revision_ref, 'artifact has no exact whole-project head')
  return revision
}

async function installRealtimeRecorder(targetPage) {
  await targetPage.addInitScript(() => {
    const records = []
    const NativeWebSocket = window.WebSocket
    window.__artifactV3Records = records
    window.WebSocket = function RecordingWebSocket(url, protocols) {
      const socket = protocols === undefined ? new NativeWebSocket(url) : new NativeWebSocket(url, protocols)
      if (String(url).includes('/v3/realtime/stream')) {
        records.push({ kind: 'constructed', at: Date.now() })
        socket.addEventListener('open', () => records.push({ kind: 'open', at: Date.now() }))
        socket.addEventListener('message', (event) => {
          if (typeof event.data !== 'string') return
          try {
            const message = JSON.parse(event.data)
            const payload = message?.payload && typeof message.payload === 'object' ? message.payload : {}
            const inner = payload?.event && typeof payload.event === 'object' ? payload.event : {}
            records.push({ kind: 'message', at: Date.now(), event_type: String(message?.event_type ?? inner?.event_type ?? payload?.event_type ?? ''), session_id: String(message?.session_id ?? inner?.session_id ?? payload?.session_id ?? ''), endpoint_cursor_present: Boolean(String(message?.endpoint_cursor ?? '').trim()) })
          } catch { records.push({ kind: 'parse_error', at: Date.now() }) }
        })
      }
      return socket
    }
    window.WebSocket.prototype = NativeWebSocket.prototype
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket)
  })
}

async function screenshot(targetPage, label) {
  const file = path.join(evidenceDir, `${label}.png`)
  const bytes = await targetPage.screenshot({ path: file, fullPage: false })
  assert(bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])) && bytes.length > 12000, `${label} is not a substantive captured PNG`)
  const record = { label, file, sha256: sha256(bytes), bytes: bytes.length, viewport: targetPage.viewportSize() }
  result.screenshots.push(record)
  return record
}

async function animationSample(previewPage, expectedLabel = '') {
  for (const id of ['hero', 'pricing', 'footer']) await previewPage.locator(`#${id}`).waitFor({ state: 'visible', timeout: 30000 })
  const sample = async () => previewPage.evaluate((expected) => {
    const rows = ['hero', 'pricing', 'footer'].map((id) => {
      const part = document.getElementById(id)
      const marker = part?.querySelector('[data-motion-marker]') || part
      const rect = part?.getBoundingClientRect()
      const style = marker ? getComputedStyle(marker) : null
      const animations = part?.getAnimations({ subtree: true }) || []
      return {
        id, text: part?.textContent?.trim() || '', animationCount: animations.length,
        runningCount: animations.filter((item) => item.playState === 'running').length,
        times: animations.map((item) => Number(item.currentTime || 0)),
        signature: style ? `${style.transform}|${style.opacity}|${style.backgroundColor}|${style.boxShadow}` : '',
        rect: rect ? { left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom } : null,
        color: part ? getComputedStyle(part).color : '', fontSize: part ? parseFloat(getComputedStyle(part).fontSize) : 0,
      }
    })
    return {
      rows, innerWidth, innerHeight,
      scrollWidth: document.documentElement.scrollWidth, scrollHeight: document.documentElement.scrollHeight,
      expectedPresent: !expected || document.body.innerText.includes(expected),
    }
  }, expectedLabel)
  const before = await sample()
  const first = await screenshot(previewPage, `${expectedLabel ? 'candidate' : 'root'}-animation-frame-a`)
  await sleep(750)
  const after = await sample()
  const second = await screenshot(previewPage, `${expectedLabel ? 'candidate' : 'root'}-animation-frame-b`)
  assert(after.expectedPresent, `preview is missing requested visible label ${expectedLabel}`)
  assert(before.scrollWidth <= before.innerWidth + 2 && before.scrollHeight <= before.innerHeight + 2, 'complete preview has clipping/overflow beyond its viewport')
  for (const row of before.rows) {
    assert(row.text.length >= 8 && row.fontSize >= 12 && row.color && row.color !== 'rgba(0, 0, 0, 0)', `${row.id} content is unreadable`)
    assert(row.animationCount > 0 && row.runningCount > 0, `${row.id} has no running animation`)
    assert(row.rect && row.rect.left >= -1 && row.rect.top >= -1 && row.rect.right <= before.innerWidth + 1 && row.rect.bottom <= before.innerHeight + 1, `${row.id} is clipped outside the preview viewport`)
    const next = after.rows.find((item) => item.id === row.id)
    assert(next && next.times.some((value, index) => value > (row.times[index] || 0) + 300), `${row.id} animation timeline did not advance`)
    assert(next.signature !== row.signature, `${row.id} has a running but visually static motion marker`)
  }
  assert(first.sha256 !== second.sha256, 'animation frame captures are pixel-identical')
  return { before, after }
}

async function screenshotPreview(sessionID, artifactID, revision, expectedLabel = '') {
  const url = `${desktopURL}${artifactRoute(sessionID, `/${encodeURIComponent(artifactID)}/preview`)}?revision=${encodeURIComponent(revision.revision_ref)}`
  const previewPage = await context.newPage()
  await previewPage.goto(url, { waitUntil: 'networkidle', timeout: 60000 })
  const sample = await animationSample(previewPage, expectedLabel)
  await previewPage.close()
  return sample
}

function initialPrompt() {
  return [
    `Create one provider-backed managed Artifact V3 browser project for checked-in E2E ${testID}.`,
    'Call task exactly once in regular mode with exactly one managed Designer launch and animation_profile motion_ui, then wait for that one Designer. Do not launch an Iteration Swarm, multiple Designers, Coder, or Finder.',
    'The Designer must use only its context-bound artifact_v3_author whole-project workspace and create conventional files including swarm-artifact.json, index.html, and CSS/JavaScript as needed.',
    'The manifest must use schema_version swarm.artifact/v3, entrypoint index.html, and exactly three selector parts: hero=#hero, pricing=#pricing, footer=#footer.',
    'Make one polished responsive 1440x900 composition with all three parts simultaneously visible, readable, and fully inside the viewport with no scrollbars. Give each part a distinct obvious continuously moving CSS or WAAPI animation. Each part must contain a data-motion-marker element whose computed transform, opacity, background, or shadow visibly changes throughout the animation.',
    'Hero must visibly say Animated Hero, Pricing must show three readable price choices including Team $29, and Footer must visibly say Animated Footer. Use shared styles and shared script behavior while preserving the three stable part IDs.',
    'Build and preview the complete project, repair compile, locator, animation, contrast, clipping, overflow, or pixel problems in the same authoring turn, and finish only after one complete validated root commit is the visible Artifact head.',
    'Do not use V1/V2 artifact writers, storyboards, Video Studio, MP4, independent part bytes, source_artifact, or a monolithic upload.',
  ].join(' ')
}

function visibleChangeRequest() {
  return [
    'Visible change request from the user: change the Pricing part to a vivid magenta treatment and add the exact readable label TARGETED PRICING TURN inside #pricing.',
    'Keep Hero and Footer present, readable, and animated. Keep all three stable part IDs and their data-motion-marker animations working.',
    'Call task exactly once in regular mode with exactly one managed Designer launch, using artifact_v3_source parsed from the exact Artifact V3 reference and current head above, target_part_ids=["pricing"], section_target id=pricing kind=selector label=Pricing, output_mode=managed, and animation_profile motion_ui.',
    'Create exactly one complete candidate from that exact prior head. Build, preview, repair, and finish the whole project. Do not select the candidate or move head, and do not use any V1/V2 writer.',
  ].join(' ')
}

async function openDesktopStudio(sessionID, artifactID) {
  await page.goto(`${desktopURL}${result.ids.desktop_path}`, { waitUntil: 'domcontentloaded', timeout: 60000 })
  const row = page.locator(`[data-artifact-v3-id="${artifactID}"]`).first()
  await row.waitFor({ state: 'visible', timeout: 60000 })
  await row.click()
  const studio = page.locator('[data-testid="desktop-artifact-v3-studio"]')
  await studio.waitFor({ state: 'visible', timeout: 30000 })
  await studio.locator('[data-artifact-v3-preview]').waitFor({ state: 'visible', timeout: 30000 })
  return studio
}

async function submitSidebarIteration(sessionID, artifactID, rootRevision) {
  const studio = await openDesktopStudio(sessionID, artifactID)
  const navigator = studio.locator('[data-artifact-v3-part-navigator]')
  assert(await navigator.locator('[data-artifact-v3-part]').count() === 3, 'Desktop part navigator does not show exactly three parts')
  const pricing = navigator.locator('[data-artifact-v3-part="pricing"]')
  await pricing.click()
  assert(await pricing.getAttribute('aria-pressed') === 'true', 'Pricing was not selected in the Artifact V3 sidebar')
  assert(await navigator.locator('[data-artifact-v3-part="hero"]').getAttribute('aria-pressed') === 'false', 'Hero was selected unexpectedly')
  assert(await navigator.locator('[data-artifact-v3-part="footer"]').getAttribute('aria-pressed') === 'false', 'Footer was selected unexpectedly')
  await screenshot(page, 'desktop-pricing-only-selected')
  const iterate = studio.locator('[data-artifact-v3-iterate]')
  assert(text(await iterate.textContent()).includes('Iterate 1 selected'), 'Desktop did not describe exactly one selected target')
  await iterate.click()
  const composer = page.getByLabel('Continue Desktop V3 conversation')
  await composer.waitFor({ state: 'visible', timeout: 30000 })
  const generated = await composer.inputValue()
  assert(generated.includes('Target part IDs (intent only): pricing'), 'sidebar iteration draft lost the exact Pricing target')
  assert(generated.includes(rootRevision.revision_ref), 'sidebar iteration draft lost the exact current revision reference')
  await composer.fill(`${generated}\n\n${visibleChangeRequest()}`)
  await screenshot(page, 'desktop-normal-composer-targeted-request')
  const requestPromise = page.waitForRequest((request) => request.method() === 'POST' && new URL(request.url()).pathname === `/v3/sessions/${sessionID}/messages`, { timeout: 30000 })
  const responsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `/v3/sessions/${sessionID}/messages`, { timeout: 30000 })
  await page.getByRole('button', { name: 'Send message' }).click()
  const [request, response] = await Promise.all([requestPromise, responsePromise])
  assert(response.ok(), `normal Desktop composer submission failed with HTTP ${response.status()}`)
  const body = request.postDataJSON()
  assert(text(body?.content).includes('Target part IDs (intent only): pricing') && text(body?.content).includes('TARGETED PRICING TURN'), 'normal Desktop message body lost the targeted user request')
  const decoded = await response.json()
  const runID = text(decoded?.run_intent?.run_id || decoded?.run_id)
  assert(runID, 'normal Desktop composer response returned no run ID')
  result.ids.followup_run_id = runID
  result.ui = { selected_part_ids: ['pricing'], draft_sha256: sha256(body.content), message_client_request_id: text(body.client_request_id), normal_message_path: true }
  return waitForRun(sessionID, runID, 'sidebar-targeted-followup')
}

function targetedTurn(artifact, rootRevision) {
  const turns = artifact.turns || []
  const turn = [...turns].reverse().find((item) => item?.base_commit_oid === rootRevision.commit_oid)
  assert(turn?.turn_id && turn?.status === 'awaiting_selection', 'targeted whole-project turn is not awaiting selection')
  assert(Array.isArray(turn.target_part_ids) && turn.target_part_ids.length === 1 && turn.target_part_ids[0] === 'pricing', 'follow-up turn did not target exactly Pricing')
  assert(Array.isArray(turn.candidates) && turn.candidates.length === 1, 'follow-up turn did not produce exactly one complete candidate')
  const candidate = turn.candidates[0]
  assert(candidate.status === 'ready' && candidate.revision?.commit_oid && candidate.revision?.tree_oid && candidate.revision?.revision_ref, 'follow-up candidate is not a ready exact revision')
  assert(candidate.build?.status === 'succeeded' && candidate.validation?.status === 'valid', 'follow-up candidate lacks complete build and rendered validation evidence')
  assert(Array.isArray(candidate.revision.parents) && candidate.revision.parents.length === 1 && candidate.revision.parents[0] === rootRevision.commit_oid, 'follow-up candidate is not a direct child of the exact prior head')
  return { turn, candidate }
}

async function verifyStudioTurns(sessionID, artifactID, turn, candidate, rootRevision) {
  const studio = await openDesktopStudio(sessionID, artifactID)
  const turns = studio.locator('[data-artifact-v3-turns] [data-artifact-v3-turn]')
  assert(await turns.count() === 2, 'Artifact Studio does not show the initial and follow-up turns coherently')
  const targetTurn = studio.locator(`[data-artifact-v3-turn="${turn.turn_id}"]`)
  await targetTurn.waitFor({ state: 'visible', timeout: 30000 })
  assert(text(await targetTurn.textContent()).includes('target pricing'), 'Artifact Studio follow-up turn lost the targeted Pricing identity')
  const candidateRow = targetTurn.locator(`[data-artifact-v3-candidate="${candidate.candidate_id}"]`)
  await candidateRow.waitFor({ state: 'visible', timeout: 30000 })
  await candidateRow.getByRole('button').first().click()
  await studio.locator(`[data-artifact-v3-preview-revision="${candidate.revision.commit_oid}"]`).waitFor({ state: 'visible', timeout: 30000 })
  assert(await studio.locator('[data-artifact-v3-revision-history] button').count() >= 2, 'Artifact Studio revision history omitted the candidate or prior head')
  await screenshot(page, 'desktop-turn-by-turn-artifact-studio')
  const rootButton = studio.locator(`[data-artifact-v3-revision="${rootRevision.commit_oid}"]`).first()
  await rootButton.click()
  await studio.getByText(/Viewing prior revision/).waitFor({ state: 'visible', timeout: 30000 })
  await screenshot(page, 'desktop-exact-prior-revision')
  result.gates.desktop_turn_timeline = true
  result.gates.desktop_prior_revision = true
}

async function runLive() {
  assert(apiURL && /^https?:\/\//.test(apiURL), '--api-url is required for the live journey')
  assert(desktopURL && /^https?:\/\//.test(desktopURL), '--desktop-url is invalid')
  assert(Number.isFinite(timeoutMs) && timeoutMs >= 300000 && timeoutMs <= 600000, '--timeout-ms must be between 300000 and 600000')
  await auth()
  const assignment = await configureModels()
  const selected = await topology()
  let session
  if (sessionOverride) {
    assert(initialRunOverride && desktopPathOverride.startsWith('/'), 'resuming requires --initial-run-id and an absolute --desktop-path')
    const existing = await api('GET', `/v3/sessions/${encodeURIComponent(sessionOverride)}`, undefined, 'read resumed Artifact V3 session')
    assert(text(existing.body?.session?.id || existing.body?.id) === sessionOverride, 'resumed Artifact V3 session was not found')
    session = { sessionID: sessionOverride, workspacePath: '', workspaceName: '' }
    result.ids.session_id = sessionOverride
    result.ids.desktop_path = desktopPathOverride
    result.ids.initial_run_id = initialRunOverride
  } else {
    session = await createSession(selected, assignment)
  }
  const routeProbe = await api('GET', artifactRoute(session.sessionID), undefined, 'probe Artifact V3 route', true)
  assert(routeProbe.ok && routeProbe.body?.ok === true && Array.isArray(routeProbe.body?.artifacts), `native Artifact V3 catalog is unavailable (HTTP ${routeProbe.status})`)
  const playwright = loadPlaywright()
  browser = await playwright.chromium.launch({ headless, executablePath: browserExecutable || undefined })
  context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  page = await context.newPage()
  await installRealtimeRecorder(page)
  await page.goto(`${desktopURL}/${slug(session.workspaceName)}`, { waitUntil: 'domcontentloaded', timeout: 60000 })
  await page.locator('body').waitFor({ state: 'visible' })

  const childrenBefore = delegatedDesigners(await bootstrapSessions(), session.sessionID)
  if (sessionOverride) {
    await waitForRun(session.sessionID, initialRunOverride, 'initial-resume')
  } else {
    assert(childrenBefore.length === 0, 'fresh journey session already has delegated Designer children')
    await postTurn(session.sessionID, 'initial', initialPrompt())
  }
  const items = await catalog(session.sessionID)
  assert(items.length === 1, `initial request produced ${items.length} Artifact V3 projects instead of one`)
  const artifact = items[0]
  const rootDetail = await detail(session.sessionID, artifact.id)
  const rootRevision = currentRevision(rootDetail)
  assert(rootDetail.parts?.length === 3 && rootDetail.parts.map((part) => part.id).join(',') === 'hero,pricing,footer', 'root Artifact does not contain exactly hero, pricing, and footer')
  assert(rootDetail.head?.build?.status === 'succeeded' && rootDetail.head?.validation?.status === 'valid', 'root head lacks whole-project build/render evidence')
  const childrenAfterRoot = delegatedDesigners(await bootstrapSessions(), session.sessionID)
  assert(childrenAfterRoot.length === 1, `initial turn launched ${childrenAfterRoot.length} Designers instead of exactly one`)
  if (sessionOverride) assert(childrenBefore.length <= 1, `resumed initial turn already had ${childrenBefore.length} Designer children before completion`)
  result.ids.artifact_id = artifact.id
  result.ids.initial_designer_session_id = text(childrenAfterRoot[0]?.id)
  result.revisions.root = rootRevision
  result.animations.root = await screenshotPreview(session.sessionID, artifact.id, rootRevision)
  result.gates.one_initial_designer = true
  result.gates.three_animated_parts = true
  result.gates.root_complete_preview = true

  const followSnapshot = await submitSidebarIteration(session.sessionID, artifact.id, rootRevision)
  const withCandidate = await detail(session.sessionID, artifact.id)
  assert(currentRevision(withCandidate).commit_oid === rootRevision.commit_oid, 'candidate generation moved the head before user selection')
  assert((withCandidate.turns || []).length === 2, `Artifact has ${(withCandidate.turns || []).length} turns instead of initial plus follow-up`)
  const { turn, candidate } = targetedTurn(withCandidate, rootRevision)
  const childrenAfterFollowup = delegatedDesigners(await bootstrapSessions(), session.sessionID)
  assert(childrenAfterFollowup.length === 2, `targeted follow-up left ${childrenAfterFollowup.length} total Designers; expected one new Designer after the initial one`)
  const newChild = childrenAfterFollowup.find((child) => !childrenAfterRoot.some((prior) => text(prior?.id) === text(child?.id)))
  assert(newChild?.id, 'targeted follow-up did not produce one distinct Designer child')
  result.ids.followup_designer_session_id = text(newChild.id)
  result.ids.turn_id = turn.turn_id
  result.ids.candidate_id = candidate.candidate_id
  result.revisions.candidate = candidate.revision
  result.animations.candidate = await screenshotPreview(session.sessionID, artifact.id, candidate.revision, 'TARGETED PRICING TURN')
  assert(!JSON.stringify(result.animations.root).includes('TARGETED PRICING TURN'), 'root revision was mutated by the targeted follow-up')
  await verifyStudioTurns(session.sessionID, artifact.id, turn, candidate, rootRevision)

  const messages = followSnapshot.messages_by_session?.[session.sessionID] || []
  const userFollowup = [...messages].reverse().find((message) => text(message?.role).toLowerCase() === 'user' && text(message?.content).includes('TARGETED PRICING TURN'))
  assert(userFollowup && text(userFollowup.content).includes('Target part IDs (intent only): pricing'), 'durable session message history lost the sidebar-targeted user interaction')
  const events = followSnapshot.events_by_session?.[session.sessionID] || []
  const artifactEvents = events.filter((event) => text(event?.event_type).startsWith('artifact.v3.'))
  assert(artifactEvents.length >= 4 && !forbiddenLegacyWrite(artifactEvents), 'durable replay lacks native Artifact V3 turn events or contains legacy write identity')
  const records = await page.evaluate(() => window.__artifactV3Records || [])
  const liveEvents = records.filter((record) => record.kind === 'message' && record.session_id === session.sessionID && record.event_type.startsWith('artifact.v3.'))
  assert(liveEvents.length >= 1 && liveEvents.some((record) => record.endpoint_cursor_present), 'Desktop observed no cursor-bearing Artifact V3 realtime event')
  result.realtime = { recorded: records.length, artifact_events: liveEvents.length, replay_events: artifactEvents.length }
  result.gates.sidebar_one_part_message = true
  result.gates.exact_child_revision = true
  result.gates.unrelated_parts_preserved_and_animated = true
  result.gates.http_native_v3 = true
  result.gates.realtime_native_v3 = true
  result.gates.no_legacy_writes = true
  result.result = 'PASS'
}

await fs.mkdir(evidenceDir, { recursive: true })
try {
  if (preflight) {
    assert(apiURL && /^https?:\/\//.test(apiURL), '--api-url is required for provider-backed preflight')
    await auth()
    await configureModels()
    const selected = await topology()
    const session = await createSession(selected, result.model.action)
    const probe = await api('GET', artifactRoute(session.sessionID), undefined, 'probe Artifact V3 route', true)
    assert(probe.ok && probe.body?.ok === true && Array.isArray(probe.body?.artifacts), `native Artifact V3 catalog is unavailable (HTTP ${probe.status})`)
    result.result = 'PREFLIGHT_READY'
  } else {
    await runLive()
  }
} catch (error) {
  result.error = error instanceof Error ? error.stack : String(error)
  log(result.error)
  if (result.result !== 'PASS' && result.result !== 'PREFLIGHT_READY') result.result = 'RED'
} finally {
  if (page) {
    const records = await page.evaluate(() => window.__artifactV3Records || []).catch(() => [])
    await fs.writeFile(path.join(evidenceDir, 'realtime-records.json'), `${JSON.stringify(records, null, 2)}\n`).catch(() => undefined)
  }
  if (browser) await browser.close().catch(() => undefined)
  try { await restoreModels() } catch (error) {
    result.failures.push(`model restoration failed: ${error instanceof Error ? error.message : error}`)
    result.result = 'RED'
  }
  result.completed_at = new Date().toISOString()
  result.evidence_dir = evidenceDir
  await fs.writeFile(path.join(evidenceDir, 'summary.json'), `${JSON.stringify(result, null, 2)}\n`)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (!['PASS', 'PREFLIGHT_READY'].includes(result.result)) process.exitCode = 2
}
