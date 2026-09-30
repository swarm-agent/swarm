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

test('archive and ask are real outlined buttons, preserve callbacks and precede checkbox wiring', () => {
  const calls: string[] = []
  const tree = TaskCardActionButtons({ onArchiveTask: () => calls.push('archive'), onAskOrchestrator: () => calls.push('ask') })
  const controls = buttons(tree)
  assert.deepEqual(controls.map(button => button.props.children), ['Archive task', 'Ask orchestrator'])
  let stopped = 0
  for (const button of controls) {
    assert.equal(button.props.type, 'button')
    assert.match(button.props.className, /border/)
    button.props.onClick({ stopPropagation: () => stopped++ })
  }
  assert.deepEqual(calls, ['archive', 'ask'])
  assert.equal(stopped, 2)
  assert.equal(buttons(TaskCardActionButtons({})).length, 0)
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const actionRow = source.slice(source.indexOf('        actions={'), source.indexOf('        extraBadges={'))
  assert.match(actionRow, /flex items-center gap-1\.5/)
  assert.ok(actionRow.indexOf('<TaskCardActionButtons') < actionRow.indexOf('type="checkbox"'))
  assert.doesNotMatch(source, /self-end[^\n]*Archive task/)
})
