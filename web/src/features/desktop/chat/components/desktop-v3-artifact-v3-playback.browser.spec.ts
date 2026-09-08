import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: native Studio must preserve authored elapsed WebGL playback and
// controls under its actual opaque sandbox, injected selection script and API CSP.
// Threat: deterministic seek/still readiness falsely presented as live playback,
// swallowed controls or blocked transitive module imports. Narrow browser layer:
// real Studio + selection script + pinned Three.js; API/installation are mocked,
// with exact-graph/capability rejection proved separately by Go tests. This is a
// representative fixture, NOT the original artifact or an installed-app proof.
test('native viewer preserves elapsed Three.js playback and authored lifecycle', { timeout: 60_000 }, async () => {
  const selection = await readFile('../swarmd/internal/runtime/artifact_v3_preview_selection.js', 'utf8')
  const api = await readFile('../swarmd/internal/api/sessions_v3_artifacts.go', 'utf8')
  const csp = api.match(/sessionsV3ArtifactPreviewHTMLCSP\s*=\s*"([^"]+)"/)![1]!
  const require = createRequire(import.meta.url)
  const runtime = dirname(require.resolve('three'))
  const revision = `revision-${'a'.repeat(40)}`
  const prefix = '/v3/sessions/parent/artifacts-v3/artifact/preview/access/token/files/swarm-animation-runtime/'
  const moduleURL = (name: string) => `${prefix}${name}?revision=${revision}`
  const modules = new Map<string, string>()
  for (const name of ['three.module.js', 'three.core.js']) {
    let body = await readFile(join(runtime, name), 'utf8')
    for (const dependency of ['three.module.js', 'three.core.js']) {
      for (const quote of ["'", '"']) body = body.split(`${quote}./${dependency}${quote}`).join(JSON.stringify(moduleURL(dependency)))
    }
    modules.set(name, body)
  }
  const parts = [{ id: 'scene', label: 'Representative scene', locator: { kind: 'selector', path: 'index.html', value: '#scene' } }]
  const head = { revision_ref: revision, commit_oid: 'a'.repeat(40), manifest: { parts } }
  const config = { revision_ref: revision, parts: [{ id: 'scene', selector: '#scene' }], part_ids: ['scene'] }
  const html = `<!doctype html><html><head><script type="importmap">${JSON.stringify({ imports: { three: moduleURL('three.module.js') } })}</script>
    <script>${selection.replace('__SWARM_ARTIFACT_V3_SELECTION_CONFIG__', JSON.stringify(config))}</script>
    <style>body{margin:0;background:#101826;color:white;font:16px sans-serif}canvas{display:block;width:320px;height:240px}button,input{margin:8px}</style></head>
    <body><section id="scene"><p>Representative Three.js playback fixture</p><div id="mount"></div><button id="play">Pause</button><input id="seek" aria-label="Seek" type="range" min="0" max="8000" value="0"><p id="status">Loading</p></section>
    <script type="module">
    try {
      const THREE=await import('three');
      const renderer=new THREE.WebGLRenderer({antialias:false,preserveDrawingBuffer:true});renderer.setSize(320,240);document.querySelector('#mount').append(renderer.domElement);
      const scene=new THREE.Scene();scene.background=new THREE.Color('#101826');const camera=new THREE.PerspectiveCamera(45,320/240,.1,100);camera.position.z=4;
      const cube=new THREE.Mesh(new THREE.BoxGeometry(1,1,1),new THREE.MeshNormalMaterial());scene.add(cube);
      const reduced=matchMedia('(prefers-reduced-motion: reduce)');let running=!reduced.matches,time=0,last=0,raf=0,seeks=0,frames=0;
      function draw(){cube.rotation.x=time/1300;cube.rotation.y=time/700;renderer.render(scene,camera);frames++;}
      function stop(){cancelAnimationFrame(raf);raf=0;last=0;}
      function tick(now){if(!running||document.hidden)return; if(last)time+=(now-last);last=now;draw();raf=requestAnimationFrame(tick);}
      function start(){stop();if(running&&!document.hidden)raf=requestAnimationFrame(tick);}
      function label(){document.querySelector('#play').textContent=running?'Pause':'Play';document.querySelector('#status').textContent=running?'Playing':'Paused';}
      document.querySelector('#play').onclick=()=>{running=!running;label();start();};
      function seek(ms){stop();running=false;time=ms;seeks++;draw();label();return {time_ms:ms};}
      document.querySelector('#seek').oninput=e=>seek(Number(e.target.value));
      document.addEventListener('visibilitychange',()=>{if(document.hidden)stop();else start();});
      reduced.onchange=()=>{if(reduced.matches){running=false;stop();label();}};
      renderer.domElement.addEventListener('webglcontextlost',e=>{e.preventDefault();running=false;stop();document.querySelector('#status').textContent='WebGL context lost';});
      globalThis.__SWARM_ANIMATION_V1__={version:'swarm.animation/v1',ready:()=>true,seek};
      globalThis.fixtureState=()=>({time,frames,seeks,running,hidden:document.hidden});draw();label();start();
    }catch(error){document.querySelector('#status').textContent='Runtime failed: '+error.message;}
    </script></body></html>`
  const bundle = await build({ stdin: { contents: `import React from 'react';import{createRoot}from'react-dom/client';import{DesktopV3ArtifactV3Studio}from'./src/features/desktop/chat/components/desktop-v3-artifact-v3-studio';
    function App(){const[open,setOpen]=React.useState(true);return <><button id="reopen" onClick={()=>setOpen(true)}>Reopen</button><DesktopV3ArtifactV3Studio open={open} artifact={{artifactId:'artifact',ownerSessionId:'parent',label:'Playback fixture'}} onOpenChange={setOpen}/></>}createRoot(document.getElementById('root')).render(<App/>);`, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'], ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    page.setDefaultTimeout(7000)
    let failModule = false
    const moduleRequests: string[] = []
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: '<html><body><div id="root"></div></body></html>' })
      if (url.pathname.startsWith(prefix)) {
        moduleRequests.push(url.pathname + url.search)
        assert.equal(url.searchParams.get('revision'), revision)
        assert.equal(route.request().headers().origin, 'null')
        if (failModule) return route.fulfill({ status: 503, body: 'unavailable' })
        return route.fulfill({ contentType: 'text/javascript', headers: { 'Access-Control-Allow-Origin': 'null', 'Cache-Control': 'no-store' }, body: modules.get(url.pathname.split('/').at(-1)!)! })
      }
      if (url.pathname.endsWith('/preview/access/token')) return route.fulfill({ contentType: 'text/html', headers: { 'Content-Security-Policy': csp }, body: html })
      if (url.pathname.endsWith('/preview/access')) return route.fulfill({ json: { ok: true, preview_url: `/v3/sessions/parent/artifacts-v3/artifact/preview/access/token?revision=${revision}` } })
      if (url.pathname.endsWith('/revisions')) return route.fulfill({ json: { ok: true, revisions: [head] } })
      if (url.pathname.endsWith('/artifacts-v3/artifact')) return route.fulfill({ json: { ok: true, artifact: { id: 'artifact', owner_session_id: 'parent', head, parts, revisions: [head], turns: [] } } })
      return route.abort('blockedbyclient')
    })
    await page.goto('https://artifact.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    const frame = page.frameLocator('[data-artifact-v3-complete-preview]')
    const state = () => frame.locator('body').evaluate(() => (globalThis as any).fixtureState())
    const pixels = async () => createHash('sha256').update(await frame.locator('canvas').screenshot()).digest('hex')
    await frame.locator('#status').filter({ hasText: 'Playing' }).waitFor()
    assert.equal(await page.locator('iframe').getAttribute('sandbox'), 'allow-scripts')
    assert.equal(await page.locator('iframe').evaluate(el => (el as HTMLIFrameElement).contentDocument === null), true)
    const first = await pixels(); await page.waitForTimeout(250); const second = await pixels()
    assert.notEqual(first, second, 'elapsed canvas pixels change without seeking')
    assert.equal((await state()).seeks, 0)
    assert.ok(moduleRequests.includes(moduleURL('three.module.js')) && moduleRequests.includes(moduleURL('three.core.js')))
    await frame.locator('#play').click()
    const paused = await pixels(); const pausedState = await state(); await page.waitForTimeout(180)
    assert.equal(await pixels(), paused); assert.equal((await state()).time, pausedState.time)
    assert.equal(await page.locator('[data-artifact-v3-part="scene"]').getAttribute('aria-pressed'), 'false')
    await frame.locator('#seek').fill('4200'); assert.equal((await state()).time, 4200)
    const sought = await pixels(); assert.notEqual(sought, paused)
    await frame.locator('#play').click(); await page.waitForTimeout(180); assert.notEqual(await pixels(), sought)
    // Explicitly synthetic visibility signal: proves authored handler integration,
    // not OS/background-tab throttling in the installed browser.
    await frame.locator('body').evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange')) })
    const hidden = await state(); await page.waitForTimeout(180); assert.equal((await state()).time, hidden.time)
    await frame.locator('body').evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')) })
    await page.waitForTimeout(180); assert.ok((await state()).time > hidden.time)
    await page.emulateMedia({ reducedMotion: 'reduce' }); await frame.locator('#status').filter({ hasText: 'Paused' }).waitFor()
    const reduced = await pixels(); await page.waitForTimeout(180); assert.equal(await pixels(), reduced)
    await frame.locator('#play').click(); await page.waitForTimeout(180); assert.notEqual(await pixels(), reduced)
    await frame.locator('canvas').evaluate(el => { const gl = (el as HTMLCanvasElement).getContext('webgl2')!; gl.getExtension('WEBGL_lose_context')!.loseContext() })
    await frame.locator('#status').filter({ hasText: 'WebGL context lost' }).waitFor()
    const lost = await state(); await page.waitForTimeout(100); assert.equal((await state()).frames, lost.frames)
    await page.getByRole('button', { name: 'Close Artifact Studio', exact: true }).click(); await page.locator('iframe').waitFor({ state: 'detached' }); assert.equal(await page.locator('iframe').count(), 0)
    await page.locator('#reopen').click(); await frame.locator('#status').filter({ hasText: 'Paused' }).waitFor()
    assert.equal((await state()).time, 0); assert.equal((await state()).seeks, 0)
    await page.getByRole('button', { name: 'Close Artifact Studio', exact: true }).click(); failModule = true; await page.locator('#reopen').click()
    await frame.locator('#status').filter({ hasText: 'Runtime failed:' }).waitFor()
    assert.equal(await frame.locator('canvas').count(), 0)
    console.log('PASS: elapsed pixels, pause, seek/resume, synthetic visibility, reduced-motion/manual play, WebGL loss, reopen, module failure; actual Studio/selection/CSP, mocked API, software WebGL')
  } finally { await browser.close() }
})
