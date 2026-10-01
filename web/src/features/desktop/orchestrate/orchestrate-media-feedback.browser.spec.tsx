import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { fixtureRead, project, snapshot } from './swarm-responsive-browser-fixtures'

// Requirement: persisted project shelf failures stay visible and never fabricate
// saved media. Authority is OrchestrateView's project-media POST/DELETE handlers,
// distinct from session composer uploads. Rendered HTTP-boundary failure tests
// prove retained input and unchanged shelf; not live backend/provider evidence.
test('shelf rejects malformed upload and paste responses and reports delete failure', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import{mountResponsiveFixture}from'./src/features/desktop/orchestrate/swarm-responsive-browser-fixtures';mountResponsiveFixture('populated');` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    const media = { id: 'retained-doc', title: 'Retained document', kind: 'doc', data: 'Retained text', filename: 'retained.md' }
    const writes: string[] = []
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100vh"></div>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      if (req.method() !== 'GET') {
        writes.push(`${req.method()} ${url.pathname}`)
        return route.fulfill({ status: req.method() === 'DELETE' ? 503 : 200, json: req.method() === 'DELETE' ? { error: 'Delete unavailable' } : {} })
      }
      if (url.pathname === `/v3/projects/${project.id}/media`) return route.fulfill({ json: { media: [media] } })
      const data = fixtureRead(url, 'populated')
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Unconfigured fixture read' } })
    })
    await page.goto('https://media.test/fixture/swarm')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const shelf = page.getByRole('complementary', { name: 'Swarm navigation' })
    await shelf.getByText('Retained document', { exact: true }).waitFor()
    await shelf.getByTitle('Remove uploaded media').click()
    await shelf.getByRole('alert').getByText(/Delete unavailable/).waitFor()
    assert.equal(await shelf.getByText('Retained document', { exact: true }).count(), 1)
    await shelf.locator('input[type=file]').setInputFiles({ name: 'failed.md', mimeType: 'text/markdown', buffer: Buffer.from('Task-only document') })
    await shelf.getByRole('alert').getByText(/no persisted media/).waitFor()
    assert.equal(await shelf.getByText('failed.md', { exact: true }).count(), 0)
    await shelf.getByTitle('Paste Markdown / Text Document').click()
    const content = page.getByPlaceholder('Paste Markdown spec, architecture guidelines, or text here...')
    await content.fill('Keep this unsaved document')
    await page.getByRole('button', { name: 'Save & Tag Document' }).click()
    await page.locator('.swarm-local-dialog').getByRole('alert').getByText(/no persisted document/).waitFor()
    assert.equal(await content.inputValue(), 'Keep this unsaved document')
    assert.ok(writes.filter(w => w === `POST /v3/projects/${project.id}/media`).length === 2)
    assert.equal(writes.some(w => w.includes('/v3/sessions/')), false)
    assert.equal(await shelf.getByText(/To attach to chat, use the composer paperclip/).count(), 1)
  } finally { await browser.close() }
})
