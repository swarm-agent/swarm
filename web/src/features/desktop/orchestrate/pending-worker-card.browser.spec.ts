import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: concise consent summary is visible before acceptance; full instructions
// are keyboard-expandable without mutations. Acceptance uses the actual revision.
// Threat: hidden edits get approved or a failed accept is displayed as success.
// Boundary: rendered PendingWorkerCard -> canonical desktopWorkers mutation. This
// component fixture tests interactions, not live daemon/provider execution.
test('pending worker expansion, exact acceptance, conflict and revision review', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {PendingWorkerCard} from './src/features/desktop/orchestrate/pending-worker-card';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    let worker={id:'worker_review',account_scope_id:'account',name:'Repository Reviewer',description:'Review only when asked.',instructions:'Inspect repository changes and report findings. Do not modify files.',revision:1,lifecycle_state:'pending',created_at:1,updated_at:1,proposed_bindings:{primary:'workspace_demo'},workspace_requirements:[{role:'primary',required:true}]};
    window.calls=[]; window.stale=false; window.succeed=false;
    desktopWorkers.mutate=async input=>{window.calls.push(input);if(window.succeed)return {worker:{...worker,lifecycle_state:'active'}};throw new Error('Worker revision conflict; review again')};
    const root=createRoot(document.getElementById('root'));
    const catalog={accountScopeId:'account',workspaces:[{workspaceId:'workspace_demo',path:'/projects/demo',workspaceName:'Demo repository'}]};
    const render=()=>root.render(<PendingWorkerCard key={worker.revision} worker={worker} accountScopeId='account' workspaceSlug='demo' workspaceCatalog={catalog} stale={window.stale} onAccepted={accepted=>{window.accepted=accepted}}/>);
    window.setStale=()=>{window.stale=true;render()}; window.clearStale=()=>{window.stale=false;render()};
    window.revise=()=>{worker={...worker,revision:2,instructions:'Changed instructions require review',automations:[{id:'job_review',worker_id:worker.id,name:'Daily review',description:'Check changes',activation_mode:'cron',enabled:true,schedule:{kind:'cron',cron:'0 9 * * *',timezone:'UTC'},plan_document:{title:'Review plan',checkpoints:[{id:'cp-1',title:'Inspect',tasks:['Full task instructions'],acceptance_criteria:['All changes reviewed']}]}}]};render()};render();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 900, height: 1000 } })
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<style>body{background:#101827;color:#e2e8f0;font:14px system-ui;margin:24px}svg{width:16px;height:16px}button,a{margin:8px}article{max-width:780px}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><div id="root"></div>' }))
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByTestId('pending-worker-card').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Accept worker', exact: true }).count(), 1)
    await page.getByText('Demo repository', { exact: false }).waitFor()
    await page.getByText('Review only when asked.', { exact: true }).waitFor()
    assert.equal(await page.getByText('Inspect repository changes and report findings. Do not modify files.', { exact: true }).count(), 0)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await page.getByRole('button', { name: 'View instructions and job plan', exact: true }).click()
    await page.getByText('No job attached; waits for a task after acceptance', { exact: true }).waitFor()
    assert.equal(await page.getByRole('link', { name: 'Open worker detail' }).getAttribute('href'), '/demo/workers/worker_review')
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await page.evaluate(() => (window as any).setStale())
    await page.getByRole('alert').getByText(/blocked on stale revisions/).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Accept worker', exact: true }).isDisabled(), true)
    await page.evaluate(() => (window as any).clearStale())
    await page.getByRole('button', { name: 'Accept worker', exact: true }).click()
    await page.getByRole('alert').getByText('Worker revision conflict; review again').waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [{ action: 'accept', workerId: 'worker_review', expected_revision: 1 }])
    await page.evaluate(() => (window as any).revise())
    await page.getByRole('button', { name: 'View instructions and job plan', exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Accept worker', exact: true }).count(), 1)
    await page.getByRole('button', { name: 'View instructions and job plan', exact: true }).click()
    await page.getByText('Changed instructions require review', { exact: true }).waitFor()
    await page.getByText(/Recurring · daily at 09:00 \(UTC\)/).first().waitFor()
    assert.equal(await page.getByText('Full task instructions', { exact: true }).isVisible(), false)
    await page.getByText('Daily review · View full job plan', { exact: true }).click()
    await page.getByText('Full task instructions', { exact: true }).waitFor({ state: 'visible' })
    await page.getByText('All changes reviewed', { exact: true }).waitFor({ state: 'visible' })
    assert.equal(await page.getByRole('button', { name: 'View instructions and job plan', exact: true }).count(), 0)
    await page.getByRole('button', { name: 'Hide instructions and job plan', exact: true }).focus()
    await page.keyboard.press('Enter')
    assert.equal(await page.getByTestId('pending-worker-expanded').count(), 0)
    assert.equal((await page.evaluate(() => (window as any).calls)).length, 1)
    await page.evaluate(() => { (window as any).succeed = true })
    await page.getByRole('button', { name: 'Accept worker', exact: true }).click()
    await page.waitForFunction(() => (window as any).accepted?.lifecycle_state === 'active')
    assert.deepEqual((await page.evaluate(() => (window as any).calls))[1], { action: 'accept', workerId: 'worker_review', expected_revision: 2 })
    if (process.env.SWARM_PENDING_CARD_SCREENSHOT) await page.screenshot({ path: process.env.SWARM_PENDING_CARD_SCREENSHOT, fullPage: true })
  } finally { await browser.close() }
})
