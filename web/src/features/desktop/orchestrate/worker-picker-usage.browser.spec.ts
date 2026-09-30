// Purpose: WorkerModelPicker and WorkerBudget must stage worker-only options and
// user CAS writes, never write on load, reset account settings or invent usage.
// Threat: reselect resets policy, keyboard trap, narrow overflow, implicit budget
// mutation. Rendered browser interactions are the narrowest proof of those UI
// boundaries; fixture transport is not a benchmark or live qualification.
import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import { mkdir } from 'node:fs/promises'
test('compact chooser searches, retains saved options and budget saves exact CAS', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {ensureDesktopSession} from './src/app/api';
    import {desktopUsage} from './src/features/desktop/runtime/desktop-usage';
    import {WorkerModelPicker} from './src/features/desktop/orchestrate/worker-model-picker';
    import {WorkerBudget,ScopeUsage} from './src/features/desktop/orchestrate/scope-usage';
    window.profiles=[];
    function App(){const [profile,setProfile]=useState({source:'temporary',action:{provider:'fixture',model:'catalog-model',thinking:'high',service_tier:'priority',context_mode:''}});return <><WorkerModelPicker accountScopeId='acct' profile={profile} disabled={false} onChange={p=>{window.profiles.push(p);setProfile(p)}}/><WorkerBudget accountScopeId='acct' workerId='worker-fixture'/><div aria-label='Card usage'><ScopeUsage input={{accountScopeId:'acct',scope:{kind:'worker',id:'worker-fixture'}}}/></div><div aria-label='Detail usage'><ScopeUsage input={{accountScopeId:'acct',scope:{kind:'worker',id:'worker-fixture'}}}/></div></>}
    const root=createRoot(document.getElementById('root'));let key=0;
    window.remount=()=>root.render(<App key={++key}/>);
    window.switchAccount=()=>ensureDesktopSession(true).then(window.remount);
    window.replaceUsage=usage=>desktopUsage.acceptFrame({kind:'usage.scope.updated',event:{payload:{scope_totals:[usage]}}});
    ensureDesktopSession().then(window.remount);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  assert.ok(css.includes('border-box'), 'real Tailwind product CSS must be compiled')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 360, height: 800 } })
    const writes: Array<{ url: string; body: any }> = []
    const assignment = { provider: 'fixture', model: 'catalog-model', thinking: 'low' }
    const usage = { kind: 'worker', id: 'worker-fixture', revision: 1, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, media_receipts: 0, history_complete: false, receipt_count: 1, total_tokens: 9, coverage: 'observed_receipts_only', unknown_receipts: 0, input_tokens: 9, output_tokens: 0, catalog_cost_usd: 1, provider_cost_usd: 0, provider_estimate_cost_usd: 0, nominal_subscription_cost_usd: 0, media_cost_usd: 0, free_receipts: 0, subscription_receipts: 0 }
    let revision = 4, catalog: 'error' | 'ready' | 'empty' = 'error', account = 'acct', blocked = false
    let holdCatalog = false, releaseCatalog: (() => void) | undefined
    let markCatalogRequested: (() => void) | undefined
    const catalogRequested = new Promise<void>(resolve => { markCatalogRequested = resolve })
    await page.route('**/*', async route => {
      const request = route.request(), url = request.url()
      const respond = (data: unknown) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(data) })
      if (request.method() !== 'GET') writes.push({ url, body: request.postDataJSON() })
      if (url.endsWith('/v1/auth/desktop/session')) return respond({ user_id: 'owner', account_scope_id: account })
      if (url.endsWith('/v1/agent-model-settings')) return respond({ agent_model_settings: { swarm: { action: assignment, plan: assignment }, system_agents: Object.fromEntries(['compact', 'finder', 'coder', 'designer', 'router'].map(name => [name, assignment])), updated_at: 1 } })
      if (url.endsWith('/v1/providers')) return respond({ providers: [{ id: 'fixture', ready: true, runnable: true }] })
      if (url.includes('/v1/models/favorites')) return respond({ records: [] })
      if (url.includes('/v1/models') && holdCatalog) await new Promise<void>(resolve => { releaseCatalog = resolve; markCatalogRequested?.() })
      if (url.includes('/v1/models') && catalog === 'error') return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Fixture catalog unavailable' }) })
      if (url.includes('/v1/models') && catalog === 'empty') return respond({ records: [] })
      if (url.includes('/v1/models')) return respond({ records: [{ model: 'catalog-model', thinking_options: ['low', 'high'], service_tiers: ['standard', 'priority'], pricing: { input_price_per_million_tokens: 1, output_price_per_million_tokens: 2 } }] })
      if (url.includes('/v3/usage/scope')) return respond({ usage, recorded: true })
      if (url.includes('/v3/usage/worker-budget')) {
        if (request.method() === 'PUT') { revision++; return respond({ account_scope_id: 'acct', worker_id: 'worker-fixture', revision }) }
        return respond({ account_scope_id: 'acct', worker_id: 'worker-fixture', revision, updated_at: 0, account_policy: { account_scope_id: 'acct', enabled: false, daily_cost_limit_usd: 0, updated_at: 0 }, account_usage: { account_scope_id: 'acct', date: '2026-01-01', total_cost_usd: 1, total_tokens: 9 }, daily_cost_limit_usd: 0, daily_tokens_limit: 0, date: '2026-01-01', usage, remaining_cost_usd: null, remaining_tokens: null, account_remaining_cost_usd: null, account_remaining_tokens: null, blocked, blocked_reason: blocked ? 'Fixture accounting review' : undefined, inflight: false, account_inflight: false, account_coverage: 'observed_receipts_only', limitations: 'Observed receipts only.' })
      }
      return route.fulfill({ contentType: 'text/html', body: '<meta name="viewport" content="width=device-width,initial-scale=1"><div id="root" class="min-w-0 p-3"></div>' })
    })
    await page.goto('https://worker.test/'); await page.addStyleTag({ content: css }); await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('region', { name: 'Worker daily budget' }).locator('summary').waitFor()
    await page.getByText(/details and caps/).click()
    await page.getByRole('button', { name: 'Edit worker caps' }).waitFor()
    assert.deepEqual(writes, [])
    await page.getByRole('button', { name: 'Retry', exact: true }).waitFor()
    catalog = 'ready'; await page.getByRole('button', { name: 'Retry', exact: true }).click()
    await page.getByRole('button', { name: 'Change Execution model' }).click()
    const search = page.getByRole('textbox', { name: 'Search Execution models' })
    await search.fill('absent'); await page.getByText('No matching models.').waitFor()
    await search.fill('fixture'); await page.getByText(/Catalog estimate:/).waitFor()
    await page.getByRole('button', { name: /fixture\/catalog-model/ }).click()
    assert.equal(await page.getByRole('button', { name: 'Change Execution model' }).evaluate(el => el === document.activeElement), true)
    const profiles = await page.evaluate(() => (window as any).profiles)
    assert.equal(profiles[0].action.thinking, 'high'); assert.equal(profiles[0].action.service_tier, 'priority')
    await page.getByRole('button', { name: 'Change Execution model' }).click()
    await search.press('ArrowDown')
    assert.equal(await page.getByRole('button', { name: /Account default · follows/ }).evaluate(el => el === document.activeElement), true)
    await page.keyboard.press('End')
    assert.equal(await page.getByRole('button', { name: /fixture\/catalog-model/ }).evaluate(el => el === document.activeElement), true)
    await page.keyboard.press('Home'); await page.keyboard.press('Escape')
    assert.equal(await page.getByRole('button', { name: 'Change Execution model' }).evaluate(el => el === document.activeElement), true)
    await page.getByRole('button', { name: 'Edit worker caps' }).click()
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    assert.deepEqual(writes, [])
    await page.getByRole('button', { name: 'Edit worker caps' }).click()
    revision = 5
    await page.getByRole('button', { name: 'Reload budget' }).click()
    await page.getByText('Policy changed or is stale. Cancel and reload before saving.').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Save caps' }).isDisabled(), true)
    assert.deepEqual(writes, [])
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.getByRole('button', { name: 'Edit worker caps' }).click()
    await page.getByRole('textbox', { name: 'Daily worker USD' }).fill('2.5')
    await page.getByRole('button', { name: 'Save caps' }).click()
    await page.getByRole('button', { name: 'Edit worker caps' }).waitFor()
    await page.evaluate(value => (window as any).replaceUsage(value), { ...usage, revision: 8, total_tokens: 77, input_tokens: 77, catalog_cost_usd: 0, unknown_receipts: 1 })
    for (const view of ['Card usage', 'Detail usage']) {
      await page.locator(`[aria-label="${view}"] summary`).filter({ hasText: /77 tokens.*Cost unknown.*history incomplete/ }).waitFor()
    }
    assert.equal(writes.length, 1); assert.ok(writes[0].url.includes('/v3/usage/worker-budget'))
    assert.deepEqual(writes[0].body, { expected_revision: 5, daily_cost_limit_usd: 2.5, daily_tokens_limit: 0 })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
    assert.equal(await page.getByRole('region', { name: 'Worker daily budget' }).evaluate(el => getComputedStyle(el).borderTopStyle), 'solid')
    blocked = true; await page.getByRole('button', { name: 'Reload budget' }).click()
    await page.getByText(/Blocked: Fixture accounting review/).first().waitFor()
    catalog = 'empty'; await page.evaluate(() => (window as any).remount())
    await page.getByRole('button', { name: 'Change Execution model' }).click()
    await page.getByText('No catalog models available.').waitFor()
    await page.getByRole('button', { name: /Account default · follows/ }).click()
    assert.equal(await page.getByRole('button', { name: 'Change Execution model' }).evaluate(el => el === document.activeElement), true)
    await page.getByRole('button', { name: 'Change Execution model' }).click()
    await page.getByRole('button', { name: 'Close chooser' }).click()
    assert.equal(await page.getByRole('button', { name: 'Change Execution model' }).evaluate(el => el === document.activeElement), true)
    if (process.env.SWARM_TEST_SCREENSHOT_DIR) {
      const directory = path.resolve(process.env.SWARM_TEST_SCREENSHOT_DIR)
      assert.ok(directory !== path.resolve('..') && !directory.startsWith(path.resolve('..') + path.sep), 'screenshots must be outside the repository')
      await mkdir(directory, { recursive: true })
      await page.screenshot({ path: path.join(directory, 'worker-picker-budget-narrow.png'), fullPage: true })
    }
    holdCatalog = true; catalog = 'ready'
    await page.evaluate(() => (window as any).remount())
    await page.getByText('Loading account models…').waitFor()
    await catalogRequested
    account = 'foreign'; await page.evaluate(() => (window as any).switchAccount())
    releaseCatalog?.()
    await page.getByText('Reconnect to this account to change models.').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Change Execution model' }).isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: 'Edit worker caps' }).count(), 0)
    assert.equal(writes.length, 1)
    assert.equal(await page.getByRole('textbox', { name: 'Search Execution models' }).count(), 0)
  } finally { await browser.close() }
})
