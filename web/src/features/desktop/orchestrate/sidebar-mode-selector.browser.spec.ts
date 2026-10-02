import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'

// Purpose: SidebarModeSelector must expose exactly one current mode even when
// TanStack Link considers the workspace Chat destination an ancestor. Render the
// real router/component and production CSS: this is the narrowest layer proving
// aria-current precedence, scoped navigation, theme styling and keyboard focus.
// No daemon/provider is needed; this is a UI regression test, not a benchmark.
test('mode selection survives nested routes and matches in both sidebar contexts', { timeout: 30000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {createRootRoute,createRoute,createRouter,createMemoryHistory,RouterProvider,useRouterState} from '@tanstack/react-router';
    import {SidebarModeSelector} from './src/features/desktop/orchestrate/sidebar-mode-selector';
    window.clicks=0; window.chatClicks=0;
    function Shell(){const path=useRouterState({select:s=>s.location.pathname});
      const scoped=path.startsWith('/demo'); const mode=path.split('/').includes('swarm')?'swarm':'chat';
      return <div className={mode==='swarm'?'swarm-section':''}><aside style={{width:260}}>
        <SidebarModeSelector mode={mode} workspaceSlug={scoped?'demo':undefined} pendingReviews={3}
          onNavigate={()=>window.clicks++} onNavigateChat={()=>window.chatClicks++}/>
        <div>Tasks</div></aside></div>}
    const root=createRootRoute({component:Shell});
    const paths=['/','/session/$id','/swarm','/swarm/$','/$workspaceSlug','/$workspaceSlug/session/$id','/$workspaceSlug/swarm','/$workspaceSlug/swarm/$'];
    const routeTree=root.addChildren(paths.map(path=>createRoute({getParentRoute:()=>root,path})));
    window.router=createRouter({routeTree,history:createMemoryHistory({initialEntries:['/demo/swarm/workers']})});
    createRoot(document.getElementById('root')).render(<RouterProvider router={window.router}/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent', loader: { '.css': 'empty' } })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://selector.test/')
    await page.addStyleTag({ content: await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addStyleTag({ content: ':root { --swarm-surface:var(--app-surface-elevated); --swarm-surface-subtle:var(--app-surface-subtle); --swarm-surface-hover:var(--app-surface-hover); --swarm-text:var(--app-text); --swarm-text-muted:var(--app-text-muted); --swarm-text-subtle:var(--app-text-subtle); --swarm-border:var(--app-border); --swarm-accent:var(--app-primary); }' })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const nav = page.getByRole('navigation', { name: 'Chat and Swarm mode' })
    await nav.waitFor()
    for (const theme of [
      '--app-surface-subtle:#101820;--app-surface-elevated:#304050;--app-text:#ffffff;--app-text-subtle:#a0a8b0;--app-text-muted:#b0c0d0;--app-border:#405060;--app-primary:#80c0ff;--app-surface-hover:#203040;',
      '--app-surface-subtle:#e0e4e8;--app-surface-elevated:#ffffff;--app-text:#101820;--app-text-subtle:#606870;--app-text-muted:#405060;--app-border:#a0b0c0;--app-primary:#2040b0;--app-surface-hover:#d0d8e0;',
    ]) {
      await page.locator('html').evaluate((el, css) => el.setAttribute('style', css), theme)
      for (const prefix of ['', '/demo']) {
        for (const suffix of ['', '/session/example', '/swarm', '/swarm/projects', '/swarm/projects/example/charter', '/swarm/workers', '/swarm/workers/example', '/swarm/agents', '/swarm/settings']) {
          const path = `${prefix}${suffix}` || '/'
          await page.evaluate(async path => { await (window as any).router.navigate({ to: path }) }, path)
          const swarm = suffix.startsWith('/swarm')
          const selected = nav.locator('[data-selected="true"]')
          const inactive = nav.locator('[data-selected="false"]')
          assert.equal(await selected.count(), 1, path)
          assert.equal(await nav.locator('[aria-current="page"]').count(), 1, path)
          assert.equal(await selected.getAttribute('aria-current'), 'page')
          assert.equal(await inactive.getAttribute('aria-current'), null, 'ancestor Chat must not be current')
          assert.equal(await selected.getAttribute('aria-label'), swarm ? 'Switch to Swarm Orchestrate Mode' : 'Switch to Chat Mode')
          assert.equal(await nav.getByRole('link', { name: 'Switch to Chat Mode', exact: true }).getAttribute('href'), prefix || '/')
          assert.equal(await nav.getByRole('link', { name: 'Switch to Swarm Orchestrate Mode', exact: true }).getAttribute('href'), `${prefix}/swarm`)
          const style = (el: Element) => { const s = getComputedStyle(el); return [s.backgroundColor, s.borderColor, s.color] }
          const activeStyle = await selected.evaluate(style)
          const inactiveStyle = await inactive.evaluate(style)
          for (let i = 0; i < activeStyle.length; i++) assert.notEqual(activeStyle[i], inactiveStyle[i], `${path}: selected fill/border/text differ`)
          assert.equal(await nav.evaluate(el => getComputedStyle(el).display), 'grid')
          assert.equal(await nav.getByLabel('3 worker reviews pending').count(), 1)
        }
      }
    }
    await page.keyboard.press('Tab')
    assert.equal(await nav.locator('a:focus-visible').count(), 1)
    assert.equal(await nav.locator('a:focus-visible').evaluate(el => getComputedStyle(el).outlineStyle), 'solid')
    await nav.getByRole('link', { name: 'Switch to Chat Mode', exact: true }).click()
    await page.waitForFunction(() => (window as any).router.state.location.pathname === '/demo')
    await nav.getByRole('link', { name: 'Switch to Swarm Orchestrate Mode', exact: true }).click()
    await page.waitForFunction(() => (window as any).router.state.location.pathname === '/demo/swarm')
    assert.deepEqual(await page.evaluate(() => [(window as any).clicks, (window as any).chatClicks]), [2, 1])
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
