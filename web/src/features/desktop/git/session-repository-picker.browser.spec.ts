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
    const source = {...base,id:'source',session_id:'parent',workspace_path:'/project',kind:'parent',default:true,active:true,lifecycle:'active'};
    const retained = {...source,id:'retained',workspace_path:'/project/old',default:false,active:false};
    const program = {...base,id:'program',session_id:'parent',workspace_id:'extra',workspace_name:'Project',source_path:'/extra',workspace_path:'/extra/program',kind:'lane',program_id:'program-one',job_id:'job-one',default:false,active:false};
    const worker = {...base,id:'worker',session_id:'worker',workspace_path:'/worker',kind:'worker',default:false,lifecycle:'failed'};
    function Fixture() {
      const [state,setState] = React.useState({items:[source,retained,program,worker],selectedKey:repositoryKey(source),loading:false,stale:false,error:'',nextCursor:'cursor',historyCoverage:'retained'});
      return <><SessionRepositoryPicker inventory={state} onSelect={key=>setState(s=>({...s,selectedKey:key}))} onRefresh={()=>setState(s=>({...s,items:s.items.map(r=>({...r,default:!r.default}))}))} onLoadMore={()=>setState(s=>({...s,stale:true,error:'Authorization expired'}))}/><output>{state.selectedKey}</output></>;
    }
    createRoot(document.getElementById('root')).render(<Fixture/>);
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', define: { 'process.env.NODE_ENV': '"test"' } })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined, executablePath: process.env.SWARM_TEST_BROWSER_EXECUTABLE || undefined })
  try {
    const page = await browser.newPage({ viewport: { width: 360, height: 640 } })
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    assert.equal(await page.locator('fieldset').count(), 2)
    assert.equal(await page.getByText('Active execution lane', { exact: false }).count(), 1)
    assert.equal(await page.getByText('Program program-one · Job job-one').count(), 1)
    assert.equal(await page.getByRole('button', { name: /^parent · branch/ }).count(), 2)
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

// Requirement: real state-driven navigation reaches late workers while keeping
// rendered rows bounded and an evicted selection explicit. Styled hermetic DOM
// fixture also supports parent-owned pixel inspection at both viewport sizes.
test('styled repository windows reach late workers without implicit retargeting', { timeout: 60_000 }, async () => {
  const { build: viteBuild } = await import('vite')
  const { default: react } = await import('@vitejs/plugin-react')
  const { default: tailwindcss } = await import('@tailwindcss/vite')
  const result = await viteBuild({ configFile: false, logLevel: 'error', plugins: [react(), tailwindcss(), {
    name: 'repository-fixture',
    resolveId(id) { if (id === 'virtual:repository-fixture') return '\0repository-fixture' },
    load(id) { if (id === '\0repository-fixture') return `
      import React from 'react'; import {createRoot} from 'react-dom/client';
      import {SessionRepositoryPicker} from '${process.cwd()}/src/features/desktop/git/session-repository-picker.tsx';
      import {SessionRepositoryInventory} from '${process.cwd()}/src/features/desktop/state/session-repositories.ts';
      import '${process.cwd()}/src/theme.css';
      const inventory = new SessionRepositoryInventory(async cursor => {
        const offset = Number(cursor || 0);
        return {ok:true,history_coverage:'retained',next_cursor:offset < 440 ? String(offset+20) : '',items:Array.from({length:20},(_,i)=>({
          id:'row-'+(offset+i),session_id:'owner-'+(offset+i),workspace_id:'workspace',workspace_name:'Project',source_path:'/project',workspace_path:'/project/worker-'+(offset+i),
          kind:'worker',branch:'branch-'+(offset+i),attached:false,default:false,retained:true,lifecycle:'failed',availability:'unavailable',error:'Retained worker',files_truncated:false
        }))};
      });
      function Fixture(){ const state=React.useSyncExternalStore(inventory.subscribe,inventory.snapshot); return React.createElement(SessionRepositoryPicker,{inventory:state,onSelect:inventory.select,onRefresh:inventory.refresh,onLoadMore:inventory.loadMore}); }
      createRoot(document.getElementById('root')).render(React.createElement(Fixture)); inventory.refresh();
    ` },
  }], build: { write: false, minify: false, rolldownOptions: { input: 'virtual:repository-fixture', output: { inlineDynamicImports: true } } } })
  assert.ok(!Array.isArray(result) && 'output' in result)
  const js = result.output.filter(item => item.type === 'chunk').map(item => item.code).join('\n')
  const css = result.output.filter(item => item.type === 'asset' && item.fileName.endsWith('.css')).map(item => String(item.source)).join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined, executablePath: process.env.SWARM_TEST_BROWSER_EXECUTABLE || undefined })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.abort())
    await page.setContent('<div id="root" style="max-width:360px;padding:12px;box-sizing:border-box"></div>')
    await page.addStyleTag({ content: css }); await page.addScriptTag({ content: js, type: 'module' })
    await page.getByRole('button', { name: /worker · branch-0 / }).click()
    for (let i = 1; i < 23; i++) {
      await page.getByRole('button', { name: 'Load more repositories', exact: true }).click()
      await page.getByRole('button', { name: new RegExp('worker · branch-' + (i * 20) + ' ') }).waitFor()
      assert.ok(await page.locator('button[aria-pressed]').count() <= 200)
    }
    assert.equal(await page.getByRole('button', { name: /worker · branch-459 / }).count(), 1)
    assert.equal(await page.locator('button[aria-pressed="true"]').count(), 0)
    await page.getByRole('status').filter({ hasText: 'Selected repository is not' }).waitFor()
    for (const width of [375, 1440]) {
      await page.setViewportSize({ width, height: 800 })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
      const region = page.getByTestId('repository-scroll-region')
      assert.ok((await region.boundingBox())!.height <= 321)
      assert.equal(await region.evaluate(el => el.scrollHeight > el.clientHeight), true)
      await region.evaluate(el => { el.scrollTop = 0 })
      if (process.env.SWARM_TEST_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.SWARM_TEST_SCREENSHOT_DIR}/repositories-${width}.png` })
    }
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await page.locator('button[aria-pressed="true"]').waitFor()
    assert.match(await page.locator('button[aria-pressed="true"]').innerText(), /branch-0/)
  } finally { await browser.close() }
})

// Requirement: recovery-driven refresh keeps exact inspection selection in the
// real picker, with dirty duplicate labels and an explicit missing-row warning.
// The only transport is a read fixture: selecting a lane cannot adopt it.
test('recovery refresh preserves exact browser inspection and missing selection', { timeout: 30_000 }, async () => {
  const bundle = await build({ stdin: { contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {SessionRepositoryPicker} from './src/features/desktop/git/session-repository-picker';
    import {SessionRepositoryInventory,repositoryEventInvalidates,scheduleRepositoryRefresh} from './src/features/desktop/state/session-repositories';
    const base={session_id:'parent',workspace_id:'project',workspace_name:'Project',source_path:'/project',kind:'parent',branch:'same',attached:true,default:false,base_commit:'base',retained:true,availability:'available',files_truncated:false,lifecycle:'retained',status:{has_git:true,staged_count:1,modified_count:2,untracked_count:3,conflict_count:0}};
    const old={...base,id:'old',workspace_path:'/project/old',active:false};
    const current={...base,id:'current',workspace_path:'/project/current',active:true};
    let rows=[old,current];
    const inventory=new SessionRepositoryInventory(async()=>({ok:true,items:rows,history_coverage:'retained'}));
    const scheduler=scheduleRepositoryRefresh(inventory,()=>true);
    function recover(){ rows=[{...current,active:false},{...base,id:'copy',workspace_path:'/project/copy',active:true}];
      const action={type:'realtime.applyEvent',event:{source:'realtime',sessionId:'parent',eventType:'session.worktree.copied',payload:{}}};
      if(repositoryEventInvalidates(action,new Set(['parent']))) scheduler.invalidate();
    }
    function Fixture(){const state=React.useSyncExternalStore(inventory.subscribe,inventory.snapshot);return <><SessionRepositoryPicker inventory={state} onSelect={inventory.select} onRefresh={inventory.refresh} onLoadMore={inventory.loadMore}/><button onClick={recover}>Publish recovery</button></>;}
    createRoot(document.getElementById('root')).render(<Fixture/>); inventory.refresh();
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', define: { 'process.env.NODE_ENV': '"test"' } })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined, executablePath: process.env.SWARM_TEST_BROWSER_EXECUTABLE || undefined })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.abort())
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const current = page.getByRole('button', { name: 'parent · same · /project/current', exact: true })
    await current.waitFor()
    assert.equal(await current.getAttribute('aria-pressed'), 'true')
    const old = page.getByRole('button', { name: 'parent · same · /project/old', exact: true })
    await old.click()
    assert.equal(await old.getAttribute('aria-pressed'), 'true')
    assert.equal(await current.getAttribute('aria-pressed'), 'false')
    assert.equal(await page.getByText('Active execution lane', { exact: false }).count(), 1)
    await page.getByRole('button', { name: 'Publish recovery', exact: true }).click()
    await page.getByRole('button', { name: 'parent · same · /project/copy', exact: true }).waitFor()
    assert.equal(await page.locator('button[aria-pressed="true"]').count(), 0)
    assert.equal(await page.getByText('Selected repository is not in the loaded inventory.', { exact: false }).count(), 1)
    assert.equal(await page.getByText('1 staged · 2 unstaged · 3 untracked · 0 conflicts', { exact: true }).count(), 2)
    await current.click()
    assert.equal(await current.getAttribute('aria-pressed'), 'true')
    assert.equal(await page.getByText('Active execution lane', { exact: false }).count(), 1)
  } finally { await browser.close() }
})
