// Requirement: an attach-only proof must reject missing/forged lifecycle evidence,
// retain work on stalls, and never configure/authenticate/deploy the target.
// Threat: a green API status or repeated create hides discarded drafts and UI failure.
// Authority: runtime_manage_artifact_v3_draft.go, ArtifactV3AuthorGate/Finish,
// DesktopV3ArtifactV3Sidebar/Studio and run.tool-history.v2. Pure predicates are
// the narrow hermetic layer; these tests do NOT establish browser/provider health.
import assert from 'node:assert/strict'
import test from 'node:test'
import fs from 'node:fs/promises'
import { allowedRequest, brokenHTML, repairedHTML, editedHTML, endpoint, noProgressFixture, openProofBrowser, parseOptions, toolRecords, verifyRepair, verifySidebar, waitStage } from '../../scripts/runners/artifact-v3-edit-repair.mjs'

const handle = { session_id: 'fixture-session', artifact_id: 'fixture-artifact', turn_id: 'fixture-turn', candidate_id: 'fixture-candidate', grant_id: 'fixture-grant' }
const manifest = JSON.stringify({ parts: ['hero', 'pricing', 'footer'].map((id) => ({ id, label: id, locator: { kind: 'selector', path: 'index.html', value: '#' + id } })) })
const firstRef = { session_id: handle.session_id, artifact_id: handle.artifact_id, revision_ref: 'revision-fixture-first' }
function fixture(initial = true) {
  const h = { ...handle, ...(initial ? {} : { turn_id: 'fixture-next-turn', candidate_id: 'fixture-next-candidate', grant_id: 'fixture-next-grant' }) }
  const start = { args: initial ? { action: 'create', content: brokenHTML } : { action: 'begin_v3', artifact_v3_reference: { ...firstRef }, target_part_ids: ['hero'] }, body: { status: initial ? 'fixing' : 'editing', draft_handle: h, gate: { Ready: false, Diagnostics: [{ Code: 'hidden_part', Message: 'Hero is hidden' }] } } }
  const op = (operation, result, extra = {}) => ({ args: { action: 'author_v3', draft_handle: { ...h }, operation }, body: { draft_handle: { ...h }, result, ...extra } })
  return [start,
    op({ action: 'read_file', path: 'index.html' }, { Content: initial ? brokenHTML : repairedHTML }),
    op({ action: 'read_file', path: 'swarm-artifact.json' }, { Content: manifest }),
    op({ action: 'edit_file', path: 'index.html', old_string: initial ? 'display:none' : 'Orchard launch', new_string: initial ? 'display:block' : 'Orchard spring launch' }, {}),
    op({ action: 'read_file', path: 'index.html' }, { Content: initial ? repairedHTML : editedHTML }),
    op({ action: 'read_file', path: 'swarm-artifact.json' }, { Content: manifest }),
    op({ action: 'build_preview' }, { Ready: true, Diagnostics: [] }),
    op({ action: 'finish_turn' }, { Revision: { CommitOID: (initial ? 'a' : 'b').repeat(40) } }, { status: initial ? 'ready' : 'awaiting_selection', media_inspect_reference: initial ? firstRef : { ...firstRef, revision_ref: 'revision-fixture-second' } }),
  ]
}
const verify = (records) => verifyRepair(records, { sessionID: handle.session_id })

test('explicit endpoints and strict options reject unsafe, duplicate and setup options', () => {
  assert.throws(() => parseOptions([]), /explicit_endpoint/)
  for (const value of ['', 'http://example.invalid', 'http://user:secret@localhost', 'file:///demo', 'http://localhost/?token=secret']) assert.throws(() => endpoint(value))
  assert.equal(endpoint('http://127.0.0.1:15655').port, '15655')
  for (const args of [['--deploy', 'yes'], ['--stage'], ['--stage', 'live', '--stage', 'live'], ['--stage-ms', '600001'], ['--stage-ms', 'NaN']]) assert.throws(() => parseOptions(args))
  assert.equal(parseOptions(['--stage', 'no-progress-fixture']).stage, 'no-progress-fixture')
  const options = parseOptions(['--browser-mode', 'dedicated-cdp', '--desktop-url', 'http://localhost:15655', '--cdp-url', 'http://localhost:9222', '--workspace-path', '/fixture', '--workspace-name', 'fixture', '--binding-id', 'fixture-binding', '--swarm-id', 'fixture-swarm', '--output', '/fixture-evidence'])
  assert.equal(options.stageMs, 600000)
  assert.equal(options.stallMs, 90000)
})

test('write allowlist excludes settings/auth/setup/reset/tunnels and foreign session mutations', async () => {
  for (const route of ['/v1/auth/desktop/session', '/v1/settings', '/v1/onboarding', '/v3/sessions/foreign/messages', '/v3/sessions/fixture/permissions/resolve_all', '/deploy', '/reset']) assert.equal(Boolean(allowedRequest('POST', route, 'fixture')), false)
  assert.equal(allowedRequest('POST', '/v3/sessions/fixture/messages', 'fixture'), true)
  assert.equal(allowedRequest('POST', '/v3/sync/hydrate', 'fixture'), true)
  const source = await fs.readFile(new URL('../../scripts/runners/artifact-v3-edit-repair.mjs', import.meta.url), 'utf8')
  // Supplemental static guard, not the lifecycle or security evidence above.
  for (const forbidden of ['node:child_process', '.storageState(', '.addCookies(', 'launchPersistentContext(', 'headless: false', '--no-sandbox', 'resolve_all']) assert.equal(source.includes(forbidden), false)
})

test('exact failed draft read/edit/build/finish and second native Part edit pass', () => {
  const first = verify(fixture()); assert.equal(first.artifact_id, handle.artifact_id)
  const second = verifyRepair(fixture(false), { sessionID: handle.session_id, initial: false, artifactID: handle.artifact_id, sourceRef: firstRef })
  assert.notEqual(first.commit_oid, second.commit_oid)
  assert.equal(first.manifest_sha256, second.manifest_sha256)
  assert.deepEqual(first.part_ids, ['hero', 'pricing', 'footer'])
  assert.equal('variant_id' in first, false)
})

test('wrong or duplicate identities, incomplete lifecycle, stale diagnostics and byte drift fail closed', () => {
  const mutations = [
    (r) => { r.push(structuredClone(r[0])) },
    (r) => { r.splice(3, 1) },
    (r) => { r[0].body.draft_handle.session_id = 'foreign' },
    (r) => { r[3].args.draft_handle.candidate_id = 'foreign' },
    (r) => { r[3].body.draft_handle.grant_id = 'foreign' },
    (r) => { r[0].body.gate.Diagnostics = [] },
    (r) => { r[4].body.result.Content += 'unrelated change' },
    (r) => { r[5].body.result.Content += ' ' },
    (r) => { r[6].body.result.Diagnostics = [{ Code: 'still-broken' }] },
    (r) => { r[6].body.result.Ready = false },
    (r) => { r[7].body.status = 'fixing' },
    (r) => { r[7].body.media_inspect_reference.artifact_id = 'foreign' },
    (r) => { delete r[7].body.result.Revision.CommitOID },
    (r) => { r[2].body.result.Content = r[5].body.result.Content = JSON.stringify({ parts: [{ id: 'hero' }, { id: 'hero' }, { id: 'footer' }] }) },
  ]
  for (const mutate of mutations) { const records = structuredClone(fixture()); mutate(records); assert.throws(() => verify(records)) }
  const second = fixture(false); second[0].args.artifact_v3_reference.revision_ref = 'foreign'
  assert.throws(() => verifyRepair(second, { sessionID: handle.session_id, initial: false, artifactID: handle.artifact_id, sourceRef: { ...firstRef, revision_ref: 'correct-source' } }))
})

test('tool history requires full output scoped to exact run and unique call IDs', () => {
  const message = { role: 'tool', content: JSON.stringify({ run_id: 'fixture-run', tool_name: 'manage_artifact', call_id: 'fixture-call', arguments: JSON.stringify(fixture()[0].args), output: JSON.stringify({ artifact_v3: fixture()[0].body }) }) }
  assert.equal(toolRecords([message], 'fixture-run').length, 1)
  assert.equal(toolRecords([message], 'different-run').length, 0)
  assert.throws(() => toolRecords([message, message], 'fixture-run'), /duplicate_tool_identity/)
  const excerpt = JSON.parse(message.content); delete excerpt.output; excerpt.completed_output = 'summary'
  assert.throws(() => toolRecords([{ ...message, content: JSON.stringify(excerpt) }], 'fixture-run'), /complete_tool_evidence/)
})

test('sidebar evidence requires named visible lifecycle for one exact native identity', () => {
  const rows = ['Creating', 'Fixing', 'Ready'].map((status) => ({ id: 'fixture-artifact', status, named: true, heading: true }))
  verifySidebar(rows, 'fixture-artifact')
  assert.throws(() => verifySidebar(rows.slice(1), 'fixture-artifact'))
  assert.throws(() => verifySidebar([...rows, { ...rows[0], id: 'duplicate' }], 'fixture-artifact'))
  assert.throws(() => verifySidebar(rows.map((r) => ({ ...r, named: false })), 'fixture-artifact'))
})

test('no-progress fixture stops with retained source and deadline bounds progress churn', async () => {
  assert.deepEqual(await noProgressFixture(), { kind: 'hermetic_no_progress_fixture', elapsed_ms: 2000, retained: true })
  let clock = 0; let beats = 0
  await assert.rejects(waitStage({ sample: async () => clock, done: () => false, stageMs: 25000, stallMs: 12000, now: () => clock, pause: async (ms) => { clock += ms }, heartbeat: () => { beats++ } }), /stage_deadline_work_retained/)
  assert.equal(clock, 25000); assert.equal(beats, 3)
})

// Requirement: headless Desktop attachment owns only ephemeral browser state and
// retains the caller's exact origin. Fake Playwright is the narrow layer proving
// launch arguments and cleanup on failure, not authentication or rendered health.
test('explicit headless mode isolates browser and keeps sandbox enabled', async () => {
  const args = ['--browser-mode', 'headless', '--desktop-url', 'http://localhost:15655/demo', '--workspace-path', '/fixture', '--workspace-name', 'fixture', '--binding-id', 'fixture-binding', '--swarm-id', 'fixture-swarm', '--output', '/fixture-evidence']
  const options = parseOptions(args)
  assert.equal(options['desktop-url'], 'http://localhost:15655/demo')
  assert.throws(() => parseOptions([...args, '--cdp-url', 'http://localhost:9222']), /headless_cdp_conflict/)
  let closed = 0; let launched = 0
  const context = {}
  const browser = { newContext: async (value) => { assert.deepEqual(value, { viewport: { width: 1440, height: 1000 } }); return context }, close: async () => { closed++ } }
  const chromium = { launch: async (value) => { launched++; assert.deepEqual(value, { headless: true, chromiumSandbox: true, timeout: 15000 }); return browser }, connectOverCDP: () => { throw new Error('must_not_attach_operator') } }
  assert.deepEqual(await openProofBrowser(chromium, options), { browser, context })
  assert.equal(launched, 1); assert.equal(closed, 0)
  browser.newContext = async () => { throw new Error('context_failed') }
  await assert.rejects(openProofBrowser(chromium, options), /context_failed/)
  assert.equal(closed, 1)
  await assert.rejects(openProofBrowser(chromium, {}), /explicit_browser_mode/)
  assert.equal(launched, 2)
})

test('tool evidence rejects conflicting or missing run identities and accepts explicit message run scope', () => {
  const envelope = { tool: 'manage_artifact', call_id: 'fixture-call', arguments: fixture()[0].args, output: { artifact_v3: fixture()[0].body } }
  assert.equal(toolRecords([{ role: 'tool', run_id: 'fixture-run', content: envelope }], 'fixture-run').length, 1)
  assert.throws(() => toolRecords([{ role: 'tool', run_id: 'fixture-run', content: { ...envelope, run_id: 'foreign' } }], 'fixture-run'), /conflicting_tool_run_identity/)
  assert.throws(() => toolRecords([{ role: 'tool', content: envelope }], 'fixture-run'), /tool_run_scope_unavailable/)
})
