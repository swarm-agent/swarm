import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent model control no longer emits removed split agent fields', () => {
  assert.doesNotMatch(source, /AgentModelControlAction|DraftMode|planProvider|planModel|autoProvider|autoModel|modelMode/)
})

test('compiled system model settings still hydrate and persist service tier on their dedicated settings path', () => {
  assert.match(source, /function modelDraftForProfile[\s\S]*compactSettings\.service_tier[\s\S]*coderSettings\.service_tier[\s\S]*designerSettings\.service_tier[\s\S]*routerSettings\.service_tier[\s\S]*finderSettings\.service_tier/)
  assert.match(source, /service_tier: singleDraft\.serviceTier\.trim\(\)/)
  assert.match(source, /showServiceTier/)
})
