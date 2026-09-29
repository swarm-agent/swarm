// Purpose: A task card must show bounded, accessible live work without opening chat.
// Threat: a full-width transcript, no pre-token/tool-only indication, or a terminal
// animation misrepresents the current run. Boundary: TaskLiveActivity component;
// server-rendered markup is the narrowest assertion for its presentation contract.
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskLiveActivity } from './task-live-activity'
import type { RunningTask } from './orchestrate-types'

const task = (status: RunningTask['status'], extra: Partial<RunningTask> = {}) =>
  ({ id: 'task', title: 'Task', status, ...extra }) as RunningTask

test('compact task activity starts before tokens, shows tool-only work and bounded text, and disappears on review', () => {
  const starting = renderToStaticMarkup(<TaskLiveActivity task={task('running')} />)
  assert.match(starting, /role="status"/)
  assert.match(starting, /Starting session/)
  assert.match(starting, /max-w-full min-w-0/)
  assert.doesNotMatch(starting, /overflow-y-auto|class="w-full/)

  const tool = renderToStaticMarkup(<TaskLiveActivity task={task('running', { toolActivitySummary: 'Reading files' })} />)
  assert.match(tool, /Reading files/)
  const text = renderToStaticMarkup(<TaskLiveActivity task={task('in_progress', { liveAssistantText: 'Writing a focused test' })} />)
  assert.match(text, /Writing a focused test/)
  const longText = renderToStaticMarkup(<TaskLiveActivity task={task('running', { liveAssistantText: 'x'.repeat(500) })} />)
  assert.doesNotMatch(longText, /x{161}/)
  assert.equal(renderToStaticMarkup(<TaskLiveActivity task={task('needs_review', { liveAssistantText: 'Old text' })} />), '')
  assert.equal(renderToStaticMarkup(<TaskLiveActivity task={task('failed', { toolActivitySummary: 'Old tool' })} />), '')
})
