// Purpose: TaskCardSummary keeps deployed identity, source lineage and truthful running
// counts discoverable without collapsed handoff/session stacks. Threat: worktree aliases
// replace agent identity, proposals claim deployed defaults, or unavailable evidence is
// reported as zero/success. SSR is the narrowest presentation boundary; the browser
// fixture below checks actual narrow geometry, tooltip labels and quiet event updates.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync } from 'node:fs'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { TaskCardSummary, taskCardFacts } from './task-card-summary'
import type { RunningTask } from './orchestrate-types'
import { reduceDesktopProjectsState } from '../state/desktop-projects-state'

const task: RunningTask = {
  id: 'compact', title: 'Compact task', agentType: 'coder', status: 'needs_review',
  workspaceTarget: '/source/repository', workspacePath: '/isolated/task-copy',
  sourceWorkspacePath: '/source/repository', worktreeName: 'task-copy', worktreeBranch: 'agent/task-copy',
  activeAgent: '@Coder', activeModel: 'deployed-model', activeProvider: 'deployed-provider', model: 'proposed-default',
  elapsed: '1m', subtasks: [], gitStatus: 'diverged', unintegratedCommits: 1,
  handoffSummary: 'Implemented keyboard navigation. Tests not run; parent validation required.',
  sessionSummary: { totalSessions: 3, runningSessions: 99, reviewSessions: 0, failedSessions: 0, completedSessions: 0,
    sessionStates: [{ sessionId: 'one', status: 'running', hydrated: true },
      { sessionId: 'one', status: 'running', hydrated: true },
      { sessionId: 'two', status: 'running', hydrated: false }, { sessionId: 'three', status: 'queued' }] },
}
const markup = (patch: Partial<RunningTask> = {}, expanded = false) => renderToStaticMarkup(<TaskCardSummary task={{ ...task, ...patch }} expanded={expanded} />)
const text = (html: string) => html.replace(/<[^>]*>/g, '')

test('collapsed metadata shows each source identity once and leaves evidence behind expansion', () => {
  const html = markup(), visible = text(html)
  assert.match(visible, /Coderdeployed-provider \/ deployed-modelrepositoryagent\/task-copy/)
  assert.equal((visible.match(/agent\/task-copy/g) || []).length, 1)
  assert.doesNotMatch(visible, /proposed-default|task-copytask-copy|Implemented|Tests not run|Review the|Validation still/)
  assert.doesNotMatch(html, /data-session-id|Ready for review/)
  assert.match(visible, /1 AI working · \?/)
  assert.match(visible, /1 unintegrated commit(?!s)/)
  assert.match(html, /title="\/source\/repository"/)
  assert.match(html, /title="Worktree: agent\/task-copy"/)
  const expanded = markup({}, true)
  assert.match(expanded, /data-session-id="one"/)
  assert.match(expanded, /Implemented keyboard navigation/)
  assert.match(expanded, /Tests not run; parent validation required/)
  assert.match(expanded, /Validation still needs to be run/)
})

test('unknown, zero, pending and stale facts never imply deployment or verification', () => {
  assert.match(text(markup({ sessionSummary: undefined })), /AIs working: unknown/)
  assert.match(text(markup({ sessionSummary: { ...task.sessionSummary!, totalSessions: 0, sessionStates: [] } })), /0 AIs working/)
  const pending = text(markup({ status: 'pending_approval' }))
  assert.match(pending, /Proposed coder/)
  assert.match(pending, /0 AIs working/)
  assert.doesNotMatch(pending, /deployed-model|deployed-provider|proposed-default|unintegrated/)
  assert.match(text(markup({ sourceWorkspacePath: undefined })), /Workspace unavailable/)
  assert.doesNotMatch(text(markup({ sourceWorkspacePath: undefined })), /isolated/)
  assert.equal(taskCardFacts({ ...task, unintegratedCommits: 2 }).git, '2 unintegrated commits')
  assert.equal(taskCardFacts({ ...task, gitStatus: 'stale' }).git, 'Git: last known state')
  assert.equal(taskCardFacts({ ...task, gitStatus: 'unknown' }).git, 'Git: not inspected')
})

// Purpose: production metadata CSS must bound long labels at narrow card widths,
// keep state/count legible and disable collapsed motion regardless of OS settings.
// A hermetic browser component is the narrowest layout boundary; this is not a
// provider-backed realtime or full Orchestrate integration test.
test('narrow long metadata stays bounded and live event updates remain static', { timeout: 30000 }, async () => {
  const fixture = { ...task, status: 'running', title: 'Long task '.repeat(40), activeModel: 'long-model-'.repeat(40),
    worktreeBranch: 'agent/long-branch-'.repeat(40), currentTool: 'read first.ts', currentToolEventKey: 'one' }
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client'; import {flushSync} from 'react-dom';
    import {TaskCardSummary} from './src/features/desktop/orchestrate/task-card-summary';
    import {TaskCardActivity} from './src/features/desktop/orchestrate/task-card-activity';
    let task=${JSON.stringify(fixture)};
    const root=createRoot(document.getElementById('root'));
    function render(){flushSync(()=>root.render(<article className="swarm-task-card" data-expanded="false">
      <TaskCardSummary task={task}/><TaskCardActivity task={task}/><button id="action">Details</button>
    </article>))}
    window.update=patch=>{task={...task,...patch};render()};render();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ reducedMotion: 'no-preference' })
    const css = readFileSync(new URL('./swarm-section.css', import.meta.url), 'utf8')
    await page.route('**/*', route => route.fulfill({ contentType: 'text/html', body: `<style>
      *{box-sizing:border-box}#root{width:240px}.swarm-task-header-row{display:flex}.min-w-0{min-width:0}.flex-1{flex:1}.shrink-0{flex-shrink:0}h3{margin:0}svg{width:16px}${css}
      </style><div id="root" class="swarm-section"></div>` }))
    await page.goto('https://compact.test/')
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    assert.match(await page.getByTestId('task-card-summary').innerText(), /1 AI working/)
    assert.match(await page.getByTestId('task-card-summary').innerText(), /running/)
    assert.equal(await page.locator('.swarm-task-card').evaluate(node => node.scrollWidth <= node.clientWidth), true)
    assert.equal(await page.locator('.swarm-task-meta span').nth(1).getAttribute('title'), `${task.activeProvider} / ${fixture.activeModel}`)
    await page.locator('#action').focus()
    await page.evaluate(() => { (window as any).eventNode = document.querySelector('.swarm-task-focus-event') })
    await page.evaluate(() => (window as any).update({ currentTool: 'read second.ts', currentToolEventKey: 'two', toolCallCount: 2 }))
    assert.match(await page.getByTestId('task-card-activity').innerText(), /second.ts/)
    assert.equal(await page.evaluate(() => document.querySelector('.swarm-task-focus-event') === (window as any).eventNode), true)
    assert.equal(await page.locator('.swarm-task-focus-event').evaluate(node => getComputedStyle(node).animationName), 'none')
    assert.equal(await page.evaluate(() => document.activeElement?.id), 'action')
    assert.equal(await page.locator('.swarm-task-card').evaluate(node => node.scrollWidth <= node.clientWidth), true)
  } finally { await browser.close() }
})

// Purpose: existing canonical project refresh must retain loaded metadata while
// marking invalidation/error stale. A reducer test is the narrowest authority for
// refresh churn; no component-local cache should conceal freshness or new attempts.
test('background hydration retains card facts without clearing stale/error authority', () => {
  let state = reduceDesktopProjectsState({}, { type: 'projects.beginLoad', projectId: 'project', requestId: 'initial' })
  state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'project', requestId: 'initial', generation: 0, tasks: [task], media: [] })
  state = reduceDesktopProjectsState(state, { type: 'projects.invalidate', projectId: 'project' })
  const previous = state.project.tasks[0]
  state = reduceDesktopProjectsState(state, { type: 'projects.beginLoad', projectId: 'project', requestId: 'refresh' })
  assert.equal(state.project.tasks[0], previous)
  assert.equal(state.project.stale, true)
  assert.match(text(renderToStaticMarkup(<TaskCardSummary task={state.project.tasks[0]} />)), /1 unintegrated commit/)
  state = reduceDesktopProjectsState(state, { type: 'projects.loadError', projectId: 'project', requestId: 'refresh', generation: state.project.generation, error: 'Refresh unavailable' })
  assert.equal(state.project.tasks[0], previous)
  assert.equal(state.project.stale, true)
  assert.equal(state.project.error, 'Refresh unavailable')
})
