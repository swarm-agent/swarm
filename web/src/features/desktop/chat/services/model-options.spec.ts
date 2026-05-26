import assert from 'node:assert/strict'
import test from 'node:test'

import { displayModelName } from './model-options'

test('displayModelName strips Fireworks account model prefix', () => {
  assert.equal(displayModelName('fireworks', 'accounts/fireworks/models/kimi-k2p6', ''), 'kimi-k2p6')
})

test('displayModelName preserves hyphens after Fireworks prefix stripping', () => {
  assert.notEqual(displayModelName('fireworks', 'accounts/fireworks/models/kimi-k2p6', ''), 'kimik2p6')
})

test('displayModelName leaves non-Fireworks model ids unchanged', () => {
  assert.equal(displayModelName('codex', 'gpt-5.4', ''), 'gpt-5.4')
})
