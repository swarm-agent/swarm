import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: DeliverablePreview must execute self-contained motion and controls but
// never grant authored HTML/SVG the host origin or a network/navigation channel.
// Real Chromium at the component boundary is the narrowest layer that can prove
// CSP, sandbox, animation timelines and semantic Markdown; no provider is mocked
// or benchmarked here. Requests are observed, not merely expected to reject.
test('live deliverables animate, Markdown is semantic, hostile documents remain isolated', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {DeliverablePreview} from './src/features/desktop/tools/media-library/deliverable-preview';
    const root=createRoot(document.getElementById('root'));
    window.show=(content,markdown=false)=>root.render(<DeliverablePreview content={content} markdown={markdown} title="Preview"/>);
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', jsx: 'automatic' })
  const browser = await chromium.launch({ channel: process.env.SWARM_TEST_BROWSER_CHANNEL || undefined })
  try {
    const page = await browser.newPage(); page.setDefaultTimeout(4000)
    const requests: string[] = []
    await page.route('**/*', route => {
      const url = route.request().url()
      if (url === 'http://localhost/fixture') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      requests.push(url); return route.abort()
    })
    await page.goto('http://localhost/fixture')
    await page.evaluate(() => { localStorage.setItem('secret', 'host-only'); (window as any).__TAURI_INTERNALS__ = { secret: 'host-only' } })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const show = (content: string, markdown = false) => page.evaluate(({ content, markdown }) => (window as any).show(content, markdown), { content, markdown })
    const inner = () => page.frameLocator('iframe[title="Preview"]').frameLocator('iframe[title="Live deliverable"]')
    await show(`<style>@keyframes move{to{transform:translateX(100px)}}#motion{animation:move 1s linear infinite}</style>
      <div id="motion">CSS</div><svg width="200" height="100"><circle id="dot" r="5" cy="20"><animate attributeName="cx" values="10;150;10" dur="1s" repeatCount="indefinite"/></circle></svg>
      <button onclick="this.textContent='clicked'">Local control</button><output id="ticks">0</output>
      <script>let n=0;function tick(){document.getElementById('ticks').textContent=++n;requestAnimationFrame(tick)}tick()</script>`)
    await inner().getByRole('button').click()
    assert.equal(await inner().getByRole('button').textContent(), 'clicked')
    const frames = await inner().locator('#motion').evaluate(async element => {
      // Keep the browser closure self-contained: tsx injects its Node-only __name
      // helper for nested named functions, which Playwright cannot serialize.
      const a = [getComputedStyle(element).transform, document.querySelector<SVGCircleElement>('#dot')!.cx.animVal.value, Number(document.querySelector('#ticks')!.textContent)]
      for (let i = 0; i < 8; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
      return [a, [getComputedStyle(element).transform, document.querySelector<SVGCircleElement>('#dot')!.cx.animVal.value, Number(document.querySelector('#ticks')!.textContent)]]
    })
    for (let i = 0; i < 3; i++) assert.notEqual(frames[0][i], frames[1][i])
    await show('<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100"><circle id="standalone" r="5" cy="20"><animate attributeName="cx" values="10;150;10" dur="1s" repeatCount="indefinite"/></circle></svg>')
    const svgFrames = await inner().locator('#standalone').evaluate(async element => {
      const circle = element as SVGCircleElement; const first = circle.cx.animVal.value
      for (let i = 0; i < 8; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
      return [first, circle.cx.animVal.value]
    })
    assert.notEqual(svgFrames[0], svgFrames[1])
    let popups = 0; page.on('popup', () => popups++)
    await show(`<base href="http://localhost/v3/"><meta http-equiv="Content-Security-Policy" content="default-src * 'unsafe-inline'">
      <script>
      const denied={}; for(const [key,fn] of Object.entries({dom:()=>top.document.body,storage:()=>localStorage.secret,cookie:()=>document.cookie,bridge:()=>top.__TAURI_INTERNALS__.secret,nav:()=>top.location='http://localhost/escaped'})){try{fn();denied[key]=false}catch{denied[key]=true}}
      denied.popup=window.open('http://localhost/popup')===null;
      Promise.all(['http://localhost/v3/sessions','https://example.invalid/leak','relative'].map(url=>fetch(url).then(()=>false,()=>true))).then(values=>{denied.fetch=values.every(Boolean);document.body.dataset.result=JSON.stringify(denied)});
      top.postMessage({protocol:'swarm-artifact-player',ok:true,result:{secret:'attack'}},'*');
      </script><img src="https://example.invalid/image"><svg onload="fetch('http://localhost/svg').catch(()=>{})"></svg>`)
    await inner().locator('body[data-result]').waitFor()
    const denied = JSON.parse((await inner().locator('body').getAttribute('data-result'))!)
    assert.deepEqual(denied, { dom: true, storage: true, cookie: true, bridge: true, nav: true, popup: true, fetch: true })
    // A document can normally navigate itself even in sandbox; the outer frame's
    // immutable frame-src policy must stop meta refresh and script navigation.
    await inner().locator('body').evaluate(() => { location.href = 'http://localhost/v3/sessions' })
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
    assert.equal(page.url(), 'http://localhost/fixture'); assert.equal(popups, 0); assert.deepEqual(requests, [])
    assert.equal(await page.evaluate(() => localStorage.secret), 'host-only')
    await show('# Readable plan\n\nParagraph.\n\n- First\n- Second\n\n```js\nconst x = 1\n```\n\n| Name | Value |\n| --- | --- |\n| A | B |\n\n[Docs](https://example.invalid)\n\n<script>window.compromised=true</script>\n\n[Bad](javascript:alert(1))', true)
    await page.getByRole('heading', { name: 'Readable plan' }).waitFor()
    assert.equal(await page.locator('article li').count(), 2)
    // The shared MarkdownRenderer preserves pipe tables as paragraph text; it
    // does not implement a table block. Require readable retained content.
    assert.match(await page.locator('article').innerText(), /\| Name \| Value \|/)
    assert.equal(await page.locator('article pre code').count(), 1)
    assert.equal(await page.locator('article a[href^="javascript:"]').count(), 0)
    assert.equal(await page.locator('iframe').count(), 0)
    assert.equal(await page.evaluate(() => 'compromised' in window), false)
    assert.deepEqual(requests, [])
  } finally { await browser.close() }
})
