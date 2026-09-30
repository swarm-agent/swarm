// Purpose: WorkerModelPicker and WorkerBudget must stage worker-only options and
// user CAS writes, never write on load, reset account settings or invent usage.
// Threat: reselect resets policy, keyboard trap, narrow overflow, implicit budget
// mutation. Rendered browser interactions are the narrowest proof of those UI
// boundaries; fixture transport is not a benchmark or live qualification.
import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
test('compact chooser searches, retains saved options and budget saves exact CAS', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {ensureDesktopSession} from './src/app/api';
    import {WorkerModelPicker} from './src/features/desktop/orchestrate/worker-model-picker';
    import {WorkerBudget} from './src/features/desktop/orchestrate/scope-usage';
    window.profiles=[];
    function App(){const [profile,setProfile]=useState({source:'temporary',action:{provider:'fixture',model:'catalog-model',thinking:'high',service_tier:'priority',context_mode:''}});return <><WorkerModelPicker accountScopeId='acct' profile={profile} disabled={false} onChange={p=>{window.profiles.push(p);setProfile(p)}}/><WorkerBudget accountScopeId='acct' workerId='worker-fixture'/></>}
    ensureDesktopSession().then(()=>createRoot(document.getElementById('root')).render(<App/>));
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 360, height: 800 } })
    const writes: Array<{ url: string; body: any }> = []
    const assignment = { provider: 'fixture', model: 'catalog-model', thinking: 'low' }
    const usage = { kind: 'worker', id: 'worker-fixture', revision: 1, receipt_count: 1, total_tokens: 9, coverage: 'observed_receipts_only', unknown_receipts: 0, input_tokens: 9, output_tokens: 0, catalog_cost_usd: 1, provider_cost_usd: 0, provider_estimate_cost_usd: 0, nominal_subscription_cost_usd: 0, media_cost_usd: 0, free_receipts: 0, subscription_receipts: 0 }
    let revision = 4
    await page.route('**/*', route => {
      const request = route.request(), url = request.url()
      const respond = (data: unknown) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) })
      if (request.method() !== 'GET') writes.push({ url, body: request.postDataJSON() })
      if (url.endsWith('/v1/auth/desktop/session')) return respond({ user_id: 'owner', account_scope_id: 'acct' })
      if (url.endsWith('/v1/agent-model-settings')) return respond({ agent_model_settings: { swarm: { action: assignment, plan: assignment }, system_agents: Object.fromEntries(['compact', 'finder', 'coder', 'designer', 'router'].map(name => [name, assignment])), updated_at: 1 } })
      if (url.endsWith('/v1/providers')) return respond({ providers: [{ id: 'fixture', ready: true, runnable: true }] })
      if (url.includes('/v1/models/favorites')) return respond({ records: [] })
      if (url.includes('/v1/models')) return respond({ records: [{ model: 'catalog-model', thinking_options: ['low', 'high'], service_tiers: ['standard', 'priority'], pricing: { input_price_per_million_tokens: 1, output_price_per_million_tokens: 2 } }] })
      if (url.includes('/v3/usage/worker-budget')) {
        if (request.method() === 'PUT') { revision++; return respond({ account_scope_id: 'acct', worker_id: 'worker-fixture', revision }) }
        return respond({ account_scope_id: 'acct', worker_id: 'worker-fixture', revision, daily_cost_limit_usd: 0, daily_tokens_limit: 0, date: '2026-01-01', usage, remaining_cost_usd: null, remaining_tokens: null, account_remaining_cost_usd: null, account_remaining_tokens: null, blocked: false, inflight: false, account_inflight: false, account_coverage: 'observed_receipts_only', limitations: 'Observed receipts only.' })
      }
      return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
    })
    await page.goto('https://worker.test/'); await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: 'Edit worker caps' }).waitFor()
    assert.deepEqual(writes, [])
    await page.getByRole('button', { name: 'Change Execution model' }).click()
    const search = page.getByRole('textbox', { name: 'Search Execution models' })
    await search.fill('absent'); await page.getByText('No matching models.').waitFor()
    await search.fill('fixture'); await page.getByText(/Catalog estimate:/).waitFor()
    await page.getByRole('button', { name: /fixture\/catalog-model/ }).click()
    const profiles = await page.evaluate(() => (window as any).profiles)
    assert.equal(profiles[0].action.thinking, 'high'); assert.equal(profiles[0].action.service_tier, 'priority')
    await page.getByRole('button', { name: 'Change Execution model' }).click(); await search.press('Escape')
    assert.equal(await page.getByRole('button', { name: 'Change Execution model' }).evaluate(el => el === document.activeElement), true)
    await page.getByRole('button', { name: 'Edit worker caps' }).click()
    await page.getByRole('textbox', { name: 'Daily worker USD' }).fill('2.5')
    await page.getByRole('button', { name: 'Save caps' }).click()
    await page.getByRole('button', { name: 'Edit worker caps' }).waitFor()
    assert.equal(writes.length, 1); assert.ok(writes[0].url.includes('/v3/usage/worker-budget'))
    assert.deepEqual(writes[0].body, { expected_revision: 4, daily_cost_limit_usd: 2.5, daily_tokens_limit: 0 })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
  } finally { await browser.close() }
})
