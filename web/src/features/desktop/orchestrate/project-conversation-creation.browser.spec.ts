import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: exercise createProjectConversation -> canonical cache -> mounted
// useProjectConversations -> real sidebar links, without remount/retry. Controlled
// HTTP responses prove the initial-read race and failed admission; this browser
// integration is not daemon/provider evidence and does not replace backend tests.
test('created conversations appear immediately while an older summary read is in flight', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,createMemoryHistory,RouterProvider,useRouterState} from '@tanstack/react-router';
    import {ProjectConversationSidebar} from './src/features/desktop/orchestrate/project-conversation-sidebar';
    import {useProjectConversations} from './src/features/desktop/runtime/project-conversations';
    import {createProjectConversation} from './src/features/desktop/orchestrate/project-conversations';
    window.mounts=0;
    function App(){const list=useProjectConversations('p');const path=useRouterState({select:s=>s.location.pathname});const [error,setError]=useState('');
      React.useEffect(()=>{window.mounts++},[]);window.refresh=list.refresh;
      window.create=async()=>{try{const id=await createProjectConversation('p','request');setError('');return id}catch(e){setError(e.message);return null}};
      return <><span data-error>{error}</span><ProjectConversationSidebar projectId='p' projectName='Project' selectedId={path.split('/').at(-1)} sessions={list.sessions} loading={list.loading} creating={false} error={list.error} onCreate={()=>window.create()} onRetry={list.refresh} onSelect={()=>{}}/></>}
    const root=createRootRoute({component:App});const routeTree=root.addChildren(['/projects/$projectId','/projects/$projectId/sessions/$sessionId'].map(path=>createRoute({getParentRoute:()=>root,path})));
    window.router=createRouter({routeTree,history:createMemoryHistory({initialEntries:['/projects/p']})});
    createRoot(document.getElementById('root')).render(<RouterProvider router={window.router}/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    let releaseSummary: (() => Promise<void>) | undefined
    let summaryStarted!: () => void
    const summaryRequest = new Promise<void>(resolve => { summaryStarted = resolve })
    let reads = 0
    let created = 'one'
    let failure = false
    let foreign = false
    let mismatched = false
    let unconfirmed = false
    const session = (id: string) => ({ id, title: id, workspace_path: '', workspace_name: '', mode: 'auto', created_at: id === 'one' ? 10 : 20, updated_at: 20, message_count: 0, last_message_at: 0, metadata: { project_id: foreign ? 'other' : 'p', agent_name: 'system-orchestrator' } })
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.pathname === '/v1/auth/desktop/session') return route.fulfill({ json: { user_id: 'user', account_scope_id: 'account' } })
      if (url.pathname === '/v3/projects/p/sessions') {
        if (route.request().method() === 'POST') return route.fulfill(failure ? { status: 500, json: { error: 'creation failed' } } : { json: { ok: !unconfirmed, session_id: mismatched ? 'wrong' : created, session: session(created), projection: { session_id: created, last_seq: 1 }, mutation: {}, realtime_outbox: null } })
        reads++
        if (reads === 1) { releaseSummary = () => route.fulfill({ json: { sessions: [] } }); summaryStarted(); return }
        return route.fulfill({ json: { sessions: ['one', 'two'].map(id => ({ session: session(id) })) } })
      }
      return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
    })
    await page.goto('https://conversation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await summaryRequest
    await page.getByRole('button', { name: 'New session', exact: true }).waitFor()
    // Wait for the actual held request, not elapsed time.
    await page.waitForFunction(() => document.querySelector('[role="status"]') !== null)
    assert.equal(await page.evaluate(() => (window as any).create()), 'one')
    const nav = page.getByRole('navigation', { name: 'Conversation sessions' })
    await nav.getByRole('link', { name: 'one Project' }).waitFor()
    assert.ok(releaseSummary)
    await releaseSummary()
    await page.getByRole('status').waitFor({ state: 'hidden' })
    assert.equal(await nav.getByRole('link').count(), 1)
    created = 'two'
    assert.equal(await page.evaluate(() => (window as any).create()), 'two')
    await nav.getByRole('link', { name: 'two Project' }).waitFor()
    assert.deepEqual(await nav.getByRole('link').evaluateAll(links => links.map(link => link.getAttribute('href'))), ['/projects/p/sessions/two', '/projects/p/sessions/one'])
    await nav.getByRole('link', { name: 'two Project' }).click()
    await page.waitForFunction(() => (window as any).router.state.location.pathname.endsWith('/two'))
    await page.evaluate(() => (window as any).create()) // idempotent replay
    const refetched = page.waitForResponse(response => response.url().includes('/v3/projects/p/sessions?'))
    await page.evaluate(() => (window as any).refresh())
    await refetched
    await page.getByRole('status').waitFor({ state: 'hidden' })
    assert.equal(await nav.getByRole('link').count(), 2)
    assert.match(await nav.locator('[aria-current="page"]').innerText(), /two/)
    failure = true
    assert.equal(await page.evaluate(() => (window as any).create()), null)
    failure = false; foreign = true; created = 'foreign'
    assert.equal(await page.evaluate(() => (window as any).create()), null)
    foreign = false; mismatched = true
    assert.equal(await page.evaluate(() => (window as any).create()), null)
    mismatched = false; unconfirmed = true
    assert.equal(await page.evaluate(() => (window as any).create()), null)
    assert.equal(await nav.getByRole('link').count(), 2)
    assert.equal(await page.evaluate(() => (window as any).mounts), 1)
  } finally { await browser.close() }
})
