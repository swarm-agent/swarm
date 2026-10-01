import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { fixtureRead, project, snapshot } from './swarm-responsive-browser-fixtures'

// Requirement: OrchestrateView owns project-only identity/theme persistence and
// mounts cards only for its selection. A rendered routed page with HTTP fixtures
// proves settings placement, PATCH payloads, reload, switching and clear failure
// postconditions without asserting source strings or claiming live durability.
test('project dropdown, settings-only appearance, persisted PNG and authoritative clear', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import{mountResponsiveFixture}from'./src/features/desktop/orchestrate/swarm-responsive-browser-fixtures';mountResponsiveFixture('populated');` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    const saved: any = { ...project }
    const second = { ...project, id: 'second-project', name: 'Second project', primary_session_id: '' }
    const writes: { path: string; body: any }[] = []
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      if (req.method() === 'PATCH' && url.pathname === `/v3/projects/${project.id}`) {
        const body = req.postDataJSON(); writes.push({ path: url.pathname, body }); Object.assign(saved, body)
        return route.fulfill({ json: { project: saved } })
      }
      if (req.method() !== 'GET') {
        writes.push({ path: url.pathname, body: req.postData() })
        return route.fulfill({ json: {} }) // malformed reset must fail closed
      }
      if (url.pathname === '/v3/projects') return route.fulfill({ json: { projects: [saved, second] } })
      if (url.pathname === `/v3/projects/${project.id}`) return route.fulfill({ json: { project: saved } })
      if (url.pathname === `/v3/projects/${second.id}`) return route.fulfill({ json: { project: second } })
      if (url.pathname.startsWith(`/v3/projects/${second.id}/`)) return route.fulfill({ json: { tasks: [], media: [] } })
      const data = fixtureRead(url, 'populated')
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Unconfigured fixture read' } })
    })
    const mount = async () => { await page.goto('https://project.test/fixture/swarm'); await page.addScriptTag({ content: bundle.outputFiles[0].text }); await page.getByLabel('Current project').waitFor() }
    await mount()
    assert.equal(await page.getByLabel('Project theme', { exact: true }).count(), 0)
    assert.equal(await page.getByRole('link', { name: 'Orchestrate tips' }).count(), 0)
    await page.getByRole('region', { name: 'Project workers', exact: true }).getByRole('button', { name: /^Inspect / }).waitFor()
    assert.equal(await page.getByRole('region', { name: 'Project workers', exact: true }).count(), 1)
    await page.getByTestId('clear-orchestrator-context-btn').click()
    await page.getByRole('alert').getByText(/no authoritative session/).waitFor()
    assert.equal(writes.filter(w => w.path === '/v3/sessions').length, 0)
    await page.getByRole('link', { name: 'Settings', exact: true }).click()
    const theme = page.getByLabel('Project theme', { exact: true })
    await theme.waitFor()
    const themeId = await theme.locator('option').nth(1).getAttribute('value')
    assert.ok(themeId)
    await theme.selectOption(themeId!)
    await page.waitForFunction(id => document.querySelector<HTMLSelectElement>('#swarm-project-theme')?.value === id && !document.querySelector<HTMLSelectElement>('#swarm-project-theme')?.disabled, themeId)
    assert.equal(saved.theme_id, themeId)
    await theme.selectOption('')
    await page.waitForFunction(() => !document.querySelector<HTMLSelectElement>('#swarm-project-theme')?.disabled)
    assert.equal(saved.theme_id, '')
    const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5XkAAAAASUVORK5CYII=', 'base64')
    await page.getByLabel('Project PNG').setInputFiles({ name: 'project.png', mimeType: 'image/png', buffer: png })
    await page.getByRole('button', { name: 'Reset project image' }).waitFor()
    await page.waitForFunction(() => !!document.querySelector('.swarm-project-identity img'))
    assert.ok(saved.icon_png_data_url.startsWith('data:image/png;base64,'))
    assert.deepEqual(Object.keys(writes.find(w => w.body?.icon_png_data_url)?.body), ['icon_png_data_url'])
    await mount()
    await page.waitForFunction(() => !!document.querySelector('.swarm-project-identity img'))
    await page.getByLabel('Current project').selectOption(second.id)
    await page.waitForFunction(() => !document.querySelector('.swarm-project-identity img'))
    assert.equal(await page.getByLabel('Current project').inputValue(), second.id)
    assert.equal(await page.getByRole('region', { name: 'Project workers', exact: true }).count(), 1)
    assert.equal(await page.getByRole('region', { name: 'Project workers', exact: true }).getByRole('button', { name: /^Inspect / }).count(), 0)
    await page.getByLabel('Current project').selectOption(project.id)
    await page.getByRole('link', { name: 'Settings', exact: true }).click()
    await page.getByRole('button', { name: 'Reset project image' }).click()
    await page.waitForFunction(() => !document.querySelector('.swarm-project-identity img'))
    assert.equal(saved.icon_png_data_url, '')
    assert.equal(writes.some(w => w.path.includes('/workspace/') || w.path === '/v1/ui/settings'), false)
  } finally { await browser.close() }
})
