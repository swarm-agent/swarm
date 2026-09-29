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
    const worker={id:'worker_123', account_scope_id:'acct',name:'Daily audit',instructions:'Inspect daily',revision:2,lifecycle_state:'active',created_at:1,updated_at:2,automations:[{id:'wauto_1',worker_id:'worker_123',name:'Review',revision:1,enabled:true,activation_mode:'cron',schedule:{kind:'cron',cron:'0 9 * * *',timezone:'UTC'},plan_document:{title:'Audit',checkpoints:[{id:'cp-1',title:'Check'}]}}]};
    const run={id:'run_1',worker_id:'worker_123',account_scope_id:'acct',worker_revision:2,request_source:'schedule',status:'running',session_id:'session_1',created_at:Date.now(),error:'warning',deliverables:[{label:'report'}]};
    const feed=(input,data)=>{const key=workerPageKey(input); dispatchDesktopV3Cache({type:'workers.begin',key,input,requestId:'req-'+input.kind});dispatchDesktopV3Cache({type:'workers.finish',key,requestId:'req-'+input.kind,generation:0,data})};
    window.invalidate=()=>dispatchDesktopV3Cache({type:'workers.invalidate',workerId:'worker_123',accountScopeId:'acct'});
    feed({kind:'detail',accountScopeId:'acct',workerId:'worker_123'},{worker});feed({kind:'runs',accountScopeId:'acct',workerId:'worker_123',limit:25},{runs:[run],next_cursor:'opaque'});feed({kind:'history',accountScopeId:'acct',workerId:'worker_123',limit:10},{revisions:[{worker_id:worker.id,account_scope_id:'acct',revision:2,worker,committed_at:Date.now(),change_summary:'Edited'}]});
    desktopWorkers.acquire=()=>({ready:Promise.resolve(),release:()=>{}});
    desktopWorkers.mutate=async (input)=>{window.calls.push(input);throw new Error('stop barrier failed')};
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
    await page.getByText(/Today .*1 run\(s\) on this page/).waitFor()
    await page.getByText(/Next scheduled run: not reported by the durable worker API/).waitFor()
    await page.getByText('Deliverable reference:').waitFor()
    assert.equal(await page.locator('a[href="/demo/session_1"]').count(), 1)
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
    await page.evaluate(() => (window as any).invalidate())
    await page.getByText('Detail refreshing; do not act on stale revisions.').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Select for next Orchestrator message' }).isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: 'Pause' }).isDisabled(), true)
  } finally { await browser.close() }
})
