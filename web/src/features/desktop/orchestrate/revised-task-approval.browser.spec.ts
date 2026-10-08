import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: MinimalTaskCard must hydrate a collapsed pending card's current API
// definition, including a replacement revision, without approving or opening a
// session. Missing/failed/stale detail must remain disabled and explicitly
// retryable. This hermetic component/runtime/reducer boundary is the narrowest
// layer proving real button behavior and exact acceptance payloads; it does not
// claim daemon execution or provider health.
test('collapsed revised approval hydrates exact detail and recovers from failure without auto approval', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {desktopProjects} from './src/features/desktop/runtime/desktop-projects';
    import {mapBackendTask,reduceDesktopProjectsState} from './src/features/desktop/state/desktop-projects-state';
    import {buildTaskAcceptancePayload} from './src/features/desktop/orchestrate/orchestrate-task-helpers';
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    const root=createRoot(document.getElementById('root'));
    let state={},revision=2,pending=[];
    const row=()=>({id:'proposal',title:'Revised feature',agent:'swarm',status:'pending_approval',revision,
      session_id:'owner',plan_binding:{session_id:'owner',plan_id:'plan',definition_revision:revision},
      board_summary:{plan:{id:'plan',session_id:'owner',version:revision,status:'pending_approval',approval_state:'pending'}}});
    window.reads=0;window.approvals=[];window.selections=0;
    const render=()=>{const task=state.project?.tasks[0];if(task)root.render(
      <QueryClientProvider client={client}><MinimalTaskCard task={task} projectId="project"
        onApprove={()=>window.approvals.push(buildTaskAcceptancePayload(task))}
        onSelect={()=>window.selections++}/></QueryClientProvider>)};
    // Inject only transport/store dependencies; production runtime methods and
    // the singleton used by the card remain the actual inspection authority.
    desktopProjects.deps={getState:()=>state,dispatch:action=>{state=reduceDesktopProjectsState(state,action);render()},
      subscribe:()=>()=>{},subscribeGit:()=>()=>{},fetchTasks:async()=>({tasks:[row()]}),fetchMedia:async()=>({media:[]}),
      fetchTask:async()=>{window.reads++;const captured=row();return new Promise((resolve,reject)=>pending.push({resolve,reject,captured}))}};
    window.fail=()=>pending.shift().reject(new Error('Detail unavailable'));
    window.resolve=(invalid=false)=>{const next=pending.shift();const {board_summary,...task}=next.captured;
      const text='Revision '+task.revision+' rejects invalid input';
      next.resolve({task:{...task,plan_document:{id:'plan',revision_id:'plan:v'+task.revision,title:'Exact review',status:'pending_approval',
        info:{goal:'Preserve user approval'},requirements:[{id:'r',text,checkpoint_id:invalid?'missing':'cp'}],
        checkpoints:[{id:'cp',title:'Implement',tasks:['Fix handler'],acceptance_criteria:[text]}]}}})};
    window.revise=()=>{revision++;desktopProjects.setOptimisticTasks('project',()=>[mapBackendTask(row())])};
    window.lease=desktopProjects.acquire('project');
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({
      contentType: new URL(route.request().url()).pathname === '/' ? 'text/html' : 'application/json',
      body: new URL(route.request().url()).pathname === '/' ? '<div id="root"></div>' : '{}',
    }))
    await page.goto('https://revised-approval.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const approve = page.getByTestId('approve-task-btn')
    const details = page.getByTestId('toggle-task-details-btn')
    await page.waitForFunction(() => (window as any).reads === 1)
    assert.equal(await approve.isDisabled(), true)
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    await page.evaluate(() => (window as any).fail())
    const retry = page.getByRole('button', { name: 'Retry loading plan' })
    await retry.waitFor()
    assert.match(await page.getByTestId('task-review-hydration').innerText(), /Detail unavailable/)
    assert.equal(await approve.isDisabled(), true)
    assert.equal(await page.evaluate(() => (window as any).reads), 1, 'failure must not cause automatic retry churn')
    await retry.click()
    await page.waitForFunction(() => (window as any).reads === 2)
    await page.evaluate(() => (window as any).resolve())
    await page.waitForFunction(() => !(document.querySelector('[data-testid="approve-task-btn"]') as HTMLButtonElement)?.disabled)
    assert.deepEqual(await page.evaluate(() => (window as any).approvals), [])
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    await approve.click()
    assert.deepEqual(await page.evaluate(() => (window as any).approvals), [{ session_id: 'owner', plan_id: 'plan', definition_revision: 2 }])

    // An invalid replacement cannot reuse the previous review document.
    await page.evaluate(() => (window as any).revise())
    await page.waitForFunction(() => (window as any).reads === 3)
    assert.equal(await approve.isDisabled(), true)
    await page.evaluate(() => (window as any).resolve(true))
    await page.getByRole('alert').filter({ hasText: 'not bound' }).waitFor()
    assert.equal(await approve.isDisabled(), true)
    await page.evaluate(() => (window as any).revise())
    await page.waitForFunction(() => (window as any).reads === 4)
    await page.evaluate(() => (window as any).resolve())
    await page.waitForFunction(() => !(document.querySelector('[data-testid="approve-task-btn"]') as HTMLButtonElement)?.disabled)
    assert.match(await page.getByRole('region', { name: 'Plan checklist', exact: true }).innerText(), /Revision 4 rejects/)
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    assert.equal(await page.evaluate(() => (window as any).selections), 0)
    assert.equal(await page.evaluate(() => (window as any).approvals.length), 1, 'hydration and revision are not approval')
    await approve.click()
    assert.deepEqual(await page.evaluate(() => (window as any).approvals), [
      { session_id: 'owner', plan_id: 'plan', definition_revision: 2 },
      { session_id: 'owner', plan_id: 'plan', definition_revision: 4 },
    ])
    assert.deepEqual(errors, [])
    await page.evaluate(() => (window as any).lease.release())
  } finally { await browser.close() }
})
