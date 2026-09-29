import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: a human must expand the actual pending revision before acceptance.
// Threat: hidden edits get approved or a failed accept is displayed as success.
// Boundary: rendered PendingWorkerCard -> canonical desktopWorkers mutation. This
// component fixture tests interactions, not live daemon/provider execution.
test('pending worker expansion, exact acceptance, conflict and revision review', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {PendingWorkerCard} from './src/features/desktop/orchestrate/pending-worker-card';
    import {desktopWorkers} from './src/features/desktop/runtime/desktop-workers';
    let worker={id:'worker_review',account_scope_id:'account',name:'Repository Reviewer',description:'Review only when asked.',instructions:'Inspect repository changes and report findings. Do not modify files.',revision:1,lifecycle_state:'pending',created_at:1,updated_at:1,proposed_bindings:{primary:'workspace_demo'},workspace_requirements:[{role:'primary',required:true}]};
    window.calls=[]; desktopWorkers.mutate=async input=>{window.calls.push(input);throw new Error('Worker revision conflict; review again')};
    const root=createRoot(document.getElementById('root'));
    const render=()=>root.render(<PendingWorkerCard key={worker.revision} worker={worker} accountScopeId='account' workspaceSlug='demo'/>);
    window.revise=()=>{worker={...worker,revision:2,instructions:'Changed instructions require review'};render()};render();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 900, height: 1000 } })
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<style>body{background:#101827;color:#e2e8f0;font:14px system-ui;margin:24px}svg{width:16px;height:16px}button,a{margin:8px}article{max-width:780px}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><div id="root"></div>' }))
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByTestId('pending-worker-card').waitFor()
    assert.equal(await page.getByRole('button', { name: 'Accept worker', exact: true }).count(), 0)
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [])
    await page.getByRole('button', { name: 'Expand', exact: true }).click()
    await page.getByText('No job attached; waits for a task after acceptance', { exact: true }).waitFor()
    assert.equal(await page.getByRole('link', { name: 'Open worker detail' }).getAttribute('href'), '/demo/workers/worker_review')
    await page.getByRole('button', { name: 'Accept worker', exact: true }).click()
    await page.getByRole('alert').getByText('Worker revision conflict; review again').waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), [{ action: 'accept', workerId: 'worker_review', expected_revision: 1 }])
    await page.evaluate(() => (window as any).revise())
    await page.getByText('r2', { exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Accept worker', exact: true }).count(), 0)
    await page.getByRole('button', { name: 'Expand', exact: true }).click()
    await page.getByText('Changed instructions require review', { exact: true }).waitFor()
    if (process.env.SWARM_PENDING_CARD_SCREENSHOT) await page.screenshot({ path: process.env.SWARM_PENDING_CARD_SCREENSHOT, fullPage: true })
  } finally { await browser.close() }
})
