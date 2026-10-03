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
  const planDocument = {
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
  const task = { id: 'proposal', title: 'Search improvements', tier: 'complex', status: 'pending_approval', agentType: 'plan', outcomeType: 'plan_spec', agents: [], planSummary: 'Generic deliverable ready narrative', revision: 1, planDocument, planBinding: { planId: 'plan', sessionId: 'session', definitionRevision: 1 } }
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
    assert.deepEqual(await checklist.locator('li').allTextContents(), planDocument.requirements.map(r => `□${r.text}`))
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    assert.equal(await full.count(), 0)
    assert.equal(await checklist.getByRole('checkbox').count(), 0)
    assert.doesNotMatch(await page.getByTestId('orchestrate-task-card').innerText(), /Technical index migration|Generic deliverable ready narrative|Branch clean/)
    await details.focus(); await details.press('Enter')
    await full.waitFor()
    assert.match(await full.innerText(), /Technical index migration/)
    assert.match(await full.innerText(), /Retain all technical notes/)
    assert.match(await full.innerText(), /Make results useful/)
    assert.doesNotMatch(await full.innerText(), /"checkpoints"|"acceptance_criteria"|"info"|Generic deliverable ready/)
    assert.equal(await full.locator('pre').count(), 0)
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
    assert.deepEqual(await checklist.locator('li').allTextContents(), planDocument.checkpoints.flatMap(cp => cp.acceptance_criteria.filter(text => text !== 'Branch clean').map(text => `□${text}`)))
    assert.equal(await page.getByTestId('approve-task-btn').isDisabled(), true, 'legacy presentation must not loosen approval validation')
    await details.click(); await full.waitFor()
    assert.match(await full.innerText(), /Make results useful/)
    assert.match(await full.innerText(), /Technical index migration/)
    assert.doesNotMatch(await full.innerText(), /"checkpoints"|"acceptance_criteria"/)
    await details.click()
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
      assert.match(await full.innerText(), /Make results useful/)
      assert.doesNotMatch(await full.innerText(), /"checkpoints"|"acceptance_criteria"|"info"/)
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

// Requirement: direct Coder proposals expose authored changes while collapsed,
// not mapBackendTask's synthetic pipeline or verification text. MinimalTaskCard
// owns visibility; mapper/reducer receipts own acceptance. Browser interactions
// prove disclosure and lifecycle updates without claiming live backend execution.
test('direct coding proposals show changes until accepted and retain details', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {mapBackendTask} from './src/features/desktop/state/desktop-projects-state';
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    const root=createRoot(document.getElementById('root'));
    const original={id:'direct',title:'Improve search',agent:'coder',tier:'direct',outcome_type:'code_pr',
      description:'- [ ] Search every project file.\\n- Show matching filenames.',
      plan_summary:'Code PR & Verified Tests ready',full_plan_markdown:'## Technical details\\nKeep the existing index format.',
      pipeline_stages:['Code Implementation','Verification & Pull Request']};
    window.calls=[];
    window.renderTask=(status='pending_approval',description=original.description)=>root.render(
      <QueryClientProvider client={client}><MinimalTaskCard task={mapBackendTask({...original,status,description})}
        onApprove={()=>window.calls.push('approve')} onSelect={()=>window.calls.push('select')}/></QueryClientProvider>);
    window.renderTask();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: new URL(route.request().url()).pathname === '/' ? 'text/html' : 'application/json', body: new URL(route.request().url()).pathname === '/' ? '<div id="root"></div>' : '{}' }))
    await page.goto('https://direct-card.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const checklist = page.getByRole('region', { name: 'Task checklist', exact: true })
    const details = page.getByTestId('toggle-task-details-btn')
    const card = page.getByTestId('orchestrate-task-card')
    await checklist.waitFor()
    assert.deepEqual(await checklist.locator('li').allTextContents(), ['□Search every project file.', '□Show matching filenames.'])
    assert.equal(await checklist.getByRole('checkbox').count(), 0)
    assert.equal(await details.getAttribute('aria-expanded'), 'false')
    assert.doesNotMatch(await card.innerText(), /Code Implementation|Verification & Pull Request|Code PR & Verified Tests/)
    await checklist.getByText('Show matching filenames.', { exact: true }).click()
    await page.getByRole('region', { name: 'Full proposed task', exact: true }).getByText(/Keep the existing index format\./).waitFor()
    const full = page.getByRole('region', { name: 'Full proposed task', exact: true })
    assert.equal(await full.getByRole('heading', { name: 'Technical details', exact: true }).isVisible(), true)
    assert.match(await full.innerText(), /Search every project file/)
    assert.doesNotMatch(await full.innerText(), /##|Code Implementation|Verification & Pull Request/)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['select'])
    await details.click()
    await page.getByTestId('approve-task-btn').click()
    assert.equal(await checklist.isVisible(), true, 'click alone is not acceptance')
    for (const status of ['queued', 'running', 'completed']) {
      await page.evaluate(status => (window as any).renderTask(status), status)
      await checklist.waitFor({ state: 'detached' })
      await page.waitForFunction(status => document.querySelector('[data-testid="orchestrate-task-card"]')?.getAttribute('data-task-state') === status, status)
      assert.equal(await page.getByTestId('approve-task-btn').count(), 0)
      await details.click()
      await page.getByRole('region', { name: 'Full proposed task', exact: true }).getByText(/Keep the existing index format\./).waitFor()
      assert.equal(await full.getByRole('heading', { name: 'Technical details', exact: true }).isVisible(), true)
      assert.match(await full.innerText(), /Search every project file/)
      assert.doesNotMatch(await full.innerText(), /##|Code Implementation|Verification & Pull Request/)
      await details.click()
    }
    await page.evaluate(() => (window as any).renderTask('pending_approval', ''))
    await checklist.waitFor()
    assert.equal(await checklist.locator('li').count(), 0, 'missing scope must not invent changes from stages or verification')
    assert.match(await checklist.innerText(), /No proposed changes/)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})

// Requirement: the ordinary expanded MinimalTaskCard DOM must read persisted,
// direct serialized, and legacy plans without exposing API objects or prompts.
// Controlled hydration checks pending/accepted/completed presentation independently
// of authority; browser DOM (not hidden payloads) proves actual disclosure output.
test('expanded persisted and legacy plans remain readable throughout acceptance lifecycle', { timeout: 60000 }, async () => {
  const plan = { title: 'Recover preferences', info: { goal: 'Retain preferences after interruption', notes: 'Keep the backup file' }, requirements: [{ id: 'r', checkpoint_id: 'cp', text: 'Old preferences survive an interrupted write' }], checkpoints: [{ id: 'cp', title: 'Write safely', tasks: ['Flush then rename'], acceptance_criteria: ['Old preferences survive an interrupted write'], task_program: { stages: [{ id: 's', title: 'Storage changes' }], jobs: [{ id: 'j', stage_id: 's', title: 'Atomic persistence', deliverable: 'Recoverable preferences', acceptance_criteria: ['Recovery preserves every value'], meta_prompt: 'PRIVATE_AGENT_PROMPT' }] } }], execution_state: { current_run_id: 'PRIVATE_ROUTING' } }
  const legacy = { title: plan.title, goal: plan.info.goal, checkpoints: [{ id: 'cp', title: 'Write safely', subtasks: [{ title: 'Flush then rename', notes: 'Keep the backup file' }], acceptanceCriteria: [{ criterion: 'Old preferences survive an interrupted write' }] }] }
  const markdown = '## Recover preferences\n\nRetain preferences after interruption\n\n### Tasks\n- Flush then rename\n\n### Acceptance criteria\n- [ ] Old preferences survive an interrupted write\n\nKeep the backup file'
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {mapBackendTask} from './src/features/desktop/state/desktop-projects-state';
    const root=createRoot(document.getElementById('root')); const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    const plan=${JSON.stringify(plan)}, legacy=${JSON.stringify(legacy)}, markdown=${JSON.stringify(markdown)};
    window.renderVariant=(variant,status='pending_approval')=>{
      const base={id:'proposal',title:'Recover preferences',status,agent:'plan',outcome_type:'plan_spec',description:'Preserve saved preferences',revision:1};
      const sources={persisted:{plan_document:JSON.stringify({document:plan})},legacy:{plan_document:legacy},prose:{plan_document:markdown},
        direct:{agent:'coder',outcome_type:'code_pr',full_plan_markdown:JSON.stringify(plan)},
        directMarkdown:{agent:'coder',outcome_type:'code_pr',full_plan_markdown:markdown},
        unknown:{plan_document:{schema:{type:'PRIVATE_SCHEMA'},meta_prompt:'PRIVATE_PROMPT'}},
        malformed:{plan_document:'{"checkpoints":'}};
      root.render(<QueryClientProvider client={client}><MinimalTaskCard key={variant+status} task={mapBackendTask({...base,...sources[variant]})} onApprove={()=>{}} /></QueryClientProvider>);
    };window.renderVariant('persisted');
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: new URL(route.request().url()).pathname === '/' ? 'text/html' : 'application/json', body: new URL(route.request().url()).pathname === '/' ? '<div id="root"></div>' : '{}' }))
    await page.goto('https://persisted-plan.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    for (const variant of ['persisted', 'legacy', 'prose', 'direct', 'directMarkdown']) {
      for (const status of ['pending_approval', 'running', 'completed']) {
        await page.evaluate(([variant, status]) => (window as any).renderVariant(variant, status), [variant, status])
        const card = page.getByTestId('orchestrate-task-card')
        await page.waitForFunction(status => document.querySelector('[data-testid="orchestrate-task-card"]')?.getAttribute('data-task-state') === status, status)
        assert.equal(await card.locator('[aria-label="Plan checklist"], [aria-label="Task checklist"]').count(), status === 'pending_approval' ? 1 : 0)
        await page.getByTestId('toggle-task-details-btn').click()
        const full = page.getByRole('region', { name: variant.startsWith('direct') ? 'Full proposed task' : 'Full current plan', exact: true })
        await full.waitFor()
        for (const content of ['Retain preferences after interruption', 'Flush then rename', 'Old preferences survive an interrupted write', 'Keep the backup file']) assert.ok((await full.innerText()).includes(content), `${variant}/${status}: ${content}`)
        assert.doesNotMatch(await full.innerText(), /PRIVATE_|"checkpoints"|"document"|acceptance_criteria|meta_prompt|##|Code Implementation/)
        assert.equal(await full.locator('pre').count(), 0)
        assert.ok(await full.getByRole('heading').count() >= 3)
        if (variant === 'persisted' || variant === 'direct') {
          assert.match(await full.innerText(), /Atomic persistence/)
          assert.match(await full.innerText(), /Recovery preserves every value/)
        }
        if (variant === 'prose' || variant === 'directMarkdown') assert.equal(await full.getByRole('heading', { name: 'Tasks', exact: true }).isVisible(), true)
        if (status !== 'pending_approval') assert.equal(await page.getByTestId('approve-task-btn').count(), 0)
      }
    }
    for (const variant of ['unknown', 'malformed']) {
      await page.evaluate(variant => (window as any).renderVariant(variant), variant)
      await page.waitForFunction(() => document.querySelector('[data-testid="toggle-task-details-btn"]')?.getAttribute('aria-expanded') === 'false')
      await page.getByTestId('toggle-task-details-btn').click()
      const full = page.getByRole('region', { name: 'Full current plan', exact: true })
      await full.getByRole('status').waitFor()
      assert.match(await full.innerText(), /Readable plan details are unavailable/)
      assert.doesNotMatch(await full.innerText(), /PRIVATE_|"checkpoints"|Flush then rename/)
      assert.equal(await page.getByTestId('approve-task-btn').isDisabled(), true)
    }
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
