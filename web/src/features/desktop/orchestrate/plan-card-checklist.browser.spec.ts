import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: MinimalTaskCard exposes current bound outcomes while pending and collapsed,
// never technical narratives or mutable completion state. Its existing disclosure
// and approval boundaries must stay independent. This browser component test is
// the narrowest layer proving real clicks, keyboard disclosure, and same-card
// revision updates; fixtures do not claim daemon/provider execution.
test('plan cards show current checklists before disclosure without approving or selecting', { timeout: 60000 }, async () => {
  const document = {
    title: 'Improve search', info: { goal: 'Make results useful', notes: 'Retain all technical notes' },
    requirements: [
      { id: 'r2', text: 'Search results include the matching filename.', checkpoint_id: 'cp2' },
      { id: 'r1', text: 'Users can search all their project files.', checkpoint_id: 'cp1' },
    ],
    checkpoints: [
      { id: 'cp1', title: 'Index files', tasks: ['Technical index migration'], acceptance_criteria: ['Users can search all their project files.', 'Branch clean'] },
      { id: 'cp2', title: 'Render results', tasks: ['Technical renderer rewrite'], acceptance_criteria: ['Search results include the matching filename.'] },
    ],
  }
  const task = { id: 'proposal', title: 'Search improvements', tier: 'complex', status: 'pending_approval', agentType: 'plan', outcomeType: 'plan_spec', agents: [], planSummary: 'Generic deliverable ready narrative', revision: 1, planDocument: document, planBinding: { planId: 'plan', sessionId: 'session', definitionRevision: 1 } }
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    const root=createRoot(document.getElementById('root')); const original=${JSON.stringify(task)};
    window.calls=[];
    window.renderTask=(mode='current',busy=false)=>{
      let task={...original};
      if(mode==='revised') task={...task,revision:2,planBinding:{...task.planBinding,definitionRevision:2},planDocument:{...task.planDocument,requirements:[{id:'r2',text:'Results highlight the matching filename.',checkpoint_id:'cp2'}],checkpoints:[{...task.planDocument.checkpoints[1],acceptance_criteria:['Results highlight the matching filename.']}]}};
      if(mode==='legacy') task={...task,planDocument:{...task.planDocument,requirements:undefined}};
      if(mode==='missing') task={...task,planDocument:null};
      if(mode==='invalid') task={...task,planDocument:{...task.planDocument,requirements:[{id:'bad',text:'Unbound obsolete outcome',checkpoint_id:'gone'}]}};
      if(mode==='running') task={...task,status:'running'};
      if(mode==='plain') task={...task,agentType:'coder',tier:'direct',outcomeType:'code_pr',planDocument:null,planBinding:undefined};
      root.render(<QueryClientProvider client={client}><MinimalTaskCard task={task} isApproving={busy} onApprove={()=>window.calls.push('approve')} onSelect={()=>window.calls.push('select')}/></QueryClientProvider>);
    }; window.renderTask();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: new URL(route.request().url()).pathname === '/' ? 'text/html' : 'application/json', body: new URL(route.request().url()).pathname === '/' ? '<div id="root"></div>' : '{}' }))
    await page.goto('https://plan-card.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const checklist = page.getByRole('region', { name: 'Plan checklist', exact: true })
    const details = page.getByTestId('toggle-task-details-btn')
    const full = page.getByRole('region', { name: 'Full current plan', exact: true })
    await checklist.waitFor()
    assert.deepEqual(await checklist.locator('li').allTextContents(), document.requirements.map(r => `□${r.text}`))
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    assert.equal(await full.count(), 0)
    assert.equal(await checklist.getByRole('checkbox').count(), 0)
    assert.doesNotMatch(await page.getByTestId('orchestrate-task-card').innerText(), /Technical index migration|Generic deliverable ready narrative|Branch clean/)
    await details.focus(); await details.press('Enter')
    await full.waitFor()
    assert.match(await full.innerText(), /Technical index migration/)
    assert.match(await full.innerText(), /Retain all technical notes/)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await details.click()
    assert.equal(await full.count(), 0)
    await page.getByTestId('approve-task-btn').click()
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['approve'])
    await page.evaluate(() => (window as any).renderTask('current', true))
    await page.waitForFunction(() => (document.querySelector('[data-testid="approve-task-btn"]') as HTMLButtonElement)?.disabled)
    await page.evaluate(() => (window as any).renderTask('revised'))
    await checklist.getByText('Results highlight the matching filename.', { exact: true }).waitFor()
    assert.doesNotMatch(await checklist.innerText(), /Users can search|Search results include/)
    await checklist.getByText('Results highlight the matching filename.', { exact: true }).click()
    await full.waitFor()
    assert.doesNotMatch(await full.innerText(), /Users can search|Search results include/)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['approve', 'select'])
    await details.click()
    await page.evaluate(() => (window as any).renderTask('legacy'))
    await checklist.getByText('Users can search all their project files.', { exact: true }).waitFor()
    assert.deepEqual(await checklist.locator('li').allTextContents(), document.checkpoints.flatMap(cp => cp.acceptance_criteria.filter(text => text !== 'Branch clean').map(text => `□${text}`)))
    assert.equal(await page.getByTestId('approve-task-btn').isDisabled(), true, 'legacy presentation must not loosen approval validation')
    await page.evaluate(() => (window as any).renderTask('invalid'))
    await checklist.getByRole('alert').waitFor()
    assert.equal(await checklist.locator('li').count(), 0, 'invalid authored requirements must not fall back to unrelated criteria')
    await page.evaluate(() => (window as any).renderTask('missing'))
    await page.waitForFunction(() => !document.querySelector('[aria-label="Plan checklist"] [role="alert"]'))
    assert.match(await checklist.innerText(), /No bound requirements or acceptance criteria/)
    await page.evaluate(() => (window as any).renderTask('running'))
    await checklist.waitFor({ state: 'detached' })
    assert.equal(await page.getByTestId('approve-task-btn').count(), 0)
    await details.click(); await full.waitFor()
    assert.match(await full.innerText(), /Technical renderer rewrite/)
    await details.click()
    await page.evaluate(() => (window as any).renderTask('plain'))
    await checklist.waitFor({ state: 'detached' })
    assert.equal(await full.count(), 0)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})

// Requirement: pending review is task lifecycle state, not the presence of a
// retained plan. MinimalTaskCard must follow the accepted response immediately,
// retain pending review on failure, and permit a newly published revision. The
// production mapper/reducer used by handleApproveTask owns optimistic receipts
// and stale snapshot fencing. This component harness exercises those boundaries
// with controlled outcomes, not a live approval API or provider run.
test('acceptance hides pending review across stale refresh, completion hydration and fresh revisions', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {mapBackendTask,reduceDesktopProjectsState} from './src/features/desktop/state/desktop-projects-state';
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    let root=createRoot(document.getElementById('root'));
    const original={id:'proposal',title:'Search improvements',tier:'complex',status:'pending_approval',agent:'plan',outcome_type:'plan_spec',revision:1,
      plan_binding:{plan_id:'plan',session_id:'session',definition_revision:1},
      plan_document:{title:'Improve search',status:'pending',info:{goal:'Make results useful'},
        requirements:[{id:'r',text:'Results show the filename.',checkpoint_id:'cp'}],
        checkpoints:[{id:'cp',title:'Render results',tasks:['Technical renderer rewrite'],acceptance_criteria:['Results show the filename.']}]}};
    let state={},busy=false,error='',serial=0;
    window.calls=[];
    const dispatch=action=>{state=reduceDesktopProjectsState(state,action)};
    const snapshot=task=>{
      const requestId='snapshot-'+(++serial);
      dispatch({type:'projects.beginLoad',projectId:'project',requestId});
      dispatch({type:'projects.loadSuccess',projectId:'project',requestId,generation:state.project.generation,tasks:[mapBackendTask(task)]});
    };
    const render=()=>root.render(<QueryClientProvider client={client}><MinimalTaskCard task={state.project.tasks[0]} isApproving={busy} taskError={error}
      onApprove={()=>{window.calls.push('approve');busy=true;error='';render()}}
      onSelect={()=>window.calls.push('select')}/></QueryClientProvider>);
    window.settleApproval=ok=>{
      if(ok) dispatch({type:'projects.updateTasks',projectId:'project',tasks:previous=>previous.map(task=>mapBackendTask({...original,status:'queued',revision:2}))});
      else error='Acceptance failed';
      busy=false;render();
    };
    window.staleRefresh=()=>{snapshot(original);render()};
    window.hydrate=(status,revision,remount=false)=>{
      if(remount){root.unmount();root=createRoot(document.getElementById('root'));state={}}
      snapshot({...original,status,revision});render();
    };
    window.revise=()=>{
      const text='Results highlight the filename.';
      snapshot({...original,revision:8,plan_binding:{...original.plan_binding,definition_revision:2},plan_document:{...original.plan_document,
        requirements:[{id:'r',text,checkpoint_id:'cp'}],checkpoints:[{...original.plan_document.checkpoints[0],acceptance_criteria:[text]}]}});render();
    };
    snapshot(original);render();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: new URL(route.request().url()).pathname === '/' ? 'text/html' : 'application/json', body: new URL(route.request().url()).pathname === '/' ? '<div id="root"></div>' : '{}' }))
    await page.goto('https://plan-lifecycle.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const checklist = page.getByRole('region', { name: 'Plan checklist', exact: true })
    const approve = page.getByTestId('approve-task-btn')
    const details = page.getByTestId('toggle-task-details-btn')
    const card = page.getByTestId('orchestrate-task-card')
    const full = page.getByRole('region', { name: 'Full current plan', exact: true })
    await checklist.waitFor()
    await approve.click()
    await page.waitForFunction(() => (document.querySelector('[data-testid="approve-task-btn"]') as HTMLButtonElement)?.disabled)
    assert.equal(await checklist.isVisible(), true, 'in-flight is not acceptance')
    await page.evaluate(() => (window as any).settleApproval(false))
    await page.getByTestId('task-error-banner').waitFor()
    assert.equal(await checklist.isVisible(), true)
    assert.equal(await approve.isEnabled(), true)
    await page.getByTestId('retry-approve-btn').click()
    await page.evaluate(() => (window as any).settleApproval(true))
    await checklist.waitFor({ state: 'detached' })
    assert.equal(await approve.count(), 0)
    assert.equal(await page.locator('.swarm-task-proposal').count(), 0)
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['approve', 'approve'])
    await page.evaluate(() => (window as any).staleRefresh())
    assert.equal(await card.getAttribute('data-task-state'), 'queued', 'older pending snapshot cannot replace acceptance receipt')
    assert.equal(await checklist.count(), 0)
    for (const [status, revision] of [['in_progress', 3], ['running', 4], ['completed', 5], ['failed', 6], ['rejected', 7]] as const) {
      await page.evaluate(([status, revision]) => (window as any).hydrate(status, revision), [status, revision])
      await page.waitForFunction(status => document.querySelector('[data-testid="orchestrate-task-card"]')?.getAttribute('data-task-state') === status, status === 'in_progress' ? 'running' : status)
      assert.equal(await card.isVisible(), true)
      assert.equal(await checklist.count(), 0, `${status} must not show retained pending requirements`)
      assert.equal(await page.locator('.swarm-task-proposal').count(), 0)
      await details.click(); await full.waitFor()
      assert.match(await full.innerText(), /Results show the filename\./)
      assert.match(await full.innerText(), /Technical renderer rewrite/)
      await details.click()
      assert.equal(await full.count(), 0)
    }
    await page.evaluate(() => (window as any).hydrate('completed', 7, true))
    await page.waitForFunction(() => document.querySelector('[data-testid="orchestrate-task-card"]')?.getAttribute('data-task-state') === 'completed')
    assert.equal(await checklist.count(), 0, 'fresh hydration must not depend on a local accepted flag')
    assert.equal(await approve.count(), 0)
    await details.click(); await full.waitFor()
    assert.match(await full.innerText(), /Technical renderer rewrite/)
    await details.click()
    await page.evaluate(() => (window as any).revise())
    await checklist.getByText('Results highlight the filename.', { exact: true }).waitFor()
    assert.doesNotMatch(await checklist.innerText(), /Results show the filename/)
    assert.equal(await approve.isEnabled(), true, 'new pending definition permits fresh acceptance')
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    await approve.click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['approve', 'approve', 'approve'])
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
