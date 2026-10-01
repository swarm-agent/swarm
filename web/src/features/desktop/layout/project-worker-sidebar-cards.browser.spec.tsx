import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: project cards stay visible regardless of the old collapse preference,
// isolate account/project records, prioritize reviews, and never dispatch workers.
// Boundary: ProjectWorkerSidebar + real V3 cache + navigation preferences. Only
// network acquisition is replaced; rendered browser interactions are the narrowest
// layer proving keyboard inspection, hide/restore, partial and failure presentation.
test('project worker cards are ungated, scoped, review-first and presentation-only', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {ProjectWorkerSidebar} from './src/features/desktop/layout/project-worker-sidebar';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    import {dispatchDesktopV3Cache,getDesktopV3CacheSnapshot} from './src/features/desktop/state/desktop-v3-cache-store';
    import {workerPageKey} from './src/features/desktop/state/desktop-workers-state';
    window.inspected=[]; window.browsed=0; window.mutations=[];
    desktopWorkers.acquire=()=>({ready:Promise.resolve(),release:()=>{}});
    desktopWorkers.mutate=async input=>{window.mutations.push(input);throw Error('Unexpected mutation')};
    localStorage.setItem('swarm:worker-navigation:acct:project-a',JSON.stringify({collapsed:true,hidden:['review']}));
    const root=createRoot(document.getElementById('root'));
    window.renderSidebar=(projectId='project-a',showActivity=false)=>root.render(<ProjectWorkerSidebar accountScopeId='acct' projectId={projectId} showActivity={showActivity} onInspect={id=>window.inspected.push(id)} onBrowse={()=>window.browsed++}/>);
    const input={kind:'list',accountScopeId:'acct',limit:100};
    let request=0;
    window.feed=(pending,data,error)=>{const read=pending?{...input,lifecycleState:'pending'}:input; const key=workerPageKey(read); const requestId=String(++request);
      dispatchDesktopV3Cache({type:'workers.begin',key,input:read,requestId});
      dispatchDesktopV3Cache({type:'workers.finish',key,requestId,generation:getDesktopV3CacheSnapshot().workerPages[key].generation,data,error});};
    window.invalidate=()=>dispatchDesktopV3Cache({type:'workers.invalidate',accountScopeId:'acct'});
    const worker=(id,extra={})=>({id,name:id,account_scope_id:'acct',metadata:{project_id:'project-a'},instructions:'',lifecycle_state:'active',revision:1,created_at:10,updated_at:10,...extra});
    window.populate=()=>{window.feed(false,{workers:[worker('ordinary'),worker('other-project',{metadata:{project_id:'project-b'}}),worker('other-account',{account_scope_id:'foreign'}),worker('update',{pending_review:worker('update')}),...Array.from({length:5},(_,i)=>worker('extra-'+i))],next_cursor:'opaque-list'});
      window.feed(true,{workers:[worker('review',{lifecycle_state:'pending',created_at:1})],next_cursor:'opaque-pending'});};
    window.renderSidebar();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 320, height: 700 } })
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://sidebar.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByText('Loading workers…', { exact: true }).waitFor()
    await page.evaluate(() => (window as any).populate())
    await page.getByRole('button', { name: 'Inspect ordinary', exact: true }).waitFor()
    assert.equal(await page.locator('[aria-expanded], details, summary').count(), 0)
    assert.equal(await page.getByRole('button', { name: /Expand|Collapse/ }).count(), 0)
    assert.equal(await page.getByText('other-project', { exact: true }).count(), 0)
    assert.equal(await page.getByText('other-account', { exact: true }).count(), 0)
    assert.equal(await page.getByRole('listitem').count(), 5)
    assert.match(await page.getByRole('listitem').first().innerText(), /^update\nPending approval/)
    // Hidden proposals remain discoverable via the independent approval action.
    await page.getByRole('button', { name: '2+ pending approval', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).inspected), ['update'])
    await page.getByRole('button', { name: 'Show hidden workers (1)', exact: true }).click()
    await page.getByRole('button', { name: 'Restore review in sidebar', exact: true }).click()
    await page.getByRole('button', { name: 'Inspect review', exact: true }).focus()
    await page.keyboard.press('Enter')
    assert.deepEqual(await page.evaluate(() => (window as any).inspected), ['update', 'review'])
    await page.getByRole('button', { name: 'Hide review in sidebar', exact: true }).click()
    assert.equal(await page.getByRole('button', { name: 'Inspect review', exact: true }).count(), 0)
    await page.getByRole('button', { name: 'Browse all workers · sidebar is partial', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).browsed), 1)
    // Reusing the component for another project cannot leak this project's cards.
    await page.evaluate(() => (window as any).renderSidebar('project-b'))
    await page.getByRole('button', { name: 'Inspect other-project', exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Inspect ordinary', exact: true }).count(), 0)
    await page.evaluate(() => (window as any).renderSidebar())
    await page.getByRole('button', { name: 'Inspect ordinary', exact: true }).waitFor()
    await page.evaluate(() => (window as any).invalidate())
    await page.getByRole('status').getByText('Last-known workers · may be out of date', { exact: true }).waitFor()
    await page.evaluate(() => (window as any).feed(false, undefined, 'Read failed'))
    await page.getByRole('alert').getByText('Workers unavailable: Read failed', { exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Inspect ordinary', exact: true }).count(), 1)
    assert.equal(await page.getByText('No workers on this page', { exact: true }).count(), 0)
    await page.evaluate(() => { (window as any).feed(false, { workers: [] }); (window as any).feed(true, { workers: [] }) })
    await page.getByText('No workers on this page', { exact: true }).waitFor()
    assert.equal(await page.getByRole('listitem').count(), 0)
    assert.equal(await page.getByRole('button', { name: /sidebar is partial/ }).count(), 0)
    assert.deepEqual(await page.evaluate(() => (window as any).mutations), [])
  } finally { await browser.close() }
})
