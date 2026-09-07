import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'
import { chromium } from 'playwright'

// Requirement: the actual header attachment consumer must paginate, replace
// removed/default identities, and report authorization errors without hiding
// retained data as current. Browser mounting with the real query and mocked
// transport is the narrowest layer proving effects, dialog and responsive CSS.
test('attachment dialog supports 64 identities, live removal and failed refresh at mobile and desktop sizes', { timeout: 60_000 }, async () => {
  const server = await createServer({ server: { host: '127.0.0.1', port: 0 }, logLevel: 'error' })
  server.middlewares.use('/attachment-fixture', async (_req, res) => {
    res.setHeader('Content-Type', 'text/html')
    res.end(await server.transformIndexHtml('/attachment-fixture', `<html><body><div id="root"></div><script type="module">
      import React from 'react';
      import {createRoot} from 'react-dom/client';
      import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
      import {SessionAttachments} from '/src/features/desktop/chat/components/session-attachments.tsx';
      import '/src/theme.css';
      const client = new QueryClient();
      let revision = 0;
      const root = createRoot(document.getElementById('root'));
      window.updateAttachments = () => root.render(React.createElement(QueryClientProvider, {client}, React.createElement(SessionAttachments, {sessionId:'fixture-session', revision:++revision})));
      window.updateAttachments();
    </script></body></html>`))
  })
  await server.listen()
  const address = server.httpServer!.address()
  assert.ok(address && typeof address !== 'string')
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage()
    let removed = false
    let forbidden = false
    const cursors: string[] = []
    await page.route('**/v3/sessions/fixture-session/repositories?*', async (route) => {
      if (forbidden) { await route.fulfill({ status: 403, json: { error: 'Not authorized' } }); return }
      const cursor = new URL(route.request().url()).searchParams.get('cursor') || ''
      cursors.push(cursor)
      const offset = cursor ? Number(cursor.slice('opaque-'.length)) : 0
      const all = Array.from({ length: removed ? 63 : 64 }, (_, index) => ({ id: `row-${index}`, workspace_id: `workspace-${index}`, workspace_name: 'Same name ' + 'long-name-'.repeat(30), source_path: `/workspaces/source-${index}`, kind: 'source', attached: true, default: index === (removed ? 62 : 63), availability: 'available' }))
      await route.fulfill({ json: { ok: true, items: all.slice(offset, offset + 20), next_cursor: offset + 20 < all.length ? `opaque-${offset + 20}` : '' } })
    })
    await page.goto(`http://127.0.0.1:${address.port}/attachment-fixture`)
    await page.getByRole('button', { name: 'Session workspaces: 64 workspaces', exact: true }).waitFor()
    assert.deepEqual(cursors, ['', 'opaque-20', 'opaque-40', 'opaque-60'])
    await page.getByRole('button', { name: 'Session workspaces: 64 workspaces', exact: true }).click()
    for (const width of [375, 1440]) {
      await page.setViewportSize({ width, height: 800 })
      const bounds = await page.getByRole('dialog').evaluate((dialog) => {
        const box = dialog.getBoundingClientRect()
        return { left: box.left, right: box.right, height: box.height, overflow: dialog.scrollHeight > dialog.clientHeight, horizontal: dialog.scrollWidth > dialog.clientWidth }
      })
      assert.ok(bounds.left >= 0 && bounds.right <= width && bounds.height <= 640)
      assert.equal(bounds.overflow, true)
      assert.equal(bounds.horizontal, false)
    }
    assert.equal(await page.locator('[data-workspace-id]').count(), 64)
    assert.match(await page.locator('[data-workspace-id="workspace-63"]').innerText(), /Default/)
    removed = true
    await page.evaluate(() => (window as unknown as { updateAttachments(): void }).updateAttachments())
    await page.waitForFunction(() => document.querySelectorAll('[data-workspace-id]').length === 63)
    assert.equal(await page.locator('[data-workspace-id="workspace-63"]').count(), 0)
    assert.match(await page.locator('[data-workspace-id="workspace-62"]').innerText(), /Default/)
    forbidden = true
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await page.getByRole('status').filter({ hasText: 'Workspace list stale' }).waitFor()
    assert.equal(await page.locator('[data-workspace-id]').count(), 63)
    await page.keyboard.press('Escape')
    assert.equal(await page.getByRole('dialog').isVisible(), false)
  } finally {
    await browser.close()
    await server.close()
  }
})
