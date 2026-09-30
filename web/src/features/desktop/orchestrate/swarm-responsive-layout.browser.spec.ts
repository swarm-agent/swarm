import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import { readFile } from 'node:fs/promises'

// Requirement: use available container width, keep one conversation/draft mounted,
// and make inactive panes unfocusable with a modal, keyboard-operable navigation.
// Threat: viewport breakpoints squeeze embedded lanes, resize destroys drafts, or
// hidden controls retain focus. Authority: useSwarmResponsiveLayout and production
// swarm-section.css. A browser component boundary is the narrowest layer proving
// actual geometry, native inert behavior, focus restoration and React identity.
// This fixture is presentation evidence, not daemon/provider or all-route evidence.
test('Swarm shell preserves panes and focus across container widths', { timeout: 60000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState,useEffect} from 'react'; import {createRoot} from 'react-dom/client';
    import {useSwarmResponsiveLayout,SwarmLayoutControls} from './src/features/desktop/orchestrate/swarm-responsive-layout';
    window.mounts=0; window.unmounts=0;
    function Conversation(){useEffect(()=>{window.mounts++;return()=>window.unmounts++},[]);return <textarea aria-label="Draft" defaultValue="saved draft"/>}
    function Shell(){const [root,setRoot]=useState(null);const l=useSwarmResponsiveLayout(root);return <div ref={setRoot} className="swarm-section swarm-responsive-shell relative" data-layout={l.mode} data-split={l.split} data-panel={l.panel} data-navigation-open={l.navigationOpen}>
      <SwarmLayoutControls layout={l}/>
      <aside id={l.navigationId} className="swarm-navigation-sidebar flex flex-col" aria-label="Swarm navigation" role={l.navigationOpen?'dialog':undefined} aria-modal={l.navigationOpen?true:undefined}>
        <button className="swarm-navigation-close" onClick={()=>l.setNavigationOpen(false)}>Close navigation</button>
        <div><input aria-label="Search tasks"/></div>
        <nav className="swarm-route-navigation"><a href="#tasks" aria-label="Tasks" aria-current="page"><svg width="16" height="16"/><span>Tasks</span></a><a href="#settings" aria-label="Settings"><svg width="16" height="16"/><span>Settings</span></a></nav>
        <div><button>Account</button></div>
      </aside>
      <main className="swarm-main-panel flex flex-col"><button>Main action</button><div className="swarm-workers-layout"><nav>Worker list</nav><div>Worker detail</div></div></main>
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
    assert.ok((await page.locator('.swarm-main-panel').boundingBox())!.width >= 400)
    await resize(800, 'rail', false)
    assert.equal(await page.locator('.swarm-conversation-panel').evaluate(node => (node as HTMLElement).inert), true)
    await page.getByRole('button', { name: 'Chat', exact: true }).click()
    assert.equal(await page.locator('.swarm-main-panel').evaluate(node => (node as HTMLElement).inert), true)
    await page.locator('.swarm-main-panel > button').evaluate(node => (node as HTMLElement).focus())
    assert.notEqual(await page.evaluate(() => document.activeElement?.textContent), 'Main action', 'native inert rejects programmatic hidden-pane focus')
    await draft.fill('edited draft')
    await draft.focus()
    const open = page.getByRole('button', { name: 'Open Swarm navigation' })
    await open.click()
    assert.equal(await open.getAttribute('aria-expanded'), 'true')
    assert.equal(await page.getByRole('dialog').getAttribute('aria-modal'), 'true')
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Close navigation')
    await page.keyboard.press('Shift+Tab')
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Account')
    await page.keyboard.press('Tab')
    assert.equal(await page.evaluate(() => document.activeElement?.textContent), 'Close navigation')
    await page.keyboard.press('Escape')
    assert.equal(await open.evaluate(node => document.activeElement === node), true)
    await resize(390, 'phone', false)
    assert.equal(await draft.inputValue(), 'edited draft')
    assert.ok(await draft.evaluate(node => node === (window as any).draftNode))
    await open.click()
    await page.locator('.swarm-navigation-backdrop').click({ position: { x: 370, y: 20 }, force: true })
    await page.waitForFunction(() => document.querySelector('.swarm-responsive-shell')?.getAttribute('data-navigation-open') === 'false')
    await open.click()
    await resize(1400, 'expanded', true)
    await page.waitForFunction(() => document.querySelector('.swarm-responsive-shell')?.getAttribute('data-navigation-open') === 'false')
    assert.equal(await page.getByRole('dialog').count(), 0)
    assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')), 'Tasks')
    assert.deepEqual(await page.evaluate(() => [(window as any).mounts, (window as any).unmounts]), [1, 0])
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
