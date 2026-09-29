import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: The V3 hub reads the canonical worker cache and exposes revision-guarded
// actions and paged runs. Threat: stale list/status, invented success on failed stop,
// silent loss of run errors/links, or unconfirmed destructive actions.
// Boundary: WorkerHub/WorkerDetail -> desktopWorkers runtime -> V3 cache. A real
// rendered component is the narrowest layer for the interactive states; server tests
// separately prove account isolation, mutation effects, and stop barriers.
test('durable worker detail renders runs and failures; controls require confirmation and preserve server errors', { timeout: 30000 }, async () => {
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
    await page.getByText('Daily audit').waitFor()
    await page.getByText('Inspect daily').waitFor()
    await page.getByText('0 9 * * * (UTC)').waitFor()
    await page.getByText(/Total 42 runs · 39 succeeded · 2 failed/).first().waitFor()
    await page.getByText(/Next eligible schedule: none reported/).first().waitFor()
    await page.getByText('Deliverable reference:').waitFor()
    assert.equal(await page.getByTestId('durable-worker-run').getByRole('link', { name: 'Open execution session session_1', exact: true }).getAttribute('href'), '/demo/session_1')
    assert.equal(await page.getByTestId('durable-worker-run').getByRole('link', { name: 'Open source session', exact: true }).getAttribute('href'), '/demo/session_1')
    await page.getByRole('button', { name: 'Archive…' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await page.getByRole('button', { name: 'Keep worker' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await page.getByRole('button', { name: 'Pause' }).click()
    await page.getByRole('alert').getByText('stop barrier failed').waitFor()
    assert.deepEqual((await page.evaluate(() => (window as any).calls))[0], { action: 'pause', workerId: 'worker_123', expected_revision: 2 })
    await page.getByRole('button', { name: 'Delete…' }).click()
    assert.equal((await page.evaluate(() => (window as any).calls)).length, 1)
    await page.getByRole('button', { name: 'Confirm delete' }).click()
    await page.getByRole('alert').getByText('stop barrier failed').waitFor()
    assert.deepEqual((await page.evaluate(() => (window as any).calls))[1], { action: 'delete', workerId: 'worker_123', expected_revision: 2 })
    await page.getByRole('group', { name: 'Confirm delete worker' }).waitFor()
    await page.getByRole('button', { name: 'Request run cancellation' }).click()
    await page.getByRole('alert').getByText('stop barrier failed').waitFor()
    assert.deepEqual((await page.evaluate(() => (window as any).calls))[2], { action: 'cancelRun', workerId: 'worker_123', runId: 'run_1' })
    await page.getByRole('button', { name: 'Select for next Orchestrator message' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).selected), [{ id: 'worker_123', revision: 2, name: 'Daily audit' }])
    // A transport failure keeps the admission key for an identical retry; edits create a new intent.
    await page.getByLabel('Explicit task prompt').fill('Review this change')
    await page.getByRole('button', { name: 'Send task (starts a run)', exact: true }).click()
    await page.getByRole('button', { name: 'Send task (starts a run)', exact: true }).click()
    let calls = await page.evaluate(() => (window as any).calls)
    const direct = calls.filter((call: any) => call.action === 'direct')
    assert.equal(direct.length, 2)
    assert.equal(direct[0].idempotency_key, direct[1].idempotency_key)
    await page.getByLabel('Explicit task prompt').fill('Review a different change')
    await page.getByRole('button', { name: 'Send task (starts a run)', exact: true }).click()
    calls = await page.evaluate(() => (window as any).calls)
    assert.notEqual(calls.at(-1).idempotency_key, direct[0].idempotency_key)
    // Accepted stop requests must not be presented as acknowledged completion.
    await page.evaluate(() => { (window as any).stopping = true })
    await page.getByRole('button', { name: 'Pause', exact: true }).click()
    await page.getByRole('status').getByText(/Stopping: cancellation is awaiting acknowledgement/).waitFor()
    assert.equal(await page.getByText('Lifecycle change confirmed.', { exact: true }).count(), 0)
    await page.evaluate(() => (window as any).invalidate())
    await page.getByText('Detail refreshing; do not act on stale revisions.').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Select for next Orchestrator message' }).isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: 'Pause' }).isDisabled(), true)
  } finally { await browser.close() }
})
