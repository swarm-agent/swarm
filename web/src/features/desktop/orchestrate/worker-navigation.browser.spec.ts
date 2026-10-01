import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: canonical proposals remain discoverable under only their project,
// including collapsed/hidden states; inspection cannot send chat or approve implicitly.
// Threat: legacy-permission-only attention, cross-account/project leakage, optimistic
// acceptance, false running/count labels, and preference actions mutating workers.
// Authority: ProjectWorkerSidebar/WorkerApprovalAttention/WorkerDetail and the shared
// V3 cache. This rendered fixture is the narrowest navigation/acceptance composition
// layer; runtime event/reconnect and server isolation tests prove their own boundaries.
// It is not provider-backed dogfood or proof of the full application's chat lifecycle.
test('canonical proposal arrival, scoped presentation, bounded activity and in-place acceptance', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {ProjectWorkerSidebar} from './src/features/desktop/layout/project-worker-sidebar';
    import {WorkerApprovalAttention} from './src/features/desktop/notifications/components/worker-approval-attention';
    import {WorkerDetail} from './src/features/desktop/orchestrate/worker-hub';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    import {dispatchDesktopV3Cache,getDesktopV3CacheSnapshot} from './src/features/desktop/state/desktop-v3-cache-store';
    import {workerPageKey} from './src/features/desktop/state/desktop-workers-state';
    let worker={id:'worker_review',account_scope_id:'acct',name:'Review repository',instructions:'Inspect only',revision:1,lifecycle_state:'pending',created_at:1,updated_at:1,metadata:{project_id:'project_a'}};
    window.calls=[];window.demands=[];window.fail=true;
    const feed=(input,data)=>{const key=workerPageKey(input),requestId=crypto.randomUUID();dispatchDesktopV3Cache({type:'workers.begin',key,input,requestId});dispatchDesktopV3Cache({type:'workers.finish',key,requestId,generation:getDesktopV3CacheSnapshot().workerPages[key].generation,data})};
    const list={kind:'list',accountScopeId:'acct',limit:100};
    const pending={...list,lifecycleState:'pending'};
    const detail={kind:'detail',accountScopeId:'acct',workerId:worker.id};
    const summary={kind:'summary',accountScopeId:'acct',workerId:worker.id,timezone:'UTC',date:new Date().toISOString().slice(0,10)};
    feed(list,{workers:[]});feed(pending,{workers:[]});
    const foreign={...worker,id:'worker_foreign',account_scope_id:'other',name:'Foreign account'};
    const other={...worker,id:'worker_other',name:'Other project',metadata:{project_id:'project_b'}};
    window.arrive=()=>{feed(list,{workers:[worker,foreign,other],next_cursor:'opaque'});feed(pending,{workers:[worker,foreign,other]});feed(detail,{worker});feed(summary,{worker_id:worker.id,next_scheduled_at:0,runs:{active:[],active_runs:0,active_truncated:false,date:summary.date,timezone:'UTC',daily_runs:7,daily_success:7,daily_failed:0,daily_cancelled:0,scanned_runs:7,truncated:true,day_start_at:0,day_end_at:0}});feed({kind:'runs',accountScopeId:'acct',workerId:worker.id,limit:25},{runs:[]})};
    desktopWorkers.acquire=input=>{window.demands.push(input);return {ready:Promise.resolve(),release:()=>{}}};
    desktopWorkers.mutate=async input=>{window.calls.push(input);if(window.fail)throw Error('Revision conflict; review again');worker={...worker,lifecycle_state:'active',revision:2};window.arrive();feed(pending,{workers:[other]});return {worker}};
    function App(){const [id,setId]=useState('');const [scope,setScope]=useState('project_a');window.scope=setScope;return <><ProjectWorkerSidebar accountScopeId='acct' projectId={scope} showActivity onInspect={setId} onBrowse={()=>{}}/><WorkerApprovalAttention accountScopeId='acct' onInspect={setId} onBrowse={()=>{}}/><main>{id&&<WorkerDetail workerId={id} accountScopeId='acct' onSelectWorker={()=>{}} onClose={()=>setId('')}/>}</main><textarea aria-label='Retained chat draft' defaultValue='Keep my draft'/></>}
    createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const sidebar = page.getByRole('region', { name: 'Project workers' })
    await sidebar.getByText('No workers on this page').waitFor()
    await page.evaluate(() => (window as any).arrive())
    await sidebar.getByText('Review repository', { exact: true }).waitFor()
    assert.equal(await sidebar.getByText('Foreign account').count(), 0)
    assert.equal(await sidebar.getByText('Other project').count(), 0)
    await sidebar.getByText(/Not running.*7 runs today \(UTC\).*partial/).waitFor()
    assert.equal((await page.evaluate(() => (window as any).demands)).filter((input: any) => input.kind === 'summary').length, 1)
    await sidebar.getByRole('button', { name: 'Hide Review repository in sidebar' }).click()
    assert.equal(await sidebar.getByText('Review repository', { exact: true }).count(), 0)
    await sidebar.getByRole('button', { name: 'Collapse project Workers' }).click()
    await sidebar.getByRole('button', { name: '1 pending approval', exact: true }).click()
    await page.getByRole('heading', { name: 'Review repository', exact: true }).first().waitFor()
    assert.equal(await page.getByRole('textbox', { name: 'Retained chat draft' }).inputValue(), 'Keep my draft')
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await page.getByRole('button', { name: 'Accept worker', exact: true }).click()
    await page.getByRole('alert').getByText('Revision conflict; review again').waitFor()
    assert.equal(await page.getByTestId('pending-worker-card').count(), 1)
    await page.evaluate(() => { (window as any).fail = false })
    await page.getByRole('button', { name: 'Accept worker', exact: true }).click()
    await page.getByRole('button', { name: 'Pause worker', exact: true }).waitFor()
    assert.equal(await page.getByTestId('pending-worker-card').count(), 0)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [
      { action: 'accept', workerId: 'worker_review', expected_revision: 1 },
      { action: 'accept', workerId: 'worker_review', expected_revision: 1 },
    ])
    await sidebar.getByRole('button', { name: 'Expand project Workers' }).click()
    await sidebar.getByRole('button', { name: 'Show hidden workers (1)' }).click()
    await sidebar.getByRole('button', { name: 'Restore Review repository in sidebar' }).click()
    await sidebar.getByText('Enabled · On-demand').waitFor()
    assert.equal(await page.getByRole('textbox', { name: 'Retained chat draft' }).inputValue(), 'Keep my draft')
    assert.equal((await page.evaluate(() => (window as any).calls)).length, 2)
    await sidebar.getByRole('button', { name: 'Hide Review repository in sidebar' }).click()
    await page.evaluate(() => (window as any).scope('project_b'))
    await sidebar.getByText('Other project', { exact: true }).waitFor()
    assert.equal(await sidebar.getByText('Review repository', { exact: true }).count(), 0)
    await page.evaluate(() => (window as any).scope('project_a'))
    await sidebar.getByRole('button', { name: 'Show hidden workers (1)' }).waitFor()
    assert.equal(await sidebar.getByText('Review repository', { exact: true }).count(), 0)
    // Storage reload repair: storage events invalidate only presentation snapshots.
    await page.evaluate(() => window.dispatchEvent(new StorageEvent('storage')))
    await sidebar.getByRole('button', { name: 'Show hidden workers (1)' }).waitFor()
  } finally { await browser.close() }
})

// Requirement: swarmWorkerLink stays under the persistent layout for history navigation.
// Authority: the production destination helper plus TanStack Router lifecycle; this
// narrow rendered router/composer test does not replace a full OrchestrateView smoke test.
test('worker inspection and browser history retain the mounted Orchestrator composer', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,RouterProvider,Outlet,Link,useNavigate,useRouterState} from '@tanstack/react-router';
    import {swarmWorkerLink,swarmPageLink,swarmActivePage} from './src/features/desktop/orchestrate/swarm-navigation';
    import {OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    window.sent=[];
    function Layout(){const navigate=useNavigate();const worker=useRouterState({select:s=>s.location.search.workerId});const section=useRouterState({select:s=>s.matches.at(-1)?.params.swarmSection});const active=swarmActivePage(section,worker);return <><button onClick={()=>navigate(swarmWorkerLink('demo','worker_review'))}>Inspect worker</button><button onClick={()=>navigate(swarmWorkerLink('demo','worker_second'))}>Inspect second</button><button onClick={()=>navigate(swarmPageLink('demo','home'))}>Tasks</button><nav><Link {...swarmPageLink('demo','home')} activeOptions={{exact:true,includeSearch:false}} aria-current={active==='home'?'page':undefined}>Tasks tab</Link><Link {...swarmPageLink('demo','workers')} activeOptions={{exact:true,includeSearch:false}} aria-current={active==='workers'?'page':undefined}>Workers tab</Link></nav><p>{worker||'Tasks center'}</p><OrchestratorChatComposer sessionId='session_fixture' submitMessage={async operation=>window.sent.push(operation)}/><Outlet/></>}
    const root=createRootRoute({component:Outlet});const layout=createRoute({getParentRoute:()=>root,id:'swarm-layout',component:Layout,validateSearch:s=>({workerId:typeof s.workerId==='string'?s.workerId:undefined})});
    const home=createRoute({getParentRoute:()=>layout,path:'/$workspaceSlug/swarm'});const section=createRoute({getParentRoute:()=>layout,path:'/$workspaceSlug/swarm/$swarmSection'});
    const router=createRouter({routeTree:root.addChildren([layout.addChildren([home,section])])});
    createRoot(document.getElementById('root')).render(<RouterProvider router={router}/>);
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://worker.test/demo/swarm')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByTestId('orchestrator-chat-input')
    await input.fill('Keep this unsent draft')
    await page.getByRole('button', { name: 'Inspect worker', exact: true }).click()
    await page.getByText('worker_review', { exact: true }).waitFor()
    assert.match(page.url(), /demo\/swarm\/workers\?workerId=worker_review/)
    assert.equal(await input.inputValue(), 'Keep this unsent draft')
    assert.equal(await page.getByRole('link', { name: 'Workers tab' }).getAttribute('aria-current'), 'page')
    assert.equal(await page.getByRole('link', { name: 'Tasks tab' }).getAttribute('aria-current'), null)
    await page.getByRole('button', { name: 'Inspect second', exact: true }).click()
    await page.getByText('worker_second', { exact: true }).waitFor()
    await page.goBack()
    await page.getByText('worker_review', { exact: true }).waitFor()
    await page.getByRole('button', { name: 'Tasks', exact: true }).click()
    await page.getByText('Tasks center', { exact: true }).waitFor()
    await page.goBack()
    await page.getByText('worker_review', { exact: true }).waitFor()
    assert.equal(await input.inputValue(), 'Keep this unsent draft')
    await page.goForward()
    await page.getByText('Tasks center', { exact: true }).waitFor()
    assert.equal(await input.inputValue(), 'Keep this unsent draft')
    assert.deepEqual(await page.evaluate(() => (window as any).sent), [])
    await page.goto('https://worker.test/demo/swarm/workers?workerId=worker_second')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByText('worker_second', { exact: true }).waitFor()
    await page.reload()
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByText('worker_second', { exact: true }).waitFor()
    assert.equal(await page.getByRole('link', { name: 'Workers tab' }).getAttribute('aria-current'), 'page')
    assert.equal(await page.getByRole('link', { name: 'Tasks tab' }).getAttribute('aria-current'), null)
  } finally { await browser.close() }
})
