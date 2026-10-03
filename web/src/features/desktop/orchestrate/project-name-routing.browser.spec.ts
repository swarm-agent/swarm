import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fixtureRead, project, sessionId, snapshot } from './swarm-responsive-browser-fixtures'

// Purpose: OrchestratePage/View must resolve browser names before API calls and
// provenance admission. Render the real page with bounded HTTP/controller fixtures
// to prove ID bookmark migration, reload, session switching and compact geometry.
// This is deterministic browser integration, not live daemon/provider evidence.
test('project name URLs preserve API identity, reload and compact navigation', { timeout: 60000 }, async () => {
  const js = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import {mountResponsiveFixture} from './src/features/desktop/orchestrate/swarm-responsive-browser-fixtures'; mountResponsiveFixture('populated', true);` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n') + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    page.setDefaultTimeout(7000)
    const requests: string[] = [], errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    const conversation = (id: string) => ({ ...snapshot().sessions_by_id[sessionId], id, title: `Session ${id}`, metadata: { agent_name: 'system-orchestrator', project_id: project.id } })
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      requests.push(`${req.method()} ${url.pathname}`)
      if (url.pathname === '/v3/projects') return route.fulfill({ json: { projects: [{ ...project, name: 'Swarm Go' }] } })
      if (url.pathname === `/v3/projects/${project.id}/sessions`) return route.fulfill({ json: { sessions: Array.from({ length: 30 }, (_, i) => ({ session: conversation(i ? `session-${i}` : sessionId) })) } })
      if (url.pathname === `/v3/sessions/${sessionId}` || url.pathname === '/v3/sessions/session-1') return route.fulfill({ json: { session: conversation(url.pathname.split('/').at(-1)!) } })
      if (url.pathname === '/v3/sync/hydrate') {
        const ids = req.postDataJSON().session_ids || [sessionId]
        const data = snapshot()
        for (const key of ['projections_by_session', 'messages_by_session', 'events_by_session', 'session_views_by_id', 'permission_summaries_by_session'] as const) {
          if (!ids.includes(sessionId)) (data as any)[key] = {}
        }
        return route.fulfill({ json: { ...data, session_order: ids, sessions_by_id: Object.fromEntries(ids.map((id: string) => [id, conversation(id)])) } })
      }
      if (url.pathname === '/v1/account/avatar') return route.fulfill({ json: { image: '', user_id: url.searchParams.get('user_id'), account_scope_id: url.searchParams.get('account_scope_id') } })
      const data = fixtureRead(url, 'populated')
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Unconfigured fixture read' } })
    })
    const mount = async (url: string) => {
      await page.goto(url); await page.addStyleTag({ content: css }); await page.addScriptTag({ content: js.outputFiles[0].text })
      await page.waitForFunction(() => document.querySelector<HTMLSelectElement>('[aria-label="Current project"]')?.value === 'responsive-project')
    }
    await mount(`https://project.test/projects/${project.id}/sessions/${sessionId}?section=workers#retained`)
    await page.waitForURL(`**/projects/swarm-go/sessions/${sessionId}?section=workers#retained`)
    const nav = page.getByRole('navigation', { name: 'Swarm destinations' })
    assert.equal(await nav.locator('[aria-current="page"]').count(), 1)
    assert.equal(await nav.getByRole('link', { name: 'Workers', exact: true }).getAttribute('aria-current'), 'page')
    assert.equal(await nav.evaluate(el => el.previousElementSibling?.tagName), 'HEADER')
    await page.getByRole('navigation', { name: 'Conversation sessions' }).getByRole('link', { name: 'Session session-1 Swarm Go', exact: true }).click()
    await page.waitForURL('**/projects/swarm-go/sessions/session-1')
    await mount(page.url())
    assert.equal(await page.getByLabel('Current project').inputValue(), project.id)
    assert.ok(requests.includes(`GET /v3/projects/${project.id}/sessions`))
    assert.ok(requests.includes('GET /v3/sessions/session-1'))
    assert.equal(requests.some(url => url.includes('/v3/projects/swarm-go')), false)
    for (const label of ['Workers', 'Tasks', 'Agents']) {
      await nav.getByRole('link', { name: label, exact: true }).click()
      await page.waitForFunction(label => document.querySelector('.swarm-route-navigation [aria-current="page"]')?.getAttribute('aria-label') === label, label)
      assert.equal(await nav.locator('[aria-current="page"]').count(), 1)
    }
    await page.goBack()
    await page.waitForFunction(() => document.querySelector('.swarm-route-navigation [aria-current="page"]')?.getAttribute('aria-label') === 'Tasks')
    await page.goForward()
    await page.waitForFunction(() => document.querySelector('.swarm-route-navigation [aria-current="page"]')?.getAttribute('aria-label') === 'Agents')
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 900 })
      if (width === 390) await page.getByRole('button', { name: 'Open Swarm navigation', exact: true }).click()
      const sidebar = page.locator('.swarm-navigation-sidebar')
      assert.equal(await sidebar.evaluate(el => el.scrollWidth <= el.clientWidth), true)
      assert.equal(await sidebar.evaluate(el => {
        const children = [...el.children].filter(child => getComputedStyle(child).display !== 'none').map(child => child.getBoundingClientRect()).filter(rect => rect.height > 0)
        return children.every((rect, index) => !index || rect.top >= children[index - 1].bottom - 1)
      }), true, 'sidebar sections must not overlap')
      assert.equal(await sidebar.getByRole('alert').count(), 0, 'healthy fixture must not expose hydration failures')
      await sidebar.getByRole('button', { name: 'New session', exact: true }).scrollIntoViewIfNeeded()
      assert.equal(await sidebar.getByRole('button', { name: 'New session', exact: true }).isVisible(), true)
      if (process.env.SWARM_PROJECT_PAGE_SCREENSHOT) await sidebar.screenshot({ path: process.env.SWARM_PROJECT_PAGE_SCREENSHOT.replace('.png', `-${width}.png`) })
    }
    await page.setViewportSize({ width: 1440, height: 900 })
    await mount(`https://project.test/projects/${project.id}/sections/workers`)
    await page.waitForURL('**/projects/swarm-go/sections/workers')
    assert.equal(await nav.getByRole('link', { name: 'Workers', exact: true }).getAttribute('aria-current'), 'page')
    const beforeUnknown = requests.length
    await page.goto('https://project.test/projects/missing-project/sessions/session-1')
    await page.addStyleTag({ content: css }); await page.addScriptTag({ content: js.outputFiles[0].text })
    await page.getByRole('alert').filter({ hasText: 'Project not found or name is ambiguous' }).waitFor()
    assert.equal(requests.slice(beforeUnknown).some(url => url.includes('/v3/projects/missing-project')), false)
    assert.equal(await page.getByTestId('orchestrator-chat-input').count(), 0)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
