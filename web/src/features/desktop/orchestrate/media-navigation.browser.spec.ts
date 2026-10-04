import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { projectTestStyles } from './project-test-styles'
import { fixtureRead, project, snapshot } from './swarm-responsive-browser-fixtures'

// Purpose: OrchestrateView must classify navigation using durable task.agent, not
// titles, attachments or outputs. Exercise the real project runtime and sidebar:
// all task counts exclude media, code-with-media stays visible, tab switching keeps
// selected turns, project changes/reloads reconstruct history, events update the
// same cards, and read retries never dispatch generation. HTTP fixtures isolate
// this UI boundary; this is not live daemon/provider or persistence evidence.
test('right-side media stays separate from tasks across tabs, events, projects and reload', { timeout: 90_000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,RouterProvider} from '@tanstack/react-router';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {ensureDesktopSession} from './src/app/api';
    import {OrchestratePage} from './src/features/desktop/orchestrate/orchestrate-page';
    import {desktopProjects} from './src/features/desktop/runtime/desktop-projects';
    async function mount() {
      await ensureDesktopSession();
      const root=createRootRoute({component:()=> <OrchestratePage workspaceSlug='fixture'/>});
      const routes=['/projects','/projects/$projectId','/projects/$projectId/sessions/$sessionId'].map(path=>createRoute({getParentRoute:()=>root,path,validateSearch:s=>s}));
      const router=createRouter({routeTree:root.addChildren(routes)});
      window.refreshMedia=()=>desktopProjects.invalidate('${project.id}');
      window.mediaEvent=(id,revision)=>desktopProjects.acceptFrame({kind:'project.updated',project_id:'${project.id}',event:{payload:{project_id:'${project.id}',task_id:id,action:'task_updated',revision}}});
      const client=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity}}});
      createRoot(document.getElementById('root')).render(<QueryClientProvider client={client}><RouterProvider router={router}/></QueryClientProvider>);
    } mount();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const css = await projectTestStyles()
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    page.setDefaultTimeout(8000)
    const errors: string[] = []; page.on('pageerror', error => errors.push(error.message))
    const second = { ...project, id: 'second-project', name: 'Second project' }
    const image = { id: 'output', title: 'Blue square', kind: 'image', type: 'image', status: 'ready', media_url: '/fixture.png' }
    const rows = [
      { id: 'code', title: 'Code with image attachment', agent: 'coder', revision: 1, status: 'queued', attached_media: [{ id: 'input', title: 'Photo', kind: 'image' }], deliverables: [image] },
      { id: 'image', title: 'Image generation', agent: 'image', revision: 1, status: 'completed', deliverables: [image] },
      { id: 'image-edit', title: 'Image edit', agent: 'image', tier: 'direct', revision: 1, status: 'in_progress', deliverables: [{ id: 'edit-output', title: 'Edited blue square', kind: 'image', type: 'image', status: 'generating', parent_deliverable_id: 'output' }] },
      { id: 'video', title: 'Video generation', agent: 'video', tier: 'direct', revision: 1, status: 'in_progress', deliverables: [{ id: 'video-output', title: 'Video output', kind: 'video', status: 'generating' }] },
      { id: 'audio', title: 'Audio generation', agent: 'audio', revision: 1, status: 'failed', last_error: 'Audio generation failed' },
    ]
    const writes: string[] = []; let failReads = false
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      if (url.pathname === '/fixture.png') return route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256"><rect width="256" height="256" fill="#3b82f6"/></svg>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      if (url.pathname === '/v3/projects') return route.fulfill({ json: { projects: [project, second] } })
      if (url.pathname === '/v1/account/avatar') return route.fulfill({ json: { image: '', user_id: url.searchParams.get('user_id'), account_scope_id: url.searchParams.get('account_scope_id') } })
      if (req.method() === 'GET' && url.pathname.endsWith('/sessions')) return route.fulfill({ json: { sessions: [] } })
      if (url.pathname === `/v3/projects/${second.id}`) return route.fulfill({ json: { project: second } })
      if (url.pathname === `/v3/projects/${project.id}/designs`) return route.fulfill({ json: { designs: url.searchParams.get('view') === 'archived' ? [] : [{ project_id: project.id, title: 'Design generation', request: { id: 'design-request', parent_session_id: 'design-session', state: 'running', candidates: [{ spec: { artifact_id: 'design-output', kind: 'html' }, state: 'running' }] } }], next_cursor: '' } })
      if (url.pathname.startsWith(`/v3/projects/${project.id}/tasks/`)) return route.fulfill({ json: { task: rows.find(row => url.pathname.endsWith('/' + row.id)) } })
      if (url.pathname === `/v3/projects/${project.id}/tasks`) {
        if (failReads) return route.fulfill({ status: 503, json: { error: 'Media read unavailable' } })
        return route.fulfill({ json: { tasks: url.searchParams.get('view') === 'archived' ? [{ ...rows[1], id: 'old-image', title: 'Archived image' }, { ...rows[0], id: 'old-code', title: 'Archived code' }] : rows } })
      }
      if (url.pathname.startsWith(`/v3/projects/${second.id}/`)) return route.fulfill({ json: { tasks: [], media: [], designs: [] } })
      if (req.method() !== 'GET') { writes.push(`${req.method()} ${url.pathname}`); return route.fulfill({ status: 400, json: { error: 'Unexpected mutation' } }) }
      const data = fixtureRead(url, 'populated')
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Unconfigured fixture read' } })
    })
    const mount = async () => { await page.addStyleTag({ content: css }); await page.addScriptTag({ content: bundle.outputFiles[0].text }) }
    await page.goto(`https://media-navigation.test/projects/${project.id}`); await mount()
    const main = page.locator('main.swarm-main-panel')
    await main.getByText('Code with image attachment', { exact: true }).waitFor()
    assert.equal(await main.getByTestId('media-task-card').count(), 0)
    assert.equal(await main.getByRole('group', { name: 'Task status filter' }).innerText().then(text => text.replace(/\s/g, '')), 'All1Running0Review0Queued1Done0Archived')
    const tabs = page.getByRole('tablist', { name: 'Project sidebar views' })
    await tabs.getByRole('tab', { name: 'Media', exact: true }).click()
    const media = page.getByRole('tabpanel', { name: 'Media', exact: true })
    await media.getByRole('heading', { name: 'Image generation', exact: true }).waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 4, 'image edit is one retained thread, not a duplicate card')
    assert.equal(await media.getByText('Code with image attachment', { exact: true }).count(), 0)
    await media.getByRole('heading', { name: 'Design generation', exact: true }).waitFor()
    await media.getByTestId('media-task-card').filter({ hasText: 'Video generation' }).getByRole('status', { name: 'Turn 1: running' }).waitFor()
    const thread = media.getByTestId('media-task-card').filter({ hasText: 'Image generation' })
    await thread.getByRole('tab', { name: /Turn 1/ }).click()
    await thread.getByRole('button', { name: 'Preview / save', exact: true }).waitFor()
    await thread.evaluate(element => { (window as any).retainedThread = element })
    await tabs.getByRole('tab', { name: 'Chat', exact: true }).click()
    await tabs.getByRole('tab', { name: 'Chat', exact: true }).press('ArrowRight')
    assert.equal(await thread.evaluate(element => element === (window as any).retainedThread), true)
    assert.equal(await thread.getByRole('tab', { name: /Turn 1/ }).getAttribute('aria-selected'), 'true')
    rows[3].status = 'failed'; Object.assign(rows[3], { revision: 2, last_error: 'Video provider failure', deliverables: [{ id: 'video-output', title: 'Video output', kind: 'video', status: 'failed' }] })
    await page.evaluate(() => (window as any).mediaEvent('video', 2))
    await media.getByRole('alert').filter({ hasText: 'Video provider failure' }).waitFor()
    assert.equal(await main.getByTestId('media-task-card').count(), 0)
    assert.equal(await media.getByTestId('media-task-card').count(), 4)
    await main.getByRole('button', { name: 'Archived', exact: true }).click()
    const archived = page.getByRole('dialog', { name: 'Archived tasks', exact: true })
    await archived.getByText('Archived code', { exact: true }).waitFor()
    assert.equal(await archived.getByText('Archived image', { exact: true }).count(), 0)
    await archived.getByRole('button', { name: 'Close archived tasks' }).click()
    await media.getByRole('button', { name: 'Archived media', exact: true }).click()
    await media.getByRole('region', { name: 'Archived media' }).getByRole('heading', { name: 'Archived image', exact: true }).waitFor()
    assert.equal(await media.getByText('Archived code', { exact: true }).count(), 0)
    await page.getByLabel('Current project').selectOption(second.id)
    await tabs.getByRole('tab', { name: 'Media', exact: true }).click()
    await media.getByText('No image, video or audio generations yet.').waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 0)
    await page.getByLabel('Current project').selectOption(project.id)
    await tabs.getByRole('tab', { name: 'Media', exact: true }).click()
    await media.getByRole('heading', { name: 'Image generation', exact: true }).waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 4)
    failReads = true
    await page.evaluate(() => (window as any).refreshMedia())
    await media.getByRole('button', { name: 'Retry media', exact: true }).waitFor()
    failReads = false
    await media.getByRole('button', { name: 'Retry media', exact: true }).click()
    await media.getByRole('button', { name: 'Retry media', exact: true }).waitFor({ state: 'hidden' })
    await page.reload(); await mount()
    await tabs.getByRole('tab', { name: 'Media', exact: true }).click()
    await media.getByRole('heading', { name: 'Image generation', exact: true }).waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 4)
    assert.equal(await thread.getByRole('tab').count(), 2)
    for (const width of [1440, 375]) {
      await page.setViewportSize({ width, height: 1000 })
      if (width === 375) await page.getByRole('button', { name: 'Chat', exact: true }).click()
      await media.getByRole('heading', { name: 'Project media' }).waitFor()
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
      if (process.env.SWARM_MEDIA_NAV_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.SWARM_MEDIA_NAV_SCREENSHOT_DIR}/media-navigation-${width}.png` })
    }
    assert.deepEqual(writes, [], 'browsing, retrying reads and reloading must not generate or mutate records')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
