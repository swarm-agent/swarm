import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: the hidden AgentModelControl mounted by OrchestrateView must settle
// without local-state feedback, including when optional modelProfiles is omitted.
// Threat: a fresh default [] retriggers the closed-dialog effect, whose fresh
// selectedFavoriteIds [] schedules another render indefinitely. Exercise the real
// component and React effects in a bounded browser fixture, not source strings or
// a CPU benchmark. Network is denied; cached settings isolate the render contract.
test('closed model control settles with omitted profiles and fresh parent props', { timeout: 30_000 }, async () => {
  const bundle = await build({
    stdin: {
      contents: `import React,{Profiler,useState} from 'react';
        import{createRoot}from'react-dom/client';
        import{QueryClient,QueryClientProvider}from'@tanstack/react-query';
        import{AgentModelControl}from'./src/features/desktop/chat/components/agent-model-control';
        const client=new QueryClient({defaultOptions:{queries:{enabled:false,retry:false}}});
        const assignment={provider:'fixture',model:'fixture',thinking:'medium',serviceTier:'',contextMode:''};
        client.setQueryData(['agent-model-settings'],{swarm:{action:assignment,plan:assignment},systemAgents:Object.fromEntries(['compact','finder','coder','designer','router'].map(k=>[k,assignment])),updatedAt:1});
        window.commits=0;window.loop=false;
        const root=createRoot(document.getElementById('root'));
        function App(){const[n,setN]=useState(0);return <><button onClick={()=>setN(n+1)}>Parent {n}</button><Profiler id="control" onRender={()=>{if(++window.commits>40){window.loop=true;throw new Error('render budget exceeded')}}}><AgentModelControl currentAgent="swarm" selectedPrimaryAgent="swarm" agents={[]} selectedModel={null} modelOptions={[]} showTrigger={false}/></Profiler></>}
        root.render(<QueryClientProvider client={client}><App/></QueryClientProvider>);`,
      resolveDir: process.cwd(), loader: 'tsx',
    },
    bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent',
  })
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => route.abort())
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    const settle = () => page.evaluate(async () => {
      for (let i = 0; i < 8; i++) await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
      return { commits: (window as any).commits as number, loop: (window as any).loop as boolean }
    })
    await page.getByRole('button', { name: 'Parent 0' }).waitFor()
    const mounted = await settle()
    assert.equal(mounted.loop, false)
    assert.ok(mounted.commits < 10, `mount commits: ${mounted.commits}`)
    assert.equal((await settle()).commits, mounted.commits, 'idle effects must stop committing')
    await page.getByRole('button', { name: 'Parent 0' }).click()
    await page.getByRole('button', { name: 'Parent 1' }).waitFor()
    const rerendered = await settle()
    assert.equal(rerendered.loop, false)
    assert.ok(rerendered.commits - mounted.commits < 5)
    assert.equal((await settle()).commits, rerendered.commits, 'fresh parent arrays must also settle')
    assert.deepEqual(errors, [])
  } finally {
    await browser.close()
  }
})
