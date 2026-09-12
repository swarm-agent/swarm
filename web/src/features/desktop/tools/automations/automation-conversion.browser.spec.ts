import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: typed conversion uses PolicyApproval.ApproveUser, not ordinary
// plan acceptance, and requires a separate enable gesture. Foreign/stale reads
// cannot produce any write. Real controls plus mocked API boundaries are the
// narrowest deterministic interaction proof (not backend authorization proof).
test('typed conversion preserves intent and separates approval from enable', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {AutomationPlanApproval} from './src/features/desktop/tools/automations/automation-plan-approval';
    import {normalizeStructuredPlanDocument,structuredPlanDocumentToWire,StructuredPlanReviewView} from './src/features/desktop/chat/components/structured-plan-document';
    window.writes=[]; window.foreign=false; window.stale=false;
    window.raw={id:'proposal',revision_id:'1',title:'Hourly review',info:{goal:'Review'},checkpoints:[],automation:{scope:{account_id:'account',workspace_id:'workspace'},automation_id:'auto',definition_revision:3,existing:false,definition:{name:'Hourly review',session_id:'parent',enabled:false,plans:[],schedule:{kind:'interval',interval_seconds:3600,timezone:'UTC',missed_policy:'skip',overlap_policy:'serialize'},authorization:{mode:'approval_required',expires_at:2000000000000}}}};
    const document=normalizeStructuredPlanDocument(window.raw); window.roundtrip=structuredPlanDocumentToWire(document);
    createRoot(document.getElementById('root')).render(<><StructuredPlanReviewView document={document}/><AutomationPlanApproval document={document} parentSessionId='parent'/><div>Retained artifact</div></>);
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent', plugins: [{ name: 'boundaries', setup(b) {
    b.onResolve({ filter: /(?:app\/api|desktop-automations|desktop-automation-api|automation-session)$/ }, args => ({ path: args.path.split('/').pop()!, namespace: 'fixture' }))
    b.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ loader: 'tsx', contents: args.path === 'api' ? `export const requestJson=async()=>({plan:{id:'proposal',version:1,document:{...window.raw,revision_id:window.stale?'2':'1'}}});` : args.path === 'desktop-automations' ? `export const desktopAutomations={mutate:async input=>{window.writes.push(input);return input.action==='approve'?{enable_proposal:{method:'POST',path:'/v3/automations',body:{action:'enable',workspace_id:'workspace',id:'auto',mutation_id:'enable',expected_revision:3}}}:{};}};` : args.path === 'desktop-automation-api' ? `export const validateAutomationMutation=()=>{}; export const readAutomations=async()=>({record:{revision:3,scope:{account_id:'account',workspace_id:'workspace'},automation_id:'auto',definition:{session_id:window.foreign?'foreign':'parent'}},policy_sha256:'a'.repeat(64)});` : `export function AutomationSessionPanel(){return null}` }))
  } }] })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: 'Approve automation conversion' }).waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).roundtrip.automation), await page.evaluate(() => (window as any).raw.automation))
    assert.equal(await page.getByText('UTC', { exact: true }).count(), 1)
    await page.evaluate(() => { (window as any).stale = true })
    await page.getByRole('button', { name: 'Approve automation conversion' }).click()
    await page.getByRole('alert').filter({ hasText: 'proposal changed' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).writes.length), 0)
    await page.evaluate(() => { (window as any).stale = false; (window as any).foreign = true })
    await page.getByRole('button', { name: 'Approve automation conversion' }).click()
    await page.getByRole('alert').filter({ hasText: 'configuration changed' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).writes.length), 0)
    await page.evaluate(() => { (window as any).foreign = false })
    await page.getByRole('button', { name: 'Approve automation conversion' }).click()
    await page.getByRole('heading', { name: 'Review automation enable' }).waitFor()
    const writes = await page.evaluate(() => (window as any).writes)
    assert.equal(writes.length, 1)
    assert.equal(writes[0].action, 'approve')
    assert.equal(writes[0].proposal.session_id, 'parent')
    assert.equal(writes[0].proposal.revision, 1)
    assert.match(writes[0].proposal.document_sha256, /^[a-f0-9]{64}$/)
    await page.getByRole('button', { name: 'Accept reviewed request' }).click()
    await page.waitForFunction(() => (window as any).writes.length === 2)
    assert.equal(await page.evaluate(() => (window as any).writes[1].action), 'enable')
    assert.equal(await page.getByText('Retained artifact').count(), 1)
  } finally { await browser.close() }
})
