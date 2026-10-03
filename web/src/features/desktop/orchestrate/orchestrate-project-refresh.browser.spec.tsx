import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { readFile } from 'node:fs/promises'
import { chromium } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import { fixtureRead, project, snapshot } from './swarm-responsive-browser-fixtures'

// Requirement: OrchestrateView owns project-only identity/theme persistence and
// mounts cards only for its selection. A rendered routed page with HTTP fixtures
// proves settings placement, PATCH payloads, reload, switching and clear failure
// postconditions without asserting source strings or claiming live durability.
// Also proves the minimal header keeps native keyboard selection, confines long
// names, and routes Charter through Projects; icon-only upload must open a real
// chooser from keyboard activation. Browser DOM is required for these regressions.
test('project dropdown, settings-only appearance, persisted PNG and authoritative clear', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import{mountResponsiveFixture}from'./src/features/desktop/orchestrate/swarm-responsive-browser-fixtures';mountResponsiveFixture('populated');` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  // Exercise production utility styles too: without them the hydrated transcript
  // overflows its pane and incorrectly intercepts project control pointer input.
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n') + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8')
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
      if (url.pathname === '/v1/account/avatar') return route.fulfill({ json: { image: '', user_id: url.searchParams.get('user_id'), account_scope_id: url.searchParams.get('account_scope_id') } })
      if (url.pathname === '/v1/account/username' && req.method() === 'PUT') {
        const body = req.postDataJSON(); writes.push({ path: url.pathname, body })
        return route.fulfill({ json: { username: body.username } })
      }
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
    const mount = async () => { await page.goto('https://project.test/fixture/swarm'); await page.addStyleTag({ content: css }); await page.addScriptTag({ content: bundle.outputFiles[0].text }); await page.getByLabel('Current project').waitFor() }
    await mount()
    // Requirement: OrchestratorChatSidebar must not repeat the conversation/project
    // title above the chat. The mounted DOM is the narrowest behavioral check for
    // this extra header, while retaining the labeled sidebar and composer controls.
    const chatSidebar = page.getByRole('complementary', { name: 'Swarm Orchestrator AI Chat', exact: true })
    await chatSidebar.getByTestId('clear-orchestrator-context-btn').waitFor()
    assert.equal(await chatSidebar.locator(':scope > header').count(), 0)
    assert.equal(await page.getByLabel('Current project').inputValue(), project.id)
    // Requirement: OrchestrateView keeps a compact identity/bell header and a
    // distinct icon-segmented mode switch immediately before Tasks. Rendered
    // geometry catches the header/text regression that source checks cannot.
    // Personal photo selection must not steal the independent username edit.
    await page.getByTestId('account-name-button').click()
    await page.getByLabel('Account username').fill('Renamed operator')
    await page.getByTestId('account-name-save-btn').click()
    await page.getByRole('button', { name: 'Change account name (current: Renamed operator)' }).waitFor()
    assert.ok(writes.some(write => write.path === '/v1/account/username' && write.body.username === 'Renamed operator'))
    const modes = page.getByRole('navigation', { name: 'Chat and Swarm mode' })
    const header = page.locator('.swarm-unified-header')
    assert.equal(await header.getByRole('link', { name: 'Switch to Chat Mode' }).count(), 0)
    assert.equal(await modes.getByRole('link', { name: 'Switch to Chat Mode' }).getAttribute('href'), '/fixture')
    assert.equal(await modes.getByRole('link', { name: 'Switch to Swarm Orchestrate Mode' }).getAttribute('href'), '/fixture/swarm')
    assert.equal(await modes.getByRole('link', { name: 'Switch to Swarm Orchestrate Mode' }).getAttribute('aria-current'), 'page')
    assert.equal(await modes.locator('svg').count(), 2)
    assert.equal(await modes.evaluate(el => el.nextElementSibling?.getAttribute('aria-label')), 'Swarm destinations')
    assert.equal(await header.evaluate(el => el.scrollWidth <= el.clientWidth && el.getBoundingClientRect().height < 80), true)
    assert.equal(await modes.evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length), 2)
    const bell = header.getByRole('button', { name: /^Open notifications/ })
    await bell.waitFor()
    assert.equal(await bell.evaluate(el => el.getBoundingClientRect().left > el.parentElement!.firstElementChild!.getBoundingClientRect().left), true)
    await bell.click()
    await page.getByRole('button', { name: 'Close notifications', exact: true }).click()
    await page.setViewportSize({ width: 390, height: 844 })
    // Measure the sidebar at narrow width even while its responsive drawer is closed.
    assert.equal(await modes.evaluate(el => el.scrollWidth <= el.clientWidth), true)
    assert.equal(await header.evaluate(el => el.scrollWidth <= el.clientWidth), true)
    await page.setViewportSize({ width: 1440, height: 1000 })
    const nav = page.getByRole('navigation', { name: 'Swarm destinations' })
    assert.equal(await nav.getByRole('link', { name: 'Project Charter', exact: true }).count(), 0)
    assert.equal(await nav.getByRole('link', { name: 'Tasks', exact: true }).count(), 1)
    assert.equal(await page.getByPlaceholder('Search tasks & projects...').count(), 0)
    const selector = page.getByLabel('Current project')
    await selector.focus()
    assert.equal(await selector.evaluate(el => el === document.activeElement), true)
    await selector.press('ArrowDown')
    await page.waitForFunction(() => document.querySelector<HTMLSelectElement>('[aria-label="Current project"]')?.value === 'second-project')
    await selector.selectOption(project.id)
    const identity = page.locator('.swarm-project-identity')
    await identity.evaluate(el => { (el as HTMLElement).style.width = '140px'; (el as HTMLElement).style.flex = 'none' })
    assert.equal(await identity.locator('.swarm-project-name').evaluate(el => getComputedStyle(el).textOverflow), 'ellipsis')
    assert.equal(await identity.evaluate(el => el.scrollWidth <= el.clientWidth), true)
    await selector.selectOption('__create')
    await page.getByText('Project Name', { exact: true }).waitFor()
    assert.equal(writes.some(w => w.path === '/v3/projects'), false)
    await selector.selectOption('__manage')
    await page.getByRole('heading', { name: 'Registered Projects' }).waitFor()
    await page.getByRole('link', { name: 'Project Charter', exact: true }).click()
    await page.getByRole('heading', { name: `Project Charter: ${project.name}`, exact: true }).waitFor()
    assert.equal(await nav.getByRole('link', { name: 'Projects', exact: true }).getAttribute('aria-current'), 'location')
    await page.getByRole('link', { name: '← Projects', exact: true }).click()
    const upload = page.getByRole('button', { name: 'Upload image, video, audio, or document', exact: true })
    assert.equal(await upload.textContent(), '')
    await upload.focus()
    const chooser = page.waitForEvent('filechooser')
    await upload.press('Enter')
    assert.equal((await chooser).isMultiple(), true)
    assert.equal(await page.getByRole('button', { name: 'Paste Markdown / Text Document' }).textContent(), '')
    await nav.getByRole('link', { name: 'Tasks', exact: true }).click()
    assert.equal(await page.getByLabel('Project theme', { exact: true }).count(), 0)
    assert.equal(await page.getByRole('link', { name: 'Orchestrate tips' }).count(), 0)
    await page.getByRole('region', { name: 'Project workers', exact: true }).getByRole('button', { name: /^Inspect / }).waitFor()
    assert.equal(await page.getByRole('region', { name: 'Project workers', exact: true }).count(), 1)
    // Earlier switching to a project without a primary session can legitimately
    // request one. A rejected clear must not create an additional session.
    const sessionCreatesBeforeClear = writes.filter(w => w.path === '/v3/sessions').length
    await page.getByTestId('clear-orchestrator-context-btn').click()
    await page.getByRole('alert').getByText(/no authoritative session/).waitFor()
    assert.equal(writes.filter(w => w.path === '/v3/sessions').length, sessionCreatesBeforeClear)
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
