import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: compact Execution and disclosed Planning slots show canonical account defaults after
// slot-local reset, and settings edits stage revision-guarded proposals only.
// Threat: cosmetic default labels over pinned models, account writes or implicit
// acceptance. WorkerSettingsReview/WorkerModelPicker rendered with read API fixtures
// are the narrowest interactive layer; Go tests own admission/review persistence.
test('worker reset shows actual defaults and only proposes a pending settings revision', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {ensureDesktopSession} from './src/app/api';
    import {WorkerSettingsReview} from './src/features/desktop/orchestrate/worker-settings-review';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    window.mutations=[];
    desktopWorkers.mutate=async input=>{window.mutations.push(input);return {}};
    const worker={id:'worker_fixture',account_scope_id:'acct',name:'Fixture',instructions:'Review',revision:7,lifecycle_state:'active',created_at:1,updated_at:1,model_profile:{source:'temporary',action:{provider:'fixture',model:'action-override'},plan:{provider:'fixture',model:'plan-override'}}};
    ensureDesktopSession().then(()=>createRoot(document.getElementById('root')).render(<WorkerSettingsReview worker={worker} accountScopeId='acct' disabled={false}/>));
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const writes: string[] = []
    const assignment = { provider: 'fixture', model: 'account-action', thinking: 'high' }
    await page.route('**/*', route => {
      const request = route.request()
      if (request.method() !== 'GET') writes.push(request.url())
      if (request.url().endsWith('/v1/auth/desktop/session')) return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ user_id: 'owner', account_scope_id: 'acct' }) })
      if (request.url().endsWith('/v1/agent-model-settings')) return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ agent_model_settings: { swarm: { action: assignment, plan: { ...assignment, model: 'account-plan' } }, system_agents: Object.fromEntries(['compact', 'finder', 'coder', 'designer', 'router'].map(name => [name, assignment])), updated_at: 1 } }) })
      if (request.url().includes('/v1/')) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Catalog unavailable in fixture' }) })
      return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
    })
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const action = page.getByRole('region', { name: 'Execution model', exact: true })
    const plan = page.getByRole('region', { name: 'Planning model', exact: true })
    await action.getByText(/action-override/).waitFor()
    await page.locator('summary').filter({ hasText: 'Planning model ·' }).click()
    await plan.getByText(/plan-override/).waitFor()
    await action.getByRole('button', { name: 'Change Execution model' }).click()
    await action.getByRole('button', { name: /Account default ·/ }).click()
    await action.getByText(/account-action/).waitFor()
    await action.getByText('Thinking · high').waitFor()
    await plan.getByText(/plan-override/).waitFor()
    await plan.getByRole('button', { name: 'Change Planning model' }).click()
    await plan.getByRole('button', { name: /Account default ·/ }).click()
    await plan.getByText(/account-plan/).waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).mutations), [])
    await page.getByRole('group', { name: 'Execution mode', exact: true }).getByRole('button', { name: 'Plan', exact: true }).click()
    await page.getByRole('button', { name: 'Propose changes' }).click()
    const mutations = await page.evaluate(() => (window as any).mutations)
    assert.equal(mutations.length, 1)
    assert.equal(mutations[0].action, 'update')
    assert.equal(mutations[0].expected_revision, 7)
    assert.equal(mutations[0].changes.execution_mode, 'plan')
    assert.equal(mutations[0].changes.model_profile.action_use_account_default, true)
    assert.equal(mutations[0].changes.model_profile.plan_use_account_default, true)
    assert.deepEqual(writes, [])
    assert.equal(await page.getByRole('button', { name: /Accept|Activate/ }).count(), 0)
  } finally { await browser.close() }
})

// Requirement: the actual PendingWorkerCard entry point uses the shared dashboard,
// never accepts unsaved edits and accepts the server candidate by revision only.
// Threat: hidden/local model copies replace a saved candidate, or refresh makes
// acceptance appear approved before persistence. This component interaction is
// the narrowest UI proof; Go tests independently prove the durable boundary.
test('pending dashboard proposes before accepting and reflects approved candidate', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {ensureDesktopSession} from './src/app/api';
    import {PendingWorkerCard} from './src/features/desktop/orchestrate/pending-worker-card';
    import {WorkerSettingsReview} from './src/features/desktop/orchestrate/worker-settings-review';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    window.mutations=[];
    const oldProfile={source:'temporary',action:{provider:'fixture',model:'old-action',thinking:'high'},plan:{provider:'fixture',model:'plan-model',thinking:'low'}};
    const base={id:'review-fixture',account_scope_id:'acct',name:'Review',instructions:'Review',revision:4,lifecycle_state:'paused',created_at:1,updated_at:1,execution_mode:'auto',model_profile:oldProfile};
    const candidate={...base,lifecycle_state:'pending',model_profile:{...oldProfile,action:{provider:'fixture',model:'candidate-action',thinking:'medium',service_tier:'standard',context_mode:'extended'}}};
    function App(){const [worker,setWorker]=useState({...base,pending_review:candidate});
      desktopWorkers.mutate=async input=>{window.mutations.push(input);
        const result=input.action==='update'?{...worker,revision:worker.revision+1,pending_review:{...worker.pending_review,...input.changes}}:{...worker.pending_review,id:worker.id,revision:worker.revision+1,lifecycle_state:'paused',pending_review:undefined};
        setWorker(result);return {worker:result};};
      return worker.pending_review?<PendingWorkerCard worker={worker} accountScopeId='acct' workspaceCatalog={{accountScopeId:'acct',workspaces:[]}}/>:<WorkerSettingsReview worker={worker} accountScopeId='acct' disabled={false}/>;}
    ensureDesktopSession().then(()=>createRoot(document.getElementById('root')).render(<App/>));
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => {
      if (route.request().url().endsWith('/v1/auth/desktop/session')) return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ user_id: 'owner', account_scope_id: 'acct' }) })
      if (route.request().url().includes('/v1/')) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Unavailable fixture catalog' }) })
      return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
    })
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const action = page.getByRole('region', { name: 'Execution model', exact: true })
    await action.getByText(/candidate-action/).waitFor()
    await action.getByText(/Thinking · medium/).waitFor()
    await page.getByRole('group', { name: 'Execution mode', exact: true }).getByRole('button', { name: 'Plan', exact: true }).click()
    await page.waitForFunction(() => (document.querySelector('[data-testid="accept-pending-worker"]') as HTMLButtonElement)?.disabled)
    assert.deepEqual(await page.evaluate(() => (window as any).mutations), [])
    await page.getByRole('button', { name: 'Propose changes' }).click()
    await page.waitForFunction(() => !(document.querySelector('[data-testid="accept-pending-worker"]') as HTMLButtonElement)?.disabled)
    await page.getByRole('button', { name: 'Accept changes', exact: true }).click()
    await page.getByText('Approved', { exact: true }).waitFor()
    await action.getByText(/candidate-action/).waitFor()
    const mutations = await page.evaluate(() => (window as any).mutations)
    assert.equal(mutations.length, 2)
    assert.equal(mutations[0].action, 'update')
    assert.equal(mutations[0].expected_revision, 4)
    assert.deepEqual(mutations[0].changes.model_profile.action, { provider: 'fixture', model: 'candidate-action', thinking: 'medium', service_tier: 'standard', context_mode: 'extended' })
    assert.deepEqual(mutations[1], { action: 'accept', workerId: 'review-fixture', expected_revision: 5 })
    assert.equal(await page.getByRole('button', { name: 'Accept changes', exact: true }).count(), 0)
  } finally { await browser.close() }
})
