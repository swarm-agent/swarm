import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'vite'
import { chromium } from 'playwright'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Requirement: the real attachment query must paginate, replace default/removal
// identities and report failed refreshes. Bundle the actual component and CSS;
// intercept every request so the fixture never contacts an ambient daemon.
test('attachment dialog supports 64 identities, live removal and failed refresh at mobile and desktop sizes', { timeout: 60_000 }, async () => {
  const result = await build({ configFile: false, logLevel: 'error', plugins: [react(), tailwindcss(), {
    name: 'attachment-fixture',
    resolveId(id) { if (id === 'virtual:attachment-fixture') return '\0attachment-fixture' },
    load(id) { if (id === '\0attachment-fixture') return `
      import React from 'react';
      import {createRoot} from 'react-dom/client';
      import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
      import {SessionAttachments} from '${process.cwd()}/src/features/desktop/chat/components/session-attachments.tsx';
      import '${process.cwd()}/src/theme.css';
      const client = new QueryClient();
      let revision = 0;
      const root = createRoot(document.getElementById('root'));
      window.updateAttachments = () => root.render(React.createElement(QueryClientProvider, {client}, React.createElement(SessionAttachments, {sessionId:'fixture-session', revision:++revision})));
      window.updateAttachments();
    ` },
  }], build: { write: false, minify: false, rolldownOptions: { input: 'virtual:attachment-fixture', output: { inlineDynamicImports: true } } } })
  assert.ok(!Array.isArray(result) && 'output' in result)
  const js = result.output.filter((item) => item.type === 'chunk').map((item) => item.code).join('\n')
  const css = result.output.filter((item) => item.type === 'asset' && item.fileName.endsWith('.css')).map((item) => String(item.source)).join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  try {
    const page = await browser.newPage()
    let removed = false
    let forbidden = false
    const cursors: string[] = []
    await page.route('**/*', async (route) => {
      const url = new URL(route.request().url())
      if (url.pathname === '/attachment-fixture') { await route.fulfill({ contentType: 'text/html', body: '<html><body><div id="root"></div></body></html>' }); return }
      if (url.pathname === '/v1/auth/desktop/session') { await route.fulfill({ json: { ok: true, user_id: 'fixture-user', account_scope_id: 'fixture-account' } }); return }
      if (url.pathname !== '/v3/sessions/fixture-session/repositories') { await route.abort(); return }
      if (forbidden) { await route.fulfill({ status: 403, json: { error: 'Not authorized' } }); return }
      const cursor = url.searchParams.get('cursor') || ''
      cursors.push(cursor)
      const offset = cursor ? Number(cursor.slice('opaque-'.length)) : 0
      const attachments = Array.from({ length: removed ? 63 : 64 }, (_, index) => ({ id: `row-${index}`, workspace_id: `workspace-${index}`, workspace_name: 'Same name ' + 'long-name-'.repeat(30), source_path: `/workspaces/source-${index}`, kind: 'source', attached: true, default: index === (removed ? 62 : 63), availability: 'available' }))
      const all = [...(removed ? [] : Array.from({ length: 400 }, (_, index) => ({ ...attachments[0], id: `worker-${index}`, kind: 'worker', attached: false }))), ...attachments]
      await route.fulfill({ json: { ok: true, items: all.slice(offset, offset + 20), next_cursor: offset + 20 < all.length ? `opaque-${offset + 20}` : '' } })
    })
    await page.goto('http://fixture.invalid/attachment-fixture')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: js, type: 'module' })
    await page.getByRole('button', { name: 'Workspaces: Workspaces loading incomplete', exact: true }).waitFor()
    assert.deepEqual(cursors, ['', 'opaque-20', 'opaque-40', 'opaque-60'])
    await page.getByRole('button', { name: 'Workspaces: Workspaces loading incomplete', exact: true }).click()
    await page.getByText('Available workspaces', { exact: true }).click()
    for (let window = 0; window < 5; window++) {
      const before = cursors.length
      await page.getByRole('button', { name: 'Load more workspaces', exact: true }).click()
      await page.waitForFunction(() => !document.querySelector('button[aria-haspopup="dialog"]')?.textContent?.includes('Loading'))
      assert.equal(cursors.length - before, 4)
      assert.ok(await page.locator('[data-workspace-id]').count() <= 64)
    }
    await page.getByRole('button', { name: /^Workspaces: Same name/ }).waitFor()
    assert.equal(cursors.length, 24)
    for (const width of [375, 1440]) {
      await page.setViewportSize({ width, height: 800 })
      const bounds = await page.getByRole('dialog').evaluate((dialog) => {
        const box = dialog.getBoundingClientRect()
        return { left: box.left, right: box.right, height: box.height, overflow: dialog.scrollHeight > dialog.clientHeight, horizontal: dialog.scrollWidth > dialog.clientWidth }
      })
      assert.ok(bounds.left >= 0 && bounds.right <= width && bounds.height <= 640)
      assert.equal(bounds.overflow, true)
      assert.equal(bounds.horizontal, false)
      if (process.env.SWARM_TEST_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.SWARM_TEST_SCREENSHOT_DIR}/attachments-${width}.png` })
    }
    assert.equal(await page.locator('[data-workspace-id]').count(), 64)
    assert.match(await page.locator('[data-workspace-id="workspace-63"]').innerText(), /Default/)
    removed = true
    // Token revisions must not refetch; canonical reconnect/focus does.
    const beforeRevision = cursors.length
    await page.evaluate(() => { for (let i = 0; i < 100; i++) (window as unknown as { updateAttachments(): void }).updateAttachments() })
    await page.waitForTimeout(1_100)
    assert.equal(cursors.length, beforeRevision)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await page.waitForFunction(() => document.querySelectorAll('[data-workspace-id]').length === 63)
    assert.equal(await page.locator('[data-workspace-id="workspace-63"]').count(), 0)
    assert.match(await page.locator('[data-workspace-id="workspace-62"]').innerText(), /Default/)
    forbidden = true
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await page.getByRole('status').filter({ hasText: 'Workspace list stale' }).waitFor()
    assert.equal(await page.locator('[data-workspace-id]').count(), 63)
    forbidden = false
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await page.getByRole('status').filter({ hasText: /^63 workspaces$/ }).waitFor()
    await page.keyboard.press('Escape')
    assert.equal(await page.getByRole('dialog').isVisible(), false)
  } finally { await browser.close() }
})
