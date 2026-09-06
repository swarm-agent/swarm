// Purpose: HTML watchdog + StartupScreen must survive unavailable bundles/CSS,
// never hand off a suspended route, and preserve storage on explicit retry.
// Authority: index.html, startup-recovery.tsx. Chromium with intercepted assets
// is the narrowest layer proving paint, React commit, browser load and navigation.
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { test } from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

const html = await readFile(new URL('../../index.html', import.meta.url), 'utf8')
const fixture = await build({
  stdin: {
    contents: `import React, { lazy } from 'react'; import { createRoot } from 'react-dom/client';
      import { StartupScreen } from './startup-recovery';
      window.__swarmStartup.started();
      const Screen = lazy(() => new Promise(resolve => { window.finishScreen = () => resolve({default: () => <h1>Ready screen</h1>}) }));
      createRoot(document.getElementById('root')).render(<React.StrictMode><StartupScreen><Screen /></StartupScreen></React.StrictMode>);`,
    loader: 'tsx', resolveDir: new URL('.', import.meta.url).pathname,
  }, bundle: true, write: false, format: 'esm', jsx: 'automatic', logLevel: 'silent',
})

test('HTML startup recovery and committed-screen handoff in Chromium', { timeout: 90000 }, async (t) => {
  // Intentionally stalled documents never reach document.fonts.ready.
  process.env.PW_TEST_SCREENSHOT_NO_FONTS_READY = '1'
  const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) })
  t.after(() => browser.close())
  const context = await browser.newContext({ viewport: { width: 1000, height: 700 } })
  let mode = 'stall'
  let navigations = 0
  let pendingStyle: import('playwright').Route | undefined
  await context.route('https://startup.invalid/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/') {
      navigations++
      const body = mode.startsWith('css') ? html.replace('</head>', '<link rel="stylesheet" href="/stalled.css"></head>') : html
      await route.fulfill({ contentType: 'text/html', body })
    } else if (path === '/src/main.tsx') {
      if (mode === 'fail') await route.abort('failed')
      else if (mode === 'stall' || mode.startsWith('css')) { /* hold request until page closes */ }
      else await route.fulfill({ contentType: 'text/javascript', body: fixture.outputFiles[0].text })
    } else if (path === '/stalled.css') {
      if (mode === 'css-fail') await route.abort('failed')
      else pendingStyle = route
    } else await route.fulfill({ status: 404, body: '' })
  })
  const page = await context.newPage()
  page.setDefaultTimeout(5000)
  await page.clock.install()
  await page.goto('https://startup.invalid/', { waitUntil: 'commit' })
  await page.locator('#swarm-startup-title').waitFor()
  assert.equal(await page.locator('#root').isVisible(), false)
  assert.equal(await page.locator('#swarm-startup-title').textContent(), 'Starting Swarm…')
  await page.clock.fastForward(20000)
  assert.match(await page.locator('#swarm-startup-message').innerText(), /app files/)
  assert.equal(navigations, 1)
  await page.evaluate(() => { localStorage.setItem('retained', 'local'); sessionStorage.setItem('retained', 'session') })
  await page.screenshot({ path: join(process.env.TMPDIR!, 'startup-recovery-desktop.png') })
  await page.setViewportSize({ width: 320, height: 568 })
  await page.screenshot({ path: join(process.env.TMPDIR!, 'startup-recovery-mobile.png') })
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true)
  mode = 'healthy'
  await page.locator('#swarm-startup-retry').click({ noWaitAfter: true })
  await page.waitForFunction(() => typeof (window as any).finishScreen === 'function')
  assert.equal(navigations, 2)
  await page.clock.fastForward(20000)
  assert.match(await page.locator('#swarm-startup-message').innerText(), /startup checks or screen/)
  assert.equal(await page.locator('#root').isVisible(), false, 'suspended React screen cannot signal readiness')
  await page.locator('#swarm-startup-retry').focus()
  await page.evaluate(() => (window as any).finishScreen())
  await page.locator('#root h1').waitFor()
  assert.equal(await page.locator('#swarm-startup').isVisible(), false)
  assert.equal(await page.evaluate(() => document.activeElement?.id), 'root')
  assert.deepEqual(await page.evaluate(() => [localStorage.getItem('retained'), sessionStorage.getItem('retained')]), ['local', 'session'])
  await page.clock.fastForward(60000)
  assert.equal(await page.locator('#swarm-startup').isVisible(), false)
  assert.equal(navigations, 2)
  await page.evaluate(() => window.dispatchEvent(new Event('vite:preloadError')))
  assert.match(await page.locator('#swarm-startup-title').innerText(), /could not load/)
  await page.evaluate(() => window.__swarmStartup?.ready())
  assert.equal(await page.locator('#swarm-startup').isVisible(), true, 'late ready cannot erase fatal failure')
  await page.close()

  for (const failure of ['fail', 'css-stall', 'css-fail']) {
    mode = failure
    const failed = await context.newPage()
    await failed.clock.install()
    await failed.goto('https://startup.invalid/', { waitUntil: 'commit' })
    await failed.waitForFunction(() => Boolean(document.getElementById('swarm-startup')))
    if (failure === 'css-stall') await failed.clock.fastForward(20000)
    else await failed.waitForFunction(() => document.getElementById('swarm-startup-title')?.textContent === 'Swarm could not load')
    await failed.locator('#swarm-startup-retry').waitFor({ state: 'visible' })
    assert.equal(await failed.locator('#root').isVisible(), false)
    if (failure === 'css-stall') {
      assert.equal(await failed.locator('link[rel="stylesheet"]').getAttribute('media'), 'not all')
      await failed.screenshot({ path: join(process.env.TMPDIR!, 'startup-recovery-css.png') })
      await failed.evaluate(() => {
        document.getElementById('root')!.textContent = 'Recovered screen'
        window.__swarmStartup?.ready()
      })
      assert.equal(await failed.locator('#root').isVisible(), false, 'pending CSS must block app handoff')
      assert.ok(pendingStyle)
      await pendingStyle.fulfill({ contentType: 'text/css', body: '#root { color: white; background: black; }' })
      await failed.locator('#root').waitFor({ state: 'visible' })
      assert.equal(await failed.locator('link[rel="stylesheet"]').getAttribute('media'), '')
      assert.equal(await failed.locator('#swarm-startup').isVisible(), false)
    }
    await failed.close()
  }
  mode = 'healthy'
  const healthy = await context.newPage()
  await healthy.clock.install()
  await healthy.goto('https://startup.invalid/', { waitUntil: 'domcontentloaded' })
  await healthy.waitForFunction(() => typeof (window as any).finishScreen === 'function')
  await healthy.evaluate(() => (window as any).finishScreen())
  await healthy.locator('#root h1').waitFor()
  await healthy.clock.fastForward(60000)
  assert.equal(await healthy.locator('#swarm-startup').isVisible(), false, 'healthy commit cancels watchdog')
  await healthy.close()
  const noScriptContext = await browser.newContext({ javaScriptEnabled: false })
  const noScript = await noScriptContext.newPage()
  await noScript.route('https://startup.invalid/**', route => route.fulfill({ contentType: 'text/html', body: html }))
  await noScript.goto('https://startup.invalid/', { waitUntil: 'domcontentloaded' })
  assert.match(await noScript.locator('noscript').innerText(), /needs JavaScript/)
  assert.equal(await noScript.locator('#swarm-startup-retry').isVisible(), true)
  await noScriptContext.close()
})
