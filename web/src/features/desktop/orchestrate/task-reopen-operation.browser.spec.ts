import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: MinimalTaskCard and the production reopen controller must retain exact
// instructions on rejected/unconfirmed mutations, synchronously exclude duplicate
// and integration races, and clear only confirmed task authority. Browser JSX plus
// deferred transport is the narrowest observable UI layer; no claim of live launch.
test('reopen retains instructions, exposes collapsed errors and gates concurrent operations', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState,useSyncExternalStore} from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {mapBackendTask} from './src/features/desktop/state/desktop-projects-state';
    import {taskReopenOperations as controller} from './src/features/desktop/orchestrate/task-reopen-operation';
    import {createTaskIntegrationController} from './src/features/desktop/orchestrate/task-integration-operation';
    const task={id:'task-a',revision:7,title:'Task',status:'completed',agentType:'coder',outcomeType:'code_pr',subtasks:[],workspaceTarget:'local',elapsed:'0s',sessionId:'old',baseBranch:'dev',worktreeBranch:'agent/a'};
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});window.calls=[];window.applied=[];window.integrationCalls=0;window.approvals=0;
    const integrations=createTaskIntegrationController();
    window.integrate=()=>integrations.run({id:'project-a',name:'Project'},task,async()=>{window.integrationCalls++;return {status:'integrated',task:{id:task.id,session_id:task.sessionId,is_integrated:true}}},()=>{});
    function App(){const [project,setProject]=useState('project-a');const [mount,setMount]=useState(true);const [approval,setApproval]=useState(false);window.approval=setApproval;window.project=setProject;window.mount=setMount;
      useSyncExternalStore(controller.subscribe,controller.getSnapshot,controller.getSnapshot);
      const submit=feedback=>controller.run(project,task.id,task,feedback,body=>{window.calls.push(body);return new Promise((resolve,reject)=>{window.resolve=resolve;window.reject=message=>reject(new Error(message));});},returned=>window.applied.push({project,task:mapBackendTask(returned)}));
      window.submit=submit;
      return <QueryClientProvider client={client}>{mount && <MinimalTaskCard key={project} projectId={project} task={approval ? {...task,status:'pending_approval'} : task} taskError={approval ? 'Approval failed' : undefined} onApprove={()=>{window.approvals++}} onReopen={submit}/>}</QueryClientProvider>;
    }createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://reopen.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const open = page.getByRole('button', { name: 'Reopen task', exact: true })
    const input = page.getByRole('textbox', { name: 'Instructions for the agent to resume work' })
    const resume = page.getByRole('button', { name: 'Resume Run', exact: true })
    const pending = page.getByRole('button', { name: 'Reopening…', exact: true })
    const draft = '  preserve exact instructions  '
    await open.click()
    await input.fill(draft)
    for (const failure of ['409 follow-up source must be clean and committed', '409 retained task has unintegrated commits', '409 stale revision', '503 runner service unavailable', 'missing']) {
      await input.press('Enter')
      await pending.waitFor()
      assert.equal(await pending.isDisabled(), true)
      assert.equal(await pending.getAttribute('aria-busy'), 'true')
      assert.equal(await input.inputValue(), draft)
      const before = await page.evaluate(() => (window as any).calls.length)
      await page.evaluate(() => { document.querySelector('form')!.requestSubmit(); void (window as any).submit('  preserve exact instructions  '); void (window as any).integrate() })
      assert.equal(await page.evaluate(() => (window as any).calls.length), before)
      assert.equal(await page.evaluate(() => (window as any).integrationCalls), 0)
      if (failure === '503 runner service unavailable') {
        await page.evaluate(() => (window as any).mount(false))
        await input.waitFor({ state: 'detached' })
        await page.evaluate(() => (window as any).mount(true))
        await open.waitFor()
        assert.equal(await open.isDisabled(), true, 'remount cannot unlock the pending operation')
        await page.evaluate(() => { void (window as any).submit('  preserve exact instructions  ') })
        assert.equal(await page.evaluate(() => (window as any).calls.length), before)
      }
      if (failure === 'missing') await page.evaluate(() => (window as any).resolve({ status: 'reopened' }))
      else await page.evaluate(message => (window as any).reject(message), failure)
      await resume.waitFor()
      assert.equal(await input.inputValue(), draft)
      const alert = page.getByRole('alert').filter({ hasText: 'Action Failed' })
      await alert.waitFor()
      assert.match(await alert.innerText(), failure === 'missing' ? /no confirmed task authority/ : new RegExp(failure))
      assert.equal(await page.getByTestId('toggle-task-details-btn').getAttribute('aria-expanded'), 'false')
      await page.getByTestId('toggle-task-details-btn').click()
      assert.equal(await page.getByTestId('task-error-banner').count(), 1)
      await page.getByTestId('toggle-task-details-btn').click()
      assert.equal(await page.evaluate(() => (window as any).applied.length), 0)
    }
    const bodies = await page.evaluate(() => (window as any).calls)
    assert.ok(bodies.every((body: any) => body.feedback === draft && body.revision === 7 && body.client_request_id === bodies[0].client_request_id))
    await resume.click()
    await pending.waitFor()
    await page.evaluate(() => (window as any).resolve({ status: 'reopened', task: { id: 'task-a', session_id: 'new', revision: 8, status: 'in_progress' } }))
    await input.waitFor({ state: 'detached' })
    assert.equal(await page.evaluate(() => (window as any).applied.length), 1)
    assert.equal(await page.evaluate(() => (window as any).applied[0].task.sessionId), 'new')
    assert.equal(await page.evaluate(() => (window as any).applied[0].task.revision), 8)
    await open.click()
    assert.equal(await input.inputValue(), '')
    await input.fill('   ')
    await resume.click()
    await pending.waitFor()
    assert.equal(await page.evaluate(() => (window as any).calls.at(-1).feedback), 'Continue this task and address remaining requirements.')
    await page.evaluate(() => (window as any).resolve({ status: 'reopened', task: { id: 'task-a', session_id: 'blank', revision: 9 } }))
    await input.waitFor({ state: 'detached' })
    await open.click()
    await input.fill('project switch')
    await resume.click()
    await pending.waitFor()
    await page.evaluate(() => (window as any).project('project-b'))
    await open.click()
    await input.fill('other project draft')
    await page.evaluate(() => (window as any).resolve({ status: 'reopened', task: { id: 'task-a', session_id: 'newer', revision: 9 } }))
    await page.waitForFunction(() => (window as any).applied.at(-1)?.task.sessionId === 'newer')
    assert.equal(await input.inputValue(), 'other project draft')
    assert.equal(await page.evaluate(() => (window as any).applied.at(-1).project), 'project-a')
    await page.evaluate(() => (window as any).approval(true))
    const approvalError = page.getByTestId('task-error-banner')
    await approvalError.waitFor()
    assert.match(await approvalError.innerText(), /Approval failed/)
    await page.getByTestId('retry-approve-btn').click()
    assert.equal(await page.evaluate(() => (window as any).approvals), 1)
    await page.getByTestId('toggle-task-details-btn').click()
    assert.equal(await approvalError.count(), 1, 'approval retains one accessible retry banner outside disclosure')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
