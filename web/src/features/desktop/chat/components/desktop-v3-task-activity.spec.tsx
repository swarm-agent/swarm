import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { DesktopV3TaskActivity } from './desktop-v3-task-activity'
import { buildDesktopV3ConversationRenderItems } from './desktop-v3-existing-conversation-pane'
import { buildDesktopV3RunStatusModel } from './desktop-v3-run-status'
import { createEmptyDesktopV3CacheState, applyCacheEvent } from '../../state/desktop-v3-cache-reducer'
import { selectRenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import { isTaskOutcomeMessage, selectTaskActivities, taskOutcomeActivity } from '../../state/desktop-v3-task-updates'
import type { MessageSnapshot, V3SessionEvent, V3SessionRunIntent } from '../../state/desktop-v3-cache-types'

const update = { session_id: 'child', parent_session_id: 'parent', event_id: 'report-1', event_seq: 2, kind: 'progress', summary: 'Finished the parser. <script>unsafe()</script>' }
const receipt: V3SessionEvent = { id: 'receipt', session_id: 'parent', seq: 8, event_type: 'session.task.delivered', payload: { RunID: 'goal', Updates: [update] }, ts_unix_ms: 1 }
const outcome: MessageSnapshot = { id: 'outcome', session_id: 'parent', global_seq: 7, role: 'system', created_at: 1, metadata: { task_wait_owner_run_id: 'goal' }, content: 'Delegated project task outcomes:\n' + JSON.stringify({ project_id: 'private-project', guidance: 'internal guidance', tasks: [{ task_id: 'opaque-task', title: 'Parser', status: 'needs_review', summary: 'Ready to inspect' }] }) }

// Purpose: canonical reducer/selector plus actual transcript builder must display durable
// task reports once, not raw internal messages. This layer exercises replay/hydration and
// the render boundary without inventing a second UI cache or requiring a live provider.
test('task event replay produces one readable activity and no outcome bubble', () => {
  const state = createEmptyDesktopV3CacheState()
  state.messagesBySession.parent = { items: [outcome], loaded: true, hasMore: false } as typeof state.messagesBySession[string]
  const before = selectRenderedSessionMessages(state, 'parent')
  for (let i = 0; i < 2; i++) applyCacheEvent(state, { source: 'realtime', sessionId: 'parent', eventType: receipt.event_type, payload: receipt.payload as never, sessionEvent: receipt })
  const selected = selectRenderedSessionMessages(state, 'parent')
  assert.equal(selected.taskActivities?.length, 1)
  assert.notEqual(selected.taskActivities, before.taskActivities, 'event-only delivery must invalidate the chat selection')
  assert.equal(selectRenderedSessionMessages(state, 'parent').taskActivities, selected.taskActivities, 'unchanged source retains selector identity')
  const items = buildDesktopV3ConversationRenderItems(selected)
  assert.equal(items.filter(item => item.type === 'message').length, 0)
  assert.equal(items.filter(item => item.type === 'task-activity').length, 2)
  assert.equal(state.messagesBySession.parent.items[0].content, outcome.content, 'presentation must not rewrite provider context')
  assert.deepEqual(selectTaskActivities([receipt, receipt], 'parent'), selected.taskActivities)
  const markup = items.map(item => item.type === 'task-activity' ? renderToStaticMarkup(<DesktopV3TaskActivity activity={item.activity} />) : '').join('')
  assert.match(markup, /Task progress/)
  assert.match(markup, /Received by the Orchestrator; not scope approval/)
  assert.match(markup, /Parser · Ready for review/)
  assert.doesNotMatch(markup, /private-project|opaque-task|Delegated project|internal guidance|<script>/)
  assert.match(markup, /&lt;script&gt;/)
})

// Purpose: malformed envelopes and cross-session receipts cannot leak raw internals or
// masquerade as delivered task data; task text stays plain escaped text at the component.
test('task activity fails closed on malformed or foreign data without hiding user prose', () => {
  for (const content of ['not json', 'Delegated project task outcomes:\n{bad', 'x'.repeat(100_001)]) {
    const activity = taskOutcomeActivity({ ...outcome, content })
    assert.equal(activity.rows.length, 0)
    assert.doesNotMatch(renderToStaticMarkup(<DesktopV3TaskActivity activity={activity} />), /not json|\{bad/)
  }
  assert.equal(isTaskOutcomeMessage({ ...outcome, role: 'user' }), false)
  assert.equal(isTaskOutcomeMessage({ ...outcome, metadata: {} }), false)
  assert.deepEqual(selectTaskActivities([receipt], 'foreign'), [])
  assert.deepEqual(selectTaskActivities([{ ...receipt, payload: { Updates: [{ ...update, parent_session_id: 'foreign' }] } }], 'parent'), [])
  assert.deepEqual(selectTaskActivities([{ ...receipt, payload: { Updates: [{ ...update, kind: 'arbitrary' }] } }], 'parent'), [])
  const reported = { ...receipt, session_id: 'child', event_type: 'session.task.reported', payload: update }
  assert.match(selectTaskActivities([reported], 'child')[0].detail, /delivery is not yet confirmed/)
  assert.deepEqual(selectTaskActivities([reported], 'parent'), [])
})

// Purpose: durable run state, not a task message or a stale live overlay, determines
// waiting/resuming/resumed labels. Terminal states must never claim a fresh resume.
test('task continuation labels distinguish queued, running, waiting and stopped', () => {
  const run: V3SessionRunIntent = { session_id: 'parent', run_id: 'resume', task_wait_owner_run_id: 'goal', status: 'pending_executor', created_at: 10, updated_at: 10, event_seq: 9 }
  assert.equal(buildDesktopV3RunStatusModel({ currentRunIntent: run })?.label, 'Resuming after tasks')
  assert.equal(buildDesktopV3RunStatusModel({ currentRunIntent: { ...run, status: 'running' } })?.label, 'Resumed after tasks')
  assert.equal(buildDesktopV3RunStatusModel({ latestRunIntent: { ...run, status: 'cancelled' } })?.label, 'Stopped')
  assert.equal(buildDesktopV3RunStatusModel({ currentRunIntent: { ...run, status: 'waiting_tasks' } })?.active, false)
  assert.equal(buildDesktopV3RunStatusModel({ currentRunIntent: { ...run, task_wait_owner_run_id: undefined } })?.label, 'Running')
})
