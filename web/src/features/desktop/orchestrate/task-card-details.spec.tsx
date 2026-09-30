// Purpose: outputs require an actual ready target; pending contracts never become
// validation or delivered evidence. Authority: taskOutputTarget, TaskCardOutputs,
// TaskExpectedOutputs. Pure target admission and server markup are the narrowest
// layer proving the negative states, independent of browser geometry or providers.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { taskOutputTarget, TaskCardOutputs, TaskExpectedOutputs } from './task-card-details'
import type { MediaDeliverable, RunningTask } from './orchestrate-types'

test('planned, generating, rejected and targetless items are not delivered evidence', () => {
  for (const status of ['pending', 'generating', 'rejected', 'ready', 'accepted'] as const) {
    const item: MediaDeliverable = { id: 'output', title: 'Code PR & Verified Tests', type: 'code', status, createdAt: 0, author: 'coder' }
    assert.equal(taskOutputTarget(item), undefined)
    const task = { deliverables: [item] } as RunningTask
    assert.doesNotMatch(renderToStaticMarkup(<TaskCardOutputs task={task} />), /Code PR|<a|<button|<img/)
    assert.match(renderToStaticMarkup(<TaskExpectedOutputs task={task} />), /Expected outputs/)
    assert.doesNotMatch(renderToStaticMarkup(<TaskExpectedOutputs task={task} />), /Tests passed|Validation verified/)
  }
})

test('ready code and report targets stay links, not media thumbnails or invented URLs', () => {
  for (const type of ['code', 'pr', 'report'] as const) {
    const item: MediaDeliverable = { id: 'output', title: 'Actual result', type, status: 'ready', mediaUrl: '/result/exact', createdAt: 0, author: 'coder' }
    assert.equal(taskOutputTarget(item), '/result/exact')
    const html = renderToStaticMarkup(<TaskCardOutputs task={{ deliverables: [item] } as RunningTask} />)
    assert.match(html, /href="\/result\/exact"/)
    assert.doesNotMatch(html, /<img|<video|<button/)
    for (const mediaUrl of ['javascript:alert(1)', '//untrusted.test/result', 'file:///private/result', '']) {
      assert.equal(taskOutputTarget({ ...item, mediaUrl, previewUrl: '/not-a-code-target' }), undefined)
    }
  }
})
