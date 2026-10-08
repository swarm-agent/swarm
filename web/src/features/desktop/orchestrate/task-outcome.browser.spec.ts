import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: OrchestrateView's sidebar must not duplicate central task attention,
// for zero, one or many actionable tasks, or leave a spacer between navigation
// and conversations. Render the production JSX slot with bounded navigation doubles
// and real central outcome components: this is the narrowest DOM/layout check of
// that composition without booting the whole routed project and daemon runtime.
// Existing task-attention tests own permission and task lifecycle behavior.
test('sidebar omits task attention while central task outcomes remain visible', { timeout: 30000 }, async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const start = source.indexOf('        <ProjectNavigation activePage=')
  const end = source.indexOf('        <ProjectConversationSidebar ', start)
  assert.ok(start >= 0 && end > start)
  const slot = source.slice(start, end)
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React,{useState} from 'react';import {createRoot} from 'react-dom/client';
    import {TaskCardSummary} from './src/features/desktop/orchestrate/task-card-summary';
    import {TaskOutcomeDetails} from './src/features/desktop/orchestrate/task-outcome-view';
    import {mapBackendTask} from './src/features/desktop/state/desktop-projects-state';
    function ProjectNavigation({deliverableCount,mediaCount,onSelect}) {
      return <nav><button onClick={onSelect}>Tasks</button><span>Deliverables {deliverableCount}</span><span>Media {mediaCount}</span></nav>
    }
    function App(){const [count,setCount]=useState(0);const [selected,setSelected]=useState('');
      window.setCount=setCount;
      const liveTasks=Array.from({length:count},(_,i)=>mapBackendTask({id:'task-'+i,title:'Conflict '+i,agent:'coder',status:'needs_review',integration:{state:'conflict'},deliverables:[]}));
      const activeNavTab='home',selectedProjectSegment='project',routeConversationId=null,workspaceSlug='workspace',allMediaLibraryItems=[{}],projects=[{}];
      const setIsOnboardingActive=()=>{},responsiveLayout={setPanel:setSelected,setNavigationOpen:()=>{}};
      return <><aside>${slot}<div data-testid="conversations">Conversations</div></aside>
        <output>{selected}</output><main>{liveTasks.map(task=><article key={task.id}><TaskCardSummary task={task}/><TaskOutcomeDetails task={task}/></article>)}</main></>
    }
    createRoot(document.getElementById('root')).render(<App/>);
  ` }, bundle: true, write: false, platform: 'browser', format: 'iife', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setContent('<div id="root"></div>')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByTestId('conversations').waitFor()
    const anchor = await page.getByTestId('conversations').boundingBox()
    for (const count of [0, 1, 2, 6]) {
      await page.evaluate(count => (window as any).setCount(count), count)
      await page.waitForFunction(count => document.querySelectorAll('main article').length === count, count)
      assert.doesNotMatch(await page.locator('aside').innerText(), /tasks? needs? attention/i)
      assert.equal(await page.locator('aside [aria-label="Project task attention"]').count(), 0)
      assert.deepEqual(await page.locator('aside').evaluate(node => Array.from(node.children, child => child.tagName)), ['NAV', 'DIV'])
      assert.deepEqual(await page.getByTestId('conversations').boundingBox(), anchor)
      assert.match(await page.locator('aside').innerText(), /Deliverables 0/)
      assert.match(await page.locator('aside').innerText(), /Media 1/)
      if (count) assert.match(await page.locator('main').innerText(), /Integration conflict/)
    }
    await page.getByRole('button', { name: 'Tasks', exact: true }).click()
    assert.equal(await page.locator('output').innerText(), 'main')
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
