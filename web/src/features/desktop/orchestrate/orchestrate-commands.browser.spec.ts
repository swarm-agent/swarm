import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { projectTestStyles } from './project-test-styles'
import { snapshot, sessionId } from './swarm-responsive-browser-fixtures'

// Purpose: rendered OrchestratorChatComposer must navigate/open Codex usage without V3 append or settings/credit mutations,
// retain draft/context/attachments, and distinguish keyboard navigation from IME
// and Shift+Enter. This rendered boundary proves observable UI + submission
// postconditions; route integration/history and pixel review remain parent gates.
test('Orchestrate palette navigates and opens Codex usage while preserving composer state', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    window.sent=[];window.pages=[];
    createRoot(document.getElementById('root')).render(<OrchestratorChatComposer sessionId='${sessionId}'
      project={{id:'project-fixture',name:'Project fixture'}} selectedTaskId='task-fixture' selectedWorker={{id:'worker-fixture',revision:2,name:'Worker'}}
      onCommandNavigate={(page)=>window.pages.push(page)} submitMessage={async(operation)=>window.sent.push(operation)}/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const css = await projectTestStyles()
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
    page.setDefaultTimeout(7000)
    page.on('pageerror', error => console.error(error.message))
    const apiCalls: Array<{ path: string; method: string }> = []
    let failUsage = false
    await page.route('**/*', route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      apiCalls.push({ path, method: route.request().method() })
      if (path === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      if (path === '/v3/projects/project-fixture/tasks/task-fixture') return route.fulfill({ json: { task: { id: 'task-fixture', project_id: 'project-fixture', title: 'Fixture task', agent: 'coder', revision: 1, status: 'queued' } } })
      if (path === '/v1/codex/account/usage' && failUsage) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'usage unavailable' }) })
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ plan_type: 'fixture-plan', credits: [], available_count: 0 }) })
    })
    await page.goto('https://commands.test/')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByTestId('orchestrator-chat-input')
    await input.fill('Keep my recovery draft /src/file')
    assert.equal(await page.getByRole('button', { name: '/ Commands', exact: true }).count(), 0)
    await input.fill('/')
    await input.press('ArrowDown')
    await input.press('Enter')
    assert.deepEqual(await page.evaluate(() => (window as any).pages), ['projects'])
    assert.equal(await input.inputValue(), '/')
    assert.equal(await input.evaluate(element => element === document.activeElement), true)
    assert.equal(await page.getByTestId('composer-task-context-badge').count(), 1)
    await input.fill('/sett')
    await page.getByRole('option', { name: /\/settings/ }).waitFor()
    await input.press('Escape')
    assert.equal(await page.getByRole('listbox').count(), 0)
    assert.equal(await input.inputValue(), '/sett')
    await input.fill('/agents')
    await input.dispatchEvent('compositionstart')
    await input.press('Enter')
    assert.deepEqual(await page.evaluate(() => (window as any).pages), ['projects'])
    await input.dispatchEvent('compositionend')
    await input.press('Enter')
    assert.deepEqual(await page.evaluate(() => (window as any).pages), ['projects', 'agents'])
    for (const command of ['actions', 'artifact', 'commit', 'commit ai', 'integrate', 'plan', 'task plan']) {
      await input.fill(` /${command.toUpperCase().replaceAll(' ', '\t')} `)
      await page.getByRole('button', { name: 'Send message', exact: true }).click()
      await page.getByTestId('chat-send-error').getByText(/Unsupported Orchestrate command/).waitFor()
      assert.equal((await page.evaluate(() => (window as any).sent)).length, 0)
    }
    await input.fill('/settings')
    await input.press('Shift+Enter')
    assert.match(await input.inputValue(), /\n/)
    await input.fill('/')
    await page.getByRole('option', { name: /\/workers/ }).click()
    assert.equal(await input.inputValue(), '/')
    assert.deepEqual(await page.evaluate(() => (window as any).pages), ['projects', 'agents', 'workers'])
    assert.equal((await page.evaluate(() => (window as any).sent)).length, 0)
    await page.setViewportSize({ width: 1440, height: 900 })
    await input.fill('/w')
    await input.fill('/')
    assert.equal(await page.getByRole('option').count(), 8)
    for (const mode of ['pointer', 'keyboard', 'submit']) {
      await input.fill('')
      await input.fill(mode === 'submit' ? '  /CODEX  ' : '/cod')
      if (mode !== 'submit') await page.getByRole('option', { name: /\/codex/ }).waitFor()
      if (mode === 'pointer') await page.getByRole('option', { name: /\/codex/ }).click()
      else if (mode === 'keyboard') await input.press('Enter')
      else await page.getByRole('button', { name: 'Send message', exact: true }).click()
      const dialog = page.getByRole('dialog', { name: 'Codex usage', exact: true })
      await dialog.waitFor()
      if (failUsage) await dialog.getByText(/Usage could not be loaded/).waitFor()
      else await dialog.getByText('ChatGPT plan: fixture-plan', { exact: true }).waitFor()
      if (mode === 'pointer' && process.env.SWARM_TASK_CODEX_SCREENSHOT) await page.screenshot({ path: process.env.SWARM_TASK_CODEX_SCREENSHOT })
      assert.equal(await input.inputValue(), mode === 'submit' ? '  /CODEX  ' : '/cod')
      assert.equal(await page.getByTestId('composer-task-context-badge').count(), 1)
      assert.equal((await page.evaluate(() => (window as any).sent)).length, 0)
      await dialog.getByRole('button', { name: 'Close', exact: true }).click()
      failUsage = true
    }
    assert.equal(apiCalls.filter(call => call.path === '/v1/codex/account/usage').length, 3)
    assert.ok(apiCalls.every(call => call.method === 'GET' || (call.method === 'POST' && call.path === '/v3/sync/hydrate')), JSON.stringify(apiCalls))
    assert.ok(apiCalls.every(call => !/settings|consume/.test(call.path)), JSON.stringify(apiCalls))
    await input.fill('Discuss /codex tomorrow')
    await page.getByRole('button', { name: 'Send message', exact: true }).click()
    await page.waitForFunction(() => (window as any).sent.length === 1)
  } finally { await browser.close() }
})
