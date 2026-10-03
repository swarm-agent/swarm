// Purpose: MinimalTaskCard must expose authored requirements and native expanded
// checkpoint rows, never substitute Markdown for a missing structured plan.
// A browser toggle test is the narrowest proof of the actual disclosure path.
import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

test('expanded task plans use native sections and missing plans stay explicit', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    const root=createRoot(document.getElementById('root'));
    window.renderTask=(missing=false)=>root.render(<QueryClientProvider client={new QueryClient()}>
      <MinimalTaskCard key={String(missing)} task={{id:'task',title:'Feature',status:'pending_approval',agentType:'coder',workspaceTarget:'local',elapsed:'',subtasks:[],
        fullPlanMarkdown:'# Legacy prose must not become a plan',
        planDocument:missing?null:{title:'Authored plan',requirements:[{id:'save',text:'Preferences persist',checkpoint_id:'cp'}],
          checkpoints:[{id:'cp',title:'Persistence',tasks:['Write preferences'],acceptance_criteria:['Preferences persist']}]}}}/>
    </QueryClientProvider>);window.renderTask();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://task.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByTestId('toggle-task-details-btn').click()
    assert.equal(await page.locator('[data-requirement-id="save"]').count(), 1)
    await page.getByTestId('toggle-plan-spec-btn').click()
    const reader = page.getByTestId('task-plan-reader')
    assert.equal(await reader.locator('section[data-testid="plan-checkpoint-cp"]').count(), 1)
    assert.equal(await reader.getByRole('listitem').filter({ hasText: 'Preferences persist' }).count(), 1)
    assert.doesNotMatch(await reader.textContent() || '', /Legacy prose/)
    await page.evaluate(() => (window as any).renderTask(true))
    await page.getByTestId('toggle-task-details-btn').click()
    await page.getByTestId('toggle-plan-spec-btn').click()
    assert.match(await reader.textContent() || '', /not been authored yet/)
    assert.doesNotMatch(await reader.textContent() || '', /Legacy prose/)
    assert.equal(await page.locator('[data-requirement-id]').count(), 0)
  } finally {
    await browser.close()
  }
})
