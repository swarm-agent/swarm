import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: DesktopV3ChatHeader and AgentModelControl must expose favorites from
// the project header, preserve selection failures, and route Agents to the owning
// Orchestrator surface rather than opening legacy setup. This rendered component
// boundary is the narrowest test of clicks/keyboard/portal routing; it does not
// claim backend persistence or provider execution coverage.
test('project header opens favorites and routes Agents without legacy setup', { timeout: 30000 }, async () => {
  const fixture = `import React,{useState} from 'react';
    import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {DesktopV3ChatHeader} from './src/features/desktop/chat/components/desktop-v3-chat-header';
    import {AgentModelControl} from './src/features/desktop/chat/components/agent-model-control';
    const client=new QueryClient({defaultOptions:{queries:{enabled:false,retry:false}}});
    const favorite={profileId:'favorite',name:'Favorite',provider:'fixture',model:'chosen',thinking:'',serviceTier:'',contextMode:''};
    window.applied=[];window.agents=0;window.reject=true;
    function App(){const [signal,setSignal]=useState(0);const [model,setModel]=useState('initial');
      return <QueryClientProvider client={client}>
        <DesktopV3ChatHeader title='Project Atlas' workspaceName='Workspace' modelLabel={model} onOpenModelFavorites={()=>setSignal(s=>s+1)} modelFavoritesAnchorId='header'/>
        <AgentModelControl currentAgent='system-orchestrator' selectedPrimaryAgent='system-orchestrator' agents={[]} modelOptions={[]} selectedModel={null} modelProfiles={[favorite]} showTrigger={false} openSignal={signal} popoverAnchorId='header' onOpenAgents={()=>window.agents++} onApplyModelFavoriteChatOnly={async p=>{if(window.reject)throw Error('Selection rejected');window.applied.push(p);setModel(p.model)}}/>
      </QueryClientProvider>}
    createRoot(document.getElementById('root')).render(<App/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 440, height: 844 } })
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://header.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    assert.equal(await page.getByRole('heading', { name: 'Project Atlas' }).count(), 2)
    const trigger = page.getByRole('button', { name: 'Model favorites: initial' })
    await trigger.focus()
    await page.keyboard.press('Enter')
    await page.getByRole('menu', { name: 'Model favorites' }).waitFor()
    await page.getByRole('button', { name: 'Use Favorite in this chat only' }).click()
    await page.getByText('Selection rejected', { exact: true }).waitFor()
    assert.deepEqual(await page.evaluate(() => (window as any).applied), [])
    assert.equal(await trigger.count(), 1, 'failure leaves canonical label unchanged')
    await page.evaluate(() => { (window as any).reject = false })
    await page.getByRole('button', { name: 'Use Favorite in this chat only' }).click()
    await page.getByRole('button', { name: 'Model favorites: chosen' }).waitFor()
    assert.equal(await page.evaluate(() => (window as any).applied.length), 1)
    await page.getByRole('button', { name: 'Model favorites: chosen' }).click()
    await page.getByRole('button', { name: 'Agents', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).agents), 1)
    assert.equal(await page.getByRole('dialog', { name: 'Agent and model settings' }).count(), 0)
    assert.equal(await page.getByRole('menu', { name: 'Model favorites' }).count(), 0)
  } finally {
    await browser.close()
  }
})
