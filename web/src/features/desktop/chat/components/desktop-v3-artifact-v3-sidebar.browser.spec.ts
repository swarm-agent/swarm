import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { readFile, mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { compile } from 'tailwindcss'

// Requirement: clicking the real sidebar opens the first ready pending option,
// displays authored title and pending context, and never accepts on navigation.
// Authority: catalog normalization -> Sidebar -> native Studio load/selection.
// Hermetic intercepted browser exercises pointer/reopen/refresh, not live runtime.
test('native sidebar opens pending option without selecting head', { timeout: 30_000 }, async () => {
  const require = createRequire(import.meta.url)
  const compiler = await compile(await readFile('src/theme.css', 'utf8'), { loadStylesheet: async (id) => ({ path: require.resolve(id === 'tailwindcss' ? 'tailwindcss/index.css' : id), base: process.cwd(), content: await readFile(require.resolve(id === 'tailwindcss' ? 'tailwindcss/index.css' : id), 'utf8') }) })
  const sources = await Promise.all(['sidebar', 'studio'].map((name) => readFile(`src/features/desktop/chat/components/desktop-v3-artifact-v3-${name}.tsx`, 'utf8')))
  const css = compiler.build(sources.join(' ').split(/[\s'"`]+/))
  const evidence = process.env.SWARM_ARTIFACT_TEST_EVIDENCE_DIR
  if (evidence) await mkdir(evidence, { recursive: true })
  const bundle = await build({
    stdin: { contents: `import React,{useEffect,useState} from 'react'; import {createRoot} from 'react-dom/client';
      import {DesktopV3ArtifactV3Sidebar} from './src/features/desktop/chat/components/desktop-v3-artifact-v3-sidebar';
      import {DesktopV3ArtifactV3Studio} from './src/features/desktop/chat/components/desktop-v3-artifact-v3-studio';
      import {fetchDesktopV3NativeArtifactCatalog} from './src/features/desktop/session-v3/artifact-v3-api';
      function App(){const [items,setItems]=useState([]),[active,setActive]=useState(null),[open,setOpen]=useState(false);
      useEffect(()=>{fetchDesktopV3NativeArtifactCatalog('parent').then(setItems)},[]);
      return <><DesktopV3ArtifactV3Sidebar artifacts={items} onOpenArtifact={a=>{setActive(a);setOpen(true)}}/><DesktopV3ArtifactV3Studio artifact={active} open={open} onOpenChange={setOpen} onIterate={()=>{}}/></>}
      createRoot(document.getElementById('root')).render(<App/>);`, resolveDir: process.cwd(), loader: 'tsx' },
    bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent',
  })
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    page.setDefaultTimeout(5_000)
    const revision = (id: string) => ({ revision_ref: `revision-${id.repeat(40)}`, commit_oid: id.repeat(40), manifest: { parts: [{ id: 'narration', label: 'Narration', locator: { kind: 'selector', path: 'index.html', value: '#narration' } }] }, build: { status: 'succeeded' }, validation: { status: 'valid' } })
    const base = revision('a'), first = revision('b'), second = revision('c')
    let accepted = false
    const mutations: string[] = []
    const artifact = () => ({ id: 'artifact', owner_session_id: 'parent', label: 'Launch narration & direction', revision: 9, head: base, parts: base.manifest.parts,
      turns: [
        { turn_id: 'older', status: 'selected', created_at: 1, selected_candidate_id: 'old', candidates: [{ candidate_id: 'old', status: 'ready', revision: base }] },
        { turn_id: 'pending', status: accepted ? 'selected' : 'awaiting_selection', selected_candidate_id: accepted ? 'first' : '', created_at: 2, revision: 7, candidates: [{ candidate_id: 'failed', status: 'failed' }, { candidate_id: 'first', status: 'ready', revision: first }, { candidate_id: 'second', status: 'ready', revision: second }] },
        { turn_id: 'new-failure', status: 'failed', created_at: 3, candidates: [{ candidate_id: 'failed-new', status: 'failed' }] },
      ] })
    await page.route('**/*', async (route) => {
      const request = route.request(), path = new URL(request.url()).pathname
      if (path === '/') return route.fulfill({ contentType: 'text/html', body: `<html><head><style>${css}\n[data-testid="desktop-session-artifact-v3-sidebar"]{width:300px}</style></head><body><div id="root"></div></body></html>` })
      if (path.includes('/preview/access/token')) return route.fulfill({ contentType: 'text/html', body: '<main id="narration">Narration preview</main>' })
      let payload: unknown
      if (path.endsWith('/preview/access')) payload = { ok: true, preview_url: `/v3/sessions/parent/artifacts-v3/artifact/preview/access/token?revision=${request.postDataJSON().revision_ref}` }
      else if (request.method() !== 'GET') { mutations.push(path); return route.abort() }
      else if (path.endsWith('/artifacts-v3')) payload = { ok: true, artifacts: [artifact()] }
      else if (path.endsWith('/artifacts-v3/artifact')) payload = { ok: true, artifact: artifact() }
      else if (path.endsWith('/revisions')) payload = { ok: true, revisions: [base] }
      else return route.abort()
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(payload) })
    })
    await page.goto('https://artifact.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    const sidebar = page.locator('[data-artifact-v3-sidebar-id="artifact"]')
    assert.match(await sidebar.textContent() ?? '', /Launch narration & direction/)
    assert.match(await sidebar.textContent() ?? '', /1 pending turn · 2 options to review/)
    if (evidence) await sidebar.screenshot({ path: path.join(evidence, 'sidebar.png') })
    await sidebar.click()
    await page.locator(`[data-artifact-v3-preview-revision="${first.commit_oid}"]`).waitFor()
    await page.frameLocator('[data-artifact-v3-complete-preview]').getByText('Narration preview').waitFor()
    if (evidence) await page.screenshot({ path: path.join(evidence, 'pending-studio.png') })
    assert.match(await page.locator('[data-artifact-v3-primary-preview]').textContent() ?? '', /Pending change preview · not accepted/)
    assert.equal(await page.locator('[data-artifact-v3-turns] > details').first().getAttribute('data-artifact-v3-turn'), 'pending')
    assert.equal(await page.locator('[data-artifact-v3-turn="pending"]').getAttribute('open'), '')
    assert.match(await page.locator('[data-artifact-v3-candidate="first"]').textContent() ?? '', /Viewing/)
    assert.equal(await page.locator('[data-artifact-v3-iterate]').isDisabled(), true)
    assert.deepEqual(mutations, [])
    // Refresh never overrides a user's explicit history navigation.
    await page.locator(`[data-artifact-v3-revision="${base.commit_oid}"]`).click()
    await page.getByRole('button', { name: 'Refresh Artifact V3 Studio' }).click()
    await page.locator(`[data-artifact-v3-preview-revision="${base.commit_oid}"]`).waitFor()
    await page.getByRole('button', { name: 'Close Artifact V3 Studio' }).click()
    await sidebar.click()
    await page.locator(`[data-artifact-v3-preview-revision="${first.commit_oid}"]`).waitFor()
    await page.getByRole('button', { name: 'Close Artifact V3 Studio' }).click()
    // Stale sidebar metadata cannot reopen a candidate from a now-selected turn.
    accepted = true
    await sidebar.click()
    await page.locator(`[data-artifact-v3-preview-revision="${base.commit_oid}"]`).waitFor()
    assert.deepEqual(mutations, [])
  } finally { await browser.close() }
})
