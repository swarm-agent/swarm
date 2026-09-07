import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { compile } from 'tailwindcss'
import { readFile, mkdir } from 'node:fs/promises'
import path from 'node:path'

// Requirement: populated and empty Studio sidebar content must scroll above its
// pinned actions at desktop widths. Threat: shrinking content overlaps navigation.
// Exercise the production sidebar with compiled production utilities in Chromium;
// this component fixture does not establish live authentication or project health.
test('Studio sidebar keeps long populated and empty content clear of actions', { timeout: 30000 }, async () => {
  const source = await readFile('src/features/desktop/tools/components/swarm-tool-sidebar.tsx', 'utf8')
  const pageSource = await readFile('src/features/desktop/tools/pages/video-tool-page.tsx', 'utf8')
  const layout = pageSource.match(/layoutClassName="([^"]+)"/)![1]
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {SwarmToolSidebar} from './src/features/desktop/tools/components/swarm-tool-sidebar';
    const root=createRoot(document.getElementById('root'));
    window.show=(populated)=>root.render(<div className="flex h-screen bg-[var(--app-bg)] text-[var(--app-text)]">
    <SwarmToolSidebar layoutClassName={${JSON.stringify(layout)}} backLabel="Workspace" onBack={()=>{}} darkModeEnabled={false} onToggleDarkMode={()=>{}} toolIcon="V" toolTitle="Video" toolDescription="Edit a cut, review changes, then render. Original media stays untouched." createLabel="Start new video session" createTitle="" onCreateTitleChange={()=>{}} createPlaceholder="Video session" onCreate={()=>{}} creating={false} sessionsLabel="Video sessions" sessions={populated?[{id:'session',title:'Launch film'}]:[]} selectedSessionId={populated?'session':null} onSelectSession={()=>{}} emptySessionsMessage="No video sessions yet." defaultSessionTitle="Video" compactSelectedSession={populated} prioritizeChildren={populated} beforeSessions={<details><summary>Retained video library</summary>Older projects</details>} actions={['Render queue','Add folder','Show files','Open session'].map(label=>({id:label,label,onClick:()=>{}}))}>
      {populated?<div><h2 className="text-sm font-semibold">Launch film · Current cut r4</h2><p className="mt-2 text-xs">8.0s · 30 fps timeline</p><details className="mt-3"><summary>History & proposal recovery · 1 older pending</summary>Older initial cut remains unchanged.</details><h3 className="mt-4 text-sm">Assets in this cut</h3>{Array.from({length:24},(_,i)=><div key={i} className="border-b border-[var(--app-border)] py-3 text-sm">Scene {i+1}<p className="text-xs">Production ready · MP4</p></div>)}</div>:null}
    </SwarmToolSidebar><main className="flex-1 p-6"><h1 className="text-xl">{populated?'Populated sidebar fixture':'Empty sidebar fixture'}</h1><p className="mt-3 text-sm">Production component · synthetic data · no project mutations</p></main></div>); window.show(true);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const cssRoot = path.dirname(new URL(import.meta.resolve('tailwindcss/package.json')).pathname)
  const compiler = await compile('@import "tailwindcss";', { loadStylesheet: async (id) => ({ path: path.join(cssRoot, id === 'tailwindcss' ? 'index.css' : id), base: cssRoot, content: await readFile(path.join(cssRoot, id === 'tailwindcss' ? 'index.css' : id), 'utf8') }) })
  const css = compiler.build([...new Set((source + pageSource + fixture).split(/[\s"'`{}]+/))])
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: `<style>${css}:root{--app-bg:#111318;--app-surface:#1c2028;--app-text:#e9edf3;--app-text-muted:#b3bdca;--app-text-subtle:#929dab;--app-border:#353c47;--app-primary:#8bc5ff}body{margin:0;font-family:Arial}</style><div id="root"></div>` }))
    await page.goto('https://studio.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    for (const [width, height] of [[1920,1080],[1280,720]]) {
      await page.setViewportSize({ width, height })
      for (const populated of [true,false]) {
        await page.evaluate(value => (window as any).show(value), populated)
        await page.getByRole('heading', {name: populated?'Populated sidebar fixture':'Empty sidebar fixture'}).waitFor()
        const scroll = await page.locator('[data-tool-sidebar-scroll]').boundingBox()
        const action = await page.getByRole('button', {name:'Render queue',exact:true}).boundingBox()
        assert.ok(scroll && action && scroll.y + scroll.height <= action.y + 1, 'content stays above pinned actions')
        assert.ok(action!.y + action!.height < height)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false)
        if (process.env.SWARM_VISUAL_OUTPUT) {
          await mkdir(process.env.SWARM_VISUAL_OUTPUT, { recursive:true })
          await page.screenshot({path:path.join(process.env.SWARM_VISUAL_OUTPUT,`sidebar-${populated?'populated':'empty'}-${width}.png`)})
        }
      }
    }
  } finally { await browser.close() }
})
