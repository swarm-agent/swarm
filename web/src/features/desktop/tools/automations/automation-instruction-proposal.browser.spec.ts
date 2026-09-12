import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: sidechat instruction proposals must never execute supplied URLs or
// authorize recurrence. Actual card controls prove separate draft/approval/CAS
// gestures and zero writes for forged paths, foreign ownership and stale context.
// API stubs isolate the consumer; backend plan authorization remains parent proof.
test('instruction proposal controls bind context and separate every write', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {AutomationInstructionContext,AutomationInstructionProposal} from './src/features/desktop/tools/automations/automation-instruction-proposal';
    window.calls=[];window.rev=3;window.owner='parent';window.saved=null;
    window.record=()=>({automation_id:'auto',scope:{workspace_id:'workspace'},revision:window.rev,definition:{session_id:window.owner,name:'Review',enabled:true,schedule:{kind:'manual'},authorization:{mode:'approved_policy',approval_reference:'old'},plans:[{id:'primary',plan:{session_id:'parent',plan_id:'old',revision:1}}]}});
    const root=createRoot(document.getElementById('root'));let key=0;
    window.show=(path='/v3/sessions/parent/plans',method='POST',trusted=true)=>root.render(<AutomationInstructionContext.Provider value={trusted?{parentSessionId:'parent',automation_id:'auto',automation_revision:3,workspace_id:'workspace'}:null}><AutomationInstructionProposal key={++key} payload={{status:'requires_user_approval',applied:false,proposal:{method,path,body:{document:{id:'untrusted',title:'Instructions',info:{goal:'Review'},checkpoints:[{id:'cp',tasks:['Review']}]},title:'Instructions',activate:false,status:'draft',approval_state:'pending'}}}}/></AutomationInstructionContext.Provider>);window.show();
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent', plugins: [{ name: 'boundaries', setup(b) {
    b.onResolve({ filter: /(?:app\/api|desktop-automation-api|desktop-automations)$/ }, args => ({ path: args.path.split('/').pop()!, namespace: 'fixture' }))
    b.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api' ? `export const requestJson=async(url,init)=>{if(!init)return structuredClone(window.saved);const body=JSON.parse(init.body);window.calls.push({url,body});window.saved={document_sha256:'a'.repeat(64),plan:{id:body.plan_id,session_id:'parent',version:window.calls.length,document:body.document,approval_state:body.approval_state}};return structuredClone(window.saved)};` : args.path === 'desktop-automation-api' ? `export const readAutomations=async()=>({record:window.record()});` : `export const desktopAutomations={mutate:async body=>{window.calls.push({url:'/v3/automations',body});return {}}};` }))
  } }] })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const draft = page.getByRole('button', { name: 'Save instruction draft' })
    await draft.waitFor()
    for (const [path, method, trusted] of [['/v3/sessions/foreign/plans', 'POST', true], ['/v3/sessions/parent/plans?activate=true', 'POST', true], ['/v3/sessions/parent/plans', 'DELETE', true], ['/v3/sessions/parent/plans', 'POST', false]] as const) {
      await page.evaluate(([p, m, t]) => (window as any).show(p, m, t), [path, method, trusted])
      await draft.waitFor({ state: 'detached' })
      assert.equal(await page.evaluate(() => (window as any).calls.length), 0)
    }
    await page.evaluate(() => { (window as any).rev = 4; (window as any).show() })
    await draft.click()
    await page.getByRole('alert').waitFor()
    assert.equal(await page.evaluate(() => (window as any).calls.length), 0)
    await page.evaluate(() => { (window as any).rev = 3; (window as any).owner = 'foreign' })
    await draft.click()
    assert.equal(await page.evaluate(() => (window as any).calls.length), 0)
    await page.evaluate(() => { (window as any).owner = 'parent' })
    await draft.click()
    const approve = page.getByRole('button', { name: 'Approve exact instruction revision' })
    await approve.waitFor()
    assert.equal(await page.evaluate(() => (window as any).calls.length), 1)
    await page.evaluate(() => { (window as any).saved.plan.version = 99 })
    await approve.click()
    await page.getByRole('alert').waitFor()
    assert.equal(await page.evaluate(() => (window as any).calls.length), 1)
    await page.evaluate(() => { (window as any).saved.plan.version = 1 })
    await approve.click()
    const save = page.getByRole('button', { name: 'Save paused automation configuration' })
    await save.waitFor()
    assert.equal(await page.evaluate(() => (window as any).calls.length), 2)
    await page.evaluate(() => { (window as any).rev = 4 })
    await save.click()
    await page.getByRole('alert').waitFor()
    assert.equal(await page.evaluate(() => (window as any).calls.length), 2)
    await page.evaluate(() => { (window as any).rev = 3 })
    await save.click()
    await page.getByRole('status').waitFor()
    const calls = await page.evaluate(() => (window as any).calls)
    assert.equal(calls[0].body.activate, false)
    assert.equal(calls[0].body.approval_state, 'pending')
    assert.notEqual(calls[0].body.plan_id, 'untrusted')
    assert.equal(calls[1].body.approval_state, 'approved')
    assert.deepEqual(calls[1].body.document, calls[0].body.document)
    assert.equal(calls[2].body.id, 'auto')
    assert.equal(calls[2].body.expected_revision, 3)
    assert.equal(calls[2].body.definition.enabled, false)
    assert.equal(calls[2].body.definition.authorization.approval_reference, '')
    assert.equal(calls[2].body.definition.plans[0].plan.document_sha256, 'a'.repeat(64))
  } finally { await browser.close() }
})
