#!/usr/bin/env node
import crypto from 'node:crypto'
import { execFile } from 'node:child_process'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const rootDir = path.resolve(scriptDir, '..', '..')
const webPackage = path.resolve(String(process.env.SWARM_RUNNER_WEB_PACKAGE || path.join(rootDir, 'web', 'package.json')))
const argv = process.argv.slice(2)
const option = (name, fallback = '') => { const index = argv.indexOf(name); return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback }
const flag = (name) => argv.includes(name)
const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const desktopURL = String(option('--desktop-url', process.env.SWARM_DESKTOP_URL || apiURL)).replace(/\/$/, '')
const provider = String(option('--provider', process.env.SWARM_RUNNER_PROVIDER || 'fireworks')).trim().toLowerCase()
const actionModel = String(option('--action-model', process.env.SWARM_RUNNER_ACTION_MODEL || '')).trim()
const basicHTMLActionModel = String(option('--basic-html-action-model', process.env.SWARM_RUNNER_BASIC_HTML_ACTION_MODEL || 'kimi-k3')).trim()
const actionThinking = String(option('--action-thinking', process.env.SWARM_RUNNER_ACTION_THINKING || 'off')).trim().toLowerCase()
const designerModel = String(option('--designer-model', process.env.SWARM_RUNNER_DESIGNER_MODEL || '')).trim()
const designerThinking = String(option('--designer-thinking', process.env.SWARM_RUNNER_DESIGNER_THINKING || 'off')).trim().toLowerCase()
const workspaceOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const sessionOverride = String(option('--session-id', process.env.SWARM_RUNNER_SESSION_ID || '')).trim()
const initialRunOverride = String(option('--initial-run-id', process.env.SWARM_RUNNER_INITIAL_RUN_ID || '')).trim()
const desktopPathOverride = String(option('--desktop-path', process.env.SWARM_RUNNER_DESKTOP_PATH || '')).trim()
const browserExecutable = String(option('--browser-executable', process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || '')).trim()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const preflight = flag('--preflight')
const stage = String(option('--stage', 'full')).trim().toLowerCase()
const noDesignerStage = stage === 'basic-html' || stage === 'targeted-part' || stage === 'selected-continuation'
const headless = !flag('--headful')
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()
const testID = `artifact-v3-three-animated-parts-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const tmpRoot = process.env.TMPDIR || os.tmpdir()
const evidenceDir = path.resolve(option('--evidence-dir', path.join(tmpRoot, testID)))
const deadline = Date.now() + timeoutMs
const result = {
  result: 'NOT_DONE', test: 'artifact-v3-multipart-e2e', test_id: testID,
  started_at: new Date().toISOString(), provider, stage, model: {}, ids: {}, revisions: {},
  ui: {}, animations: {}, screenshots: [], realtime: {}, gates: {}, failures: [],
}
let token = suppliedToken
let browser
let context
let page
let originalSwarmSettings = null
let originalDesignerSettings = null
let modelSettingsChanged = false
const execFileAsync = promisify(execFile)
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (message) => process.stderr.write(`[artifact-v3-multipart-e2e] ${message}\n`)
const gate = (name, state, detail = '') => {
  result.gates[name] = state === 'PASS'
  log(`GATE ${name} ${state}${detail ? ` — ${detail}` : ''}`)
}
const fail = (message) => { result.failures.push(message); log(`BLOCKED — ${message}`); throw new Error(message) }
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
  try { return createRequire(webPackage)('playwright') } catch (error) { fail(`Playwright is unavailable from the trusted web package boundary ${webPackage}: ${error instanceof Error ? error.message : error}`) }
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
  const resolvedActionModel = noDesignerStage ? basicHTMLActionModel : actionModel
  const action = modelAssignment(records, resolvedActionModel, actionThinking, 'Swarm action')
  const designer = noDesignerStage ? null : modelAssignment(records, designerModel, designerThinking, 'Designer')
  const settings = (await api('GET', '/v1/agent-model-settings', undefined, 'read model settings')).body?.agent_model_settings || {}
  originalSwarmSettings = settings.swarm || null
  originalDesignerSettings = settings.system_agents?.designer || null
  assert(originalSwarmSettings && (noDesignerStage || originalDesignerSettings), 'canonical Swarm or Designer model setting is missing')
  modelSettingsChanged = true
  await api('PATCH', '/v1/agent-model-settings', { swarm: { action, plan: originalSwarmSettings.plan || action } }, 'configure Fireworks Swarm model')
  if (designer) await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer } }, 'configure Fireworks Designer model')
  const configured = (await api('GET', '/v1/agent-model-settings', undefined, 'verify model settings')).body?.agent_model_settings || {}
  assert(configured?.swarm?.action?.model === action.model && (!designer || configured?.system_agents?.designer?.model === designer.model), 'Fireworks model settings did not persist')
  result.model = designer ? { action, designer } : { action }
  gate('provider', 'PASS', `${provider}/${action.model}`)
  gate('model-settings', 'PASS', noDesignerStage ? 'Swarm action model configured' : 'Swarm and Designer models configured')
  return action
}

async function restoreModels() {
  if (!modelSettingsChanged) return
  await api('PATCH', '/v1/agent-model-settings', { swarm: originalSwarmSettings }, 'restore Swarm model')
  if (!noDesignerStage) await api('PATCH', '/v1/agent-model-settings', { system_agents: { designer: originalDesignerSettings } }, 'restore Designer model')
  gate('model-settings-restored', 'PASS')
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
  gate('fresh-session', 'PASS', 'ordinary primary Swarm auto session created')
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

async function replayEvents(sessionID) {
  const events = []
  let afterSeq = 0
  for (let pageIndex = 0; pageIndex < 20; pageIndex++) {
    const response = await api('GET', `/v3/sessions/${encodeURIComponent(sessionID)}/events?after_seq=${afterSeq}&limit=500`, undefined, 'replay durable session events')
    const page = Array.isArray(response.body?.events) ? response.body.events : []
    events.push(...page)
    const nextSeq = Number(response.body?.next_seq || afterSeq)
    if (page.length === 0 || nextSeq <= afterSeq) break
    afterSeq = nextSeq
    if (page.length < 500) break
  }
  return events
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

async function waitForTargetedTurn(sessionID, artifactID, baseCommit) {
  const timeoutAt = Math.min(deadline, Date.now() + 30000)
  while (Date.now() < timeoutAt) {
    const artifact = await detail(sessionID, artifactID)
    if ((artifact.turns || []).some((turn) => turn?.base_commit_oid === baseCommit && turn?.status === 'awaiting_selection')) return artifact
    await sleep(500)
  }
  return detail(sessionID, artifactID)
}

function currentRevision(artifact) {
  const revision = artifact?.head || artifact?.current_revision
  assert(revision?.commit_oid && revision?.tree_oid && revision?.revision_ref, 'artifact has no exact whole-project head')
  return revision
}

async function installRealtimeRecorder(targetPage) {
  await targetPage.addInitScript(() => {
    const storageKey = 'swarm.artifact-v3-e2e.realtime-records'
    let records = []
    try {
      const persisted = JSON.parse(sessionStorage.getItem(storageKey) || '[]')
      if (Array.isArray(persisted)) records = persisted.slice(-10000)
    } catch { records = [] }
    const record = (value) => {
      records.push(value)
      if (records.length > 10000) records = records.slice(-10000)
      window.__artifactV3Records = records
      try { sessionStorage.setItem(storageKey, JSON.stringify(records)) } catch { /* bounded diagnostics are best-effort */ }
    }
    const NativeWebSocket = window.WebSocket
    window.__artifactV3Records = records
    window.WebSocket = function RecordingWebSocket(url, protocols) {
      const socket = protocols === undefined ? new NativeWebSocket(url) : new NativeWebSocket(url, protocols)
      if (String(url).includes('/v3/realtime/stream')) {
        record({ kind: 'constructed', at: Date.now() })
        socket.addEventListener('open', () => record({ kind: 'open', at: Date.now() }))
        socket.addEventListener('message', (event) => {
          if (typeof event.data !== 'string') return
          try {
            const message = JSON.parse(event.data)
            const payload = message?.payload && typeof message.payload === 'object' ? message.payload : {}
            const inner = payload?.event && typeof payload.event === 'object' ? payload.event : {}
            record({ kind: 'message', at: Date.now(), frame_kind: String(message?.kind ?? ''), event_type: String(message?.event_type ?? message?.event?.event_type ?? inner?.event_type ?? payload?.event_type ?? ''), session_id: String(message?.session_id ?? message?.event?.session_id ?? inner?.session_id ?? payload?.session_id ?? ''), endpoint_cursor_present: Boolean(String(message?.endpoint_cursor ?? '').trim()) })
          } catch { record({ kind: 'parse_error', at: Date.now() }) }
        })
      }
      return socket
    }
    window.WebSocket.prototype = NativeWebSocket.prototype
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket)
  })
}

async function captureFailureSessionDump() {
  if (result.result !== 'RED' || !result.ids.session_id) return
  const outputFile = path.join(evidenceDir, 'session-dump.json')
  const sessionPath = result.ids.desktop_path || `/${result.ids.session_id}`
  const sessionURL = `${desktopURL}${sessionPath}`
  try {
    const completed = await execFileAsync(path.join(rootDir, 'scripts', 'session-dump-via-api.sh'), [sessionURL, outputFile], {
      env: { ...process.env, TMPDIR: tmpRoot },
      timeout: 150000,
      maxBuffer: 64 * 1024,
    })
    const stat = await fs.stat(outputFile)
    result.session_dump = { status: 'captured', output_file: outputFile, bytes: stat.size, helper_output: text(completed.stdout) }
  } catch (error) {
    result.session_dump = { status: 'failed', output_file: outputFile, error: text(error?.stderr || error?.message || error).slice(0, 1200) }
    result.failures.push(`automatic session dump failed: ${result.session_dump.error}`)
  }
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
  log(`OBSERVE preview viewport=${before.innerWidth}x${before.innerHeight} document=${before.scrollWidth}x${before.scrollHeight}`)
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
  const access = await api('POST', artifactRoute(sessionID, `/${encodeURIComponent(artifactID)}/preview/access`), { revision_ref: revision.revision_ref }, 'authorize exact Artifact V3 preview')
  const previewPath = text(access.body?.preview_url)
  assert(previewPath.startsWith('/v3/sessions/') && previewPath.includes('/artifacts-v3/') && previewPath.includes('/preview/access/'), 'Artifact V3 preview access response is invalid')
  const previewPage = await context.newPage()
  await previewPage.goto(`${desktopURL}${previewPath}`, { waitUntil: 'networkidle', timeout: 60000 })
  const sample = noDesignerStage
    ? await staticVisualSample(previewPage, revision.manifest?.parts || [], expectedLabel)
    : await animationSample(previewPage, expectedLabel)
  await previewPage.close()
  return sample
}

async function staticVisualSample(previewPage, parts, expectedLabel = '') {
  const targets = parts.map((part) => ({ id: text(part?.id), label: text(part?.label), selector: text(part?.locator?.value) }))
  assert(targets.length === 3 && targets.every((part) => part.id && part.selector), 'static preview requires exactly three stable selector Parts')
  for (const part of targets) await previewPage.locator(part.selector).first().waitFor({ state: 'visible', timeout: 30000 })
  const sample = await previewPage.evaluate((expectedParts) => {
    const rows = expectedParts.map(({ id, label, selector }) => {
      const part = document.querySelector(selector)
      const rect = part?.getBoundingClientRect()
      const style = part ? getComputedStyle(part) : null
      return { id, label, selector, text: part?.textContent?.trim() || '', rect: rect ? { left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom } : null, color: style?.color || '', fontSize: style ? parseFloat(style.fontSize) : 0 }
    })
    return { rows, innerWidth, innerHeight, scrollWidth: document.documentElement.scrollWidth, scrollHeight: document.documentElement.scrollHeight, bodyText: document.body.innerText }
  }, targets)
  log(`OBSERVE preview viewport=${sample.innerWidth}x${sample.innerHeight} document=${sample.scrollWidth}x${sample.scrollHeight}`)
  await screenshot(previewPage, expectedLabel ? 'targeted-part-candidate-preview' : 'basic-html-root-preview')
  assert(sample.scrollWidth <= sample.innerWidth + 2 && sample.scrollHeight <= sample.innerHeight + 2, `static complete preview overflows viewport ${sample.innerWidth}x${sample.innerHeight} with document ${sample.scrollWidth}x${sample.scrollHeight}`)
  assert(sample.bodyText.includes('Team') && sample.bodyText.includes('$29'), 'static preview is missing the required Team $29 pricing choice')
  assert(!expectedLabel || sample.bodyText.includes(expectedLabel), `static preview is missing requested visible label ${expectedLabel}`)
  assert(sample.rows.some((row) => row.text.includes('Team') && row.text.includes('$29')), 'no declared Part contains the required Team $29 pricing choice')
  for (const row of sample.rows) {
    assert(row.text.length >= 8 && row.fontSize >= 12 && row.color && row.color !== 'rgba(0, 0, 0, 0)', `${row.id} static content is unreadable`)
    assert(row.rect && row.rect.left >= -1 && row.rect.top >= -1 && row.rect.right <= sample.innerWidth + 1 && row.rect.bottom <= sample.innerHeight + 1, `${row.id} is clipped outside the static preview viewport`)
  }
  return sample
}

function initialPrompt() {
  if (noDesignerStage) {
    return [
      'Create one polished static HTML artifact for a small software product page.',
      'It must have three useful visible sections: a hero introducing the product, pricing with three readable choices including Team $29, and a footer with a short call to action.',
      'Make the complete page responsive, readable at 1440x900, fully inside the viewport, and free of clipping, overlap, and scrollbars.',
      'Use shared styling so this is one coherent artifact, and expose the three sections as useful stable parts that I can request changes to later.',
      'Do not add animation, video, a storyboard, or multiple design alternatives yet.',
    ].join(' ')
  }
  return [
    `Create one provider-backed managed Artifact V3 browser project for checked-in E2E ${testID}.`,
    'Do not inspect or search the repository. This prompt is the complete authoritative brief.',
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
  if (noDesignerStage) {
    return [
      'Visible change request from the user: change only the Pricing part intent to a vivid magenta treatment and add the exact readable label TARGETED PRICING TURN inside the existing Pricing section.',
      'Use the exact native Artifact V3 reference and current revision already supplied by the Artifact Studio draft.',
      'Use manage_artifact read_v3 to read that exact complete HTML and its Parts, preserve all three stable Part IDs, then use revise_v3 with target_part_ids set to the exact Pricing Part ID from that read result and the complete corrected HTML.',
      'Create exactly one exact-base complete candidate. Inspect its pixels, but do not select it or move the selected head. Do not delegate to Designer and do not use V1/V2 artifact identity.',
    ].join(' ')
  }
  return [
    'Visible change request from the user: change the Pricing part to a vivid magenta treatment and add the exact readable label TARGETED PRICING TURN inside #pricing.',
    'Keep Hero and Footer present, readable, and animated. Keep all three stable part IDs and their data-motion-marker animations working.',
    'Call task exactly once in regular mode with exactly one managed Designer launch, using artifact_v3_source parsed from the exact Artifact V3 reference and current head above, target_part_ids=["pricing"], section_target id=pricing kind=selector label=Pricing, output_mode=managed, and animation_profile motion_ui.',
    'Create exactly one complete candidate from that exact prior head. Build, preview, repair, and finish the whole project. Do not select the candidate or move head, and do not use any V1/V2 writer.',
  ].join(' ')
}

async function openDesktopStudio(sessionID, artifactID) {
  await page.goto(`${desktopURL}${result.ids.desktop_path}`, { waitUntil: 'domcontentloaded', timeout: 60000 })
  const rows = page.locator(`[data-artifact-v3-id="${artifactID}"]`)
  const row = page.locator(`[data-artifact-v3-id="${artifactID}"]:visible`).first()
  await row.waitFor({ state: 'visible', timeout: 60000 })
  log(`OBSERVE desktop_artifact_cards total=${await rows.count()} visible=${await page.locator(`[data-artifact-v3-id="${artifactID}"]:visible`).count()}`)
  await row.click()
  const studio = page.locator('[data-testid="desktop-artifact-v3-studio"]:visible').first()
  await studio.waitFor({ state: 'visible', timeout: 30000 })
  await studio.locator('[data-artifact-v3-preview]:visible').first().waitFor({ state: 'visible', timeout: 30000 })
  return studio
}

async function submitSidebarIteration(sessionID, artifactID, rootRevision) {
  const studio = await openDesktopStudio(sessionID, artifactID)
  const navigator = studio.locator('[data-artifact-v3-part-navigator]')
  assert(await navigator.locator('[data-artifact-v3-part]').count() === 3, 'Desktop part navigator does not show exactly three parts')
  const pricingID = rootRevision.manifest.parts.find((part) => text(part?.label).toLowerCase().includes('pricing'))?.id
  assert(pricingID, 'root manifest has no semantic Pricing Part')
  const pricing = navigator.locator(`[data-artifact-v3-part="${pricingID}"]`)
  await pricing.click()
  assert(await pricing.getAttribute('aria-pressed') === 'true', 'Pricing was not selected in the Artifact V3 sidebar')
  for (const part of rootRevision.manifest.parts.filter((part) => part.id !== pricingID)) {
    assert(await navigator.locator(`[data-artifact-v3-part="${part.id}"]`).getAttribute('aria-pressed') === 'false', `${part.label || part.id} was selected unexpectedly`)
  }
  await screenshot(page, 'desktop-pricing-only-selected')
  const iterate = studio.locator('[data-artifact-v3-iterate]')
  assert(text(await iterate.textContent()).includes('Iterate 1 selected'), 'Desktop did not describe exactly one selected target')
  await iterate.click()
  const composer = page.getByLabel('Continue Desktop V3 conversation')
  await composer.waitFor({ state: 'visible', timeout: 30000 })
  const generated = await composer.inputValue()
  assert(generated.includes(`Target part IDs (intent only): ${pricingID}`), 'sidebar iteration draft lost the exact Pricing target')
  assert(generated.includes(rootRevision.revision_ref), 'sidebar iteration draft lost the exact current revision reference')
  await composer.fill(`${generated}\n\n${visibleChangeRequest()}`)
  await screenshot(page, 'desktop-normal-composer-targeted-request')
  const requestPromise = page.waitForRequest((request) => request.method() === 'POST' && new URL(request.url()).pathname === `/v3/sessions/${sessionID}/messages`, { timeout: 30000 })
  const responsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `/v3/sessions/${sessionID}/messages`, { timeout: 30000 })
  await page.getByRole('button', { name: 'Send message' }).click()
  const [request, response] = await Promise.all([requestPromise, responsePromise])
  assert(response.ok(), `normal Desktop composer submission failed with HTTP ${response.status()}`)
  const body = request.postDataJSON()
  assert(text(body?.content).includes(`Target part IDs (intent only): ${pricingID}`) && text(body?.content).includes('TARGETED PRICING TURN'), 'normal Desktop message body lost the targeted user request')
  const decoded = await response.json()
  const runID = text(decoded?.run_intent?.run_id || decoded?.run_id)
  assert(runID, 'normal Desktop composer response returned no run ID')
  result.ids.followup_run_id = runID
  result.ui = { selected_part_ids: [pricingID], draft_sha256: sha256(body.content), message_client_request_id: text(body.client_request_id), normal_message_path: true }
  return waitForRun(sessionID, runID, 'sidebar-targeted-followup')
}

function targetedTurn(artifact, rootRevision) {
  const turns = artifact.turns || []
  const pricingID = rootRevision.manifest.parts.find((part) => text(part?.label).toLowerCase().includes('pricing'))?.id
  const turn = [...turns].reverse().find((item) => item?.base_commit_oid === rootRevision.commit_oid && item?.status === 'awaiting_selection' && Array.isArray(item.target_part_ids) && item.target_part_ids.length === 1 && item.target_part_ids[0] === pricingID)
  assert(turn?.turn_id && turn?.status === 'awaiting_selection', 'targeted whole-project turn is not awaiting selection')
  assert(Array.isArray(turn.target_part_ids) && turn.target_part_ids.length === 1 && turn.target_part_ids[0] === pricingID, 'follow-up turn did not target exactly the Pricing Part')
  assert(Array.isArray(turn.candidates) && turn.candidates.length === 1, 'follow-up turn did not produce exactly one complete candidate')
  const candidate = turn.candidates[0]
  assert(candidate.status === 'ready' && candidate.revision?.commit_oid && candidate.revision?.tree_oid && candidate.revision?.revision_ref, 'follow-up candidate is not a ready exact revision')
  assert(candidate.build?.status === 'succeeded' && candidate.validation?.status === 'valid', 'follow-up candidate lacks complete build and rendered validation evidence')
  assert(Array.isArray(candidate.revision.parents) && candidate.revision.parents.length === 1 && candidate.revision.parents[0] === rootRevision.commit_oid, 'follow-up candidate is not a direct child of the exact prior head')
  return { turn, candidate }
}

async function submitSelectedContinuation(sessionID, artifactID, selectedRevision, parts) {
  const studio = page.locator('[data-testid="desktop-artifact-v3-studio"]:visible').first()
  await studio.getByRole('button', { name: 'Close Artifact V3 Studio' }).click()
  const composer = page.getByLabel('Continue Desktop V3 conversation')
  await composer.waitFor({ state: 'visible', timeout: 30000 })
  const heroID = parts.find((part) => text(part?.label).toLowerCase().includes('hero'))?.id
  assert(heroID, 'selected revision has no semantic Hero Part')
  const content = [
    'Continue the selected Artifact V3 revision with one new exact-base candidate.',
    `Use exact artifact reference session_id=${sessionID}, artifact_id=${artifactID}, revision_ref=${selectedRevision.revision_ref}.`,
    `Change the existing Hero Part ${heroID} to include the exact readable label CONTINUED SELECTED TURN while preserving every stable Part ID and the Pricing candidate changes.`,
    `Use manage_artifact read_v3, then revise_v3 with target_part_ids=["${heroID}"] and the complete corrected HTML. Inspect the candidate pixels, do not select it, do not delegate to Designer, and do not use V1/V2 identity.`,
  ].join(' ')
  await composer.fill(content)
  const responsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `/v3/sessions/${sessionID}/messages`, { timeout: 30000 })
  await page.getByRole('button', { name: 'Send message' }).click()
  const response = await responsePromise
  assert(response.ok(), `selected continuation message failed with HTTP ${response.status()}`)
  const decoded = await response.json()
  const runID = text(decoded?.run_intent?.run_id || decoded?.run_id)
  assert(runID, 'selected continuation returned no run ID')
  result.ids.continuation_run_id = runID
  const snapshot = await waitForRun(sessionID, runID, 'selected-continuation')
  return { snapshot, heroID }
}

async function selectCandidateInStudio(sessionID, artifactID, turn, candidate) {
  const studio = await openDesktopStudio(sessionID, artifactID)
  const row = studio.locator(`[data-artifact-v3-turn="${turn.turn_id}"] [data-artifact-v3-candidate="${candidate.candidate_id}"]`)
  await row.waitFor({ state: 'visible', timeout: 30000 })
  const select = row.getByRole('button', { name: 'Select head' })
  await select.waitFor({ state: 'visible', timeout: 30000 })
  const responsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `${artifactRoute(sessionID, `/${encodeURIComponent(artifactID)}`)}/turns/${encodeURIComponent(turn.turn_id)}/select`, { timeout: 30000 })
  await select.click()
  const response = await responsePromise
  assert(response.ok(), `Artifact Studio selection failed with HTTP ${response.status()}`)
  const selected = await response.json()
  const head = selected?.head
  assert(text(head?.commit_oid) === candidate.revision.commit_oid, 'Artifact Studio selected the wrong exact candidate commit')
  const timeoutAt = Math.min(deadline, Date.now() + 30000)
  while (Date.now() < timeoutAt) {
    const artifact = await detail(sessionID, artifactID)
    if (currentRevision(artifact).commit_oid === candidate.revision.commit_oid) return artifact
    await sleep(500)
  }
  return detail(sessionID, artifactID)
}

async function verifyStudioTurns(sessionID, artifactID, turn, candidate, rootRevision, expectedTurnCount) {
  const studio = await openDesktopStudio(sessionID, artifactID)
  const turns = studio.locator('[data-artifact-v3-turns] [data-artifact-v3-turn]')
  assert(await turns.count() === expectedTurnCount, `Artifact Studio shows ${await turns.count()} turns; expected ${expectedTurnCount} including prior visual repair history and the targeted follow-up`)
  const targetTurn = studio.locator(`[data-artifact-v3-turn="${turn.turn_id}"]`)
  await targetTurn.waitFor({ state: 'visible', timeout: 30000 })
  assert(text(await targetTurn.textContent()).toLowerCase().includes('target ' + turn.target_part_ids[0].toLowerCase()), 'Artifact Studio follow-up turn lost the targeted Pricing identity')
  const candidateRow = targetTurn.locator(`[data-artifact-v3-candidate="${candidate.candidate_id}"]`)
  await candidateRow.waitFor({ state: 'visible', timeout: 30000 })
  await candidateRow.getByRole('button').first().click()
  await studio.locator(`[data-artifact-v3-preview-revision="${candidate.revision.commit_oid}"]`).waitFor({ state: 'visible', timeout: 30000 })
  assert(await studio.locator('[data-artifact-v3-revision-history] button').count() >= 2, 'Artifact Studio revision history omitted the candidate or prior head')
  await screenshot(page, 'desktop-turn-by-turn-artifact-studio')
  const rootButton = studio.locator(`[data-artifact-v3-revision="${rootRevision.commit_oid}"]`).first()
  await rootButton.click()
  await studio.locator(`[data-artifact-v3-preview-revision="${rootRevision.commit_oid}"]`).waitFor({ state: 'visible', timeout: 30000 })
  assert(await rootButton.getAttribute('class').then((value) => text(value).includes('border-[var(--app-primary)]')), 'Artifact Studio did not select the exact prior revision')
  await screenshot(page, 'desktop-exact-prior-revision')
  result.gates.desktop_turn_timeline = true
  result.gates.desktop_prior_revision = true
}

async function runLive() {
  assert(apiURL && /^https?:\/\//.test(apiURL), '--api-url is required for the live journey')
  assert(desktopURL && /^https?:\/\//.test(desktopURL), '--desktop-url is invalid')
  assert(['basic-html', 'targeted-part', 'selected-continuation', 'full'].includes(stage), '--stage must be basic-html, targeted-part, selected-continuation, or full')
  assert(Number.isFinite(timeoutMs) && timeoutMs >= 300000 && timeoutMs <= 600000, '--timeout-ms must be between 300000 and 600000')
  await auth()
  const assignment = await configureModels()
  const selected = await topology()
  let session
  if (sessionOverride) {
    assert(initialRunOverride && desktopPathOverride.startsWith('/'), 'resuming requires --initial-run-id and an absolute --desktop-path')
    const existing = await api('GET', `/v3/sessions/${encodeURIComponent(sessionOverride)}`, undefined, 'read resumed Artifact V3 session')
    const resumedSession = existing.body?.session || existing.body
    assert(text(resumedSession?.id) === sessionOverride, 'resumed Artifact V3 session was not found')
    const workspaceName = text(resumedSession?.workspace_name || resumedSession?.workspaceName)
    assert(workspaceName, 'resumed Artifact V3 session has no workspace name')
    session = { sessionID: sessionOverride, workspacePath: text(resumedSession?.workspace_path || resumedSession?.workspacePath), workspaceName }
    result.ids.session_id = sessionOverride
    result.ids.desktop_path = `/${slug(workspaceName)}/${sessionOverride}`
    result.ids.initial_run_id = initialRunOverride
  } else {
    session = await createSession(selected, assignment)
  }
  const routeProbe = await api('GET', artifactRoute(session.sessionID), undefined, 'probe Artifact V3 route', true)
  assert(routeProbe.ok && routeProbe.body?.ok === true && Array.isArray(routeProbe.body?.artifacts), `native Artifact V3 catalog is unavailable (HTTP ${routeProbe.status})`)
  gate('native-v3-route', 'PASS', 'authenticated catalog available')
  const playwright = loadPlaywright()
  browser = await playwright.chromium.launch({ headless, executablePath: browserExecutable || undefined })
  context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  page = await context.newPage()
  await installRealtimeRecorder(page)
  await page.goto(`${desktopURL}${result.ids.desktop_path}`, { waitUntil: 'domcontentloaded', timeout: 60000 })
  await page.locator('body').waitFor({ state: 'visible' })
  await page.waitForFunction((sessionID) => (window.__artifactV3Records || []).some((record) => record.kind === 'message' && record.frame_kind === 'replay.complete' && record.session_id === sessionID && record.endpoint_cursor_present), session.sessionID, { timeout: 30000 })
  gate('realtime-subscribed-before-create', 'PASS', 'exact session subscription replay completed at a durable cursor')

  const childrenBefore = delegatedDesigners(await bootstrapSessions(), session.sessionID)
  let initialSnapshot
  if (sessionOverride) {
    initialSnapshot = await waitForRun(session.sessionID, initialRunOverride, 'initial-resume')
  } else {
    assert(childrenBefore.length === 0, 'fresh journey session already has delegated Designer children')
    initialSnapshot = await postTurn(session.sessionID, 'initial', initialPrompt())
  }
  const items = await catalog(session.sessionID)
  log(`OBSERVE artifacts=${items.length}`)
  assert(items.length === 1, `initial request produced ${items.length} Artifact V3 projects instead of one`)
  gate('one-artifact', 'PASS')
  const artifact = items[0]
  const rootDetail = await detail(session.sessionID, artifact.id)
  const rootRevision = currentRevision(rootDetail)
  const rootTurnCount = (rootDetail.turns || []).length
  const partIDs = rootDetail.parts?.map((part) => text(part.id)) || []
  log(`OBSERVE parts=${partIDs.join(',') || 'none'} head=${rootRevision.revision_ref}`)
  assert(partIDs.length === 3 && new Set(partIDs).size === 3 && partIDs.every(Boolean), `root Artifact must expose exactly three distinct stable Part IDs; got [${partIDs.join(',')}]`)
  gate('three-parts', 'PASS', partIDs.join(','))
  assert(rootDetail.head?.build?.status === 'succeeded' && rootDetail.head?.validation?.status === 'valid', 'root head lacks whole-project build/render evidence')
  gate('build-preview', 'PASS', `revision=${rootRevision.revision_ref}`)
  const childrenAfterRoot = delegatedDesigners(await bootstrapSessions(), session.sessionID)
  const expectedInitialDesigners = noDesignerStage ? 0 : 1
  log(`OBSERVE designer_children=${childrenAfterRoot.length}`)
  assert(childrenAfterRoot.length === expectedInitialDesigners, `initial ${stage} turn launched ${childrenAfterRoot.length} Designers instead of exactly ${expectedInitialDesigners}`)
  gate('no-designer', expectedInitialDesigners === 0 ? 'PASS' : 'SKIP', `children=${childrenAfterRoot.length}`)
  if (sessionOverride) assert(childrenBefore.length <= expectedInitialDesigners, `resumed initial turn already had ${childrenBefore.length} Designer children before completion`)
  result.ids.artifact_id = artifact.id
  if (childrenAfterRoot[0]?.id) result.ids.initial_designer_session_id = text(childrenAfterRoot[0].id)
  result.revisions.root = rootRevision
  result.animations.root = await screenshotPreview(session.sessionID, artifact.id, rootRevision)
  if (noDesignerStage) result.gates.no_designer_delegation = true
  else result.gates.one_initial_designer = true
  result.gates.root_complete_preview = true
  if (stage === 'basic-html') {
    const studio = await openDesktopStudio(session.sessionID, artifact.id)
    assert(await studio.locator('[data-artifact-v3-part-navigator] [data-artifact-v3-part]').count() === 3, 'Desktop basic HTML Part navigator does not show exactly three parts')
    await screenshot(page, 'desktop-basic-html-artifact-studio')
    const events = await replayEvents(session.sessionID)
    const artifactEvents = events.filter((event) => text(event?.event_type).startsWith('artifact.v3.'))
    log(`OBSERVE replay_events=${events.length} artifact_v3_events=${artifactEvents.length}`)
    assert(artifactEvents.length >= 2 && !forbiddenLegacyWrite(artifactEvents), 'basic HTML durable replay lacks native Artifact V3 genesis events or contains legacy identity')
    const records = await page.evaluate(() => window.__artifactV3Records || [])
    const liveEvents = records.filter((record) => record.kind === 'message' && record.session_id === session.sessionID && record.event_type.startsWith('artifact.v3.'))
    assert(liveEvents.some((record) => record.endpoint_cursor_present), 'Desktop observed no cursor-bearing Artifact V3 event for basic HTML')
    result.realtime = { recorded: records.length, artifact_events: liveEvents.length, replay_events: artifactEvents.length }
    gate('visible-pixels', 'PASS', 'root preview captured and inspected')
    gate('desktop-studio', 'PASS', 'three-Part navigator visible')
    gate('durable-replay', 'PASS', `events=${artifactEvents.length}`)
    gate('realtime', 'PASS', `cursor-bearing-events=${liveEvents.length}`)
    gate('no-legacy-writes', 'PASS')
    result.result = 'PASS'
    log('JOURNEY basic-html PASS')
    return
  }
  if (!noDesignerStage) result.gates.three_animated_parts = true

  const followSnapshot = await submitSidebarIteration(session.sessionID, artifact.id, rootRevision)
  const withCandidate = await waitForTargetedTurn(session.sessionID, artifact.id, rootRevision.commit_oid)
  assert(currentRevision(withCandidate).commit_oid === rootRevision.commit_oid, 'candidate generation moved the head before user selection')
  assert((withCandidate.turns || []).length === rootTurnCount + 1, `Artifact has ${(withCandidate.turns || []).length} turns; expected prior history ${rootTurnCount} plus one targeted follow-up`)
  const { turn, candidate } = targetedTurn(withCandidate, rootRevision)
  const childrenAfterFollowup = delegatedDesigners(await bootstrapSessions(), session.sessionID)
  const expectedFollowupDesigners = noDesignerStage ? 0 : 2
  assert(childrenAfterFollowup.length === expectedFollowupDesigners, `targeted follow-up left ${childrenAfterFollowup.length} total Designers; expected ${expectedFollowupDesigners}`)
  if (!noDesignerStage) {
    const newChild = childrenAfterFollowup.find((child) => !childrenAfterRoot.some((prior) => text(prior?.id) === text(child?.id)))
    assert(newChild?.id, 'targeted follow-up did not produce one distinct Designer child')
    result.ids.followup_designer_session_id = text(newChild.id)
  }
  result.ids.turn_id = turn.turn_id
  result.ids.candidate_id = candidate.candidate_id
  result.revisions.candidate = candidate.revision
  result.animations.candidate = await screenshotPreview(session.sessionID, artifact.id, candidate.revision, 'TARGETED PRICING TURN')
  assert(!JSON.stringify(result.animations.root).includes('TARGETED PRICING TURN'), 'root revision was mutated by the targeted follow-up')
  await verifyStudioTurns(session.sessionID, artifact.id, turn, candidate, rootRevision, rootTurnCount + 1)
  if (stage === 'selected-continuation') {
    const selectedArtifact = await selectCandidateInStudio(session.sessionID, artifact.id, turn, candidate)
    const selectedRevision = currentRevision(selectedArtifact)
    assert(selectedRevision.commit_oid === candidate.revision.commit_oid, 'selected candidate did not become the exact Artifact head')
    assert((selectedArtifact.turns || []).some((item) => item.turn_id === turn.turn_id && item.status === 'selected'), 'selected turn did not persist selected status')
    result.revisions.selected = selectedRevision
    result.gates.explicit_candidate_selection = true
    const beforeContinuationTurns = (selectedArtifact.turns || []).length
    const { heroID } = await submitSelectedContinuation(session.sessionID, artifact.id, selectedRevision, selectedRevision.manifest.parts)
    const continuedArtifact = await waitForTargetedTurn(session.sessionID, artifact.id, selectedRevision.commit_oid)
    assert(currentRevision(continuedArtifact).commit_oid === selectedRevision.commit_oid, 'continuation candidate moved the selected head')
    assert((continuedArtifact.turns || []).length === beforeContinuationTurns + 1, 'continuation did not add exactly one turn')
    const continuedTurn = [...(continuedArtifact.turns || [])].reverse().find((item) => item.base_commit_oid === selectedRevision.commit_oid && item.status === 'awaiting_selection' && item.target_part_ids?.[0] === heroID)
    assert(continuedTurn?.candidates?.length === 1, 'continued selected revision did not produce exactly one Hero candidate')
    const continuedCandidate = continuedTurn.candidates[0]
    assert(continuedCandidate.revision?.parents?.[0] === selectedRevision.commit_oid, 'continued candidate did not parent the exact selected revision')
    result.revisions.continued = continuedCandidate.revision
    result.animations.continued = await screenshotPreview(session.sessionID, artifact.id, continuedCandidate.revision, 'CONTINUED SELECTED TURN')
    assert(JSON.stringify(result.animations.continued).includes('TARGETED PRICING TURN'), 'continued candidate lost the selected Pricing change')
    result.gates.continued_from_selected_revision = true
  }
  if (stage === 'targeted-part') {
    result.gates.targeted_part_candidate = true
    result.gates.exact_base_ancestry = true
    result.gates.head_unchanged_before_selection = true
    result.gates.no_designer_delegation = true
  }

  const messages = followSnapshot.messages_by_session?.[session.sessionID] || []
  const userFollowup = [...messages].reverse().find((message) => text(message?.role).toLowerCase() === 'user' && text(message?.content).includes('TARGETED PRICING TURN'))
  assert(userFollowup && turn.target_part_ids.every((id) => text(userFollowup.content).includes(`Target part IDs (intent only): ${id}`)), 'durable session message history lost the sidebar-targeted user interaction')
  const events = await replayEvents(session.sessionID)
  const artifactEvents = events.filter((event) => text(event?.event_type).startsWith('artifact.v3.'))
  log(`OBSERVE replay_events=${events.length} artifact_v3_events=${artifactEvents.length}`)
  assert(artifactEvents.length >= 4 && !forbiddenLegacyWrite(artifactEvents), 'durable replay lacks native Artifact V3 turn events or contains legacy write identity')
  const records = await page.evaluate(() => window.__artifactV3Records || [])
  const liveEvents = records.filter((record) => record.kind === 'message' && record.session_id === session.sessionID && record.event_type.startsWith('artifact.v3.'))
  assert(liveEvents.length >= 1 && liveEvents.some((record) => record.endpoint_cursor_present), 'Desktop observed no cursor-bearing Artifact V3 realtime event')
  result.realtime = { recorded: records.length, artifact_events: liveEvents.length, replay_events: artifactEvents.length }
  result.gates.sidebar_one_part_message = true
  result.gates.exact_child_revision = true
  if (noDesignerStage) result.gates.unrelated_parts_preserved = true
  else result.gates.unrelated_parts_preserved_and_animated = true
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
  await captureFailureSessionDump()
  await fs.writeFile(path.join(evidenceDir, 'summary.json'), `${JSON.stringify(result, null, 2)}\n`)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (!['PASS', 'PREFLIGHT_READY'].includes(result.result)) process.exitCode = 2
}
