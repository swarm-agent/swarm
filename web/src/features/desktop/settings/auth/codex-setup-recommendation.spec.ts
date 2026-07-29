import assert from 'node:assert/strict'
import test from 'node:test'
import { codexSetupRecommendation } from './codex-setup-recommendation'

test('Codex local setup remains recommended on loopback desktop access', () => {
  assert.equal(codexSetupRecommendation('localhost'), 'browser')
  assert.equal(codexSetupRecommendation('127.0.0.1'), 'browser')
  assert.equal(codexSetupRecommendation('::1'), 'browser')
})

test('Codex device code is recommended on remote desktop access', () => {
  assert.equal(codexSetupRecommendation('testbench.example.ts.net'), 'device')
  assert.equal(codexSetupRecommendation('192.168.1.20'), 'device')
})
