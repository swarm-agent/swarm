import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: filter-driven card mount hydration must not change project attention
// or trigger project/Git reads. Production ProjectTaskAttention + canonical runtime
// and reducer are composed with a minimal filter shell. This checks DOM identity,
// layout and actual request counts, not full OrchestrateView routing or daemon Git.
test('project attention survives filters, hydration and unchanged Git repair', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState,useEffect} from 'react';import {createRoot} from 'react-dom/client';
    import {ProjectTaskAttention} from './src/features/desktop/orchestrate/task-outcome-view';
    import {DesktopProjectsRuntime} from './src/features/desktop/runtime/desktop-projects';
    import {reduceDesktopProjectsState} from './src/features/desktop/state/desktop-projects-state';
    import {createEmptyDesktopV3CacheState} from './src/features/desktop/state/desktop-v3-cache-reducer';
    import {dispatchDesktopV3Cache} from './src/features/desktop/state/desktop-v3-cache-store';
    window.permission=(id,count)=>dispatchDesktopV3Cache({type:'realtime.applyEvent',event:{source:'realtime',sessionId:id,eventType:'permission.summary.updated',payload:{session_id:id,pending_approval_count:count,updated_at:Date.now()}}});
    const cache=createEmptyDesktopV3CacheState();let state={},notify=()=>{};
    const runtime=new DesktopProjectsRuntime({getState:()=>state,dispatch:a=>{state=reduceDesktopProjectsState(state,a);notify()},subscribe:()=>()=>{},
      fetchTasks:id=>fetch('/projects/'+id).then(r=>r.json()),fetchMedia:async()=>({media:[]}),fetchTask:(p,id)=>fetch('/git/'+id).then(r=>r.json())});
    function Card({task}){useEffect(()=>{runtime.acceptSessionMutation({action:{type:'hydrate.apply',requestedSessionIds:[task.sessionId]},previousState:cache,nextState:cache})},[task.id]);return <p>{task.title}</p>}
    function App(){const [project,setProject]=useState('one');const [filter,setFilter]=useState('All');const [search,setSearch]=useState('');const [source,setSource]=useState('all');const [,update]=useState(0);
      notify=()=>update(n=>n+1);useEffect(()=>{const lease=runtime.acquire(project);return lease.release},[project]);
      window.repair=()=>runtime.acceptFrame({kind:'rehydrate.required'});
      const tasks=state[project]?.tasks||[];window.opened='';
      return <><aside><ProjectTaskAttention tasks={tasks} onOpen={t=>{window.opened=t.id}}/><p data-testid='anchor'>Chat / Swarm</p></aside>
        {['All','Running','Review'].map(f=><button key={f} onClick={()=>setFilter(f)}>{f}</button>)}
        <input aria-label='Search' value={search} onChange={e=>setSearch(e.target.value)}/>
        <select aria-label='Source' value={source} onChange={e=>setSource(e.target.value)}><option value='all'>All sources</option><option value='other'>Other source</option></select>
        <button onClick={()=>setProject(p=>p==='one'?'two':'one')}>Switch project</button>
        {tasks.filter(t=>source==='all'&&(filter==='All'||t.status===(filter==='Running'?'running':'needs_review'))&&t.title.includes(search)).map(t=><Card key={t.id} task={t}/>)}</>}
    createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    let projects = 0, git = 0
    const integrated = { id: 'done', title: 'Delivered', agent: 'coder', session_id: 'done', status: 'completed', git_status: 'clean', is_integrated: true }
    const blocked = { id: 'blocked', title: 'Retained conflict', agent: 'coder', session_id: 'blocked', status: 'needs_review', integration: { state: 'conflict' } }
    await page.route('**/*', async route => {
      const path = new URL(route.request().url()).pathname
      if (path.startsWith('/projects/')) { projects++; await route.fulfill({ json: { tasks: path.endsWith('/one') ? [integrated, blocked] : [] } }); return }
      if (path.startsWith('/git/')) { git++; await route.fulfill({ json: { task: path.endsWith('/done') ? integrated : blocked } }); return }
      await route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
    })
    await page.goto('https://attention.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const summary = page.getByRole('region', { name: 'Project task attention' })
    await summary.getByRole('button', { name: 'Retained conflict · Integration conflict' }).waitFor()
    await page.waitForFunction(() => document.querySelectorAll('aside button').length === 1)
    // Let initial scoped detail inspections finish, then observe every summary mutation.
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
    assert.equal(projects, 1); assert.equal(git, 2)
    const text = await summary.innerText()
    const anchor = await page.getByTestId('anchor').boundingBox()
    await summary.evaluate(node => {
      (window as any).original = node
      ;(window as any).changes = []
      new MutationObserver(() => (window as any).changes.push(node.textContent)).observe(node, { subtree: true, childList: true, characterData: true })
    })
    for (let i = 0; i < 3; i++) for (const name of ['Running', 'Review', 'All']) {
      await page.getByRole('button', { name, exact: true }).click()
      assert.equal(await summary.innerText(), text)
      assert.deepEqual(await page.getByTestId('anchor').boundingBox(), anchor)
    }
    await page.getByRole('textbox', { name: 'Search' }).fill('no visible cards')
    await page.getByRole('textbox', { name: 'Search' }).fill('')
    await page.getByRole('combobox', { name: 'Source' }).selectOption('other')
    await page.getByRole('combobox', { name: 'Source' }).selectOption('all')
    assert.equal(projects, 1); assert.equal(git, 2)
    assert.equal(await summary.evaluate(node => node === (window as any).original), true)
    await page.evaluate(() => (window as any).repair())
    await page.waitForFunction(() => !document.body.textContent?.includes('2 tasks need attention'))
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
    assert.equal(await summary.innerText(), text)
    assert.deepEqual(await page.evaluate(() => (window as any).changes), [])
    await page.evaluate(() => (window as any).permission('foreign', 1))
    assert.equal(await summary.innerText(), text)
    await page.evaluate(() => (window as any).permission('done', 1))
    await summary.getByRole('button', { name: 'Delivered · Permission requested' }).waitFor()
    assert.match(await summary.innerText(), /2 tasks need attention/)
    await page.evaluate(() => (window as any).permission('done', 0))
    await summary.getByRole('button', { name: 'Delivered · Permission requested' }).waitFor({ state: 'detached' })
    assert.equal(await summary.innerText(), text)
    await summary.getByRole('button').click()
    assert.equal(await page.evaluate(() => (window as any).opened), 'blocked')
    await page.getByRole('button', { name: 'Switch project' }).click()
    await summary.waitFor({ state: 'detached' })
    await page.getByRole('button', { name: 'Switch project' }).click()
    await summary.waitFor()
    assert.equal(await summary.innerText(), text)
    await page.reload()
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await summary.waitFor()
    assert.equal(await summary.innerText(), text, 'reload restores the retained conflict')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
