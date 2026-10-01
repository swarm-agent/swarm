// Purpose: on-demand help must work with hover, keyboard and touch without submitting;
// scenes must survive disclosure/re-render. Threat: inaccessible title-only help or lost
// optional input. Authority: MediaTaskHelp/Scenes/Select/Default and ImagePromptControls.
// Narrow layer: isolated real React DOM in Chromium; no daemon/provider or live media.
import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

test('compact media help, consent and scene controls work at narrow widths', { timeout: 30000 }, async () => {
  const fixture = `import React,{useState,useReducer} from 'react';import {createRoot} from 'react-dom/client';
    import {MediaTaskHelp,MediaTaskScenes,MediaTaskSelect,MediaTaskDefault} from './src/features/desktop/orchestrate/media-task-controls';
    import {ImagePromptControls,imagePromptReducer,initialImagePromptState} from './src/features/desktop/orchestrate/image-task-prompt';
    window.saves=0;window.submits=0;
    function App(){const [scenes,setScenes]=useState('');const [state,dispatch]=useReducer(imagePromptReducer,initialImagePromptState);
      return <form onSubmit={event=>{event.preventDefault();window.submits++}}>
        <MediaTaskHelp label="Generation help">Router only runs with consent.</MediaTaskHelp>
        <MediaTaskSelect label="Images" value={state.count} values={[1,2,4,5,10,25]} onChange={value=>dispatch({type:'count',count:Number(value)})}/>
        <ImagePromptControls count={state.count} aiVariants={state.aiVariants} onChange={aiVariants=>dispatch({type:'choice',aiVariants})}/>
        <MediaTaskScenes value={scenes} onChange={setScenes}/>
        <MediaTaskDefault isDefault={false} onSave={()=>window.saves++}/>
        <button type="button">Outside help</button>
      </form>};createRoot(document.getElementById('root')).render(<App/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 320, height: 700 }, hasTouch: true })
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    const help = page.getByRole('button', { name: 'Generation help', exact: true })
    const note = page.getByText('Router only runs with consent.', { exact: false })
    assert.equal(await note.count(), 0)
    await help.hover()
    await note.waitFor()
    await page.mouse.move(319, 699)
    await note.waitFor({ state: 'hidden' })
    await help.focus()
    await note.waitFor()
    await help.press('Escape')
    await note.waitFor({ state: 'hidden' })
    await help.tap()
    await note.waitFor()
    await page.getByRole('button', { name: 'Close Generation help', exact: true }).tap()
    await note.waitFor({ state: 'hidden' })
    await help.focus()
    await page.getByRole('button', { name: 'Outside help' }).focus()
    await note.waitFor({ state: 'hidden' })
    assert.equal(await page.getByRole('radio', { name: 'AI variants', exact: true }).isDisabled(), true)
    await page.getByLabel('Images', { exact: true }).selectOption('4')
    assert.equal(await page.getByRole('radio', { name: 'Same prompt', exact: true }).isChecked(), true)
    await page.getByRole('radio', { name: 'AI variants', exact: true }).check()
    await page.getByLabel('Images', { exact: true }).selectOption('1')
    await page.getByLabel('Images', { exact: true }).selectOption('25')
    assert.equal(await page.getByRole('radio', { name: 'AI variants', exact: true }).isChecked(), false)
    await page.locator('summary').click()
    await page.getByLabel('Scene prompts', { exact: true }).fill('Dawn\nNight')
    await page.locator('summary').click() // Closing does not remove controlled data.
    await page.getByLabel('Images', { exact: true }).selectOption('2')
    assert.equal(await page.getByLabel('Scene prompts', { exact: true }).inputValue(), 'Dawn\nNight')
    assert.equal(await page.evaluate(() => (window as any).saves), 0)
    await page.getByRole('button', { name: 'Set default', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).saves), 1)
    assert.equal(await page.evaluate(() => (window as any).submits), 0)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
  } finally { await browser.close() }
})
