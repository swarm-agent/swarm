import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: MinimalTaskCard/useTaskAttention must expose requests before execution
// without expanding history, and render the pending proposal rather than an old
// accepted plan. Real cache hydration, disclosure and review controls are the
// narrowest integration layer proving arrival, restoration and decision routing.
// HTTP is intercepted; this is not evidence of live provider resumption.
test('collapsed task keeps pending permissions and their proposed plan actionable', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {dispatchDesktopV3Cache} from './src/features/desktop/state/desktop-v3-cache-store';
    import {hydrateResponseToAction} from './src/features/desktop/state/desktop-v3-cache-wire';
    const task={id:'task',title:'Permission task',status:'in_progress',agentType:'coder',sessionId:'child',revision:1,progress:0,elapsed:'0s',workspaceTarget:'local',sourceWorkspacePath:'/repo'};
    const wire=(id,tool,requirement,args)=>({id,session_id:'child',run_id:'run',call_id:id,tool_name:tool,requirement,tool_arguments:JSON.stringify(args),status:'pending',mode:'auto',created_at:1,updated_at:1});
    const ordinary=wire('ordinary','write','tool_approval',{path:'/repo/pending.txt',content:'Await approval'});
    const plan=wire('proposal','plan_manage','plan_new_request',{action:'request_new_plan',document:{title:'Proposed migration',info:{goal:'Preserve durable records'},checkpoints:[{id:'cp-1',title:'Verify storage',tasks:['Check restored records'],acceptance_criteria:['No records lost']}]}});
    let pending=[];window.requests=[];
    const snapshot=()=>({sessions_by_id:{child:{id:'child',account_scope_id:'account',user_id:'user'}},session_views_by_id:{child:{pending_permissions:pending,has_active_plan:false}},permission_summaries_by_session:{child:{pending_approval_count:pending.length}},selector:{kind:'session_ids',session_ids:['child']},scope_id:'review',snapshot_endpoint_cursor:'opaque',sync_scope:{surface:'desktop',stream_kind:'v3.sync.snapshot',selector_filter_hash:'review',resource_set:'session_view,permission_summaries'}});
    const hydrate=()=>dispatchDesktopV3Cache(hydrateResponseToAction(snapshot(),['child']));hydrate();
    window.arrive=()=>{pending=[ordinary,plan];hydrate()};
    window.fetch=async(input,init)=>{const url=String(input);
      if(url.endsWith('/v3/sync/hydrate'))return new Response(JSON.stringify(snapshot()));
      if(url.includes('/permissions/')&&url.endsWith('/resolve')){const body=JSON.parse(init.body);window.requests.push({url,body});const id=url.split('/').at(-2);const p=pending.find(p=>p.id===id);pending=pending.filter(p=>p.id!==id);return new Response(JSON.stringify({permission:{...p,status:body.action==='deny'?'denied':'approved',decision:body.action,resolved_at:10,updated_at:10}}))}
      return new Response(JSON.stringify({}));};
    const root=createRoot(document.getElementById('root'));const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    window.mount=()=>root.render(<QueryClientProvider client={client}><MinimalTaskCard task={task}/></QueryClientProvider>);
    window.unmount=()=>root.render(null);window.mount();
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://pending.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const toggle = page.getByTestId('toggle-task-details-btn')
    await toggle.waitFor()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    assert.equal(await page.getByTestId('desktop-inline-plan-review').count(), 0)
    await page.evaluate(() => (window as any).arrive())
    const plan = page.getByTestId('desktop-inline-plan-review')
    await plan.waitFor()
    for (const text of ['Proposed migration', 'Preserve durable records', 'Verify storage', 'Check restored records', 'No records lost']) assert.ok((await plan.innerText()).includes(text), text)
    const review = page.getByRole('button', { name: 'Review permission', exact: true })
    assert.equal(await review.count(), 1)
    await toggle.click()
    assert.equal(await plan.count(), 1)
    assert.equal(await review.count(), 1)
    await toggle.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await review.click()
    const dialog = page.getByRole('dialog')
    await dialog.waitFor()
    assert.match(await dialog.innerText(), /pending.txt/)
    await dialog.getByRole('button', { name: /^Deny/ }).click()
    await dialog.waitFor({ state: 'hidden' })
    assert.equal(await review.count(), 0)
    assert.equal(await plan.count(), 1)
    // Remount from the canonical hydrated cache, with no transcript/tool-start event.
    await page.evaluate(() => (window as any).unmount())
    await toggle.waitFor({ state: 'hidden' })
    await page.evaluate(() => (window as any).mount())
    await plan.waitFor()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await plan.getByRole('button', { name: 'Accept once', exact: true }).click()
    await plan.waitFor({ state: 'hidden' })
    const requests = await page.evaluate(() => (window as any).requests)
    assert.deepEqual(requests.map((r: any) => [r.url.split('/').at(-2), r.body.action]), [['ordinary', 'deny'], ['proposal', 'approve']])
    assert.ok(requests.every((r: any) => r.url.includes('/v3/sessions/child/permissions/')))
    assert.equal(requests[1].body.approved_arguments?.document, undefined, 'approval must not replay client plan content')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
