import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'
import path from 'node:path'

// Purpose: the actual OrchestratorChatSidebar must render canonical start-only
// events, update one call card through output/terminals, and scope demands and
// replay to the selected session. Desktop V3 cache/hydration own activity, not
// local component state. A hermetic React/router/cache browser test is the
// narrowest proof of DOM identity, accessibility, switching and real CSS layout;
// controlled events/receipts are NOT live daemon/provider or reconnect evidence.
test('Orchestrator shows start-only cards and retains one centered card through replay', { timeout: 60_000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {createRootRoute,createRouter,createMemoryHistory,RouterProvider} from '@tanstack/react-router';
    import {OrchestratorChatSidebar} from './src/features/desktop/orchestrate/OrchestrateView';
    import {snapshot,sessionId} from './src/features/desktop/orchestrate/swarm-responsive-browser-fixtures';
    import {dispatchDesktopV3Cache,getDesktopV3CacheSnapshot} from './src/features/desktop/state/desktop-v3-cache-store';
    import {hydrateResponseToAction,reconnectResponseToActions} from './src/features/desktop/state/desktop-v3-cache-wire';
    import {retainDesktopV3RealtimeController,setDesktopV3RealtimeControllerFactoryForTests} from './src/features/desktop/realtime/v3-realtime-controller';
    window.demands=new Map();window.requests=[];
    setDesktopV3RealtimeControllerFactoryForTests(()=>({start:async()=>{},stop:()=>{},acquireSessionDemand(owner,id){const token={id};window.demands.set(owner,token);return {ready:Promise.resolve(),release(){if(window.demands.get(owner)===token)window.demands.delete(owner)}}}}));
    const lease=retainDesktopV3RealtimeController({ownerKey:'tool-stream-fixture'});
    const data=id=>{const base=snapshot('empty');const session={...base.sessions_by_id[sessionId],id,metadata:{project_id:'project',agent_name:'system-orchestrator'}};return {...base,session_order:[id],selector:{kind:'session_ids',session_ids:[id]},sessions_by_id:{[id]:session},projections_by_session:{[id]:{session_id:id,last_event_seq:3,projection_high_watermark_seq:3,updated_at:3}},messages_by_session:{[id]:[]},events_by_session:{[id]:[]},session_views_by_id:{[id]:{pending_permissions:[],has_active_plan:false,active_plan:null,agentic_settings:{}}},permission_summaries_by_session:{[id]:{session_id:id,pending_approval_count:0}},sync_scope:{...base.sync_scope,resource_set:'messages,events,session_view,permission_summaries'}}};
    window.snapshot=data;
    for(const id of ['one','two'])dispatchDesktopV3Cache(hydrateResponseToAction(data(id),[id]));
    let seq=10;window.events=[];
    window.emit=(type,extra={},id='one',call='call-one')=>{const payload={run_id:'run-'+id,call_id:call,tool_instance_id:'step-1:'+call,tool_name:'manage_projects',arguments:JSON.stringify({action:'inspect_files',workspace_id:'repository',inspection:{tool:'search',arguments:{path:'src',query:'sidebar'}}}),recorded_at:seq,...extra};const event={id:'event-'+seq,session_id:id,seq:seq++,event_type:type,payload,ts_unix_ms:100};window.events.push(event);dispatchDesktopV3Cache({type:'realtime.applyEvent',event:{source:'outbox',sessionId:id,eventType:type,payload,sessionEvent:event}})};
    window.commit=()=>{const base=data('one'),tool=getDesktopV3CacheSnapshot().liveRunsBySession.one['run-one'].toolCallsByCallId['call-one'];base.sessions_by_id.one.message_count=1;base.sessions_by_id.one.last_message_at=100;base.messages_by_session.one=[{id:'result-one',session_id:'one',global_seq:seq++,role:'tool',created_at:100,metadata:{call_id:'call-one'},content:JSON.stringify({path_id:'run.v3.provider-tool-result.v1',tool:'manage_projects',call_id:'call-one',arguments:tool.argumentsText,output:'{"status":"ok"}'})}];dispatchDesktopV3Cache(hydrateResponseToAction(base,['one']))};
    window.reconnect=()=>reconnectResponseToActions(data('one')).forEach(dispatchDesktopV3Cache);
    window.repair=()=>{const base=data('one');base.events_by_session.one=window.events.filter(e=>e.session_id==='one');dispatchDesktopV3Cache(hydrateResponseToAction(base,['one']))};
    function App(){const [id,set]=useState('one');window.select=set;return <OrchestratorChatSidebar sessionId={id} project={{id:'project',name:'Project'}}/>}
    const root=createRootRoute({component:App});const router=createRouter({routeTree:root,history:createMemoryHistory({initialEntries:['/']})});
    const client=new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false}}});
    const reactRoot=createRoot(document.getElementById('root'));window.hide=()=>{reactRoot.unmount();lease.release();client.clear()};
    reactRoot.render(<QueryClientProvider client={client}><RouterProvider router={router}/></QueryClientProvider>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const styles = await buildStyles({ configFile: false, publicDir: false, logLevel: 'silent', plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n') + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8')
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 800, height: 900 } })
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (route.request().isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="width:440px;height:800px;display:flex"></div>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: await page.evaluate(id => (window as any).snapshot(id), route.request().postDataJSON().session_ids[0]) })
      if (url.pathname === '/v1/auth/desktop/session') return route.fulfill({ json: { user_id: 'user', account_scope_id: 'account' } })
      if (/\/v3\/sessions\/[^/]+\/artifacts-v3$/.test(url.pathname)) return route.fulfill({ json: { artifacts: [] } })
      if (/\/v3\/sessions\/[^/]+\/artifact-v2$/.test(url.pathname)) return route.fulfill({ json: { collections: [] } })
      if (/\/v3\/sessions\/[^/]+\/repositories$/.test(url.pathname)) return route.fulfill({ json: { repositories: [] } })
      if (url.pathname === '/v1/agent-model-settings') return route.fulfill({ json: { agent_model_settings: { swarm: {}, system_agents: {} } } })
      if (url.pathname === '/v1/model-profiles') return route.fulfill({ json: { model_profiles: [] } })
      if (url.pathname === '/v1/providers') return route.fulfill({ json: { providers: [] } })
      if (url.pathname === '/v1/media/settings/catalog') return route.fulfill({ json: { image_models: [], video_generation_models: [], audio_models: [] } })
      return route.fulfill({ status: 501, json: { error: 'Unconfigured tool stream fixture read' } })
    })
    await page.goto('https://tool-stream.test/')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const sidebar = page.getByRole('complementary', { name: 'Swarm Orchestrator AI Chat', exact: true })
    await sidebar.getByTestId('desktop-chat-content-lane').waitFor()
    await page.waitForFunction(() => (window as any).demands.get('orchestrator-active-conversation')?.id === 'one')
    await page.evaluate(() => (window as any).emit('session.tool.started'))
    const card = sidebar.locator('[data-tool-call-id="call-one"]')
    await card.getByText('Searching project files…', { exact: true }).waitFor()
    assert.equal(await card.getAttribute('aria-busy'), 'true')
    assert.equal(await card.getByText('Running', { exact: true }).count(), 1)
    await page.evaluate(() => { (window as any).cardNode = document.querySelector('[data-tool-call-id="call-one"]') })
    await page.evaluate(() => (window as any).emit('session.tool.delta', { output: 'partial result' }))
    await card.getByText('Running', { exact: true }).waitFor()
    assert.equal(await card.count(), 1)
    assert.equal(await page.evaluate(() => (window as any).cardNode === document.querySelector('[data-tool-call-id="call-one"]')), true)
    for (const width of [280, 440]) {
      await page.locator('#root').evaluate((node, width) => { node.style.width = width + 'px' }, width)
      const layout = await card.evaluate(node => {
        const card = node.getBoundingClientRect(), lane = node.closest('[data-testid="desktop-chat-content-lane"]')!
        const box = lane.getBoundingClientRect(), style = getComputedStyle(lane)
        return { symmetry: Math.abs((card.left - box.left) - (box.right - card.right)), gutter: parseFloat(style.paddingLeft), overflow: lane.scrollWidth > lane.clientWidth + 1 }
      })
      assert.ok(layout.symmetry <= 2, 'equal horizontal sidebar card margins')
      assert.ok(layout.gutter >= 12)
      assert.equal(layout.overflow, false)
    }
    await page.evaluate(() => (window as any).select('two'))
    await page.waitForFunction(() => (window as any).demands.get('orchestrator-active-conversation')?.id === 'two')
    assert.equal(await card.count(), 0)
    await page.evaluate(() => (window as any).emit('session.tool.started', {}, 'two', 'call-two'))
    await sidebar.locator('[data-tool-call-id="call-two"]').waitFor()
    await page.evaluate(() => (window as any).select('one'))
    await card.waitFor()
    assert.equal(await sidebar.locator('[data-tool-call-id="call-two"]').count(), 0)
    await page.evaluate(() => { (window as any).reconnect(); (window as any).repair() })
    await card.getByText('Running', { exact: true }).waitFor()
    assert.equal(await card.count(), 1)
    await page.evaluate(() => { (window as any).cardNode = document.querySelector('[data-tool-call-id="call-one"]') })
    await page.evaluate(() => (window as any).emit('session.tool.completed', { output: '{"status":"ok"}' }))
    await card.getByText('Done', { exact: true }).waitFor()
    assert.equal(await card.getAttribute('data-tool-activity-state'), 'done')
    assert.equal(await page.evaluate(() => (window as any).cardNode === document.querySelector('[data-tool-call-id="call-one"]')), true, 'completion updates the same call card')
    for (const [call, terminal, label] of [['call-error', 'session.tool.failed', 'Failed'], ['call-cancel', 'session.tool.cancelled', 'Cancelled']]) {
      await page.evaluate(([call, terminal]) => { (window as any).emit('session.tool.started', {}, 'one', call); (window as any).emit(terminal, terminal.endsWith('failed') ? { error: 'inspection denied' } : {}, 'one', call) }, [call, terminal])
      const terminalCard = sidebar.locator('[data-tool-call-id="' + call + '"]')
      await terminalCard.getByText(label, { exact: true }).waitFor()
      assert.equal(await terminalCard.count(), 1)
      assert.equal(await terminalCard.getAttribute('aria-busy'), null)
    }
    await page.evaluate(() => (window as any).repair())
    assert.equal(await sidebar.locator('[data-tool-call-id]').count(), 3)
    await page.evaluate(() => (window as any).commit())
    await card.getByText('Done', { exact: true }).waitFor()
    assert.equal(await card.count(), 1, 'committed result suppresses the matching live overlay')
    assert.deepEqual(errors, [])
    await page.evaluate(() => (window as any).hide())
    assert.equal(await page.evaluate(() => (window as any).demands.size), 0)
  } finally { await browser.close() }
})
