import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup keeps direct assignments behind the favorites launcher', () => {
  assert.match(source, /screen === 'favorites'/)
  assert.match(source, /role=\{screen === 'favorites' \? 'menu' : 'dialog'\}/)
  assert.match(source, /data-model-favorites-anchor/)
  assert.match(source, /favoritesPosition\.bottom/)
  assert.match(source, /favoritesPosition\.top/)
  assert.match(source, /overflow-x-hidden overflow-y-auto overscroll-contain/)
  assert.match(source, /Thinking \{profile\.thinking \|\| 'off'\} · Priority/)
  assert.match(source, /No favorites yet/)
  assert.match(source, /> Agent Setup/)
  assert.match(source, /Switch the current chat and the canonical Default Model/)
  assert.match(source, /title="Default Model"/)
  assert.match(source, /title="Plan Model"/)
  assert.match(source, /Configure this system agent’s model directly/)
  assert.match(source, /label="Provider"[\s\S]*label="Model"[\s\S]*label="Thinking"[\s\S]*label="Service tier"/)
  assert.doesNotMatch(source, /Make account default|Continue for this chat only|Save as new/)
})
