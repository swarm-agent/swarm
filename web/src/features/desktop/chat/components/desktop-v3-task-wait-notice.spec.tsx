// Purpose: the person sees a task wait start and end as a compact status line in
// the chat. Requirement: a system message with metadata source task_wait_notice
// (written by the daemon when a run pends on wait_tasks and when it ends) is
// recognised, renders as one plain status line with its text and the task
// titles as its tooltip, and other system messages (the task outcome card) are
// not mistaken for it. Threat: the wait line falling through to a raw system
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

test('task wait lines render as compact status lines', () => {
  const start = message({
    id: 'run-1-wait-notice',
    content: 'This conversation is now waiting on 2 task(s) on forge: Fix login, Add\n<b>tests</b>. It wakes when they settle; a new message ends the wait.',
    metadata: { source: 'task_wait_notice', task_wait_notice: 'waiting', task_count: 2, machine: 'forge', task_titles: ['Fix login', 'Add <b>tests</b>'] },
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
  assert.match(html, /waiting on 2 task\(s\) on forge: Fix login, Add &lt;b&gt;tests&lt;\/b&gt;\./)
  assert.doesNotMatch(html, /<b>/)
  assert.match(html, /title="Fix login\nAdd &lt;b&gt;tests&lt;\/b&gt;"/)

  const end = message({ id: 'run-1-wait-ended', content: 'Wait ended: tasks settled.', metadata: { source: 'task_wait_notice', task_wait_notice: 'ended', ended_by: 'tasks' } })
  const ended = taskWaitNotice(end)
  assert.equal(ended.ended, true)
  assert.match(renderToStaticMarkup(<DesktopV3TaskWaitNotice notice={ended} />), /data-ended="true".*Wait ended: tasks settled\./s)

  const outcome = message({ content: 'Delegated project task outcomes:\n{}', metadata: { task_wait_owner_run_id: 'run-1' } })
  assert.equal(isTaskWaitNoticeMessage(outcome), false)
  assert.equal(isTaskWaitNoticeMessage(message({ role: 'user', content: 'hi', metadata: { source: 'task_wait_notice' } })), false)
})
