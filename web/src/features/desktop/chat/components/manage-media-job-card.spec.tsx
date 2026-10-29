// Purpose: the chat's media card shows a media job's state and, once ready,
// its files through the same MediaTaskCard the Orchestrate studio uses.
// Requirement: before the task is read it says Generating with no file; a
// ready task shows Ready, its title and its file; a task that ended without a
// file says Failed with the reason. Threat: a card that looks finished while
// the job runs, or a failed job without its reason.
// Boundary: ManageMediaJobCardView over a RunningTask; static rendering is the
// narrowest layer that proves the markup without mounting the project runtime.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import type { RunningTask } from '../../orchestrate/orchestrate-types'
import { ManageMediaJobCardView } from './manage-media-job-card'

function task(partial: Partial<RunningTask>): RunningTask {
  return { id: 'task_m1', title: 'A bee', description: 'A bee', agentType: 'image', status: 'running', deliverables: [], ...partial } as unknown as RunningTask
}

test('the media card follows its job', () => {
  const waiting = renderToStaticMarkup(<ManageMediaJobCardView task={null} />)
  assert.match(waiting, /data-media-job-state="generating"/)
  assert.match(waiting, />Generating</)
  assert.doesNotMatch(waiting, /media-task-card/)

  const ready = renderToStaticMarkup(<ManageMediaJobCardView task={task({
    status: 'needs_review',
    deliverables: [{ id: 'd1', title: 'bee.png', type: 'image', status: 'ready', mediaUrl: '/v3/projects/p/tasks/task_m1/deliverables/d1?field=media', createdAt: 1, author: 'image' }],
  })} />)
  assert.match(ready, /data-media-job-state="ready"/)
  assert.match(ready, /data-task-id="task_m1"/)
  assert.match(ready, />Ready</)
  assert.match(ready, /data-testid="media-task-card"/)

  const failed = renderToStaticMarkup(<ManageMediaJobCardView task={task({ status: 'failed', lastError: 'quota exceeded' } as Partial<RunningTask>)} />)
  assert.match(failed, /data-media-job-state="failed"/)
  assert.match(failed, />Failed</)
  assert.match(failed, /quota exceeded/)
})
