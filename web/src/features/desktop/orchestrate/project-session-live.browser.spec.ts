import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'

// Purpose: useProjectConversations and ProjectConversationSidebar must consume
// canonical cache updates and completed durations, release demands/timers, keep
// selection hidden until explicitly requested, scope selection, and preserve
// failures without silently stopping runs. Real React/router/cache plus controlled
// HTTP receipts is the narrowest observable browser proof; not live provider E2E.
// Reconnect must rediscover missed membership via useProjectConversations without
// duplicating demands/timers; unmount must detach the reconnect listener.
test('project live rows, timers, scoped selection and durable archive receipts', { timeout: 40000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,createMemoryHistory,RouterProvider,useRouterState} from '@tanstack/react-router';
    import {ProjectConversationSidebar} from './src/features/desktop/orchestrate/project-conversation-sidebar';
    import {useProjectConversations} from './src/features/desktop/runtime/project-conversations';
    import {desktopProjects} from './src/features/desktop/runtime/desktop-projects';
    import {dispatchDesktopV3Cache} from './src/features/desktop/state/desktop-v3-cache-store';
    import {hydrateResponseToAction,reconnectResponseToActions} from './src/features/desktop/state/desktop-v3-cache-wire';
    window.demands=new Set();window.calls=[];window.fail='fail';window.intervals=new Set();window.listReads=0;
    const si=window.setInterval.bind(window),ci=window.clearInterval.bind(window);window.setInterval=(fn,ms)=>{const id=si(fn,ms);if(ms===1000)window.intervals.add(id);return id};window.clearInterval=id=>{window.intervals.delete(id);ci(id)};
    const session=(id,title,at)=>({id,title,created_at:1,updated_at:at,last_message_at:at,account_scope_id:'account',user_id:'user',metadata:{agent_name:'system-orchestrator',project_id:'project'}});
    const records={idle:session('idle','Recent idle',300),run:session('run','Running session',100),fail:session('fail','Retry session',200)};
    const tombstones={};let sequence=1;let status='running';let approval=1;let runId='work';
    const snapshot=(ids=Object.keys(records))=>({sessions_by_id:Object.fromEntries(ids.filter(id=>records[id]).map(id=>[id,records[id]])),tombstones_by_session:Object.fromEntries(ids.filter(id=>tombstones[id]).map(id=>[id,tombstones[id]])),current_run_state_by_session:ids.includes('run')?{run:{session_id:'run',run_id:runId,status,created_at:Date.now()-9000,started_at:Date.now()-9000,completed_at:status==='completed'?Date.now():0,duration_ms:status==='completed'?12000:undefined}}:{},permission_summaries_by_session:ids.includes('run')?{run:{pending_approval_count:approval}}:{},projections_by_session:Object.fromEntries(ids.map(id=>[id,{session_id:id,last_event_seq:sequence,projection_high_watermark_seq:sequence,updated_at:sequence}])),scope_id:'project-fixture',selector:{kind:'session_ids',session_ids:ids},snapshot_endpoint_cursor:'opaque'+sequence,sync_scope:{surface:'desktop',stream_kind:'v3.sync.snapshot',selector_filter_hash:'project',resource_set:'current_run_state,permission_summaries'}});
    window.add=()=>{records.remote=session('remote','Remote conversation',500);sequence++;desktopProjects.acceptFrame({kind:'project.updated',project_id:'project'})};
    window.reconnect=()=>{records.offline=session('offline','Created while disconnected',700);sequence++;reconnectResponseToActions(snapshot(['run'])).forEach(dispatchDesktopV3Cache)};
    window.rename=()=>{records.remote={...records.remote,title:'Renamed remotely',updated_at:600};sequence++;dispatchDesktopV3Cache(hydrateResponseToAction(snapshot(),Object.keys(records)))};
    window.remove=()=>{delete records.remote;tombstones.remote={session_id:'remote',kind:'deleted',deleted:true,updated_at:++sequence};dispatchDesktopV3Cache(hydrateResponseToAction(snapshot(['remote']),['remote']))};
    window.change=(next,pending)=>{if(status==='completed'&&next==='running')runId='next-work';status=next;approval=pending;sequence++;dispatchDesktopV3Cache(hydrateResponseToAction(snapshot(),Object.keys(records)))};
    window.fetch=async(input,init)=>{const url=String(input);const body=init?.body?JSON.parse(init.body):{};
      if(url.includes('/auth/desktop/session'))return new Response(JSON.stringify({user_id:'user',account_scope_id:'account'}));
      if(url.includes('/v3/projects/project/sessions')){window.listReads++;return new Response(JSON.stringify(url.includes('archived_mode')?{tombstones:Object.values(tombstones)}:{sessions:Object.values(records).map(session=>({session}))}))};
      if(url.endsWith('/v3/sync/hydrate'))return new Response(JSON.stringify(snapshot(body.session_ids)));
      window.calls.push({url,body});
      if(url.endsWith('/v3/sessions:archive')){const id=body.session_ids[0];if(id===window.fail)return new Response(JSON.stringify({error:'Archive unavailable; retry'}),{status:409});const s=records[id];delete records[id];const tombstone={session_id:id,kind:'archived',archived:true,updated_at:++sequence,session:s};tombstones[id]=tombstone;return new Response(JSON.stringify({ok:true,archived:true,results:[{session_id:id,archived:true,tombstone}]}))}
      if(url.endsWith('/v3/sessions:unarchive')){if(window.restoreFail)return new Response(JSON.stringify({error:'Restore unavailable; retry'}),{status:409});const id=body.session_ids[0];if(body.expected_updated_at_by_id[id]!==tombstones[id].updated_at)throw Error('Wrong version');records[id]=tombstones[id].session;delete tombstones[id];sequence++;return new Response(JSON.stringify({ok:true,unarchived_session_ids:[id]}))}
      throw Error('Unexpected '+url)
    };
    function Panel(){const data=useProjectConversations('project');const path=useRouterState({select:s=>s.location.pathname});return <ProjectConversationSidebar projectId='example' projectName='Example' selectedId={path.split('/').at(-1)} {...data} creating={false} onCreate={()=>{}} onRetry={data.refresh} onSelect={()=>{}}/>}
    function App(){const [show,set]=useState(true);window.hide=()=>set(false);return show?<Panel/>:null}
    const root=createRootRoute({component:App});const routeTree=root.addChildren(['/projects/$projectId','/projects/$projectId/sessions/$sessionId'].map(path=>createRoute({getParentRoute:()=>root,path})));
    window.router=createRouter({routeTree,history:createMemoryHistory({initialEntries:['/projects/example/sessions/idle']})});createRoot(document.getElementById('root')).render(<RouterProvider router={window.router}/>);
  ` }, plugins: [{ name: 'scoped-controller-fixture', setup(builder) {
    builder.onResolve({ filter: /v3-realtime-controller$/ }, () => ({ path: 'controller', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: `export async function requireDesktopV3RealtimeControllerReady(){return {acquireSessionDemand(owner,id){window.demands.add(id);return {release(){window.demands.delete(id)}}}}}`, loader: 'js' }))
  } }], bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 760 } })
    page.setDefaultTimeout(6000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root" style="width:260px;padding:8px"></div>' }))
    await page.goto('https://project.test/')
    await page.addStyleTag({ content: `*{box-sizing:border-box}body{margin:0;background:#0d121f;color:#e2e8f0;font-family:sans-serif}h2,p{margin:0}a{color:inherit;text-decoration:none}button{background:transparent;border:0;color:inherit} :root{--swarm-text:#e2e8f0;--swarm-text-muted:#94a3b8;--swarm-border:#334155;--swarm-accent:#60a5fa;--swarm-surface-hover:#1e293b}` + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const nav = page.getByRole('navigation', { name: 'Conversation sessions' })
    await nav.getByText('Needs approval').waitFor()
    assert.match(await nav.getByRole('link').first().innerText(), /Running session/)
    assert.deepEqual(await page.evaluate(() => [...(window as any).demands].sort()), ['fail', 'idle', 'run'])
    await page.waitForFunction(() => (window as any).intervals.size === 1)
    const before = await page.locator('.swarm-session-timer').innerText()
    await page.waitForFunction(value => document.querySelector('.swarm-session-timer')?.textContent !== value, before)
    assert.match(await nav.getByRole('link').first().innerText(), /Running session/)
    assert.equal(await page.getByRole('checkbox').count(), 0)
    if (process.env.SWARM_PROJECT_LIVE_SCREENSHOT) await page.locator('#root').screenshot({ path: process.env.SWARM_PROJECT_LIVE_SCREENSHOT.replace('.png', '-default.png') })
    await page.getByRole('button', { name: 'Select sessions', exact: true }).click()
    const all = page.getByRole('checkbox', { name: 'Select all loaded unarchived project sessions' })
    await page.getByRole('checkbox', { name: 'Select Recent idle', exact: true }).check()
    assert.equal(await all.evaluate((el: HTMLInputElement) => el.indeterminate), true)
    await all.check()
    assert.equal(await page.getByText('3 selected · 3 loaded').count(), 1)
    if (process.env.SWARM_PROJECT_LIVE_SCREENSHOT) await page.locator('#root').screenshot({ path: process.env.SWARM_PROJECT_LIVE_SCREENSHOT })
    await page.getByRole('button', { name: 'Archive selected', exact: true }).click()
    await page.getByText('1 archived; 2 not changed.').waitFor()
    assert.equal(await nav.getByRole('link', { name: 'Recent idle Example' }).count(), 0)
    assert.equal(await page.getByRole('checkbox', { name: 'Select Retry session', exact: true }).isChecked(), true)
    assert.match(await page.getByRole('alert').innerText(), /Active work.*stop it explicitly/s)
    assert.equal(await page.evaluate(() => (window as any).calls.some(c => c.body.session_ids?.includes('run'))), false)
    if (process.env.SWARM_PROJECT_LIVE_SCREENSHOT) await page.locator('#root').screenshot({ path: process.env.SWARM_PROJECT_LIVE_SCREENSHOT.replace('.png', '-failure.png') })
    await page.waitForFunction(() => (window as any).router.state.location.pathname === '/projects/example')
    await page.evaluate(() => (window as any).change('completed', 0))
    await page.waitForFunction(() => (window as any).intervals.size === 0)
    assert.equal(await nav.getByText('Needs approval').count(), 0)
    await nav.getByText('Completed', { exact: true }).waitFor()
    assert.equal(await page.locator('.swarm-session-timer').innerText(), '0:12')
    await page.getByRole('button', { name: 'Archived', exact: true }).click()
    await page.getByRole('button', { name: 'Restore Recent idle', exact: true }).waitFor()
    assert.equal(await page.getByText('0 selected · 1 loaded').count(), 1)
    if (process.env.SWARM_PROJECT_LIVE_SCREENSHOT) await page.locator('#root').screenshot({ path: process.env.SWARM_PROJECT_LIVE_SCREENSHOT.replace('.png', '-archived.png') })
    await page.getByRole('checkbox', { name: 'Select all loaded archived project sessions' }).check()
    await page.evaluate(() => { (window as any).restoreFail = true })
    await page.getByRole('button', { name: 'Restore selected', exact: true }).click()
    await page.getByText('0 restored; 1 not changed.').waitFor()
    assert.equal(await page.getByRole('checkbox', { name: 'Select Recent idle', exact: true }).isChecked(), true)
    await page.evaluate(() => { (window as any).restoreFail = false })
    await page.getByRole('button', { name: 'Restore selected', exact: true }).click()
    await page.getByText('1 restored.').waitFor()
    await page.getByRole('button', { name: 'Back to sessions', exact: true }).click()
    await nav.getByRole('link', { name: 'Recent idle Example' }).waitFor()
    await page.evaluate(() => { (window as any).fail = ''; })
    await page.getByRole('button', { name: 'Archive Retry session', exact: true }).click()
    await page.getByText('1 archived.').waitFor()
    await page.waitForFunction(() => !(window as any).demands.has('fail'))
    await page.evaluate(() => (window as any).change('running', 0))
    await page.waitForFunction(() => (window as any).intervals.size === 1)
    await page.evaluate(() => (window as any).add())
    await nav.getByRole('link', { name: 'Remote conversation Example' }).waitFor()
    await page.waitForFunction(() => (window as any).demands.has('remote'))
    await page.evaluate(() => (window as any).rename())
    await nav.getByRole('link', { name: 'Renamed remotely Example' }).waitFor()
    assert.equal(await nav.getByRole('link', { name: 'Remote conversation Example' }).count(), 0)
    await page.evaluate(() => (window as any).remove())
    await page.waitForFunction(() => !(window as any).demands.has('remote'))
    assert.equal(await nav.getByRole('link', { name: 'Renamed remotely Example' }).count(), 0)
    await page.getByRole('button', { name: 'Done selecting', exact: true }).click()
    const readsBeforeReconnect = await page.evaluate(() => (window as any).listReads)
    await page.evaluate(() => (window as any).reconnect())
    await nav.getByRole('link', { name: 'Created while disconnected Example' }).waitFor()
    await page.waitForFunction(() => (window as any).demands.has('offline'))
    assert.ok(await page.evaluate(() => (window as any).listReads) > readsBeforeReconnect)
    assert.equal(await page.evaluate(() => (window as any).intervals.size), 1)
    assert.equal(await page.getByRole('checkbox').count(), 0)
    await page.evaluate(() => (window as any).hide())
    await page.waitForFunction(() => (window as any).intervals.size === 0 && (window as any).demands.size === 0)
    const readsAfterUnmount = await page.evaluate(() => (window as any).listReads)
    await page.evaluate(() => (window as any).reconnect())
    assert.equal(await page.evaluate(() => (window as any).listReads), readsAfterUnmount)
    assert.equal(await nav.count(), 0)
  } finally { await browser.close() }
})
