// Purpose: the person sees a task watch start and end as a compact status line
// in the chat. Requirement: a system message with metadata source
// task_wait_notice (written by the daemon when wait_tasks starts a watch and
// when the watched tasks settle) is
// recognised, renders as one plain status line with its text and the task
// titles as its tooltip, and other system messages (the task outcome card) are
// not mistaken for it. Threat: the watch line falling through to a raw system
// bubble, or untrusted linked-machine titles adding markup or lines.
// Boundary: isTaskWaitNoticeMessage/taskWaitNotice and the real
// DesktopV3TaskWaitNotice component; static rendering is the narrowest layer
// that proves the markup without mounting the conversation runtime.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { isTaskOutcomeMessage, isTaskWaitNoticeMessage, taskWaitNotice } from '../../state/desktop-v3-task-updates'
import type { MessageSnapshot } from '../../state/desktop-v3-cache-types'
import { DesktopV3TaskWaitNotice } from './desktop-v3-task-activity'

function message(partial: Partial<MessageSnapshot>): MessageSnapshot {
  return { id: 'm', session_id: 's', global_seq: 7, role: 'system', content: '', created_at: 1, ...partial } as MessageSnapshot
}

test('task watch lines render as compact status lines', () => {
  const start = message({
    id: 'watch-1-watch-notice',
    content: 'Watching 2 tasks on forge: Fix login, Add\n<b>tests</b>.',
    metadata: { source: 'task_wait_notice', task_wait_notice: 'watching', task_watch_id: 'watch-1', task_count: 2, machine: 'forge', task_titles: ['Fix login', 'Add <b>tests</b>'] },
  })
  assert.equal(isTaskWaitNoticeMessage(start), true)
  assert.equal(isTaskOutcomeMessage(start), false)
  const notice = taskWaitNotice(start)
  assert.equal(notice.ended, false)
  assert.equal(notice.timelineSeq, 7)
  assert.doesNotMatch(notice.text, /\n/)
  const html = renderToStaticMarkup(<DesktopV3TaskWaitNotice notice={notice} />)
  assert.match(html, /data-testid="desktop-task-wait-notice"/)
  assert.match(html, /role="status"/)
  assert.match(html, /Watching 2 tasks on forge: Fix login, Add &lt;b&gt;tests&lt;\/b&gt;\./)
  assert.match(html, /👁/)
  assert.doesNotMatch(html, /<b>/)
  assert.match(html, /title="Fix login\nAdd &lt;b&gt;tests&lt;\/b&gt;"/)

  const end = message({ id: 'watch-1-watch-ended', content: 'Tasks settled: Fix login, Add tests.', metadata: { source: 'task_wait_notice', task_wait_notice: 'ended', ended_by: 'tasks', task_watch_id: 'watch-1' } })
  const ended = taskWaitNotice(end)
  assert.equal(ended.ended, true)
  assert.match(renderToStaticMarkup(<DesktopV3TaskWaitNotice notice={ended} />), /data-ended="true".*Tasks settled: Fix login, Add tests\./s)

  const outcome = message({ content: 'Delegated project task outcomes:\n{}', metadata: { task_wait_owner_run_id: 'run-1' } })
  assert.equal(isTaskWaitNoticeMessage(outcome), false)
  assert.equal(isTaskWaitNoticeMessage(message({ role: 'user', content: 'hi', metadata: { source: 'task_wait_notice' } })), false)
})

// Purpose: the conversation shows what it watches beside Idle/Working, never
// as pending. Requirement: DesktopV3TaskWatchBadge counts every watched task
// across watches, names linked machines, renders titles only as plain text in
// its tooltip, and renders nothing with no watches; buildDesktopV3RunStatusModel
// never reports a waiting state for a retired waiting_tasks run. Threat: the
// pending badge coming back, or untrusted titles adding markup. Boundary: the
// real components rendered statically, the narrowest layer for the markup.
test('task watches show as a watching badge and never as waiting', async () => {
  const { DesktopV3TaskWatchBadge, buildDesktopV3RunStatusModel } = await import('./desktop-v3-run-status')
  assert.equal(renderToStaticMarkup(<DesktopV3TaskWatchBadge watches={[]} />), '')
  const html = renderToStaticMarkup(<DesktopV3TaskWatchBadge watches={[
    { task_ids: ['t1', 't2'], titles: ['Fix login', '<b>x</b>'], machine: 'forge' },
    { task_ids: ['t3'], titles: ['Docs'] },
  ]} />)
  assert.match(html, /data-testid="desktop-v3-task-watch-badge"/)
  assert.match(html, /👁/)
  assert.match(html, /Watching · 3 tasks on forge/)
  assert.doesNotMatch(html, /<b>/)
  const model = buildDesktopV3RunStatusModel({ currentRunIntent: { session_id: 's', run_id: 'r', status: 'waiting_tasks', created_at: 1, updated_at: 1, event_seq: 1 } as never })
  assert.notEqual(model?.kind, 'waiting')
})
