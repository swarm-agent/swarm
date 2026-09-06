#!/usr/bin/env node
// Attach-only proof. Uses Desktop's canonical session bootstrap; never copies credentials,
// configures providers, deploys, or selects candidates. CDP must be a dedicated test browser.
// Parent validation: node --test tests/scripts/artifact_v3_edit_repair_test.mjs
import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const check = (value, code) => { if (!value) throw new Error(code) }
const hash = (value) => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const decode = (value) => typeof value === 'string' ? JSON.parse(value) : value
const handleKeys = ['session_id', 'artifact_id', 'turn_id', 'candidate_id', 'grant_id']
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
export const brokenHTML = '<!doctype html><html><head><meta charset="utf-8"><title>Fictional Orchard</title></head><body><main id="hero" style="display:none">Orchard launch</main><section id="pricing">Fictional plan: 12 credits</section><footer id="footer">Fictional demo only</footer></body></html>'
export const repairedHTML = brokenHTML.replace('display:none', 'display:block')
export const editedHTML = repairedHTML.replace('Orchard launch', 'Orchard spring launch')

export function endpoint(value) {
  let url
  try { url = new URL(value) } catch { throw new Error('explicit_endpoint_required') }
  check(['http:', 'https:'].includes(url.protocol) && !url.username && !url.password && !url.search && !url.hash, 'unsafe_endpoint')
  check(['localhost', '127.0.0.1', '[::1]'].includes(url.hostname), 'loopback_endpoint_required')
  return url
}
export function parseOptions(argv) {
  const allowed = new Set(['desktop-url', 'cdp-url', 'browser-mode', 'workspace-path', 'workspace-name', 'binding-id', 'swarm-id', 'output', 'stage', 'stage-ms', 'stall-ms'])
  const values = {}
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i]?.replace(/^--/, '')
    check(argv[i]?.startsWith('--') && allowed.has(key) && !(key in values) && argv[i + 1] && !argv[i + 1].startsWith('--'), 'invalid_option')
    values[key] = argv[i + 1]
  }
  const stage = values.stage || 'live'
  check(['live', 'no-progress-fixture'].includes(stage), 'invalid_stage')
  const stageMs = Number(values['stage-ms'] || 600000)
  const stallMs = Number(values['stall-ms'] || 90000)
  check(Number.isInteger(stageMs) && stageMs >= 1000 && stageMs <= 600000 && Number.isInteger(stallMs) && stallMs >= 1000 && stallMs <= stageMs, 'invalid_budget')
  if (stage === 'live') {
    endpoint(values['desktop-url'])
    check(['headless', 'dedicated-cdp'].includes(values['browser-mode']), 'explicit_browser_mode_required')
    if (values['browser-mode'] === 'dedicated-cdp') endpoint(values['cdp-url'])
    else check(!values['cdp-url'], 'headless_cdp_conflict')
    for (const key of ['workspace-path', 'workspace-name', 'binding-id', 'swarm-id', 'output']) check(values[key]?.trim(), 'missing_' + key)
  }
  return { ...values, stage, stageMs, stallMs }
}

// An explicit write allowlist prevents setup/auth/settings/permission side effects.
export function allowedRequest(method, route, sessionID = '') {
  if (method === 'GET') return route === '/v1/auth/desktop/session' || (sessionID && route.startsWith(`/v3/sessions/${encodeURIComponent(sessionID)}/artifacts-v3`))
  return method === 'POST' && (route === '/v3/sessions' || route === '/v3/sync/hydrate' || (sessionID && route === `/v3/sessions/${encodeURIComponent(sessionID)}/messages`))
}

export async function waitStage({ sample, done, progress = hash, stageMs, stallMs, now = Date.now, pause = sleep, heartbeat = () => {} }) {
  const start = now(); let changed = start; let prior; let beat = start
  while (now() - start < stageMs) {
    const value = await sample()
    const fingerprint = progress(value)
    if (fingerprint !== prior) { prior = fingerprint; changed = now() }
    if (now() >= beat) { heartbeat(); beat = now() + 10000 }
    if (done(value)) return value
    if (now() - changed >= stallMs) throw new Error('no_progress_work_retained')
    await pause(500)
  }
  throw new Error('stage_deadline_work_retained')
}

export function toolRecords(messages, runID) {
  const seen = new Set(); const records = []
  for (const message of messages) {
    if (message.role !== 'tool') continue
    let envelope
    try { envelope = decode(message.content) } catch { throw new Error('tool_evidence_not_json') }
    const evidenceRun = message.run_id || envelope.run_id
    if (!evidenceRun && (envelope.tool_name || envelope.tool) === 'manage_artifact') throw new Error('tool_run_scope_unavailable')
    if (evidenceRun !== runID) continue
    check(!message.run_id || !envelope.run_id || message.run_id === envelope.run_id, 'conflicting_tool_run_identity')
    if ((envelope.tool_name || envelope.tool) !== 'manage_artifact') continue
    check(envelope.call_id && !seen.has(envelope.call_id), 'duplicate_tool_identity')
    seen.add(envelope.call_id)
    check(envelope.arguments && envelope.output && !envelope.error, 'complete_tool_evidence_required')
    const args = decode(envelope.arguments); const output = decode(envelope.output)
    check(output.artifact_v3, 'native_tool_output_required')
    records.push({ args, body: output.artifact_v3 })
  }
  return records
}
const diagnostics = (gate) => [...(gate?.Diagnostics || []), ...(gate?.Build?.Diagnostics || []), ...(gate?.Preview?.Diagnostics || [])]
const sameHandle = (a, b) => handleKeys.every((key) => typeof a?.[key] === 'string' && a[key] && a[key] === b?.[key])

// Fail closed on missing privacy-redacted output; display summaries are not proof.
export function verifyRepair(records, { sessionID, initial = true, artifactID, sourceRef } = {}) {
  const starts = records.filter(({ args }) => ['create', 'begin_v3', 'revise_v3'].includes(args.action))
  check(starts.length === 1 && starts[0].args.action === (initial ? 'create' : 'begin_v3'), 'single_native_start_required')
  const start = starts[0]; const handle = start.body.draft_handle
  check(sameHandle(handle, handle) && handle.session_id === sessionID && (!artifactID || handle.artifact_id === artifactID), 'wrong_draft_identity')
  if (initial) {
    check(start.args.content === brokenHTML && start.body.status === 'fixing', 'meaningful_failed_create_required')
    check(diagnostics(start.body.gate).some((d) => d.Code && d.Message), 'validation_diagnostic_required')
  } else {
    assert.deepEqual(start.args.artifact_v3_reference, sourceRef, 'wrong_source_revision')
    assert.deepEqual(start.args.target_part_ids, ['hero'], 'wrong_part_target')
  }
  const operations = records.filter(({ args }) => args.action === 'author_v3')
  check(operations.every(({ args, body }) => sameHandle(args.draft_handle, handle) && sameHandle(body.draft_handle, handle)), 'draft_handle_changed')
  const actions = operations.map(({ args }) => args.operation?.action)
  assert.deepEqual(actions, ['read_file', 'read_file', 'edit_file', 'read_file', 'read_file', 'build_preview', 'finish_turn'], 'incomplete_incremental_lifecycle')
  const [read, manifest, edit, reread, remanifest, build, finish] = operations
  for (const op of [read, edit, reread]) check(op.args.operation.path === 'index.html', 'wrong_source_path')
  for (const op of [manifest, remanifest]) check(op.args.operation.path === 'swarm-artifact.json', 'missing_manifest_read')
  const before = initial ? brokenHTML : repairedHTML; const after = initial ? repairedHTML : editedHTML
  check(read.body.result?.Content === before && reread.body.result?.Content === after, 'unrelated_source_bytes_changed')
  assert.deepEqual(edit.args.operation, { action: 'edit_file', path: 'index.html', old_string: initial ? 'display:none' : 'Orchard launch', new_string: initial ? 'display:block' : 'Orchard spring launch' }, 'targeted_patch_required')
  check(manifest.body.result?.Content === remanifest.body.result?.Content, 'manifest_bytes_changed')
  const parts = JSON.parse(manifest.body.result.Content).parts
  check(Array.isArray(parts) && parts.length === 3 && new Set(parts.map((p) => p.id)).size === 3, 'unique_stable_parts_required')
  assert.deepEqual(parts.map((p) => p.id).sort(), ['footer', 'hero', 'pricing'])
  const gate = build.body.result
  check(gate?.Ready === true && diagnostics(gate).length === 0, 'current_diagnostics_not_clear')
  check(finish.body.status === (initial ? 'ready' : 'awaiting_selection'), 'finish_not_ready')
  const ref = finish.body.media_inspect_reference; const commit = finish.body.result?.Revision?.CommitOID
  check(ref?.session_id === sessionID && ref?.artifact_id === handle.artifact_id && typeof ref?.revision_ref === 'string' && /^[a-f0-9]{40}$/.test(commit || ''), 'exact_native_revision_required')
  check(!sourceRef || ref.revision_ref !== sourceRef.revision_ref, 'revision_did_not_advance')
  return { kind: 'artifact_v3', ...ref, commit_oid: commit, draft_handle: handle, source_sha256: hash(after), manifest_sha256: hash(manifest.body.result.Content), part_ids: parts.map((p) => p.id) }
}

export function verifyPartAttachment(selections, source) {
  check(Array.isArray(selections) && selections.length === 1, 'single_part_attachment_required')
  const selection = selections[0]
  for (const key of ['session_id', 'artifact_id', 'revision_ref']) check(selection[key] === source[key], 'composer_lost_exact_source')
  assert.deepEqual(selection.target_part_ids, ['hero'], 'composer_lost_part_target')
  check(selection.action === 'use', 'composer_wrong_attachment_action')
}

export function verifySidebar(observations, artifactID) {
  for (const status of ['Creating', 'Fixing', 'Ready']) check(observations.some((o) => o.id === artifactID && o.status === status && o.named && o.heading), 'missing_live_sidebar_' + status)
  check(observations.every((o) => o.id === artifactID), 'duplicate_sidebar_identity')
}

export async function noProgressFixture() {
  const retained = { draft: 'fixture-draft', source: brokenHTML }; const original = hash(retained)
  let clock = 0; let beats = 0
  await assert.rejects(waitStage({ sample: async () => retained, done: () => false, stageMs: 10000, stallMs: 2000, now: () => clock, pause: async (ms) => { clock += ms }, heartbeat: () => { beats++ } }), /no_progress_work_retained/)
  check(hash(retained) === original && clock === 2000 && beats > 0, 'fixture_retention_failed')
  return { kind: 'hermetic_no_progress_fixture', elapsed_ms: clock, retained: true }
}

const instructions = `Use only primary manage_artifact, no delegation or workspace writes. Do not alter provider settings, permissions, or other artifacts. Do not recreate after a failed create. For each retained draft use author_v3 with its EXACT draft_handle, in this order: read_file index.html; read_file swarm-artifact.json; one edit_file; read_file index.html; read_file swarm-artifact.json; build_preview; finish_turn. Preserve every unrelated byte and all Part IDs. Do not select follow-up candidates. If anything fails, report honestly and retain work.`

// Isolated ephemeral context only; no persistent profile, cookies or storage-state import.
export async function openProofBrowser(chromium, options) {
  if (options['browser-mode'] === 'headless') {
    const browser = await chromium.launch({ headless: true, chromiumSandbox: true, timeout: 15000, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
    try {
      return { browser, context: await browser.newContext({ viewport: { width: 1440, height: 1000 } }) }
    } catch (error) { await browser.close(); throw error }
  }
  check(options['browser-mode'] === 'dedicated-cdp', 'explicit_browser_mode_required')
  const browser = await chromium.connectOverCDP(options['cdp-url'], { timeout: 15000 })
  try {
    const origin = endpoint(options['desktop-url']).origin
    const context = browser.contexts().find((ctx) => ctx.pages().some((page) => {
      try { return new URL(page.url()).origin === origin } catch { return false }
    }))
    check(context, 'authenticated_browser_origin_required')
    return { browser, context }
  } catch (error) { await browser.close(); throw error }
}

export async function runLive(options) {
  const require = createRequire(new URL('../../web/package.json', import.meta.url))
  const { chromium } = require('playwright')
  const { browser, context } = await openProofBrowser(chromium, options)
  let page; let sessionID = ''; let outputDir
  const ledger = { schema: 'artifact-v3-edit-repair/v1', status: 'incomplete', stages: [], identities: [] }
  let heartbeat
  try {
    const url = endpoint(options['desktop-url'])
    outputDir = await fs.mkdtemp(path.join(path.resolve(options.output), 'artifact-edit-'))
    await fs.chmod(outputDir, 0o700)
    heartbeat = setInterval(() => process.stdout.write('artifact-edit proof active; bounded stage in progress\n'), 10000)
    page = await context.newPage(); page.setDefaultTimeout(10000); page.setDefaultNavigationTimeout(15000)
    await page.goto(url.href, { waitUntil: 'domcontentloaded' })
    const api = async (method, route, body) => {
      check(allowedRequest(method, route, sessionID), 'request_outside_attach_contract')
      return page.evaluate(async ({ method, route, body }) => {
        const response = await fetch(route, { method, credentials: 'include', signal: AbortSignal.timeout(10000), headers: { 'Content-Type': 'application/json' }, ...(body ? { body: JSON.stringify(body) } : {}) })
        if (!response.ok) throw new Error('api_http_' + response.status)
        const text = await response.text()
        if (text.length > 2000000) throw new Error('evidence_response_too_large')
        return JSON.parse(text)
      }, { method, route, body })
    }
    const identity = await api('GET', '/v1/auth/desktop/session')
    check((identity.user_id || identity.userID) && (identity.account_scope_id || identity.accountScopeID), 'desktop_identity_required')
    const created = await api('POST', '/v3/sessions', { client_request_id: randomUUID(), title: 'Fictional Orchard repair proof', workspace_path: options['workspace-path'], workspace_name: options['workspace-name'], workspace_binding_id: options['binding-id'], swarm_id: options['swarm-id'], target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: 'swarm' })
    sessionID = created.session?.id || created.session_id
    check(typeof sessionID === 'string' && sessionID, 'missing_session_identity'); ledger.session_id = sessionID
    const slug = options['workspace-name'].toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
    await page.goto(new URL(`/${slug}/${encodeURIComponent(sessionID)}`, url.origin).href, { waitUntil: 'domcontentloaded' })
    // Observe actual DOM transitions, not polling API statuses; no reload until ready.
    await page.evaluate(() => {
      window.__repairUI = []
      const observe = () => {
        for (const sidebar of document.querySelectorAll('[data-testid="desktop-session-artifact-v3-sidebar"]')) {
        for (const item of sidebar.querySelectorAll('[data-artifact-v3-sidebar-id]')) {
          const text = item.textContent || ''; const status = ['Creating', 'Fixing', 'Ready', 'Error'].find((s) => text.includes(s))
          if (!item.getClientRects().length || getComputedStyle(item).visibility === 'hidden') continue
          const row = { id: item.getAttribute('data-artifact-v3-sidebar-id'), status, named: text.includes('Fictional Orchard'), heading: sidebar.querySelector('h2')?.textContent === 'Artifacts' }
          if (status && !window.__repairUI.some((v) => v.id === row.id && v.status === row.status)) window.__repairUI.push(row)
        }
        }
      }
      new MutationObserver(observe).observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true }); observe()
    })
    const hydrate = () => api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [sessionID], history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 0, manifest_policy: 'manifest' }, resources: { messages: true, run_intents: true, current_run_state: true }, include_active: true })
    const waitRun = async (runID) => {
      check(runID, 'missing_run_identity')
      const snapshot = await waitStage({ sample: hydrate, stageMs: options.stageMs, stallMs: options.stallMs,
        progress: (s) => hash([s.messages_by_session?.[sessionID], s.run_intents_by_session?.[sessionID]?.map((r) => [r.run_id, r.status])]),
        done: (s) => {
          const intent = s.run_intents_by_session?.[sessionID]?.find((r) => r.run_id === runID)
          if (['failed', 'cancelled', 'blocked', 'expired', 'interrupted'].includes(intent?.status)) throw new Error('provider_run_not_completed')
          return intent?.status === 'completed'
        } })
      return toolRecords(snapshot.messages_by_session?.[sessionID] || [], runID)
    }
    const prompt = `${instructions}\nCreate exactly one text/html artifact with collection_name Fictional Orchard, filename index.html, using these EXACT bytes. This intentionally hidden Hero must encounter the real validation gate before repair. Repair ONLY display:none to display:block. Do not pre-fix.\n${brokenHTML}`
    const sent = await api('POST', `/v3/sessions/${sessionID}/messages`, { client_request_id: randomUUID(), role: 'user', content: prompt })
    const first = verifyRepair(await waitRun(sent.run_intent?.run_id || sent.run_id), { sessionID })
    ledger.identities.push(first); ledger.stages.push('primary-incremental-repair')
    ledger.sidebar_observations = await page.evaluate(() => window.__repairUI)
    verifySidebar(ledger.sidebar_observations, first.artifact_id)
    ledger.stages.push('live-sidebar-creating-fixing-ready')
    const route = `/v3/sessions/${sessionID}/artifacts-v3`
    const catalog = await api('GET', route)
    check(catalog.artifacts?.length === 1 && catalog.artifacts[0].id === first.artifact_id, 'duplicate_artifact_creation')
    const open = async () => {
      await page.locator(`[data-artifact-v3-sidebar-id=${JSON.stringify(first.artifact_id)}]:visible`).click()
      const studio = page.locator('[data-artifact-v3-studio]'); await studio.waitFor({ state: 'visible' })
      await studio.locator('[data-artifact-v3-complete-preview]').waitFor({ state: 'visible' })
      check(await studio.locator('[data-artifact-v3-part]').count() === 3, 'ui_parts_missing')
      return studio
    }
    const studio = await open()
    const preview = studio.locator('[data-artifact-v3-complete-preview]').contentFrame()
    await preview.getByText('Orchard launch', { exact: true }).waitFor({ state: 'visible' })
    await preview.getByText('Fictional plan: 12 credits', { exact: true }).waitFor({ state: 'visible' })
    await preview.getByText('Fictional demo only', { exact: true }).waitFor({ state: 'visible' })
    await page.screenshot({ path: path.join(outputDir, 'ready-studio.png') })
    await studio.getByText('Previous repair errors', { exact: true }).click()
    check(await studio.locator('[data-artifact-repair-history] p:visible').count() > 0, 'repair_history_missing')
    check(await studio.locator('[data-artifact-v3-diagnostics]').count() === 0, 'current_errors_not_cleared')
    await page.screenshot({ path: path.join(outputDir, 'repair-history.png') })
    await studio.locator('[data-artifact-v3-part="hero"]').click()
    await studio.locator('[data-artifact-v3-iterate]').click()
    const composer = page.getByLabel('Continue Desktop V3 conversation')
    await page.getByText(/pending update/i).waitFor({ state: 'visible' })
    await composer.fill(`${instructions}\nUse begin_v3 on the attached exact reference targeting hero. Change ONLY Orchard launch to Orchard spring launch.`)
    const responsePromise = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `/v3/sessions/${sessionID}/messages`)
    await page.getByRole('button', { name: 'Send message', exact: true }).click()
    const response = await responsePromise; check(response.ok(), 'composer_submission_failed')
    const submitted = response.request().postDataJSON()
    verifyPartAttachment(submitted.artifact_selections, first)
    const secondSent = await response.json()
    const sourceRef = { session_id: sessionID, artifact_id: first.artifact_id, revision_ref: first.revision_ref }
    const second = verifyRepair(await waitRun(secondSent.run_intent?.run_id || secondSent.run_id), { sessionID, initial: false, artifactID: first.artifact_id, sourceRef })
    check(first.manifest_sha256 === second.manifest_sha256, 'parts_changed_between_turns')
    ledger.identities.push(second); ledger.stages.push('studio-composer-second-edit')
    await page.reload({ waitUntil: 'domcontentloaded' }); const reopened = await open()
    const detail = await api('GET', `${route}/${encodeURIComponent(first.artifact_id)}`)
    const head = detail.artifact?.head || detail.artifact?.current_revision
    check(head?.revision_ref === first.revision_ref && head?.commit_oid === first.commit_oid, 'unselected_edit_moved_source')
    check((await api('GET', route)).artifacts?.length === 1, 'reopen_duplicate_identity')
    check(await reopened.locator('[data-artifact-v3-candidate]').count() === 1, 'reopen_lost_candidate')
    await reopened.locator('[data-artifact-v3-complete-preview]').contentFrame().getByText('Orchard spring launch', { exact: true }).waitFor({ state: 'visible' })
    await page.screenshot({ path: path.join(outputDir, 'edited-candidate.png') })
    // Studio intentionally opens pending changes. Viewing HEAD is not selecting it.
    await reopened.locator(`[data-artifact-v3-revision=${JSON.stringify(first.commit_oid)}]`).click()
    await reopened.locator(`[data-artifact-v3-preview-revision=${JSON.stringify(first.commit_oid)}]`).waitFor({ state: 'visible' })
    const reopenedPreview = reopened.locator('[data-artifact-v3-complete-preview]').contentFrame()
    await reopenedPreview.getByText('Orchard launch', { exact: true }).waitFor({ state: 'visible' })
    await page.screenshot({ path: path.join(outputDir, 'reopened-original.png') })
    ledger.stages.push('refresh-reopen-preserves-selected-source')
    // Explicit synthetic UI failure stage; never represented as a live provider failure.
    // Only this new page is intercepted; durable state and other tabs are untouched.
    await page.route('**' + route, async (intercept) => {
      const request = intercept.request()
      if (request.method() !== 'GET') return intercept.continue()
      const body = structuredClone(catalog)
      body.artifacts[0].status = 'error'
      body.artifacts[0].current_draft = { ...body.artifacts[0].current_draft, status: 'error' }
      await intercept.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
    })
    await page.reload({ waitUntil: 'domcontentloaded' })
    const errorItem = page.locator(`[data-artifact-v3-sidebar-id=${JSON.stringify(first.artifact_id)}]:visible`)
    await errorItem.getByText('Error', { exact: true }).waitFor({ state: 'visible' })
    check((await errorItem.innerText()).includes('Fictional Orchard'), 'error_lost_meaningful_name')
    ledger.stages.push('synthetic-error-sidebar-ui')
    ledger.no_progress = await noProgressFixture(); ledger.status = 'passed'
  } catch (error) {
    // Never persist raw provider/browser errors, transcripts, credentials or source bytes.
    const message = String(error.message)
    ledger.status = 'failed'; ledger.failure = { code: /^[a-z][a-z0-9_]{1,100}$/.test(message) ? message : 'proof_requirement_failed', diagnostic_sha256: hash(message) }
    throw new Error('proof_failed_work_retained; inspect private ledger and fictional session')
  } finally {
    clearInterval(heartbeat)
    try {
      if (outputDir) await fs.writeFile(path.join(outputDir, 'ledger.json'), JSON.stringify(ledger, null, 2), { mode: 0o600, flag: 'wx' })
    } finally {
      try { if (page) await page.close() } finally { await browser.close() }
    } // CDP disconnects; headless mode closes only our owned ephemeral browser.
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseOptions(process.argv.slice(2))
    if (options.stage === 'no-progress-fixture') { await noProgressFixture(); console.log('no-progress fixture passed (not live proof)') }
    else { await runLive(options); console.log('proof complete; private ledger written') }
  } catch { console.error('artifact-edit proof failed; no setup or recovery mutation attempted'); process.exitCode = 1 }
}
