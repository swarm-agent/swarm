import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: first-use discovery explains limitations and only emits editable
// draft intent; untrusted proposal data must not crash the readable summary.
// Authorities: AutomationOverview, parseAutomationProposal, AutomationProposalSummary.
// This hermetic component layer proves callback and rendering contracts, not API
// approval, live conversation submission, responsive styling or visual quality.
test('starter navigation is keyboard accessible and summaries reject malformed plans', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {AutomationOverview} from './src/features/desktop/tools/automations/automation-overview';
    import {AutomationProposalSummary} from './src/features/desktop/tools/automations/automation-summary';
    import {parseAutomationProposal} from './src/features/desktop/tools/automations/automation-proposal';
    window.drafts=[]; window.updates=0;
    const definition={name:'Review',enabled:false,plans:[{id:'review',plan:{session_id:'session',plan_id:'plan',revision:2}}],schedule:{kind:'manual',timezone:'UTC',missed_policy:'skip',overlap_policy:'serialize'},authorization:{mode:'approval_required',expires_at:2000000000000}};
    const body={action:'save',workspace_id:'workspace',id:'automation',mutation_id:'mutation',expected_revision:0,definition};
    const parse=(body)=>parseAutomationProposal({result:{status:'requires_user_approval',applied:false,proposal:{method:'POST',path:'/v3/automations',body}}});
    window.malformed=[{...definition,plans:[null]},{...definition,authorization:{allowed_tools:{bad:true}}},{...definition,schedule:{...definition.schedule,expression:{bad:true}}}].map(definition=>parse({...body,definition}));
    createRoot(document.getElementById('root')).render(<><AutomationOverview onPrompt={draft=>window.drafts.push(draft)} onUpdates={()=>window.updates++}/><AutomationProposalSummary proposal={parse(body)}/></>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('heading', { name: 'Plan once. Stay in control.' }).waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).drafts), [])
    assert.deepEqual(await page.evaluate(() => (window as any).malformed), [null, null, null])
    for (const title of ['overnight supervision', 'archive readiness', 'ready-to-test summary']) {
      await page.getByRole('button', { name: `Customize ${title}` }).focus()
      await page.keyboard.press('Enter')
    }
    const drafts = await page.evaluate(() => (window as any).drafts as string[])
    assert.equal(drafts.length, 3)
    assert.match(drafts[0], /unavailable/)
    assert.match(drafts[1], /separate approval/)
    assert.match(drafts[2], /Do not run tests/)
    assert.equal(await page.getByText('Limited · planning only', { exact: true }).count(), 1)
    assert.equal(await page.getByText('Manual — explicit run request · UTC', { exact: true }).count(), 1)
    assert.equal(await page.getByText('review · plan plan · revision 2', { exact: true }).count(), 1)
    await page.getByRole('button', { name: 'View outcomes and history' }).click()
    assert.equal(await page.evaluate(() => (window as any).updates), 1)
  } finally { await browser.close() }
})
