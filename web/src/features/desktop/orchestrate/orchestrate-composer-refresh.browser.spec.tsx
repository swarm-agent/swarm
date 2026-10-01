import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: OrchestratorChatComposer preserves scoped drafts and final speech,
// grows without hiding controls, and ContextRemaining never treats lifetime usage
// as occupancy. Browser DOM/input is the narrowest layer proving these contracts;
// speech events and submission are deterministic boundaries, not live mic proof.
// Fixed send geometry must survive textarea growth (32px desktop, 44px phone),
// preventing the oversized/stretching send regression at the production CSS boundary.
test('unified composer grows, isolates drafts, displays live speech and waits for final flush', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import{createRoot}from'react-dom/client';
    import{OrchestratorChatComposer}from'./src/features/desktop/orchestrate/OrchestrateView';
    import{ContextRemaining}from'./src/features/desktop/orchestrate/orchestrator-composer-surface';
    window.sent=[];window.SpeechRecognition=class {start(){window.speech=this;this.onstart?.()}stop(){window.stopped=true}abort(){}};
    function App(){const [id,setId]=useState('one');const [usage,setUsage]=useState({total_tokens:9000,context_window:10000});window.scope=setId;window.usage=setUsage;
      return <div className='swarm-section'><OrchestratorChatComposer key={id} sessionId={id} project={{id,name:id}} contextControls={<ContextRemaining usage={usage}/>} submitMessage={async op=>{window.sent.push(op)}}/></div>}
    createRoot(document.getElementById('root')).render(<App/>);` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://composer.test/')
    await page.addStyleTag({ content: await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8') })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByTestId('orchestrator-chat-input')
    await input.waitFor()
    assert.equal(await page.getByTestId('orchestrator-context-label').textContent(), 'Context unknown')
    await page.evaluate(() => (window as any).usage({ context_window: 10000, remaining_tokens: 2500 }))
    await page.getByText('25% context remaining').waitFor()
    const short = (await input.boundingBox())!.height
    await input.fill('Long draft\n'.repeat(100))
    await page.waitForFunction(() => document.querySelector('textarea')!.clientHeight > 100)
    assert.ok((await input.boundingBox())!.height > short)
    assert.ok((await input.boundingBox())!.height <= 844 * .32 + 1)
    const send = page.getByRole('button', { name: 'Send message', exact: true })
    assert.equal(await send.isVisible(), true)
    assert.equal((await send.boundingBox())!.height, 44)
    assert.equal((await send.boundingBox())!.width, 44)
    await page.setViewportSize({ width: 1280, height: 844 })
    assert.equal((await send.boundingBox())!.height, 32)
    assert.equal((await send.boundingBox())!.width, 32)
    await page.setViewportSize({ width: 390, height: 844 })
    await input.fill('Typed')
    await page.getByRole('button', { name: 'Start dictation' }).click()
    await page.evaluate(() => (window as any).speech.onresult({ resultIndex: 0, results: [{ isFinal: false, 0: { transcript: 'pending speech' } }] }))
    await page.getByRole('status').getByText('pending speech').waitFor()
    assert.equal(await input.inputValue(), 'Typed')
    await page.getByRole('button', { name: 'Send message', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).sent.length), 0)
    assert.equal(await page.evaluate(() => (window as any).stopped), true)
    await page.evaluate(() => { const s=(window as any).speech; const event={resultIndex:0,results:[{isFinal:true,0:{transcript:'final speech'}}]}; s.onresult(event);s.onresult(event);s.onend() })
    await page.waitForFunction(() => document.querySelector('textarea')!.value === 'Typed final speech')
    await page.evaluate(() => (window as any).scope('two'))
    await page.waitForFunction(() => document.querySelector('textarea')!.value === '')
    await input.fill('Other project')
    await page.evaluate(() => (window as any).scope('one'))
    await page.waitForFunction(() => document.querySelector('textarea')!.value === 'Typed final speech')
    await page.getByRole('button', { name: 'Send message', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('textarea')!.value === '')
    assert.equal(await page.evaluate(() => (window as any).sent.length), 1)
    assert.ok((await input.boundingBox())!.height <= short + 1)
  } finally { await browser.close() }
})
