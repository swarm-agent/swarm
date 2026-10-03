import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: render the real shared ProjectCreationFlow and its HTTP runtime. First
// and later project creation must select registered mixed folders, confirm before
// writing, wait for generation, and recover from add/create/provider failures and
// navigation without duplicate writes or early chat. Browser interaction is the
// narrowest layer for button ordering, retained errors and unmount navigation.
// HTTP fixtures are deterministic contract evidence, not live AI evidence.
test('shared project creation recovers failures, remounts and opens chat only after ready', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {ProjectCreationFlow} from './src/features/desktop/orchestrate/project-creation-flow';
    import {desktopProjects} from './src/features/desktop/runtime/desktop-projects';
    const w=window; let root=createRoot(document.getElementById('root'));
    w.saved=null; w.opened=[]; w.cancelled=0;
    w.mount=(resume=false)=>{root.render(<ProjectCreationFlow key={Math.random()} project={resume?w.saved:undefined} onSaved={p=>w.saved=p} onOpen={(p,s)=>w.opened.push([p.id,s])} onCancel={()=>{w.cancelled++;root.render(null)}}/>)};
    w.updated=()=>desktopProjects.acceptFrame({kind:'project.updated',project_id:'example'});
    w.mount();` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } }); page.setDefaultTimeout(5000)
    const posts: Array<{ path: string; body: any }> = [], errors: string[] = []
    let addFails = true, createFails = true, status = 'running', attempt = 1
    const project = () => ({ id: 'example', name: 'Example', workspaces: [{ workspace_id: 'docs', path: '/fixture/docs', label: 'Docs', role: 'auxiliary' }], project_context: status === 'ready' ? '# Fixture generated context' : '', context_generation: { status, attempt, error: status === 'failed' ? 'Provider unavailable; check credentials' : '' } })
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/*', async route => {
      const req = route.request(), path = new URL(req.url()).pathname
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      if (req.method() === 'POST') posts.push({ path, body: req.postDataJSON() })
      if (path === '/v1/auth/desktop/session') return route.fulfill({ json: { ok: true, user_id: 'fixture', account_scope_id: 'fixture' } })
      if (path === '/v1/workspace/list') return route.fulfill({ json: { workspaces: [{ id: 'code', path: '/fixture/code', name: 'Git code' }] } })
      if (path === '/v1/workspace/add') return route.fulfill(addFails ? { status: 403, json: { error: 'Folder inaccessible' } } : { json: { workspace: { workspace_id: 'docs', resolved_path: '/fixture/docs', workspace_name: 'Non-Git docs' } } })
      if (path === '/v3/projects' && req.method() === 'POST') return route.fulfill(createFails ? { status: 503, json: { error: 'Creation response unavailable' } } : { json: { project: project() } })
      if (path === '/v3/projects/example') return route.fulfill({ json: { project: project() } })
      if (path.endsWith('/context:retry')) { status = 'running'; attempt++; return route.fulfill({ json: { project: project() } }) }
      if (path.endsWith('/sessions') && req.method() === 'POST') return route.fulfill({ json: { session_id: 'conversation', session: { id: 'conversation', metadata: { project_id: 'example', agent_name: 'system-orchestrator' } } } })
      return route.fulfill({ status: 501, json: { error: 'Unexpected fixture request ' + path } })
    })
    await page.goto('https://creation.test'); await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByLabel('Project name', { exact: true }).fill('Example')
    await page.getByLabel('Git code').check()
    await page.getByLabel('Add folder path').fill('/fixture/docs')
    await page.getByRole('button', { name: 'Add folder', exact: true }).click()
    await page.getByRole('alert').filter({ hasText: 'Folder inaccessible' }).waitFor()
    assert.equal(await page.getByLabel('Non-Git docs').count(), 0)
    addFails = false
    await page.getByRole('button', { name: 'Retry adding folder' }).click()
    await page.getByLabel('Non-Git docs').waitFor()
    assert.equal(await page.getByLabel('Non-Git docs').isChecked(), true)
    await page.getByRole('button', { name: 'Review project', exact: true }).click()
    assert.equal(posts.filter(p => p.path === '/v3/projects').length, 0)
    await page.getByRole('button', { name: 'Create project and generate context' }).click()
    await page.getByRole('alert').filter({ hasText: 'Creation response unavailable' }).waitFor()
    const original = posts.find(p => p.path === '/v3/projects')!.body
    assert.deepEqual(original.workspaces.map((w: any) => w.workspace_id), ['code', 'docs'])
    assert.equal(original.project_context, undefined)
    await page.getByRole('button', { name: 'Back to projects' }).click()
    await page.evaluate(() => (window as any).mount())
    createFails = false
    await page.getByRole('button', { name: 'Retry project creation' }).click()
    await page.getByRole('status').filter({ hasText: 'Generating project context' }).waitFor()
    assert.deepEqual(posts.filter(p => p.path === '/v3/projects').map(p => p.body), [original, original])
    assert.equal(posts.filter(p => p.path.endsWith('/sessions')).length, 0)
    status = 'failed'; await page.evaluate(() => (window as any).updated())
    await page.getByRole('alert').filter({ hasText: 'Provider unavailable' }).waitFor()
    await page.getByRole('button', { name: 'Back to projects' }).click()
    await page.evaluate(() => (window as any).mount(true))
    await page.getByRole('button', { name: 'Retry / resume generation' }).click()
    assert.equal(posts.filter(p => p.path === '/v3/projects').length, 2)
    await page.getByRole('status').filter({ hasText: 'Generating project context' }).waitFor()
    status = 'ready'; await page.evaluate(() => (window as any).updated())
    await page.getByRole('button', { name: 'Continue to project chat' }).click()
    await page.waitForFunction(() => (window as any).opened.length === 1)
    const chat = posts.find(p => p.path.endsWith('/sessions'))!.body
    assert.equal(chat.workspace_path, undefined); assert.equal(chat.worktree_name, undefined)
    assert.equal(chat.client_request_id, 'desktop-project-first:example')
    // Subsequent creation starts the same empty flow, not a different wizard.
    await page.evaluate(() => (window as any).mount())
    assert.equal(await page.getByLabel('Project name', { exact: true }).inputValue(), '')
    assert.equal(await page.getByRole('button', { name: 'Review project', exact: true }).isDisabled(), true)
    status = 'running'
    await page.getByLabel('Project name', { exact: true }).fill('Example')
    await page.getByLabel('Git code').check()
    await page.getByRole('button', { name: 'Review project', exact: true }).click()
    await page.getByRole('button', { name: 'Create project and generate context' }).click()
    await page.getByRole('status').filter({ hasText: 'Generating project context' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).opened.length), 1)
    status = 'ready'; await page.evaluate(() => (window as any).updated())
    await page.waitForFunction(() => (window as any).opened.length === 2)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
