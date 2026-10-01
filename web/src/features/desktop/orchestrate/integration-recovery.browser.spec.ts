import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: copying recovery into the project composer appends, focuses and never
// sends; switching worker/project sidebars must not lose or misdirect drafts.
// Boundary: real OrchestratorChatComposer + draft store, with only submission replaced.
// A rendered browser is the narrowest layer proving focus and remount behavior.
test('recovery draft survives remount, stays scoped and requires explicit send', { timeout: 30000 }, async () => {
  const fixture = `import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    import {orchestratorDrafts} from './src/features/desktop/orchestrate/integration-recovery';
    window.sent=[]; window.copy=()=>orchestratorDrafts.append('project-a:orchestrator','Recovery brief');
    function App(){const [sid,setSid]=useState('orchestrator'); window.switchSession=setSid;
      return <OrchestratorChatComposer key={sid} sessionId={sid} project={{id:'project-a'}} submitMessage={async op=>{window.sent.push(op.request.content)}}/>;}
    createRoot(document.getElementById('root')).render(<App/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://recovery.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByTestId('orchestrator-chat-input')
    await input.fill('My existing draft')
    await page.evaluate(() => (window as any).switchSession('worker'))
    await page.waitForFunction(() => (document.querySelector('textarea') as HTMLTextAreaElement)?.value === '')
    await input.fill('Worker draft')
    await page.evaluate(() => (window as any).copy())
    assert.equal(await input.inputValue(), 'Worker draft')
    await page.evaluate(() => (window as any).switchSession('orchestrator'))
    await page.waitForFunction(() => (document.querySelector('textarea') as HTMLTextAreaElement)?.value === 'My existing draft\n\nRecovery brief')
    assert.equal(await input.evaluate(element => document.activeElement === element), true)
    assert.deepEqual(await page.evaluate(() => (window as any).sent), [])
    await page.evaluate(() => (window as any).switchSession('worker'))
    await page.waitForFunction(() => (document.querySelector('textarea') as HTMLTextAreaElement)?.value === 'Worker draft')
    await page.evaluate(() => (window as any).switchSession('orchestrator'))
    await page.waitForFunction(() => (document.querySelector('textarea') as HTMLTextAreaElement)?.value === 'My existing draft\n\nRecovery brief')
    await page.getByRole('button', { name: 'Send message' }).click()
    await page.waitForFunction(() => (window as any).sent.length === 1)
    assert.deepEqual(await page.evaluate(() => (window as any).sent), ['My existing draft\n\nRecovery brief'])
  } finally { await browser.close() }
})
