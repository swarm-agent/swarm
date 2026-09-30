import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: The V3 hub reads the canonical worker cache and exposes revision-guarded
// actions, visible model/execution controls and paged runs. Threat: stale status,
// invented success on failed stop, lost run links or accidental direct dispatch.
// Boundary: WorkerHub/WorkerDetail -> desktopWorkers runtime -> V3 cache. A real
// rendered component is the narrowest layer for the interactive states; server tests
// separately prove account isolation, mutation effects, and stop barriers.
test('operational worker detail categorizes runs, forwards context and preserves failed stop errors', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {WorkerDetail} from './src/features/desktop/orchestrate/worker-hub';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    import {dispatchDesktopV3Cache} from './src/features/desktop/state/desktop-v3-cache-store';
    import {workerPageKey} from './src/features/desktop/state/desktop-workers-state';
    window.calls=[]; window.selected=[];
    const worker={id:'worker_123', account_scope_id:'acct',name:'Daily audit',instructions:'Inspect daily',revision:2,lifecycle_state:'active',created_at:1,updated_at:2,local_bindings:{primary:'workspace_1'},automations:[{id:'wauto_1',worker_id:'worker_123',name:'Review',revision:1,enabled:true,activation_mode:'cron',schedule:{kind:'cron',cron:'0 9 * * *',timezone:'UTC'},plan_document:{title:'Audit',checkpoints:[{id:'cp-1',title:'Check'}]}}]};
    const run={id:'run_1',worker_id:'worker_123',account_scope_id:'acct',worker_revision:2,request_source:'schedule',status:'running',session_id:'session_1',created_at:Date.now(),error:'warning',deliverables:[{label:'report'}]};
    const feed=(input,data)=>{const key=workerPageKey(input); dispatchDesktopV3Cache({type:'workers.begin',key,input,requestId:'req-'+input.kind});dispatchDesktopV3Cache({type:'workers.finish',key,requestId:'req-'+input.kind,generation:0,data})};
    window.invalidate=()=>dispatchDesktopV3Cache({type:'workers.invalidate',workerId:'worker_123',accountScopeId:'acct'});
    feed({kind:'detail',accountScopeId:'acct',workerId:'worker_123'},{worker});feed({kind:'runs',accountScopeId:'acct',workerId:'worker_123',limit:25},{runs:[run],next_cursor:'opaque'});feed({kind:'history',accountScopeId:'acct',workerId:'worker_123',limit:10},{revisions:[{worker_id:worker.id,account_scope_id:'acct',revision:2,worker,committed_at:Date.now(),change_summary:'Edited'}]});
    feed({kind:'summary',accountScopeId:'acct',workerId:'worker_123',timezone:'UTC',date:new Date().toISOString().slice(0,10)},{worker_id:'worker_123',next_scheduled_at:0,runs:{active:[],active_runs:2,active_truncated:false,timezone:'UTC',date:new Date().toISOString().slice(0,10),daily_runs:42,daily_success:39,daily_failed:2,daily_cancelled:1,scanned_runs:42,truncated:false,day_start_at:0,day_end_at:0}});
    desktopWorkers.acquire=()=>({ready:Promise.resolve(),release:()=>{}});
    desktopWorkers.mutate=async (input)=>{window.calls.push(input);if(window.stopping)return {worker:{...worker,lifecycle_state:'stopping'}};throw new Error('stop barrier failed')};
    createRoot(document.getElementById('root')).render(<WorkerDetail workerId='worker_123' accountScopeId='acct' workspaceSlug='demo' onSelectWorker={w=>window.selected.push(w)} onClose={()=>{}}/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('heading', { name: 'Daily audit', exact: true }).waitFor()
    await page.getByText('Inspect daily').first().waitFor()
    await page.getByText('0 9 * * * (UTC)').waitFor()
    await page.getByText(/42 runs today \(UTC\) · 39 succeeded · 2 failed/).waitFor()
    await page.getByText(/Next eligible schedule: None reported/).waitFor()
    await page.getByText(/1 deliverable\(s\)/).waitFor()
    assert.equal(await page.getByTestId('durable-worker-run').getByRole('link', { name: 'Open execution session', exact: true }).getAttribute('href'), '/demo/session_1')
    assert.equal(await page.locator('form, input, textarea').count(), 0)
    await page.getByRole('group', { name: 'Execution mode', exact: true }).waitFor()
    assert.equal(await page.getByRole('region', { name: 'Action model', exact: true }).count(), 1)
    assert.equal(await page.getByRole('region', { name: 'Plan model', exact: true }).count(), 1)
    await page.getByRole('button', { name: 'Failed', exact: true }).click()
    assert.equal(await page.getByTestId('durable-worker-run').count(), 0)
    await page.getByRole('button', { name: 'In progress', exact: true }).click()
    assert.equal(await page.getByTestId('durable-worker-run').count(), 1)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await page.getByRole('button', { name: 'Pause worker' }).click()
    await page.getByRole('alert').getByText('stop barrier failed').waitFor()
    assert.deepEqual((await page.evaluate(() => (window as any).calls))[0], { action: 'pause', workerId: 'worker_123', expected_revision: 2 })
    await page.getByRole('button', { name: 'Stop run' }).click()
    await page.getByTestId('durable-worker-run').getByRole('alert').waitFor()
    assert.deepEqual((await page.evaluate(() => (window as any).calls))[1], { action: 'cancelRun', workerId: 'worker_123', runId: 'run_1' })
    await page.getByRole('button', { name: 'Ask Orchestrator' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).selected), [{ id: 'worker_123', revision: 2, name: 'Daily audit' }])
    assert.equal((await page.evaluate(() => (window as any).calls)).some((call: any) => call.action === 'direct' || call.action === 'create'), false)
    // Accepted stop requests must not be presented as acknowledged completion.
    await page.evaluate(() => { (window as any).stopping = true })
    await page.getByRole('button', { name: 'Pause worker', exact: true }).click()
    await page.getByRole('status').getByText(/Stopping: awaiting active run acknowledgement/).waitFor()
    assert.equal(await page.getByText('Lifecycle change confirmed.', { exact: true }).count(), 0)
    await page.evaluate(() => (window as any).invalidate())
    await page.getByText('Updating details; actions are unavailable until the current revision is loaded.').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Ask Orchestrator' }).isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: 'Pause worker' }).isDisabled(), true)
  } finally { await browser.close() }
})

// Requirement: list arrival alone never opens an anonymous first-worker detail;
// only route-provided identity renders details, including history changes.
// WorkerHub and canonical V3 cache are the narrowest rendered selection boundary.
test('worker hub requires URL identity before displaying a detail', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {WorkerHub} from './src/features/desktop/orchestrate/worker-hub';
    import {ensureDesktopSession} from './src/app/api';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    import {dispatchDesktopV3Cache,getDesktopV3CacheSnapshot} from './src/features/desktop/state/desktop-v3-cache-store';
    import {workerPageKey} from './src/features/desktop/state/desktop-workers-state';
    const worker={id:'worker_first',account_scope_id:'acct',name:'First',instructions:'Review',revision:1,lifecycle_state:'active',created_at:1,updated_at:1};
    const feed=(input,data)=>{const key=workerPageKey(input),requestId=crypto.randomUUID();dispatchDesktopV3Cache({type:'workers.begin',key,input,requestId});dispatchDesktopV3Cache({type:'workers.finish',key,requestId,generation:getDesktopV3CacheSnapshot().workerPages[key].generation,data})};
    feed({kind:'list',accountScopeId:'acct',limit:100},{workers:[worker]});
    desktopWorkers.acquire=()=>({ready:Promise.resolve(),release:()=>{}});
    window.inspected=[];
    function App(){const [id,setId]=useState();window.route=setId;return <WorkerHub initialWorkerId={id} onInspectWorker={id=>window.inspected.push(id)} onSelectWorker={()=>{}} onAddWorker={()=>{}}/>}
    ensureDesktopSession().then(()=>createRoot(document.getElementById('root')).render(<App/>));
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.request().url().endsWith('/v1/auth/desktop/session')
      ? route.fulfill({ contentType: 'application/json', body: JSON.stringify({ user_id: 'owner', account_scope_id: 'acct' }) })
      : route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: 'Inspect First' }).waitFor()
    assert.equal(await page.getByTestId('durable-worker-detail').count(), 0)
    await page.getByRole('button', { name: 'Inspect First' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).inspected), ['worker_first'])
    assert.equal(await page.getByTestId('durable-worker-detail').count(), 0)
    await page.evaluate(() => (window as any).route('worker_first'))
    await page.getByTestId('durable-worker-detail').waitFor()
    await page.evaluate(() => (window as any).route(undefined))
    await page.getByRole('heading', { name: 'A worker for the work that repeats' }).waitFor()
    assert.equal(await page.getByTestId('durable-worker-detail').count(), 0)
  } finally { await browser.close() }
})
