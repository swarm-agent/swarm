// Purpose: WorkerModelPicker and WorkerBudget must stage worker-only options and
// user CAS writes, never write on load, reset account settings or invent usage.
// Threat: reselect resets policy, keyboard trap, narrow overflow, implicit budget
// mutation. WorkerBudgetEditor must expose limits; TaskUsageFooter must keep the
// recorded task token split on the same row at narrow/wide widths, never a session
// total. These boundaries reject above-ceiling writes, retain tokens on dollar edits,
// show only structured holds as stopped, and preserve drafts on failed CAS.
// Rendered browser interactions are the narrowest proof of those UI
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
    import {TaskUsageFooter} from './src/features/desktop/orchestrate/task-usage-metadata';
    import {WorkerModelPicker} from './src/features/desktop/orchestrate/worker-model-picker';
    import {WorkerBudget,InlineUsage} from './src/features/desktop/orchestrate/scope-usage';
    window.profiles=[];
    function App(){const [profile,setProfile]=useState({source:'temporary',action:{provider:'fixture',model:'catalog-model',thinking:'high',service_tier:'priority',context_mode:''}});return <><WorkerModelPicker accountScopeId='acct' profile={profile} disabled={false} onChange={p=>{window.profiles.push(p);setProfile(p)}}/><WorkerBudget accountScopeId='acct' workerId='worker-fixture'/><TaskUsageFooter task={{id:'task-fixture',worker_id:'worker-fixture',sessionId:'unrelated-session'}} projectId='project-fixture'><button className='shrink-0' onClick={()=>{window.detailsClicks=(window.detailsClicks||0)+1}}>Show details</button></TaskUsageFooter><div aria-label='Detail usage'><InlineUsage input={{accountScopeId:'acct',scope:{kind:'worker',id:'worker-fixture'}}}/></div></>}
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
    page.setDefaultTimeout(4000)
    page.on('pageerror', error => console.error('UI page error:', error.message))
    const capture = async (name: string) => {
      if (!process.env.SWARM_TEST_SCREENSHOT_DIR) return
      const directory = path.resolve(process.env.SWARM_TEST_SCREENSHOT_DIR)
      assert.ok(directory !== path.resolve('..') && !directory.startsWith(path.resolve('..') + path.sep), 'screenshots must be outside the repository')
      await mkdir(directory, { recursive: true })
      await page.screenshot({ path: path.join(directory, name + '.png'), fullPage: true })
    }
    const writes: Array<{ url: string; body: any }> = []
    const assignment = { provider: 'fixture', model: 'catalog-model', thinking: 'low' }
    const usage = { kind: 'worker', id: 'worker-fixture', revision: 1, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, media_receipts: 0, history_complete: false, receipt_count: 1, total_tokens: 9, coverage: 'observed_receipts_only', unknown_receipts: 0, input_tokens: 9, output_tokens: 0, catalog_cost_usd: 1, provider_cost_usd: 0, provider_estimate_cost_usd: 0, nominal_subscription_cost_usd: 0, media_cost_usd: 0, free_receipts: 0, subscription_receipts: 0 }
    let savedDollars = 0, savedTokens = 900, overallLimit = 3, held = false, failSave = false, overallEnabled = true, noReceipts = false, accountUnknown = false
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
      if (url.includes('/v1/model/catalog') && holdCatalog) await new Promise<void>(resolve => { releaseCatalog = resolve; markCatalogRequested?.() })
      if (url.includes('/v1/model/catalog') && catalog === 'error') return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Fixture catalog unavailable' }) })
      if (url.includes('/v1/model/catalog') && catalog === 'empty') return respond({ records: [] })
      if (url.includes('/v1/model/catalog')) return respond({ records: [{ model: 'catalog-model', thinking_options: ['low', 'high'], service_tiers: ['standard', 'priority'], pricing: { input_price_per_million_tokens: 1, output_price_per_million_tokens: 2 } }] })
      if (url.includes('/v3/usage/scope')) { const task = new URL(url).searchParams.get('kind') === 'task'; return respond({ usage: task ? { ...usage, kind: 'task', id: 'task-fixture', project_id: 'project-fixture' } : usage, recorded: true }) }
      if (url.includes('/v3/usage/worker-budget')) {
        if (request.method() === 'PUT' && failSave) return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'Fixture revision conflict' }) })
        if (request.method() === 'PUT') { revision++; savedDollars = request.postDataJSON().daily_cost_limit_usd; savedTokens = request.postDataJSON().daily_tokens_limit; return respond({ account_scope_id: 'acct', worker_id: 'worker-fixture', revision }) }
        return respond({ account_scope_id: 'acct', worker_id: 'worker-fixture', revision, updated_at: 0, account_policy: { account_scope_id: 'acct', enabled: overallEnabled, daily_cost_limit_usd: overallLimit, updated_at: 0 }, account_usage: { account_scope_id: 'acct', date: '2026-01-01', total_cost_usd: accountUnknown ? 0 : 1, unknown_receipts: accountUnknown ? 1 : 0, total_tokens: 9 }, daily_cost_limit_usd: savedDollars, daily_tokens_limit: savedTokens, date: '2026-01-01', usage: noReceipts ? { ...usage, revision: 0, total_tokens: 0, input_tokens: 0, receipt_count: 0, catalog_cost_usd: 0, coverage: 'no_records' } : usage, remaining_cost_usd: savedDollars ? Math.max(0, savedDollars - 1) : null, remaining_tokens: Math.max(0, savedTokens - 9), account_remaining_cost_usd: overallEnabled && overallLimit ? Math.max(0, overallLimit - 1) : null, account_remaining_tokens: null, blocked: blocked || held, blocked_reason: held ? 'daily hold' : blocked ? 'Fixture accounting review' : undefined, reset_at: Date.parse('2026-01-02T00:00:00Z'), hold: held ? { date: '2026-01-01', reason: 'daily_budget_exhausted', cap_source: 'worker', dimension: 'usd', limit: 2.5, usage: 2.5, reset_at: Date.parse('2026-01-02T00:00:00Z') } : undefined, inflight: false, account_inflight: false, account_coverage: 'observed_receipts_only', limitations: 'Observed receipts only.' })
      }
      return route.fulfill({ contentType: 'text/html', body: '<meta name="viewport" content="width=device-width,initial-scale=1"><div id="root" class="min-w-0 p-3"></div>' })
    })
    await page.goto('https://worker.test/'); await page.addStyleTag({ content: css }); await page.addScriptTag({ content: bundle.outputFiles[0].text })
    console.log('UI step: budget hydration')
    const budgetBox = page.getByRole('region', { name: 'Worker daily budget' })
    await page.getByRole('radio', { name: 'Set worker limit' }).waitFor()
    assert.equal(await budgetBox.locator('summary').count(), 0)
    await page.getByRole('radio', { name: 'Use overall limit' }).focus()
    await page.keyboard.press('ArrowRight')
    assert.equal(await page.getByRole('radio', { name: 'Set worker limit' }).isChecked(), true)
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    assert.equal(await page.getByRole('link', { name: 'Change overall limit in Usage' }).getAttribute('href'), '/usage')
    await page.getByText('Overall ceiling:', { exact: false }).waitFor()
    assert.deepEqual(writes, [])
    await page.getByRole('button', { name: 'Retry', exact: true }).waitFor()
    await capture('worker-catalog-error-narrow')
    console.log('UI step: catalog retry')
    catalog = 'ready'; await page.getByRole('button', { name: 'Retry', exact: true }).click()
    await page.getByRole('button', { name: 'Change Execution model' }).click()
    const search = page.getByRole('textbox', { name: 'Search Execution models' })
    await search.fill('absent'); await page.getByText('No matching models.').waitFor()
    await search.fill('fixture'); await page.getByText(/Catalog estimate:/).waitFor()
    await capture('worker-chooser-narrow')
    await page.setViewportSize({ width: 1100, height: 800 })
    await capture('worker-chooser-wide')
    await page.setViewportSize({ width: 360, height: 800 })
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
    await page.getByRole('radio', { name: 'Set worker limit' }).check()
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    assert.deepEqual(writes, [])
    console.log('UI step: stale budget')
    await page.getByRole('radio', { name: 'Set worker limit' }).check()
    revision = 5
    await page.getByRole('button', { name: 'Reload budget' }).click()
    await page.getByText('Policy changed or is stale. Cancel and reload before saving.').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Save limit' }).isDisabled(), true)
    assert.deepEqual(writes, [])
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.getByRole('radio', { name: 'Set worker limit' }).check()
    await page.getByRole('textbox', { name: 'Daily worker USD' }).fill('4')
    await page.getByRole('button', { name: 'Save limit' }).click()
    await page.getByText('Worker limit cannot exceed the overall $3/day ceiling.').waitFor()
    assert.deepEqual(writes, [])
    await page.getByRole('textbox', { name: 'Daily worker USD' }).fill('2.5')
    await page.getByRole('button', { name: 'Save limit' }).click()
    await page.waitForFunction(() => !Array.from(document.querySelectorAll('button')).some(el => el.textContent === 'Cancel'))
    assert.equal(await page.getByRole('textbox', { name: 'Daily worker tokens' }).inputValue(), '900')
    await page.evaluate(value => (window as any).replaceUsage(value), { ...usage, revision: 8, total_tokens: 77, input_tokens: 77, catalog_cost_usd: 0, unknown_receipts: 1 })
    await page.evaluate(value => (window as any).replaceUsage(value), { ...usage, kind: 'task', id: 'task-fixture', project_id: 'project-fixture', revision: 8, total_tokens: 77, input_tokens: 77, catalog_cost_usd: 0, unknown_receipts: 1 })
    await page.locator('[aria-label="Detail usage"] [aria-label="Lifetime observed usage"]').filter({ hasText: /77 tokens.*Cost unknown.*history incomplete/ }).waitFor()
    await page.getByTestId('task-token-split').filter({ hasText: 'I 77' }).waitFor()
    await page.evaluate(value => (window as any).replaceUsage(value), { ...usage, kind: 'task', id: 'task-fixture', project_id: 'project-fixture', revision: 9, total_tokens: 999999, input_tokens: 12345, output_tokens: 678, cache_read_tokens: 9012, cache_write_tokens: 345, thinking_tokens: 67 })
    const split = page.getByTestId('task-token-split')
    await split.filter({ hasText: 'CR 9K' }).waitFor()
    assert.deepEqual(await split.locator(':scope > span').allTextContents(), ['I 12.3K', 'O 678', 'CR 9K', 'CW 345', 'T 67'])
    assert.match(await split.getAttribute('title') || '', /Input: 12,345; Output: 678; Cache read: 9,012; Cache write: 345; Thinking: 67 tokens/)
    assert.doesNotMatch(await split.innerText(), /999|Observed|cost|history/)
    assert.equal(await page.locator('[aria-label="Task usage metadata"] details, [aria-label="Task usage metadata"] button').count(), 0)
    await page.locator('[aria-label="Task usage metadata"]').click()
    assert.equal(await page.evaluate(() => (window as any).detailsClicks || 0), 0)
    await page.getByRole('button', { name: 'Show details', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).detailsClicks), 1)
    assert.doesNotMatch(await page.locator('[aria-label="Task usage metadata"]').innerText(), /Worker|limit/)
    assert.equal(writes.length, 1); assert.ok(writes[0].url.includes('/v3/usage/worker-budget'))
    assert.deepEqual(writes[0].body, { expected_revision: 5, daily_cost_limit_usd: 2.5, daily_tokens_limit: 900 })
    const assertFooterRow = async () => {
      const button = await page.getByRole('button', { name: 'Show details', exact: true }).boundingBox()
      const metadata = await page.locator('[aria-label="Task usage metadata"]').boundingBox()
      const footer = await page.getByTestId('task-usage-footer').boundingBox()
      assert.ok(button && metadata && footer)
      assert.ok(metadata.x >= button.x + button.width)
      assert.ok(Math.abs((button.y + button.height / 2) - (metadata.y + metadata.height / 2)) < 1, 'tokens and Show details share a row')
      assert.ok(Math.abs(metadata.x + metadata.width - footer.x - footer.width) < 1, 'tokens align at the right edge')
    }
    await assertFooterRow()
    await capture('worker-usage-unknown-narrow')
    await page.setViewportSize({ width: 1100, height: 800 })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
    const actionBounds = await page.getByRole('button', { name: 'Show details', exact: true }).boundingBox()
    const metaBounds = await page.locator('[aria-label="Task usage metadata"]').boundingBox()
    assert.ok(actionBounds && metaBounds && metaBounds.x > actionBounds.x)
    await assertFooterRow()
    await capture('worker-budget-inline-wide')
    await page.setViewportSize({ width: 360, height: 800 })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
    assert.equal(await page.getByRole('region', { name: 'Worker daily budget' }).evaluate(el => getComputedStyle(el).borderTopStyle), 'solid')
    blocked = true; await page.getByRole('button', { name: 'Reload budget' }).click()
    await page.getByText(/Blocked: Fixture accounting review/).first().waitFor()
    held = true; await page.getByRole('button', { name: 'Reload budget' }).click()
    await page.getByText(/Stopped for today · Worker dollar limit reached/).waitFor()
    await capture('worker-budget-stopped-narrow')
    accountUnknown = true; await page.getByRole('button', { name: 'Reload budget' }).click()
    await budgetBox.getByText('Overall today used · Cost unknown · partial pricing · 9 tokens', { exact: true }).waitFor()
    assert.equal(await budgetBox.getByText(/Overall today used.*\$0/).count(), 0)
    accountUnknown = false
    overallLimit = 2; await page.getByRole('button', { name: 'Reload budget' }).click()
    await page.getByText(/Saved worker limit exceeds the new overall ceiling/).waitFor()
    assert.equal(writes.length, 1)
    await page.getByRole('textbox', { name: 'Daily worker USD' }).fill('1.5')
    failSave = true; await page.getByRole('button', { name: 'Save limit' }).click()
    await page.getByRole('alert').filter({ hasText: /Fixture revision conflict/ }).waitFor()
    assert.equal(writes.length, 2)
    assert.equal(await page.getByRole('textbox', { name: 'Daily worker USD' }).inputValue(), '1.5')
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    held = false; blocked = false; overallEnabled = false; noReceipts = true
    await page.getByRole('button', { name: 'Reload budget' }).click()
    await budgetBox.getByText(/No recorded receipts · cost unknown/).waitFor()
    await page.getByRole('radio', { name: 'Use overall limit' }).check()
    await page.getByText(/No dollar safety limit/).waitFor()
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    overallEnabled = true; overallLimit = 0
    await page.getByRole('button', { name: 'Reload budget' }).click()
    await page.getByText('No dollar limit set', { exact: true }).waitFor()
    assert.equal(writes.length, 2)
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
    console.log('UI step: account switch')
    holdCatalog = true; catalog = 'ready'
    await page.evaluate(() => (window as any).remount())
    await page.getByText('Loading account models…').waitFor()
    await capture('worker-loading-narrow')
    await catalogRequested
    account = 'foreign'; await page.evaluate(() => (window as any).switchAccount())
    releaseCatalog?.()
    await page.getByText('Reconnect to this account to change models.').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Change Execution model' }).isDisabled(), true)
    assert.equal(await page.getByRole('radio', { name: 'Set worker limit' }).count(), 0)
    assert.equal(writes.length, 2)
    assert.equal(await page.getByRole('textbox', { name: 'Search Execution models' }).count(), 0)
    assert.equal(await page.getByText(/Thinking · high|Tier · priority/).count(), 0)
    assert.equal(await page.getByText(/catalog-model/).count(), 0)
    await capture('worker-account-switched-narrow')
  } finally { await browser.close() }
})
