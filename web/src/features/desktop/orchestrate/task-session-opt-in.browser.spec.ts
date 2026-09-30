import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: task inspection, referencing and error presentation are separated
// from explicit execution-chat consent. Threat: draft loss, implicit planner dispatch,
// bulk selection becoming context authority, and stale attempt errors. Authority:
// MinimalTaskCard, OrchestratorChatComposer, aggregateTaskLiveState. This rendered
// component composition proves their interaction, not backend recovery dispatch or
// the full OrchestrateView create/approve handlers (parent integration validation).
test('task details, independent context chips and execution errors preserve unsent chat', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard,OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    import {aggregateTaskLiveState} from './src/features/desktop/orchestrate/orchestrate-task-helpers';
    const rows=[{id:'task_a',title:'Alpha',status:'running',agentType:'coder',revision:1,sessionId:'session_a',progress:10},{id:'task_b',title:'Beta',status:'running',agentType:'coder',revision:1,sessionId:'session_b',progress:20}];
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
  try {
    const page = await browser.newPage()
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
    await cards.first().getByRole('button', { name: 'Ask Orchestrator', exact: true }).click()
    await cards.nth(1).getByRole('button', { name: 'Ask Orchestrator', exact: true }).click()
    assert.equal(await page.getByTestId('composer-attached-task').count(), 2)
    await page.getByRole('button', { name: 'Remove task context Alpha', exact: true }).click()
    assert.equal(await page.getByTestId('composer-attached-task').count(), 1)
    await cards.first().getByRole('checkbox').check()
    await cards.nth(1).getByRole('checkbox').check()
    await cards.first().getByRole('button', { name: 'Archive task', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).archived), ['task_a'])
    await page.evaluate(() => (window as any).fail())
    await page.getByTestId('task-session-error').waitFor()
    assert.match(await page.getByTestId('task-session-error').textContent() || '', /Permission denied/)
    assert.doesNotMatch(await page.getByTestId('task-session-error').textContent() || '', /fixture-secret/)
    assert.equal(await page.getByTestId('conversation').textContent(), 'orchestrator')
    assert.deepEqual(await page.evaluate(() => (window as any).planned), [])
    await page.getByTestId('task-session-error').getByRole('button', { name: 'Investigate session', exact: true }).click()
    assert.equal(await page.getByTestId('conversation').textContent(), 'session_a')
    await page.getByRole('button', { name: 'Back to Orchestrator', exact: true }).click()
    await page.evaluate(() => (window as any).retry())
    await page.waitForFunction(() => !document.querySelector('[data-testid="task-session-error"]'))
    assert.equal(await input.inputValue(), 'Keep my unsent draft')
    assert.equal(await input.evaluate(node => node === (window as any).originalComposer), true)
    assert.deepEqual(await page.evaluate(() => (window as any).sent), [])
  } finally { await browser.close() }
})
