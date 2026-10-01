// Purpose: MinimalTaskCard disclosure grows strictly below its stable compact summary.
// Threat: expanded evidence shifts the toggle/header, duplicates sessions/outputs or
// invents successful validation. Authority: MinimalTaskCard, TaskCardSummary,
// TaskCardOutputs, TaskAttemptHistory. Browser geometry, focus and real handlers are
// the narrowest layer proving this UI contract; provider and pixel review are separate.
import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { build } from 'esbuild'
import { chromium } from 'playwright'

test('actual task toggle preserves summary geometry, output targets and deliberate history', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react';import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {MinimalTaskCard} from './src/features/desktop/orchestrate/OrchestrateView';
    import {TaskAttemptHistory} from './src/features/desktop/orchestrate/task-attempt-history';
    window.opened=[];window.previewed=[];
    const task={id:'task',title:'Sidebar',status:'needs_review',agentType:'coder',workspaceTarget:'local',elapsed:'',subtasks:[],
      sessionId:'child',gitStatus:'clean',isIntegrated:false,handoffSummary:'Fixed sidebar. Tests not run; parent validation required.',
      sessionSummary:{totalSessions:1,sessionStates:[{sessionId:'child',title:'Implementation',role:'coder',status:'needs_review',hydrated:true}]},
      deliverables:[{id:'pending',type:'code',title:'Code PR & Verified Tests',status:'pending'},
        {id:'report',type:'report',title:'Actual report',status:'ready',mediaUrl:'/reports/result'},
        {id:'image',type:'image',title:'Actual image',status:'ready',mediaUrl:'/media/result'},
        {id:'missing',type:'code',title:'No target',status:'ready'}]};
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}>
      <div className='swarm-section'><MinimalTaskCard task={task} onInvestigateSession={id=>window.opened.push(id)} onPreviewDeliverable={d=>window.previewed.push(d.id)}
        previousRuns={<TaskAttemptHistory projectId='project' taskId='task' onOpen={id=>window.opened.push(id)}/>}/></div>
    </QueryClientProvider>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  const errors: string[] = []
  try {
    const page = await browser.newPage({ viewport: { width: 360, height: 640 } })
    page.setDefaultTimeout(5000)
    let historyRequests = 0
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => {
      if (route.request().url().includes('/history?')) {
        historyRequests++
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ attempts: [], next_cursor: 0 }) })
      }
      return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
    })
    await page.goto('https://task.test/')
    await page.addStyleTag({ content: readFileSync('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const summary = page.getByTestId('task-card-summary')
    const toggle = page.getByTestId('toggle-task-details-btn')
    const before = await summary.innerHTML()
    const headerY = (await summary.boundingBox())!.y
    const toggleY = (await toggle.boundingBox())!.y
    await toggle.focus()
    await page.keyboard.press('Enter')
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true')
    assert.equal(await summary.innerHTML(), before)
    assert.equal((await summary.boundingBox())!.y, headerY)
    assert.equal((await toggle.boundingBox())!.y, toggleY)
    const detailsId = await toggle.getAttribute('aria-controls')
    assert.equal(await page.evaluate(id => {
      const details = document.getElementById(id!)!
      const toggle = document.querySelector('[data-testid="toggle-task-details-btn"]')!
      return details.getBoundingClientRect().top >= toggle.getBoundingClientRect().bottom
    }, detailsId), true)
    assert.equal(await page.locator('[data-session-id="child"]').count(), 1)
    await page.getByRole('button', { name: 'View coder session Implementation' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).opened), ['child'])
    assert.equal(await page.getByRole('link', { name: 'Actual report' }).getAttribute('href'), '/reports/result')
    await page.getByRole('button', { name: 'Actual image', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).previewed), ['image'])
    assert.equal(await page.getByRole('button', { name: 'No target', exact: true }).count(), 0)
    assert.equal(await page.getByRole('link', { name: 'Code PR & Verified Tests' }).count(), 0)
    assert.match(await page.locator('.swarm-task-handoff').innerText(), /Tests not run/)
    assert.equal(historyRequests, 0)
    assert.equal(await page.evaluate(() => {
      const outputs = document.querySelector('[aria-label="Outputs"]')!
      const history = document.querySelector('[aria-label="Previous runs"]')!
      return Boolean(outputs.compareDocumentPosition(history) & Node.DOCUMENT_POSITION_FOLLOWING)
    }), true)
    await page.getByRole('button', { name: 'View previous runs' }).click()
    await page.getByRole('button', { name: 'Refresh history' }).waitFor()
    assert.equal(historyRequests, 1)
    await page.getByRole('button', { name: 'Hide previous runs' }).click()
    assert.equal(historyRequests, 1)
    await page.getByTestId('collapse-task-details-btn').click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false')
    await page.waitForFunction(() => document.activeElement?.getAttribute('data-testid') === 'toggle-task-details-btn')
    assert.equal(await toggle.evaluate(node => node === document.activeElement), true)
    assert.equal(await summary.innerHTML(), before)
    await page.getByRole('heading', { name: 'Sidebar', exact: true }).click()
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true')
    assert.deepEqual(errors, [])
  } catch (error) {
    assert.deepEqual(errors, [], 'task disclosure must render without browser errors')
    throw error
  } finally { await browser.close() }
})
