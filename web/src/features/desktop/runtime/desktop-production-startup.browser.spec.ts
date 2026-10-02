import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { build, preview } from 'vite'
import { chromium } from 'playwright'

// Purpose: the actual production entry and Vite chunk graph must initialize before
// DesktopDesignState is used. Eager construction previously crashed with "not a
// constructor" despite a successful build. Only executing emitted chunks in a real
// browser proves this boundary; no module substitutions or component-only bundles.
// HTTP admission fixtures deliberately stop at the locked vault: this proves boot,
// not daemon authentication, Designer execution, or provider-backed acceptance.
test('production Desktop chunks boot to the locked vault without initialization errors', { timeout: 120_000 }, async t => {
  assert.ok(process.env.TMPDIR, 'caller must provide a scratch TMPDIR')
  const root = fileURLToPath(new URL('../../../../', import.meta.url))
  const outDir = await mkdtemp(join(process.env.TMPDIR, 'swarm-production-startup-'))
  t.after(() => rm(outDir, { recursive: true, force: true }))
  const configFile = join(root, 'vite.config.ts')
  await build({ root, configFile, logLevel: 'error', build: { outDir, emptyOutDir: true } })
  const server = await preview({
    root, configFile, logLevel: 'error', build: { outDir },
    preview: { host: '127.0.0.1', port: 0, strictPort: false, proxy: {} },
  })
  t.after(() => new Promise<void>((resolve, reject) => {
    server.httpServer.closeAllConnections()
    server.httpServer.close(error => error ? reject(error) : resolve())
  }))
  const address = server.httpServer.address()
  assert.ok(address && typeof address !== 'string')
  const origin = `http://127.0.0.1:${address.port}`
  const browser = await chromium.launch({
    headless: true, timeout: 15_000,
    ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
      : { channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined }),
  })
  t.after(() => browser.close())
  const context = await browser.newContext({ serviceWorkers: 'block' })
  const unexpected: string[] = []
  const fixtures: Record<string, unknown> = {
    '/v1/auth/desktop/session': { user_id: 'fixture-user', account_scope_id: 'fixture-account' },
    '/v1/onboarding/tailscale-origin': { required: false },
    '/v1/onboarding': { ok: true, needs_onboarding: false, vault: { enabled: true, unlocked: false } },
    '/v1/vault': { enabled: true, unlocked: false },
  }
  await context.route('**/*', async route => {
    const url = new URL(route.request().url())
    if (url.origin !== origin) { unexpected.push(url.origin); await route.abort(); return }
    if (Object.hasOwn(fixtures, url.pathname)) { await route.fulfill({ json: fixtures[url.pathname] }); return }
    if (/^\/v[123]\//.test(url.pathname)) { unexpected.push(url.pathname); await route.abort(); return }
    await route.continue()
  })
  const page = await context.newPage()
  page.setDefaultTimeout(15_000)
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(origin, { waitUntil: 'load', timeout: 20_000 })
  try {
    await page.getByRole('button', { name: /unlock/i }).first().waitFor()
  } catch (error) {
    assert.fail(`Production boot did not reach vault: ${errors.join('; ')}; ${await page.locator('body').innerText()}; ${String(error)}`)
  }
  assert.deepEqual(errors, [], 'production module graph must not throw')
  assert.deepEqual(unexpected, [], 'only explicit local admission fixtures may be requested')
  assert.equal(await page.getByText('Swarm could not load', { exact: true }).isVisible(), false)
  assert.equal(await page.locator('#swarm-startup').isVisible(), false)
})
