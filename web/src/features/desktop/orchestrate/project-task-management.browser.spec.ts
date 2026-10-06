import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium, type Route } from 'playwright'
import { projectTestStyles } from './project-test-styles'
import { fixtureRead, project, snapshot } from './swarm-responsive-browser-fixtures'

// Purpose: real OrchestrateView management wiring must retain failed selection,
// remove only acknowledged tasks, cap requests and reject stale archive-dialog
// responses after project switching. HTTP fixtures prove UI contracts only, not
// measured daemon latency, provider execution or backend liveness protection.
// Scope archive controls to the task canvas, not the separate session/media archives.
test('project task archive retains partial failures and isolates late archived-list responses', { timeout: 45000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,RouterProvider} from '@tanstack/react-router';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {ensureDesktopSession} from './src/app/api';
    import {OrchestratePage} from './src/features/desktop/orchestrate/orchestrate-page';
    async function mount() {
      await ensureDesktopSession();
      const root=createRootRoute({component:()=> <OrchestratePage workspaceSlug='fixture'/>});
      const routes=['/projects','/projects/$projectId','/projects/$projectId/sessions/$sessionId'].map(path=>createRoute({getParentRoute:()=>root,path,validateSearch:s=>s}));
      const router=createRouter({routeTree:root.addChildren(routes)});
      const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}});
      createRoot(document.getElementById('root')).render(<QueryClientProvider client={client}><RouterProvider router={router}/></QueryClientProvider>);
    }
    mount();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const css = await projectTestStyles()
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    page.setDefaultTimeout(7000)
    page.on('pageerror', error => console.error(error.message))
    const second = { ...project, id: 'second-project', name: 'Second project' }
    const rows = Array.from({ length: 6 }, (_, index) => ({ id: `task-${index}`, title: `Archive candidate ${index}`, agent: 'coder', revision: 1, status: 'queued' }))
    const archived = new Set<string>()
    const writes: string[] = []
    const pending: Route[] = []
    const waiters: Array<{ count: number; resolve: () => void }> = []
    const waitForCount = (count: number) => pending.length >= count ? Promise.resolve() : new Promise<void>(resolve => waiters.push({ count, resolve }))
    let oldArchived: Route | undefined
    let active = 0, peak = 0
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      if (url.pathname === '/v3/projects') return route.fulfill({ json: { projects: [project, second] } })
      if (url.pathname === '/v1/account/avatar') return route.fulfill({ json: { image: '', user_id: url.searchParams.get('user_id'), account_scope_id: url.searchParams.get('account_scope_id') } })
      if (req.method() === 'GET' && url.pathname.endsWith('/sessions')) return route.fulfill({ json: { sessions: [] } })
      if (url.pathname === `/v3/projects/${second.id}`) return route.fulfill({ json: { project: second } })
      if (url.pathname === `/v3/projects/${project.id}/tasks` && url.searchParams.get('view') === 'archived') { oldArchived = route; return }
      if (url.pathname === `/v3/projects/${project.id}/tasks`) return route.fulfill({ json: { tasks: rows.filter(row => !archived.has(row.id)) } })
      if (url.pathname.startsWith(`/v3/projects/${second.id}/`)) return route.fulfill({ json: { tasks: url.searchParams.get('view') === 'archived' ? [{ id: 'second-archive', title: 'Second archived task', revision: 2 }] : [], media: [] } })
      if (req.method() === 'POST' && url.pathname.endsWith('/archive')) {
        writes.push(url.pathname)
        active++; peak = Math.max(peak, active)
        pending.push(route)
        for (const waiter of waiters) if (pending.length >= waiter.count) waiter.resolve()
        return
      }
      if (req.method() !== 'GET') { writes.push(`${req.method()} ${url.pathname}`); return route.fulfill({ status: 400, json: { error: 'Unexpected mutation' } }) }
      const data = fixtureRead(url, 'populated')
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Unconfigured fixture read' } })
    })
    await page.goto(`https://task-management.test/projects/${project.id}`)
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('checkbox', { name: 'Select all', exact: true }).click()
    const archive = page.getByRole('button', { name: 'Archive', exact: true })
    await archive.click()
    // Release each batch only after Chromium has delivered the reserved requests.
    const finish = async (route: Route) => {
      const id = new URL(route.request().url()).pathname.split('/').at(-2)!
      assert.equal(route.request().postDataJSON().revision, 1)
      active--
      if (id === 'task-2') await route.fulfill({ status: 409, json: { error: 'stale revision' } })
      else { archived.add(id); await route.fulfill({ json: { task: { id, revision: 2, archived: true } } }) }
    }
    // Use Playwright request events instead of timing assertions for concurrency.
    await waitForCount(4)
    assert.equal(pending.length, 4)
    assert.equal(await archive.isDisabled(), true)
    for (let index = 0; index < 6; index++) {
      await waitForCount(index + 1)
      await finish(pending[index])
    }
    await page.getByText(/5 archived, 1 failed/).waitFor()
    assert.equal(peak, 4)
    assert.equal(writes.length, 6)
    assert.ok(writes.every(path => path.endsWith('/archive')))
    assert.equal(await archive.isEnabled(), true, 'failed selection remains retryable')
    if (process.env.SWARM_TASK_ARCHIVE_SCREENSHOT) await page.screenshot({ path: process.env.SWARM_TASK_ARCHIVE_SCREENSHOT })
    await page.locator('main.swarm-main-panel').getByRole('button', { name: 'Archived', exact: true }).click()
    await page.getByText('Loading archived tasks…').waitFor()
    await page.getByRole('button', { name: 'Close archived tasks', exact: true }).click()
    await page.getByLabel('Current project').selectOption(second.id)
    await page.locator('main.swarm-main-panel').getByRole('button', { name: 'Archived', exact: true }).click()
    await page.getByText('Second archived task', { exact: true }).waitFor()
    assert.ok(oldArchived)
    await oldArchived!.fulfill({ json: { tasks: [{ id: 'old-archive', title: 'Wrong project archive', revision: 2 }] } })
    await page.getByText('Second archived task', { exact: true }).waitFor()
    assert.equal(await page.getByText('Wrong project archive', { exact: true }).count(), 0)
  } finally { await browser.close() }
})
