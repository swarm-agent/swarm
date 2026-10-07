// Purpose: TaskListHeader/Toolbar must keep overflow Git states independent,
// toggle real source-linked task filtering, close on Esc/outside click, preserve
// editable slash input, support tab keys and fit narrow lanes. Actual components
// and production CSS are the narrowest proof of DOM interactions/geometry;
// finite status fixtures are not evidence of daemon Git or live provider health.
import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { readFile } from 'node:fs/promises'
import { chromium } from 'playwright'

test('task header supports workspace filtering, keyboard controls and narrow layout', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {TaskListHeader,TaskListToolbar} from './src/features/desktop/orchestrate/task-list-toolbar';
    import {taskMatchesWorkspace} from './src/features/desktop/runtime/use-project-workspace-git';
    const git={has_git:true,branch:'dev',dirty_count:0,ahead_count:0,behind_count:0,conflict_count:0};
    const workspaces=Array.from({length:6},(_,i)=>({key:'repo-'+i,id:'id-'+i,name:'repo-'+i,path:'/fixture/repo-'+i,loading:i===2,status:i===2?undefined:{...git,branch:i===3?'detached':'dev',dirty_count:i===0?3:0,ahead_count:i===0?2:0,behind_count:i===0?1:0},error:i===1?'Git read failed':undefined}));
    const tasks=[{id:'one',sourceWorkspaceId:'id-0'},{id:'two',sourceWorkspaceId:'id-1'}];
    function App(){const [workspace,setWorkspace]=useState(null),[status,setStatus]=useState('all'),[source,setSource]=useState('all'),[search,setSearch]=useState(''),[selected,setSelected]=useState(0);
      const visible=workspace?tasks.filter(task=>taskMatchesWorkspace(task,workspaces.find(row=>row.key===workspace))):tasks;
      return <main className="swarm-section swarm-main-panel"><TaskListHeader workspaces={workspaces} selectedWorkspace={workspace} onWorkspace={key=>setWorkspace(prev=>prev===key?null:key)} onRefreshGit={()=>{}} onNewTask={()=>{}}/>
      <TaskListToolbar search={search} onSearch={setSearch} source={source} onSource={setSource} status={status} onStatus={setStatus} counts={{all:visible.length,running:0,needs_review:0,queued:0,completed:0}} total={visible.length} selected={selected} busy={false} onSelectAll={()=>setSelected(visible.length)} onClear={()=>setSelected(0)} onArchive={()=>{}} onDelete={()=>{}} onArchived={()=>{}}/>
      <div aria-label="Visible fixture tasks">{visible.map(task=><p key={task.id}>{task.id}</p>)}</div><textarea aria-label="Outside draft"/><button>Outside</button></main>
    }createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1000, height: 800 } })
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setContent('<div id="root"></div>')
    await page.addStyleTag({ content: `*{box-sizing:border-box} body{margin:0} .swarm-section{--swarm-accent:coral;--swarm-surface:#301d28;--swarm-background-inset:#21131b;--swarm-text:#eee;--swarm-text-muted:#bbb;--swarm-border-muted:#51313d} .px-4{padding-inline:16px} .swarm-task-list-toolbar{min-width:0} .swarm-main-panel{container:swarm-main/inline-size;width:900px} ` + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const chips = page.locator('.swarm-workspace-chip')
    await chips.first().waitFor()
    assert.equal(await chips.count(), 3)
    assert.match(await chips.nth(0).innerText(), /↑2.*↓1/)
    assert.match(await chips.nth(1).innerText(), /Status unavailable/)
    assert.match(await chips.nth(2).innerText(), /Loading…/)
    await chips.first().click()
    assert.equal(await page.locator('[aria-label="Visible fixture tasks"]').innerText(), 'one')
    await chips.first().click()
    assert.equal(await page.locator('[aria-label="Visible fixture tasks"] p').count(), 2)
    const more = page.getByRole('button', { name: /6 workspaces/ })
    await more.click()
    const dialog = page.getByRole('dialog', { name: 'Workspace Git status' })
    assert.equal(await dialog.locator('tbody tr').count(), 6)
    assert.equal(await dialog.getByRole('img', { name: 'Detached HEAD' }).count(), 1)
    await dialog.getByRole('button', { name: 'repo-1', exact: true }).click()
    assert.equal(await page.locator('[aria-label="Visible fixture tasks"]').innerText(), 'two')
    await page.keyboard.press('Escape')
    assert.equal(await dialog.count(), 0)
    assert.equal(await more.evaluate(node => node === document.activeElement), true)
    await more.click()
    await page.getByRole('button', { name: 'Outside', exact: true }).click()
    assert.equal(await dialog.count(), 0)
    const search = page.getByRole('searchbox', { name: 'Search tasks' })
    await page.keyboard.press('/')
    assert.equal(await search.evaluate(node => node === document.activeElement), true)
    const draft = page.getByRole('textbox', { name: 'Outside draft' })
    await draft.focus(); await page.keyboard.press('/')
    assert.equal(await draft.inputValue(), '/')
    await page.getByRole('tab', { name: /^All/ }).focus(); await page.keyboard.press('ArrowRight')
    assert.equal(await page.getByRole('tab', { name: /^Running/ }).getAttribute('aria-selected'), 'true')
    await page.keyboard.press('End')
    assert.equal(await page.getByRole('tab', { name: /^Media/ }).getAttribute('aria-selected'), 'true')
    await page.keyboard.press('Home')
    await page.getByRole('checkbox', { name: 'Select all', exact: true }).check()
    await page.getByRole('checkbox', { name: 'Clear selection', exact: true }).uncheck()
    assert.equal(await page.getByRole('checkbox', { name: 'Select all', exact: true }).isChecked(), false)
    const searchHeight = (await page.locator('.swarm-task-search').boundingBox())!.height
    assert.equal(searchHeight, 42)
    assert.equal((await page.locator('.swarm-task-source-control').boundingBox())!.height, searchHeight)
    await page.locator('main').evaluate(node => { node.style.width = '390px' })
    assert.equal(await chips.first().isVisible(), false)
    await more.click()
    assert.equal(await dialog.locator('tbody tr').count(), 6)
    await page.keyboard.press('Escape')
    assert.ok(await page.locator('main').evaluate(node => node.scrollWidth <= node.clientWidth + 1), 'only tabs/table scroll, not lane')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})

// Purpose: useProjectWorkspaceGit must reuse the status query/cache and push-only
// subscription, isolate a failed workspace, and keep loaded labels during a
// pushed refresh. Real React/query/API composition with finite fetch fixtures is
// the narrowest layer proving observer behavior (not backend authorization).
test('workspace Git hook isolates failures and refreshes only the pushed repository', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {useProjectWorkspaceGit} from './src/features/desktop/runtime/use-project-workspace-git';
    const paths=['/fixture/a','/fixture/b'];const workspaces=paths.map((path,i)=>({key:path,path,name:'repo-'+i}));
    window.reads=[];window.version=1;window.hold=false;window.fail=true;window.watches=0;
    window.fetch=async(input,init)=>{const url=new URL(typeof input==='string'?input:input.url,location.href);
      if(url.pathname==='/v1/workspace/git/status'){
        const path=url.searchParams.get('workspace_path');window.reads.push(path);
        if(path===paths[1]&&window.fail)return Response.json({error:'One repo failed'},{status:503});
        if(path===paths[0]&&window.hold)await new Promise(resolve=>window.release=resolve);
        return Response.json({ok:true,status:{workspace_path:path,has_git:true,branch:'branch-'+window.version,dirty_count:0,ahead_count:0,behind_count:0,conflict_count:0,files:[]}});
      }
      if(url.pathname==='/v1/workspace/git/subscriptions'){
        window.watches++;window.selectors=JSON.parse(init.body).repositories;const stream=new ReadableStream({start(controller){window.notice=notice=>controller.enqueue(new TextEncoder().encode('data: '+JSON.stringify(notice)+'\\n\\n'));init?.signal?.addEventListener('abort',()=>controller.close(),{once:true})}});
        return new Response(stream,{headers:{'Content-Type':'text/event-stream'}});
      }
      return Response.json({error:'Unexpected fixture fetch'},{status:400});
    };
    function App(){const git=useProjectWorkspaceGit(workspaces);return <><button onClick={git.refresh}>Retry Git</button>{git.workspaces.map(row=><p key={row.key} data-testid={row.name}>{row.error?'error':row.status?.branch||'loading'}</p>)}</>}
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><App/></QueryClientProvider>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.waitForFunction(() => document.querySelector('[data-testid="repo-0"]')?.textContent === 'branch-1' && document.querySelector('[data-testid="repo-1"]')?.textContent === 'error')
    assert.deepEqual(await page.evaluate(() => (window as any).reads), ['/fixture/a', '/fixture/b'])
    await page.waitForFunction(() => (window as any).selectors?.[0]?.branch === 'branch-1')
    await page.evaluate(() => { const w = window as any; w.hold = true; w.version = 2; w.notice({ index: 0, kind: 'changed' }) })
    await page.waitForFunction(() => typeof (window as any).release === 'function')
    assert.equal(await page.getByTestId('repo-0').innerText(), 'branch-1', 'loaded branch survives background read')
    assert.equal(await page.getByTestId('repo-1').innerText(), 'error')
    await page.evaluate(() => { const w = window as any; w.hold = false; w.release() })
    await page.waitForFunction(() => document.querySelector('[data-testid="repo-0"]')?.textContent === 'branch-2')
    assert.deepEqual(await page.evaluate(() => (window as any).reads), ['/fixture/a', '/fixture/b', '/fixture/a'])
    await page.waitForFunction(() => (window as any).selectors?.[0]?.branch === 'branch-2')
    await page.evaluate(() => { const w = window as any; w.notice({ index: 0, kind: 'lost', error: 'Watch lost' }) })
    await page.waitForFunction(() => document.querySelector('[data-testid="repo-0"]')?.textContent === 'error')
    await page.evaluate(() => { const w = window as any; w.notice({ index: 0, kind: 'ready' }); w.fail = false })
    await page.waitForFunction(() => document.querySelector('[data-testid="repo-0"]')?.textContent === 'branch-2')
    const watches = await page.evaluate(() => (window as any).watches)
    await page.getByRole('button', { name: 'Retry Git' }).click()
    await page.waitForFunction(() => document.querySelector('[data-testid="repo-1"]')?.textContent === 'branch-2')
    assert.ok(await page.evaluate(() => (window as any).watches) > watches, 'explicit retry re-acquires lost lanes')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
