import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { compile } from 'tailwindcss'

// Purpose: DesktopSettingsPage and OrchestrateSettings must expose the existing
// MemoryModal at desktop/mobile widths without an ambient checkout. The actual
// router, modal and production CSS prove click/keyboard close, focus restoration,
// /memory deep-link exit, map/requested views and no accidental mutations. Only
// unrelated Settings panels and HTTP responses are isolated; this is the
// narrowest browser boundary for accessibility, not backend authorization proof.
test('Settings exposes account Memory at desktop/mobile widths and preserves deep links', { timeout: 60000 }, async () => {
  const fixture = `import React from 'react';import{createRoot}from'react-dom/client';
    import{createRootRoute,createRoute,createRouter,RouterProvider}from'@tanstack/react-router';
    import{DesktopSettingsPage}from'./src/features/desktop/settings/components/desktop-settings-page';
    import{OrchestrateSettings}from'./src/features/desktop/orchestrate/orchestrate-settings';
    const root=createRootRoute();
    const settings=createRoute({getParentRoute:()=>root,path:'/settings',component:DesktopSettingsPage});
    const scoped=createRoute({getParentRoute:()=>root,path:'/$workspaceSlug/settings',component:DesktopSettingsPage});
    const memory=createRoute({getParentRoute:()=>root,path:'/memory',component:()=> <DesktopSettingsPage initialMemoryOpen/>});
    const executive=createRoute({getParentRoute:()=>root,path:'/swarm/$swarmSection',component:OrchestrateSettings});
    const home=createRoute({getParentRoute:()=>root,path:'/',component:()=> <h1>Launcher</h1>});
    const router=createRouter({routeTree:root.addChildren([settings,scoped,memory,executive,home])});
    createRoot(document.getElementById('root')).render(<RouterProvider router={router}/>);`
  const panels: Record<string, string> = {
    'account-settings-page': 'AccountSettingsPage', 'behavior-settings-page': 'BehaviorSettingsPage',
    'media-settings-page': 'MediaSettingsPage', 'auth-settings-page': 'AuthSettingsPage',
    'permissions-settings-page': 'PermissionsSettingsPage', 'notifications-settings-page': 'NotificationsSettingsPage',
    'themes-settings-page': 'ThemesSettingsPage', 'shortcuts-settings-page': 'ShortcutsSettingsPage',
    'vault-settings-page': 'VaultSettingsPage', 'worktree-settings-page': 'WorktreeSettingsPage',
    'tailscale-settings-page': 'TailscaleSettingsPage', 'actions-settings-page': 'ActionsSettingsPage',
    'cloud-settings-page': 'CloudSettingsPage',
  }
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent', plugins: [{ name: 'isolated-settings-panels', setup(builder) {
    builder.onResolve({ filter: /settings-page$/ }, args => {
      const name = args.path.split('/').at(-1)!
      return panels[name] ? { path: name, namespace: 'panel' } : undefined
    })
    builder.onLoad({ filter: /.*/, namespace: 'panel' }, args => ({ contents: `import React from 'react';export const ${panels[args.path]}=()=>React.createElement('h2',null,'Settings panel');`, loader: 'js' }))
  } }] })
  const require = createRequire(import.meta.url)
  const compiler = await compile(await readFile('src/theme.css', 'utf8'), { base: path.resolve('src'), loadStylesheet: async (id, base) => {
    const file = id.startsWith('.') ? path.resolve(base, id) : require.resolve(id === 'tailwindcss' ? 'tailwindcss/index.css' : id)
    return { path: file, base: path.dirname(file), content: await readFile(file, 'utf8') }
  } })
  const sources = await Promise.all([
    'src/features/desktop/settings/components/desktop-settings-page.tsx',
    'src/features/desktop/orchestrate/orchestrate-settings.tsx',
    'src/features/desktop/memory/memory-page.tsx',
    'src/components/ui/dialog.tsx', 'src/components/ui/button.tsx', 'src/components/ui/select.tsx',
  ].map(file => readFile(file, 'utf8')))
  const css = compiler.build(sources.join(' ').split(/[\s'"`]+/))
  const browser = await chromium.launch({ headless: true, timeout: 10000, ...(process.env.SWARM_TEST_BROWSER ? { executablePath: process.env.SWARM_TEST_BROWSER } : {}) })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    const mutations: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (route.request().method() !== 'GET') mutations.push(url.pathname)
      if (url.pathname === '/v1/auth/desktop/session') {
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ user_id: 'test-user', account_scope_id: 'test-account' }) })
      } else if (url.pathname === '/v1/memory') {
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ revision: 1, entries: [] }) })
      } else if (url.pathname.startsWith('/v1/')) {
        await route.abort()
      } else {
        await route.fulfill({ contentType: 'text/html', body: '<html><body><div id="root"></div></body></html>' })
      }
    })
    const mount = async (route: string) => {
      await page.goto(`https://settings.test${route}`)
      await page.addStyleTag({ content: css })
      await page.addScriptTag({ content: bundle.outputFiles.find(file => file.path.endsWith('.js'))?.text ?? bundle.outputFiles[0].text })
    }
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 })
      for (const route of ['/settings', '/test-workspace/settings', '/swarm/settings']) {
        await mount(route)
        const entry = page.getByRole('button', { name: 'Memory', exact: true })
        await entry.waitFor({ state: 'visible' })
        assert.equal(await entry.count(), 1)
        assert.equal(await entry.getAttribute('aria-haspopup'), 'dialog')
        await entry.click()
        const modal = page.getByRole('dialog', { name: 'Memory', exact: true })
        await modal.waitFor({ state: 'visible' })
        await page.getByText('No memories yet', { exact: true }).waitFor()
        assert.match(await modal.innerText(), /Orchestrator only, not Swarm or workers/)
        assert.equal(await modal.getByRole('button', { name: 'Your requested memories', exact: true }).getAttribute('aria-pressed'), 'true')
        assert.equal(await modal.getByRole('button', { name: 'Workspace map', exact: true }).isVisible(), true)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true)
        const box = await modal.locator('.memory-modal').boundingBox()
        assert.ok(box && box.x >= 0 && box.x + box.width <= width)
        await page.keyboard.press('Escape')
        await modal.waitFor({ state: 'detached' })
        assert.equal(new URL(page.url()).pathname, route)
        assert.equal(await entry.evaluate(el => el === document.activeElement), true)
        await entry.click()
        await page.getByRole('button', { name: 'Close memory', exact: true }).click()
        await modal.waitFor({ state: 'detached' })
      }
      await mount('/memory')
      await page.getByRole('dialog', { name: 'Memory', exact: true }).waitFor({ state: 'visible' })
      await page.getByRole('button', { name: 'Close memory', exact: true }).click()
      await page.waitForURL('**/settings')
      await page.getByRole('dialog').waitFor({ state: 'detached' })
      await page.getByRole('button', { name: 'Memory', exact: true }).click()
      await page.getByRole('dialog', { name: 'Memory', exact: true }).waitFor({ state: 'visible' })
    }
    assert.deepEqual(mutations, [])
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
