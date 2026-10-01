import assert from 'node:assert/strict'
import test from 'node:test'
import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import { build } from 'vite'
import { chromium } from 'playwright'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Purpose: render the production Media Center, request card and viewer with hermetic HTTP
// fixtures. This is the narrowest browser boundary proving one-click exact previews,
// no retired gallery, sandbox/download separation, historical edits and visible CAS
// failure without selection-on-browse. Stored-preview thumbnails must be sandboxed
// and noninteractive so ready outputs remain one-click targets. This is not provider evidence.
test('Media Center opens independent designs directly and preserves historical edit authority', { timeout: 60_000 }, async () => {
  const result = await build({ configFile: false, logLevel: 'error', plugins: [react(), tailwindcss(), {
    name: 'design-media-fixture',
    resolveId(id) { if (id === 'virtual:design-media') return '\0design-media' },
    load(id) { if (id === '\0design-media') return `
      import React from 'react'; import {createRoot} from 'react-dom/client';
      import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
      import {HistoricalMediaLibrary} from '${process.cwd()}/src/features/desktop/tools/media-library/historical-media-library.tsx';
      import {desktopDesigns, acceptDesktopDesignEvent} from '${process.cwd()}/src/features/desktop/runtime/desktop-design-runtime.ts';
      import '${process.cwd()}/src/theme.css';
      const root = createRoot(document.getElementById('root')); const client = new QueryClient({defaultOptions:{queries:{retry:false}}});
      window.showProject = projectId => root.render(React.createElement(QueryClientProvider,{client},React.createElement(HistoricalMediaLibrary,{projectId,key:projectId})));
      window.refreshDesigns = () => acceptDesktopDesignEvent({kind:'project.updated', project_id:'first'});
      window.showProject('first');
    ` },
  }], build: { write: false, minify: false, rolldownOptions: { input: 'virtual:design-media', output: { inlineDynamicImports: true } } } })
  assert.ok(!Array.isArray(result) && 'output' in result)
  const js = result.output.filter(item => item.type === 'chunk').map(item => item.code).join('\n')
  const css = result.output.filter(item => item.type === 'asset' && item.fileName.endsWith('.css')).map(item => String(item.source)).join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  const page = await browser.newPage(); page.setDefaultTimeout(7000)
  const evidenceDir = process.env.SWARM_TEST_SCREENSHOT_DIR
  if (evidenceDir) await mkdir(evidenceDir, { recursive: true })
  try {
    const ref = (revision: number) => ({ artifact_id: 'design', revision, sha256: `hash-${revision}` })
    const revisions = [1, 2, 3].map(n => ({ ref: ref(n), kind: 'html', request_id: 'request', candidate: 0, attempt: { number: 1, state: 'succeeded', result: ref(n) }, ...(n > 1 ? { base: ref(1) } : {}) }))
    const actions: Record<string, unknown>[] = []; const messages: unknown[] = []
    let accepted = false; let editKey = ''
    const wrapper = '<!doctype html><p>Stored capture</p><script>parent.__unsafeDesign=true</script>'
    const row = { project_id: 'first', title: 'Landing page', request: { id: 'request', parent_session_id: 'session', state: 'partial_success', candidates: [
      { spec: { artifact_id: 'design', kind: 'html' }, state: 'succeeded', attempts: [revisions[2].attempt] },
      { spec: { artifact_id: 'failed', kind: 'html' }, state: 'failed', failure_reason: 'validation_failed', router_alert: 'Model configuration warning' },
      { spec: { artifact_id: 'queued', kind: 'html' }, state: 'queued' },
    ] } }
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.pathname === '/fixture') return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      if (url.pathname === '/v1/auth/desktop/session') return route.fulfill({ json: { user_id: 'fixture', account_scope_id: 'fixture' } })
      if (url.pathname === '/v3/projects/second/designs') return route.fulfill({ json: { designs: [], next_cursor: '' } })
      if (url.pathname === '/v3/projects/first/designs') return route.fulfill({ json: { designs: [row, ...(accepted ? [{ ...row, title: 'Edit landing page', request: { ...row.request, id: 'edit', source_message_id: 'edit-message', client_request_id: editKey, state: 'running', candidates: [{ spec: { artifact_id: 'design', kind: 'html', base: ref(1) }, state: 'running' }] } }] : [])], next_cursor: '' } })
      if (url.pathname === '/v3/sessions/session/messages') return route.fulfill({ json: { messages, has_more_older: false } })
      if (url.pathname === '/v3/sessions/session/designs/artifacts/design') {
        if (route.request().method() === 'GET') return route.fulfill({ json: { artifact: { id: 'design', kind: 'html', revision_count: 3, selection_version: 4, selected: ref(2) }, revisions } })
        const body = route.request().postDataJSON(); actions.push(body)
        if (body.action === 'preview_html') return route.fulfill({ contentType: 'text/html', body: wrapper })
        if (body.action === 'select') return route.fulfill({ status: 409, body: 'stale selection' })
        if (body.action === 'download') return route.fulfill({ contentType: 'text/html', body: '<script>unsafe authored bytes</script>' })
        if (body.action === 'edit') {
          editKey = body.idempotency_key
          messages.push({ id: 'edit-message', session_id: 'session', role: 'user', content: 'Branch', metadata: { design_edit_request: { client_request_id: editKey, base: body.ref, state: 'requested' } } })
          return route.fulfill({ json: { ok: true, message_id: 'edit-message' } })
        }
      }
      if (url.pathname === '/v3/artifacts') return route.fulfill({ json: { ok: true, artifacts: [] } })
      if (url.pathname.includes('catalog')) return route.fulfill({ json: { ok: true, artifacts: [], image_models: [], video_models: [] } })
      if (url.pathname.includes('settings')) return route.fulfill({ json: {} })
      return route.fulfill({ json: { artifacts: [] } })
    })
    await page.goto('http://localhost/fixture'); await page.addStyleTag({ content: css }); await page.addScriptTag({ content: js, type: 'module' })
    const readyOutput = page.getByRole('button', { name: 'Preview Landing page candidate 1', exact: true })
    const thumbnail = readyOutput.locator('iframe')
    await thumbnail.waitFor()
    assert.equal(await thumbnail.getAttribute('sandbox'), '')
    assert.equal(await thumbnail.evaluate(element => getComputedStyle(element).pointerEvents), 'none')
    await readyOutput.click()
    const dialog = page.getByRole('dialog')
    await dialog.locator('iframe[title="Design revision 3"]').waitFor()
    assert.equal(await page.getByRole('button', { name: /Delegated designs/ }).count(), 0)
    assert.equal(actions.some(action => action.action === 'select'), false)
    assert.equal(await dialog.getByRole('button', { name: 'Tag media for task' }).count(), 0)
    await dialog.getByRole('button', { name: 'Revision 1 · original', exact: true }).click()
    const preview = dialog.locator('iframe[title="Design revision 1"]'); await preview.waitFor()
    assert.equal(await preview.getAttribute('sandbox'), '')
    assert.equal(await page.evaluate(() => '__unsafeDesign' in window), false)
    await dialog.getByLabel('Edit this exact revision (1)').fill('Branch')
    await dialog.getByRole('button', { name: 'Request delegated edit', exact: true }).click()
    await dialog.getByText('Edit requested from revision 1; awaiting parent acceptance.').waitFor()
    assert.deepEqual(actions.find(action => action.action === 'edit')?.ref, ref(1))
    accepted = true
    await page.evaluate(() => (window as unknown as { refreshDesigns(): void }).refreshDesigns())
    await dialog.getByText('Edit from revision 1: accepted · running', { exact: true }).waitFor()
    await dialog.getByRole('button', { name: 'Select this revision' }).click()
    await dialog.getByRole('alert').filter({ hasText: '409' }).waitFor()
    const selection = actions.find(action => action.action === 'select')!
    assert.deepEqual(selection.expected_current, ref(2)); assert.equal(selection.expected_version, 4)
    assert.equal(actions.filter(action => action.action === 'select').length, 1)
    await dialog.getByRole('button', { name: 'Prepare HTML download' }).click()
    await dialog.getByRole('link', { name: 'Download exact revision' }).waitFor()
    assert.equal(await preview.getAttribute('srcdoc'), wrapper)
    for (const width of [375, 1440]) {
      await page.setViewportSize({ width, height: 800 })
      const bounds = await dialog.evaluate(element => {
        const box = element.getBoundingClientRect()
        return { left: box.left, right: box.right, bottom: box.bottom, overflow: element.scrollWidth > element.clientWidth }
      })
      assert.ok(bounds.left >= 0 && bounds.right <= width && bounds.bottom <= 800)
      assert.equal(bounds.overflow, false)
      await dialog.getByRole('button', { name: 'Close viewer' }).scrollIntoViewIfNeeded()
      await dialog.getByRole('heading', { name: 'Viewing revision 1', exact: true }).scrollIntoViewIfNeeded()
      if (evidenceDir) await page.screenshot({ path: resolve(evidenceDir, `design-viewer-${width}.png`) })
    }
    await dialog.getByRole('button', { name: 'Close viewer' }).click()
    await page.getByRole('button', { name: 'View Landing page · Candidate 1 · Revision 3', exact: true }).click()
    await page.getByRole('dialog').locator('iframe[title="Design revision 3"]').waitFor()
    await page.getByRole('dialog').getByText('Edit from revision 1: accepted · running', { exact: true }).waitFor()
    await page.getByRole('button', { name: 'Close viewer' }).click()
    for (const width of [375, 1440]) {
      await page.setViewportSize({ width, height: 800 })
      await page.getByRole('button', { name: 'Preview Landing page candidate 1', exact: true }).scrollIntoViewIfNeeded()
      if (evidenceDir) await page.screenshot({ path: resolve(evidenceDir, `design-library-${width}.png`) })
    }
    await page.evaluate(() => (window as unknown as { showProject(id: string): void }).showProject('second'))
    assert.equal(await page.locator('iframe').count(), 0)
    await page.getByText('No media artifacts found').waitFor()
    assert.equal(await page.getByText('Landing page', { exact: true }).count(), 0)
  } catch (error) {
    console.error((await page.locator('body').innerText()).slice(0, 6000))
    if (evidenceDir) await page.screenshot({ path: resolve(evidenceDir, 'design-failure.png') })
    throw error
  } finally { await browser.close() }
})
