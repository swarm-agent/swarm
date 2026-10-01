import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'vite'
import { chromium } from 'playwright'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Purpose: exercise the real gallery/rendering/edit boundary with deterministic HTTP fixtures.
// After two revisions, historical edits must retain their exact base, preview HTML must stay
// sandboxed, and mobile modal controls must remain reachable without overlapping content.
// This is a hermetic component regression test, not provider-backed acceptance.
test('gallery preserves historical edit bases, sandbox, modal layout and session isolation', { timeout: 60_000 }, async () => {
  const result = await build({ configFile: false, logLevel: 'error', plugins: [react(), tailwindcss(), {
    name: 'design-fixture',
    resolveId(id) { if (id === 'virtual:design-fixture') return '\0design-fixture' },
    load(id) { if (id === '\0design-fixture') return `
      import React from 'react';
      import {createRoot} from 'react-dom/client';
      import {DesktopDesignGallery} from '${process.cwd()}/src/features/desktop/chat/components/desktop-design-gallery.tsx';
      import '${process.cwd()}/src/theme.css';
      const root = createRoot(document.getElementById('root'));
      window.showSession = sessionId => root.render(React.createElement(DesktopDesignGallery, {sessionId, key:sessionId}));
      window.showSession('first');
    ` },
  }], build: { write: false, minify: false, rolldownOptions: { input: 'virtual:design-fixture', output: { inlineDynamicImports: true } } } })
  assert.ok(!Array.isArray(result) && 'output' in result)
  const js = result.output.filter(item => item.type === 'chunk').map(item => item.code).join('\n')
  const css = result.output.filter(item => item.type === 'asset' && item.fileName.endsWith('.css')).map(item => String(item.source)).join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const ref = (revision: number) => ({ artifact_id: 'design', revision, sha256: `hash-${revision}` })
    const edits: unknown[] = []
    let count = 1
    const wrapper = '<!doctype html><p>Stored capture</p><script>parent.__unsafeDesign = true</script>'
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.pathname === '/fixture') { await route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }); return }
      if (url.pathname === '/v3/sessions/second/designs') { await route.fulfill({ json: { requests: [], next_cursor: '' } }); return }
      if (url.pathname === '/v3/sessions/first/designs') {
        await route.fulfill({ json: { requests: [{ id: 'private-first', state: 'succeeded', candidates: [{ spec: { artifact_id: 'design', kind: 'html' }, state: 'succeeded' }] }], next_cursor: '' } }); return
      }
      if (url.pathname === '/v3/sessions/first/designs/artifacts/design') {
        if (route.request().method() === 'GET') {
          await route.fulfill({ json: { artifact: { id: 'design', kind: 'html', revision_count: count, selection_version: 0 }, revisions: Array.from({ length: count }, (_, i) => ({ ref: ref(i + 1), kind: 'html', attempt: { number: 1, state: 'succeeded' }, ...(i ? { base: ref(i) } : {}) })) } }); return
        }
        const body = route.request().postDataJSON()
        if (body.action === 'preview_html') { await route.fulfill({ contentType: 'text/html', body: wrapper }); return }
        if (body.action === 'edit') { edits.push(body.ref); count++; await route.fulfill({ json: { accepted: true } }); return }
      }
      await route.abort()
    })
    await page.goto('http://fixture.invalid/fixture')
    await page.addStyleTag({ content: css }); await page.addScriptTag({ content: js, type: 'module' })
    await page.getByRole('button', { name: 'Delegated designs (1)', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Delegated designs' })
    assert.equal(await dialog.getAttribute('aria-modal'), 'true')
    await page.getByRole('button', { name: 'Variant 1 · html' }).click()
    await page.getByRole('button', { name: 'Revision 1 · original' }).click()
    for (let i = 1; i <= 2; i++) {
      await page.getByLabel(`Edit this exact revision (${i})`).fill(`Edit ${i}`)
      await page.getByRole('button', { name: 'Request delegated edit' }).click()
      await page.getByRole('button', { name: `Revision ${i + 1} ← ${i}`, exact: true }).waitFor()
      await page.getByRole('button', { name: `Revision ${i + 1} ← ${i}`, exact: true }).click()
    }
    await page.getByRole('button', { name: 'Revision 1 · original' }).click()
    await page.getByLabel('Edit this exact revision (1)').fill('Branch from original')
    await page.getByRole('button', { name: 'Request delegated edit' }).click()
    await page.getByRole('status').filter({ hasText: 'Edit requested from revision 1' }).waitFor()
    assert.deepEqual(edits, [ref(1), ref(2), ref(1)])
    const preview = page.locator('iframe[title="Design revision 1"]')
    await preview.waitFor()
    assert.equal(await preview.getAttribute('sandbox'), '')
    assert.equal(await preview.getAttribute('srcdoc'), wrapper)
    assert.equal(await page.evaluate(() => '__unsafeDesign' in window), false)
    for (const width of [375, 1440]) {
      await page.setViewportSize({ width, height: 800 })
      const bounds = await dialog.evaluate(element => {
        const box = element.getBoundingClientRect()
        const header = element.querySelector('header')!.getBoundingClientRect()
        const content = element.querySelector('nav')!.parentElement!.getBoundingClientRect()
        return { left: box.left, right: box.right, bottom: box.bottom, overlap: header.bottom > content.top, overflow: element.scrollWidth > element.clientWidth }
      })
      assert.ok(bounds.left >= 0 && bounds.right <= width && bounds.bottom <= 800)
      assert.equal(bounds.overlap, false); assert.equal(bounds.overflow, false)
    }
    await page.getByRole('button', { name: 'Close', exact: true }).focus()
    await page.keyboard.press('Escape')
    assert.equal(await dialog.count(), 0)
    await page.evaluate(() => (window as unknown as { showSession(id: string): void }).showSession('second'))
    await page.getByRole('button', { name: 'Delegated designs (0)', exact: true }).click()
    await page.getByText('No delegated requests yet.').waitFor()
    assert.equal(await page.getByText('Request private-first', { exact: true }).count(), 0)
    assert.equal(await page.locator('iframe').count(), 0)
  } finally { await browser.close() }
})
