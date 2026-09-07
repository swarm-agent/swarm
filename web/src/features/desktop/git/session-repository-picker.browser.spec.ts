import assert from 'node:assert/strict'
import { test } from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: users can select a retained worker without selecting the default
// source; errors remain visible. Real DOM clicks exercise the picker, not source
// strings. Hermetic browser fixture; requires installed Playwright Chromium.
test('browser repository selection remains explicit across default changes', { timeout: 30_000 }, async () => {
  const bundle = await build({ stdin: { contents: `
    import React from 'react';
    import { createRoot } from 'react-dom/client';
    import { SessionRepositoryPicker } from './src/features/desktop/git/session-repository-picker';
    import { repositoryKey } from './src/features/desktop/state/session-repositories';
    const base = {workspace_id:'project',workspace_name:'Project',source_path:'/project',attached:true,branch:'branch',base_commit:'',retained:true,availability:'available',files_truncated:false};
    const source = {...base,id:'source',session_id:'parent',workspace_path:'/project',kind:'source',default:true,lifecycle:'active'};
    const worker = {...base,id:'worker',session_id:'worker',workspace_path:'/worker',kind:'worker',default:false,lifecycle:'failed'};
    function Fixture() {
      const [state,setState] = React.useState({items:[source,worker],selectedKey:repositoryKey(source),loading:false,stale:false,error:'',nextCursor:'cursor',historyCoverage:'retained'});
      return <><SessionRepositoryPicker inventory={state} onSelect={key=>setState(s=>({...s,selectedKey:key}))} onRefresh={()=>setState(s=>({...s,items:s.items.map(r=>({...r,default:!r.default}))}))} onLoadMore={()=>setState(s=>({...s,stale:true,error:'Authorization expired'}))}/><output>{state.selectedKey}</output></>;
    }
    createRoot(document.getElementById('root')).render(<Fixture/>);
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', define: { 'process.env.NODE_ENV': '"test"' } })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 360, height: 640 } })
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: /worker · branch/ }).click()
    const selected = await page.locator('output').textContent()
    assert.ok(selected?.includes('worker'))
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    assert.equal(await page.locator('output').textContent(), selected)
    await page.getByRole('button', { name: 'Load more repositories' }).click()
    assert.equal(await page.getByRole('alert').textContent(), 'Authorization expired')
    assert.equal(await page.getByRole('button', { name: 'Load more repositories' }).isDisabled(), true)
  } finally { await browser.close() }
})
