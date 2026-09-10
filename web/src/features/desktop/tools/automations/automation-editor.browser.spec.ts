import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: the production configuration editor is keyboard/label accessible
// and must never attach an old draft to a newer revision. Threat: background
// invalidation silently overwrites concurrent edits. A hermetic browser component
// fixture exercises real form events and prop updates, not source-string checks.
// It does not prove server permission enforcement or full workspace visual quality.
test('configuration uses accessible inputs and refuses stale draft submission', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {AutomationEditor} from './src/features/desktop/tools/automations/automation-editor';
    const root=createRoot(document.getElementById('root'));
    window.calls=[];
    const initial={name:'Daily review',enabled:false,plans:[{id:'first',plan:{session_id:'session',plan_id:'plan',revision:1}}],schedule:{kind:'manual',timezone:'UTC',missed_policy:'skip',overlap_policy:'independent'},authorization:{mode:'approval_required'}};
    window.show=(revision)=>root.render(<AutomationEditor initial={initial} revision={revision} disabled={false} onSave={async value=>{window.calls.push(value)}}/>);
    window.show(1);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    // PolicyApproval.ApproveUser rejects absent expiry: the actual editor must
    // expose and persist this field, not force users to craft API bodies.
    await page.getByLabel('Policy expiry (UTC epoch milliseconds)', { exact: true }).fill('2000000000000')
    await page.getByLabel('Name', { exact: true }).fill('Keyboard review')
    await page.getByRole('button', { name: 'Save configuration' }).focus()
    await page.keyboard.press('Enter')
    await page.waitForFunction(() => (window as any).calls.length === 1)
    assert.equal(await page.evaluate(() => (window as any).calls[0].name), 'Keyboard review')
    assert.equal(await page.evaluate(() => (window as any).calls[0].authorization.expires_at), 2000000000000)
    await page.getByLabel('Name', { exact: true }).fill('Unsaved draft')
    await page.evaluate(() => (window as any).show(2))
    await page.getByRole('alert').filter({ hasText: 'Configuration changed' }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Save configuration' }).isDisabled(), true)
    assert.equal(await page.getByLabel('Name', { exact: true }).inputValue(), 'Unsaved draft')
    await page.locator('form').evaluate(form => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    assert.equal(await page.evaluate(() => (window as any).calls.length), 1)
  } finally { await browser.close() }
})
