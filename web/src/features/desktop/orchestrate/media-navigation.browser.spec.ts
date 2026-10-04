import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { projectTestStyles } from './project-test-styles'
import { fixtureRead, project, snapshot } from './swarm-responsive-browser-fixtures'

// Purpose: OrchestrateView must classify navigation using durable task.agent, not
// titles, attachments or outputs. Exercise the real project runtime and Tasks row:
// all task counts exclude media, code-with-media stays visible, tab switching keeps
// selected turns, project changes/reloads reconstruct history, events update the
// same cards, and read retries never dispatch generation. HTTP fixtures isolate
// this UI boundary; this is not live daemon/provider or persistence evidence.
// Creation uses the real modal and a retained-slot HTTP response, never paid media.
test('Media shares the Tasks row and main content across filters, events, projects and reload', { timeout: 90_000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,RouterProvider} from '@tanstack/react-router';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {ensureDesktopSession} from './src/app/api';
    import {OrchestratePage} from './src/features/desktop/orchestrate/orchestrate-page';
    import {desktopProjects} from './src/features/desktop/runtime/desktop-projects';
    import {desktopDesigns} from './src/features/desktop/runtime/desktop-design-runtime';
    async function mount() {
      await ensureDesktopSession();
      const root=createRootRoute({component:()=> <OrchestratePage workspaceSlug='fixture'/>});
      const routes=['/projects','/projects/$projectId','/projects/$projectId/sessions/$sessionId'].map(path=>createRoute({getParentRoute:()=>root,path,validateSearch:s=>s}));
      const router=createRouter({routeTree:root.addChildren(routes)});
      window.refreshMedia=()=>desktopProjects.invalidate('${project.id}');
      window.refreshDesigns=()=>desktopDesigns.project('${project.id}').refresh();
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
    const design = { project_id: project.id, title: 'Design generation', request: { id: 'design-request', parent_session_id: 'design-session', state: 'running', candidates: [{ spec: { artifact_id: 'design-output', kind: 'html' }, state: 'running' }] } }
    const designWrites: any[] = []; let failDesign = ''
    const writes: string[] = []; let failReads = false; let allowCreation = false
    const archivedIDs = new Set<string>(); let failArchive = ''; let holdArchive: (() => void) | undefined; let delayArchive = false; let archiveReached: (() => void) | undefined
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      if (url.pathname === '/fixture.png') return route.fulfill({ contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256"><rect width="256" height="256" fill="#3b82f6"/></svg>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      // The modal's existing plan preview is a read, not generation admission.
      if (allowCreation && req.method() === 'POST' && url.pathname === `/v3/projects/${project.id}/tasks:preview`) return route.fulfill({ json: { task_plan: {}, model_preview: {}, workspace_diagnostic: 'Fixture has no workspace' } })
      if (req.method() === 'POST' && url.pathname.startsWith('/v3/sessions/design-session/designs/artifacts/')) {
        const body = req.postDataJSON()
        if (body.action === 'preview_html') return route.fulfill({ contentType: 'text/html', body: '<p>Design preview</p>' })
        assert.equal(body.action, 'archive')
        designWrites.push(body)
        if (body.ref.artifact_id === failDesign) return route.fulfill({ status: 503, json: { error: 'Design archive unavailable' } })
        const candidate = design.request.candidates.find(row => row.spec.artifact_id === body.ref.artifact_id)!
        Object.assign(candidate, { archived: true, archive_version: body.expected_version + 1 })
        return route.fulfill({ json: { artifact: { id: body.ref.artifact_id, archived: true, archive_version: body.expected_version + 1 } } })
      }
      if (req.method() !== 'GET') {
        writes.push(`${req.method()} ${url.pathname}`)
        if (allowCreation && req.method() === 'POST' && url.pathname === `/v3/projects/${project.id}/tasks`) {
          const body = req.postDataJSON()
          assert.equal(body.agent, 'image')
          const created = { id: body.id, title: 'New image generation', agent: 'image', tier: 'direct', revision: 1, status: 'in_progress', deliverables: [{ id: 'new-image-slot', title: 'New image', kind: 'image', status: 'generating' }] }
          rows.push(created)
          return route.fulfill({ json: { task: created } })
        }
        const archive = url.pathname.match(/\/tasks\/([^/]+)\/archive$/)
        if (archive && req.method() === 'POST') {
          const row = rows.find(row => row.id === archive[1])!
          assert.equal(req.postDataJSON().revision, row.revision)
          if (row.id === failArchive) return route.fulfill({ status: 503, json: { error: 'Archive temporarily unavailable' } })
          const finish = () => { archivedIDs.add(row.id); row.revision++; void route.fulfill({ json: { task: { ...row, archived: true } } }) }
          if (delayArchive) { holdArchive = finish; archiveReached?.(); return }
          finish(); return
        }
        return route.fulfill({ status: 400, json: { error: 'Unexpected mutation' } })
      }
      if (url.pathname === '/v3/projects') return route.fulfill({ json: { projects: [project, second] } })
      if (url.pathname === '/v1/account/avatar') return route.fulfill({ json: { image: '', user_id: url.searchParams.get('user_id'), account_scope_id: url.searchParams.get('account_scope_id') } })
      if (req.method() === 'GET' && url.pathname.endsWith('/sessions')) return route.fulfill({ json: { sessions: [] } })
      if (url.pathname === `/v3/projects/${second.id}`) return route.fulfill({ json: { project: second } })
      if (url.pathname === `/v3/projects/${project.id}/designs`) return route.fulfill({ json: { designs: url.searchParams.get('view') === 'archived' ? [] : [design], next_cursor: '' } })
      if (url.pathname.startsWith(`/v3/projects/${project.id}/tasks/`)) return route.fulfill({ json: { task: (() => { const row = rows.find(row => url.pathname.endsWith('/' + row.id)); return row && { ...row, archived: archivedIDs.has(row.id) } })() } })
      if (url.pathname === `/v3/projects/${project.id}/tasks`) {
        if (failReads) return route.fulfill({ status: 503, json: { error: 'Media read unavailable' } })
        return route.fulfill({ json: { tasks: url.searchParams.get('view') === 'archived' ? [{ ...rows[1], id: 'old-image', title: 'Archived image' }, { ...rows[0], id: 'old-code', title: 'Archived code' }, ...rows.filter(row => archivedIDs.has(row.id))] : rows.filter(row => !archivedIDs.has(row.id)) } })
      }
      if (url.pathname.startsWith(`/v3/projects/${second.id}/`)) return route.fulfill({ json: { tasks: [], media: [], designs: [] } })
      const data = fixtureRead(url, 'populated')
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Unconfigured fixture read' } })
    })
    const mount = async () => { await page.addStyleTag({ content: css }); await page.addScriptTag({ content: bundle.outputFiles[0].text }) }
    await page.goto(`https://media-navigation.test/projects/${project.id}`); await mount()
    const main = page.locator('main.swarm-main-panel')
    await main.getByText('Code with image attachment', { exact: true }).waitFor()
    assert.equal(await main.locator('[data-testid="media-task-card"]:visible').count(), 0)
    assert.equal(await main.getByRole('group', { name: 'Task status filter' }).innerText().then(text => text.replace(/\s/g, '')), 'All1Running0Review0Queued1Done0Media4Archived')
    const tabs = main.getByRole('group', { name: 'Task status filter' })
    assert.deepEqual(await tabs.getByRole('button').allTextContents(), ['All1', 'Running0', 'Review0', 'Queued1', 'Done0', 'Media4', 'Archived'])
    assert.equal(await page.getByRole('tablist', { name: 'Project sidebar views' }).count(), 0)
    assert.equal(await page.locator('.swarm-project-right-sidebar, #project-sidebar-media, #project-sidebar-chat').count(), 0)
    assert.equal(await page.locator('.swarm-conversation-panel').count(), 1, 'original Chat panel remains')
    for (const name of ['Running0', 'Review0', 'Queued1', 'Done0', 'All1']) {
      await tabs.getByRole('button', { name: new RegExp(`^${name.slice(0, -1)}\\s*${name.slice(-1)}$`) }).click()
      assert.equal(await main.locator('[data-testid="media-task-card"]:visible').count(), 0)
      assert.equal(await main.getByText('Code with image attachment', { exact: true }).count(), ['All1', 'Queued1'].includes(name) ? 1 : 0)
    }
    await tabs.getByRole('button', { name: /^Media\s*\d/ }).click()
    const media = main.getByRole('region', { name: 'Project media', exact: true })
    assert.equal(await tabs.getByRole('button', { name: /^Media\s*\d/ }).getAttribute('aria-pressed'), 'true')
    assert.equal(await main.getByTestId('orchestrate-task-list').count(), 0)
    assert.equal(await page.locator('.swarm-conversation-panel').getByTestId('media-task-card').count(), 0)
    await media.getByRole('heading', { name: 'Image generation', exact: true }).waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 4, 'image edit is one retained thread, not a duplicate card')
    assert.equal(await media.getByText('Code with image attachment', { exact: true }).count(), 0)
    await media.getByRole('heading', { name: 'Design generation', exact: true }).waitFor()
    await media.getByTestId('media-task-card').filter({ hasText: 'Video generation' }).getByRole('status', { name: 'Turn 1: running' }).waitFor()
    const thread = media.getByTestId('media-task-card').filter({ hasText: 'Image generation' })
    await thread.getByRole('tab', { name: /Turn 1/ }).click()
    await thread.getByRole('button', { name: 'Preview / save', exact: true }).waitFor()
    await thread.evaluate(element => { (window as any).retainedThread = element })
    await tabs.getByRole('button', { name: /^All\s*1$/ }).click()
    assert.equal(await media.isVisible(), false)
    await tabs.getByRole('button', { name: /^Media\s*\d/ }).click()
    assert.equal(await thread.evaluate(element => element === (window as any).retainedThread), true)
    assert.equal(await thread.getByRole('tab', { name: /Turn 1/ }).getAttribute('aria-selected'), 'true')
    rows[3].status = 'failed'; Object.assign(rows[3], { revision: 2, last_error: 'Video provider failure', deliverables: [{ id: 'video-output', title: 'Video output', kind: 'video', status: 'failed' }] })
    await page.evaluate(() => (window as any).mediaEvent('video', 2))
    await media.getByRole('alert').filter({ hasText: 'Video provider failure' }).waitFor()
    assert.equal(await main.locator('[data-testid="media-task-card"]:visible').count(), 4)
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
    await tabs.getByRole('button', { name: /^Media\s*\d/ }).click()
    await media.getByText('No image, video or audio generations yet.').waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 0)
    await page.getByLabel('Current project').selectOption(project.id)
    await tabs.getByRole('button', { name: /^Media\s*\d/ }).click()
    await media.getByRole('heading', { name: 'Image generation', exact: true }).waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 4)
    failReads = true
    await page.evaluate(() => (window as any).refreshMedia())
    await media.getByRole('button', { name: 'Retry media', exact: true }).waitFor()
    failReads = false
    await media.getByRole('button', { name: 'Retry media', exact: true }).click()
    await media.getByRole('button', { name: 'Retry media', exact: true }).waitFor({ state: 'hidden' })
    await page.reload(); await mount()
    await tabs.getByRole('button', { name: /^Media\s*\d/ }).click()
    await media.getByRole('heading', { name: 'Image generation', exact: true }).waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 4)
    assert.equal(await thread.getByRole('tab').count(), 2)
    for (const width of [1440, 375]) {
      await page.setViewportSize({ width, height: 1000 })
      await media.getByRole('heading', { name: 'Project media' }).waitFor()
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
      if (process.env.SWARM_MEDIA_NAV_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.SWARM_MEDIA_NAV_SCREENSHOT_DIR}/media-navigation-${width}.png` })
    }
    assert.deepEqual(writes, [], 'browsing, retrying reads and reloading must not generate or mutate records')
    // New creation must land in this same main-content view, including on mobile.
    allowCreation = true
    await tabs.getByRole('button', { name: /^All\s*1$/ }).click()
    await main.getByRole('button', { name: 'New task', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Deploy Autonomous Task' })
    await dialog.getByTestId('deploy-tab-image').click()
    await dialog.getByRole('textbox').fill('New image generation')
    await dialog.getByRole('button', { name: 'Deploy & Start', exact: true }).click()
    await dialog.waitFor({ state: 'hidden' })
    await media.getByRole('heading', { name: 'New image generation', exact: true }).waitFor()
    assert.equal(await tabs.getByRole('button', { name: /^Media\s*\d/ }).getAttribute('aria-pressed'), 'true')
    assert.equal(await main.isVisible(), true)
    assert.equal(await page.locator('.swarm-conversation-panel').isVisible(), false, 'mobile creation must not open Chat')
    assert.equal(await media.getByTestId('media-task-card').count(), 5)
    await tabs.getByRole('button', { name: /^All\s*1$/ }).click()
    await tabs.getByRole('button', { name: /^Media\s*\d/ }).click()
    await page.reload(); await mount()
    await tabs.getByRole('button', { name: /^Media\s*\d/ }).click()
    await media.getByRole('heading', { name: 'New image generation', exact: true }).waitFor()
    assert.equal(await media.getByTestId('media-task-card').count(), 5)
    assert.deepEqual(writes, [`POST /v3/projects/${project.id}/tasks`], 'creation dispatches once; navigation and reload never recreate media')
    // Purpose: real card selection + canonical archive receipts must never expand
    // into code tasks, hidden archived rows, or turns arriving after selection.
    // This browser boundary proves UI effects and API payloads, not daemon durability.
    await page.setViewportSize({ width: 1440, height: 1000 })
    const management = media.locator('[aria-label="Media management"]')
    const selectImage = () => media.getByRole('checkbox', { name: 'Select media card: Image generation', exact: true })
    await selectImage().check()
    await tabs.getByRole('button', { name: /^All\s*1$/ }).click()
    await tabs.getByRole('button', { name: /^Media\s*5$/ }).click()
    assert.equal(await selectImage().isChecked(), false, 'leaving Media clears archive selection but retains turn navigation')
    await selectImage().check()
    await media.getByRole('button', { name: 'Archived media', exact: true }).click()
    assert.equal(await selectImage().isChecked(), false, 'archive scope changes clear selection')
    assert.equal(await media.getByRole('region', { name: 'Archived media' }).getByRole('checkbox').count(), 0)
    await media.getByRole('button', { name: 'Archived media', exact: true }).click()
    await selectImage().check()
    await page.getByLabel('Current project').selectOption(second.id)
    await tabs.getByRole('button', { name: /^Media\s*0$/ }).click()
    await page.getByLabel('Current project').selectOption(project.id)
    await tabs.getByRole('button', { name: /^Media\s*5$/ }).click()
    assert.equal(await selectImage().isChecked(), false, 'project selection never leaks')
    await management.getByRole('button', { name: 'Select all', exact: true }).click()
    assert.match(await management.innerText(), /4 of 5 cards selected · 5 records/, 'unfinished design is counted but not archiveable')
    await management.getByRole('button', { name: 'Clear selection', exact: true }).click()
    await selectImage().check()
    await media.getByRole('checkbox', { name: 'Select media card: Audio generation', exact: true }).check()
    assert.match(await management.innerText(), /2 of 5 cards selected · 3 records/)
    for (const width of [1440, 375]) {
      await page.setViewportSize({ width, height: 1000 })
      await management.scrollIntoViewIfNeeded()
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
      if (process.env.SWARM_MEDIA_NAV_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.SWARM_MEDIA_NAV_SCREENSHOT_DIR}/media-selection-${width}.png` })
    }
    await page.setViewportSize({ width: 1440, height: 1000 })
    // A later child in the selected thread must stay unselected and active.
    rows.push({ id: 'late-image', title: 'Later image', agent: 'image', revision: 1, status: 'completed', deliverables: [{ ...image, id: 'late-output', parent_deliverable_id: 'edit-output' }] } as typeof rows[number])
    await page.evaluate(() => (window as any).mediaEvent('late-image', 1))
    await thread.getByRole('tab', { name: /Turn 3/ }).waitFor()
    assert.equal(await selectImage().evaluate((node: HTMLInputElement) => node.indeterminate), true)
    assert.match(await management.innerText(), /2 of 5 cards selected · 3 records/)
    const beforeArchive = writes.length
    failArchive = 'audio'
    await management.getByRole('button', { name: 'Archive selected', exact: true }).click()
    await media.getByRole('status').filter({ hasText: '2 records archived, 1 failed.' }).waitFor()
    assert.deepEqual(writes.slice(beforeArchive).sort(), ['image', 'image-edit', 'audio'].map(id => `POST /v3/projects/${project.id}/tasks/${id}/archive`).sort())
    assert.deepEqual([...archivedIDs].sort(), ['image', 'image-edit'])
    await tabs.getByRole('button', { name: /^Media\s*5$/ }).waitFor()
    assert.match(await management.innerText(), /1 of 5 cards selected · 1 records/)
    assert.equal(await media.getByRole('checkbox', { name: 'Select media card: Audio generation', exact: true }).isChecked(), true)
    assert.equal(await media.getByRole('checkbox', { name: 'Select media card: Later image', exact: true }).isChecked(), false)
    failArchive = ''; delayArchive = true
    const heldRequest = new Promise<void>(resolve => { archiveReached = resolve })
    await management.getByRole('button', { name: 'Archive selected', exact: true }).click()
    await management.getByRole('button', { name: 'Archiving…', exact: true }).waitFor()
    assert.equal(await management.getByRole('button', { name: 'Archiving…', exact: true }).isDisabled(), true)
    assert.equal(await management.getByRole('button', { name: 'Select all', exact: true }).isDisabled(), true)
    await heldRequest
    assert.ok(holdArchive); holdArchive!(); delayArchive = false
    await media.getByRole('status').filter({ hasText: '1 records archived, 0 failed.' }).waitFor()
    await tabs.getByRole('button', { name: /^Media\s*4$/ }).waitFor()
    assert.equal(writes.length, beforeArchive + 4, 'retry only sends the failed record')
    await management.getByRole('button', { name: 'Select all', exact: true }).click()
    assert.match(await management.innerText(), /3 of 4 cards selected · 3 records/)
    const beforeAll = writes.length
    await management.getByRole('button', { name: 'Archive selected', exact: true }).click()
    await media.getByRole('status').filter({ hasText: '3 records archived, 0 failed.' }).waitFor()
    await tabs.getByRole('button', { name: /^Media\s*1$/ }).waitFor()
    assert.equal(writes.length, beforeAll + 3)
    assert.equal(archivedIDs.has('code'), false)
    assert.equal(archivedIDs.has('old-image'), false)
    // Design archival uses exact artifact references, versions and a stable retry
    // key; hidden archived candidates must never be added by select-all.
    design.request.state = 'succeeded'
    design.request.candidates = ['design-one', 'design-two', 'design-hidden'].map((id, index) => ({
      spec: { artifact_id: id, kind: 'html' }, state: 'succeeded', archived: index === 2, archive_version: 4,
      attempts: [{ number: 1, state: 'succeeded', result: { artifact_id: id, revision: 1, sha256: id } }],
    }))
    await page.evaluate(() => (window as any).refreshDesigns())
    const designCard = media.getByTestId('media-task-card').filter({ hasText: 'Design generation' })
    await designCard.getByRole('checkbox').waitFor({ state: 'visible' })
    await management.getByRole('button', { name: 'Select all', exact: true }).click()
    assert.match(await management.innerText(), /1 of 1 cards selected · 2 records/)
    failDesign = 'design-two'
    await management.getByRole('button', { name: 'Archive selected', exact: true }).click()
    await media.getByRole('status').filter({ hasText: '1 records archived, 1 failed.' }).waitFor()
    assert.deepEqual(designWrites.map(body => body.ref.artifact_id).sort(), ['design-one', 'design-two'])
    assert.ok(designWrites.every(body => body.expected_version === 4 && body.ref.revision === 1))
    assert.match(await management.innerText(), /1 of 1 cards selected · 1 records/)
    failDesign = ''
    await management.getByRole('button', { name: 'Archive selected', exact: true }).click()
    await tabs.getByRole('button', { name: /^Media\s*0$/ }).waitFor()
    assert.equal(designWrites.length, 3)
    assert.equal(designWrites[2].idempotency_key, designWrites.find(body => body.ref.artifact_id === 'design-two').idempotency_key)
    assert.equal(await media.getByTestId('media-task-card').count(), 0)
    await tabs.getByRole('button', { name: /^All\s*1$/ }).click()
    await main.getByText('Code with image attachment', { exact: true }).waitFor()
    const taskManagement = main.locator('[aria-label="Task management"]')
    await taskManagement.getByRole('button', { name: 'Select all', exact: true }).click()
    assert.match(await taskManagement.innerText(), /1 of 1 selected/)
    await taskManagement.getByRole('button', { name: 'Clear', exact: true }).click()
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
