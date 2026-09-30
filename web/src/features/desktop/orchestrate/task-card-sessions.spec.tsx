// Purpose: collapsed task cards expose every current AI session, count only running
// executions, and keep archive/ask controls inline before selection. Regression:
// planned jobs, duplicate coordinator entries and prior generations inflate counts
// or route both controls to one session. Authority: taskWithCurrentSessions,
// aggregateTaskLiveState, TaskCardSummary and MinimalTaskCard's action wiring.
// Pure selector + server-rendered component/event props are the narrowest layer;
// no browser, transcripts, providers or worker dispatch are required.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync } from 'node:fs'
import { taskWithCurrentSessions, taskCardSessions } from './task-card-sessions'
import { aggregateTaskLiveState } from './orchestrate-task-helpers'
import { TaskCardSummary } from './task-card-summary'
import { TaskCardActionButtons } from './task-card-action-buttons'
import type { RunningTask } from './orchestrate-types'
import type { DesktopV3CacheState } from '../state/desktop-v3-cache-types'

const task = { id: 'task', title: 'Task', agentType: 'coder', status: 'running', sessionId: 'parent' } as RunningTask
const data = (status: string) => ({ view: { current_run_state: { status, run_id: 'run' } } })
function state() {
  return {
    sessionsById: Object.fromEntries(['one', 'two', 'old'].map(id => [id, { kind: 'full', session: {
      id, title: id, metadata: { parent_session_id: 'parent', lineage_kind: 'delegated_subagent',
        requested_subagent: 'coder', parent_run_id: id === 'old' ? 'previous' : 'current' },
    } }])),
    sessionViewsById: { parent: { current_run_state: { run_id: 'current' } } },
  } as unknown as DesktopV3CacheState
}
function buttons(node: React.ReactNode): React.ReactElement<any>[] {
  if (!React.isValidElement(node)) return []
  const element = node as React.ReactElement<any>
  return [...(element.type === 'button' ? [element] : []), ...React.Children.toArray(element.props.children).flatMap(buttons)]
}

test('two delegated children count separately and open exact targets without card clicks', () => {
  const current = taskWithCurrentSessions(task, state())
  assert.deepEqual(current.sessionIds, ['one', 'parent', 'two'])
  const live = aggregateTaskLiveState(current, { parent: data('running'), one: data('running'), two: data('running') })
  const opened: string[] = []
  const tree = TaskCardSummary({ task: live, onOpenSession: id => opened.push(id) })
  assert.match(renderToStaticMarkup(tree), /2 AIs working/)
  const controls = buttons(tree).filter(button => button.props['data-session-id'])
  assert.deepEqual(controls.map(button => button.props['data-session-id']), ['one', 'two'])
  let stopped = 0
  for (const button of controls) button.props.onClick({ stopPropagation: () => stopped++ })
  assert.deepEqual(opened, ['one', 'two'])
  assert.equal(stopped, 2)
})

test('queued, completed, unknown and duplicate sessions never inflate working count', () => {
  const live = aggregateTaskLiveState(taskWithCurrentSessions({ ...task, sessionIds: ['one', 'one', 'two', 'old'] }, state()),
    { parent: data('running'), one: data('pending_executor'), two: data('completed') })
  const sessions = taskCardSessions(live)
  assert.deepEqual(sessions.map(item => item.status), ['queued', 'completed'])
  assert.match(renderToStaticMarkup(<TaskCardSummary task={live} />), /0 AIs working/)
  const unknown = { ...live, sessionSummary: { ...live.sessionSummary!, sessionStates: [{ sessionId: 'one', status: 'running' as const, hydrated: false }] } }
  assert.match(renderToStaticMarkup(<TaskCardSummary task={unknown} />), /session state unavailable/)
  assert.equal(taskCardSessions(unknown)[0].status, 'unknown')
})

test('program retry supersedes historical/duplicate entries and updates count', () => {
  const program = { ...task, sessionIds: ['old', 'one', 'two'], taskProgramStatus: {
    jobs: [{ job_id: 'job', state: 'running', child_session_id: 'old', current_session_id: 'one',
      generation_history: [{ session_id: 'old' }] }, { job_id: 'job-two', state: 'queued', child_session_id: 'two' }],
  } } as RunningTask
  const current = taskWithCurrentSessions(program, state())
  assert.ok(!current.sessionIds?.includes('old'))
  const live = aggregateTaskLiveState(current, { one: data('running'), two: data('pending_executor') })
  assert.match(renderToStaticMarkup(<TaskCardSummary task={live} />), /1 AI working/)
  const done = aggregateTaskLiveState(current, { one: data('completed'), two: data('pending_executor') })
  assert.match(renderToStaticMarkup(<TaskCardSummary task={done} />), /0 AIs working/)
  const single = aggregateTaskLiveState(task, { parent: data('running') })
  assert.deepEqual(taskCardSessions(single).map(item => item.sessionId), ['parent'])
  assert.match(renderToStaticMarkup(<TaskCardSummary task={single} />), /1 AI working/)
})

// Purpose: TaskCardActionButtons must render only recognizable icons, retaining exact
// accessible names/tooltips, keyboard focus and usable targets. Server markup and
// element props are the narrowest layer proving this without a browser/layout claim.
test('archive and ask render icon-only outlined buttons with accessible names', () => {
  const tree = TaskCardActionButtons({ onArchiveTask: () => {}, onAskOrchestrator: () => {} })
  const controls = buttons(tree)
  assert.deepEqual(controls.map(button => button.props['aria-label']), ['Archive task', 'Ask orchestrator'])
  for (const [index, button] of controls.entries()) {
    assert.equal(button.props.type, 'button')
    assert.equal(button.props.title, button.props['aria-label'])
    assert.match(button.props.className, /border/)
    assert.match(button.props.className, /\bh-8 w-8\b/)
    assert.match(button.props.className, /focus-visible:outline-2/)
    assert.match(button.props.className, /focus-visible:outline-sky-400/)
    const markup = renderToStaticMarkup(button)
    assert.match(markup, index === 0 ? /lucide-archive/ : /lucide-message-circle/)
    assert.match(markup, /<svg[^>]*aria-hidden="true"/)
    assert.equal(markup.replace(/<[^>]*>/g, ''), '', 'no visible text inside the button')
  }
})

// Purpose: optional callbacks control availability, and each action stops bubbling
// before invoking only its own callback. TaskCardActionButtons owns this boundary;
// direct event props prove it for both/single/missing callbacks without live jobs.
test('archive and ask preserve optional callbacks and stop click propagation first', () => {
  for (const available of [['archive', 'ask'], ['archive'], ['ask'], []]) {
    const calls: string[] = []
    let propagationStopped = false
    const callback = (action: string) => () => {
      assert.equal(propagationStopped, true, 'stop before invoking the action')
      calls.push(action)
    }
    const tree = TaskCardActionButtons({
      onArchiveTask: available.includes('archive') ? callback('archive') : undefined,
      onAskOrchestrator: available.includes('ask') ? callback('ask') : undefined,
    })
    const controls = buttons(tree)
    assert.deepEqual(controls.map(button => button.props['aria-label']),
      available.map(action => action === 'archive' ? 'Archive task' : 'Ask orchestrator'))
    let stopped = 0
    for (const button of controls) {
      propagationStopped = false
      button.props.onClick({ stopPropagation: () => { propagationStopped = true; stopped++ } })
      assert.equal(propagationStopped, true, 'card selection/expansion must not receive the click')
    }
    assert.deepEqual(calls, available)
    assert.equal(stopped, available.length)
    if (available.length === 0) assert.equal(renderToStaticMarkup(tree), '')
  }
})

// Purpose: MinimalTaskCard retains the existing inline action slot immediately before
// the separate checkbox label. This wiring check supplements the rendered/handler
// contracts above; it does not claim browser-level selection or layout verification.
test('task action icons remain inline immediately before checkbox wiring', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const actionRow = source.slice(source.indexOf('        actions={'), source.indexOf('        extraBadges={'))
  assert.match(actionRow, /flex items-center gap-1\.5/)
  assert.match(actionRow, /<TaskCardActionButtons onArchiveTask=\{onArchiveTask\} onAskOrchestrator=\{onAskOrchestrator\} \/>\s*\{onToggleMarked && \(/)
  assert.ok(actionRow.indexOf('<TaskCardActionButtons') < actionRow.indexOf('type="checkbox"'))
  assert.doesNotMatch(source, /self-end[^\n]*Archive task/)
})
