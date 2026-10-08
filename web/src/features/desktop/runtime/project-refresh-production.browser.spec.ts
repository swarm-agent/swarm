import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdtemp, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build, preview } from 'vite'
import { chromium, type Route } from 'playwright'
import { fixtureRead, project } from '../orchestrate/swarm-responsive-browser-fixtures'

// Requirement: the actual production app/router must render 13 task cards and
// collapsed question controls while unrelated optional HTTP reads are stalled.
// Real compiled chunks, browser and canonical cache are exercised; HTTP fixtures
// prove ordering/volume/cancellation only, NOT real daemon speed or provider work.
test('production 13-card refresh overlaps bounded permission batches and preserves collapsed questions', { timeout: 120_000 }, async t => {
  assert.ok(process.env.TMPDIR)
  const root = fileURLToPath(new URL('../../../../', import.meta.url))
  const outDir = await mkdtemp(join(process.env.TMPDIR, 'project-refresh-production-'))
  t.after(() => rm(outDir, { recursive: true, force: true }))
  await build({ root, configFile: join(root, 'vite.config.ts'), logLevel: 'error', build: { outDir, emptyOutDir: true } })
  const server = await preview({ root, configFile: join(root, 'vite.config.ts'), logLevel: 'error', build: { outDir }, preview: { host: '127.0.0.1', port: 0, proxy: {} } })
  t.after(() => { server.httpServer.closeAllConnections(); server.httpServer.close() })
  const address = server.httpServer.address(); assert.ok(address && typeof address !== 'string')
  const origin = `http://127.0.0.1:${address.port}`
  const browser = await chromium.launch({ headless: true, channel: 'chrome' }); t.after(() => browser.close())
  const context = await browser.newContext({ serviceWorkers: 'block' })
  const page = await context.newPage(); page.setDefaultTimeout(12_000)
  const tasks = Array.from({ length: 13 }, (_, i) => ({ id: `task-${i}`, title: `Task ${i}`, session_id: `session-${i}`, agent: 'coder', status: 'needs_review', revision: 1 }))
  const reads: string[] = [], unexpected: string[] = [], errors: string[] = []
  const hydration: Array<{ route: Route; ids: string[]; resources: Record<string, boolean> }> = []
  const held: Route[] = []
  let lists = 0
  page.on('pageerror', error => errors.push(error.message))
  const snapshot = (ids: string[], resources: Record<string, boolean>, question = false) => ({
    ok: true, rev: 1, scope_id: 'fixture', snapshot_endpoint_cursor: 'opaque',
    selector: { kind: 'session_ids', session_ids: ids }, session_order: ids,
    sync_scope: { surface: 'desktop', stream_kind: 'v3.sync.snapshot', selector_filter_hash: 'fixture', resource_set: Object.keys(resources).filter(key => resources[key]).join(',') },
    sessions_by_id: Object.fromEntries(ids.map(id => [id, { id, account_scope_id: 'fixture-account', user_id: 'fixture-operator', workspace_path: '.', title: id, mode: 'auto', created_at: 1, updated_at: 1 }])),
    projections_by_session: {}, current_run_state_by_session: {}, known_sessions: {}, tombstones_by_session: {},
    permission_summaries_by_session: Object.fromEntries(ids.map(id => [id, { session_id: id, pending_approval_count: question && id === 'session-12' ? 1 : 0, updated_at: 1 }])),
    session_views_by_id: resources.permission_details ? Object.fromEntries(ids.map(id => [id, { has_active_plan: false, pending_permissions: question && id === 'session-12' ? [{ id: 'question', session_id: id, run_id: 'run', call_id: 'call', tool_name: 'ask_user', tool_arguments: JSON.stringify({ questions: [{ id: 'q', question: 'Choose direction', options: ['A', 'B'] }] }), status: 'pending', created_at: 1, updated_at: 1 }] : [] }])) : {},
    replay_instructions: { stream_path: '/v3/sync/stream', transport: 'http_post', after_endpoint_cursor: 'opaque', bootstrap_required_on_cursor_error: true },
  })
  await context.route('**/*', async route => {
    const url = new URL(route.request().url()), path = url.pathname
    if (!/^\/v[123]\//.test(path)) return route.continue()
    reads.push(path)
    if (['/v1/me', '/v1/media/settings/catalog', `/v3/projects/${project.id}/media`].includes(path)) { held.push(route); return }
    if (path === '/v1/onboarding/tailscale-origin') return route.fulfill({ json: { required: false } })
    if (path === '/v1/onboarding') return route.fulfill({ json: { ok: true, needs_onboarding: false, vault: { enabled: false, unlocked: true } } })
    if (path === '/v3/sync/bootstrap') return route.fulfill({ json: snapshot([], {}) })
    if (path === '/v3/sync/hydrate') {
      const input = route.request().postDataJSON()
      hydration.push({ route, ids: input.session_ids, resources: input.resources }); return
    }
    if (path === `/v3/projects/${project.id}/tasks`) { lists++; return route.fulfill({ json: { tasks } }) }
    if (path === `/v3/projects/${project.id}/sessions`) return route.fulfill({ json: { sessions: [] } })
    if (path === '/v1/model/catalog/check') return route.fulfill({ json: { ok: true } })
    if (path === '/v1/model/catalog') return route.fulfill({ json: { models: [] } })
    if (path === '/v1/models/favorites') return route.fulfill({ json: { favorites: [] } })
    if (path === '/v1/account/avatar') return route.fulfill({ json: { image: '' } })
    const value = fixtureRead(url, 'populated')
    if (value === undefined) unexpected.push(path)
    return route.fulfill({ status: value === undefined ? 501 : 200, json: value ?? { error: 'Unconfigured boundary' } })
  })
  await page.goto(`${origin}/projects/${project.id}`, { waitUntil: 'domcontentloaded' })
  await page.getByTestId('orchestrate-task-card').nth(12).waitFor().catch(async error => { throw new Error(`${String(error)}; requests=${JSON.stringify(reads)}; errors=${JSON.stringify(errors)}; body=${(await page.locator('body').innerText()).slice(0, 1500)}`) })
  await page.waitForFunction(() => document.querySelectorAll('[data-testid="orchestrate-task-card"]').length === 13)
  // Wait on request arrival rather than arbitrary sleeps: both independent batches
  // must start while neither response nor optional catalog is released.
  await assert.doesNotReject(async () => {
    const deadline = Date.now() + 10_000
    while (hydration.length < 2 && Date.now() < deadline) await new Promise(resolve => setImmediate(resolve))
    assert.equal(hydration.length, 2)
  })
  assert.deepEqual(hydration.map(item => item.ids.length), [8, 5])
  assert.ok(hydration.every(item => item.resources.permission_details && !item.resources.session_view))
  assert.equal(lists, 1); assert.ok(held.length >= 2)
  await hydration[1].route.fulfill({ json: snapshot(hydration[1].ids, hydration[1].resources, true) })
  await page.getByRole('button', { name: 'Submit response', exact: true }).waitFor()
  for (const name of ['A', 'B', 'Custom response']) assert.equal(await page.getByRole('button', { name, exact: true }).count(), 1)
  assert.equal(await page.getByRole('dialog').count(), 0)
  assert.equal(await page.getByTestId('orchestrate-task-card').count(), 13)
  assert.equal(await page.getByTestId('toggle-task-details-btn').last().getAttribute('aria-expanded'), 'false')
  await hydration[0].route.fulfill({ json: snapshot(hydration[0].ids, hydration[0].resources) })
  assert.equal(reads.filter(path => /^\/v3\/projects\/[^/]+\/tasks\/[^/]+$/.test(path)).length, 0)
  assert.deepEqual(errors, []); assert.deepEqual(unexpected, [])
})
