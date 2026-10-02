import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium, type Page } from 'playwright'

// Purpose: live speech must be replaceable display state, while only unique finals
// append to the latest typed draft. Late callbacks must not cross cancel, send,
// disabled, unmount or project/session boundaries. Authority: useOrchestratorDictation
// owns recognition identity and functional draft updates. Rendering the real hook
// with a controlled browser speech API is the narrowest deterministic lifecycle
// test; this does not verify microphone hardware or provider transcription.
test('live dictation separates interim revisions from canonical drafts and invalidates late events', { timeout: 30000 }, async () => {
  const fixture = `import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {useOrchestratorDictation} from './src/features/desktop/orchestrate/use-orchestrator-dictation';
    window.captures=[];window.sent=[];
    class Recognition {
      stops=0;aborts=0;
      constructor(){window.captures.push(this)}
      start(){if(window.startFailure)throw Error('device busy');this.onstart?.()}
      stop(){if(window.stopFailure)throw Error('stop failed');this.stops++}
      abort(){this.aborts++}
    }
    window.SpeechRecognition=Recognition;window.Recognition=Recognition;
    function Composer({scope,disabled}){
      const [draft,setDraft]=useState('');
      const d=useOrchestratorDictation(scope,disabled,setDraft);
      window.dictation=d;
      return <><textarea aria-label="Draft" value={draft} onChange={e=>setDraft(e.target.value)}/>
        <output data-testid="interim">{d.interimText}</output>
        <output data-testid="active">{String(d.active)}</output>
        <output data-testid="error">{d.error}</output>
        <button onClick={d.toggle}>Toggle</button><button onClick={d.cancel}>Cancel</button>
        <button disabled={d.active||disabled} onClick={()=>{d.cancel();window.sent.push(draft);setDraft('')}}>Send</button>
      </>;
    }
    function App(){const [scope,setScope]=useState('alpha:one');const [disabled,setDisabled]=useState(false);const [mounted,setMounted]=useState(true);
      window.scope=setScope;window.disable=setDisabled;window.mount=setMounted;
      return mounted?<Composer scope={scope} disabled={disabled}/>:null;
    }
    createRoot(document.getElementById('root')).render(<App/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }))
    await page.goto('https://dictation.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const input = page.getByRole('textbox', { name: 'Draft' })
    const toggle = page.getByRole('button', { name: 'Toggle', exact: true })
    const send = page.getByRole('button', { name: 'Send', exact: true })
    const state = async (interim: string, active = true) => {
      await page.waitForFunction(({ interim, active }) => {
        const d = (window as any).dictation
        return d.interimText === interim && d.active === active
      }, { interim, active })
      assert.equal(await page.getByTestId('interim').textContent(), interim)
      assert.equal(await page.getByTestId('active').textContent(), String(active))
    }
    await input.waitFor()
    assert.equal(await page.evaluate(() => (window as any).captures.length), 0)
    await input.fill('Typed')
    await toggle.click()
    assert.equal(await page.evaluate(() => (window as any).captures.at(-1).interimResults), true)
    await emit(page, [[false, 'a rough guess']])
    await state('a rough guess')
    await emit(page, [[false, 'a better guess'], [false, 'next words']])
    await state('a better guess next words')
    await emit(page, [[false, 'a better guess']])
    await state('a better guess')
    assert.equal(await input.inputValue(), 'Typed')
    await input.fill('Edited while listening')
    await emit(page, [[true, 'confirmed'], [false, 'next words']])
    await state('next words')
    await emit(page, [[true, 'confirmed'], [false, 'revised next']], 1)
    await state('revised next')
    await emit(page, [[true, 'confirmed'], [false, 'revised next']])
    assert.equal(await input.inputValue(), 'Edited while listening confirmed')
    assert.equal(await send.isDisabled(), true)
    await toggle.click()
    assert.equal(await page.evaluate(() => (window as any).captures.at(-1).stops), 1)
    await state('revised next')
    assert.equal(await send.isDisabled(), true) // Stop is not yet a finalized draft.
    await emit(page, [[true, 'confirmed'], [true, 'final flush']], 1)
    await state('')
    await page.evaluate(() => {
      const r = (window as any).captures.at(-1)
      ;(window as any).late = r.onresult
      r.onend()
    })
    await state('', false)
    assert.equal(await input.inputValue(), 'Edited while listening confirmed final flush')
    await send.click()
    await page.evaluate(() => (window as any).late({ resultIndex: 0, results: [{ isFinal: true, 0: { transcript: 'late sent' } }] }))
    assert.equal(await input.inputValue(), '')
    assert.deepEqual(await page.evaluate(() => (window as any).sent), ['Edited while listening confirmed final flush'])

    for (const boundary of ['cancel', 'disabled', 'session', 'project', 'unmount']) {
      await input.fill('Keep typing')
      await toggle.click()
      await emit(page, [[false, 'pending']])
      await state('pending')
      await page.evaluate(boundary => {
        const w = window as any
        w.old = w.captures.at(-1)
        w.late = w.old.onresult
        w.lateStart = w.old.onstart
        w.lateEnd = w.old.onend
        w.lateError = w.old.onerror
        if (boundary === 'cancel') w.dictation.cancel()
        if (boundary === 'disabled') w.disable(true)
        if (boundary === 'session') w.scope('alpha:two')
        if (boundary === 'project') w.scope('beta:two')
        if (boundary === 'unmount') w.mount(false)
      }, boundary)
      if (boundary === 'unmount') await input.waitFor({ state: 'detached' })
      else await state('', false)
      await page.evaluate(() => {
        const w = window as any
        w.late({ resultIndex: 0, results: [{ isFinal: true, 0: { transcript: 'forbidden final' } }, { isFinal: false, 0: { transcript: 'forbidden interim' } }] })
        w.lateStart(); w.lateError({ error: 'network' }); w.lateEnd()
      })
      assert.equal(await page.evaluate(() => (window as any).old.aborts), 1)
      if (boundary === 'unmount') {
        await page.evaluate(() => (window as any).mount(true))
        await input.waitFor()
      }
      await state('', false)
      assert.equal(await input.inputValue(), boundary === 'unmount' ? '' : 'Keep typing')
      assert.equal(await page.getByTestId('error').textContent(), '')
      if (boundary === 'disabled') {
        const count = await page.evaluate(() => (window as any).captures.length)
        await toggle.click()
        assert.equal(await page.evaluate(() => (window as any).captures.length), count)
        await page.evaluate(() => (window as any).disable(false))
      }
    }

    await input.fill('Retain on error')
    await page.evaluate(() => { const w = window as any; w.SpeechRecognition = undefined; w.webkitSpeechRecognition = undefined })
    await toggle.click()
    await page.getByTestId('error').filter({ hasText: 'not supported' }).waitFor()
    await page.evaluate(() => { const w = window as any; w.webkitSpeechRecognition = w.Recognition })
    for (const [code, message] of [['not-allowed', 'permission was denied'], ['audio-capture', 'No microphone'], ['no-speech', 'No speech'], ['network', 'could not connect']]) {
      await toggle.click()
      await emit(page, [[false, 'discard on failure']])
      await state('discard on failure')
      await page.evaluate(code => (window as any).captures.at(-1).onerror({ error: code }), code)
      await state('', false)
      await page.getByTestId('error').filter({ hasText: message }).waitFor()
      assert.equal(await input.inputValue(), 'Retain on error')
    }
    await page.evaluate(() => { (window as any).startFailure = true })
    await toggle.click()
    await page.getByTestId('error').filter({ hasText: 'could not start' }).waitFor()
    await state('', false)
    await page.evaluate(() => { const w = window as any; w.startFailure = false; w.stopFailure = true })
    await toggle.click()
    await emit(page, [[false, 'pending stop failure']])
    await state('pending stop failure')
    await toggle.click()
    await state('', false)
    await page.getByTestId('error').filter({ hasText: 'failed to stop' }).waitFor()
    assert.equal(await input.inputValue(), 'Retain on error')
    assert.equal(await page.evaluate(() => (window as any).captures.at(-1).aborts), 1)
  } finally { await browser.close() }
})

async function emit(page: Page, segments: [boolean, string][], resultIndex = 0) {
  await page.evaluate(({ segments, resultIndex }) => {
    const r = (window as any).captures.at(-1)
    r.onresult({ resultIndex, results: segments.map(([isFinal, transcript]) => ({ isFinal, 0: { transcript } })) })
  }, { segments, resultIndex })
}
