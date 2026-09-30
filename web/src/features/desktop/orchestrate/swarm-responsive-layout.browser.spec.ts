import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import { readFile } from 'node:fs/promises'

// Requirement: use available container width, keep helper-owned children mounted,
// and make inactive panes unfocusable with a modal, keyboard-operable navigation.
// Threat: viewport breakpoints squeeze embedded lanes, resize destroys drafts, or
// hidden controls retain focus. Authority: useSwarmResponsiveLayout and production
// swarm-section.css. A browser component boundary is the narrowest layer proving
// actual geometry, native inert behavior, focus restoration and React identity.
// This surrogate draft is helper-only evidence, not actual conversation identity,
// daemon/provider or all-route evidence; the real-component test below is separate.
test('Swarm shell preserves panes and focus across container widths', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState,useEffect} from 'react'; import {createRoot} from 'react-dom/client';
    import {useSwarmResponsiveLayout,SwarmLayoutControls} from './src/features/desktop/orchestrate/swarm-responsive-layout';
    // Native observations still measure real geometry; omit the optional field.
    const NativeObserver=window.ResizeObserver;
    window.ResizeObserver=class extends NativeObserver { constructor(callback){super(entries=>callback(entries.map(entry=>({target:entry.target,contentRect:entry.contentRect}))))} };
    window.mounts=0; window.unmounts=0;
    function Conversation(){useEffect(()=>{window.mounts++;return()=>window.unmounts++},[]);return <textarea aria-label="Draft" defaultValue="saved draft"/>}
    function Shell(){const [root,setRoot]=useState(null);const l=useSwarmResponsiveLayout(root);return <div ref={setRoot} className="swarm-section swarm-responsive-shell relative" data-layout={l.mode} data-split={l.split} data-panel={l.panel} data-navigation-open={l.navigationOpen}>
      <SwarmLayoutControls layout={l}/>
      <aside id={l.navigationId} tabIndex={-1} className="swarm-navigation-sidebar flex flex-col" aria-label="Swarm navigation" role={l.navigationOpen?'dialog':undefined} aria-modal={l.navigationOpen?true:undefined}>
        <button className="swarm-navigation-close" onClick={()=>l.setNavigationOpen(false)}>Close navigation</button>
        <div><input aria-label="Search tasks"/></div>
        <nav className="swarm-route-navigation"><a href="#tasks" aria-label="Tasks" aria-current="page"><svg width="16" height="16"/><span>Tasks</span></a><a href="#settings" aria-label="Settings"><svg width="16" height="16"/><span>Settings</span></a></nav>
        <div className="flex"><button>Account</button><textarea aria-label="Navigation notes"/><select aria-label="Navigation choice"><option>Choice</option></select><button tabIndex={2}>Last navigation action</button></div>
      </aside>
      <main className="swarm-main-panel flex flex-col" style={{display:'none'}}><button>Hidden route action</button></main><main className="swarm-main-panel flex flex-col"><button>Main action</button><div className="swarm-workers-layout"><nav>Worker list</nav><div>Worker detail</div></div></main>
      <div className="swarm-conversation-panel flex flex-col"><div className="swarm-ai-sidebar flex flex-col"><div className="swarm-chat-composer-lane"><Conversation/></div></div></div>
    </div>};createRoot(document.getElementById('root')).render(<Shell/>);
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1600, height: 900 } })
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setContent('<div id="root" style="width:1400px;height:800px"></div>')
    await page.addStyleTag({ content: css + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const shell = page.locator('.swarm-responsive-shell')
    const resize = async (width: number, mode: string, split: boolean) => {
      await page.locator('#root').evaluate((node, width) => { node.style.width = width + 'px' }, width)
      await page.waitForFunction(({ mode, split }) => {
        const node = document.querySelector('.swarm-responsive-shell')
        return node?.getAttribute('data-layout') === mode && node.getAttribute('data-split') === String(split)
      }, { mode, split })
      assert.ok(await shell.evaluate(node => node.scrollWidth <= node.clientWidth + 1), 'shell fits container without masking horizontal overflow')
    }
    await resize(1400, 'expanded', true)
    const draft = page.getByRole('textbox', { name: 'Draft' })
    await draft.fill('retained draft')
    await draft.evaluate(node => { (window as any).draftNode = node })
    await resize(1150, 'rail', true)
    assert.equal(Math.round((await page.locator('.swarm-navigation-sidebar').boundingBox())!.width), 68)
    assert.ok((await page.locator('.swarm-main-panel').last().boundingBox())!.width >= 400)
    await resize(800, 'rail', false)
    assert.equal(await page.locator('.swarm-conversation-panel').evaluate(node => (node as HTMLElement).inert), true)
    await page.getByRole('button', { name: 'Chat', exact: true }).click()
    assert.equal(await page.locator('.swarm-main-panel').last().evaluate(node => (node as HTMLElement).inert), true)
    await page.locator('.swarm-main-panel > button').last().evaluate(node => (node as HTMLElement).focus())
    assert.notEqual(await page.evaluate(() => document.activeElement?.textContent), 'Main action', 'native inert rejects programmatic hidden-pane focus')
    await draft.fill('edited draft')
    await draft.focus()
    const open = page.getByRole('button', { name: 'Open Swarm navigation' })
    await open.click()
    assert.equal(await page.locator('.swarm-layout-controls button').first().getAttribute('aria-expanded'), 'true')
    assert.equal(await page.locator('.swarm-layout-controls').evaluate(node => (node as HTMLElement).inert), true)
    assert.deepEqual(await page.locator('.swarm-main-panel').evaluateAll(nodes => nodes.map(node => (node as HTMLElement).inert)), [true, true])
    await page.locator('.swarm-layout-controls button').first().evaluate(node => (node as HTMLElement).focus())
    assert.equal(await page.getByRole('dialog').evaluate(node => node.contains(document.activeElement)), true)
    assert.equal(await page.locator('.swarm-navigation-sidebar > div').last().evaluate(node => getComputedStyle(node).display), 'flex')
    assert.equal(await page.getByRole('dialog').getAttribute('aria-modal'), 'true')
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Last navigation action')
    await page.keyboard.press('Shift+Tab')
    assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')), 'Navigation choice')
    await page.keyboard.press('Tab')
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Last navigation action')
    await page.keyboard.press('Escape')
    await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Open Swarm navigation')
    assert.equal(await open.evaluate(node => document.activeElement === node), true)
    await resize(390, 'phone', false)
    assert.equal(await draft.inputValue(), 'edited draft')
    assert.ok(await draft.evaluate(node => node === (window as any).draftNode))
    await open.click()
    await page.getByRole('dialog').evaluate(node => { for (const child of Array.from(node.children)) (child as HTMLElement).style.display = 'none' })
    await page.keyboard.press('Tab')
    assert.equal(await page.getByRole('dialog').evaluate(node => document.activeElement === node), true, 'empty drawer retains focus on its fallback')
    await page.locator('.swarm-navigation-backdrop').click({ position: { x: 370, y: 20 }, force: true })
    await page.waitForFunction(() => document.querySelector('.swarm-responsive-shell')?.getAttribute('data-navigation-open') === 'false')
    await page.locator('.swarm-navigation-sidebar').evaluate(node => { for (const child of Array.from(node.children)) (child as HTMLElement).style.removeProperty('display') })
    await open.click()
    await resize(1400, 'expanded', true)
    await page.waitForFunction(() => document.querySelector('.swarm-responsive-shell')?.getAttribute('data-navigation-open') === 'false')
    assert.equal(await page.getByRole('dialog').count(), 0)
    assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')), 'Tasks')
    assert.deepEqual(await page.evaluate(() => [(window as any).mounts, (window as any).unmounts]), [1, 0])
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})

// Requirement: real task controls and deploy/model dialogs stay operable in a
// phone lane, including long project labels. Threat: action overflow, undersized
// targets, escaped focus and footer clipping. Authority: OrchestrateView,
// TaskListHeader/Toolbar, AgentModelControl and useSwarmModalFocus. Browser
// composition with finite HTTP/cache fixtures is the narrowest proof of rendered
// controls; it does not prove live provider behavior or session durability.
test('real task and model dialogs contain focus and fit narrow lanes', { timeout: 90000 }, async () => {
  const { fixtureRead, snapshot } = await import('./swarm-responsive-browser-fixtures')
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import {mountResponsiveFixture} from './src/features/desktop/orchestrate/swarm-responsive-browser-fixtures'; mountResponsiveFixture('populated');` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 740 } })
    page.setDefaultTimeout(5000)
    const errors: string[] = [], mutations: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url())
      if (request.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root" style="height:100dvh;width:100%"></div>' })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: snapshot() })
      if (request.method() !== 'GET') { mutations.push(url.pathname); return route.fulfill({ status: 400, json: { error: 'Unexpected mutation' } }) }
      const data = fixtureRead(url, 'populated')
      return route.fulfill({ status: data === undefined ? 501 : 200, json: data ?? { error: 'Missing fixture read' } })
    })
    await page.goto('https://responsive.test/fixture/swarm')
    await page.addStyleTag({ content: css + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const primary = page.getByRole('button', { name: 'New task', exact: true })
    await primary.waitFor()
    assert.ok((await primary.boundingBox())!.height >= 44)
    await page.getByRole('button', { name: 'Select all', exact: true }).click()
    assert.ok(await page.locator('.swarm-task-list-toolbar').evaluate(node => node.scrollWidth <= node.clientWidth + 1), 'selected management groups wrap without clipping')
    assert.ok(await page.locator('.swarm-task-list-header').evaluate(node => node.scrollWidth <= node.clientWidth + 1), 'long title fits lane')
    await primary.click()
    const deploy = page.getByRole('dialog', { name: 'Deploy Autonomous Task' })
    await deploy.waitFor()
    assert.equal(await deploy.evaluate(node => node.contains(document.activeElement)), true)
    const footer = deploy.locator('button').last()
    await footer.scrollIntoViewIfNeeded()
    const footerBox = (await footer.boundingBox())!, dialogBox = (await deploy.boundingBox())!
    assert.ok(footerBox.y >= dialogBox.y && footerBox.y + footerBox.height <= dialogBox.y + dialogBox.height + 1, 'deploy footer is reachable by scrolling')
    assert.ok(await deploy.evaluate(node => node.scrollWidth <= node.clientWidth + 1))
    const close = deploy.getByRole('button', { name: 'Close deploy task dialog' })
    await close.focus()
    await page.keyboard.press('Shift+Tab')
    assert.equal(await deploy.evaluate(node => node.contains(document.activeElement)), true)
    await primary.evaluate(node => (node as HTMLElement).focus())
    assert.equal(await deploy.evaluate(node => node.contains(document.activeElement)), true, 'programmatic focus escape is contained')
    await deploy.getByTestId('deploy-modal-change-model-btn').click()
    const model = page.getByRole('dialog', { name: 'Agent and model settings' })
    await model.waitFor()
    assert.equal(await model.evaluate(node => node.contains(document.activeElement)), true, 'nested model dialog owns focus')
    for (const button of await model.getByRole('button', { name: 'Close', exact: true }).all()) assert.ok((await button.boundingBox())!.height >= 44)
    assert.ok(await model.locator('.agent-model-dialog-surface').evaluate(node => node.scrollWidth <= node.clientWidth + 1))
    await page.keyboard.press('Escape')
    await model.waitFor({ state: 'detached' })
    assert.equal(await deploy.count(), 1, 'nested Escape does not close deployment')
    await page.keyboard.press('Escape')
    await deploy.waitFor({ state: 'detached' })
    assert.equal(await primary.evaluate(node => document.activeElement === node), true)
    assert.deepEqual(mutations, [], 'presentation actions do not submit tasks or model assignments')
    assert.deepEqual(errors, [])
    await page.evaluate(() => (window as any).responsive.unmount())
  } finally { await browser.close() }
})
