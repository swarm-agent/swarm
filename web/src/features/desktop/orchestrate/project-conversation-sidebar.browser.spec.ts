import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'


// Purpose: ProjectConversationSidebar is the visible project/session navigation
// boundary. Render the real TanStack links to prove canonical URLs, one selected
// flat row, explicit-only selection, single-column destinations, Router titles/project
// metadata and retry/empty/loading/creation affordances.
// Browser DOM is the narrowest proof; this is not daemon or provider evidence.
test('project sidebar selects canonical session routes and exposes recoverable list states', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,createMemoryHistory,RouterProvider,useRouterState} from '@tanstack/react-router';
    import {ProjectConversationSidebar} from './src/features/desktop/orchestrate/project-conversation-sidebar';
    import {ProjectNavigation} from './src/features/desktop/orchestrate/project-navigation';
    import {swarmActivePage} from './src/features/desktop/orchestrate/swarm-navigation';
    window.created=0;window.retried=0;
    function App(){const location=useRouterState({select:s=>s.location});const path=location.pathname;const [state,setState]=useState({loading:false,creating:false,error:'',sessions:[{id:'one',title:'Router first title'},{id:'two',title:'Router second title'}]});window.update=setState;
      return <aside className='swarm-navigation-sidebar' style={{width:'100%',height:600,display:'flex',flexDirection:'column'}}><header>Example project</header><ProjectNavigation projectSegment='example-project' sessionId={path.split('/').at(-1)} activePage={swarmActivePage(location.search.section,location.search.workerId)} deliverableCount={0} mediaCount={0} onSelect={()=>{}}/><ProjectConversationSidebar projectId='example-project' projectName='Example project' selectedId={path.split('/').at(-1)} {...state}
        onCreate={()=>{window.created++;setState(s=>({...s,creating:true}))}} onRetry={()=>window.retried++} onSelect={()=>{}}/></aside>}
    const root=createRootRoute({component:App});const routeTree=root.addChildren(['/projects','/projects/$projectId/sessions/$sessionId'].map(path=>createRoute({getParentRoute:()=>root,path})));
    window.router=createRouter({routeTree,history:createMemoryHistory({initialEntries:['/projects/example-project/sessions/one']})});
    createRoot(document.getElementById('root')).render(<RouterProvider router={window.router}/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://conversation.test/')
    await page.addStyleTag({ content: `*{box-sizing:border-box}body{margin:0;background:#0d121f;color:#e2e8f0;font-family:sans-serif}h2,p{margin:0}a{color:inherit;text-decoration:none}button{background:transparent;border:0;color:inherit} :root{--swarm-text:#e2e8f0;--swarm-text-muted:#94a3b8;--swarm-border:#334155;--swarm-accent:#60a5fa;--swarm-surface-hover:#1e293b}` + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const nav = page.getByRole('navigation', { name: 'Conversation sessions' })
    await nav.waitFor()
    assert.equal(await nav.locator('[aria-current="page"]').count(), 1)
    assert.match(await nav.locator('[aria-current="page"]').innerText(), /Router first title/)
    assert.equal(await page.getByRole('heading', { name: 'Sessions', exact: true }).count(), 1)
    assert.equal(await page.getByRole('button', { name: 'Refresh sessions' }).count(), 0)
    assert.equal(await page.getByRole('checkbox').count(), 0)
    assert.equal(await nav.getByText('Example project', { exact: true }).count(), 2)
    const second = nav.getByRole('link', { name: 'Router second title Example project' })
    await second.hover()
    assert.equal(await page.getByRole('checkbox').count(), 0)
    await page.getByRole('button', { name: 'Select sessions', exact: true }).click()
    await page.getByRole('checkbox', { name: 'Select Router first title', exact: true }).check()
    assert.equal(await second.getAttribute('href'), '/projects/example-project/sessions/two')
    await second.click()
    await page.waitForFunction(() => (window as any).router.state.location.pathname.endsWith('/two'))
    assert.equal(await nav.locator('[aria-current="page"]').count(), 1)
    assert.match(await nav.locator('[aria-current="page"]').innerText(), /Router second title/)
    assert.equal(await page.getByRole('checkbox').count(), 0)
    // Requirement: compact real links must expose only one destination even with
    // a shared pathname, plus focusable, unclipped controls and dense long rows.
    const destinations = page.getByRole('navigation', { name: 'Swarm destinations' })
    for (const label of ['Deliverables', 'Media', 'Settings', 'Agents', 'Usage']) {
      await destinations.getByRole('link', { name: label, exact: true }).click()
      await page.waitForFunction(label => document.querySelector('.swarm-route-navigation [aria-current="page"]')?.getAttribute('aria-label') === label, label)
      assert.equal(await destinations.locator('[aria-current="page"]').count(), 1)
    }
    await page.evaluate(() => (window as any).router.navigate({ search: { workerId: 'worker-one' } }))
    await page.waitForFunction(() => !document.querySelector('.swarm-route-navigation [aria-current="page"]'))
    assert.equal(await destinations.getByRole('link').count(), 5)
    await page.evaluate(() => (window as any).update(s => ({ ...s, sessions: Array.from({length:30}, (_,i) => ({id:String(i),title:'Readable session '+i+' long-title-'.repeat(20)})) })))
    for (const width of [288, 240]) {
      await page.locator('#root').evaluate((element, width) => { element.style.width = width+'px' }, width)
      assert.equal(await destinations.evaluate(el => el.previousElementSibling?.tagName), 'HEADER')
      assert.equal(await destinations.evaluate(el => el.nextElementSibling?.getAttribute('aria-label')), 'Project sessions')
      assert.equal(await page.locator('aside').evaluate(el => el.scrollWidth <= el.clientWidth), true)
      assert.equal(await nav.evaluate(el => el.scrollHeight > el.clientHeight), true)
      const boxes = await destinations.getByRole('link').evaluateAll(links => links.map(link => { const r = link.getBoundingClientRect(); return { x: r.x, y: r.y, bottom: r.bottom } }))
      assert.ok(boxes.every((box, i) => box.x === boxes[0].x && (!i || box.y >= boxes[i - 1].bottom)))
      const create = page.getByRole('button', { name: 'New session', exact: true })
      assert.equal(await create.textContent(), '')
      assert.equal(await create.locator('svg').count(), 1)
      const row = await nav.getByRole('link').first().boundingBox()
      assert.ok(row && row.height >= 44 && row.height <= 50)
      await destinations.getByRole('link', { name: 'Deliverables', exact: true }).focus()
      await page.keyboard.press('Tab')
      assert.equal(await destinations.getByRole('link', { name: 'Media', exact: true }).evaluate(el => el === document.activeElement), true)
    }
    if (process.env.SWARM_PROJECT_SIDEBAR_SCREENSHOT) await page.locator('#root').screenshot({ path: process.env.SWARM_PROJECT_SIDEBAR_SCREENSHOT })
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
