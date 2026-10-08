import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: SessionPermissionAttention must repair request details after reconnect
// even if the cached summary remains zero. The real hook/cache and intercepted
// hydrate boundary prove stale detail is not accepted as absence of ask-user.
// No permission decision is bypassed; provider resumption needs live validation.
test('reconnect reloads same-summary parent permission details', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {SessionPermissionAttention} from './src/features/desktop/orchestrate/task-attention';
    import {dispatchDesktopV3Cache} from './src/features/desktop/state/desktop-v3-cache-store';
    import {hydrateResponseToAction} from './src/features/desktop/state/desktop-v3-cache-wire';
    const snapshot=(pending=[])=>({sessions_by_id:{parent:{id:'parent',account_scope_id:'account',user_id:'user'}},session_views_by_id:{parent:{pending_permissions:pending,has_active_plan:false}},permission_summaries_by_session:{parent:{pending_approval_count:0}},selector:{kind:'session_ids',session_ids:['parent']},scope_id:'review',snapshot_endpoint_cursor:'opaque',sync_scope:{surface:'desktop',stream_kind:'v3.sync.snapshot',selector_filter_hash:'review',resource_set:'session_view,permission_summaries'}});
    dispatchDesktopV3Cache(hydrateResponseToAction(snapshot(),['parent']));window.reads=0;
    window.fetch=async(input)=>{const url=String(input);if(url.includes('/auth/desktop/session'))return new Response(JSON.stringify({user_id:'user',account_scope_id:'account'}));if(!url.endsWith('/v3/sync/hydrate'))throw Error('Unexpected '+url);window.reads++;return new Response(JSON.stringify(snapshot([{id:'ask',session_id:'parent',run_id:'run',call_id:'call',tool_name:'ask_user',tool_arguments:JSON.stringify({questions:[{id:'q',question:'Choose direction',options:['A','B']}]}),status:'pending',created_at:1,updated_at:1}])))};
    window.reconnect=()=>dispatchDesktopV3Cache({type:'reconnect.applySnapshot',snapshot:{sessions_by_id:{parent:{id:'parent',account_scope_id:'account',user_id:'user'}},snapshot_endpoint_cursor:'reconnected'}});
    createRoot(document.getElementById('root')).render(<SessionPermissionAttention sessionId='parent'/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://permission.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    // Let passive effects install the reconnect subscription before dispatch.
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
    const before = await page.evaluate(() => (window as any).reads)
    await page.evaluate(() => (window as any).reconnect())
    await page.waitForFunction(count => (window as any).reads > count, before)
    await page.getByRole('button', { name: 'Submit response', exact: true }).waitFor()
    for (const name of ['A', 'B', 'Custom response']) assert.equal(await page.getByRole('button', { name, exact: true }).count(), 1)
    assert.equal(await page.getByRole('dialog').count(), 0)
    assert.match(await page.locator('#root').innerText(), /Choose direction/)
  } finally { await browser.close() }
})
