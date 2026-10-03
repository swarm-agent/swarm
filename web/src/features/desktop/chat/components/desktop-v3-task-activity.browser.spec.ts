import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile, mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { compile } from 'tailwindcss'

// Purpose: real task activity/status components must wrap untrusted long text at
// narrow widths without showing internal JSON. The browser uses production CSS
// and components; this is hermetic DOM/layout validation, not a provider benchmark.
test('task activity remains readable at desktop and mobile widths', { timeout: 30_000 }, async () => {
  const require = createRequire(import.meta.url)
  const compiler = await compile(await readFile('src/theme.css', 'utf8'), { base: path.resolve('src'), loadStylesheet: async (id, base) => {
    const file = id.startsWith('.') ? path.resolve(base, id) : require.resolve(id === 'tailwindcss' ? 'tailwindcss/index.css' : id)
    return { path: file, base: path.dirname(file), content: await readFile(file, 'utf8') }
  } })
  const sources = await Promise.all(['desktop-v3-task-activity', 'desktop-v3-run-status'].map(name => readFile(`src/features/desktop/chat/components/${name}.tsx`, 'utf8')))
  const css = compiler.build(sources.join(' ').split(/[\s'"`]+/))
  const bundle = await build({ stdin: { contents: `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {DesktopV3TaskActivity} from './src/features/desktop/chat/components/desktop-v3-task-activity';
    import {DesktopV3RunStatusPill} from './src/features/desktop/chat/components/desktop-v3-run-status';
    const rows=[{title:'Parser audit',status:'Ready for review',summary:'The parser is ready for review. This is not accepted completion.'}];
    const activity={id:'one',timelineSeq:1,label:'Task wait ended',detail:'Task outcomes are available. A continuation was queued; this does not mean it is running.',rows};
    createRoot(document.getElementById('root')).render(<main><h2>Task updates</h2><div style={{display:'flex',gap:8,flexWrap:'wrap',marginBottom:16}}>{['Waiting for tasks','Resuming after tasks','Resumed after tasks'].map(label=><DesktopV3RunStatusPill key={label} model={{kind:'waiting',label,active:false}} now={1}/>)}</div><DesktopV3TaskActivity activity={activity}/><div style={{height:16}}/><DesktopV3TaskActivity activity={{...activity,label:'Task progress',detail:'Received by the Orchestrator; not scope approval.',rows:[{title:'',status:'',summary:'Completed the checks. <script>not executable</script> '+ 'long-summary-'.repeat(20)}]}}/></main>);`, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.abort())
    await page.setContent(`<html><head><style>${css}\nbody{background:#111827;color:#e5e7eb;padding:16px}main{max-width:720px;margin:auto}h2{font-size:20px;margin:0 0 16px}</style></head><body><div id="root"></div></body></html>`)
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByText('Parser audit · Ready for review').waitFor()
    const evidence = process.env.SWARM_TASK_TEST_EVIDENCE_DIR
    if (evidence) await mkdir(evidence, { recursive: true })
    for (const width of [1100, 390]) {
      await page.setViewportSize({ width, height: 900 })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true)
      assert.equal(await page.locator('[data-testid="desktop-task-activity"]').count(), 2)
      assert.doesNotMatch(await page.locator('main').innerText(), /task_wait_owner_run_id|Delegated project task outcomes/)
      assert.equal(await page.locator('main script').count(), 0)
      if (evidence) await page.screenshot({ path: path.join(evidence, `tasks-${width}.png`) })
    }
  } finally { await browser.close() }
})
