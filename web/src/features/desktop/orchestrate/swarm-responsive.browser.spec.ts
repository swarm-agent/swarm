import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { chromium, type Locator, type Page } from 'playwright'
import { fixtureRead, sessionId, snapshot, taskTitle, worker, type FixtureState } from './swarm-responsive-browser-fixtures'

// Requirement/threat: container resizing and route changes must not replace the
// actual conversation/composer, lose unsent drafts or create hidden focus targets.
// Authority: OrchestratePage/OrchestrateView, useSwarmResponsiveLayout,
// DesktopV3ExistingConversationPane, orchestratorDrafts and canonical V3 cache.
// Browser composition is the narrowest layer proving geometry + DOM identity +
// real input handlers together. Fixture UI coverage is not live provider/daemon
// proof, real transport/reconnect proof, physical keyboard proof or OS zoom proof.
// No renderer/component is mocked. Only HTTP data and the realtime controller
// boundary are fixtures. Missing HTTP contracts are reported, never passed as {}.
const widths = [360, 390, 639, 640, 641, 768, 820, 1024, 1099, 1100, 1101, 1279, 1280, 1281, 1440]
const destinations = [
  ['', 'Tasks and Canvas'], ['projects', 'Projects'], ['workers', 'Workers'],
  ['deliverables', 'Deliverables'], ['media', 'Media Studio and Library'],
  ['charter', 'Project Charter'], ['agents', 'Agents'], ['settings', 'Settings'], ['help', 'Orchestrate tips'],
] as const

// Compilation is shared, but every scenario gets a fresh browser page/cache/store.
// No persistent output, listeners, server, recursive workload or live network.
let assets: Promise<{ js: string; css: string }> | undefined
function compile() {
  return assets ??= (async () => {
    const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import {mountResponsiveFixture} from './src/features/desktop/orchestrate/swarm-responsive-browser-fixtures'; mountResponsiveFixture(window.fixtureState);` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
    const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
    const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
    const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
    return { js: bundle.outputFiles[0].text, css: css + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') }
  })()
}
async function setup(page: Page, state: FixtureState = 'populated') {
  const compiled = await compile()
  const mutations: { path: string; body: unknown }[] = []
  const missing = new Set<string>()
  const errors: string[] = []
  let hydrations = 0
  let releaseHydrate: (() => void) | undefined
  const hydrateGate = new Promise<void>(resolve => { releaseHydrate = resolve })
  let rejectSend = true
  page.setDefaultTimeout(5000)
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url())
    if (request.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<!doctype html><html><body style="margin:0"><div id="root" style="height:100dvh;width:100%"></div></body></html>' })
    if (url.pathname === '/v3/sync/hydrate') {
      hydrations++
      if (state === 'loading') await hydrateGate
      return route.fulfill({ status: state === 'error' ? 503 : 200, contentType: 'application/json', body: JSON.stringify(state === 'error' ? { error: 'Fixture hydration unavailable' } : snapshot(state)) })
    }
    if (url.pathname === `/v3/sessions/${sessionId}/artifacts/responsive-image`) return route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360"><rect width="640" height="360" fill="#245"/></svg>' })
    if (request.method() !== 'GET') {
      const body = request.headers()['content-type']?.includes('json') ? request.postDataJSON() : request.postData()
      mutations.push({ path: url.pathname, body })
      if (url.pathname === `/v3/sessions/${sessionId}/media`) return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ asset: { id: 'fixture-upload', modality: 'image', detected_mime_type: 'image/png', file_name: 'fixture.png', size: 68, digest_sha256: 'a'.repeat(64), contract_hash: 'fixture-contract' } }) })
      if (url.pathname === `/v3/sessions/${sessionId}/messages`) {
        if (rejectSend) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ ok: false, error: 'Fixture send rejected' }) })
        const message = { ...body, id: body.message_id, session_id: sessionId, global_seq: 4, created_at: 4 }
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ok: true, message, run_intent: { session_id: sessionId, run_id: body.run_id, status: 'completed', event_seq: 4, created_at: 4, updated_at: 4 }, projection: { session_id: sessionId, last_event_seq: 4, projection_high_watermark_seq: 4, updated_at: 4 } }) })
      }
      return route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ ok: false, error: 'Unexpected fixture mutation' }) })
    }
    const data = fixtureRead(url, state)
    if (data === undefined) missing.add(url.pathname)
    return route.fulfill({ status: data === undefined ? 501 : 200, contentType: 'application/json', body: JSON.stringify(data === undefined ? { error: `Missing responsive fixture: ${url.pathname}` } : data) })
  })
  await page.goto('https://responsive.test/fixture/swarm')
  await page.evaluate(state => { (window as any).fixtureState = state }, state)
  await page.addStyleTag({ content: compiled.css })
  await page.addScriptTag({ content: compiled.js })
  await page.locator('.swarm-responsive-shell').waitFor()
  await settle(page)
  return { mutations, missing, errors, hydrations: () => hydrations, releaseHydrate: () => releaseHydrate!(), acceptSend: () => { rejectSend = false } }
}
async function settle(page: Page) {
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}
async function noOverflow(page: Page, label: string) {
  const result = await page.evaluate(() => {
    const root = document.querySelector('.swarm-responsive-shell')!
    return { document: document.documentElement.scrollWidth - document.documentElement.clientWidth, root: root.scrollWidth - root.clientWidth }
  })
  assert.ok(result.document <= 1 && result.root <= 1, `${label}: horizontal overflow ${JSON.stringify(result)}`)
}
async function target(locator: Locator, label: string) {
  await locator.scrollIntoViewIfNeeded()
  const box = await locator.boundingBox()
  assert.ok(box && box.width >= 43.5 && box.height >= 43.5, `${label}: primary hit target must be >=44px: ${JSON.stringify(box)}`)
  assert.equal(await locator.isEnabled(), true, `${label}: actionable`)
  assert.equal(await locator.evaluate(el => {
    const b = el.getBoundingClientRect()
    const hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2)
    return b.top >= -1 && b.bottom <= window.innerHeight + 1 && !!hit && (hit === el || el.contains(hit))
  }), true, `${label}: on screen and unobscured`)
}
async function pane(page: Page, name: 'Main' | 'Chat') {
  if (await page.locator('.swarm-responsive-shell').getAttribute('data-split') === 'false') await page.getByRole('button', { name, exact: true }).click()
  await settle(page)
}
async function navigate(page: Page, name: string) {
  await pane(page, 'Main')
  if (await page.locator('.swarm-responsive-shell').getAttribute('data-layout') === 'phone') await page.getByRole('button', { name: 'Open Swarm navigation', exact: true }).click()
  const link = page.getByRole('navigation', { name: 'Swarm destinations' }).getByRole('link', { name, exact: true })
  await link.click()
  await settle(page)
  assert.equal(await link.getAttribute('aria-current'), 'page')
}
async function evidence(page: Page, scenario: string) {
  const dir = process.env.SWARM_RESPONSIVE_EVIDENCE_DIR
  if (!dir) return
  const revision = process.env.SWARM_RESPONSIVE_COMMIT
  assert.match(revision || '', /^[a-f0-9]{40}$/, 'evidence requires the exact tested commit SHA')
  assert.ok(path.isAbsolute(dir), 'caller must supply an absolute scratch/evidence directory')
  const relative = path.relative(path.resolve(process.cwd(), '..'), dir)
  assert.ok(relative === '..' || relative.startsWith(`..${path.sep}`), 'evidence must be outside the repository (use run-provided TMPDIR)')
  await mkdir(dir, { recursive: true })
  const viewport = page.viewportSize()!
  const name = `${scenario}-${viewport.width}x${viewport.height}-${revision}`
  await page.screenshot({ path: path.join(dir, `${name}.png`), fullPage: false })
  await writeFile(path.join(dir, `${name}.json`), JSON.stringify({ scenario, viewport, commit: revision, url: page.url(), kind: 'fixture UI; pixel inspection required' }) + '\n')
}

for (const state of ['populated', 'empty', 'loading', 'error'] as const) {
  test(`production Swarm ${state}: container breakpoint matrix and retained conversation`, { timeout: 180000 }, async () => {
    const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
    try {
      const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
      const fixture = await setup(page, state)
      if (state === 'loading' || state === 'error') {
        await page.getByTestId('orchestrator-chat-input').waitFor()
        if (state === 'error') await page.getByText('Failed to load session.', { exact: true }).waitFor()
      }
      if (state !== 'empty') {
        await page.getByTestId('orchestrator-chat-input').waitFor()
        await page.getByTestId('orchestrator-chat-input').fill('Retain this unsent real composer draft')
        await page.evaluate(() => {
          const input = document.querySelector<HTMLTextAreaElement>('[data-testid="orchestrator-chat-input"]')!
          input.setSelectionRange(7, 11)
          ;(window as any).originalConversation = document.querySelector('[data-testid="desktop-v3-existing-conversation-pane"]')
          ;(window as any).originalComposer = input
        })
        if (state === 'populated') await page.waitForFunction(() => (window as any).responsive.acquired.length > 0)
      }
      const counters = await page.evaluate(() => JSON.stringify({ acquired: (window as any).responsive.acquired, released: (window as any).responsive.released, connected: (window as any).responsive.connected }))
      const reads = fixture.hydrations()
      if (state !== 'empty') assert.deepEqual(await page.getByTestId('orchestrator-chat-input').evaluate(el => [(el as HTMLTextAreaElement).selectionStart, (el as HTMLTextAreaElement).selectionEnd]), [7, 11])
      for (const width of widths) {
        await page.setViewportSize({ width, height: 844 })
        const layout = width >= 1280 ? 'expanded' : width >= 640 ? 'rail' : 'phone'
        await page.waitForFunction(layout => document.querySelector('.swarm-responsive-shell')?.getAttribute('data-layout') === layout, layout)
        assert.equal(await page.locator('.swarm-responsive-shell').getAttribute('data-split'), String(width >= 1100))
        await noOverflow(page, `${state}/${width}`)
        if (state !== 'empty') {
          await pane(page, 'Chat')
          await target(page.getByRole('button', { name: 'Send message', exact: true }), 'send')
          assert.equal(await page.getByTestId('orchestrator-chat-input').inputValue(), 'Retain this unsent real composer draft')
          assert.equal(await page.evaluate(() => (window as any).originalComposer === document.querySelector('[data-testid="orchestrator-chat-input"]') && (window as any).originalConversation === document.querySelector('[data-testid="desktop-v3-existing-conversation-pane"]')), true)
          await pane(page, 'Main')
          await pane(page, 'Chat')
          await evidence(page, `${state}-chat`)
        }
      }
      await page.setViewportSize({ width: 1440, height: 900 })
      await page.locator('#root').evaluate(root => { root.style.width = '390px' })
      await page.waitForFunction(() => document.querySelector('.swarm-responsive-shell')?.getAttribute('data-layout') === 'phone')
      await noOverflow(page, 'embedded 390px in 1440px viewport')
      await page.locator('#root').evaluate(root => { root.style.width = '100%' })
      await page.setViewportSize({ width: 820, height: 360 })
      await settle(page)
      await noOverflow(page, 'landscape/reduced height')
      if (state !== 'empty') {
        await pane(page, 'Chat')
        await target(page.getByRole('button', { name: 'Send message', exact: true }), 'landscape send')
        assert.deepEqual(await page.getByTestId('orchestrator-chat-input').evaluate(el => [(el as HTMLTextAreaElement).selectionStart, (el as HTMLTextAreaElement).selectionEnd]), [7, 11], 'resize and pane changes retain selection')
        assert.equal(await page.evaluate(() => JSON.stringify({ acquired: (window as any).responsive.acquired, released: (window as any).responsive.released, connected: (window as any).responsive.connected })), counters, 'resize and pane switches cause no acquire/release/connect churn')
        assert.equal(fixture.hydrations(), reads, 'resize must not hydrate again')
      }
      assert.deepEqual(fixture.mutations, [], 'no mutation before explicit submit')
      fixture.releaseHydrate()
      if (state === 'populated') {
        await page.evaluate(() => (window as any).responsive.unmount())
        await page.waitForFunction(() => (window as any).responsive.released.length === (window as any).responsive.acquired.length)
        assert.equal(await page.getByTestId('desktop-v3-existing-conversation-pane').count(), 0, 'real unmount releases demand and removes conversation')
      }
      assert.deepEqual([...fixture.missing], [], 'explicit HTTP fixture coverage')
      assert.deepEqual(fixture.errors, [], 'production page must render without JS errors')
    } finally { await browser.close() }
  })
}

// Requirement/threat/authority/layer: production drawer must contain keyboard focus,
// close explicitly, restore its trigger and exclude inactive panes. Native browser
// focus/Tab checks prove the UI boundary, not a physical mobile keyboard.
test('production phone drawer traps focus, restores trigger and route history retains draft', { timeout: 90000 }, async () => {
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
    const fixture = await setup(page)
    await pane(page, 'Chat')
    const input = page.getByTestId('orchestrator-chat-input')
    await input.fill('History draft')
    const trigger = page.getByRole('button', { name: 'Open Swarm navigation', exact: true })
    await trigger.click()
    assert.equal(await trigger.getAttribute('aria-expanded'), 'true')
    const dialog = page.getByRole('dialog', { name: 'Swarm navigation' })
    await dialog.waitFor()
    for (let i = 0; i < 32; i++) {
      await page.keyboard.press('Tab')
      assert.equal(await dialog.evaluate(el => el.contains(document.activeElement)), true, 'focus remains inside drawer')
    }
    await page.keyboard.press('Shift+Tab')
    assert.equal(await dialog.evaluate(el => el.contains(document.activeElement)), true)
    assert.equal(await page.locator('.swarm-conversation-panel').evaluate(el => (el as HTMLElement).inert), true)
    assert.equal(await page.locator('.swarm-main-panel').evaluate(el => (el as HTMLElement).inert), true)
    await page.keyboard.press('Escape')
    assert.equal(await trigger.getAttribute('aria-expanded'), 'false')
    assert.equal(await trigger.evaluate(el => el === document.activeElement), true)
    await trigger.click()
    await page.locator('.swarm-navigation-backdrop').click({ position: { x: 375, y: 20 }, force: true })
    assert.equal(await trigger.getAttribute('aria-expanded'), 'false')
    assert.equal(await trigger.evaluate(el => el === document.activeElement), true)
    for (const [section, name] of destinations) {
      await navigate(page, name)
      assert.equal(new URL(page.url()).pathname, `/fixture/swarm${section ? `/${section}` : ''}`)
      await noOverflow(page, name)
      await evidence(page, `destination-${section || 'tasks'}`)
      if (section === 'projects') {
        await page.getByRole('button', { name: '+ New Project', exact: true }).click()
        await page.getByRole('heading', { name: 'Create Your Project', exact: true }).waitFor()
        await noOverflow(page, 'project onboarding')
        await page.locator('.swarm-main-panel').getByRole('button', { name: 'Cancel', exact: true }).click()
      }
      if (section === 'workers') {
        await page.getByRole('button', { name: `Inspect ${worker.name}`, exact: true }).click()
        await page.getByRole('heading', { name: worker.name, exact: true }).waitFor()
        assert.equal(new URL(page.url()).searchParams.get('workerId'), worker.id)
        await noOverflow(page, 'worker detail')
      }
      if (section === 'media') {
        await page.getByRole('button', { name: 'View Responsive image fixture', exact: true }).click()
        const viewer = page.getByRole('dialog', { name: 'Media viewer: Responsive image fixture' })
        await viewer.waitFor()
        await target(viewer.getByRole('button', { name: 'Close viewer', exact: true }), 'viewer close')
        await noOverflow(page, 'media viewer')
        await evidence(page, 'media-viewer')
        await viewer.getByRole('button', { name: 'Close viewer', exact: true }).click()
      }
      if (section === 'settings') {
        for (const subpage of ['providers', 'permissions', 'vault', 'appearance', 'notifications', 'media']) {
          const link = page.getByRole('navigation', { name: 'Settings sections' }).getByRole('link', { name: subpage, exact: true })
          await link.click()
          assert.equal(await link.getAttribute('aria-current'), 'page')
          await target(link, `settings ${subpage}`)
          await noOverflow(page, `settings ${subpage}`)
        }
      }
    }
    await page.goBack()
    await page.waitForURL('**/swarm/settings#media')
    await page.goForward()
    await page.waitForURL('**/swarm/help')
    await pane(page, 'Chat')
    assert.equal(await input.inputValue(), 'History draft')
    assert.deepEqual(fixture.mutations, [], 'destination inspection does not mutate')
    // True browser rendering at CSS zoom exercises reflow, not OS browser zoom.
    await page.locator('#root').evaluate(el => { el.style.zoom = '1.25' })
    await settle(page)
    await noOverflow(page, 'CSS zoom 125%')
    assert.deepEqual([...fixture.missing], [], 'all mounted production surfaces must have explicit HTTP fixture contracts')
    assert.deepEqual(fixture.errors, [])
  } finally { await browser.close() }
})

// Requirement/threat/authority/layer: card geometry must use the actual transcript
// lane, code/table overflow must stay local, and rejected canonical sends must keep
// the draft. Production transcript + composer/HTTP handlers, not source strings.
test('production rail geometry, transcript local scroll, dialogs and rejected-send recovery', { timeout: 90000 }, async () => {
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 820, height: 900 } })
    const fixture = await setup(page)
    const nav = page.getByRole('navigation', { name: 'Swarm destinations' })
    for (const [, name] of destinations) {
      const link = nav.getByRole('link', { name, exact: true })
      await target(link, name)
      await link.focus()
      assert.equal(await link.evaluate(el => getComputedStyle(el, '::after').display), 'block', 'rail focus shows named tooltip')
    }
    await pane(page, 'Chat')
    await page.getByTestId('desktop-chat-content-lane').first().waitFor()
    const metrics = await page.locator('[data-chat-tool-message]').evaluateAll(cards => cards.map(card => {
      const lane = card.closest('[data-testid="desktop-chat-content-lane"]')!
      const bounds = card.getBoundingClientRect(), laneBounds = lane.getBoundingClientRect(), padding = parseFloat(getComputedStyle(lane).paddingLeft)
      const composer = document.querySelector('[data-testid="orchestrator-chat-composer"]')!
      return { center: Math.abs(bounds.left + bounds.width / 2 - laneBounds.left - laneBounds.width / 2), gutter: Math.abs(bounds.left - laneBounds.left - padding), composerCenter: Math.abs(bounds.left + bounds.width / 2 - (composer.getBoundingClientRect().left + composer.getBoundingClientRect().width / 2)) }
    }))
    assert.ok(metrics.length >= 2, 'Bash and read cards must actually render')
    for (const metric of metrics) assert.ok(metric.center <= 2 && metric.gutter <= 2 && metric.composerCenter <= 2, JSON.stringify(metric))
    for (const selector of ['.chat-markdown pre', '.chat-markdown table']) {
      const node = page.locator(selector).first()
      await node.waitFor()
      assert.equal(await node.evaluate(el => {
        let scroller: Element | null = el
        while (scroller && !['auto', 'scroll'].includes(getComputedStyle(scroller).overflowX)) scroller = scroller.parentElement
        if (!scroller || scroller === document.documentElement) return false
        scroller.scrollLeft = 100
        return scroller.scrollWidth > scroller.clientWidth + 1 && scroller.scrollLeft > 0
      }), true, `${selector} scroll remains local`)
    }
    await noOverflow(page, 'expanded transcript')
    const disclosure = page.getByRole('button', { name: 'View all', exact: true }).first()
    await disclosure.click()
    assert.equal(await page.locator('[data-bash-output="virtualized"]').count(), 1)
    await page.getByRole('button', { name: 'Collapse', exact: true }).first().click()
    assert.equal(await page.locator('[data-bash-output="bounded-preview"]').count(), 1)
    await noOverflow(page, 'collapsed tool output')
    await page.evaluate(() => (window as any).responsive.running(true))
    await target(page.getByRole('button', { name: 'Stop agent run', exact: true }), 'stop')
    await page.evaluate(() => (window as any).responsive.running(false))
    await pane(page, 'Main')
    const selectedCard = page.getByTestId('orchestrate-task-card').first()
    await selectedCard.getByRole('button', { name: 'Ask orchestrator', exact: true }).click()
    await pane(page, 'Chat')
    await page.getByTestId('composer-attached-task').waitFor()
    const input = page.getByTestId('orchestrator-chat-input')
    await input.fill('Send exactly once')
    await page.locator('[data-testid="orchestrator-chat-composer"] input[type="file"]').setInputFiles({ name: 'fixture.png', mimeType: 'image/png', buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL9sAAAAASUVORK5CYII=', 'base64') })
    await page.getByRole('button', { name: 'Remove attachment', exact: true }).waitFor()
    const uploadCount = fixture.mutations.length
    assert.equal(uploadCount, 1, 'one explicit attachment upload')
    await page.setViewportSize({ width: 390, height: 844 })
    await settle(page)
    await pane(page, 'Main')
    await pane(page, 'Chat')
    assert.equal(await page.getByRole('button', { name: 'Remove attachment', exact: true }).count(), 1, 'attachment survives resize/pane changes')
    assert.ok((await page.getByTestId('composer-attached-task').textContent())?.includes(taskTitle), 'selected task context survives pane changes')
    await page.getByRole('button', { name: 'Send message', exact: true }).click()
    await page.getByTestId('chat-send-error').getByText('Fixture send rejected', { exact: true }).waitFor()
    assert.equal(await input.inputValue(), 'Send exactly once')
    assert.equal(fixture.mutations.length, uploadCount + 1)
    assert.equal(await page.getByRole('button', { name: 'Remove attachment', exact: true }).count(), 1, 'rejected send retains attachment')
    fixture.acceptSend()
    await page.getByRole('button', { name: 'Send message', exact: true }).click()
    await page.waitForFunction(() => !(document.querySelector('[data-testid="orchestrator-chat-input"]') as HTMLTextAreaElement)?.value)
    assert.equal(fixture.mutations.length, uploadCount + 2, 'one append request for each explicit submit')
    assert.equal((fixture.mutations[uploadCount + 1].body as any).content, 'Send exactly once')
    assert.equal((fixture.mutations[uploadCount + 1].body as any).media[0].asset_id, 'fixture-upload')
    await navigate(page, 'Tasks and Canvas')
    const card = page.getByTestId('orchestrate-task-card').first()
    await card.getByTestId('toggle-task-details-btn').click()
    assert.equal(await card.getByTestId('toggle-task-details-btn').getAttribute('aria-expanded'), 'true')
    assert.ok((await card.textContent())?.includes(taskTitle))
    await noOverflow(page, 'expanded task detail')
    await page.getByRole('button', { name: 'Archived', exact: true }).click()
    const archived = page.getByRole('dialog', { name: 'Archived tasks' })
    await archived.waitFor()
    await target(archived.getByRole('button', { name: 'Close archived tasks' }), 'archive close')
    await archived.getByRole('button', { name: 'Close archived tasks' }).click()
    await target(page.getByRole('button', { name: 'New task', exact: true }), 'new task primary')
    await page.getByRole('button', { name: 'New task', exact: true }).click()
    const deploy = page.getByRole('dialog', { name: 'Deploy Autonomous Task' })
    await deploy.waitFor()
    await noOverflow(page, 'new task dialog')
    await evidence(page, 'new-task-dialog')
    await deploy.getByTestId('deploy-modal-change-model-btn').click()
    const model = page.getByRole('dialog', { name: 'Agent and model settings', exact: true })
    await model.waitFor()
    await target(model.getByRole('button', { name: 'Close', exact: true }).first(), 'model close')
    await noOverflow(page, 'model dialog')
    await evidence(page, 'model-dialog')
    await model.getByRole('button', { name: 'Close', exact: true }).first().click()
    assert.deepEqual([...fixture.missing], [], 'all mounted production surfaces must have explicit HTTP fixture contracts')
    assert.deepEqual(fixture.errors, [])
  } finally { await browser.close() }
})
