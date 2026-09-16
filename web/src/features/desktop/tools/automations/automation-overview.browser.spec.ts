import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: AutomationOverview uses scheduled dates and actual occurrence states,
// marks partial counts, and never renders failed reads as zero activity.
// Threat: misleading daily totals and inaccessible detail navigation. Real component
// rendering is the narrowest layer; this is not live API or styled-shell evidence.
test('daily activity preserves timezone, pagination and failure semantics', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {AutomationOverview,dailyOccurrences} from './src/features/desktop/tools/automations/automation-overview';
    window.selected=[]; window.next=0; window.retries=0;
    const rows=[{id:'run',automation_id:'automation',written_at:Date.parse('2026-03-10T12:00:00Z'),occurrence:{scheduled_at:Date.parse('2026-03-08T07:30:00Z'),state:'blocked'}}];
    window.zoned=dailyOccurrences(rows,'America/Los_Angeles')[0][0];
    const root=createRoot(document.getElementById('root'));
    window.render=(extra={})=>root.render(<AutomationOverview records={rows} names={{automation:'Nightly review'}} loading={false} stale={false} partial={true} onRetry={()=>window.retries++} onNext={()=>window.next++} onSelect={id=>window.selected.push(id)} {...extra}/>);
    window.render();`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ timezoneId: 'UTC' })
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('heading', { name: 'Daily activity' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).zoned), '2026-03-07')
    await page.getByText(/Incomplete daily counts/).waitFor()
    await page.getByRole('heading', { name: '2026-03-08 · 1 runs on this page' }).waitFor()
    await page.getByRole('button', { name: /Nightly review/ }).focus()
    await page.keyboard.press('Enter')
    assert.deepEqual(await page.evaluate(() => (window as any).selected), ['automation'])
    await page.getByRole('button', { name: 'More activity' }).click()
    assert.equal(await page.evaluate(() => (window as any).next), 1)
    await page.evaluate(() => (window as any).render({ records: [], error: 'Read failed', partial: false }))
    await page.getByRole('alert').waitFor()
    assert.equal(await page.getByText('No recorded runs yet.').count(), 0)
    await page.getByRole('button', { name: 'Retry activity' }).click()
    assert.equal(await page.evaluate(() => (window as any).retries), 1)
    await page.evaluate(() => (window as any).render({ records: [], loading: true, partial: false }))
    await page.getByText('Loading activity…').waitFor()
    assert.equal(await page.getByText('No recorded runs yet.').count(), 0)
    await page.evaluate(() => (window as any).render({ records: [], partial: false }))
    await page.getByText('No recorded runs yet.').waitFor()
  } finally { await browser.close() }
})
