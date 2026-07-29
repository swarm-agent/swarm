import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup keeps default session mode and visible model policy choices without summary cards', () => {
  assert.doesNotMatch(source, /function SummaryCard|<SummaryCard/)
  assert.match(source, /Default session mode[\s\S]*aria-label="Default session mode"[\s\S]*label="Plan"[\s\S]*label="Action"/)
  assert.match(source, /Agent model policy[\s\S]*ModelPolicyChoices/)
  assert.match(source, /function PrimaryAgentControlRow[\s\S]*Default session mode<\/div>[\s\S]*<SessionModeChoices[\s\S]*How new sessions start<\/div>[\s\S]*Agent model policy<\/div>[\s\S]*<ModelPolicyChoices[\s\S]*One model or split by mode<\/div>/)
  assert.doesNotMatch(source, /sm:grid-cols-\[minmax\(0,1fr\)_176px\]/)
  assert.match(source, /function ModelPolicyChoices[\s\S]*label="Single"[\s\S]*splitModeAllowed \? <CompactChoice[\s\S]*label="Split"/)
  assert.doesNotMatch(source, /label="Split"[\s\S]*disabled=\{!splitModeAllowed\}/)
  assert.doesNotMatch(source, /function ModelPolicyButton/)
  assert.doesNotMatch(source, /agentModeLabel\(profile\)/)
  assert.match(source, /aria-label="Agent setup sections"[\s\S]*grid-cols-\[240px_280px_minmax\(0,1fr\)\]/)
  assert.match(source, /aria-label="Agents"[\s\S]*aria-label="Saved model profiles"[\s\S]*aria-label="Selected profile settings"/)
  assert.match(source, /aria-label="Plan and action model cards"[\s\S]*title="Plan agent model"[\s\S]*title="Action agent model"/)
  assert.match(source, /min-\[1100px\]:grid-cols-2/)
  assert.match(source, /defaultSessionMode: draftSessionMode/)
})
