// Purpose: prove the real card's full-batch confirmation and cancellation in
// Chromium, including accessible controls and overflow. No daemon/provider calls.
import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { chromium } from 'playwright'

test('large batch card confirms all iterations and cancel dispatches nothing', { timeout: 60_000 }, async () => {
  const result = await build({ configFile: false, logLevel: 'error', plugins: [react(), tailwindcss(), {
    name: 'media-admission-fixture',
    resolveId(id) { if (id === 'virtual:admission') return '\0admission.tsx' },
    load(id) { if (id === '\0admission.tsx') return `
      import React from 'react';import {createRoot} from 'react-dom/client';
      import {MediaTaskCard} from '${process.cwd()}/src/features/desktop/orchestrate/media-task-card.tsx';
      import {confirmMediaBatch} from '${process.cwd()}/src/features/desktop/orchestrate/media-admission.ts';
      import {mapBackendTask} from '${process.cwd()}/src/features/desktop/state/desktop-projects-state.ts';
      import '${process.cwd()}/src/theme.css';
      const task=mapBackendTask({id:'batch',title:'Mountain images',agent:'image',status:'pending_approval',variant_count:25,deliverables:Array.from({length:25},(_,i)=>({id:'slot-'+i,title:'Image '+(i+1),kind:'image',status:'pending'}))});
      window.dispatches=0;
      createRoot(document.getElementById('root')).render(<MediaTaskCard task={task} onApprove={()=>{if(confirmMediaBatch(task,message=>window.confirm(message)))window.dispatches++}}/>);
    ` },
  }], build: { write: false, minify: false, rolldownOptions: { input: 'virtual:admission', output: { inlineDynamicImports: true } } } })
  assert.ok(!Array.isArray(result) && 'output' in result)
  const js = result.output.filter(entry => entry.type === 'chunk').map(entry => entry.code).join('\n')
  const css = result.output.filter(entry => entry.type === 'asset' && entry.fileName.endsWith('.css')).map(entry => String(entry.source)).join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  try {
    const page = await browser.newPage({ viewport: { width: 375, height: 850 } })
    page.setDefaultTimeout(7000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('http://localhost/fixture')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: js, type: 'module' })
    const button = page.getByRole('button', { name: 'Confirm 25 iterations', exact: true })
    await button.waitFor()
    page.once('dialog', dialog => { assert.match(dialog.message(), /all 25 media iterations/); void dialog.dismiss() })
    await button.click()
    assert.equal(await page.evaluate(() => (window as any).dispatches), 0)
    page.once('dialog', dialog => { void dialog.accept() })
    await button.click()
    assert.equal(await page.evaluate(() => (window as any).dispatches), 1)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
    assert.deepEqual(errors, [])
    if (process.env.SWARM_MEDIA_REVIEW_SCREENSHOT) await page.getByTestId('media-task-card').screenshot({ path: process.env.SWARM_MEDIA_REVIEW_SCREENSHOT })
  } finally { await browser.close() }
})
