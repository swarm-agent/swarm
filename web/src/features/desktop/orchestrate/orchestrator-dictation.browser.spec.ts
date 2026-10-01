import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Purpose: user-initiated browser dictation appends only final speech to the current
// authoritative draft, never sends it, and cannot cross project/session/submission
// boundaries. Threat: interim/replayed results overwrite typing or resurrect a sent
// draft; leaked capture writes into another conversation. Authority: real
// OrchestratorChatComposer, useOrchestratorDictation and orchestratorDrafts. A rendered
// browser with a controlled SpeechRecognition API is the narrowest deterministic
// layer proving controls, text, focus, errors and cleanup together. It does not prove
// real microphone hardware, browser permissions or transcription service availability.
test('Orchestrator dictation preserves drafts and isolates capture lifecycle', { timeout: 30000 }, async () => {
  const fixture = `import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
    import {OrchestratorChatComposer} from './src/features/desktop/orchestrate/OrchestrateView';
    import {orchestratorDrafts} from './src/features/desktop/orchestrate/integration-recovery';
    window.recovery=()=>orchestratorDrafts.append('beta:two','Recovery context');
    window.captures=[];window.sent=[];window.startFailure=false;
    class Recognition {
      starts=0;stops=0;aborts=0;
      constructor(){window.captures.push(this)}
      start(){this.starts++;if(window.startFailure)throw Error('device busy');this.onstart?.()}
      stop(){this.stops++}
      abort(){this.aborts++}
    }
    window.SpeechRecognition=Recognition;window.Recognition=Recognition;
    function App(){const [scope,setScope]=useState({session:'one',project:'alpha'});const [mounted,setMounted]=useState(true);
      window.scope=setScope;window.mount=setMounted;
      return mounted?<OrchestratorChatComposer sessionId={scope.session} project={{id:scope.project}}
        submitMessage={async op=>{window.sent.push(op.request.content);if(window.rejectSend)throw Error('submission failed');if(window.gateSend)await new Promise(resolve=>window.finishSend=resolve)}}/>:null;
    }
    createRoot(document.getElementById('root')).render(<App/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://dictation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByTestId('orchestrator-chat-input')
    const start = page.getByRole('button', { name: 'Start dictation', exact: true })
    const stop = page.getByRole('button', { name: 'Stop dictation', exact: true })
    await input.waitFor()
    assert.equal(await page.evaluate(() => (window as any).captures.length), 0)
    await input.fill('Existing text')
    await start.click()
    await page.getByRole('status').getByText('Listening…').waitFor()
    assert.equal(await stop.getAttribute('aria-pressed'), 'true')
    assert.equal(await input.evaluate(el => document.activeElement === el), true)
    await input.fill('Existing text plus typing')
    await page.evaluate(() => {
      const w = window as any; const r = w.captures.at(-1)
      w.late = r.onresult
      r.onresult({ resultIndex: 0, results: [{ isFinal: false, 0: { transcript: 'interim' } }] })
    })
    assert.equal(await input.inputValue(), 'Existing text plus typing')
    await page.evaluate(() => {
      const r = (window as any).captures.at(-1)
      const event = { resultIndex: 0, results: [{ isFinal: true, 0: { transcript: 'spoken words' } }] }
      r.onresult(event); r.onresult(event)
    })
    await page.waitForFunction(() => (document.querySelector('textarea') as HTMLTextAreaElement)?.value.endsWith('spoken words'))
    assert.equal(await input.inputValue(), 'Existing text plus typing spoken words')
    assert.deepEqual(await page.evaluate(() => (window as any).sent), [])
    await stop.click()
    assert.equal(await page.evaluate(() => (window as any).captures.at(-1).stops), 1)
    await page.evaluate(() => {
      const r = (window as any).captures.at(-1)
      r.onresult({ resultIndex: 1, results: [{ isFinal: true }, { isFinal: true, 0: { transcript: 'final flush' } }] })
      r.onend()
    })
    await start.waitFor()
    assert.equal(await input.inputValue(), 'Existing text plus typing spoken words final flush')
    assert.equal(await input.evaluate(el => document.activeElement === el), true)

    await start.click()
    await page.evaluate(() => { (window as any).late = (window as any).captures.at(-1).onresult })
    await input.press('Enter')
    await page.waitForFunction(() => (document.querySelector('textarea') as HTMLTextAreaElement)?.value === '')
    await page.evaluate(() => (window as any).late({ resultIndex: 0, results: [{ isFinal: true, 0: { transcript: 'late submitted speech' } }] }))
    assert.equal(await input.inputValue(), '')
    assert.deepEqual(await page.evaluate(() => (window as any).sent), ['Existing text plus typing spoken words final flush'])
    assert.equal(await page.evaluate(() => (window as any).captures.at(-1).aborts), 1)

    for (const scope of [{session:'two',project:'alpha'}, {session:'two',project:'beta'}]) {
      await input.fill('Keep original')
      await start.click()
      await page.evaluate(next => {
        const w = window as any; w.old = w.captures.at(-1); w.late = w.old.onresult; w.scope(next)
      }, scope)
      await page.waitForFunction(() => (document.querySelector('textarea') as HTMLTextAreaElement)?.value === '')
      await page.evaluate(() => (window as any).late({ resultIndex: 0, results: [{ isFinal: true, 0: { transcript: 'wrong scope' } }] }))
      assert.equal(await input.inputValue(), '')
      assert.equal(await page.evaluate(() => (window as any).old.aborts), 1)
    }
    await start.click()
    await page.evaluate(() => { const w = window as any; w.old = w.captures.at(-1); w.late = w.old.onresult; w.mount(false) })
    await input.waitFor({ state: 'detached' })
    await page.evaluate(() => { const w = window as any; w.late({ resultIndex: 0, results: [{ isFinal: true, 0: { transcript: 'after unmount' } }] }); w.mount(true) })
    await input.waitFor()
    assert.equal(await input.inputValue(), '')
    assert.equal(await page.evaluate(() => (window as any).old.aborts), 1)

    await input.fill('Retain on errors')
    await page.evaluate(() => { (window as any).SpeechRecognition = undefined })
    await start.click()
    await page.getByRole('alert').getByText(/not supported/).waitFor()
    assert.equal(await input.inputValue(), 'Retain on errors')
    await page.evaluate(() => { const w = window as any; w.webkitSpeechRecognition = w.Recognition })
    for (const [error, message] of [['not-allowed', 'permission was denied'], ['audio-capture', 'No microphone is available'], ['no-speech', 'No speech was detected']]) {
      await start.click()
      await page.evaluate(code => (window as any).captures.at(-1).onerror({ error: code }), error)
      await page.getByRole('alert').getByText(message, { exact: false }).waitFor()
      await start.waitFor()
      assert.equal(await input.inputValue(), 'Retain on errors')
      assert.equal(await page.evaluate(() => (window as any).captures.at(-1).aborts), 1)
    }
    await page.evaluate(() => { (window as any).startFailure = true })
    await start.click()
    await page.getByRole('alert').getByText(/could not start.*device busy/).waitFor()
    assert.equal(await input.inputValue(), 'Retain on errors')
    await page.evaluate(() => { const w = window as any; w.startFailure = false; w.rejectSend = true })
    await start.click()
    await input.press('Enter')
    await page.getByTestId('chat-send-error').getByText('submission failed').waitFor()
    assert.equal(await input.inputValue(), 'Retain on errors')
    assert.equal(await start.isEnabled(), true)

    await page.evaluate(() => { const w = window as any; w.rejectSend = false; w.gateSend = true })
    await start.click()
    await page.evaluate(() => { (window as any).late = (window as any).captures.at(-1).onresult })
    await input.press('Enter')
    await page.waitForFunction(() => !!(window as any).finishSend)
    assert.equal(await start.isDisabled(), true)
    assert.equal(await input.isDisabled(), true)
    await page.evaluate(() => {
      const w = window as any
      w.recovery()
      w.late({ resultIndex: 0, results: [{ isFinal: true, 0: { transcript: 'late during send' } }] })
      w.finishSend()
    })
    await page.waitForFunction(() => !(document.querySelector('textarea') as HTMLTextAreaElement)?.disabled)
    assert.equal(await input.inputValue(), 'Retain on errors\n\nRecovery context')
  } finally { await browser.close() }
})
