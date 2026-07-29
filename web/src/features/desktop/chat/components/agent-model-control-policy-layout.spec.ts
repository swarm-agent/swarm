import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup keeps default session mode and visible model policy choices without summary cards', () => {
  assert.doesNotMatch(source, /function SummaryCard|<SummaryCard/)
  assert.match(source, /Default session mode[\s\S]*aria-label="Default session mode"[\s\S]*label="Plan"[\s\S]*label="Action"/)
  assert.match(source, /Agent model policy[\s\S]*ModelPolicyChoices/)
  assert.match(source, /function ModelPolicyChoices[\s\S]*label="Single"[\s\S]*label="Split"/)
  assert.doesNotMatch(source, /function ModelPolicyButton/)
  assert.match(source, /defaultSessionMode: draftSessionMode/)
})
