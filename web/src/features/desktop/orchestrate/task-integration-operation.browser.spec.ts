import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

// Purpose: MinimalTaskCard must visibly retain one disabled operation across
// deferred requests, stale task snapshots and remounts, without changing button
// geometry. Real production JSX/CSS is the narrowest proof of accessibility and
// layout; injected promises prove UI ordering, not backend Git integration.
test('integration card stays stable through pending, failure, retry and stale success snapshots', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useSyncExternalStore,useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {createTaskIntegrationController,taskIntegrationKey} from './src/features/desktop/orchestrate/task-integration-operation';
    const controller=createTaskIntegrationController(); const project={id:'project-a',name:'Project'};
    const task={id:'task-a',title:'Integration task',status:'needs_review',agentType:'coder',outcomeType:'code_pr',agents:[],sessionId:'session-a',worktreeBranch:'agent/a',baseBranch:'dev',gitStatus:'diverged',unintegratedCommits:2};
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}}); window.calls=0;
    window.submit=()=>controller.run(project,task,()=>{window.calls++;return new Promise((resolve,reject)=>{window.finish=()=>resolve({status:'integrated',task:{id:task.id,session_id:task.sessionId,is_integrated:true}});window.fail=()=>reject(new Error('Git conflict'));});},()=>{});
    function App(){const [mount,setMount]=useState(true);const [snapshot,setSnapshot]=useState(task);
      window.mount=setMount;window.snapshot=setSnapshot;
      useSyncExternalStore(controller.subscribe,controller.getSnapshot,controller.getSnapshot);
      return <QueryClientProvider client={client}>{mount && <MinimalTaskCard task={snapshot} onSelect={()=>{}} onIntegrate={window.submit} integrationOperation={controller.get(taskIntegrationKey(project.id,snapshot))}/>}</QueryClientProvider>;}
    window.task=task;createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ reducedMotion: 'reduce' })
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root" style="width:760px"></div>' }))
    await page.goto('https://integration.test/')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const bar = page.getByTestId('task-pending-worktree-bar')
    const initial = page.getByRole('button', { name: 'Integrate into dev', exact: true })
    const bounds = await initial.boundingBox()
    await initial.click()
    await page.evaluate(() => { void (window as any).submit() }) // second click before a transport response
    const pending = page.getByRole('button', { name: 'Integrating…', exact: true })
    assert.equal(await pending.isDisabled(), true)
    assert.equal(await pending.getAttribute('aria-busy'), 'true')
    assert.equal(await page.evaluate(() => (window as any).calls), 1)
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task, isIntegrated: true, gitStatus: 'clean', unintegratedCommits: 0 }))
    assert.equal(await bar.count(), 1)
    assert.equal(await pending.isDisabled(), true)
    await page.evaluate(() => (window as any).mount(false))
    await page.waitForFunction(() => !document.querySelector('[data-testid="task-pending-worktree-bar"]'))
    await page.evaluate(() => (window as any).mount(true))
    await pending.waitFor()
    await page.evaluate(() => (window as any).fail())
    const retry = page.getByRole('button', { name: 'Retry integrate into dev', exact: true })
    await retry.waitFor()
    assert.equal(await retry.isDisabled(), false)
    assert.equal((await retry.boundingBox())?.width, bounds?.width)
    await retry.click()
    await pending.waitFor()
    await page.evaluate(() => (window as any).finish())
    const integrated = page.getByRole('button', { name: 'Integrated', exact: true })
    await integrated.waitFor()
    assert.equal(await integrated.isDisabled(), true)
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task }))
    assert.equal(await integrated.isDisabled(), true)
    const final = await integrated.boundingBox()
    assert.equal(final?.width, bounds?.width)
    assert.equal(final?.height, bounds?.height)
    assert.equal(await page.evaluate(() => (window as any).calls), 2)
    assert.equal(await pending.count(), 0)
    assert.equal(await bar.count(), 1)
  } finally { await browser.close() }
})
