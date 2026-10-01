import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: TaskAttemptHistory must retrieve chronological backend pages and
// navigate retained sessions after reload, without duplicate requests or polling.
// Threat: React-only lineage and lost navigation. The isolated rendered component
// with fixed HTTP responses is the narrow UI layer; it is not live product proof.
test('task history reload, pagination and retained-session navigation', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react';import {createRoot} from 'react-dom/client';import {TaskAttemptHistory} from './src/features/desktop/orchestrate/task-attempt-history';window.opened=[];createRoot(document.getElementById('root')).render(<TaskAttemptHistory projectId="project" taskId="task" onOpen={id=>window.opened.push(id)}/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    let requests = 0
    await page.route('**/*', route => {
      const url = new URL(route.request().url())
      if (url.pathname.endsWith('/history')) {
        requests++
        const first = url.searchParams.get('cursor') === '0'
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ attempts: [{ id: first ? 'initial' : 'followup', session_id: first ? 'original' : 'new-session', role: first ? 'coder' : 'swarm', created_at: first ? 0 : 86400200, status: 'needs_review', request: first ? '' : 'Full follow-up request', summary: first ? 'Original outcome' : 'New outcome; validation pending' }], next_cursor: first ? 1 : 0 }) })
      }
      return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
    })
    const mount = async () => { await page.goto('https://task-history.test/'); await page.addScriptTag({ content: bundle.outputFiles[0].text }) }
    await mount()
    assert.equal(requests, 0)
    await page.getByRole('button', { name: 'View previous runs' }).click()
    await page.getByText('Original outcome').waitFor()
    await page.getByRole('button', { name: 'More history' }).click()
    await page.getByText('Full follow-up request').waitFor()
    await page.getByRole('button', { name: 'Open swarm session' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).opened), ['new-session'])
    assert.equal(requests, 2)
    await mount()
    await page.getByRole('button', { name: 'View previous runs' }).click()
    await page.getByText('Original outcome').waitFor()
    await page.getByRole('button', { name: 'Open coder session' }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).opened), ['original'])
    assert.equal(requests, 3)
  } finally { await browser.close() }
})
