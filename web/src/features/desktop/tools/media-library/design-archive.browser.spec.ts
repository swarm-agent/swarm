// Purpose: DesignArchiveButton owns immediate pending and retry presentation.
// A real React DOM test with controlled mutation receipts prevents duplicate
// clicks, premature success, and hidden errors without needing a live provider.
import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

test('design archive waits for acknowledgement and exposes retry', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { contents: `import React from 'react';import {createRoot} from 'react-dom/client';
    import {DesignArchiveButton} from './src/features/desktop/tools/media-library/design-archive-button';
    import {desktopDesigns} from './src/features/desktop/runtime/desktop-design-runtime';
    if(!crypto.randomUUID) crypto.randomUUID=()=> 'test-idempotency-key';
    window.calls=0; desktopDesigns.archive=()=>{window.calls++;return new Promise((resolve,reject)=>{window.finish=resolve;window.fail=()=>reject(new Error('409: stale version'))})};
    createRoot(document.getElementById('root')).render(<DesignArchiveButton session="s" reference={{artifact_id:'a',revision:1,sha256:'digest'}} version={0}/>);`, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: 'Archive', exact: true }).evaluate(button => { (button as HTMLButtonElement).click(); (button as HTMLButtonElement).click() })
    assert.equal(await page.getByRole('button', { name: 'Archiving…' }).isDisabled(), true)
    assert.equal(await page.evaluate(() => (window as any).calls), 1)
    await page.evaluate(() => (window as any).fail())
    await page.getByRole('alert').waitFor()
    assert.match(await page.getByRole('alert').innerText(), /stale version/)
    await page.getByRole('button', { name: 'Retry', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).calls), 2)
    await page.evaluate(() => (window as any).finish())
    await page.getByRole('button', { name: 'Archive', exact: true }).waitFor()
    assert.equal(await page.getByRole('alert').count(), 0)
  } finally { await browser.close() }
})
