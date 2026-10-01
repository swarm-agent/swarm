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
// Primary actions remain above disclosure, including after remount. Detailed
// lineage stays below it; expansion cannot move the summary or primary controls.
// Reopen uses one persistent, focused form without silently changing disclosure.
// The operation's explicit accessible name must exclude its hidden retry-width
// reserve and live status semantics while preserving the same fixed geometry.
test('integration card stays stable through pending, failure, retry and stale success snapshots', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useSyncExternalStore,useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {createTaskIntegrationController,taskIntegrationKey} from './src/features/desktop/orchestrate/task-integration-operation';
    const controller=createTaskIntegrationController(); const project={id:'project-a',name:'Project'};
    const task=${JSON.stringify(integrationTask)};
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}}); window.calls=0;window.lineage=[];window.reopened=[];window.selected=0;window.completed=0;
    window.submit=()=>controller.run(project,task,()=>{window.calls++;window.lineage.push({sessionId:task.sessionId,sourceBranch:task.worktreeBranch,targetBranch:task.baseBranch});return new Promise((resolve,reject)=>{window.finish=()=>resolve({status:'integrated',task:{id:task.id,session_id:task.sessionId,is_integrated:true}});window.fail=()=>reject(new Error('Git conflict'));});},()=>{});
    function App(){const [mount,setMount]=useState(true);const [snapshot,setSnapshot]=useState(task);
      window.mount=setMount;window.snapshot=setSnapshot;
      useSyncExternalStore(controller.subscribe,controller.getSnapshot,controller.getSnapshot);
      return <QueryClientProvider client={client}>{mount && <MinimalTaskCard task={snapshot} onSelect={()=>{window.selected++}} onReopen={async feedback=>{window.reopened.push(feedback ?? null);return {ok:true};}} onComplete={()=>{window.completed++}} onIntegrate={window.submit} integrationOperation={controller.get(taskIntegrationKey(project.id,snapshot))}/>}</QueryClientProvider>;}
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
      assert.equal(await page.getByTestId('toggle-task-details-btn').getAttribute('aria-expanded'), 'false')
      await initial.waitFor()
      assert.equal(await initial.count(), 1, 'integration is reachable without disclosure')
      assert.equal(await bar.count(), 1)
      assert.equal(await page.getByRole('button', { name: 'Reopen task', exact: true }).count(), 1)
    } catch (error) {
      assert.fail(`Integration fixture readiness failed: ${String(error)}; page errors: ${JSON.stringify(errors)}; rendered text: ${(await page.locator('#root').textContent())?.slice(0, 2000)}`)
    }
    assert.deepEqual(errors, [], 'complete RunningTask fixture renders without page errors')
    assert.equal(await initial.isEnabled(), true)
    assert.equal(await initial.getAttribute('aria-label'), 'Integrate into dev')
    assert.equal(await initial.locator('[aria-hidden="true"]').first().getAttribute('aria-hidden'), 'true')
    const toggle = page.getByTestId('toggle-task-details-btn')
    const summary = page.getByTestId('task-card-summary')
    const summaryBounds = await summary.boundingBox()
    const toggleBounds = await toggle.boundingBox()
    const actionBounds = await initial.boundingBox()
    await toggle.click()
    assert.deepEqual(await summary.boundingBox(), summaryBounds)
    assert.equal((await toggle.boundingBox())?.y, toggleBounds?.y)
    assert.equal((await toggle.boundingBox())?.x, toggleBounds?.x)
    assert.deepEqual(await initial.boundingBox(), actionBounds)
    assert.equal(await page.getByTestId('task-primary-actions').count(), 1)
    assert.equal(await initial.count(), 1)
    assert.equal(await page.getByTestId('orchestrate-task-card').getAttribute('data-expanded'), 'true')
    assert.match(await page.getByTestId('task-card-summary').innerText(), /2 unintegrated commits/)
    assert.match(await bar.innerText(), /Ready to integrate/)
    const lineage = page.getByTestId('task-integration-lineage')
    assert.match(await lineage.innerText(), /agent\/a\s*→\s*dev\s*\(2 commits\)/)
    assert.equal(await bar.getByRole('button', { name: 'Integrate into dev', exact: true }).count(), 1)
    assert.equal(await lineage.evaluate(node => {
      const toggle = document.querySelector('[data-testid="toggle-task-details-btn"]')!
      const details = document.getElementById(toggle.getAttribute('aria-controls')!)!
      return details.contains(node)
        && !!(toggle.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING)
        && node.getBoundingClientRect().top >= toggle.getBoundingClientRect().bottom
    }), true, 'detailed lineage belongs below the controlling toggle')
    assert.equal(await bar.evaluate(node => {
      const toggle = document.querySelector('[data-testid="toggle-task-details-btn"]')!
      const details = document.getElementById(toggle.getAttribute('aria-controls')!)!
      return !details.contains(node) && node.getBoundingClientRect().bottom <= toggle.getBoundingClientRect().top
    }), true, 'primary controls stay above disclosure')
    await toggle.click()
    assert.equal(await lineage.count(), 0)
    assert.deepEqual(await initial.boundingBox(), actionBounds)
    assert.equal(await page.getByTestId('orchestrate-task-card').evaluate(node => node.getAnimations({ subtree: true }).length), 0)
    const bounds = await initial.boundingBox()
    assert.ok(bounds, 'initial operation has measurable dimensions')
    // One collapsed form, sensible focus, preserved draft through disclosure, and
    // cancellation without a mutation. Real keyboard submit calls the handler once.
    const reopen = page.getByRole('button', { name: 'Reopen task', exact: true })
    const instructions = page.getByRole('textbox', { name: 'Instructions for the agent to resume work' })
    await reopen.click()
    await instructions.waitFor()
    assert.equal(await instructions.evaluate(node => node === document.activeElement), true)
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    assert.equal(await page.getByRole('form', { name: 'Reopen task instructions' }).evaluate(node => {
      const actions = document.querySelector('[data-testid="task-primary-actions"]')!
      return actions.nextElementSibling === node && node.getBoundingClientRect().top >= actions.getBoundingClientRect().bottom
    }), true, 'instruction form sits directly beneath primary actions')
    await instructions.fill('Keep the existing layout')
    await toggle.click()
    assert.equal(await page.getByRole('form', { name: 'Reopen task instructions' }).count(), 1)
    assert.equal(await reopen.count(), 1)
    await toggle.click()
    assert.equal(await instructions.inputValue(), 'Keep the existing layout')
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    assert.equal(await instructions.count(), 0)
    assert.equal(await reopen.evaluate(node => node === document.activeElement), true)
    assert.deepEqual(await page.evaluate(() => (window as any).reopened), [])
    await reopen.click()
    assert.equal(await instructions.inputValue(), 'Keep the existing layout')
    await instructions.press('Enter')
    assert.deepEqual(await page.evaluate(() => (window as any).reopened), ['Keep the existing layout'])
    assert.equal(await instructions.count(), 0)
    await reopen.click()
    await instructions.fill('   ')
    await page.getByRole('button', { name: 'Resume Run', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).reopened), ['Keep the existing layout', '   '])
    assert.equal(await page.evaluate(() => (window as any).selected), 0)
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await initial.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await page.evaluate(() => { void (window as any).submit() }) // second click before a transport response
    const pending = page.getByRole('button', { name: 'Integrating…', exact: true })
    assert.equal(await pending.isDisabled(), true)
    assert.equal(await pending.getAttribute('aria-busy'), 'true')
    assert.equal(await reopen.isDisabled(), true)
    assert.equal(await pending.evaluate(node => node.getAnimations({ subtree: true }).length), 0)
    assert.equal(await page.evaluate(() => (window as any).calls), 1)
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task, isIntegrated: true, gitStatus: 'clean', unintegratedCommits: 0 }))
    assert.equal(await bar.count(), 1)
    assert.equal(await pending.isDisabled(), true)
    await page.evaluate(() => (window as any).mount(false))
    await page.waitForFunction(() => !document.querySelector('[data-testid="task-pending-worktree-bar"]'))
    await page.evaluate(() => (window as any).mount(true))
    await pending.waitFor()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await page.evaluate(() => (window as any).fail())
    const retry = page.getByRole('button', { name: 'Retry integrate into dev', exact: true })
    await retry.waitFor()
    assert.equal(await retry.isDisabled(), false)
    assert.equal(await retry.getAttribute('aria-label'), 'Retry integrate into dev')
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
    await page.evaluate(() => (window as any).mount(false))
    await integrated.waitFor({ state: 'detached' })
    await page.evaluate(() => (window as any).mount(true))
    await integrated.waitFor()
    assert.equal(await integrated.isDisabled(), true, 'success receipt survives remount with stale Git snapshot')
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
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
    await reopen.click()
    assert.equal(await instructions.count(), 1, 'integrated task has only one reopen input')
    await toggle.click()
    assert.equal(await instructions.count(), 1)
    assert.equal(await reopen.count(), 1)
    assert.equal(await integrated.count(), 1)
    await toggle.click()
    await instructions.press('Escape')
    assert.equal(await instructions.count(), 0)
    assert.equal(await reopen.evaluate(node => node === document.activeElement), true)
    // Missing target cannot invent a branch/readiness or call integration.
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task, baseBranch: undefined, activeAttemptId: 'missing-target' }))
    const unavailable = page.getByRole('button', { name: 'Target unavailable', exact: true })
    await unavailable.waitFor()
    assert.equal(await unavailable.isDisabled(), true)
    assert.doesNotMatch(await bar.innerText(), /Ready to integrate/)
    assert.equal(await page.evaluate(() => (window as any).calls), 2)
    // A completed task without Git evidence is not a merge-ready task.
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task, activeAttemptId: 'non-code', status: 'completed', gitStatus: 'clean', unintegratedCommits: 0 }))
    await bar.waitFor({ state: 'detached' })
    assert.equal(await reopen.count(), 1)
    for (const status of ['running', 'rejected', 'pending_approval']) {
      await page.evaluate(status => (window as any).snapshot({ ...(window as any).task, status, gitStatus: 'clean', unintegratedCommits: 0, activeAttemptId: status }), status)
      await reopen.waitFor({ state: 'detached' })
      assert.equal(await instructions.count(), 0)
    }
    for (const status of ['needs_review', 'failed']) {
      await page.evaluate(status => (window as any).snapshot({ ...(window as any).task, status, gitStatus: 'clean', unintegratedCommits: 0, activeAttemptId: status }), status)
      await reopen.waitFor()
    }
    await page.evaluate(() => (window as any).snapshot({ ...(window as any).task, gitStatus: 'clean', unintegratedCommits: 0, activeAttemptId: 'review' }))
    const complete = page.getByRole('button', { name: 'Complete', exact: true })
    await complete.waitFor()
    assert.equal(await complete.isEnabled(), true)
    await complete.click()
    assert.equal(await page.evaluate(() => (window as any).completed), 1)
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    assert.equal(await page.getByRole('button', { name: /Integrate into/ }).count(), 0)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
