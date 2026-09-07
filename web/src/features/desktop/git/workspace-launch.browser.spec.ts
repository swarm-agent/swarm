import assert from 'node:assert/strict'
import { test } from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: explicit default is not first-row selection; retained dirty status
// stays selectable after transport replacement and a late response. Authority:
// SessionRepositoryInventory generation/abort guard and SessionRepositoryPicker.
// Hermetic component/browser proof, NOT full-app websocket or attachment-dialog proof.
test('launch retained dirty selection survives reconnect and late response', { timeout: 30_000 }, async () => {
  const bundle = await build({ stdin: { contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {SessionRepositoryPicker} from './src/features/desktop/git/session-repository-picker';
    import {SessionRepositoryInventory,repositoryKey} from './src/features/desktop/state/session-repositories';
    const base={workspace_name:'Same display name',source_path:'/fixture',attached:true,branch:'dev',base_commit:'',retained:true,availability:'available',files_truncated:false,lifecycle:'active',status:{has_git:true,staged_count:1,modified_count:2,untracked_count:3,conflict_count:1}};
    const a={...base,id:'a',session_id:'parent',workspace_id:'a',workspace_path:'/fixture/a',kind:'parent',default:false};
    const b={...base,id:'b',session_id:'parent',workspace_id:'b',source_path:'/fixture/b',workspace_path:'/fixture/b',kind:'source',default:true};
    const c={...base,id:'c',session_id:'worker',workspace_id:'b',source_path:'/fixture/b',workspace_path:'/fixture/worker-b',kind:'worker',default:false,lifecycle:'failed',attached:false};
    let pending, calls=0; const page=(items)=>({ok:true,items,history_coverage:'retained'});
    const inventory=new SessionRepositoryInventory(async()=>{ calls++; if(calls===2)return new Promise(resolve=>pending=resolve); return page([a,b,c]); });
    function Fixture(){const state=React.useSyncExternalStore(inventory.subscribe,inventory.snapshot); return <><SessionRepositoryPicker inventory={state} onSelect={inventory.select} onRefresh={inventory.refresh} onLoadMore={inventory.loadMore}/><button onClick={()=>{inventory.dispose();inventory.refresh()}}>Reconnect fixture</button><button onClick={()=>pending(page([{...a,branch:'STALE RESPONSE'}]))}>Deliver late response</button><output>{state.selectedKey}</output></>}
    createRoot(document.getElementById('root')).render(<Fixture/>); inventory.refresh();
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', define: { 'process.env.NODE_ENV': '"test"' } })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  try {
    const page = await browser.newPage({ viewport: { width: 420, height: 800 } })
    await page.route('**/*', route => route.abort())
    await page.setContent('<style>body{background:#131923;color:#edf2fa;font:14px system-ui;margin:20px}button{background:#202b3c;color:inherit;border:1px solid #60718e;border-radius:5px;padding:10px;margin:5px;cursor:pointer}button[aria-pressed=true]{outline:2px solid #7bc8ff}fieldset{margin:10px 0;min-width:0}span{display:block;overflow-wrap:anywhere}output{display:block;font-size:11px;overflow-wrap:anywhere}</style><div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const worker = page.getByRole('button', { name: /worker · dev/ })
    await worker.waitFor()
    const explicitDefault = page.getByRole('button', { name: /source · dev · Default/ })
    assert.equal(await explicitDefault.count(), 1, 'default badge uses explicit identity, not row order')
    // Review selection is separate from execution default; retained workers are
    // not attached/default rows in the production API.
    await explicitDefault.click()
    assert.equal(await explicitDefault.getAttribute('aria-pressed'), 'true')
    assert.equal(await worker.getAttribute('aria-pressed'), 'false')
    await worker.click()
    assert.match(await worker.innerText(), /1 staged · 2 unstaged · 3 untracked · 1 conflicts/)
    assert.match(await worker.innerText(), /failed · Retained/)
    const selected = await page.locator('output').textContent()
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await page.getByRole('status').filter({ hasText: 'Loading' }).waitFor()
    await page.getByRole('button', { name: 'Reconnect fixture', exact: true }).click()
    await page.getByRole('button', { name: 'Refresh', exact: true }).waitFor({ state: 'visible' })
    await page.waitForFunction(() => !document.body.textContent?.includes('Loading repositories'))
    await page.getByRole('button', { name: 'Deliver late response', exact: true }).click()
    assert.equal(await page.locator('output').textContent(), selected)
    assert.equal(await page.getByText('STALE RESPONSE', { exact: false }).count(), 0)
    assert.equal(await worker.getAttribute('aria-pressed'), 'true')
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
    if (process.env.SWARM_TEST_SCREENSHOT_DIR) await page.screenshot({ path: process.env.SWARM_TEST_SCREENSHOT_DIR + '/workspace-launch-retained.png', fullPage: true })
  } finally { await browser.close() }
})
