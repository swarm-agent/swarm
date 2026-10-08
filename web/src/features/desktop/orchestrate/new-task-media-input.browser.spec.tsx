// Purpose: New Task must expose paste/paperclip media, block pending/failed
// uploads, and preserve its draft on rejection. OrchestrateView owns these
// transitions. A real browser with controlled HTTP responses is the narrowest
// layer proving React rendering/event integration; not live daemon/provider E2E.
import test from 'node:test'
import assert from 'node:assert/strict'
import { EventEmitter, once } from 'node:events'
import { build } from 'esbuild'
import { chromium, type Route } from 'playwright'
import { fixtureRead, project, snapshot } from './swarm-responsive-browser-fixtures'

test('New Task renders paste and video intake, upload guards, retry and rejection without media loss', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import{mountResponsiveFixture}from'./src/features/desktop/orchestrate/swarm-responsive-browser-fixtures';mountResponsiveFixture('populated', true);` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } })
    page.setDefaultTimeout(10000)
    page.on('pageerror', error => console.error(error.message))
    const uploads: { route: Route; body: any }[] = []
    const submissions: any[] = []
    const retained: any[] = []
    const events = new EventEmitter()
    const waitForCount = async (items: unknown[], count: number) => {
      while (items.length < count) await once(events, 'request', { signal: AbortSignal.timeout(10000) })
      assert.equal(items.length, count)
    }
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      if (url.pathname === `/v3/projects/${project.id}/media`) {
        if (req.method() === 'POST') { uploads.push({ route, body: req.postDataJSON() }); events.emit('request'); return }
        if (req.method() === 'GET') return route.fulfill({ json: { media: retained } })
      }
      if (url.pathname === `/v3/projects/${project.id}/tasks` && req.method() === 'POST') {
        submissions.push({ body: req.postDataJSON(), key: req.headers()['idempotency-key'] })
        events.emit('request')
        return route.fulfill({ status: 400, json: { error: 'Unsupported video: attach image frames; model settings have not been changed' } })
      }
      const data = req.method() === 'GET' ? fixtureRead(url, 'populated') : undefined
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Unconfigured fixture request' } })
    })
    await page.goto(`https://media.test/projects/${project.id}`)
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByRole('button', { name: /^New task$/i }).first().click()
    const modal = page.locator('.swarm-local-dialog')
    const prompt = modal.locator('textarea').first()
    await prompt.fill('Explain the image and video')
    const create = modal.getByRole('button', { name: /Create|Deploy|Submit/ }).last()
    // Items-only clipboard is the regression shape; actual File/FileReader and
    // React paste handling run in Chromium rather than extracting a handler.
    await prompt.evaluate(element => {
      const data = new DataTransfer()
      data.items.add(new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], 'clipboard.png', { type: 'image/png' }))
      const event = new ClipboardEvent('paste', { bubbles: true, cancelable: true })
      Object.defineProperty(event, 'clipboardData', { value: { files: [], items: data.items, getData: data.getData.bind(data) } })
      element.dispatchEvent(event)
    })
    await modal.getByRole('status').getByText(/Uploading attachments/).waitFor()
    assert.equal(await create.isDisabled(), true)
    assert.equal(submissions.length, 0)
    await waitForCount(uploads, 1)
    assert.match(uploads[0].body.url, /^data:image\/png;base64,/)
    const image = { id: 'retained-image', kind: 'image', title: 'clipboard.png', filename: 'clipboard.png', url: '/fixture/image', digestSha256: 'image-digest' }
    retained.push(image)
    await uploads[0].route.fulfill({ status: 201, json: { media: image } })
    await modal.getByText('Attached Media (1):', { exact: true }).waitFor()
    const input = modal.getByLabel('Upload attachment')
    await input.setInputFiles({ name: 'clip.mp4', mimeType: 'video/mp4', buffer: Buffer.from('video-fixture') })
    await waitForCount(uploads, 2)
    assert.equal(await input.inputValue(), '', 'file input permits selecting the same file again')
    assert.equal(await create.isDisabled(), true)
    assert.equal(uploads[1].body.kind, 'video')
    await uploads[1].route.fulfill({ status: 503, json: { error: 'Retention unavailable' } })
    await modal.getByRole('alert').getByText(/Retention unavailable/).waitFor()
    await modal.getByText('Not attached: clip.mp4', { exact: true }).waitFor()
    assert.equal(await create.isDisabled(), true)
    await modal.getByRole('button', { name: 'Retry failed attachments', exact: true }).click()
    await waitForCount(uploads, 3)
    const video = { id: 'retained-video', kind: 'video', title: 'clip.mp4', filename: 'clip.mp4', url: '/fixture/video', digestSha256: 'video-digest' }
    retained.push(video)
    await uploads[2].route.fulfill({ status: 201, json: { media: video } })
    await modal.getByText('Attached Media (2):', { exact: true }).waitFor()
    assert.equal(await create.isEnabled(), true)
    await create.click()
    await modal.getByText(/Unsupported video: attach image frames/).waitFor()
    assert.equal(await prompt.inputValue(), 'Explain the image and video')
    await modal.getByText('Attached Media (2):', { exact: true }).waitFor()
    await create.click()
    await waitForCount(submissions, 2)
    assert.deepEqual(submissions[0], submissions[1], 'retry preserves request identity and media')
    assert.deepEqual(submissions[0].body.attached_media, [image, video])
    assert.equal(submissions[0].body.prompt, 'Explain the image and video')
    assert.equal(submissions[0].body.model, undefined)
    // Explicit discard is the only route to continue after another failed file.
    await input.setInputFiles({ name: 'bad.png', mimeType: 'image/png', buffer: Buffer.from('invalid') })
    await waitForCount(uploads, 4)
    await uploads[3].route.fulfill({ status: 400, json: { error: 'Invalid image bytes' } })
    await modal.getByText('Not attached: bad.png', { exact: true }).waitFor()
    await modal.getByRole('button', { name: 'Discard failed attachments', exact: true }).click()
    assert.equal(await create.isEnabled(), true)
    assert.equal(await modal.getByText('Not attached: bad.png', { exact: true }).count(), 0)
    assert.equal(submissions.length, 2)
  } finally { await browser.close() }
})

