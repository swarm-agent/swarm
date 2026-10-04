// Purpose: accepted direct video work must never render Turn 1 as failed merely
// because its generating slot has no media/session. Preserve the DOM turn across
// completion, stale events, reload, transport errors and explicit failures.
// Pending turns show an accessible spinner rather than a right-hand status label;
// terminal results remove it without changing the underlying lifecycle.
// Authority: DesktopProjectsRuntime/reducer -> taskWithCurrentSessions ->
// aggregateTaskLiveState -> MediaTaskThreads/CreativeThreadCard. Real Chromium
// with controlled API/event payloads is the narrowest DOM transition boundary;
// this is not provider-backed generation or deployment qualification.
import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

test('direct video Turn 1 stays nonterminal until authoritative completion or failure', { timeout: 30_000 }, async () => {
  const bundle = await build({
    stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
      import React,{useSyncExternalStore} from 'react';
      import {createRoot} from 'react-dom/client';
      import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
      import {DesktopProjectsRuntime} from './src/features/desktop/runtime/desktop-projects';
      import {reduceDesktopProjectsState} from './src/features/desktop/state/desktop-projects-state';
      import {createEmptyDesktopV3CacheState} from './src/features/desktop/state/desktop-v3-cache-reducer';
      import {taskWithCurrentSessions} from './src/features/desktop/orchestrate/task-card-sessions';
      import {aggregateTaskLiveState} from './src/features/desktop/orchestrate/orchestrate-task-helpers';
      import {MediaTaskThreads} from './src/features/desktop/orchestrate/media-task-card';
      // Shape matches direct launch: no session/attempt/program, in_progress
      // task and generating video slot, not a completed provider response.
      const initial={id:'video-task',project_id:'video-project',title:'Video status',
        agent:'video',status:'in_progress',tier:'direct',revision:1,
        deliverables:[{id:'video-output',title:'Video output',kind:'video',status:'generating',thumbnail:'video'}]};
      let payload=initial,state={},transportError=false;
      const listeners=new Set();
      const cache=createEmptyDesktopV3CacheState();
      const runtime=new DesktopProjectsRuntime({getState:()=>state,
        dispatch:action=>{state=reduceDesktopProjectsState(state,action);listeners.forEach(f=>f())},
        fetchTasks:async()=>({tasks:[structuredClone(payload)]}),fetchMedia:async()=>({media:[]}),
        fetchTask:async()=>{if(transportError)throw Error('Status transport unavailable');return {task:structuredClone(payload)}},
        subscribe:()=>()=>{}});
      let lease=runtime.acquire(initial.project_id);
      window.applyVideo=async(next,error=false)=>{payload={...initial,...next};transportError=error;
        runtime.acceptFrame({kind:'project.updated',project_id:initial.project_id,event:{payload:{
          project_id:initial.project_id,task_id:initial.id,action:'task_updated',revision:payload.revision}}});
        for(let n=0;n<30;n++)await Promise.resolve();};
      window.reloadVideo=async()=>{lease.release();lease=runtime.acquire(initial.project_id);await lease.ready};
      function App(){const snapshot=useSyncExternalStore(f=>{listeners.add(f);return()=>listeners.delete(f)},()=>state);
        const tasks=(snapshot[initial.project_id]?.tasks??[]).map(task=>aggregateTaskLiveState(taskWithCurrentSessions(task,cache),{}));
        return <MediaTaskThreads tasks={tasks} visibleTaskIds={new Set([initial.id])} actions={()=>({})}/>;}
      createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><App/></QueryClientProvider>);
    ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent',
  })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => new URL(route.request().url()).pathname === '/fixture'
      ? route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      : route.abort()) // Never submit jobs or fetch real media.
    await page.goto('http://localhost/fixture')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const card = page.getByTestId('media-task-card')
    const turn = card.getByRole('tab')
    const check = async (status: string) => {
      const pending = ['running', 'pending', 'queued'].includes(status)
      await page.waitForFunction(({ status, pending }) => {
        const heading = document.querySelector('.creative-turn-heading')?.textContent
        const spinner = document.querySelector('.creative-turn-preview [role="status"]')
        return heading === `Turn 1${pending ? '' : status}` && (pending
          ? spinner?.getAttribute('aria-label') === `Turn 1: ${status}`
          : !spinner)
      }, { status, pending }, { timeout: 3000 }).catch(async error => {
        throw new Error(`Expected ${status}; rendered: ${await page.locator('body').innerText()}; errors: ${errors.join('; ')}`, { cause: error })
      })
      assert.equal(await turn.count(), 1)
      assert.equal(await card.getAttribute('data-task-id'), 'video-task')
      if (status !== 'failed') assert.doesNotMatch(await turn.innerText(), /failed/i)
      if (pending) {
        assert.doesNotMatch(await turn.innerText(), /running|pending|queued/i)
        assert.equal(await turn.locator('[role="status"] svg').count(), 1)
      }
    }
    const apply = async (payload: Record<string, unknown>, transportError = false) => {
      await page.evaluate(async ({ payload, transportError }) => { await (window as any).applyVideo(payload, transportError) }, { payload, transportError })
    }
    await check('running')
    assert.equal(await card.locator('video').count(), 0)
    let revision = 1
    for (const status of ['pending', 'queued', 'in_progress']) {
      await apply({ status, revision: ++revision })
      await check(status === 'in_progress' ? 'running' : status)
    }
    await apply({ status: 'in_progress', revision: ++revision }, true)
    await check('running')
    await apply({ status: 'in_progress', revision: ++revision })
    await page.evaluate(() => (window as any).reloadVideo())
    await check('running')
    // A full reload may remount the card; completion must retain this loaded turn.
    await page.evaluate(() => { (window as any).originalTurn = document.querySelector('[role="tab"]') })
    const ready = { status: 'needs_review', revision: ++revision, deliverables: [
      { id: 'video-output', title: 'Video output', kind: 'video', status: 'ready', media_url: '/fixture-video.mp4' },
    ] }
    await apply(ready)
    await check('needs review')
    assert.equal(await card.locator('video').getAttribute('src'), '/fixture-video.mp4')
    assert.equal(await page.evaluate(() => (window as any).originalTurn === document.querySelector('[role="tab"]')), true)
    for (const status of ['in_progress', 'failed']) {
      await apply({ status, revision: revision - 1, last_error: 'Stale failure' })
      await check('needs review')
      assert.doesNotMatch(await card.innerText(), /Stale failure/)
    }
    await apply(ready)
    await page.evaluate(() => (window as any).reloadVideo())
    await check('needs review')
    // Independent accepted request branch, not a failure of completed work.
    await apply({ status: 'in_progress', revision: 1 })
    await page.evaluate(() => (window as any).reloadVideo())
    await check('running')
    await apply({ status: 'failed', revision: 2, last_error: 'Provider rejected generation', deliverables: [
      { id: 'video-output', title: 'Video output', kind: 'video', status: 'failed', description: 'Provider rejected generation' },
    ] })
    await check('failed')
    assert.match(await card.innerText(), /Provider rejected generation/)
    await page.evaluate(() => (window as any).reloadVideo())
    await check('failed')
    for (const [index, status] of ['cancelled', 'rejected'].entries()) {
      await apply({ status, revision: index + 3 })
      await check(status)
    }
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
