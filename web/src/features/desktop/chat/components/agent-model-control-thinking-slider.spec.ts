import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent model editor keeps provider, model, thinking, and service tier on one row', () => {
  assert.match(source, /md:grid-cols-\[minmax\(130px,0\.7fr\)_minmax\(220px,1\.4fr\)_minmax\(130px,0\.7fr\)_minmax\(130px,0\.7fr\)\][\s\S]*label="Provider"[\s\S]*label="Model"[\s\S]*label="Thinking"[\s\S]*label="Service tier"/)
})

test('thinking dropdown exposes only selected model thinking options', () => {
  assert.match(source, /const thinkingOptions = thinkingOptionsForOption\(selectedOption\)/)
  assert.match(source, /<SelectField label="Thinking" value=\{normalizedThinking\} onChange=\{onThinkingChange\} options=\{thinkingOptions\.map\(\(option\) => \(\{ label: option, value: option \}\)\)\}/)
  assert.match(source, /thinking: normalizeDraftThinking\(current\.provider, model, modelOptions, current\.thinking\)/)
  assert.doesNotMatch(source, /ThinkingSlider|role="group" aria-label="Thinking"|type="range"/)
})

test('split plan and action model editors are stacked in plan-first order', () => {
  assert.match(source, /<div className="mt-4 grid gap-3">[\s\S]*title="Plan model"[\s\S]*title="Action model"/)
  assert.doesNotMatch(source, /mt-4 grid gap-3 lg:grid-cols-2/)
})
