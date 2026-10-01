import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: OrchestrateSettings must display Settings and derive its sole visible
// selection/content from the canonical TanStack location hash, including history
// and unknown-hash fallback. Production OrchestrateSettings + swarmPageLink + CSS
// run in a real router/browser; child panels are isolated to avoid unrelated APIs.
// This is the narrowest layer proving computed styling and keyboard focus, not
// account persistence or each child panel's controls.
test('Settings selection follows clicks, direct hashes and browser history with visible focus', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,RouterProvider} from '@tanstack/react-router';
    import {OrchestrateSettings} from './src/features/desktop/orchestrate/orchestrate-settings';
    const root=createRootRoute();
    const child=createRoute({getParentRoute:()=>root,path:'/swarm/$swarmSection',component:()=> <div className="swarm-section"><OrchestrateSettings/></div>});
    const router=createRouter({routeTree:root.addChildren([child])});
    createRoot(document.getElementById('root')).render(<RouterProvider router={router}/>);`
  const panels: Record<string, [string, string]> = {
    'auth-settings-page': ['AuthSettingsPage', 'providers'],
    'permissions-settings-page': ['PermissionsSettingsPage', 'permissions'],
    'vault-settings-page': ['VaultSettingsPage', 'vault'],
    'themes-settings-page': ['ThemesSettingsPage', 'appearance'],
    'notifications-settings-page': ['NotificationsSettingsPage', 'notifications'],
    'media-settings-page': ['MediaSettingsPage', 'media'],
  }
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent', plugins: [{ name: 'isolated-settings-panels', setup(builder) {
    builder.onResolve({ filter: /settings-page$/ }, args => {
      const name = args.path.split('/').at(-1)!
      return panels[name] ? { path: name, namespace: 'panel' } : undefined
    })
    builder.onLoad({ filter: /.*/, namespace: 'panel' }, args => {
      const [symbol, section] = panels[args.path]
      return { contents: `import React from 'react'; export const ${symbol}=()=>React.createElement('h2',null,'Panel: ${section}');`, loader: 'js', resolveDir: process.cwd() }
    })
  } }] })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://settings.test/swarm/settings#permissions')
    await page.addStyleTag({ content: await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addStyleTag({ content: ':root { --swarm-surface:#101820; --swarm-surface-hover:#203040; --swarm-border-muted:#405060; --swarm-border-accent:#80c0e0; --swarm-accent:#80c0e0; --swarm-text:#ffffff; --swarm-text-muted:#b0c0d0; } .swarm-section { height:auto; }' })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const nav = page.getByRole('navigation', { name: 'Settings sections' })
    const check = async (section: string) => {
      await page.getByRole('heading', { name: `Panel: ${section}`, exact: true }).waitFor()
      assert.equal(await page.getByRole('heading', { level: 1 }).textContent(), 'Settings')
      assert.equal(await page.getByRole('heading', { level: 2 }).count(), 1)
      assert.equal(await nav.locator('[aria-current="page"]').count(), 1)
      assert.equal(await nav.locator('[aria-current="page"]').textContent(), section)
      const active = await nav.locator('[aria-current="page"]').evaluate(el => ({ bg: getComputedStyle(el).backgroundColor, border: getComputedStyle(el).borderColor, shadow: getComputedStyle(el).boxShadow }))
      const inactive = await nav.locator('a:not([aria-current="page"])').first().evaluate(el => ({ bg: getComputedStyle(el).backgroundColor, border: getComputedStyle(el).borderColor }))
      assert.notEqual(active.bg, inactive.bg)
      assert.notEqual(active.border, inactive.border)
      assert.notEqual(active.shadow, 'none')
    }
    await check('permissions')
    for (const section of ['providers', 'permissions', 'vault', 'appearance', 'notifications', 'media']) {
      await nav.getByRole('link', { name: section, exact: true }).click()
      // Move off the link so hover cannot masquerade as active selection.
      await page.mouse.move(0, 0)
      await check(section)
    }
    await page.goBack()
    await check('notifications')
    await page.goForward()
    await check('media')
    await page.evaluate(() => { window.location.hash = 'unknown-section' })
    await check('providers')
    await page.evaluate(() => { window.location.hash = '' })
    await check('providers')
    await nav.getByRole('link', { name: 'providers', exact: true }).focus()
    await page.keyboard.press('ArrowRight')
    assert.equal(await nav.getByRole('link', { name: 'providers', exact: true }).evaluate(el => el.matches(':focus-visible') && getComputedStyle(el).outlineStyle === 'solid'), true)
    for (const width of [390, 1280]) {
      await page.setViewportSize({ width, height: 844 })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true)
    }
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
