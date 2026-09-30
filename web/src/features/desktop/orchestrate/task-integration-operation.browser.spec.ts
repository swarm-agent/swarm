import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import type { RunningTask } from './orchestrate-types'

const integrationTask = {
  id: 'task-a', title: 'Integration task', status: 'needs_review', agentType: 'coder', outcomeType: 'code_pr',
  subtasks: [], workspaceTarget: 'local', elapsed: '0s', sourceWorkspaceId: 'workspace-a', sourceWorkspacePath: '/repo',
  sessionId: 'session-a', worktreeBranch: 'agent/a', baseBranch: 'dev', gitStatus: 'diverged', unintegratedCommits: 2,
} satisfies RunningTask

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
    const task=${JSON.stringify(integrationTask)};
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}}); window.calls=0;window.lineage=[];
    window.submit=()=>controller.run(project,task,()=>{window.calls++;window.lineage.push({sessionId:task.sessionId,sourceBranch:task.worktreeBranch,targetBranch:task.baseBranch});return new Promise((resolve,reject)=>{window.finish=()=>resolve({status:'integrated',task:{id:task.id,session_id:task.sessionId,is_integrated:true}});window.fail=()=>reject(new Error('Git conflict'));});},()=>{});
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
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root" style="width:760px"></div>' }))
    await page.goto('https://integration.test/')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const bar = page.getByTestId('task-pending-worktree-bar')
    const initial = page.getByRole('button', { name: 'Integrate into dev', exact: true })
    try {
      await page.getByTestId('orchestrate-task-card').waitFor()
      await initial.waitFor()
    } catch (error) {
      assert.fail(`Integration fixture readiness failed: ${String(error)}; page errors: ${JSON.stringify(errors)}; rendered text: ${(await page.locator('#root').textContent())?.slice(0, 2000)}`)
    }
    assert.deepEqual(errors, [], 'complete RunningTask fixture renders without page errors')
    assert.equal(await initial.isEnabled(), true)
    // Collapsed summary owns lineage/count; the action row must not duplicate it.
    assert.equal(await page.getByTestId('orchestrate-task-card').getAttribute('data-expanded'), 'false')
    assert.match(await page.getByTestId('task-card-summary').innerText(), /2 unintegrated commits/)
    assert.doesNotMatch(await bar.innerText(), /agent\/a|Pending worktree|2 commits/)
    assert.equal(await page.getByTestId('orchestrate-task-card').evaluate(node => node.getAnimations({ subtree: true }).length), 0)
    const bounds = await initial.boundingBox()
    assert.ok(bounds, 'initial operation has measurable dimensions')
    await initial.click()
    await page.evaluate(() => { void (window as any).submit() }) // second click before a transport response
    const pending = page.getByRole('button', { name: 'Integrating…', exact: true })
    assert.equal(await pending.isDisabled(), true)
    assert.equal(await pending.getAttribute('aria-busy'), 'true')
    assert.equal(await pending.evaluate(node => node.getAnimations({ subtree: true }).length), 0)
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
    assert.deepEqual(await page.evaluate(() => (window as any).lineage), [
      { sessionId: 'session-a', sourceBranch: 'agent/a', targetBranch: 'dev' },
      { sessionId: 'session-a', sourceBranch: 'agent/a', targetBranch: 'dev' },
    ], 'retry retains exact captured lineage')
    // Requirement: a promotion from another session uses canonical pending and
    // verified completion, not this controller's click receipt. Same mounted card.
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task, activeAttemptId: 'external-attempt', integration: { state: 'in_progress' } }))
    await pending.waitFor()
    assert.equal(await pending.isDisabled(), true)
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task, activeAttemptId: 'external-attempt', status: 'completed', isIntegrated: true, gitStatus: 'clean', unintegratedCommits: 0, integration: { state: 'integrated' } }))
    await integrated.waitFor()
    assert.equal(await pending.count(), 0)
    assert.equal(await page.evaluate(() => (window as any).calls), 2, 'external promotion never invokes a click mutation')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
