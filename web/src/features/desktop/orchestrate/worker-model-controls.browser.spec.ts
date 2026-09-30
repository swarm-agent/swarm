import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: visible Action/Plan slots show canonical account defaults after
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
    const action = page.getByRole('region', { name: 'Action model', exact: true })
    const plan = page.getByRole('region', { name: 'Plan model', exact: true })
    await action.getByText('Explicit worker override: fixture/action-override', { exact: true }).waitFor()
    await plan.getByText('Explicit worker override: fixture/plan-override', { exact: true }).waitFor()
    await page.getByRole('button', { name: 'Use account default for Action' }).click()
    await action.getByText('Account default (follows future changes): fixture/account-action · high', { exact: true }).waitFor()
    await plan.getByText('Explicit worker override: fixture/plan-override', { exact: true }).waitFor()
    await page.getByRole('button', { name: 'Use account default for Plan' }).click()
    await plan.getByText('Account default (follows future changes): fixture/account-plan · high', { exact: true }).waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).mutations), [])
    await page.getByRole('combobox', { name: 'Execution mode' }).selectOption('plan')
    await page.getByRole('button', { name: 'Propose settings changes' }).click()
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
