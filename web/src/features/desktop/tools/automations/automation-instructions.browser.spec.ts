import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: direct instruction edits create an approved canonical document
// before changing a definition pin. Foreign/stale source and failed saves must
// leave pins untouched. AutomationInstructionsEditor is the narrowest control
// layer; daemon approval and CAS authorization require parent backend tests.
test('instruction editor refuses foreign source and pins only approved results', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {AutomationInstructionsEditor} from './src/features/desktop/tools/automations/automation-instructions-editor';
    window.pins=[];window.calls=[];window.approved=false;window.raw={id:'old',info:{goal:'Old objective'},checkpoints:[{id:'check',tasks:['Retained task']}]};
    const root=createRoot(document.getElementById('root'));
    window.show=(session='foreign')=>root.render(<AutomationInstructionsEditor key={session} parentSessionId='parent' binding={{id:'primary',plan:{session_id:session,plan_id:'old',revision:2,document_sha256:'digest'}}} onPin={pin=>window.pins.push(pin)}/>);window.show();
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent', plugins: [{ name: 'boundaries', setup(b) {
    b.onResolve({ filter: /(?:app\/api|automation-plan-approval|automation-editor)$/ }, args => ({ path: args.path.split('/').pop()!, namespace: 'fixture' }))
    b.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api' ? `export const requestJson=async(url,init)=>{window.calls.push({url,init});if(!init)return {plan:{id:'old',version:2,document:window.raw}};const body=JSON.parse(init.body);return {plan:{id:body.plan_id,version:1,document:body.document,status:window.approved?'approved':'pending',approval_state:window.approved?'approved':'pending'}};};` : args.path === 'automation-plan-approval' ? `export const automationDocumentDigest=async()=> 'digest';` : `export const automationControl='';` }))
  } }] })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: 'Edit executable instructions' }).click()
    await page.getByRole('alert').filter({ hasText: 'must belong' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).calls.length), 0)
    await page.evaluate(() => (window as any).show('parent'))
    await page.getByRole('button', { name: 'Edit executable instructions' }).click()
    await page.getByRole('textbox', { name: 'Executable objective' }).fill('New objective')
    await page.getByRole('button', { name: 'Approve new instruction revision' }).click()
    await page.getByRole('alert').filter({ hasText: 'not approved' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).pins.length), 0)
    await page.evaluate(() => { (window as any).approved = true })
    await page.getByRole('button', { name: 'Approve new instruction revision' }).click()
    await page.waitForFunction(() => (window as any).pins.length === 1)
    const calls = await page.evaluate(() => (window as any).calls)
    const saved = JSON.parse(calls.at(-1).init.body)
    assert.equal(saved.activate, false)
    assert.equal(saved.document.info.goal, 'New objective')
    assert.deepEqual(saved.document.checkpoints, [{ id: 'check', tasks: ['Retained task'] }])
    const pin = await page.evaluate(() => (window as any).pins[0])
    assert.equal(pin.plan_id, saved.plan_id)
    assert.notEqual(pin.plan_id, 'old')
    assert.equal(pin.document_sha256, 'digest')
  } finally { await browser.close() }
})
