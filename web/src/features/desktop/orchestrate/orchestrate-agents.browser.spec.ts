import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'

// Purpose: OrchestrateAgents must key drafts/status by compiled role, only claim
// success after a canonical PATCH, and never submit stale sibling assignments.
// Also prove OrchestrateAgents derives immutable Orchestrator-first ordering,
// visible CSS selection/focus, responsive bounds and absent-role fallback.
// Rendered editors + intercepted HTTP prove UI/request semantics; real store/API
// tests prove atomic persistence. No provider or model execution is simulated.
test('role switching preserves drafts and slow/rejected saves cannot overwrite another editor', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {OrchestrateAgents} from './src/features/desktop/orchestrate/orchestrate-agents';
    import {modelOptionsQueryOptions} from './src/features/queries/query-options';
    import {agentModelSettingsQueryKey} from './src/features/desktop/settings/swarm/queries/get-agent-model-settings';
    const a=(model)=>({provider:'test-provider',model,thinking:'high',serviceTier:'',contextMode:''});
    const roles=[{id:'swarm',label:'Swarm',group:'swarm',slot:'action'},{id:'system-orchestrator',label:'Swarm Orchestrator',group:'swarm',slot:'plan'},{id:'system-coder',label:'Coder',group:'system_agents',slot:'coder'}];
    let saved={roles,swarm:{action:a('one'),plan:a('one')},systemAgents:Object.fromEntries(['compact','finder','coder','designer','router'].map(x=>[x,a('one')])),updatedAt:1};
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});window.client=client;
    client.setQueryData(agentModelSettingsQueryKey,saved);
    client.setQueryData(modelOptionsQueryOptions().queryKey,['one','two'].map(model=>({provider:'test-provider',model,label:model,thinkingOptions:['high','low'],serviceTiers:[],serviceTierMappings:[],contextMode:'',contextModes:[]})));
    window.requests=[];window.reject=false;window.finish=null;
    window.fetch=async(input,init)=>{
      if(init?.method!=='PATCH')throw Error('Unexpected read');
      const body=JSON.parse(init.body);window.requests.push(body);
      await new Promise(resolve=>window.finish=resolve);
      if(window.reject){window.reject=false;return new Response(JSON.stringify({error:'Rejected assignment'}),{status:400})}
      const group=body.swarm?'swarm':'system_agents';const wireGroup=group==='swarm'?saved.swarm:saved.systemAgents;
      for(const [slot,value] of Object.entries(body[group]))wireGroup[slot]={provider:value.provider,model:value.model,thinking:value.thinking,serviceTier:value.service_tier,contextMode:value.context_mode};
      saved.updatedAt++;
      const wire=a=>({provider:a.provider,model:a.model,thinking:a.thinking,service_tier:a.serviceTier,context_mode:a.contextMode});
      return new Response(JSON.stringify({roles,agent_model_settings:{swarm:{action:wire(saved.swarm.action),plan:wire(saved.swarm.plan)},system_agents:Object.fromEntries(Object.entries(saved.systemAgents).map(([k,v])=>[k,wire(v)])),updated_at:saved.updatedAt}}),{status:200});
    };
    const root=createRoot(document.getElementById('root'));
    window.mount=()=>root.render(<QueryClientProvider client={client}><div className="swarm-section"><OrchestrateAgents/></div></QueryClientProvider>);
    window.unmount=()=>root.render(null);window.mount();`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://agents.test/')
    await page.addStyleTag({ content: await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addStyleTag({ content: ':root { --swarm-surface:#101820; --swarm-surface-hover:#203040; --swarm-surface-subtle:#182028; --swarm-border-muted:#405060; --swarm-border-accent:#80c0e0; --swarm-accent:#80c0e0; --swarm-text:#ffffff; --swarm-text-muted:#b0c0d0; } .swarm-section { height:auto; }' })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    try {
      await page.getByRole('navigation', { name: 'System roles' }).waitFor()
    } catch (error) {
      assert.fail(`Agents fixture render/query readiness failed: ${String(error)}; page errors: ${JSON.stringify(errors)}; rendered text: ${(await page.locator('#root').textContent())?.slice(0, 2000)}`)
    }
    assert.deepEqual(errors, [], 'role/editor render has no page errors')
    assert.equal(await page.getByText('Loading assignments and supported models…').count(), 0)
    assert.equal(await page.getByRole('alert').count(), 0, 'seeded query contract is ready')
    const roleButtons = page.getByRole('navigation', { name: 'System roles' }).getByRole('button')
    assert.deepEqual(await roleButtons.locator('strong').allTextContents(), ['Swarm Orchestrator', 'Swarm', 'Coder'])
    assert.deepEqual(await page.evaluate(() => (window as any).client.getQueryData(['agent-model-settings']).roles.map((role: any) => role.id)), ['swarm', 'system-orchestrator', 'system-coder'], 'cache order is unchanged')
    assert.equal(await roleButtons.first().getAttribute('aria-pressed'), 'true')
    assert.equal(await page.getByRole('button', { name: 'Save Swarm Orchestrator', exact: true }).count(), 1)
    const selectedBackground = await roleButtons.first().evaluate(el => getComputedStyle(el).backgroundColor)
    assert.notEqual(selectedBackground, await roleButtons.nth(1).evaluate(el => getComputedStyle(el).backgroundColor))
    await page.keyboard.press('Tab')
    assert.equal(await roleButtons.first().evaluate(el => el.matches(':focus-visible') && getComputedStyle(el).outlineStyle === 'solid'), true)
    for (const width of [390, 1280]) {
      await page.setViewportSize({ width, height: 844 })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, 'layout stays within viewport')
      assert.equal(await page.locator('.swarm-agents-layout').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length), width === 390 ? 1 : 2)
    }
    const thinking = page.getByRole('combobox', { name: /^Thinking/ })
    assert.equal(await thinking.isEnabled(), true, 'supported assigned model is editable')
    assert.deepEqual(await thinking.locator('option').allTextContents(), ['Choose thinking', 'high', 'low'])
    await page.getByRole('button', { name: /^Coder/ }).click()
    await page.mouse.move(0, 0)
    assert.equal(await page.locator('.swarm-agent-role[aria-pressed="true"]').count(), 1)
    assert.equal(await page.getByRole('button', { name: /^Coder/ }).evaluate(el => getComputedStyle(el).backgroundColor), selectedBackground)
    assert.notEqual(await roleButtons.first().evaluate(el => getComputedStyle(el).backgroundColor), selectedBackground)
    await page.getByRole('combobox', { name: /^Thinking/ }).selectOption('low')
    await page.getByRole('button', { name: /^Swarm Orchestrator/ }).click()
    await page.getByRole('combobox', { name: /^Thinking/ }).selectOption('low')
    await page.getByRole('button', { name: 'Save Swarm Orchestrator', exact: true }).click()
    await page.waitForFunction(() => !!(window as any).finish)
    assert.equal(await page.getByText(/^Saved\./).count(), 0)
    await page.getByRole('button', { name: /^Coder/ }).click()
    assert.equal(await page.getByRole('combobox', { name: /^Thinking/ }).inputValue(), 'low')
    assert.equal(await page.getByRole('combobox', { name: /^Thinking/ }).isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: 'Save Coder', exact: true }).isDisabled(), true)
    assert.equal(await page.evaluate(() => (window as any).requests.length), 1)
    await page.evaluate(() => (window as any).finish())
    await page.waitForFunction(() => !(document.querySelector('button[disabled]')))
    assert.equal(await page.getByRole('combobox', { name: /^Thinking/ }).inputValue(), 'low')
    assert.equal(await page.getByText(/^Saved\./).count(), 0)
    const request = (await page.evaluate(() => (window as any).requests))[0]
    assert.deepEqual(Object.keys(request), ['swarm'])
    assert.deepEqual(Object.keys(request.swarm), ['plan'])
    assert.equal(request.swarm.plan.thinking, 'low')
    await page.evaluate(() => { (window as any).reject = true; (window as any).finish = null })
    await page.getByRole('button', { name: 'Save Coder', exact: true }).click()
    await page.waitForFunction(() => !!(window as any).finish)
    await page.evaluate(() => (window as any).finish())
    await page.getByRole('status').getByText('Rejected assignment').waitFor()
    assert.equal(await page.getByRole('combobox', { name: /^Thinking/ }).inputValue(), 'low')
    const rejectedRequest = (await page.evaluate(() => (window as any).requests))[1]
    assert.deepEqual(Object.keys(rejectedRequest), ['system_agents'])
    assert.deepEqual(Object.keys(rejectedRequest.system_agents), ['coder'])
    await page.evaluate(() => (window as any).unmount())
    await page.getByText('Agents', { exact: true }).waitFor({ state: 'detached' })
    await page.evaluate(() => (window as any).mount())
    await page.getByRole('button', { name: /^Swarm Orchestrator/ }).click()
    assert.equal(await page.getByRole('combobox', { name: /^Thinking/ }).inputValue(), 'low')
    await page.getByRole('button', { name: /^Coder/ }).click()
    assert.equal(await page.getByRole('combobox', { name: /^Thinking/ }).inputValue(), 'high')
    await page.evaluate(() => (window as any).unmount())
    await page.getByRole('heading', { name: 'Agents', exact: true }).waitFor({ state: 'detached' })
    await page.evaluate(() => { const client = (window as any).client; const current = client.getQueryData(['agent-model-settings']); client.setQueryData(['agent-model-settings'], { ...current, roles: current.roles.filter((role: any) => role.id !== 'system-orchestrator').reverse() }); (window as any).mount() })
    await page.getByRole('button', { name: 'Save Coder', exact: true }).waitFor()
    assert.deepEqual(await roleButtons.locator('strong').allTextContents(), ['Coder', 'Swarm'], 'without Orchestrator, server order and first-role default are preserved')
    assert.equal(await roleButtons.first().getAttribute('aria-pressed'), 'true')
    await page.evaluate(() => { const client = (window as any).client; const current = client.getQueryData(['agent-model-settings']); client.setQueryData(['agent-model-settings'], { ...current, roles: undefined }) })
    await page.getByRole('alert').getByText(/System roles are unconfigured/).waitFor()
    await page.getByRole('button', { name: 'Retry', exact: true }).click()
    await page.getByRole('alert').getByText(/Unexpected read/).waitFor()
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
