// Purpose: verify production-built main/router/admission recovery after failed assets,
// stalled HTTP headers/bodies, and retry; verify Git transport teardown across repeated
// component navigation using real browser fetch, Query observers and polling owners.
// Authorities: index.html, main.tsx, DesktopVaultShell, api.ts, git/api.ts and
// page-lifecycle.ts. An ephemeral loopback fake server (never the daemon) is the
// narrowest layer proving browser transport abort plus rendered built-app recovery.
// Requires a fresh Vite build; screenshots go only to caller-provided scratch.
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { join, resolve } from 'node:path'
import { test } from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

const dist = resolve('dist')
const html = await readFile(join(dist, 'index.html'), 'utf8')
const entry = html.match(/<script[^>]+src="([^"]+)"/)![1]
const gitFixture = await build({
  stdin: { loader: 'tsx', resolveDir: new URL('.', import.meta.url).pathname, contents: `
    import React, { useEffect, useState } from 'react'; import { createRoot } from 'react-dom/client';
    import { QueryClient, QueryObserver } from '@tanstack/react-query';
    import { fetchGitStatus, gitStatusQueryKey, startGitRealtime } from '../features/desktop/git/api';
    import { startPagePolling } from './page-lifecycle';
    const client = new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}});
    function Git({workspace}) {
      useEffect(() => {
        const observer = new QueryObserver(client, {queryKey:gitStatusQueryKey(workspace), queryFn:({signal})=>fetchGitStatus(workspace,12,'',signal)});
        const release = observer.subscribe(()=>{});
        const stop = startPagePolling(async signal => { await startGitRealtime(workspace,'','held',signal); return 5000; });
        return ()=>{ release(); stop(); };
      },[workspace]);
      return <p>Git {workspace}</p>;
    }
    function App(){const [n,setN]=useState(0);return <><button onClick={()=>setN(n+1)}>Next workspace</button><button onClick={()=>setN(-1)}>Close Git</button>{n>=0&&<Git workspace={'/fixture/'+n}/>}</>}
    createRoot(document.getElementById('root')).render(<App/>);` },
  bundle: true, write: false, format: 'esm', jsx: 'automatic', logLevel: 'silent',
})

test('built Desktop startup failure/retry and browser Git navigation aborts', { timeout: 100000 }, async (t) => {
  process.env.PW_TEST_SCREENSHOT_NO_FONTS_READY = '1'
  let mode = 'healthy'
  let documents = 0
  let targeted = 0
  let closed = 0
  let activeGit = 0
  let openedGit = 0
  const server = createServer(async (req, res) => {
    const path = new URL(req.url!, 'http://fixture.invalid').pathname
    const json = (value: unknown) => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(value)) }
    if (path === '/' || path === '/git-fixture') {
      documents++
      res.setHeader('Content-Type', 'text/html')
      res.end(path === '/' ? html : '<div id="root"></div><script type="module" src="/git-fixture.js"></script>')
      return
    }
    if (path === '/git-fixture.js') { res.setHeader('Content-Type','text/javascript'); res.end(gitFixture.outputFiles[0].text); return }
    if (path.startsWith('/v1/workspace/git/')) {
      activeGit++; openedGit++
      res.on('close', () => { activeGit-- })
      return
    }
    const target = mode.startsWith('origin') ? '/v1/onboarding/tailscale-origin' : '/v1/onboarding'
    if (path === target && (mode.endsWith('headers') || mode.endsWith('body'))) {
      targeted++
      res.on('close', () => { closed++ })
      if (mode.endsWith('body')) { res.writeHead(200, { 'Content-Type': 'application/json' }); res.write('{"ok":') }
      return
    }
    if (path === '/v1/onboarding/tailscale-origin') { json({required:false}); return }
    if (path === '/v1/onboarding') { json({ok:true,needs_onboarding:false,vault:{enabled:true,unlocked:false}}); return }
    if (path === '/v1/vault') { json({enabled:true,unlocked:false}); return }
    if (path.startsWith('/v1/') || path.startsWith('/v3/')) { json({}); return }
    if ((mode === 'script-stall' && path === entry) || (mode === 'chunk-fail' && path.endsWith('.js') && path !== entry)) {
      targeted++
      if (mode === 'chunk-fail') { res.writeHead(503); res.end('fixture chunk unavailable') }
      return
    }
    if (!/^\/(assets\/[a-zA-Z0-9_.-]+|favicon\.svg|sw\.js|manifest\.webmanifest)$/.test(path)) { res.writeHead(404); res.end(); return }
    try {
      const body = await readFile(join(dist, path.slice(1)))
      res.setHeader('Content-Type', path.endsWith('.js') ? 'text/javascript' : path.endsWith('.css') ? 'text/css' : 'application/octet-stream')
      res.end(body)
    } catch { res.writeHead(404); res.end() }
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  t.after(() => { server.closeAllConnections(); server.close() })
  const address = server.address() as { port: number }
  const origin = `http://127.0.0.1:${address.port}`
  const browser = await chromium.launch({ headless:true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? {executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH} : {}) })
  t.after(() => browser.close())
  const context = await browser.newContext({serviceWorkers:'block',viewport:{width:1000,height:700}})
  await context.route('**/*', route => route.request().url().startsWith(origin + '/') ? route.continue() : route.abort())
  async function until(check: () => boolean) {
    for (let i=0;i<100;i++) { if(check()) return; await new Promise(resolve=>setTimeout(resolve,20)) }
    assert.ok(check(), 'server transport postcondition')
  }
  for (const failure of ['script-stall','chunk-fail','origin-headers','origin-body','onboarding-headers','onboarding-body','healthy']) {
    mode=failure; targeted=0; closed=0
    const beforeDocuments=documents
    const page=await context.newPage()
    page.setDefaultTimeout(7000)
    await page.clock.install()
    await page.goto(origin, {waitUntil:'commit'})
    if (failure !== 'healthy') await until(()=>targeted>0)
    if (failure === 'script-stall') {
      await page.clock.fastForward(20000)
      await page.getByText('Swarm is taking longer than expected').waitFor()
    } else if (failure === 'chunk-fail') {
      await page.getByText('Swarm could not load').waitFor()
    } else if (failure !== 'healthy') {
      // Wait for response body consumption as well as the request to reach server.
      await page.waitForTimeout(1)
      await page.clock.fastForward(15000)
      await page.getByText(failure.startsWith('origin') ? 'Unable to verify this desktop address.' : 'Unable to load Swarm onboarding.').waitFor()
      await page.getByText(/timed out/i).waitFor()
      await until(()=>closed>0)
      assert.equal(await page.locator('#swarm-startup').isVisible(),false)
      const colors = await page.getByText(/timed out/i).evaluate(el => [getComputedStyle(el).color, getComputedStyle(el.parentElement!).backgroundColor])
      const channels = (value: string) => value.match(/[\d.]+/g)!.map(Number)
      const luminance = (rgb: number[]) => rgb.slice(0,3).map(v => v/255).map(v => v <= 0.04045 ? v/12.92 : ((v+0.055)/1.055)**2.4).reduce((sum,v,i)=>sum+v*[0.2126,0.7152,0.0722][i],0)
      const fg = channels(colors[0]), bg = channels(colors[1])
      // This admission card is rendered over the shell's explicit black backdrop.
      const a = bg[3] ?? 1
      const l1=luminance(fg), l2=luminance(bg.slice(0,3).map(v=>v*a))
      const contrast = (Math.max(l1,l2)+0.05)/(Math.min(l1,l2)+0.05)
      assert.ok(contrast >= 4.5, `startup error text contrast ${contrast}`)
    }
    if (failure === 'healthy') {
      await page.getByRole('button', {name:/unlock/i}).first().waitFor()
      assert.equal(await page.locator('#swarm-startup').isVisible(),false)
    } else {
      const retry = failure === 'script-stall' || failure === 'chunk-fail' ? page.locator('#swarm-startup-retry') : page.getByRole('button',{name:'Try again'})
      await retry.focus()
      assert.equal(await retry.evaluate(el=>el===document.activeElement),true)
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true)
      await page.screenshot({path:join(process.env.TMPDIR!,`built-${failure}.png`)})
      if(failure==='onboarding-body') {
        await page.setViewportSize({width:320,height:568})
        await page.screenshot({path:join(process.env.TMPDIR!,'built-onboarding-mobile.png')})
        assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true)
      }
      await page.evaluate(()=>{localStorage.setItem('fixture-retained','yes');sessionStorage.setItem('fixture-retained','yes')})
      mode='healthy'
      await page.keyboard.press('Enter')
      await page.getByRole('button', {name:/unlock/i}).first().waitFor()
      assert.equal(await page.locator('#swarm-startup').isVisible(),false)
      assert.deepEqual(await page.evaluate(()=>[localStorage.getItem('fixture-retained'),sessionStorage.getItem('fixture-retained')]),['yes','yes'])
    }
    await page.clock.fastForward(60000)
    assert.equal(await page.locator('#swarm-startup').isVisible(),false)
    assert.equal(documents-beforeDocuments, failure==='script-stall'||failure==='chunk-fail'?2:1, 'no automatic reload')
    await page.close()
    console.log(`PASS built browser: ${failure}, recovery and keyboard retry`)
  }
  mode='git'
  const page=await context.newPage()
  await page.goto(origin+'/git-fixture')
  await until(()=>activeGit===2)
  for(let n=1;n<=8;n++) {
    await page.getByRole('button',{name:'Next workspace'}).click()
    await until(()=>openedGit===(n+1)*2 && activeGit===2)
  }
  await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pagehide')))
  await until(()=>activeGit===1)
  await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pageshow')))
  await until(()=>activeGit===2)
  await page.getByRole('button',{name:'Close Git'}).click()
  await until(()=>activeGit===0)
  await page.close()
  console.log('PASS browser Git fixture: 8 workspace switches, page lifecycle, teardown; all obsolete HTTP requests closed')
  await context.close()
})
