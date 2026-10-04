import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile, mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { build } from 'esbuild'
import { chromium, type Route } from 'playwright'
import { projectTestStyles } from './project-test-styles'
import { fixtureRead, project } from './swarm-responsive-browser-fixtures'

// Purpose: index.html's real pre-React shell plus OrchestratePage/ProjectEntryPage,
// canonical API/cache and React routing must reveal one complete task collection
// without profile/workspace/media dependencies. Real Chromium deferred HTTP routes
// prove DOM transitions, navigation, theme and request counts. This component-entry
// fixture is NOT a production-chunk, live daemon, provider or latency benchmark.
test('project hard refresh, warm entry, retry, theme and auth preserve one initial reveal', { timeout: 90_000 }, async t => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,RouterProvider,Outlet} from '@tanstack/react-router';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {ensureDesktopSession,requestStartupJson} from './src/app/api';
    import {OrchestratePage} from './src/features/desktop/orchestrate/orchestrate-page';
    import {ProjectEntryPage} from './src/features/desktop/orchestrate/project-entry-page';
    window.__swarmStartup.started();
    const root=createRootRoute({component:Outlet});
    const entry=createRoute({getParentRoute:()=>root,path:'/projects',component:ProjectEntryPage});
    const detail=createRoute({getParentRoute:()=>root,path:'/projects/$projectId',component:()=> <OrchestratePage workspaceSlug='fixture'/>});
    const router=createRouter({routeTree:root.addChildren([entry,detail])});
    window.fixtureNavigate=(id)=>id ? router.navigate({to:'/projects/$projectId',params:{projectId:id}}) : router.navigate({to:'/projects'});
    window.fixtureExpire=()=>requestStartupJson('/v1/fixture-expired').catch(()=>{});
    const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}});
    ensureDesktopSession().then(()=>createRoot(document.getElementById('root')).render(<QueryClientProvider client={client}><RouterProvider router={router}/></QueryClientProvider>));
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const css = await projectTestStyles()
  const html = (await readFile('index.html', 'utf8')).replace('<script type="module" src="/src/main.tsx"></script>', '<script src="/fixture.js"></script>')
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : { channel: 'chrome' }) })
  t.after(() => browser.close())
  const second = { ...project, id: 'second-project', name: 'Second project' }
  for (const count of [0, 1, 120]) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce', serviceWorkers: 'block' })
    const page = await context.newPage()
    page.setDefaultTimeout(12_000)
    const pending = new Map<string, Route[]>()
    const reads: string[] = [], errors: string[] = [], unknown: string[] = []
    const failTasks = count === 1
    let holdTasks = true
    let failCatalog = count === 0
    const rows = Array.from({ length: count }, (_, n) => ({ id: `startup-${n}`, title: `Startup task ${n}`, agent: 'coder', revision: 1, status: 'completed', session_id: `startup-session-${n}` }))
    const hold = (path: string, route: Route) => { pending.set(path, [...pending.get(path) || [], route]) }
    page.on('pageerror', error => errors.push(error.message))
    await context.route('**/*', async route => {
      const req = route.request(), url = new URL(req.url()), path = url.pathname
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: html })
      if (path === '/fixture.js') return route.fulfill({ contentType: 'text/javascript', body: bundle.outputFiles[0].text })
      if (!path.startsWith('/v')) return route.abort()
      reads.push(path + url.search)
      if (['/v1/me', '/v1/workspace/list', '/v1/media/settings/catalog', `/v3/projects/${project.id}/media`].includes(path)) { hold(path, route); return }
      if (path === '/v3/projects') {
        if (failCatalog) { failCatalog = false; return route.fulfill({ status: 503, json: { error: 'Catalog fixture unavailable' } }) }
        return route.fulfill({ json: { projects: [project, second] } })
      }
      if (path === `/v3/projects/${project.id}/tasks`) {
        if (holdTasks) { hold(path, route); return }
        return route.fulfill({ json: { tasks: rows } })
      }
      if (path === '/v1/fixture-expired') return route.fulfill({ status: 401, json: { error: 'Session expired' } })
      if (path === '/v3/sync/hydrate') { hold(path, route); return }
      if (path.endsWith('/sessions')) return route.fulfill({ json: { sessions: [] } })
      if (path === `/v3/projects/${second.id}/tasks`) return route.fulfill({ json: { tasks: [] } })
      if (path === `/v3/projects/${second.id}/media`) return route.fulfill({ json: { media: [] } })
      if (path === `/v3/projects/${second.id}`) return route.fulfill({ json: { project: second } })
      if (path === `/v3/projects/${second.id}/designs`) return route.fulfill({ json: { requests: [] } })
      if (path === '/v1/account/avatar') return route.fulfill({ json: { image: '', user_id: url.searchParams.get('user_id'), account_scope_id: url.searchParams.get('account_scope_id') } })
      const value = fixtureRead(url, 'populated')
      if (value === undefined) unknown.push(path)
      return route.fulfill({ status: value === undefined ? 501 : 200, json: value ?? { error: 'Unconfigured fixture read' } })
    })
    await page.addInitScript(() => {
      const win = window as any
      win.__name = (fn: unknown) => fn
      win.startupTimeline = []
      const observe = () => {
        const shell = document.getElementById('swarm-startup'), root = document.getElementById('root')
        if (!shell || !root) return
        const value = { hidden: shell.hidden, board: Boolean(root.querySelector('main.swarm-main-panel')), rows: root.querySelectorAll('[data-task-id]').length }
        const last = win.startupTimeline.at(-1)
        if (!last || last.hidden !== value.hidden || last.board !== value.board) win.startupTimeline.push(value)
      }
      new MutationObserver(observe).observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ['hidden'] })
    })
    const taskPath = `/v3/projects/${project.id}/tasks`
    const arrived = page.waitForRequest(req => new URL(req.url()).pathname === taskPath)
    await page.goto(`https://startup.test/projects/${project.id}`, { waitUntil: 'domcontentloaded' })
    await page.addStyleTag({ content: css })
    if (count === 0) {
      await page.getByRole('heading', { name: 'Unable to open project' }).waitFor()
      await page.getByRole('button', { name: 'Try again', exact: true }).click()
    }
    await arrived.catch(error => { throw new Error(`${String(error)}; errors=${JSON.stringify(errors)}; reads=${JSON.stringify(reads)}`) })
    assert.equal(await page.locator('#swarm-startup').isVisible(), count !== 0)
    assert.equal(await page.locator('.swarm-startup-mark:visible').count(), 1)
    assert.equal(await page.locator('#swarm-startup .swarm-startup-mark').evaluate(el => getComputedStyle(el).animationName), 'none')
    assert.ok(reads.includes('/v1/me'), 'profile read began independently')
    assert.ok(reads.some(path => path.startsWith('/v1/workspace/list')), 'workspace read began independently')
    const response = pending.get(taskPath)!.shift()!
    holdTasks = false
    if (failTasks) {
      await response.fulfill({ status: 503, json: { error: 'Essential fixture unavailable' } })
      await page.getByRole('heading', { name: 'Unable to open project' }).waitFor()
      assert.equal(await page.locator('#swarm-startup').isVisible(), false)
      await page.getByRole('button', { name: 'Try again', exact: true }).click()
    } else await response.fulfill({ json: { tasks: rows } })
    await page.locator('main.swarm-main-panel').waitFor().catch(async error => { throw new Error(`${String(error)}; errors=${JSON.stringify(errors)}; body=${await page.locator('body').innerText()}; reads=${JSON.stringify(reads)}`) })
    assert.equal(await page.locator('#swarm-startup').isVisible(), false)
    if (count) await page.getByText(`Startup task ${count - 1}`, { exact: true }).waitFor()
    assert.equal(reads.filter(path => /^\/v3\/projects\/[^/]+\/tasks\/[^/?]+/.test(path)).length, 0, 'no Git/model-preview fan-out')
    assert.equal(reads.filter(path => path.startsWith(taskPath)).length, failTasks ? 2 : 1)
    assert.ok(pending.get('/v1/me')?.length, 'optional profile still stalled at reveal')
    assert.ok(pending.get(`/v3/projects/${project.id}/media`)?.length, 'optional media still stalled at reveal')
    assert.equal(reads.some(path => /archived=true|view=archived/.test(path)), false)
    const timeline = await page.evaluate(() => (window as any).startupTimeline)
    assert.deepEqual(timeline.map((step: any) => [step.hidden, step.board]), (failTasks || count === 0)
      ? [[false, false], [true, false], [true, true]] : [[false, false], [true, true]])
    assert.equal(timeline.at(-1).rows, count, 'all initial task rows exist on the single board reveal')
    console.log(JSON.stringify({ fixture: 'project-startup', count, taskLists: reads.filter(path => path === taskPath).length, taskDetails: 0, timeline, requestOrder: reads }))
    if (count === 1) {
      // Observed project colors are stored by the production palette hook.
      const palette = await page.evaluate(() => JSON.parse(sessionStorage.getItem('swarm:startup-palette') || 'null'))
      assert.ok(palette?.background)
      holdTasks = true
      const refresh = page.waitForRequest(req => new URL(req.url()).pathname === taskPath)
      await page.reload({ waitUntil: 'domcontentloaded' })
      await page.addStyleTag({ content: css })
      await refresh
      assert.equal(await page.locator('#swarm-startup').isVisible(), true)
      assert.equal(await page.evaluate(() => document.documentElement.style.getPropertyValue('--startup-background')), palette.background)
      if (process.env.SWARM_STARTUP_SCREENSHOTS) {
        await mkdir(process.env.SWARM_STARTUP_SCREENSHOTS, { recursive: true })
        await page.screenshot({ path: join(process.env.SWARM_STARTUP_SCREENSHOTS, 'loading.png') })
      }
      holdTasks = false
      await pending.get(taskPath)!.shift()!.fulfill({ json: { tasks: rows } })
      await page.getByText('Startup task 0', { exact: true }).waitFor()
      if (process.env.SWARM_STARTUP_SCREENSHOTS) await page.screenshot({ path: join(process.env.SWARM_STARTUP_SCREENSHOTS, 'ready.png') })
      await page.evaluate(() => (window as any).fixtureExpire())
      await page.getByText('Your desktop authentication changed. Reload to reconnect securely.', { exact: true }).waitFor()
      assert.equal(await page.getByText('Startup task 0', { exact: true }).count(), 0)
    } else {
      const catalogCount = reads.filter(path => path === '/v3/projects').length
      if (count === 120) {
        await page.evaluate(() => { (window as any).retainedBoard = document.querySelector('main.swarm-main-panel') })
        for (const path of ['/v1/me', '/v1/workspace/list', '/v1/media/settings/catalog', `/v3/projects/${project.id}/media`]) {
          for (const route of pending.get(path) || []) await route.fulfill({ status: 503, json: { error: 'Optional fixture failed' } })
        }
        await page.getByText('Startup task 119', { exact: true }).waitFor()
        assert.equal(await page.evaluate(() => (window as any).retainedBoard === document.querySelector('main.swarm-main-panel')), true)
      }
      await page.evaluate(() => (window as any).fixtureNavigate(''))
      await page.getByRole('heading', { name: 'Swarm projects', exact: true }).waitFor()
      await page.getByRole('link', { name: second.name, exact: true }).click()
      await page.evaluate(() => (window as any).fixtureNavigate('second-project'))
      await page.getByLabel('Current project').waitFor()
      await page.waitForFunction(() => (document.querySelector('[aria-label="Current project"]') as HTMLSelectElement)?.value === 'second-project')
      assert.equal(await page.getByText('Startup task 0', { exact: true }).count(), 0)
      assert.equal(reads.filter(path => path === '/v3/projects').length, catalogCount, 'warm entry shares catalog')
    }
    assert.deepEqual(errors, [])
    assert.deepEqual(unknown, [])
    await context.close()
  }
})
