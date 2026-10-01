import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import type { RunningTask } from './orchestrate-types'

const rows: RunningTask[] = ['Alpha', 'Beta'].map((title, index) => ({
  id: index ? 'task_b' : 'task_a', title, status: 'running', agentType: 'coder', revision: 1,
  sessionId: index ? 'session_b' : 'session_a', progress: index ? 20 : 10,
  workspaceTarget: 'local', sourceWorkspacePath: '/repo', elapsed: '0s',
  subtasks: [{ id: 'subtask', title: 'Inspect keyboard navigation', completed: false }],
  planSummary: 'Implement accessible keyboard navigation',
  activePlanCheckpoints: [{ id: 'checkpoint', title: 'Keyboard plan checkpoint', status: 'in_progress',
    subtasks: [{ id: 'subtask', title: 'Inspect keyboard navigation', completed: false, status: 'in_progress' }] }],
  currentTool: 'read keyboard.ts', currentToolEventKey: 'event-first',
}))

// Requirement: task inspection, referencing and error presentation are separated
// from explicit execution-chat consent. Threat: draft loss, implicit planner dispatch,
// bulk selection becoming context authority, stale attempt errors and losing expanded
// plan/subtasks/activity while simplifying collapsed cards. Click the production
// details toggle, not fixture state, to prove expansion and draft persistence. Authority:
// MinimalTaskCard, OrchestratorChatComposer, aggregateTaskLiveState. This rendered
// component composition proves their interaction, not backend recovery dispatch or
// the full OrchestrateView create/approve handlers (parent integration validation).
test('task details, independent context chips and execution errors preserve unsent chat', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard,OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    import {aggregateTaskLiveState} from './src/features/desktop/orchestrate/orchestrate-task-helpers';
    const rows=${JSON.stringify(rows)};
    window.sent=[];window.planned=[];window.archived=[];
    function App(){const [tasks,setTasks]=useState(rows);const [ids,setIds]=useState([]);const [marked,setMarked]=useState([]);const [session,setSession]=useState('orchestrator');const [selected,setSelected]=useState('');
      window.fail=()=>setTasks(rows.map((t,i)=>i?t:aggregateTaskLiveState(t,{session_a:{view:{current_run_state:{session_id:'session_a',run_id:'attempt_current',status:'failed',active:false,blocked_reason:'Permission denied\\napi_key=fixture-secret'}}}})));
      window.retry=()=>setTasks(rows.map((t,i)=>i?t:aggregateTaskLiveState(t,{session_a:{view:{current_run_state:{session_id:'session_a',run_id:'attempt_new',created_at:2,status:'running',active:true}},intent:{run_id:'attempt_old',created_at:1,status:'failed',blocked_reason:'Old failure'}}})));
      return <><p data-testid='conversation'>{session}</p>{session!=='orchestrator'&&<button onClick={()=>setSession('orchestrator')}>Back to Orchestrator</button>}
      {tasks.map(t=><MinimalTaskCard key={t.id} task={t} isSelected={selected===t.id} isMarked={marked.includes(t.id)} onToggleMarked={()=>setMarked(v=>v.includes(t.id)?v.filter(id=>id!==t.id):[...v,t.id])} onSelect={()=>setSelected(t.id)} onAskOrchestrator={()=>setIds(v=>v.includes(t.id)?v:[...v,t.id])} onArchiveTask={()=>window.archived.push(t.id)} onOpenChat={()=>setSession(t.sessionId)} onInvestigateSession={setSession} onRefine={()=>window.planned.push(t.id)}/>)}
      <OrchestratorChatComposer sessionId='orchestrator' attachedTasks={tasks.filter(t=>ids.includes(t.id))} onRemoveAttachedTask={id=>setIds(v=>v.filter(value=>value!==id))} submitMessage={async op=>window.sent.push(op)}/></>}
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><App/></QueryClientProvider>);
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  const pageErrors: string[] = []
  const assertNoPageErrors = () => assert.deepEqual(pageErrors, [], 'task expansion must not throw a browser render error')
  try {
    const page = await browser.newPage()
    page.on('pageerror', error => pageErrors.push(error.message))
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://task.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByTestId('orchestrator-chat-input')
    await input.fill('Keep my unsent draft')
    await input.evaluate(node => { (window as any).originalComposer = node })
    const cards = page.getByTestId('orchestrate-task-card')
    await cards.first().getByText('Alpha', { exact: true }).first().click()
    assert.equal(await page.getByTestId('conversation').textContent(), 'orchestrator')
    assert.equal(await page.getByTestId('composer-attached-task').count(), 0)
    // The title bubbles to the production card handler: select AND expand, not chat.
    const titleToggle = cards.first().getByTestId('toggle-task-details-btn')
    assert.equal(await titleToggle.getAttribute('aria-expanded'), 'true')
    await cards.first().getByText('Keyboard plan checkpoint', { exact: true }).waitFor()
    assertNoPageErrors()
    // Deliberately reset so later false -> true assertions exercise a real toggle.
    await titleToggle.click()
    assert.equal(await titleToggle.getAttribute('aria-expanded'), 'false')
    assert.equal(await cards.first().getByText('Keyboard plan checkpoint', { exact: true }).count(), 0)
    assertNoPageErrors()
    await cards.first().getByRole('button', { name: 'Ask orchestrator', exact: true }).click()
    await cards.nth(1).getByRole('button', { name: 'Ask orchestrator', exact: true }).click()
    assert.equal(await page.getByTestId('composer-attached-task').count(), 2)
    await page.getByRole('button', { name: 'Remove task context Alpha', exact: true }).click()
    assert.equal(await page.getByTestId('composer-attached-task').count(), 1)
    await cards.first().getByRole('checkbox').check()
    await cards.nth(1).getByRole('checkbox').check()
    await cards.first().getByRole('button', { name: 'Archive task', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).archived), ['task_a'])
    const runningToggle = cards.first().getByTestId('toggle-task-details-btn')
    assert.equal(await runningToggle.getAttribute('aria-expanded'), 'false')
    await runningToggle.click()
    assert.equal(await runningToggle.getAttribute('aria-expanded'), 'true')
    await cards.first().getByText('Full Plan, Subtasks & Activity Logs', { exact: true }).waitFor()
    await cards.first().getByText('Keyboard plan checkpoint', { exact: true }).waitFor()
    await cards.first().getByText('Inspect keyboard navigation', { exact: true }).waitFor()
    assert.match(await cards.first().getByTestId('task-card-activity').innerText(), /keyboard.ts/)
    assertNoPageErrors()
    await runningToggle.click()
    assert.equal(await runningToggle.getAttribute('aria-expanded'), 'false')
    assert.match(await cards.first().getByTestId('task-card-activity').innerText(), /keyboard.ts/, 'collapse keeps the real activity preview')
    await page.evaluate(() => (window as any).fail())
    const alpha = cards.first()
    const toggle = alpha.getByTestId('toggle-task-details-btn')
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await alpha.getByRole('alert').filter({ hasText: '1 session error · open details' }).waitFor()
    assert.equal(await alpha.getByTestId('task-session-error').count(), 0)
    assert.doesNotMatch(await alpha.textContent() || '', /fixture-secret|Permission denied/)
    assert.equal(await alpha.getByText('Keyboard plan checkpoint', { exact: true }).count(), 0)
    await toggle.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true')
    await alpha.getByText('Full Plan, Subtasks & Activity Logs', { exact: true }).waitFor()
    // Execution Summary is a labeled container, not an exact-text summary leaf.
    // MinimalTaskCard shows it for expanded tasks with planSummary, including failed.
    const executionSummary = alpha.getByText('Execution Summary', { exact: true }).locator('..')
    await executionSummary.waitFor()
    assert.match(await executionSummary.innerText(), /Implement accessible keyboard navigation/)
    // The running execution checklist and stream are scoped to running attempts;
    // failed details retain the execution summary and investigation evidence instead.
    assert.equal(await alpha.getByText('Keyboard plan checkpoint', { exact: true }).count(), 0)
    assert.equal(await alpha.getByText('Inspect keyboard navigation', { exact: true }).count(), 0)
    assert.equal(await alpha.getByTestId('task-card-activity').count(), 0, 'failed runs must not present a live stream')
    assertNoPageErrors()
    await page.getByTestId('task-session-error').waitFor()
    assert.match(await page.getByTestId('task-session-error').textContent() || '', /Permission denied/)
    assert.doesNotMatch(await page.getByTestId('task-session-error').textContent() || '', /fixture-secret/)
    assert.equal(await page.getByTestId('conversation').textContent(), 'orchestrator')
    assert.deepEqual(await page.evaluate(() => (window as any).planned), [])
    // Collapse/reopen must retain the evidence, actions and the unsent composer.
    await toggle.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    assert.equal(await alpha.getByTestId('task-session-error').count(), 0)
    assert.equal(await alpha.getByText('Full Plan, Subtasks & Activity Logs', { exact: true }).count(), 0)
    assert.equal(await alpha.getByText('Inspect keyboard navigation', { exact: true }).count(), 0)
    assert.equal(await input.inputValue(), 'Keep my unsent draft')
    assert.equal(await input.evaluate(node => node === (window as any).originalComposer), true)
    assert.equal(await alpha.getByRole('button', { name: 'Ask orchestrator', exact: true }).isEnabled(), true)
    assert.equal(await alpha.getByRole('button', { name: 'Archive task', exact: true }).isEnabled(), true)
    await toggle.click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true')
    // The running execution checklist and stream are scoped to running attempts;
    // failed details retain the execution summary and investigation evidence instead.
    assert.equal(await alpha.getByText('Keyboard plan checkpoint', { exact: true }).count(), 0)
    assert.equal(await alpha.getByText('Inspect keyboard navigation', { exact: true }).count(), 0)
    assert.equal(await alpha.getByTestId('task-card-activity').count(), 0, 'failed runs must not present a live stream')
    assertNoPageErrors()
    assert.doesNotMatch(await page.getByTestId('task-session-error').textContent() || '', /fixture-secret/)
    await page.getByTestId('task-session-error').getByRole('button', { name: 'Investigate session', exact: true }).click()
    assert.equal(await page.getByTestId('conversation').textContent(), 'session_a')
    await page.getByRole('button', { name: 'Back to Orchestrator', exact: true }).click()
    await page.evaluate(() => (window as any).retry())
    await page.waitForFunction(() => !document.querySelector('[data-testid="task-session-error"]'))
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true')
    await alpha.getByText('Keyboard plan checkpoint', { exact: true }).waitFor()
    assert.match(await alpha.getByTestId('task-card-activity').innerText(), /Thinking/, 'new run without tool evidence must not replay the old event')
    assert.equal(await input.inputValue(), 'Keep my unsent draft')
    assert.equal(await input.evaluate(node => node === (window as any).originalComposer), true)
    assert.deepEqual(await page.evaluate(() => (window as any).sent), [])
    assertNoPageErrors()
  } catch (error) {
    assertNoPageErrors() // Surface render errors instead of a later missing-control timeout.
    throw error
  } finally { await browser.close() }
})
