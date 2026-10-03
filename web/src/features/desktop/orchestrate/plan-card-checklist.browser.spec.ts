import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: MinimalTaskCard exposes current bound outcomes while collapsed,
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
    await checklist.getByText(document.requirements[0].text, { exact: true }).waitFor()
    await details.click(); await full.waitFor()
    assert.match(await full.innerText(), /Technical renderer rewrite/)
    await details.click()
    await page.evaluate(() => (window as any).renderTask('plain'))
    await checklist.waitFor({ state: 'detached' })
    assert.equal(await full.count(), 0)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
