import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: ProjectConversationSidebar is the visible project/session navigation
// boundary. Render the real TanStack links to prove canonical URLs, one selected
// row, Router titles/project metadata and retry/empty/loading/creation affordances.
// Browser DOM is the narrowest proof; this is not daemon or provider evidence.
test('project sidebar selects canonical session routes and exposes recoverable list states', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,createMemoryHistory,RouterProvider,useRouterState} from '@tanstack/react-router';
    import {ProjectConversationSidebar} from './src/features/desktop/orchestrate/project-conversation-sidebar';
    window.created=0;window.retried=0;
    function App(){const path=useRouterState({select:s=>s.location.pathname});const [state,setState]=useState({loading:false,creating:false,error:'',sessions:[{id:'one',title:'Router first title'},{id:'two',title:'Router second title'}]});window.update=setState;
      return <ProjectConversationSidebar projectId='project-a' projectName='Example project' selectedId={path.split('/').at(-1)} {...state}
        onCreate={()=>{window.created++;setState(s=>({...s,creating:true}))}} onRetry={()=>window.retried++} onSelect={()=>{}}/>}
    const root=createRootRoute({component:App});const routeTree=root.addChildren(['/projects','/projects/$projectId/sessions/$sessionId'].map(path=>createRoute({getParentRoute:()=>root,path})));
    window.router=createRouter({routeTree,history:createMemoryHistory({initialEntries:['/projects/project-a/sessions/one']})});
    createRoot(document.getElementById('root')).render(<RouterProvider router={window.router}/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://conversation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const nav = page.getByRole('navigation', { name: 'Conversation sessions' })
    await nav.waitFor()
    assert.equal(await nav.locator('[aria-current="page"]').count(), 1)
    assert.match(await nav.locator('[aria-current="page"]').innerText(), /Router first title/)
    assert.equal(await nav.getByText('Example project').count(), 2)
    const second = nav.getByRole('link', { name: 'Router second title Example project' })
    assert.equal(await second.getAttribute('href'), '/projects/project-a/sessions/two')
    await second.click()
    await page.waitForFunction(() => (window as any).router.state.location.pathname.endsWith('/two'))
    assert.equal(await nav.locator('[aria-current="page"]').count(), 1)
    assert.match(await nav.locator('[aria-current="page"]').innerText(), /Router second title/)
    await page.getByRole('button', { name: 'New session', exact: true }).click()
    assert.equal(await page.getByRole('button', { name: 'Creating…' }).isDisabled(), true)
    assert.equal(await page.evaluate(() => (window as any).created), 1)
    await page.evaluate(() => (window as any).update({ loading: true, creating: false, error: '', sessions: [] }))
    await page.getByRole('status').waitFor()
    assert.equal(await page.getByText('No conversations yet. Start a new session.').count(), 0)
    await page.evaluate(() => (window as any).update({ loading: false, creating: false, error: 'Unable to load sessions', sessions: [] }))
    await page.getByRole('alert').waitFor()
    await page.getByRole('button', { name: 'Retry sessions' }).click()
    assert.equal(await page.evaluate(() => (window as any).retried), 1)
    await page.evaluate(() => (window as any).update({ loading: false, creating: false, error: '', sessions: [] }))
    await page.getByText('No conversations yet. Start a new session.').waitFor()
  } finally { await browser.close() }
})
