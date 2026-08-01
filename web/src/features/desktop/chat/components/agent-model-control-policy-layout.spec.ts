import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup uses the canonical flat favorite editor only', () => {
  assert.match(source, /aria-label="Saved model profiles"/)
  assert.match(source, /savedFavoriteModelLabel\(profile\)/)
  assert.match(source, /title=\{draftProfile.*'Favorite model'\}/)
  assert.match(source, /label="Provider"[\s\S]*label="Model"[\s\S]*label="Thinking"[\s\S]*label="Service tier"/)
  assert.doesNotMatch(source, /profile\.modelMode|profile\.single|profile\.plan|profile\.auto|Agent model policy|Split policy/)
})
