#!/usr/bin/env node
import crypto from 'node:crypto'
import { execFile as execFileCallback } from 'node:child_process'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import os from 'node:os'
import path from 'node:path'
import { promisify } from 'node:util'
import { fileURLToPath } from 'node:url'

const execFile = promisify(execFileCallback)
const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const rootDir = path.resolve(scriptDir, '..', '..')
const webPackage = path.join(rootDir, 'web', 'package.json')
const fixtureDir = path.join(rootDir, 'scripts', 'fixtures', 'artifact-v3-multipart')
const argv = process.argv.slice(2)
const option = (name, fallback = '') => { const index = argv.indexOf(name); return index >= 0 && index + 1 < argv.length ? argv[index + 1] : fallback }
const flag = (name) => argv.includes(name)
const apiURL = String(option('--api-url', process.env.SWARM_RUNNER_API_URL || '')).replace(/\/$/, '')
const desktopURL = String(option('--desktop-url', process.env.SWARM_DESKTOP_URL || apiURL)).replace(/\/$/, '')
const workspaceOverride = String(option('--workspace-path', process.env.SWARM_RUNNER_WORKSPACE_PATH || '')).trim()
const repositoryRoot = String(option('--repository-root', process.env.SWARM_ARTIFACT_V3_REPOSITORY_ROOT || '')).trim()
const browserExecutable = String(option('--browser-executable', process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || '')).trim()
const timeoutMs = Number(option('--timeout-ms', process.env.SWARM_RUNNER_TIMEOUT_MS || '600000'))
const preflight = flag('--preflight')
const headless = !flag('--headful')
const suppliedToken = String(process.env.SWARM_RUNNER_TOKEN || '').trim()
const testID = `artifact-v3-multipart-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`
const tmpRoot = process.env.TMPDIR || os.tmpdir()
const evidenceDir = path.resolve(option('--evidence-dir', path.join(tmpRoot, testID)))
const deadline = Date.now() + timeoutMs
const result = {
  result: 'RED_NOT_RUN', test: 'artifact-v3-multipart-e2e', test_id: testID,
  started_at: new Date().toISOString(), ids: {}, revisions: {}, repositories: {},
  screenshots: [], realtime: {}, high_cardinality: {}, gates: {}, failures: [],
}
let token = suppliedToken
let browser
let context
let page
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
  const raw = JSON.stringify(value)
  return /artifact_v2|artifact\.v2\.|artv2_|partv2_|prev2_|compv2_|published_head_id|collection_id|variant_id/.test(raw)
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
    return { ok: response.ok, status: response.status, body: decoded, text: responseText, headers: response.headers }
  } finally { clearTimeout(timer) }
}

async function loadFixture() {
  const manifest = JSON.parse(await fs.readFile(path.join(fixtureDir, 'swarm-artifact.json'), 'utf8'))
  const index = await fs.readFile(path.join(fixtureDir, 'index.html'), 'utf8')
  const css = await fs.readFile(path.join(fixtureDir, 'styles/theme.css'), 'utf8')
  const app = await fs.readFile(path.join(fixtureDir, 'src/app.js'), 'utf8')
  const plans = await fs.readFile(path.join(fixtureDir, 'src/plans.js'), 'utf8')
  assert(manifest.schema_version === 'swarm.artifact/v3' && manifest.entrypoint === 'index.html', 'fixture does not use the minimal V3 project manifest')
  assert(manifest.parts.length === 3 && new Set(manifest.parts.map((part) => part.id)).size === 3, 'fixture must declare exactly three stable unique targets')
  assert(index.includes('data-plan="team"') && app.includes("import { plans } from './plans.js'") && plans.includes("id: 'team'"), 'fixture lost the Hero-to-Pricing shared-code dependency')
  assert(css.includes('--accent:') && index.includes('styles/theme.css'), 'fixture lost its shared visual dependency')
  result.gates.fixture_dependency_rich = true
  return { manifest, index, css, app, plans }
}

async function generateHighCardinalityFixture() {
  const dir = await fs.mkdtemp(path.join(tmpRoot, 'artifact-v3-96-'))
  const parts = []
  const sections = []
  for (let index = 1; index <= 96; index += 1) {
    const id = `panel-${String(index).padStart(3, '0')}`
    parts.push({ id, label: `Panel ${index}`, locator: { kind: 'selector', path: 'index.html', value: `#${id}` } })
    sections.push(`<section id="${id}" data-artifact-part="${id}">Panel ${index}</section>`)
  }
  const manifest = { schema_version: 'swarm.artifact/v3', entrypoint: 'index.html', parts }
  await fs.mkdir(path.join(dir, 'styles'), { recursive: true })
  await fs.mkdir(path.join(dir, 'src'), { recursive: true })
  await fs.writeFile(path.join(dir, 'swarm-artifact.json'), `${JSON.stringify(manifest, null, 2)}\n`)
  await fs.writeFile(path.join(dir, 'index.html'), `<!doctype html><meta charset="utf-8"><title>96 targets</title><link rel="stylesheet" href="styles/theme.css"><script type="module" src="src/app.js"></script><main>${sections.join('')}</main>\n`)
  await fs.writeFile(path.join(dir, 'styles/theme.css'), `:root{--accent:#55d6be}section{border:2px solid var(--accent)}section[data-selected=true]{background:var(--accent)}\n`)
  await fs.writeFile(path.join(dir, 'src/app.js'), `document.addEventListener('click',event=>{const section=event.target.closest('section');if(section)section.dataset.selected='true'})\n`)
  assert(manifest.parts.length === 96 && manifest.parts[64].id === 'panel-065', 'generated fixture did not cross the former 64-target boundary')
  result.high_cardinality.expected_count = 96
  result.high_cardinality.expected_manifest_digest = sha256(await fs.readFile(path.join(dir, 'swarm-artifact.json')))
  result.gates.high_cardinality_fixture = true
  return { dir, manifest }
}

function loadPlaywright() {
  try { return createRequire(webPackage)('playwright') } catch (error) { fail(`Playwright is unavailable from web/package.json: ${error instanceof Error ? error.message : error}`) }
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
            records.push({
              kind: 'message', at: Date.now(),
              event_type: String(message?.event_type ?? inner?.event_type ?? payload?.event_type ?? ''),
              session_id: String(message?.session_id ?? inner?.session_id ?? payload?.session_id ?? ''),
              endpoint_cursor_present: Boolean(String(message?.endpoint_cursor ?? '').trim()),
            })
          } catch { records.push({ kind: 'parse_error', at: Date.now() }) }
        })
      }
      return socket
    }
    window.WebSocket.prototype = NativeWebSocket.prototype
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket)
  })
}

async function auth() {
  if (token) return
  const response = await api('GET', '/v1/auth/desktop/session', undefined, 'desktop authentication')
  token = text(response.body?.token)
  assert(token, 'desktop authentication returned no token')
}

async function topology() {
  const topologyBody = (await api('GET', '/v1/swarm/topology', undefined, 'read topology')).body || {}
  const workspaceBody = (await api('GET', '/v1/workspace/list?limit=200', undefined, 'read workspaces')).body || {}
  const workspaces = workspaceBody.workspaces || []
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

async function createSession(selected) {
  const binding = selected.binding
  const workspacePath = text(binding.source_workspace_path || binding.destination_workspace_path)
  const workspaceName = text(binding.source_workspace_name || binding.destination_workspace_name || 'artifact-v3-e2e')
  const response = await api('POST', '/v3/sessions', {
    client_request_id: `${testID}:session`, title: `${testID} whole project`,
    workspace_path: workspacePath, workspace_name: workspaceName, workspace_binding_id: binding.workspace_binding_id,
    swarm_id: selected.runtime.swarm_id, target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: 'swarm',
    model_profile: { use_account_default: true }, metadata: { runner_test: 'artifact-v3-multipart-e2e', runner_test_id: testID },
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
  const resolved = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/permissions/resolve_all`, { action: 'allow_once', reason: `${testID} checked-in artifact journey`, limit: 50 }, 'approve artifact journey permissions')
  return Number(resolved.body?.count || 0)
}

async function hydrate(sessionID) {
  return (await api('POST', '/v3/sync/hydrate', {
    surface: 'desktop', session_ids: [sessionID], history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 1000, manifest_policy: 'manifest' },
    resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: true, permission_summaries: true }, include_active: true,
  }, 'hydrate artifact session')).body || {}
}

async function postTurn(sessionID, label, content, artifactSelections = []) {
  const response = await api('POST', `/v3/sessions/${encodeURIComponent(sessionID)}/messages`, {
    client_request_id: `${testID}:${label}:${crypto.randomBytes(3).toString('hex')}`, role: 'user', content, artifact_selections: artifactSelections,
    metadata: { runner_test: 'artifact-v3-multipart-e2e', runner_test_id: testID, stage: label },
  }, `post ${label}`)
  const runID = text(response.body?.run_intent?.run_id || response.body?.run_id)
  assert(runID, `${label} returned no run ID`)
  result.ids[`${label}_run_id`] = runID
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

async function catalog(sessionID) {
  const response = await api('GET', artifactRoute(sessionID), undefined, 'read Artifact V3 catalog')
  assert(response.body?.ok === true && Array.isArray(response.body?.artifacts), 'Artifact V3 catalog shape is invalid')
  assert(!forbiddenLegacyWrite(response.body), 'Artifact V3 catalog contains a legacy write identity')
  return response.body.artifacts
}

async function detail(sessionID, artifactID) {
  const response = await api('GET', artifactRoute(sessionID, `/${encodeURIComponent(artifactID)}`), undefined, 'read Artifact V3 detail')
  const artifact = response.body?.artifact
  assert(response.body?.ok === true && artifact?.id === artifactID, 'Artifact V3 detail shape is invalid')
  assert(!forbiddenLegacyWrite(response.body), 'Artifact V3 detail contains a legacy write identity')
  return artifact
}

function currentRevision(artifact) {
  const revision = artifact?.head || artifact?.current_revision
  assert(revision?.commit_oid && revision?.tree_oid && revision?.revision_ref, 'artifact has no exact whole-project head')
  return revision
}

async function screenshotPreview(sessionID, artifactID, revision, label, verify) {
  const url = `${desktopURL}${artifactRoute(sessionID, `/${encodeURIComponent(artifactID)}/preview`)}?revision=${encodeURIComponent(revision.revision_ref)}`
  const previewPage = await context.newPage()
  await previewPage.goto(url, { waitUntil: 'networkidle', timeout: 60000 })
  await previewPage.locator('#hero').waitFor({ state: 'visible', timeout: 30000 })
  await verify(previewPage)
  const file = path.join(evidenceDir, `${label}.png`)
  const bytes = await previewPage.screenshot({ path: file, fullPage: false })
  const viewport = previewPage.viewportSize()
  assert(bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])) && bytes.length > 12000, `${label} is not a substantive captured PNG`)
  const record = { label, file, sha256: sha256(bytes), bytes: bytes.length, viewport }
  result.screenshots.push(record)
  await previewPage.close()
  return record
}

async function verifyRootPreview(previewPage) {
  await previewPage.locator('[data-plan="team"]').click()
  await previewPage.locator('.plan[data-plan="team"][data-selected="true"]').waitFor({ state: 'visible' })
  assert(await previewPage.locator('#plans').getByText('Team', { exact: true }).isVisible(), 'root complete preview lost Team plan')
  assert(text(await previewPage.locator('#revision').textContent()) === 'ROOT', 'root revision marker is missing')
}

async function verifyCandidatePreview(previewPage) {
  await previewPage.locator('[data-plan="studio"]').click()
  await previewPage.locator('.plan[data-plan="studio"][data-selected="true"]').waitFor({ state: 'visible' })
  assert(await previewPage.locator('#plans').getByText('Studio', { exact: true }).isVisible(), 'candidate complete preview lost Studio plan')
  assert(await previewPage.locator('#plans').getByText('$39 / month', { exact: true }).isVisible(), 'candidate complete preview has the wrong Studio price')
  const colors = await previewPage.evaluate(() => {
    const style = getComputedStyle(document.documentElement)
    return { accent: style.getPropertyValue('--accent').trim(), contrast: style.getPropertyValue('--accent-contrast').trim() }
  })
  assert(colors.accent && colors.accent !== '#55d6be' && colors.contrast, 'candidate did not repair the shared color theme')
}

async function git(repo, args, allowCode = false) {
  try { return text((await execFile('git', [`--git-dir=${repo}`, ...args], { timeout: 30000, maxBuffer: 4 << 20 })).stdout) }
  catch (error) { if (allowCode) return { code: error.code, stdout: text(error.stdout), stderr: text(error.stderr) }; fail(`native Git ${args.join(' ')} failed: ${text(error.stderr || error.message)}`) }
}

function repositoryPath(artifactID) {
  assert(repositoryRoot, '--repository-root is required for live native Git inspection')
  assert(/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(artifactID), 'artifact ID is unsafe for repository inspection')
  const repo = path.resolve(repositoryRoot, `${artifactID}.git`)
  assert(repo.startsWith(`${path.resolve(repositoryRoot)}${path.sep}`), 'artifact repository path escaped the trusted root')
  return repo
}

async function inspectRootRepository(artifactID, revision) {
  const repo = repositoryPath(artifactID)
  assert(await git(repo, ['rev-parse', '--is-bare-repository']) === 'true', 'artifact repository is not bare')
  await git(repo, ['fsck', '--full', '--strict'])
  const head = await git(repo, ['rev-parse', 'refs/heads/artifact'])
  assert(head === revision.commit_oid, 'native Artifact head does not equal projected root commit')
  const parentLine = (await git(repo, ['rev-list', '--parents', '-n', '1', revision.commit_oid])).split(/\s+/)
  assert(parentLine.length === 1, 'root Artifact revision unexpectedly has a parent')
  const files = (await git(repo, ['ls-tree', '-r', '--name-only', revision.commit_oid])).split('\n').filter(Boolean)
  for (const expected of ['swarm-artifact.json', 'index.html', 'styles/theme.css', 'src/app.js', 'src/plans.js']) assert(files.includes(expected), `root commit lacks conventional project file ${expected}`)
  assert(!files.some((file) => file === 'content' || file.startsWith('parts/')), 'root commit uses a synthetic blob/parts layout')
  const manifest = JSON.parse(await git(repo, ['show', `${revision.commit_oid}:swarm-artifact.json`]))
  assert(manifest.schema_version === 'swarm.artifact/v3' && manifest.parts.length === 3, 'root commit manifest is not the exact V3 fixture shape')
  result.repositories[artifactID] = { repository: repo, root_commit: revision.commit_oid, root_tree: revision.tree_oid, files }
  result.gates.native_root_commit = true
  return repo
}

async function inspectCandidateRepository(repo, rootRevision, candidateRevision) {
  await git(repo, ['fsck', '--full', '--strict'])
  const ancestry = await git(repo, ['merge-base', '--is-ancestor', rootRevision.commit_oid, candidateRevision.commit_oid], true)
  assert(typeof ancestry === 'string' || ancestry.code === 0, 'candidate commit is not a child of the exact root')
  const parentLine = (await git(repo, ['rev-list', '--parents', '-n', '1', candidateRevision.commit_oid])).split(/\s+/)
  assert(parentLine.length === 2 && parentLine[1] === rootRevision.commit_oid, 'candidate does not have the exact base as its sole parent')
  const changed = (await git(repo, ['diff', '--name-only', rootRevision.commit_oid, candidateRevision.commit_oid])).split('\n').filter(Boolean)
  for (const expected of ['index.html', 'styles/theme.css', 'src/plans.js']) assert(changed.includes(expected), `targeted candidate did not make required cross-project change ${expected}`)
  const rootAppBlob = await git(repo, ['rev-parse', `${rootRevision.commit_oid}:src/app.js`])
  const nextAppBlob = await git(repo, ['rev-parse', `${candidateRevision.commit_oid}:src/app.js`])
  assert(rootAppBlob === nextAppBlob, 'unchanged shared application module lost Git blob identity')
  result.repositories.candidate = { commit: candidateRevision.commit_oid, tree: candidateRevision.tree_oid, parent: parentLine[1], changed_files: changed, unchanged_app_blob: rootAppBlob }
  result.gates.whole_candidate_ancestry = true
  result.gates.required_cross_project_repair = true
}

function initialPrompt() {
  return [
    `Create one managed Artifact V3 browser project for checked-in E2E ${testID}.`,
    'Call task exactly once with one managed Designer/author launch and wait for it. The author must use only the context-bound whole-project Artifact V3 workspace.',
    'Create these real files: swarm-artifact.json, index.html, styles/theme.css, src/app.js, and src/plans.js. The manifest must use schema_version swarm.artifact/v3, entrypoint index.html, and exactly three selector targets hero=#hero, pricing=#pricing, footer=#footer.',
    'The Hero must contain a button with data-plan=team. Pricing must render Solo $12, Team $29, and Scale $79 from src/plans.js through an ES module in src/app.js. Hero and Pricing must share --accent and --accent-contrast from styles/theme.css. The footer must visibly show revision marker ROOT.',
    'Build and preview the complete project. Click the Hero button and prove the Team card becomes selected. Repair compile, locator, interaction, contrast, clipping, overflow, or pixel problems in the same authoring turn.',
    'Finish only after one complete validated root commit is the visible Artifact head. Do not upload one monolithic replacement, publish independent target bytes, or use a legacy artifact writer.',
  ].join(' ')
}

function followupPrompt(artifactID, rootRevision) {
  return [
    `On exact Artifact V3 ${artifactID} at base revision ${rootRevision.revision_ref}, create exactly two complete alternative candidates in one targeted turn for target pricing.`,
    'Rename plan id team to studio, visible name Team to Studio, and price $29 to $39 in src/plans.js. Keep the Hero CTA working by changing its trigger to data-plan=studio. Change the shared --accent token to a visibly different color and preserve readable --accent-contrast.',
    'This request intentionally targets Pricing but requires changes to shared CSS and Hero markup. Use the complete project workspace and make every necessary cross-project repair; do not reject those changes as outside the target.',
    'Build and preview each complete candidate. Click the Hero CTA and prove the Studio $39 card becomes selected. Leave both complete candidates awaiting selection; do not move the Artifact head.',
  ].join(' ')
}

function highCardinalityPrompt() {
  return [
    `Create a second independent managed Artifact V3 project for ${testID} with exactly 96 stable selector targets panel-001 through panel-096.`,
    'Use the same whole-project authoring protocol and one normal manifest; do not split the project, change protocol shape, truncate targets, or publish target bytes independently.',
    'The entrypoint must visibly render all 96 numbered sections, include styles/theme.css with shared --accent styling, include src/app.js, and support clicking a section to mark data-selected=true. Build and preview the complete project before moving its root head.',
  ].join(' ')
}

function highCardinalityFollowupPrompt(artifactID, revision) {
  return [
    `Iterate exact Artifact V3 ${artifactID} from ${revision.revision_ref}, targeting only panel-065 as user intent and creating one complete candidate.`,
    'Change its visible text to Panel 65 · Featured, change the shared --accent token to a visibly different color, and update the shared click module so selecting panel-065 also sets data-featured=true.',
    'This targeted request intentionally requires shared stylesheet and code changes. Build and preview the complete 96-target project, prove panel-065 remains selectable and featured, preserve all 96 stable target IDs, and leave the whole candidate awaiting selection.',
  ].join(' ')
}

function candidateRound(artifact, rootRevision, expectedCandidates = 2) {
  const turns = artifact.turns || []
  const turn = [...turns].reverse().find((item) => item?.base_revision?.commit_oid === rootRevision.commit_oid || item?.base_commit_oid === rootRevision.commit_oid)
  assert(turn?.turn_id && turn?.status === 'awaiting_selection', 'targeted whole-project turn is not awaiting selection')
  assert(Array.isArray(turn.candidates) && turn.candidates.length === expectedCandidates, `targeted turn did not produce exactly ${expectedCandidates} complete candidate(s)`)
  for (const candidate of turn.candidates) {
    assert(candidate.status === 'ready' && candidate.revision?.commit_oid && candidate.revision?.tree_oid && candidate.revision?.revision_ref, 'turn has a non-ready or inexact candidate')
    assert(candidate.build?.status === 'succeeded' && candidate.validation?.status === 'valid', 'candidate lacks complete build and rendered validation evidence')
  }
  return turn
}

async function selectCandidate(sessionID, artifact, turn, candidate) {
  const response = await api('POST', artifactRoute(sessionID, `/${encodeURIComponent(artifact.id)}/turns/${encodeURIComponent(turn.turn_id)}/select`), {
    client_request_id: `${testID}:select`, candidate_id: candidate.candidate_id,
    expected_head_ref: currentRevision(artifact).revision_ref, expected_turn_revision: turn.revision,
  }, 'select complete Artifact V3 candidate')
  assert(response.body?.ok === true && response.body?.head?.commit_oid === candidate.revision.commit_oid, 'selection did not advance to the exact whole candidate')
}

async function openDesktopStudio(sessionID, artifactID) {
  await page.goto(`${desktopURL}${result.ids.desktop_path}`, { waitUntil: 'domcontentloaded', timeout: 60000 })
  const row = page.locator(`[data-artifact-v3-id="${artifactID}"]`).first()
  await row.waitFor({ state: 'visible', timeout: 60000 })
  await row.click()
  await page.locator('[data-testid="desktop-artifact-v3-studio"]').waitFor({ state: 'visible', timeout: 30000 })
  await page.locator('[data-artifact-v3-preview]').waitFor({ state: 'visible', timeout: 30000 })
  result.gates.desktop_complete_preview = true
}

async function proveDesktopPriorRevision(rootRevision) {
  const revisionButton = page.locator(`[data-artifact-v3-revision="${rootRevision.commit_oid}"]`).first()
  await revisionButton.waitFor({ state: 'visible', timeout: 30000 })
  await revisionButton.click()
  const preview = page.locator(`[data-artifact-v3-preview-revision="${rootRevision.commit_oid}"]`).first()
  await preview.waitFor({ state: 'visible', timeout: 30000 })
  const file = path.join(evidenceDir, 'desktop-prior-revision.png')
  const bytes = await page.screenshot({ path: file, fullPage: false })
  assert(bytes.length > 20000, 'Desktop prior-revision screenshot is not substantive')
  result.screenshots.push({ label: 'desktop-prior-revision', file, sha256: sha256(bytes), bytes: bytes.length, viewport: page.viewportSize() })
  result.gates.desktop_prior_revision = true
}

async function runLive(highFixture) {
  assert(apiURL && /^https?:\/\//.test(apiURL), '--api-url is required for the live red journey')
  assert(desktopURL && /^https?:\/\//.test(desktopURL), '--desktop-url is invalid')
  assert(Number.isFinite(timeoutMs) && timeoutMs >= 300000 && timeoutMs <= 600000, '--timeout-ms must be between 300000 and 600000')
  await auth()
  const selected = await topology()
  const session = await createSession(selected)
  const routeProbe = await api('GET', artifactRoute(session.sessionID), undefined, 'probe Artifact V3 route', true)
  assert(routeProbe.ok && routeProbe.body?.ok === true && Array.isArray(routeProbe.body?.artifacts), `RED_CONTRACT_MISSING: native Artifact V3 catalog is unavailable (HTTP ${routeProbe.status})`)
  const playwright = loadPlaywright()
  browser = await playwright.chromium.launch({ headless, executablePath: browserExecutable || undefined })
  context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  page = await context.newPage()
  await installRealtimeRecorder(page)
  await page.goto(`${desktopURL}/${slug(session.workspaceName)}`, { waitUntil: 'domcontentloaded', timeout: 60000 })
  await page.locator('body').waitFor({ state: 'visible' })

  await postTurn(session.sessionID, 'initial', initialPrompt())
  const items = await catalog(session.sessionID)
  const artifact = items.find((item) => text(item?.intent_reference).includes(testID) || item?.part_count === 3)
  assert(artifact?.id, `initial turn did not create one discoverable Artifact V3; count=${items.length}`)
  const rootDetail = await detail(session.sessionID, artifact.id)
  const rootRevision = currentRevision(rootDetail)
  assert(rootDetail.parts?.length === 3 && rootDetail.parts.every((part) => ['hero', 'pricing', 'footer'].includes(part.id)), 'root Artifact target index is incomplete')
  assert(rootDetail.head?.build?.status === 'succeeded' && rootDetail.head?.validation?.status === 'valid', 'root head lacks whole-project build/render evidence')
  result.ids.artifact_id = artifact.id
  result.revisions.root = rootRevision
  const rootShot = await screenshotPreview(session.sessionID, artifact.id, rootRevision, 'root-complete', verifyRootPreview)
  const repo = await inspectRootRepository(artifact.id, rootRevision)
  await openDesktopStudio(session.sessionID, artifact.id)

  await postTurn(session.sessionID, 'targeted-followup', followupPrompt(artifact.id, rootRevision), [{ action: 'iterate', artifact_v3_ref: rootRevision.revision_ref, artifact_id: artifact.id, target_part_ids: ['pricing'] }])
  const withCandidates = await detail(session.sessionID, artifact.id)
  assert(currentRevision(withCandidates).commit_oid === rootRevision.commit_oid, 'candidate generation moved the head before selection')
  const turn = candidateRound(withCandidates, rootRevision)
  const chosen = turn.candidates[0]
  result.ids.turn_id = turn.turn_id
  result.ids.candidate_id = chosen.candidate_id
  result.revisions.candidate = chosen.revision
  const candidateShot = await screenshotPreview(session.sessionID, artifact.id, chosen.revision, 'candidate-complete', verifyCandidatePreview)
  assert(rootShot.sha256 !== candidateShot.sha256, 'root and candidate pixel captures are identical')
  await inspectCandidateRepository(repo, rootRevision, chosen.revision)
  await selectCandidate(session.sessionID, withCandidates, turn, chosen)
  const selectedDetail = await detail(session.sessionID, artifact.id)
  assert(currentRevision(selectedDetail).commit_oid === chosen.revision.commit_oid, 'selected Artifact head is not the exact candidate commit')
  assert(await git(repo, ['rev-parse', 'refs/heads/artifact']) === chosen.revision.commit_oid, 'native Git head did not follow the selected projection')
  const oldShot = await screenshotPreview(session.sessionID, artifact.id, rootRevision, 'prior-root-after-selection', verifyRootPreview)
  assert(oldShot.sha256 === rootShot.sha256, 'exact prior Revision no longer renders the same pixels after selection')
  await openDesktopStudio(session.sessionID, artifact.id)
  await proveDesktopPriorRevision(rootRevision)
  result.gates.selection_is_whole_commit = true
  result.gates.prior_revision_immutable = true

  await postTurn(session.sessionID, 'high-cardinality', highCardinalityPrompt())
  const afterHigh = await catalog(session.sessionID)
  const highItem = afterHigh.find((item) => item.id !== artifact.id && item.part_count === highFixture.manifest.parts.length)
  assert(highItem?.id, `96-target Artifact is absent or truncated; catalog=${afterHigh.map((item) => `${item.id}:${item.part_count}`).join(',')}`)
  const highDetail = await detail(session.sessionID, highItem.id)
  assert(highDetail.parts?.length === 96 && highDetail.parts[64]?.id === 'panel-065' && highDetail.parts[95]?.id === 'panel-096', '96-target detail changed shape or truncated targets')
  const highRevision = currentRevision(highDetail)
  const highRepo = repositoryPath(highItem.id)
  await git(highRepo, ['fsck', '--full', '--strict'])
  const highManifest = JSON.parse(await git(highRepo, ['show', `${highRevision.commit_oid}:swarm-artifact.json`]))
  assert(highManifest.parts.length === 96 && highManifest.schema_version === 'swarm.artifact/v3', 'native 96-target commit is invalid or truncated')
  result.ids.high_cardinality_artifact_id = highItem.id
  result.high_cardinality.actual_count = highDetail.parts.length
  result.high_cardinality.commit_oid = highRevision.commit_oid
  const highRootShot = await screenshotPreview(session.sessionID, highItem.id, highRevision, 'high-cardinality-root', async (previewPage) => {
    const panel = previewPage.locator('#panel-065')
    await panel.waitFor({ state: 'visible' })
    await panel.click()
    assert(await panel.getAttribute('data-selected') === 'true', '96-target root interaction failed')
  })
  await postTurn(session.sessionID, 'high-cardinality-followup', highCardinalityFollowupPrompt(highItem.id, highRevision), [{ action: 'iterate', artifact_v3_ref: highRevision.revision_ref, artifact_id: highItem.id, target_part_ids: ['panel-065'] }])
  const highCandidateDetail = await detail(session.sessionID, highItem.id)
  assert(currentRevision(highCandidateDetail).commit_oid === highRevision.commit_oid, '96-target candidate moved the head before selection')
  assert(highCandidateDetail.parts?.length === 96, '96-target candidate truncated the target index')
  const highTurn = candidateRound(highCandidateDetail, highRevision, 1)
  const highCandidate = highTurn.candidates[0]
  const highCandidateShot = await screenshotPreview(session.sessionID, highItem.id, highCandidate.revision, 'high-cardinality-candidate', async (previewPage) => {
    const panel = previewPage.locator('#panel-065')
    await panel.waitFor({ state: 'visible' })
    assert(text(await panel.textContent()).includes('Featured'), '96-target candidate lost its requested visible change')
    await panel.click()
    assert(await panel.getAttribute('data-selected') === 'true' && await panel.getAttribute('data-featured') === 'true', '96-target candidate shared interaction repair failed')
  })
  assert(highRootShot.sha256 !== highCandidateShot.sha256, '96-target root and candidate captures are identical')
  await selectCandidate(session.sessionID, highCandidateDetail, highTurn, highCandidate)
  const highSelected = await detail(session.sessionID, highItem.id)
  assert(currentRevision(highSelected).commit_oid === highCandidate.revision.commit_oid && highSelected.parts?.length === 96, '96-target selected history is incomplete or truncated')
  const highParents = (await git(highRepo, ['rev-list', '--parents', '-n', '1', highCandidate.revision.commit_oid])).split(/\s+/)
  assert(highParents.length === 2 && highParents[1] === highRevision.commit_oid, '96-target follow-up is not a whole-project child commit')
  await screenshotPreview(session.sessionID, highItem.id, highRevision, 'high-cardinality-prior-root', async (previewPage) => {
    const panel = previewPage.locator('#panel-065')
    await panel.waitFor({ state: 'visible' })
    assert(!text(await panel.textContent()).includes('Featured'), '96-target prior root was mutated by follow-up selection')
  })
  result.high_cardinality.child_commit_oid = highCandidate.revision.commit_oid
  result.high_cardinality.history_count = (highSelected.revisions || []).length
  assert(result.high_cardinality.history_count >= 2, '96-target whole-project history is truncated')
  result.gates.high_cardinality_real_product = true
  result.gates.high_cardinality_iteration_history = true

  const snapshot = await hydrate(session.sessionID)
  const events = snapshot.events_by_session?.[session.sessionID] || []
  const ownedEvents = events.filter((event) => text(event?.event_type).startsWith('artifact.v3.'))
  assert(ownedEvents.length >= 6, 'durable replay lacks substantive Artifact V3 events')
  assert(!forbiddenLegacyWrite(ownedEvents), 'journey replay includes a legacy artifact write')
  const records = await page.evaluate(() => window.__artifactV3Records || [])
  const liveEvents = records.filter((record) => record.kind === 'message' && record.session_id === session.sessionID && record.event_type.startsWith('artifact.v3.'))
  assert(liveEvents.length >= 1 && liveEvents.some((record) => record.endpoint_cursor_present), 'Desktop observed no cursor-bearing Artifact V3 realtime event')
  result.realtime = { recorded: records.length, artifact_events: liveEvents.length, replay_events: ownedEvents.length }
  result.gates.http_native_v3 = true
  result.gates.realtime_native_v3 = true
  result.gates.no_legacy_writes = true
  result.result = 'PASS'
}

await fs.mkdir(evidenceDir, { recursive: true })
try {
  await loadFixture()
  const highFixture = await generateHighCardinalityFixture()
  if (preflight && !apiURL) {
    result.result = 'RED_NOT_RUN'
    result.failures.push('RED_NOT_RUN: static fixtures are valid, but live task/runtime, HTTP, realtime, Desktop, Git, and pixel gates were not run')
  } else if (preflight) {
    await auth()
    const selected = await topology()
    const session = await createSession(selected)
    const probe = await api('GET', artifactRoute(session.sessionID), undefined, 'probe Artifact V3 route', true)
    assert(probe.ok && probe.body?.ok === true && Array.isArray(probe.body?.artifacts), `RED_CONTRACT_MISSING: native Artifact V3 catalog is unavailable (HTTP ${probe.status})`)
    result.result = 'PREFLIGHT_READY'
  } else {
    await runLive(highFixture)
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
  result.completed_at = new Date().toISOString()
  result.evidence_dir = evidenceDir
  await fs.writeFile(path.join(evidenceDir, 'summary.json'), `${JSON.stringify(result, null, 2)}\n`)
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (!['PASS', 'PREFLIGHT_READY'].includes(result.result)) process.exitCode = 2
}
