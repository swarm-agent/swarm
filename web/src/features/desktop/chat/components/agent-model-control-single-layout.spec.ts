import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('single-model agent setup uses compact hierarchy and capability-aware thinking slider', () => {
  assert.match(source, /SingleModelSummary[\s\S]*ActiveAgentCard/)
  assert.match(source, /lg:grid-cols-\[minmax\(140px,0\.75fr\)_minmax\(220px,1\.5fr\)_minmax\(150px,0\.75fr\)\][\s\S]*label="Provider"[\s\S]*label="Model"[\s\S]*label="Service tier"/)
  assert.match(source, /selectedOption\.thinkingOptions\.length > 0 && thinkingOptions\.length > 1/)
  assert.match(source, /<ThinkingLevelSlider options=\{thinkingOptions\} value=\{normalizedThinking\}/)
  assert.match(source, /type="range"[\s\S]*max=\{options\.length - 1\}[\s\S]*aria-valuetext=\{options\[selectedIndex\]\}/)
  assert.match(source, /thinking: defaultThinkingForOption\(option\)/)
  assert.doesNotMatch(source, /function ModelInfoPanel/)
})

test('thinking response visibility lives beside default session mode', () => {
  assert.match(source, /Default session mode[\s\S]*Thinking responses[\s\S]*Shows thinking responses[\s\S]*role="switch"/)
  assert.match(source, /onThinkingTagsToggle\(!thinkingTagsEnabled\)/)
})
