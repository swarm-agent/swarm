import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup combines default session mode with model policy without summary cards', () => {
  assert.doesNotMatch(source, /function SummaryCard|<SummaryCard/)
  assert.match(source, /Default session mode[\s\S]*aria-label="Default session mode"[\s\S]*label="Plan"[\s\S]*label="Auto"/)
  assert.match(source, /Agent model policy[\s\S]*label="Single"[\s\S]*label="Split"/)
  assert.match(source, /defaultSessionMode: draftSessionMode/)
})
