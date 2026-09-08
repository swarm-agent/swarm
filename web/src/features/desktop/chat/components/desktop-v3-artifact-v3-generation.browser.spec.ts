import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { compile } from 'tailwindcss'

// Requirement: NativeArtifactStudio consumes server generation_groups and temporal
// Parts, preserves URL navigation across roots/reload/rounds, and stages exact
// reference intent without selecting head. Threat: fabricated frontend grouping,
// failed-member selection or shared Canvas geometry selecting the wrong scene.
// This intercepted browser executes the production component and preview bridge.
test('generation navigation and shared-playhead scene reference remain intent-only', { timeout: 30_000 }, async () => {
  const bridge = await readFile('../swarmd/internal/runtime/artifact_v3_preview_selection.js', 'utf8')
  const bundle = await build({ stdin: { contents: `import React,{useState} from 'react';import{createRoot}from'react-dom/client';
    import{DesktopV3ArtifactV3Studio}from'./src/features/desktop/chat/components/desktop-v3-artifact-v3-studio';
    import{readNativeArtifactNavigation}from'./src/features/desktop/session-v3/artifact-v3-navigation';
    function App(){const[a,setA]=useState({artifactId:readNativeArtifactNavigation(location.search).artifactId||'one',ownerSessionId:'parent',label:'Scene fixture'});const[o,setO]=useState(true);return <><button onClick={()=>setO(true)}>Reopen</button><DesktopV3ArtifactV3Studio artifact={a} open={o} onOpenChange={setO} onNavigate={setA} onIterate={s=>{window.staged=s}}/></>};createRoot(document.getElementById('root')).render(<App/>);`, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const parts = ['opening', 'resolve'].map((id, i) => ({ id, label: id, locator: { kind: 'selector', path: 'index.html', value: '#canvas' }, temporal: { scene_id: id, start_ms: i * 1000, end_ms: (i + 1) * 1000 } }))
  const member = (artifact: string, index: number, wave = 'wave') => ({ wave_id: wave, index, count: 3, artifact_id: artifact, turn_id: 'turn', candidate_id: `candidate-${index}`, commit_oid: artifact === 'failed' ? '' : (artifact === 'one' ? 'a' : 'b').repeat(40), status: artifact === 'failed' ? 'failed' : 'ready' })
  const groups = ['wave', 'later'].map((wave) => ({ wave_id: wave, count: 3, members: [member('one', 1, wave), member('two', 2, wave), member('failed', 3, wave)] }))
  const theme = await readFile('src/theme.css', 'utf8')
  const defaults = await readFile('node_modules/tailwindcss/theme.css', 'utf8')
  const source = await readFile('src/features/desktop/chat/components/desktop-v3-artifact-v3-studio.tsx', 'utf8')
  const css = (await compile(`${defaults}\n${theme.replace('@import "tailwindcss";', '@tailwind utilities;')}`)).build(source.split(/[\s"'`]+/))
  const mutations: string[] = []
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    page.setDefaultTimeout(5000)
    await page.route('**/*', async (route) => {
      const request = route.request(), url = new URL(request.url())
      if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: '<html><body><div id="root"></div></body></html>' })
      const id = url.pathname.split('/artifacts-v3/')[1]?.split('/')[0] || 'one'
      const commit = (id === 'one' ? 'a' : 'b').repeat(40)
      const revision = { revision_ref: `revision-${commit}`, commit_oid: commit, manifest: { parts } }
      if (url.pathname.includes('/preview/access/token')) {
        const config = { revision_ref: revision.revision_ref, part_ids: parts.map(p => p.id), parts: parts.map((p, i) => ({ id: p.id, selector: '#canvas', time_ms: i * 1000 + 500 })) }
        return route.fulfill({ contentType: 'text/html', body: `<html><head><script>${bridge.replace('__SWARM_ARTIFACT_V3_SELECTION_CONFIG__', JSON.stringify(config))}</script></head><body><canvas id="canvas"></canvas><p id="time">0</p><script>globalThis.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',seek(ms){document.getElementById('time').textContent=String(ms);return {time_ms:globalThis.badAck?-1:ms}}}</script></body></html>` })
      }
      if (url.pathname.endsWith('/preview/access')) return route.fulfill({ json: { preview_url: `/v3/sessions/parent/artifacts-v3/${id}/preview/access/token` } })
      if (request.method() !== 'GET') mutations.push(url.pathname)
      if (url.pathname.endsWith('/revisions')) return route.fulfill({ json: { ok: true, revisions: [revision] } })
      return route.fulfill({ json: { ok: true, artifact: { id, owner_session_id: 'parent', label: id, head: revision, parts, generation_groups: groups, turns: [] } } })
    })
    const mount = async () => { await page.addStyleTag({ content: css }); await page.addScriptTag({ content: bundle.outputFiles[0]!.text }) }
    await page.goto('https://artifact.test/?keep=yes')
    await mount()
    const nav = page.getByRole('navigation', { name: 'Generation siblings and iterations' })
    await nav.getByRole('button', { name: 'Option 2 · ready' }).click()
    await page.waitForURL(/native_artifact=two/)
    assert.equal(await nav.getByRole('button', { name: 'Option 3 · failed' }).isDisabled(), true)
    await page.getByLabel('Generation round').selectOption('wave')
    await page.getByRole('button', { name: 'Refresh Artifact Studio' }).click()
    assert.equal(await page.getByLabel('Generation round').inputValue(), 'wave')
    await page.reload(); await mount()
    await nav.waitFor()
    assert.equal(await page.getByLabel('Generation round').inputValue(), 'wave')
    const scene = page.locator('[data-artifact-v3-part="resolve"]')
    await scene.focus(); await page.keyboard.press('Space')
    const frame = page.frameLocator('iframe')
    await frame.getByText('1500', { exact: true }).waitFor()
    assert.match(await scene.innerText(), /1–2s/)
    if (process.env.SWARM_TEST_SCREENSHOT) await page.screenshot({ path: process.env.SWARM_TEST_SCREENSHOT })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true)
    await page.getByRole('button', { name: 'Use as style/example reference' }).click()
    const staged = await page.evaluate(() => (window as any).staged)
    assert.equal(staged.artifact_id, 'two')
    assert.equal(staged.revision_ref, `revision-${'b'.repeat(40)}`)
    assert.deepEqual(staged.target_part_ids, ['resolve'])
    assert.match(staged.pending_request, /reference only/)
    assert.equal(new URL(page.url()).searchParams.get('native_artifact'), null)
    assert.equal(new URL(page.url()).searchParams.get('keep'), 'yes')
    await page.getByRole('button', { name: 'Reopen', exact: true }).click()
    await nav.waitFor()
    await page.frameLocator('iframe').locator('#time').waitFor()
    const preview = page.frames().find((entry) => entry.url().includes('/preview/access/token'))!
    await preview.evaluate(() => { (globalThis as any).badAck = true })
    await page.locator('[data-artifact-v3-part="opening"]').click()
    await page.getByText('The animation did not acknowledge the requested scene time.', { exact: true }).waitFor()
    assert.deepEqual(mutations, [])
  } finally { await browser.close() }
})
