import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: one-message worker context is an explicit, removable, revision-bound
// metadata reference, consumed only after a successful V3 append. Threat: a failed
// append silently dropping the user's draft/context, a second send inheriting context,
// or an in-flight replacement being cleared by the prior send.
// Boundary: OrchestratorChatComposer -> createDesktopV3ExistingMessageOperation ->
// conversation submission. The rendered composer is the narrowest layer proving chip,
// draft, and request metadata together; server tests separately prove authorization.
test('rendered Orchestrator composer retains failed context and consumes only the successfully sent selection', { timeout: 30000 }, async () => {
  const fixture = `import React, {useRef, useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    window.sent=[]; window.rejectNext=false; window.gateNext=false; window.finishSend=null;
    function App(){
      const [worker,setWorker]=useState({id:'worker_123',revision:2,name:'Audit worker'});
      const current=useRef(worker); current.current=worker;
      window.selectWorker=(next)=>{current.current=next;setWorker(next)};
      const clear=(consumed)=>{if(current.current!==consumed)return;current.current=null;setWorker(null)};
      const submit=async(operation)=>{
        window.sent.push({content:operation.request.content,metadata:operation.request.metadata});
        if(window.rejectNext){window.rejectNext=false;throw Error('stale worker reference')}
        if(window.gateNext){window.gateNext=false;await new Promise((resolve)=>{window.finishSend=resolve})}
      };
      return <OrchestratorChatComposer sessionId='sess_1' selectedWorker={worker} currentSelectedWorker={()=>current.current} onDeselectWorker={clear} submitMessage={submit}/>;
    }
    createRoot(document.getElementById('root')).render(<App/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://worker.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const chip = page.getByTestId('composer-worker-context-chip')
    const input = page.getByTestId('orchestrator-chat-input')
    const send = page.getByRole('button', { name: 'Send message' })
    await chip.getByText(/Audit worker \(r2\)/).waitFor()
    await input.fill('Inspect this worker')
    await page.evaluate(() => { (window as any).rejectNext = true })
    await send.click()
    await page.getByTestId('chat-send-error').getByText('stale worker reference').waitFor()
    assert.equal(await input.inputValue(), 'Inspect this worker')
    assert.equal(await chip.count(), 1)
    assert.deepEqual((await page.evaluate(() => (window as any).sent))[0].metadata.selected_worker, { worker_id: 'worker_123', expected_revision: 2 })

    await send.click()
    await chip.waitFor({ state: 'detached' })
    assert.equal(await input.inputValue(), '')
    await input.fill('Follow up')
    await send.click()
    await page.waitForFunction(() => (document.querySelector('[data-testid="orchestrator-chat-input"]') as HTMLTextAreaElement)?.value === '')
    const sent = await page.evaluate(() => (window as any).sent)
    assert.equal(sent[1].content, 'Inspect this worker')
    assert.deepEqual(sent[1].metadata.selected_worker, { worker_id: 'worker_123', expected_revision: 2 })
    assert.equal(Object.hasOwn(sent[2].metadata, 'selected_worker'), false)

    await page.evaluate(() => { (window as any).selectWorker({ id: 'worker_old', revision: 3, name: 'Old worker' }); (window as any).gateNext = true })
    await chip.getByText(/Old worker \(r3\)/).waitFor()
    await input.fill('Old worker message')
    await send.click()
    await page.waitForFunction(() => !!(window as any).finishSend)
    await page.evaluate(() => { (window as any).selectWorker({ id: 'worker_new', revision: 4, name: 'New worker' }); (window as any).finishSend() })
    await chip.getByText(/New worker \(r4\)/).waitFor()
    assert.deepEqual((await page.evaluate(() => (window as any).sent))[3].metadata.selected_worker, { worker_id: 'worker_old', expected_revision: 3 })
    await input.fill('Replacement message')
    await send.click()
    await chip.waitFor({ state: 'detached' })
    assert.deepEqual((await page.evaluate(() => (window as any).sent))[4].metadata.selected_worker, { worker_id: 'worker_new', expected_revision: 4 })

    await page.evaluate(() => (window as any).selectWorker({ id: 'worker_remove', revision: 5, name: 'Removable' }))
    await chip.getByText(/Removable \(r5\)/).waitFor()
    await page.getByRole('button', { name: 'Remove selected worker' }).click()
    await chip.waitFor({ state: 'detached' })
    await input.fill('General question')
    await send.click()
    await page.waitForFunction(() => (window as any).sent.length === 6)
    assert.equal(Object.hasOwn((await page.evaluate(() => (window as any).sent))[5].metadata, 'selected_worker'), false)
  } finally { await browser.close() }
})
