import assert from 'node:assert/strict'
import { test } from 'node:test'
import { confirmMediaBatch } from './media-admission'
import type { RunningTask } from './orchestrate-types'

// Purpose: handleApproveTask's pre-dispatch gate must confirm the complete batch
// once at 25+, cancel without dispatch, and leave non-media approvals unchanged.
// The pure interaction gate is the narrowest layer for this decision; backend
// HTTP tests independently prove no generation before persisted task approval.
test('media confirmation covers thresholds, full slots, cancel and non-media', () => {
  for (const agentType of ['image', 'video', 'sound', 'audio', 'coder', 'finder']) {
    for (const count of [1, 24, 25, 26]) {
      for (const accepted of [false, true]) {
        let prompts = 0
        let dispatches = 0
        const task = { agentType, variantCount: count } as RunningTask
        if (confirmMediaBatch(task, message => {
          prompts++
          assert.ok(message.includes(`all ${count} media iterations`))
          return accepted
        })) dispatches++
        const needsConfirmation = ['image', 'video', 'sound', 'audio'].includes(agentType) && count >= 25
        assert.equal(prompts, needsConfirmation ? 1 : 0)
        assert.equal(dispatches, needsConfirmation && !accepted ? 0 : 1)
      }
    }
  }
  const fullBatch = { agentType: 'image', variantCount: 1, deliverables: Array(25).fill({}) } as RunningTask
  assert.equal(confirmMediaBatch(fullBatch, message => { assert.ok(message.includes('all 25')); return false }), false)
})
