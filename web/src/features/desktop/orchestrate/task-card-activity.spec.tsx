// Purpose: one semantic activity row must stay mounted and one line tall through
// streaming updates, without stale running UI after termination. Threat: token-tail
// tickers, wrapping badges and duplicate summary rows make task cards jump.
// Boundaries: TaskCardActivity and MinimalTaskCard wiring. SSR checks labels;
// a real-browser component fixture checks rerenders/geometry/focus (not daemon
// streaming performance). The source wiring check complements, not replaces, it.
import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { compile } from 'tailwindcss'
import { TaskCardActivity } from './task-card-activity'
import type { RunningTask } from './orchestrate-types'

const task: RunningTask = {
  id: 'activity-task', title: 'Inspect source', agentType: 'coder', status: 'running',
  workspaceTarget: 'repository', elapsed: '1s', subtasks: [{ id: 'step', title: 'Inspect', completed: false }],
}
const markup = (extra: Partial<RunningTask> = {}) => renderToStaticMarkup(<TaskCardActivity task={{ ...task, ...extra }} />)

test('activity uses resolved identity and normalized tools without historical or token text', () => {
  for (const [tool, label] of [['read', 'read'], ['edit', 'edit'], ['search', 'search'], ['', 'Thinking']]) {
    const html = markup({ currentTool: tool, currentFocus: 'Old focus', toolActivitySummary: 'read ×99', liveAssistantText: 'token tail' })
    assert.match(html, new RegExp(`>${label}</span>`))
    assert.equal((html.match(/role="status"/g) || []).length, 1)
    assert.doesNotMatch(html, /Old focus|read ×99|token tail|Current Focus|🎯|animate-|Working/)
  }
  assert.match(markup({ currentTool: 'read src/main.ts', toolCallCount: 2 }), /call 2/)
  assert.match(markup({ currentTool: 'read src/main.ts' }), /src\/main.ts/)
  assert.match(markup({ agentType: '' }), /Thinking/)
  for (const status of ['needs_review', 'failed', 'cancelled', 'blocked', 'completed', 'paused']) {
    assert.equal(markup({ status: status as RunningTask['status'], currentTool: 'read' }), '')
  }
})

// Purpose: activityLabel must only resolve own toolLabels entries, preserving
// unknown-tool formatting instead of rendering inherited functions/objects.
// SSR is the narrowest observable TaskCardActivity boundary; the existing ES2020
// TypeScript build separately guards against unsupported standard-library APIs.
test('activity labels ignore inherited properties while preserving known and unknown tools', () => {
  for (const [tool, label] of [
    ['git_status', 'git_status'],
    ['custom-tool', 'custom-tool'],
    ['constructor', 'constructor'],
    ['__proto__', '__proto__'],
    ['hasOwnProperty', 'hasOwnProperty'],
  ]) {
    const html = markup({ currentTool: tool })
    assert.ok(html.includes(`>${label}</span>`), `unexpected activity label for ${tool}: ${html}`)
  }
})

test('running card wires the activity once outside expansion and suppresses summary duplication', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  assert.equal(source.split('<TaskCardActivity task={task} />').length - 1, 1)
  assert.doesNotMatch(source, /TaskLiveActivity|🎯 Current Focus/)
  assert.match(source, /task=\{isRunning \? \{ \.\.\.task, toolActivitySummary: undefined \} : task\}/)
  assert.match(source, /<TaskCardActivity task=\{task\} \/>[\s\S]*?\{expanded && \(/)
  assert.match(source, /key=\{t\.id\}/)
})

test('streaming rerenders preserve DOM, geometry and focus; terminal transitions remove activity immediately', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client'; import {flushSync} from 'react-dom';
    import {TaskCardActivity} from './src/features/desktop/orchestrate/task-card-activity';
    import {TaskCardSummary} from './src/features/desktop/orchestrate/task-card-summary';
    let task=${JSON.stringify(task)}, expanded=false;
    const root=createRoot(document.getElementById('root'));
    function render(){flushSync(()=>root.render(<article key={task.id}>
      <TaskCardSummary task={{...task,toolActivitySummary:undefined}}/>
      <TaskCardActivity task={task}/>
      <button id="expand" onClick={()=>{expanded=!expanded;render()}}>Expand</button>
      {expanded && <div id="checklist">Execution checklist</div>}
      <button id="control">Open session</button>
    </article>))}
    window.updateTask=patch=>{task={...task,...patch};render()};render();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  // Generate the layout utilities with the production Tailwind engine, rather than
  // mistaking unstyled DOM bounds for layout evidence. Full app/theme is a parent check.
  const css = (await compile('@theme { --spacing: 0.25rem; } @tailwind utilities;')).build([
    'flex', 'h-9', 'w-full', 'min-w-0', 'items-center', 'gap-2', 'overflow-hidden',
    'px-2', 'block', 'flex-1', 'truncate', 'leading-5', 'w-12', 'shrink-0', 'text-right', 'tabular-nums',
  ])
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    for (const reducedMotion of ['no-preference', 'reduce'] as const) {
      const page = await browser.newPage({ viewport: { width: 480, height: 600 }, reducedMotion })
      await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: `<style>*{box-sizing:border-box}body{margin:0;font:14px system-ui}#root{width:260px}svg{width:16px;height:16px}${css}${readFileSync(new URL('./swarm-section.css', import.meta.url), 'utf8')}</style><div id="root"></div>` }))
      await page.goto('https://activity.test/')
      await page.addScriptTag({ content: bundle.outputFiles[0].text })
      const row = page.getByTestId('task-card-activity')
      await row.waitFor()
      await page.evaluate(() => {
        const w = window as any
        w.activityNode = document.querySelector('[data-testid="task-card-activity"]')
        w.statusNode = w.activityNode.querySelector('[role="status"]')
        w.initialBounds = w.activityNode.getBoundingClientRect().toJSON()
        w.mutations = 0
        w.observer = new MutationObserver(records => { w.mutations += records.length })
        w.observer.observe(w.activityNode, { subtree: true, childList: true, characterData: true, attributes: true })
      })
      // Separate commits and frames reproduce streaming rerenders, not one batched update.
      for (let i = 0; i < 12; i++) {
        await page.evaluate(async i => {
          ;(window as any).updateTask({ liveAssistantText: 'token '.repeat(i + 1), currentFocus: `Focus ${i}`, toolActivitySummary: `read ×${i}` })
          await new Promise(requestAnimationFrame)
        }, i)
      }
      assert.equal(await page.evaluate(() => (window as any).mutations), 0)
      await page.locator('#control').focus()
      for (const currentTool of ['read', 'edit', 'long_tool_'.repeat(80), '', 'search']) {
        await page.evaluate(currentTool => (window as any).updateTask({ currentTool, planProgressPercent: currentTool ? 100 : undefined }), currentTool)
        assert.equal(await row.count(), 1)
        const stable = await page.evaluate(() => {
          const w = window as any, row = document.querySelector('[data-testid="task-card-activity"]')!
          const status = row.querySelector('[role="status"]')!, bounds = row.getBoundingClientRect()
          return row === w.activityNode && status !== w.statusNode && bounds.height === 34
            && bounds.width === w.initialBounds.width && bounds.y === w.initialBounds.y
            && document.activeElement?.id === 'control'
        })
        assert.equal(stable, true)
      }
      // Repeated same-name calls still swap, but token-only updates do not.
      await page.evaluate(() => {
        const w = window as any
        w.updateTask({ currentTool: 'read same.ts', currentToolEventKey: 'call-1', toolCallCount: 1 })
        w.previousEvent = document.querySelector('[role="status"]')
        w.updateTask({ currentToolEventKey: 'call-2', toolCallCount: 2 })
      })
      assert.equal(await page.evaluate(() => document.querySelector('[role="status"]') !== (window as any).previousEvent), true)
      assert.equal(await row.locator('[role="status"]').count(), 1)
      assert.match(await row.innerText(), /call 2/)
      await page.locator('#expand').click()
      assert.equal(await page.locator('#checklist').count(), 1)
      assert.equal(await row.count(), 1)
      assert.equal(await page.getByLabel('Live session activity', { exact: true }).count(), 0)
      assert.equal(await page.evaluate(() => document.querySelector('[data-testid="task-card-activity"]') === (window as any).activityNode), true)
      await page.locator('#expand').click()
      assert.equal(await row.count(), 1)
      for (const status of ['needs_review', 'failed', 'cancelled', 'blocked', 'completed']) {
        await page.evaluate(status => (window as any).updateTask({ status, currentTool: 'read' }), status)
        assert.equal(await row.count(), 0)
        await page.evaluate(async () => { await new Promise(requestAnimationFrame) })
        assert.equal(await row.count(), 0)
        await page.evaluate(() => (window as any).updateTask({ status: 'running' }))
      }
      await page.evaluate(() => (window as any).observer.disconnect())
      await page.close()
    }
  } finally { await browser.close() }
})
