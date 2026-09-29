import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: legacy records are deletable from Tasks/Hub without deleting chat.
// Threat: wrong workspace/generation, swallowed conflicts, stale cards, duplicate
// submissions or a DELETE request against the author conversation.
// Authority: LegacyAutomations -> DeleteAutomationDialog ->
// DesktopAutomationV2Runtime.deleteRecord -> /v3/automations/v2/control.
// This bounded component/HTTP fixture proves UI interactions and cache refresh,
// not live daemon persistence or provider execution.
test('legacy Delete preserves chat, shows conflicts, refreshes and prevents duplicate requests', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {LegacyAutomations} from './src/features/desktop/orchestrate/legacy-automations';
    createRoot(document.getElementById('root')).render(<LegacyAutomations/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    let generation = 2, deleted = false, fail = true, listReads = 0
    const writes: Array<{ method: string; path: string; body: unknown }> = []
    let release!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    let hold = false
    await page.route('**/*', async route => {
      const req = route.request(), url = new URL(req.url())
      if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      if (req.method() !== 'GET') {
        writes.push({ method: req.method(), path: url.pathname, body: req.postDataJSON() })
        if (url.pathname !== '/v3/automations/v2/control') return route.fulfill({ status: 500, body: 'Unexpected mutation' })
        if (fail) { generation = 3; return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'Automation generation conflict. Refresh and try again.' }) }) }
        if (hold) await held
        deleted = true
        return route.fulfill({ contentType: 'application/json', body: '{}' })
      }
      if (url.pathname === '/v3/automations/v2') {
        listReads++
        assert.equal(url.searchParams.get('archived_mode'), 'include')
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ records: deleted ? [] : [{
          automation_id: 'automation-fixture', account_id: 'account-fixture', workspace_id: 'owner-workspace',
          session_id: 'author-conversation', generation, enabled: false, cancelled: false,
          document: { title: 'Legacy example', info: { goal: 'Example' }, checkpoints: [] }
        }] }) })
      }
      return route.fulfill({ status: 404, body: 'Unexpected read' })
    })
    await page.goto('https://automation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const remove = page.getByRole('button', { name: 'Delete Legacy example', exact: true })
    await remove.waitFor()
    await page.getByText('Paused', { exact: true }).waitFor()
    await remove.click()
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    assert.equal(writes.length, 0)
    await remove.click()
    await page.getByRole('button', { name: 'Delete automation', exact: true }).click()
    await page.getByRole('alert').getByText(/generation conflict/).waitFor()
    assert.equal(await page.getByRole('dialog').count(), 1)
    assert.equal(await remove.count(), 1)
    assert.deepEqual(writes, [{ method: 'POST', path: '/v3/automations/v2/control', body: {
      action: 'delete_automation', workspace_id: 'owner-workspace', session_id: 'author-conversation', generation: 2,
    } }])
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.getByText('Refreshing automation state.', { exact: false }).waitFor({ state: 'hidden' })
    fail = false; hold = true
    const before = listReads
    await remove.click()
    await page.getByRole('button', { name: 'Delete automation', exact: true }).click()
    const deleting = page.getByRole('button', { name: 'Deleting…', exact: true })
    await deleting.waitFor()
    assert.equal(await deleting.isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: 'Cancel', exact: true }).isDisabled(), true)
    release()
    await remove.waitFor({ state: 'hidden' })
    await page.getByRole('dialog').waitFor({ state: 'hidden' })
    await page.getByText('No legacy automations on this page.', { exact: true }).waitFor()
    assert.equal(writes.length, 2)
    assert.equal((writes[1].body as { generation: number }).generation, 3)
    assert.ok(listReads > before)
    assert.ok(writes.every(write => write.path === '/v3/automations/v2/control' && write.method === 'POST'))
  } finally { await browser.close() }
})
