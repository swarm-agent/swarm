import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: the composer uses TaskAttention's canonical review controls. Exercise
// keyboard review, exact session/request routing, busy/failure/retry, hydration
// recovery and stale selection invalidation against the real modal/cache/API
// adapter. Only HTTP responses are intercepted; no source or provider is accessed.
// This rendered component boundary proves interactions that string tests cannot.
test('parent source review routes one-time decisions and survives recovery safely', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {TaskAttention} from './src/features/desktop/orchestrate/task-attention';
    import {taskAttentionPermissions} from './src/features/desktop/state/task-attention';
    import {dispatchDesktopV3Cache,getDesktopV3CacheSnapshot,useDesktopV3CacheSelector} from './src/features/desktop/state/desktop-v3-cache-store';
    import {hydrateResponseToAction} from './src/features/desktop/state/desktop-v3-cache-wire';
    const wire=(id,session='parent',revision=1)=>({id,session_id:session,run_id:'run',call_id:'call',tool_name:'read',tool_arguments:JSON.stringify({path:'/workspace/'+id+'.svg',critical:true,purpose:'Capture source bytes for delegated Designer provider context'}),requirement:'design_source_sensitive_read',mode:'ask',status:'pending',created_at:1,updated_at:revision});
    window.hydrate=(items=[wire('first'),wire('second')])=>dispatchDesktopV3Cache(hydrateResponseToAction({sessions_by_id:{parent:{id:'parent',account_scope_id:'account',user_id:'user'},other:{id:'other',account_scope_id:'account',user_id:'user'}},session_views_by_id:{parent:{pending_permissions:items},other:{pending_permissions:[wire('foreign','other')]}},selector:{kind:'session_ids',session_ids:['parent','other']},scope_id:'review',snapshot_endpoint_cursor:'review-cursor',sync_scope:{surface:'desktop',stream_kind:'v3.sync.snapshot',selector_filter_hash:'review',resource_set:'session_view'}},['parent','other']));
    window.hydrate();window.requests=[];window.fail=true;window.finish=null;
    window.fetch=async(input,init)=>{const url=String(input);if(!url.includes('/permissions/')||!url.endsWith('/resolve'))throw Error('Unexpected network '+url);
      const body=JSON.parse(init.body);window.requests.push({url,body});await new Promise(resolve=>window.finish=resolve);
      if(window.fail){window.fail=false;return new Response(JSON.stringify({error:'Decision unavailable; retry'}),{status:503})}
      const id=url.split('/').at(-2);return new Response(JSON.stringify({permission:{...wire(id),status:body.action==='deny'?'denied':'approved',decision:body.action,updated_at:10,resolved_at:10}}),{status:200});};
    function Review({sessionId}){const permissions=useDesktopV3CacheSelector(s=>taskAttentionPermissions(s,[sessionId]),(a,b)=>a.length===b.length&&a.every((p,i)=>p===b[i]));return <TaskAttention attention={{permissions,unresolvedCount:permissions.length,error:'',retry:()=>{}}}/>}
    const root=createRoot(document.getElementById('root'));window.mount=(id='parent')=>root.render(<Review key={id} sessionId={id}/>);window.unmount=()=>root.render(null);window.mount();
    window.expire=()=>{const p=getDesktopV3CacheSnapshot().permissionsBySession.parent.find(p=>p.id==='second');dispatchDesktopV3Cache({type:'permission.resolveResult',sessionId:'parent',permissionId:'second',permission:{...p,status:'expired',updatedAt:5,resolvedAt:5}})};`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://review.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const reviews = page.getByRole('button', { name: 'Review permission', exact: true })
    await reviews.first().waitFor()
    assert.equal(await reviews.count(), 2)
    assert.equal(await page.getByText(/foreign.svg/).count(), 0)
    await reviews.first().focus()
    await page.keyboard.press('Enter')
    const dialog = page.getByRole('dialog')
    await dialog.waitFor()
    assert.match(await dialog.innerText(), /first.svg/)
    assert.match(await dialog.innerText(), /delegated Designer provider/)
    assert.equal(await dialog.getByRole('button', { name: /Always allow|Always deny/i }).count(), 0)
    const approve = dialog.getByRole('button', { name: /^Approve/ })
    await approve.click()
    await page.waitForFunction(() => !!(window as any).finish)
    assert.equal(await approve.isDisabled(), true)
    assert.equal(await reviews.count(), 2, 'submission alone does not clear requests')
    await page.evaluate(() => (window as any).finish())
    await dialog.getByRole('alert').waitFor()
    assert.match(await dialog.getByRole('alert').innerText(), /retry/i)
    await approve.click()
    await page.waitForFunction(() => (window as any).requests.length === 2)
    await page.evaluate(() => (window as any).finish())
    await dialog.waitFor({ state: 'hidden' }).catch(async error => {
      throw new Error(`${error.message}\nRendered review: ${await dialog.innerText()}\nBrowser errors: ${errors.join('; ')}`)
    })
    assert.equal(await reviews.count(), 1)
    const requests = await page.evaluate(() => (window as any).requests)
    assert(requests.every((r: any) => r.url.endsWith('/v3/sessions/parent/permissions/first/resolve')))
    assert.deepEqual(requests.map((r: any) => r.body), [{ action: 'approve', reason: '' }, { action: 'approve', reason: '' }])
    await reviews.first().click()
    await page.evaluate(() => (window as any).mount('other'))
    await dialog.waitFor({ state: 'hidden' })
    assert.match(await page.locator('#root').innerText(), /foreign.svg/)
    await page.evaluate(() => (window as any).mount())
    await reviews.first().click()
    await page.evaluate(() => (window as any).hydrate([{id:'second',session_id:'parent',tool_name:'read',tool_arguments:JSON.stringify({path:'/workspace/changed.svg'}),requirement:'design_source_sensitive_read',mode:'ask',status:'pending',created_at:1,updated_at:4}]))
    await dialog.waitFor({ state: 'hidden' })
    assert.equal(await page.evaluate(() => (window as any).requests.length), 2, 'changed source requires a new review')
    await reviews.first().click()
    assert.match(await dialog.innerText(), /changed.svg/)
    await page.evaluate(() => (window as any).expire())
    await dialog.waitFor({ state: 'hidden' })
    assert.equal(await reviews.count(), 0, 'expired requests cannot be decided')
    assert.equal(await page.evaluate(() => (window as any).requests.length), 2)
    // A fresh pending revision arriving through canonical hydration is recoverable
    // after navigation/remount; an old resolved request is not resurrected.
    await page.evaluate(() => { (window as any).unmount(); (window as any).hydrate([{id:'third',session_id:'parent',tool_name:'read',tool_arguments:JSON.stringify({path:'/workspace/third.svg',purpose:'Designer provider context'}),requirement:'design_source_sensitive_read',mode:'ask',status:'pending',created_at:6,updated_at:6}]); (window as any).mount() })
    await reviews.first().click()
    await dialog.getByRole('button', { name: /^Deny/ }).click()
    await page.waitForFunction(() => (window as any).requests.length === 3)
    await page.evaluate(() => (window as any).finish())
    await dialog.waitFor({ state: 'hidden' })
    assert.equal(await reviews.count(), 0)
    const last = await page.evaluate(() => (window as any).requests.at(-1))
    assert.match(last.url, /parent\/permissions\/third\/resolve$/)
    assert.equal(last.body.action, 'deny')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
