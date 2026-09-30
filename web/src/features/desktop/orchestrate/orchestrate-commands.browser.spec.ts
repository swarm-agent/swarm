import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: rendered OrchestratorChatComposer must navigate without V3 append,
// retain draft/context/attachments, and distinguish keyboard navigation from IME
// and Shift+Enter. This rendered boundary proves observable UI + submission
// postconditions; route integration/history and pixel review remain parent gates.
test('Orchestrate palette is navigation-only and preserves composer state', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    window.sent=[];window.pages=[];
    createRoot(document.getElementById('root')).render(<OrchestratorChatComposer sessionId='command-fixture'
      selectedTaskId='task-fixture' selectedWorker={{id:'worker-fixture',revision:2,name:'Worker'}}
      onCommandNavigate={(page)=>window.pages.push(page)} submitMessage={async(operation)=>window.sent.push(operation)}/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://commands.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByTestId('orchestrator-chat-input')
    await input.fill('Keep my recovery draft /src/file')
    await page.getByRole('button', { name: '/ Commands', exact: true }).click()
    assert.equal(await input.inputValue(), 'Keep my recovery draft /src/file')
    await input.press('ArrowDown')
    await input.press('Enter')
    assert.deepEqual(await page.evaluate(() => (window as any).pages), ['projects'])
    assert.equal(await input.inputValue(), 'Keep my recovery draft /src/file')
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
    await page.getByRole('button', { name: '/ Commands', exact: true }).click()
    assert.equal(await page.getByRole('option').count(), 7)
  } finally { await browser.close() }
})
